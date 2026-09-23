import Foundation
import Observation

/// The New orchestrator window (§16.3). Starting creates a spike through `POST /api/spikes`.
@MainActor
@Observable
public final class NewOrchestratorForm {
    public var name = ""
    public var intent: SpikeIntent = .chore
    public var query = ""
    public var repos = ReposResponse()
    public var selection: [String] = []
    public private(set) var choice: AgentChoice
    public private(set) var advisor: AdvisorChoice
    public var request = ""
    public private(set) var catalog: [AgentCatalogEntry] = []
    public private(set) var agentChangeErrors = FieldErrors()
    public private(set) var effortNote: String?
    public private(set) var repoError: String?
    public private(set) var submitting = false
    /// "Couldn't start orchestrator. Your entries are saved." plus the daemon's reason.
    public private(set) var failure: String?

    public let settings: Settings
    public var connected: Bool
    private let client: DaemonClient
    private let takenNames: Set<String>
    private let queued: Bool
    private let format: Format
    private var requestID = UUID().uuidString
    /// The last body sent. A retry with the same entries reuses its `request_id` (I15 replay);
    /// any edit gets a new one, so the daemon never replays a stale result.
    private var lastAttempt: CreateSpikeBody?

    public init(client: DaemonClient, settings: Settings, agents: [AgentNode], connected: Bool, format: Format = Format()) {
        self.client = client
        self.settings = settings
        self.connected = connected
        self.format = format
        takenNames = Set(AgentTree.flatten(agents).map(\.name))
        queued = Self.wouldQueue(agents, max: settings.maxConcurrentAgents)
        (choice, advisor) = CatalogRules.prefill(settings)
    }

    /// Queue when live or waiting agents of ANY role already fill the shared
    /// `max_concurrent_agents` pool (docs/specs/2026-09-24-unify-agent-limits.md):
    /// orchestrators no longer have their own separate limit, so starting one
    /// now competes for the same slots every other role does.
    public static func wouldQueue(_ agents: [AgentNode], max: Int) -> Bool {
        let live = AgentTree.flatten(agents).filter { !AgentTree.isFinished($0) }
        if live.contains(where: { $0.state == .queued }) { return true }
        let running = live.filter {
            [.spawning, .running, .waiting, .stale, .pauseRequested, .quiescing, .stopping].contains(DisplayState($0))
        }
        return running.count >= max
    }

    public func load() async {
        async let c = try? client.catalog()
        async let r = try? client.repos(query: "")
        catalog = await c ?? []
        repos = await r ?? ReposResponse()
        // The catalog wasn't loaded yet when Settings prefilled `choice`: re-check the stored effort
        // against it now, so a level the model no longer offers can't survive into the picker.
        choice.effort = CatalogRules.normalizeEffort(choice.agent, CatalogRules.resolve(CatalogRules.entry(catalog, choice.agent), choice.model),
                                                     choice.effort)
    }

    public func search() async {
        if let r = try? await client.repos(query: query) { repos = r }
    }

    // MARK: derived

    public var kebab: String? { Kebab.make(name) }

    /// "Agent name: investigate-login-crash" (empty until something is typed).
    public var preview: String { kebab.map(Copy.agentName) ?? "" }

    public var nameError: String? {
        if name.isEmpty { return nil }
        guard let k = kebab else { return Kebab.emptyNameMessage }
        return takenNames.contains(k) ? Copy.nameTaken : nil
    }

    public var intentCaption: String {
        switch intent {
        case .chore: return Copy.choreCaption
        case .feature: return Copy.featureCaption
        case .debug: return Copy.debugCaption
        }
    }

    public var errors: FieldErrors {
        var e = CatalogRules.validate(choice, advisor: advisor, catalog: catalog, enabled: settings.enabledAgents, role: .orchestrator)
        if e.model == nil { e.model = agentChangeErrors.model }
        return e
    }

    public var agentOptions: [PickerOption] { CatalogRules.agentOptions(enabled: settings.enabledAgents) }
    public var modelOptions: [PickerOption] { CatalogRules.modelOptions(CatalogRules.entry(catalog, choice.agent)) }
    /// nil hides the Effort row.
    public var effortOptions: [PickerOption]? {
        CatalogRules.effortOptions(choice.agent, CatalogRules.resolve(CatalogRules.entry(catalog, choice.agent), choice.model))
    }
    public var advisorOptions: [PickerOption] { CatalogRules.advisorOptions(catalog, enabled: settings.enabledAgents) }

    public var sections: [RepoPicker.Section] { RepoPicker.sections(repos) }
    public var selectedLine: String { RepoPicker.selectedLine(selection, known: RepoPicker.known(repos)) }
    public var scanLine: String { RepoPicker.scanLine(repos, format: format) }

    public var canStart: Bool { connected && !submitting && kebab != nil && nameError == nil && errors.isValid }

    public var startLabel: String {
        if failure != nil { return Copy.tryAgain }
        return queued ? Copy.queueOrchestrator : Copy.startOrchestrator
    }

    public var queuedCaption: String? { queued ? Copy.queuedCaption : nil }

    // MARK: edits

    public func setAgent(_ value: String) {
        guard let kind = AgentKind(rawValue: value) else { return }
        (choice, agentChangeErrors) = CatalogRules.changeAgent(choice, to: kind, catalog: catalog)
        effortNote = nil
    }

    public func setModel(_ value: String) {
        let result = CatalogRules.changeModel(choice, to: value, catalog: catalog)
        choice = result.0
        effortNote = result.note
        agentChangeErrors = FieldErrors()
    }

    public func setEffort(_ value: String) {
        choice.effort = value
        effortNote = nil
    }

    public func setAdvisor(_ encoded: String) { advisor = AdvisorChoice(encoded: encoded) }

    public func toggle(_ repo: Repo) {
        guard !repo.missing else { return }
        selection = RepoPicker.toggle(selection, repo.id)
    }

    public func selectAll(_ section: RepoPicker.Section) {
        selection = RepoPicker.selectAll(selection, section.repos)
    }

    public func addFolder(_ path: String) async {
        do {
            let repo = try await client.addRepo(path: path)
            repoError = nil
            if !selection.contains(repo.id) { selection.append(repo.id) }
            await search()
            if !RepoPicker.known(repos).contains(where: { $0.id == repo.id }) { repos.all.append(repo) }
        } catch let e as DaemonError {
            repoError = e.message
        } catch {
            repoError = DaemonError.unreachable.message
        }
    }

    public func rescan() async {
        repos.scanning = true
        _ = try? await client.rescanRepos()
        await search()
    }

    // MARK: submit

    public func body() -> CreateSpikeBody? {
        guard let agent = choice.agent else { return nil }
        let text = request.trimmingCharacters(in: .whitespacesAndNewlines)
        return CreateSpikeBody(requestId: requestID, name: name.trimmingCharacters(in: .whitespacesAndNewlines),
                               intent: intent, repos: selection, agent: agent, model: choice.model,
                               effort: choice.effort.isEmpty ? nil : choice.effort,
                               advisor: CatalogRules.advisorPayload(advisor, settings: settings, catalog: catalog),
                               request: text.isEmpty ? nil : text)
    }

    /// Returns the created agent; on failure the form keeps every entry and shows the banner.
    public func submit() async -> AgentNode? {
        guard canStart, var body = body() else { return nil }
        if let last = lastAttempt, last != body {
            requestID = UUID().uuidString
            body.requestId = requestID
        }
        lastAttempt = body
        submitting = true
        defer { submitting = false }
        do {
            let created = try await client.createSpike(body)
            failure = nil
            return created.agent
        } catch let e as DaemonError {
            if case .api = e {
                requestID = UUID().uuidString
                lastAttempt = nil
            }
            failure = e == .unreachable ? Copy.launchFailed : Copy.launchFailed + " " + e.message
            return nil
        } catch {
            failure = Copy.launchFailed
            return nil
        }
    }
}

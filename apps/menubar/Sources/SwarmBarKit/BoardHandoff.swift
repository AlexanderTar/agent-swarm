import Foundation
import Observation

/// One row of the Orchestrate task window: a board item plus its live orchestrator, if any.
public struct BoardItemRow: Equatable, Sendable, Identifiable {
    public var item: BoardItem
    public var orchestrator: AgentNode?
    public var id: String { item.key }
    public init(item: BoardItem, orchestrator: AgentNode?) { self.item = item; self.orchestrator = orchestrator }
}

public enum BoardHandoffRules {
    static let eligibleTypes: Set<String> = ["epic", "bug", "chore", "spike"]

    /// Eligible rows: live-orchestrator rows first, then Draft/Ready; daemon order (updated_at DESC) within each.
    public static func rows(_ items: [BoardItem], agents: [AgentNode]) -> [BoardItemRow] {
        let live = AgentTree.flatten(agents).filter { $0.role == .orchestrator && ($0.state == .queued || $0.state == .active) }
        var orchestrated: [BoardItemRow] = [], ready: [BoardItemRow] = []
        for it in items where it.parentKey == nil && eligibleTypes.contains(it.type) {
            if let o = live.first(where: { $0.itemKey == it.key }) {
                orchestrated.append(BoardItemRow(item: it, orchestrator: o))
            } else if it.status == "ready" || it.status == "draft" {
                ready.append(BoardItemRow(item: it, orchestrator: nil))
            }
        }
        return orchestrated + ready
    }

    public static func title(_ r: BoardItemRow) -> String { Copy.boardItemTitle(r.item.key, r.item.title) }

    /// "<name> · <model label> · <effort> · <state>" (effort omitted when unknown) or "<Type> · <Draft|Ready>".
    public static func detail(_ r: BoardItemRow, catalog: [AgentCatalogEntry]) -> String {
        guard let o = r.orchestrator else { return "\(Copy.itemType(r.item.type)) · \(r.item.status == "draft" ? Copy.draft : Copy.ready)" }
        let entry = CatalogRules.entry(catalog, o.kind)
        let state = AgentTree.handoffStatus(o) ?? DisplayState(o).label ?? Copy.runningLabel
        return [o.name, CatalogRules.modelLabel(entry, o.model), CatalogRules.previewEffortLabel(entry, o.model, o.effort), state]
            .compactMap { $0 }.joined(separator: " · ")
    }

    /// An in-flight replacement blocks it too: the daemon would answer 409 "A replacement is already in progress".
    public static func canHandOff(_ a: AgentNode, connected: Bool) -> Bool {
        a.replacement == nil && AgentTree.actions(a, tmuxAlive: true, connected: connected).contains { $0.endpoint == .handoff && !$0.disabled }
    }

    /// Row menu "Hand off to…" gate: top-level orchestrator whose actions include an enabled handoff.
    public static func offersHandOffTo(_ a: AgentNode, actions: [AgentAction]) -> Bool {
        a.role == .orchestrator && a.parentName == nil && actions.contains { $0.endpoint == .handoff && !$0.disabled }
    }
}

/// The Orchestrate task window: start an orchestrator on a Draft/Ready item, or hand a live one
/// off to a different agent/model/effort/advisor.
@MainActor
@Observable
public final class BoardHandoffForm {
    /// Worker roles the Orchestrate task window overrides when starting a new orchestrator.
    /// Never orchestrator/advisor: the daemon refuses advisor overrides and the primary
    /// pickers already cover the orchestrator itself.
    public static let workerRoles: [SettingsRole] = [.coder, .reviewer, .uiReviewer, .researcher, .debugger, .mechanical, .designer]

    public let picker: AgentPickerModel
    public private(set) var rows: [BoardItemRow] = []
    public var selectedKey: String? {
        didSet {
            // Worker edits are measured against the selected orchestrator's baseline, so
            // moving to a different one (or between start and hand-off) drops them.
            let old = rows.first { $0.id == oldValue }?.orchestrator?.name
            if old != selected?.orchestrator?.name { workers = [:]; workerNotes = [:]; workerModelErrors = [:] }
        }
    }
    public private(set) var loading = true
    public private(set) var loadError: String?
    public private(set) var failure: String?
    public private(set) var submitting = false
    public var connected: Bool

    private let client: DaemonClient
    private var agents: [AgentNode]
    private var items: [BoardItem] = []
    private var preselectAgent: String?
    private var requestID = UUID().uuidString
    /// Same entries on retry reuse `requestID`; any edit or `.api` error mints a new one.
    private var lastAttempt: Attempt?
    /// Worker Agent/Model/Effort edits; unedited roles show `workerBaseline`.
    private var workers: [SettingsRole: AgentChoice] = [:]
    private var workerNotes: [SettingsRole: String] = [:]
    private var workerModelErrors: [SettingsRole: String] = [:]

    private struct Attempt: Equatable {
        var key: String, agent: AgentKind, model: String, effort: String?, advisor: AdvisorPayload, roles: [String: RoleDefault]?
    }

    public init(client: DaemonClient, settings: Settings, agents: [AgentNode], connected: Bool, preselectAgent: String?) {
        self.client = client
        self.agents = agents
        self.connected = connected
        self.preselectAgent = preselectAgent
        picker = AgentPickerModel(settings: settings)
    }

    public func load() async {
        loading = true
        loadError = nil
        async let c = try? client.catalog()
        async let i = client.boardItems()
        picker.apply(catalog: await c ?? [])
        normalizeWorkerEfforts()
        do { items = try await i } catch { items = []; loadError = Copy.boardItemsLoadFailed }
        rows = BoardHandoffRules.rows(items, agents: agents)
        if let p = preselectAgent, let row = rows.first(where: { $0.orchestrator?.name == p }) { selectedKey = row.id }
        preselectAgent = nil
        if selected == nil { selectedKey = rows.first?.id }
        loading = false
    }

    /// Re-join on every state change; keeps the selection while it is still eligible.
    public func update(agents: [AgentNode]) {
        self.agents = agents
        rows = BoardHandoffRules.rows(items, agents: agents)
        if selected == nil { selectedKey = rows.first?.id }
    }

    public var selected: BoardItemRow? { rows.first { $0.id == selectedKey } }
    public var isHandoff: Bool { selected?.orchestrator != nil }
    private var queued: Bool { NewOrchestratorForm.wouldQueue(agents, max: picker.settings.maxConcurrentAgents) }
    private var handoffPossible: Bool { selected?.orchestrator.map { BoardHandoffRules.canHandOff($0, connected: connected) } ?? false }

    public var primaryLabel: String {
        if failure != nil || loadError != nil { return Copy.tryAgain }
        if isHandoff { return Copy.handOff }
        return queued ? Copy.queueOrchestrator : Copy.startOrchestrator
    }

    public var caption: String? {
        guard let row = selected else { return nil }
        if row.orchestrator != nil { return handoffPossible ? nil : Copy.handoffUnavailable }
        return queued ? Copy.queuedCaption : nil
    }

    public var canSubmit: Bool {
        if loadError != nil { return connected && !loading }
        return connected && !submitting && !loading && selected != nil && picker.isValid
            && workersValid && (!isHandoff || handoffPossible)
    }

    // MARK: worker overrides

    /// What a role starts from: the selected orchestrator's stored override in handoff mode, else Settings.
    private func workerBaseline(_ role: SettingsRole) -> RoleDefault? {
        selected?.orchestrator?.roleOverrides?[role.rawValue] ?? picker.settings[role]
    }

    public func workerChoice(_ role: SettingsRole) -> AgentChoice {
        if let w = workers[role] { return w }
        guard let d = workerBaseline(role) else { return AgentChoice(agent: picker.settings.enabledAgents.first, model: "") }
        return AgentChoice(agent: d.agent, model: d.model, effort: CatalogRules.normalizeEffort(d.agent,
            CatalogRules.resolve(CatalogRules.entry(picker.catalog, d.agent), d.model), d.effort))
    }

    public var workerAgentOptions: [PickerOption] { picker.agentOptions }

    public func workerModelOptions(_ role: SettingsRole) -> [PickerOption] {
        CatalogRules.modelOptions(CatalogRules.entry(picker.catalog, workerChoice(role).agent))
    }

    /// nil hides the Effort picker.
    public func workerEffortOptions(_ role: SettingsRole) -> [PickerOption]? {
        let w = workerChoice(role)
        return CatalogRules.effortOptions(w.agent, CatalogRules.resolve(CatalogRules.entry(picker.catalog, w.agent), w.model))
    }

    /// Empty until the catalog arrives, like `AgentPickerModel.errors`.
    public func workerErrors(_ role: SettingsRole) -> FieldErrors {
        guard picker.catalogLoaded else { return FieldErrors() }
        var e = CatalogRules.validate(workerChoice(role), advisor: .none, catalog: picker.catalog,
                                      enabled: picker.settings.enabledAgents, role: role)
        if e.model == nil { e.model = workerModelErrors[role] }
        return e
    }

    public func workerNote(_ role: SettingsRole) -> String? { workerNotes[role] }

    public var workersValid: Bool { Self.workerRoles.allSatisfy { workerErrors($0).isValid } }

    public func setWorkerAgent(_ role: SettingsRole, _ value: String) {
        guard let kind = AgentKind(rawValue: value) else { return }
        let (next, errors) = CatalogRules.changeAgent(workerChoice(role), to: kind, catalog: picker.catalog)
        workers[role] = next
        workerModelErrors[role] = errors.model
        workerNotes[role] = nil
    }

    public func setWorkerModel(_ role: SettingsRole, _ value: String) {
        let result = CatalogRules.changeModel(workerChoice(role), to: value, catalog: picker.catalog)
        workers[role] = result.0
        workerNotes[role] = result.note
        workerModelErrors[role] = nil
    }

    public func setWorkerEffort(_ role: SettingsRole, _ value: String) {
        let w = workerChoice(role)
        workers[role] = AgentChoice(agent: w.agent, model: w.model, effort: CatalogRules.normalizeEffort(w.agent,
            CatalogRules.resolve(CatalogRules.entry(picker.catalog, w.agent), w.model), value))
        workerNotes[role] = nil
    }

    /// Re-check stored worker efforts against the freshly loaded catalog, like `AgentPickerModel.apply`.
    private func normalizeWorkerEfforts() {
        for role in Self.workerRoles {
            guard let w = workers[role] else { continue }
            workers[role] = AgentChoice(agent: w.agent, model: w.model, effort: CatalogRules.normalizeEffort(w.agent,
                CatalogRules.resolve(CatalogRules.entry(picker.catalog, w.agent), w.model), w.effort))
        }
    }

    /// Only roles the user changed from their baseline (Settings, or the orchestrator's current
    /// override in handoff mode), so unchanged defaults keep following later Settings changes.
    /// nil when nothing changed.
    public var workerRolesPayload: [String: RoleDefault]? {
        var out: [String: RoleDefault] = [:]
        for role in Self.workerRoles {
            guard let w = workers[role], let agent = w.agent, let d = workerBaseline(role) else { continue }
            let effort = CatalogRules.normalizeEffort(agent,
                CatalogRules.resolve(CatalogRules.entry(picker.catalog, agent), w.model), w.effort)
            if agent != d.agent || w.model != d.model || effort != d.effort {
                out[role.rawValue] = RoleDefault(agent: agent, model: w.model, effort: effort)
            }
        }
        return out.isEmpty ? nil : out
    }

    /// Load error → reload; otherwise submit. true = close the window.
    public func primary() async -> Bool {
        if loadError != nil { await load(); return false }
        guard canSubmit, let row = selected, let agent = picker.choice.agent else { return false }
        let roles = workerRolesPayload
        let attempt = Attempt(key: row.id, agent: agent, model: picker.choice.model, effort: picker.effortPayload,
                              advisor: picker.advisorPayload, roles: roles)
        if let last = lastAttempt, last != attempt { requestID = UUID().uuidString }
        lastAttempt = attempt
        submitting = true
        defer { submitting = false }
        let base = row.orchestrator != nil ? Copy.handoffFailed : Copy.launchFailed
        do {
            if let orch = row.orchestrator {
                try await client.handoff(orch.name, HandoffRequest(requestId: requestID, agent: agent, model: attempt.model,
                                                                   effort: attempt.effort, advisor: attempt.advisor, roles: roles))
            } else {
                _ = try await client.startOrchestrator(itemKey: row.id, StartOrchestratorBody(requestId: requestID, agent: agent,
                    model: attempt.model, effort: attempt.effort, advisor: attempt.advisor, roles: roles))
            }
            failure = nil
            return true
        } catch let e as DaemonError {
            if case .api = e { requestID = UUID().uuidString; lastAttempt = nil }
            failure = e == .unreachable ? base : base + " " + e.message
            return false
        } catch {
            failure = base
            return false
        }
    }
}

import AppKit
import Foundation
import Observation

/// One image attached to the request. `data` is what gets uploaded: original bytes for a web
/// format, PNG bytes for anything `addImage` converted. The thumbnail is derived in the view
/// (`NSImage(data:)`), not stored here, so this stays `Equatable`.
public struct RequestImage: Identifiable, Equatable {
    public let id: UUID
    public var name: String
    public var data: Data
}

/// The New orchestrator window (§16.3). Starting creates a spike through `POST /api/spikes`.
@MainActor
@Observable
public final class NewOrchestratorForm {
    public var name = "" {
        didSet {
            // spec 2026-09-28: "typed in and then cleared" gates the
            // nameOrRequestRequired error -- never having touched Name at
            // all shows nothing instead, even though both fields are empty.
            if !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { nameTouched = true }
        }
    }
    private var nameTouched = false
    public var intent: SpikeIntent = .chore
    public var repos = ReposResponse()
    public var selection: [String] = []
    public private(set) var selectionNotice: String?
    public var request = ""
    public private(set) var images: [RequestImage] = []
    public private(set) var imageError: String?
    /// Set once `submit()` returns with `attachments_failed: true`; the view replaces Start with Done.
    public private(set) var startedWithUnsavedImages: AgentNode?
    public private(set) var repoError: String?
    public private(set) var submitting = false
    /// "Couldn't start orchestrator. Your entries are saved." plus the daemon's reason.
    public private(set) var failure: String?

    public let settings: Settings
    public let picker: AgentPickerModel
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
        picker = AgentPickerModel(settings: settings)
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
        async let r = client.repos(query: "")
        picker.apply(catalog: await c ?? [])
        do {
            applyRepos(try await r)
            repoError = nil
        } catch {
            repoError = Self.repoFailure(error)
        }
    }

    public func search() async {
        do {
            applyRepos(try await client.repos(query: ""))
            repoError = nil
        } catch {
            repoError = Self.repoFailure(error)
        }
    }

    // MARK: derived

    public var kebab: String? { Kebab.make(name) }

    /// "Agent name: investigate-login-crash" (empty until something is typed).
    public var preview: String { kebab.map(Copy.agentName) ?? "" }

    private var trimmedName: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var trimmedRequest: String { request.trimmingCharacters(in: .whitespacesAndNewlines) }

    public var nameError: String? {
        if trimmedName.isEmpty {
            if trimmedRequest.isEmpty && nameTouched { return Copy.nameOrRequestRequired }
            return nil
        }
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


    public var rows: [Repo] { RepoPicker.rows(repos) }
    public var scanLine: String { RepoPicker.scanLine(repos, format: format) }

    public var canStart: Bool {
        connected && !submitting && nameError == nil && picker.isValid && (!trimmedName.isEmpty || !trimmedRequest.isEmpty)
    }

    public var startLabel: String {
        if failure != nil { return Copy.tryAgain }
        return queued ? Copy.queueOrchestrator : Copy.startOrchestrator
    }

    public var queuedCaption: String? { queued ? Copy.queuedCaption : nil }

    // MARK: edits


    public func toggle(_ repo: Repo) {
        guard rows.contains(where: { $0.id == repo.id }) else { return }
        selection = RepoPicker.toggle(selection, repo.id)
    }

    public func addFolder(_ path: String) async {
        do {
            let repo = try await client.addRepo(path: path)
            applyRepos(try await client.repos(query: ""))
            let path = (repo.path as NSString).standardizingPath
            guard let verified = rows.first(where: { ($0.path as NSString).standardizingPath == path }) else {
                repoError = "Folder was added, but isn't available in the repository list. Rescan and try again."
                return
            }
            repoError = nil
            if !selection.contains(verified.id) { selection.append(verified.id) }
            selectionNotice = nil
        } catch {
            repoError = Self.repoFailure(error)
        }
    }

    public func rescan() async {
        repos.scanning = true
        do {
            _ = try await client.rescanRepos()
            applyRepos(try await client.repos(query: ""))
            repoError = nil
        } catch {
            repos.scanning = false
            repoError = Self.repoFailure(error)
        }
    }

    private static func repoFailure(_ error: Error) -> String {
        (error as? DaemonError)?.message ?? DaemonError.unreachable.message
    }

    private func applyRepos(_ response: ReposResponse) {
        repos = response
        reconcileSelection()
    }

    private func reconcileSelection() {
        let ids = Set(rows.map(\.id))
        let removed = selection.count - selection.filter(ids.contains).count
        selection.removeAll { !ids.contains($0) }
        selectionNotice = removed == 0 ? nil : "\(removed) selected \(removed == 1 ? "repository is" : "repositories are") no longer available."
    }

    // MARK: images

    private static let maxImageCount = 10
    private static let maxImageBytes = 10 << 20

    /// Reads each URL's data and adds it, using the last path component as the name.
    public func addImages(from urls: [URL]) {
        for url in urls {
            guard let data = try? Data(contentsOf: url) else { continue }
            addImage(data: data, name: url.lastPathComponent)
        }
    }

    /// PNG, JPEG, GIF and WebP bytes are kept as-is; anything else `NSImage` can read is converted
    /// to PNG. An 11th image, an unreadable file, or one over 10 MB sets `imageError` instead.
    public func addImage(data: Data, name: String) {
        guard images.count < Self.maxImageCount else {
            imageError = Copy.imageTooMany
            return
        }
        guard let uploadable = Self.uploadable(data) else {
            imageError = Copy.imageUnsupported(name)
            return
        }
        guard uploadable.count <= Self.maxImageBytes else {
            imageError = Copy.imageTooLarge(name)
            return
        }
        images.append(RequestImage(id: UUID(), name: name, data: uploadable))
        imageError = nil
    }

    public func removeImage(_ id: UUID) {
        images.removeAll { $0.id == id }
        imageError = nil
    }

    private static let pngMagic: [UInt8] = [0x89, 0x50, 0x4E, 0x47]
    private static let jpegMagic: [UInt8] = [0xFF, 0xD8, 0xFF]
    private static let gifMagic: [UInt8] = [0x47, 0x49, 0x46, 0x38]

    /// Web formats pass through unchanged (sniffed by magic bytes); anything else `NSImage` can
    /// read is converted to PNG. `nil` when the bytes aren't a readable image at all.
    static func uploadable(_ data: Data) -> Data? {
        if data.starts(with: pngMagic) || data.starts(with: jpegMagic) || data.starts(with: gifMagic) { return data }
        if data.count >= 12, data.prefix(4).elementsEqual([0x52, 0x49, 0x46, 0x46]),
           data[data.index(data.startIndex, offsetBy: 8)..<data.index(data.startIndex, offsetBy: 12)]
               .elementsEqual([0x57, 0x45, 0x42, 0x50]) { return data }
        return NSBitmapImageRep(data: data)?.representation(using: .png, properties: [:])
    }

    // MARK: submit

    public func body() -> CreateSpikeBody? {
        guard let agent = picker.choice.agent else { return nil }
        let text = request.trimmingCharacters(in: .whitespacesAndNewlines)
        let verifiedIDs = Set(rows.map(\.id))
        return CreateSpikeBody(requestId: requestID, name: name.trimmingCharacters(in: .whitespacesAndNewlines),
                               intent: intent, repos: selection.filter(verifiedIDs.contains), agent: agent, model: picker.choice.model,
                               effort: picker.effortPayload,
                               advisor: picker.advisorPayload,
                               request: text.isEmpty ? nil : text,
                               attachments: images.isEmpty ? nil : images.map { AttachmentPayload(name: $0.name, data: $0.data.base64EncodedString()) })
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
            startedWithUnsavedImages = created.attachmentsFailed == true ? created.agent : nil
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

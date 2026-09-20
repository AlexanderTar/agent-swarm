import Foundation
import Observation

/// Last known `/api/state`, kept on disk so the popover has something to show while the daemon is down.
public struct StateCache: Sendable {
    public var url: URL

    public init(url: URL) { self.url = url }

    struct Stored: Codable {
        var syncedAt: Timestamp
        var state: StateResponse
    }

    public func save(_ state: StateResponse, at date: Date) {
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? SwarmJSON.encode(Stored(syncedAt: Timestamp(date), state: state)).write(to: url, options: .atomic)
    }

    public func load() -> (StateResponse, Date)? {
        guard let data = try? Data(contentsOf: url), let s = try? SwarmJSON.decode(Stored.self, from: data) else { return nil }
        return (s.state, s.syncedAt.date)
    }
}

/// Where the popover remembers its state. `UserDefaults` in the app; `MemoryStore` in tests and mock mode.
@MainActor
public protocol KeyValueStore: AnyObject {
    func object(forKey key: String) -> Any?
    func set(_ value: Any?, forKey key: String)
}

extension UserDefaults: KeyValueStore {}

@MainActor
public final class MemoryStore: KeyValueStore {
    public private(set) var values: [String: Any] = [:]
    public init() {}
    public func object(forKey key: String) -> Any? { values[key] }
    public func set(_ value: Any?, forKey key: String) { values[key] = value }
}

/// Needs you line 2 for each request kind (§16.2, §17.5).
public enum RequestLine {
    public static func text(_ r: SwarmRequest) -> String {
        switch r.kind {
        case .question, .prompt, .blocker: return r.prompt
        case .approveSection: return Copy.approveSection(r.sectionTitle ?? r.prompt)
        case .approvePlan: return Copy.approvePlan
        case .approveReport: return Copy.approveReport
        case .acceptEpic: return Copy.acceptEpic
        case .acceptFix: return Copy.acceptFix
        case .confirmRepos: return r.proposedRepos.map(Copy.confirmRepositories) ?? Copy.confirmRepositories
        case .closeSpike: return Copy.closeSpike
        }
    }
}

/// Everything the popover and the menu bar label show, and every action they take.
@MainActor
@Observable
public final class AppModel {
    public enum Section: String, CaseIterable, Sendable {
        case needsYou, agents, usage, notifications
        var defaultOpen: Bool { self != .notifications }
    }

    public private(set) var state = StateResponse()
    public private(set) var connected = false
    public private(set) var lastSync: Date?
    public private(set) var catalog: [AgentCatalogEntry] = []
    public var usageAgent: AgentKind?
    public var answerDrafts: [String: String] = [:]
    public var answering: String?
    public private(set) var pauseAllPending = false
    public private(set) var allNotifications: [SwarmNotification]?
    public private(set) var tmuxSessions: [String: Bool] = [:]
    public private(set) var actionError: String?
    public private(set) var openSections: Set<Section>
    public private(set) var collapsedAgents: Set<String> = []
    public private(set) var openFinished: Set<String> = []
    public private(set) var openFailed: Set<String> = []
    public private(set) var compactSwitch: CompactSwitch
    private var seenRequests: Set<String>

    public let client: DaemonClient
    public let endpoint: DaemonEndpoint
    public let terminals: Terminals
    private let defaults: KeyValueStore
    private let cache: StateCache
    private let now: @MainActor () -> Date
    private let timeZone: TimeZone
    private let openURL: @MainActor (URL) -> Void
    private var notifier: Notifier!
    private var stream: EventStream!
    /// The Agents row hover preview (agent-hover-preview spec). Public: the floating panel
    /// window lives in the SwarmBar target and observes this directly.
    public private(set) var preview: PanePreviewModel!

    public init(client: DaemonClient, endpoint: DaemonEndpoint, terminals: Terminals, poster: NotificationPosting,
                defaults: KeyValueStore, cache: StateCache, connect: @escaping EventStream.Connect,
                sleep: @escaping EventStream.Sleep = { try await Task.sleep(for: $0) },
                now: @escaping @MainActor () -> Date = { Date() }, timeZone: TimeZone = .current,
                openURL: @escaping @MainActor (URL) -> Void) {
        self.client = client
        self.endpoint = endpoint
        self.terminals = terminals
        self.defaults = defaults
        self.cache = cache
        self.now = now
        self.timeZone = timeZone
        self.openURL = openURL
        openSections = Set(Section.allCases.filter { defaults.object(forKey: "section." + $0.rawValue) as? Bool ?? $0.defaultOpen })
        seenRequests = Set(defaults.object(forKey: "seenRequests") as? [String] ?? [])
        compactSwitch = (defaults.object(forKey: "compactSwitch") as? Data)
            .flatMap { try? JSONDecoder().decode(CompactSwitch.self, from: $0) } ?? CompactSwitch()
        if let (cached, at) = cache.load() {
            state = cached
            lastSync = at
        }
        notifier = Notifier(poster: poster, client: client,
                            openBoard: { [weak self] in self?.openBoard($0) },
                            openTerminal: { [weak self] in await self?.openTerminal($0) })
        stream = EventStream(endpoint: endpoint, connect: connect, sleep: sleep,
                             onEvent: { [weak self] e in Task { await self?.handle(e) } },
                             onConnected: { [weak self] up in
                                 Task { up ? await self?.refresh() : await self?.markDisconnected() }
                             })
        preview = PanePreviewModel(client: client, connected: { [weak self] in self?.connected ?? false }, sleep: sleep)
    }

    public var format: Format { Format(now: now(), timeZone: timeZone) }

    public func start() async {
        await notifier.start()
        await refresh()
        stream.start()
    }

    public func stop() { stream.stop() }

    /// "Retry connection".
    public func retryConnection() async {
        stream.restart()
        await refresh()
    }

    public func refresh() async {
        do {
            apply(try await client.state())
            if let c = try? await client.catalog() { catalog = c }
            await refreshExpandedNotifications()
        } catch is CancellationError {
            return
        } catch DaemonError.timedOut {
            return // slow, not down: keep the current state
        } catch {
            await markDisconnected()
        }
    }

    /// The "View all" list (`allNotifications`) is a separate fetch from `/api/state`'s trimmed
    /// `notifications.items`, so a plain refresh must keep it current too, or it freezes until "Read
    /// all" clears it. Dropped once the Notifications section collapses, so reopening it never shows a
    /// stale snapshot instead of a fresh one.
    private func refreshExpandedNotifications() async {
        guard isOpen(.notifications) else {
            allNotifications = nil
            return
        }
        guard allNotifications != nil else { return }
        if let all = try? await client.notifications(limit: 50) { allNotifications = all }
    }

    func markDisconnected() async {
        connected = false
        var alive: [String: Bool] = [:]
        for a in AgentTree.flatten(state.agents) where a.session != nil {
            alive[a.name] = await terminals.hasSession(a.name)
        }
        tmuxSessions = alive
    }

    private func apply(_ s: StateResponse) {
        state = s
        connected = true
        let at = now()
        lastSync = at
        cache.save(s, at: at)
        let open = Set(s.requests.filter { $0.state == "open" }.map(\.id))
        if !open.subtracting(seenRequests).isEmpty { setSection(.needsYou, open: true) }
        seenRequests = open
        defaults.set(Array(open).sorted(), forKey: "seenRequests")
        if pauseAllPending && !AgentTree.flatten(s.agents).contains(where: { DisplayState($0).isPausing }) {
            pauseAllPending = false
        }
        usageAgent = UsageSection.selection(current: usageAgent, enabled: s.settings.enabledAgents)
    }

    /// Payloads are hints: every state event refetches (contracts §5).
    public func handle(_ event: SwarmEvent) async {
        switch event {
        case let .notification(n):
            await notifier.deliver(n, settings: state.settings)
            await refresh()
        case let .terminalOpen(t):
            _ = await terminals.open(t.name)
            try? await client.terminalOpened(name: t.name)
        case .reset, .changed, .requestOpened, .usage, .settings, .catalog:
            await refresh()
        }
    }

    public func handleNotificationAction(_ action: String, userInfo: [String: String], text: String?) async {
        // A failed answer's own notification comes back with kind "answer.failed": before whatever this
        // action does next reopens the popover, the typed text must already be sitting in the draft, or
        // a retry from Notification Center loses it a second time.
        if userInfo["kind"] == "answer.failed", let req = userInfo["request"] {
            answerDrafts[req] = userInfo["text"]
        }
        await notifier.handle(action: action, userInfo: userInfo, text: text)
    }

    // MARK: label and header

    public var compact: Bool { compactSwitch.effective(setting: state.settings.menubarCompact) }

    public var label: MenuLabel {
        MenuLabel.make(activeCount: state.activeCount, connected: connected, enabled: state.settings.enabledAgents,
                       usage: state.usage, compact: compact, format: format)
    }

    /// "6 active", or "? active" while the daemon is down.
    public var activeLine: String { connected ? Copy.active(state.activeCount) : Copy.activeUnknown }

    /// "Daemon unavailable. Showing last known state from 14:32."
    public var banner: String? {
        guard !connected else { return nil }
        return lastSync.map { Copy.daemonDown(format.clock($0)) } ?? DaemonError.unreachable.message
    }

    public func labelVisible(_ visible: Bool) {
        compactSwitch.labelVisible(visible, setting: state.settings.menubarCompact)
        persistCompact()
    }

    public func userSetCompact(_ on: Bool) {
        compactSwitch.userSet(on)
        persistCompact()
    }

    /// Shown once in the popover after an automatic switch.
    public var compactNote: String? { compactSwitch.notePending ? Copy.compactFallback : nil }

    public func popoverShown() {
        guard compactSwitch.notePending else { return }
        compactSwitch.noteShown()
        persistCompact()
    }

    private func persistCompact() {
        defaults.set(try? JSONEncoder().encode(compactSwitch), forKey: "compactSwitch")
    }

    // MARK: sections

    public func isOpen(_ s: Section) -> Bool { openSections.contains(s) }

    public func setSection(_ s: Section, open: Bool) {
        if open { openSections.insert(s) } else { openSections.remove(s) }
        defaults.set(open, forKey: "section." + s.rawValue)
    }

    public func toggleAgent(_ name: String) {
        if collapsedAgents.contains(name) { collapsedAgents.remove(name) } else { collapsedAgents.insert(name) }
    }

    public func toggleFinished(_ parent: String) {
        if openFinished.contains(parent) { openFinished.remove(parent) } else { openFinished.insert(parent) }
    }

    public func toggleFailed(_ parent: String) {
        if openFailed.contains(parent) { openFailed.remove(parent) } else { openFailed.insert(parent) }
    }

    // MARK: Needs you

    public var openRequests: [SwarmRequest] {
        state.requests.filter { $0.state == "open" && $0.isHITL }.sorted { $0.createdAt < $1.createdAt }
    }

    public var visibleRequests: [SwarmRequest] { Array(openRequests.prefix(3)) }

    public var viewAllRequests: String? {
        openRequests.count > 3 ? Copy.viewAllRequests(openRequests.count) : nil
    }

    public func review(_ r: SwarmRequest) { openBoard(BoardLink.request(r.id)) }

    public func openInbox() { openBoard(BoardLink.inbox) }

    public func sendAnswer(_ requestID: String) async {
        let text = (answerDrafts[requestID] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard connected, !text.isEmpty else { return }
        do {
            try await client.answer(requestID: requestID, text: text)
            answerDrafts[requestID] = nil
            answering = nil
            actionError = nil
        } catch let e as DaemonError {
            actionError = e.message
        } catch {}
        await refresh()
    }

    // MARK: agents

    public var agentRows: [AgentTree.Row] {
        AgentTree.rows(state.agents, collapsed: collapsedAgents, openFinished: openFinished, openFailed: openFailed)
    }

    public func tmuxAlive(_ a: AgentNode) -> Bool {
        connected ? (a.session?.tmuxAlive ?? false) : (tmuxSessions[a.name] ?? false)
    }

    public func actions(_ a: AgentNode) -> [AgentAction] {
        AgentTree.actions(a, tmuxAlive: tmuxAlive(a), connected: connected)
    }

    /// The terminal button on a request row (question from a live agent).
    public func requestTerminal(_ r: SwarmRequest) -> String? {
        guard r.kind == .question, let name = r.agentName,
              let a = AgentTree.flatten(state.agents).first(where: { $0.name == name }), tmuxAlive(a) else { return nil }
        return name
    }

    public func perform(_ action: AgentAction, on agent: AgentNode) async {
        guard !action.disabled else { return }
        if action.endpoint == .terminal {
            await openTerminal(agent.name)
            return
        }
        do {
            try await client.agent(agent.name, action.endpoint, scope: action.scope)
            actionError = nil
        } catch let e as DaemonError {
            actionError = e.message
        } catch {}
        await refresh()
    }

    public func openTerminal(_ name: String) async {
        _ = await terminals.open(name)
    }

    private var anyPausable: Bool { AgentTree.flatten(state.agents).contains { DisplayState($0).isPausable && !AgentTree.isFinished($0) } }

    public var pauseAllLabel: String { pauseAllPending ? Copy.pausing : Copy.pauseAll }
    public var pauseAllDisabled: Bool { !connected || pauseAllPending || !anyPausable }

    public func pauseAll() async {
        guard !pauseAllDisabled else { return }
        pauseAllPending = true
        do {
            if try await client.pauseAll() == 0 { pauseAllPending = false }
        } catch {
            pauseAllPending = false
        }
        await refresh()
    }

    // MARK: usage

    public var usagePicker: [AgentKind] { UsageSection.pickerAgents(enabled: state.settings.enabledAgents) }

    public var usageRows: [UsageSection.Row] {
        UsageSection.rows(state.usage.first { $0.agent == usageAgent }, format: format)
    }

    public func refreshUsage() async {
        guard connected else { return }
        try? await client.refreshUsage(agent: usageAgent)
    }

    // MARK: notifications

    public var notificationsTitle: String { Copy.notifications(unread: state.notifications.unread) }

    public var visibleNotifications: [SwarmNotification] {
        allNotifications ?? Array(state.notifications.items.prefix(5))
    }

    public var showViewAllNotifications: Bool { allNotifications == nil && state.notifications.items.count > 5 }

    public func viewAllNotifications() async {
        if let all = try? await client.notifications(limit: 50) { allNotifications = all }
    }

    public func readAll() async {
        guard connected else { return }
        try? await client.readAll()
        allNotifications = nil
        await refresh()
    }

    public func open(_ n: SwarmNotification) async {
        if connected, n.readAt == nil { try? await client.markRead(notificationID: n.id) }
        if let req = n.requestId {
            openBoard(BoardLink.request(req))
        } else if let key = n.itemKey {
            openBoard(BoardLink.item(key))
        }
        await refresh()
    }

    // MARK: windows

    public func openBoard(_ fragment: String = "") { openURL(endpoint.boardURL(fragment: fragment)) }

    public func makeNewOrchestratorForm() -> NewOrchestratorForm {
        NewOrchestratorForm(client: client, settings: state.settings, agents: state.agents, connected: connected, format: format)
    }

    public func makeSettings() -> SettingsModel {
        SettingsModel(client: client, settings: state.settings, agents: state.agents, connected: connected,
                      compact: compact, format: format,
                      onCompactChange: { [weak self] in self?.userSetCompact($0) })
    }
}

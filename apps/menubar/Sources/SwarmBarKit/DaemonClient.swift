import Foundation

public enum DaemonError: Error, Equatable, Sendable {
    /// Connection refused, timed out, or no daemon token on disk.
    case unreachable
    /// §7 error body: `{"error":{"code","message"}}` with its HTTP status.
    case api(status: Int, code: String, message: String)
    case decoding(String)

    public var message: String {
        switch self {
        case .unreachable: return "Daemon unavailable."
        case let .api(_, _, message): return message
        case let .decoding(detail): return detail
        }
    }
}

/// Everything the menubar asks of the daemon (§7). Mutations send `"via":"menubar"`
/// where the route takes it, and the HTTP client adds `X-Swarm-Via: menubar`.
public protocol DaemonClient: Sendable {
    func state() async throws -> StateResponse
    func notifications(limit: Int) async throws -> [SwarmNotification]
    func agent(_ name: String, _ endpoint: AgentEndpoint, scope: PauseScope?) async throws
    func pauseAll() async throws -> Int
    func answer(requestID: String, text: String) async throws
    func markRead(notificationID: String) async throws
    func readAll() async throws
    func refreshUsage(agent: AgentKind?) async throws
    func saveSettings(_ settings: Settings) async throws -> Settings
    func catalog() async throws -> [AgentCatalogEntry]
    func refreshCatalog() async throws -> [AgentCatalogEntry]
    func repos(query: String) async throws -> ReposResponse
    func addRepo(path: String) async throws -> Repo
    func rescanRepos() async throws -> ScanStats
    func createSpike(_ body: CreateSpikeBody) async throws -> CreateSpikeResponse
    func terminalOpened(name: String) async throws
}

/// In-memory daemon used by the tests and by `SWARM_MOCK_FIXTURES=<dir> swift run SwarmBar`.
/// It records every call as a short string, e.g. `"agent pause login-coder subtree"`.
@MainActor
public final class MockDaemonClient: DaemonClient {
    public var stateResult: Result<StateResponse, DaemonError>
    public var catalogEntries: [AgentCatalogEntry]
    public var reposResponse: ReposResponse
    public var notificationList: [SwarmNotification]
    public var spikeResult: Result<CreateSpikeResponse, DaemonError>?
    public var failNext: DaemonError?
    /// Any error for the next call, e.g. `CancellationError()`.
    public var failNextWith: Error?
    public private(set) var calls: [String] = []

    public init(state: StateResponse = StateResponse(), catalog: [AgentCatalogEntry] = [],
                repos: ReposResponse = ReposResponse(), notifications: [SwarmNotification] = []) {
        stateResult = .success(state)
        catalogEntries = catalog
        reposResponse = repos
        notificationList = notifications
    }

    /// Loads `state.json`, `catalog.json`, `repos.json` and `notifications.json` from a fixture folder.
    public convenience init(fixtures dir: URL) throws {
        func load<T: Decodable>(_ name: String, _ type: T.Type) throws -> T {
            try SwarmJSON.decode(type, from: Data(contentsOf: dir.appendingPathComponent(name)))
        }
        self.init(state: try load("state.json", StateResponse.self),
                  catalog: try load("catalog.json", [AgentCatalogEntry].self),
                  repos: try load("repos.json", ReposResponse.self),
                  notifications: try load("notifications.json", [SwarmNotification].self))
    }

    private func record(_ call: String) throws {
        calls.append(call)
        if let e = failNext {
            failNext = nil
            throw e
        }
        if let e = failNextWith {
            failNextWith = nil
            throw e
        }
    }

    public func state() async throws -> StateResponse {
        try record("state")
        return try stateResult.get()
    }

    public func notifications(limit: Int) async throws -> [SwarmNotification] {
        try record("notifications \(limit)")
        return Array(notificationList.prefix(limit))
    }

    public func agent(_ name: String, _ endpoint: AgentEndpoint, scope: PauseScope?) async throws {
        try record(["agent", endpoint.rawValue, name, scope?.rawValue].compactMap { $0 }.joined(separator: " "))
    }

    public func pauseAll() async throws -> Int {
        try record("pause-all")
        return 1
    }

    public func answer(requestID: String, text: String) async throws {
        try record("answer \(requestID) \(text)")
    }

    public func markRead(notificationID: String) async throws {
        try record("read \(notificationID)")
    }

    public func readAll() async throws {
        try record("read-all")
    }

    public func refreshUsage(agent: AgentKind?) async throws {
        try record("usage-refresh \(agent?.rawValue ?? "all")")
    }

    public func saveSettings(_ settings: Settings) async throws -> Settings {
        try record("settings")
        if case var .success(s) = stateResult {
            s.settings = settings
            stateResult = .success(s)
        }
        return settings
    }

    public func catalog() async throws -> [AgentCatalogEntry] {
        try record("catalog")
        return catalogEntries
    }

    public func refreshCatalog() async throws -> [AgentCatalogEntry] {
        try record("catalog-refresh")
        return catalogEntries
    }

    public func repos(query: String) async throws -> ReposResponse {
        try record("repos \(query)")
        return reposResponse
    }

    public func addRepo(path: String) async throws -> Repo {
        try record("repo-add \(path)")
        return Repo(id: "repo_new", name: URL(fileURLWithPath: path).lastPathComponent, path: path)
    }

    public func rescanRepos() async throws -> ScanStats {
        try record("rescan")
        return ScanStats(found: reposResponse.all.count, missing: 0)
    }

    public func createSpike(_ body: CreateSpikeBody) async throws -> CreateSpikeResponse {
        try record("spike \(body.name)")
        if let r = spikeResult { return try r.get() }
        return CreateSpikeResponse(agent: AgentNode(name: body.name, kind: body.agent, model: body.model, role: .orchestrator), queued: false)
    }

    public func terminalOpened(name: String) async throws {
        try record("terminal-opened \(name)")
    }
}

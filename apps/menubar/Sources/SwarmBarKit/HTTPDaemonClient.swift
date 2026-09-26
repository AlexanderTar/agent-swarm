import Foundation

/// Where the daemon lives and where its token is (§7). `SWARM_URL` and `SWARM_HOME`
/// override the defaults, so `make dev` (port 17777, ~/.swarm-dev) works too.
public struct DaemonEndpoint: Sendable, Equatable {
    public var baseURL: URL
    public var tokenFile: URL

    public init(baseURL: URL, tokenFile: URL) {
        self.baseURL = baseURL
        self.tokenFile = tokenFile
    }

    public static func fromEnvironment(_ env: [String: String] = ProcessInfo.processInfo.environment,
                                       home: URL = FileManager.default.homeDirectoryForCurrentUser) -> DaemonEndpoint {
        let base = env["SWARM_URL"].flatMap(URL.init(string:)) ?? URL(string: "http://127.0.0.1:7777")!
        let swarmHome = env["SWARM_HOME"].map { URL(fileURLWithPath: $0) } ?? home.appendingPathComponent(".swarm")
        return DaemonEndpoint(baseURL: base, tokenFile: swarmHome.appendingPathComponent("run/daemon.token"))
    }

    /// Board links (§14, §16.2).
    public func boardURL(fragment: String = "") -> URL {
        URL(string: fragment.isEmpty ? baseURL.absoluteString + "/" : baseURL.absoluteString + "/#" + fragment)!
    }

    /// A request carrying `Authorization: Bearer <daemon token>`; throws `.unreachable` without a token.
    public func request(_ method: String, _ pathAndQuery: String, timeout: TimeInterval = 10) throws -> URLRequest {
        guard let raw = try? String(contentsOf: tokenFile, encoding: .utf8),
              case let token = raw.trimmingCharacters(in: .whitespacesAndNewlines), !token.isEmpty else {
            throw DaemonError.unreachable
        }
        var r = URLRequest(url: URL(string: baseURL.absoluteString + pathAndQuery)!)
        r.httpMethod = method
        r.timeoutInterval = timeout
        r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        if method != "GET" { r.setValue("menubar", forHTTPHeaderField: "X-Swarm-Via") }
        return r
    }
}

public final class HTTPDaemonClient: DaemonClient {
    public let endpoint: DaemonEndpoint
    private let session: URLSession

    public init(endpoint: DaemonEndpoint, session: URLSession = .shared) {
        self.endpoint = endpoint
        self.session = session
    }

    private struct Empty: Codable {}
    private struct Scope: Encodable { let scope: PauseScope }
    private struct HandoffBody: Encodable {
        let requestID: String
        enum CodingKeys: String, CodingKey { case requestID = "request_id" }
    }
    private struct PauseAll: Decodable { let requested: Int }
    private struct UsageRefresh: Encodable { let agent: AgentKind? }
    private struct AddRepo: Encodable { let path: String }

    /// Routes that legitimately run long: catalog refresh probes every agent CLI, rescan walks the disk.
    static let slowTimeout: TimeInterval = 120

    private func call<T: Decodable>(_ method: String, _ path: String, body: (any Encodable)? = nil,
                                    timeout: TimeInterval = 10, as: T.Type = T.self) async throws -> T {
        var req = try endpoint.request(method, path, timeout: timeout)
        if method != "GET" {
            // Contracts §4 lists `{}` for every mutating route without other fields.
            let payload: any Encodable = body ?? Empty()
            req.httpBody = try SwarmJSON.encode(payload)
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: req)
        } catch let e as URLError where e.code == .cancelled {
            throw CancellationError()
        } catch let e as URLError where e.code == .timedOut {
            throw DaemonError.timedOut
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            throw DaemonError.unreachable
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            if let e = try? SwarmJSON.decode(APIErrorBody.self, from: data) {
                throw DaemonError.api(status: status, code: e.error.code, message: e.error.message)
            }
            throw DaemonError.api(status: status, code: "internal", message: "HTTP \(status)")
        }
        if T.self == Empty.self { return Empty() as! T }
        do {
            return try SwarmJSON.decode(T.self, from: data)
        } catch {
            throw DaemonError.decoding("\(method) \(path): \(error)")
        }
    }

    private static func segment(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? s
    }

    public func state() async throws -> StateResponse { try await call("GET", "/api/state") }

    public func notifications(limit: Int) async throws -> [SwarmNotification] {
        try await call("GET", "/api/notifications?limit=\(limit)")
    }

    public func agent(_ name: String, _ endpoint: AgentEndpoint, scope: PauseScope?) async throws {
        // Handoff accepts a replacement intent: every POST carries a fresh
        // request id (the daemon replays a repeated one onto the same
        // operation instead of starting a second).
        let body: (any Encodable)? = endpoint == .handoff
            ? HandoffBody(requestID: UUID().uuidString)
            : scope.map { Scope(scope: $0) }
        _ = try await call("POST", "/api/agents/\(Self.segment(name))/\(endpoint.rawValue)", body: body, as: Empty.self)
    }

    public func pauseAll() async throws -> Int {
        try await call("POST", "/api/pause-all", as: PauseAll.self).requested
    }

    public func markRead(notificationID: String) async throws {
        _ = try await call("POST", "/api/notifications/\(Self.segment(notificationID))/read", as: Empty.self)
    }

    public func readAll() async throws {
        _ = try await call("POST", "/api/notifications/read-all", as: Empty.self)
    }

    public func refreshUsage(agent: AgentKind?) async throws {
        _ = try await call("POST", "/api/usage/refresh", body: UsageRefresh(agent: agent), as: Empty.self)
    }

    public func saveSettings(_ settings: Settings) async throws -> Settings {
        try await call("PUT", "/api/settings", body: settings)
    }

    public func catalog() async throws -> [AgentCatalogEntry] { try await call("GET", "/api/catalog") }

    public func refreshCatalog() async throws -> [AgentCatalogEntry] { try await call("POST", "/api/catalog/refresh", timeout: Self.slowTimeout) }

    public func repos(query: String) async throws -> ReposResponse {
        let q = query.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? ""
        return try await call("GET", "/api/repos?q=\(q)")
    }

    public func addRepo(path: String) async throws -> Repo {
        try await call("POST", "/api/repos", body: AddRepo(path: path))
    }

    public func rescanRepos() async throws -> ScanStats { try await call("POST", "/api/repos/rescan", timeout: Self.slowTimeout) }

    public func createSpike(_ body: CreateSpikeBody) async throws -> CreateSpikeResponse {
        try await call("POST", "/api/spikes", body: body)
    }

    public func terminalOpened(name: String) async throws {
        _ = try await call("POST", "/api/agents/\(Self.segment(name))/terminal-opened", as: Empty.self)
    }

    public func pane(_ name: String, lines: Int) async throws -> PaneCapture {
        try await call("GET", "/api/agents/\(Self.segment(name))/pane?lines=\(lines)", timeout: 5)
    }
}

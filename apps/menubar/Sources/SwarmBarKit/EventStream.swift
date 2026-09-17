import Foundation

public struct ServerEvent: Equatable, Sendable {
    public var id: Int64?
    public var type: String
    public var data: String

    public init(id: Int64? = nil, type: String, data: String) {
        self.id = id
        self.type = type
        self.data = data
    }
}

/// `text/event-stream` parsing (WHATWG rules the daemon uses: id, event, data, comments).
public struct SSEParser: Sendable {
    public private(set) var lastEventID: Int64?
    private var type = ""
    private var data: [String] = []
    private var id: Int64?

    public init(lastEventID: Int64? = nil) { self.lastEventID = lastEventID }

    /// Feeds one line without its line ending. Returns an event when a blank line ends one.
    public mutating func feed(_ line: String) -> ServerEvent? {
        if line.isEmpty {
            defer { type = ""; data = []; id = nil }
            guard !data.isEmpty || !type.isEmpty else { return nil }
            if let id { lastEventID = id }
            return ServerEvent(id: id, type: type.isEmpty ? "message" : type, data: data.joined(separator: "\n"))
        }
        if line.hasPrefix(":") { return nil }
        let field: Substring, value: Substring
        if let colon = line.firstIndex(of: ":") {
            field = line[..<colon]
            let rest = line[line.index(after: colon)...]
            value = rest.hasPrefix(" ") ? rest.dropFirst() : rest
        } else {
            field = Substring(line)
            value = ""
        }
        switch field {
        case "event": type = String(value)
        case "data": data.append(String(value))
        case "id": id = Int64(value)
        default: break
        }
        return nil
    }
}

/// The events the menubar acts on (§7.1). Everything else that changes state is `.changed`.
public enum SwarmEvent: Equatable, Sendable {
    case reset
    case changed(String)
    case requestOpened(SwarmRequest)
    case notification(SwarmNotification)
    case usage(UsageSnapshot)
    case terminalOpen(TerminalOpen)
    case settings(Settings)
    case catalog([AgentCatalogEntry])

    public init?(_ e: ServerEvent) {
        let data = Data(e.data.utf8)
        func decode<T: Decodable>(_ t: T.Type) -> T? { try? SwarmJSON.decode(t, from: data) }
        switch e.type {
        case "reset": self = .reset
        case "item.changed", "agent.changed", "checkpoint.created", "request.resolved": self = .changed(e.type)
        case "request.opened":
            guard let r = decode(SwarmRequest.self) else { return nil }
            self = .requestOpened(r)
        case "notification.created":
            guard let n = decode(SwarmNotification.self) else { return nil }
            self = .notification(n)
        case "usage.changed":
            guard let u = decode(UsageSnapshot.self) else { return nil }
            self = .usage(u)
        case "terminal.open":
            guard let t = decode(TerminalOpen.self) else { return nil }
            self = .terminalOpen(t)
        case "settings.changed":
            guard let s = decode(Settings.self) else { return nil }
            self = .settings(s)
        case "catalog.changed":
            guard let c = decode([AgentCatalogEntry].self) else { return nil }
            self = .catalog(c)
        default: return nil
        }
    }
}

/// Keeps `GET /api/events` open, reconnecting with backoff (1 s doubling to 30 s,
/// reset after a successful connect) and resuming with `Last-Event-ID`.
@MainActor
public final class EventStream {
    public typealias Connect = @Sendable (URLRequest) async throws -> AsyncThrowingStream<UInt8, Error>
    public typealias Sleep = @Sendable (Duration) async throws -> Void

    public static func backoff(attempt: Int) -> Duration { .seconds(min(30, 1 << min(attempt, 5))) }

    private let endpoint: DaemonEndpoint
    private let connect: Connect
    private let sleep: Sleep
    private let onEvent: @MainActor (SwarmEvent) -> Void
    private let onConnected: @MainActor (Bool) -> Void
    private var task: Task<Void, Never>?
    public private(set) var lastEventID: Int64?
    public private(set) var delays: [Duration] = []

    public init(endpoint: DaemonEndpoint, connect: @escaping Connect, sleep: @escaping Sleep = { try await Task.sleep(for: $0) },
                onEvent: @escaping @MainActor (SwarmEvent) -> Void, onConnected: @escaping @MainActor (Bool) -> Void) {
        self.endpoint = endpoint
        self.connect = connect
        self.sleep = sleep
        self.onEvent = onEvent
        self.onConnected = onConnected
    }

    public func start() {
        guard task == nil else { return }
        task = Task { await self.run() }
    }

    public func stop() {
        task?.cancel()
        task = nil
    }

    /// "Retry connection": drop any pending backoff and connect now.
    public func restart() {
        stop()
        start()
    }

    /// Runs until cancelled. Public so tests can await it with a finite connect sequence.
    public func run() async {
        var attempt = 0
        while !Task.isCancelled {
            do {
                var path = "/api/events"
                var req: URLRequest
                if let id = lastEventID { path += "?after=\(id)" }
                req = try endpoint.request("GET", path)
                req.timeoutInterval = 24 * 3600
                req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                if let id = lastEventID { req.setValue(String(id), forHTTPHeaderField: "Last-Event-ID") }
                let bytes = try await connect(req)
                attempt = 0
                onConnected(true)
                var parser = SSEParser(lastEventID: lastEventID)
                var line: [UInt8] = []
                for try await b in bytes {
                    if b == UInt8(ascii: "\n") {
                        if let e = parser.feed(String(decoding: line, as: UTF8.self)) {
                            lastEventID = parser.lastEventID
                            let typed = SwarmEvent(e)
                            if typed == .reset {
                                // The cursor expired; resume live (contracts §5).
                                lastEventID = nil
                                parser = SSEParser()
                            }
                            if let typed { onEvent(typed) }
                        }
                        line.removeAll(keepingCapacity: true)
                    } else if b != UInt8(ascii: "\r") {
                        line.append(b)
                    }
                }
            } catch {
                if Task.isCancelled { return }
            }
            if Task.isCancelled { return }
            onConnected(false)
            let delay = Self.backoff(attempt: attempt)
            delays.append(delay)
            attempt += 1
            do { try await sleep(delay) } catch { return }
        }
    }

    /// The real transport: URLSession bytes, failing on a non-200 status.
    public static func urlSession(_ session: URLSession) -> Connect {
        { req in
            let (bytes, response) = try await session.bytes(for: req)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else { throw DaemonError.unreachable }
            return AsyncThrowingStream { continuation in
                let pump = Task {
                    do {
                        for try await b in bytes { continuation.yield(b) }
                        continuation.finish()
                    } catch {
                        continuation.finish(throwing: error)
                    }
                }
                continuation.onTermination = { _ in pump.cancel() }
            }
        }
    }
}

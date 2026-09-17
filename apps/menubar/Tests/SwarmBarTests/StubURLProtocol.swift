import Foundation
@testable import SwarmBarKit

/// Serves canned HTTP responses to a URLSession and records the requests it saw.
final class StubURLProtocol: URLProtocol, @unchecked Sendable {
    struct Seen: Equatable {
        let method: String
        let path: String
        let headers: [String: String]
        let body: String
    }

    typealias Handler = @Sendable (URLRequest) throws -> (Int, Data)

    private static let lock = NSLock()
    nonisolated(unsafe) private static var handler: Handler = { _ in (200, Data("{}".utf8)) }
    nonisolated(unsafe) private static var seenRequests: [Seen] = []

    static func install(_ h: @escaping Handler) -> URLSession {
        lock.withLock {
            handler = h
            seenRequests = []
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return URLSession(configuration: config)
    }

    static var seen: [Seen] { lock.withLock { seenRequests } }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        var body = Data()
        if let stream = request.httpBodyStream {
            stream.open()
            var buf = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                body.append(buf, count: n)
            }
            stream.close()
        }
        let url = request.url!
        let path = url.query.map { url.path + "?" + $0 } ?? url.path
        let seen = Seen(method: request.httpMethod ?? "GET", path: path,
                        headers: request.allHTTPHeaderFields ?? [:], body: String(decoding: body, as: UTF8.self))
        let h = Self.lock.withLock {
            Self.seenRequests.append(seen)
            return Self.handler
        }
        do {
            let (status, data) = try h(request)
            let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1",
                                           headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

/// A daemon token in a temp folder.
func tempEndpoint(token: String? = "tok-123") throws -> DaemonEndpoint {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    try FileManager.default.createDirectory(at: dir.appendingPathComponent("run"), withIntermediateDirectories: true)
    let file = dir.appendingPathComponent("run/daemon.token")
    if let token { try Data((token + "\n").utf8).write(to: file) }
    return DaemonEndpoint(baseURL: URL(string: "http://127.0.0.1:7777")!, tokenFile: file)
}

import Foundation
import XCTest
@testable import SwarmBarKit

final class SSEParserTests: XCTestCase {
    func testParsesFieldsCommentsAndMultilineData() {
        var p = SSEParser()
        var out: [ServerEvent] = []
        for line in [": hello", "id: 7", "event: item.changed", "data: {\"a\":", "data:1}", "retry: 5", "", "", "data", ""] {
            if let e = p.feed(line) { out.append(e) }
        }
        XCTAssertEqual(out, [
            ServerEvent(id: 7, type: "item.changed", data: "{\"a\":\n1}"),
            ServerEvent(id: nil, type: "message", data: ""),
        ])
        XCTAssertEqual(p.lastEventID, 7)
    }

    func testFixtureStreamDecodesEveryMenubarEvent() throws {
        let text = String(decoding: try Fixture.data("events/stream.sse"), as: UTF8.self)
        var p = SSEParser()
        let events = text.components(separatedBy: "\n").compactMap { p.feed($0) }.compactMap(SwarmEvent.init)
        let state: StateResponse = try Fixture.decode("state.json")
        XCTAssertEqual(events.count, 10)
        XCTAssertEqual(events[0], .changed("agent.changed"))
        XCTAssertEqual(events[1], .requestOpened(state.requests[0]))
        XCTAssertEqual(events[2], .notification(state.notifications.items[0]))
        XCTAssertEqual(events[3], .usage(state.usage[1]))
        XCTAssertEqual(events[4], .terminalOpen(TerminalOpen(name: "login-form-coder", tmux: "login-form-coder")))
        XCTAssertEqual(events[5], .settings(state.settings))
        guard case let .catalog(c) = events[6] else { return XCTFail("catalog") }
        XCTAssertEqual(c.count, 4)
        XCTAssertEqual(Array(events[7...]), [.changed("item.changed"), .changed("checkpoint.created"), .changed("request.resolved")])
        XCTAssertEqual(p.lastEventID, 51, "repos.changed is parsed but not acted on")

        var r = SSEParser()
        let reset = String(decoding: try Fixture.data("events/reset.sse"), as: UTF8.self)
            .components(separatedBy: "\n").compactMap { r.feed($0) }.compactMap(SwarmEvent.init)
        XCTAssertEqual(reset, [.reset])
    }

    func testUnknownOrMalformedEventsAreDropped() {
        for type in ["request.opened", "notification.created", "usage.changed", "terminal.open", "settings.changed", "catalog.changed", "mystery"] {
            XCTAssertNil(SwarmEvent(ServerEvent(type: type, data: "{\"bad\":true}")), type)
        }
    }
}

@MainActor
final class EventStreamTests: XCTestCase {
    final class Recorder: @unchecked Sendable {
        let lock = NSLock()
        var requests: [URLRequest] = []
        var slept: [Duration] = []
    }

    func testReconnectsWithBackoffAndLastEventID() async throws {
        let rec = Recorder()
        let chunks: [[String]] = [
            ["id: 41", "event: agent.changed", "data: {\"name\":\"a\",\"root_key\":\"EPIC-1\"}", ""],
            [],
            ["id: 42", "event: agent.changed", "data: {\"name\":\"b\",\"root_key\":\"EPIC-1\"}", "", "event: reset", "data: {}", ""],
        ]
        let connect: EventStream.Connect = { req in
            let n = rec.lock.withLock { () -> Int in
                rec.requests.append(req)
                return rec.requests.count
            }
            switch n {
            case 1, 4:
                let lines = chunks[n == 1 ? 0 : 2]
                return AsyncThrowingStream { c in
                    for b in Array((lines.joined(separator: "\r\n") + "\r\n").utf8) { c.yield(b) }
                    c.finish(throwing: URLError(.networkConnectionLost))
                }
            case 2, 3:
                throw URLError(.cannotConnectToHost)
            default:
                throw CancellationError()
            }
        }
        var events: [SwarmEvent] = []
        var status: [Bool] = []
        var stream: EventStream!
        let sleep: EventStream.Sleep = { d in
            let count = rec.lock.withLock { () -> Int in
                rec.slept.append(d)
                return rec.slept.count
            }
            if count >= 5 { throw CancellationError() }
        }
        stream = EventStream(endpoint: try tempEndpoint(), connect: connect, sleep: sleep,
                             onEvent: { events.append($0) }, onConnected: { status.append($0) })
        await stream.run()

        XCTAssertEqual(events, [.changed("agent.changed"), .changed("agent.changed"), .reset])
        XCTAssertNil(stream.lastEventID, "reset drops the expired cursor")
        XCTAssertEqual(rec.slept, [.seconds(1), .seconds(2), .seconds(4), .seconds(1), .seconds(2)])
        XCTAssertEqual(status, [true, false, false, false, true, false, false])
        let reqs = rec.requests
        XCTAssertEqual(reqs.count, 5)
        XCTAssertEqual(reqs[0].url?.absoluteString, "http://127.0.0.1:7777/api/events")
        XCTAssertNil(reqs[0].value(forHTTPHeaderField: "Last-Event-ID"))
        XCTAssertEqual(reqs[1].url?.absoluteString, "http://127.0.0.1:7777/api/events?after=41")
        XCTAssertEqual(reqs[1].value(forHTTPHeaderField: "Last-Event-ID"), "41")
        XCTAssertEqual(reqs[3].value(forHTTPHeaderField: "Last-Event-ID"), "41")
        XCTAssertNil(reqs[4].value(forHTTPHeaderField: "Last-Event-ID"), "after a reset the stream resumes live")
        XCTAssertEqual(reqs[4].url?.absoluteString, "http://127.0.0.1:7777/api/events")
        XCTAssertEqual(reqs[0].value(forHTTPHeaderField: "Accept"), "text/event-stream")
        XCTAssertEqual(reqs[0].value(forHTTPHeaderField: "Authorization"), "Bearer tok-123")
    }

    func testBackoffCapsAt30Seconds() {
        XCTAssertEqual((0...7).map { EventStream.backoff(attempt: $0) },
                       [1, 2, 4, 8, 16, 30, 30, 30].map { Duration.seconds($0) })
    }

    func testStartStopRestartAndMissingToken() async throws {
        let rec = Recorder()
        var status: [Bool] = []
        let stream = EventStream(endpoint: try tempEndpoint(token: nil),
                                 connect: { _ in AsyncThrowingStream { $0.finish() } },
                                 sleep: { d in
                                     rec.lock.withLock { rec.slept.append(d) }
                                     try await Task.sleep(for: .seconds(3600))
                                 },
                                 onEvent: { _ in }, onConnected: { status.append($0) })
        stream.start()
        stream.start()
        try await Task.sleep(for: .milliseconds(100))
        stream.restart()
        try await Task.sleep(for: .milliseconds(100))
        stream.stop()
        XCTAssertEqual(status, [false, false])
        XCTAssertEqual(rec.lock.withLock { rec.slept }, [.seconds(1), .seconds(1)])
    }

    func testURLSessionTransportStreamsBytes() async throws {
        let session = StubURLProtocol.install { req in
            req.url!.path == "/api/events" ? (200, try Fixture.data("events/reset.sse")) : (401, Data())
        }
        let connect = EventStream.urlSession(session)
        let endpoint = try tempEndpoint()
        var bytes: [UInt8] = []
        for try await b in try await connect(try endpoint.request("GET", "/api/events")) { bytes.append(b) }
        XCTAssertEqual(String(decoding: bytes, as: UTF8.self), "event: reset\ndata: {}\n\n")
        do {
            _ = try await connect(try endpoint.request("GET", "/api/other"))
            XCTFail("non-200 must throw")
        } catch {
            XCTAssertEqual(error as? DaemonError, .unreachable)
        }
    }
}

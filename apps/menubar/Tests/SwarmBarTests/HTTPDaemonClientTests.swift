import Foundation
import XCTest
@testable import SwarmBarKit

final class HTTPDaemonClientTests: XCTestCase {
    func testEndpointFromEnvironment() {
        let home = URL(fileURLWithPath: "/Users/alex")
        let d = DaemonEndpoint.fromEnvironment([:], home: home)
        XCTAssertEqual(d.baseURL.absoluteString, "http://127.0.0.1:7777")
        XCTAssertEqual(d.tokenFile.path, "/Users/alex/.swarm/run/daemon.token")
        let dev = DaemonEndpoint.fromEnvironment(["SWARM_URL": "http://127.0.0.1:17777", "SWARM_HOME": "/tmp/sw"], home: home)
        XCTAssertEqual(dev.baseURL.absoluteString, "http://127.0.0.1:17777")
        XCTAssertEqual(dev.tokenFile.path, "/tmp/sw/run/daemon.token")
        XCTAssertEqual(d.boardURL().absoluteString, "http://127.0.0.1:7777/")
        XCTAssertEqual(d.boardURL(fragment: "/inbox?req=req_1").absoluteString, "http://127.0.0.1:7777/#/inbox?req=req_1")
    }

    func testGetStateSendsTheTokenAndDecodes() async throws {
        let session = StubURLProtocol.install { _ in (200, try Fixture.data("state.json")) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        let state = try await client.state()
        XCTAssertEqual(state.agents.count, 5)
        let seen = StubURLProtocol.seen
        XCTAssertEqual(seen.map(\.path), ["/api/state"])
        XCTAssertEqual(seen[0].headers["Authorization"], "Bearer tok-123")
        XCTAssertNil(seen[0].headers["X-Swarm-Via"])
    }

    func testLowTokenRoutesAndBodies() async throws {
        let session = StubURLProtocol.install { req in
            switch req.url!.path {
            case "/api/agents/orch/low-token": return (200, try Fixture.data("low-token-agent.json"))
            case "/api/low-token": return (200, try Fixture.data("low-token-all.json"))
            default: return (204, Data())
            }
        }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        let one = try await client.setLowToken(agent: "orch", on: true)
        let all = try await client.setLowTokenAll(on: false)
        XCTAssertEqual([one, all], [3, 5])
        let seen = StubURLProtocol.seen
        XCTAssertEqual(seen.map { "\($0.method) \($0.path)" }, ["POST /api/agents/orch/low-token", "POST /api/low-token"])
        XCTAssertEqual(try Fixture.json(Data(seen[0].body.utf8)), try Fixture.json(Fixture.data("low-token-request.json")))
        XCTAssertEqual(try Fixture.json(Data(seen[1].body.utf8)), ["on": false] as NSDictionary)
        XCTAssertTrue(seen.allSatisfy { $0.headers["X-Swarm-Via"] == "menubar" })
    }

    func testMutationsUseTheSpecRoutesAndBodies() async throws {
        let session = StubURLProtocol.install { req in
            switch req.url!.path {
            case "/api/pause-all": return (200, try Fixture.data("pause-all.json"))
            case "/api/settings": return (200, try Fixture.data("settings.json"))
            case "/api/catalog", "/api/catalog/refresh": return (200, try Fixture.data("catalog.json"))
            case "/api/repos": return req.httpMethod == "POST" ? (201, try Fixture.data("repo.json")) : (200, try Fixture.data("repos.json"))
            case "/api/repos/rescan": return (200, try Fixture.data("rescan.json"))
            case "/api/spikes": return (200, try Fixture.data("spike-response.json"))
            case "/api/notifications": return (200, try Fixture.data("notifications.json"))
            case "/api/agents/login-form-coder/pause": return (200, try Fixture.data("agent.json"))
            default: return (204, Data())
            }
        }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        try await client.agent("login-form-coder", .pause, scope: .subtree)
        try await client.agent("docs-fix-coder", .ack, scope: nil)
        let requested = try await client.pauseAll()
        try await client.markRead(notificationID: "ntf_05")
        try await client.readAll()
        try await client.refreshUsage(agent: .claude)
        let saved = try await client.saveSettings(try Fixture.decode("settings.json"))
        let catalog = try await client.catalog()
        let refreshed = try await client.refreshCatalog()
        let repos = try await client.repos(query: "endurio chat")
        let added = try await client.addRepo(path: "/Users/alex/.config/notes")
        let scan = try await client.rescanRepos()
        let spike = try await client.createSpike(try Fixture.decode("spike-request.json"))
        let notes = try await client.notifications(limit: 50)
        try await client.terminalOpened(name: "login-form-coder")

        XCTAssertEqual(requested, 3)
        XCTAssertEqual(saved.enabledAgents, [.claude, .codex, .agy])
        XCTAssertEqual([catalog.count, refreshed.count, notes.count], [4, 4, 5])
        XCTAssertEqual(repos.all.count, 5)
        XCTAssertEqual(added.id, "repo_notes")
        XCTAssertEqual(scan.found, 112)
        XCTAssertEqual(spike.agent.name, "investigate-login-crash")

        let seen = StubURLProtocol.seen
        XCTAssertEqual(seen.map { "\($0.method) \($0.path)" }, [
            "POST /api/agents/login-form-coder/pause",
            "POST /api/agents/docs-fix-coder/ack",
            "POST /api/pause-all",
            "POST /api/notifications/ntf_05/read",
            "POST /api/notifications/read-all",
            "POST /api/usage/refresh",
            "PUT /api/settings",
            "GET /api/catalog",
            "POST /api/catalog/refresh",
            "GET /api/repos?q=endurio%20chat",
            "POST /api/repos",
            "POST /api/repos/rescan",
            "POST /api/spikes",
            "GET /api/notifications?limit=50",
            "POST /api/agents/login-form-coder/terminal-opened",
        ])
        XCTAssertEqual(seen[0].body, #"{"scope":"subtree"}"#)
        let empty = try Fixture.json(Fixture.data("empty-request.json"))
        for i in [1, 2, 3, 4, 8, 11, 14] {
            XCTAssertEqual(try Fixture.json(Data(seen[i].body.utf8)), empty, seen[i].path)
            XCTAssertEqual(seen[i].headers["Content-Type"], "application/json")
        }
        XCTAssertTrue(seen.filter { $0.method == "GET" }.allSatisfy { $0.body.isEmpty })
        XCTAssertEqual(try Fixture.json(Data(seen[5].body.utf8)), try Fixture.json(Fixture.data("usage-refresh-request.json")))
        XCTAssertEqual(try Fixture.json(Data(seen[6].body.utf8)), try Fixture.json(Fixture.data("settings.json")))
        XCTAssertEqual(try Fixture.json(Data(seen[12].body.utf8)), try Fixture.json(Fixture.data("spike-request.json")))
        XCTAssertEqual(try Fixture.json(Data(seen[10].body.utf8)), try Fixture.json(Fixture.data("repo-add-request.json")))
        XCTAssertTrue(seen.filter { $0.method != "GET" }.allSatisfy { $0.headers["X-Swarm-Via"] == "menubar" })
        XCTAssertTrue(seen.allSatisfy { $0.headers["Authorization"] == "Bearer tok-123" })
        // Slow routes (catalog refresh probes every CLI, rescan walks the disk) get 120 s; spikes carry
        // base64 images so get 60 s (request-images spec); the rest 10 s.
        XCTAssertEqual(seen.filter { $0.timeout == 120 }.map(\.path), ["/api/catalog/refresh", "/api/repos/rescan"])
        XCTAssertEqual(seen.filter { $0.timeout == 60 }.map(\.path), ["/api/spikes"])
        XCTAssertTrue(seen.filter { ![120, 60].contains($0.timeout) }.allSatisfy { $0.timeout == 10 })
    }

    func testPaneSendsLinesAndUsesTheFiveSecondTimeout() async throws {
        let session = StubURLProtocol.install { _ in (200, try Fixture.data("pane.json")) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        let pane = try await client.pane("login-form-coder", lines: 40)
        XCTAssertEqual(pane, try Fixture.decode("pane.json"))
        let seen = StubURLProtocol.seen
        XCTAssertEqual(seen.map(\.path), ["/api/agents/login-form-coder/pane?lines=40"])
        XCTAssertEqual(seen[0].timeout, 5)
    }

    func testTimeoutsAreNotOutages() async throws {
        let session = StubURLProtocol.install { req in
            if req.url!.path == "/api/repos/rescan" { throw URLError(.timedOut) }
            throw URLError(.cannotConnectToHost)
        }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        await assertThrows(.timedOut) { _ = try await client.rescanRepos() }
        await assertThrows(.unreachable) { _ = try await client.catalog() }
        XCTAssertNotEqual(DaemonError.timedOut.message, DaemonError.unreachable.message)
    }

    func testErrorsMapToDaemonError() async throws {
        let session = StubURLProtocol.install { req in
            switch req.url!.path {
            case "/api/agents/login-review/resume": return (409, try Fixture.data("error-conflict.json"))
            case "/api/repos": return (422, try Fixture.data("error-not-repo.json"))
            case "/api/state": return (500, Data("oops".utf8))
            case "/api/catalog": return (200, Data("{\"nope\":1}".utf8))
            case "/api/usage/refresh": throw URLError(.cancelled)
            default: throw URLError(.cannotConnectToHost)
            }
        }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        await assertThrows(.api(status: 409, code: "conflict", message: "Still stopping. Try again in a few seconds.")) {
            try await client.agent("login-review", .resume, scope: nil)
        }
        await assertThrows(.api(status: 422, code: "bad_request", message: "No git repository found in this folder.")) {
            _ = try await client.addRepo(path: "/tmp")
        }
        do {
            try await client.refreshUsage(agent: nil)
            XCTFail("expected cancellation")
        } catch {
            XCTAssertTrue(error is CancellationError, "a cancelled request is not a daemon outage")
        }
        await assertThrows(.api(status: 500, code: "internal", message: "HTTP 500")) { _ = try await client.state() }
        await assertThrows(.unreachable) { try await client.readAll() }
        do {
            _ = try await client.catalog()
            XCTFail("expected a decoding error")
        } catch let DaemonError.decoding(detail) {
            XCTAssertTrue(detail.hasPrefix("GET /api/catalog"))
        }

        let noToken = HTTPDaemonClient(endpoint: try tempEndpoint(token: nil), session: session)
        await assertThrows(.unreachable) { _ = try await noToken.state() }
        let blankToken = HTTPDaemonClient(endpoint: try tempEndpoint(token: "  "), session: session)
        await assertThrows(.unreachable) { _ = try await blankToken.state() }
    }

    private func assertThrows(_ expected: DaemonError, _ body: () async throws -> Void,
                              file: StaticString = #filePath, line: UInt = #line) async {
        do {
            try await body()
            XCTFail("expected \(expected)", file: file, line: line)
        } catch {
            XCTAssertEqual(error as? DaemonError, expected, file: file, line: line)
        }
    }

    func testHandoffSendsTheCallersRequestKey() async throws {
        let session = StubURLProtocol.install { _ in (202, Data("{}".utf8)) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        try await client.agent("login-form-coder", .handoff, scope: nil, requestID: "key-1")
        try await client.agent("login-form-coder", .handoff, scope: nil, requestID: "key-1")
        let bodies = StubURLProtocol.seen.map(\.body)
        XCTAssertEqual(bodies.count, 2)
        XCTAssertTrue(bodies.allSatisfy { $0.contains("\"request_id\":\"key-1\"") }, "\(bodies)")
    }

    func testHandoffWithoutSwitchSendsOnlyRequestID() async throws {
        let session = StubURLProtocol.install { _ in (202, Data("{}".utf8)) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        try await client.handoff("auth-epic-orchestrator", HandoffRequest(requestId: "k1"))
        let seen = try XCTUnwrap(StubURLProtocol.seen.last)
        XCTAssertEqual(seen.method, "POST")
        XCTAssertEqual(seen.path, "/api/agents/auth-epic-orchestrator/handoff")
        XCTAssertEqual(try Fixture.json(Data(seen.body.utf8)), try Fixture.json(Data(#"{"request_id":"k1"}"#.utf8)))
    }

    func testHandoffWithSwitchSendsPicksAndNoneAdvisor() async throws {
        let session = StubURLProtocol.install { _ in (202, Data("{}".utf8)) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        try await client.handoff("o", HandoffRequest(requestId: "k2", agent: .codex, model: "gpt-6-astra", effort: "high", advisor: AdvisorPayload.none))
        let body = try XCTUnwrap(StubURLProtocol.seen.last).body
        XCTAssertEqual(try Fixture.json(Data(body.utf8)),
                       try Fixture.json(Data(#"{"request_id":"k2","agent":"codex","model":"gpt-6-astra","effort":"high","advisor":"none"}"#.utf8)))
    }

    func testStartOrchestratorBodyHasNoRolesOrRepos() async throws {
        let session = StubURLProtocol.install { _ in (200, try Fixture.data("agent.json")) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        _ = try await client.startOrchestrator(itemKey: "BUG-7", StartOrchestratorBody(requestId: "r", agent: .claude, model: "opus", effort: nil, advisor: .none))
        let seen = try XCTUnwrap(StubURLProtocol.seen.last)
        XCTAssertEqual(seen.path, "/api/items/BUG-7/orchestrator")
        XCTAssertFalse(seen.body.contains("roles") || seen.body.contains("repos"), seen.body)
    }

    func testStartOrchestratorBodyEncodesRoles() throws {
        let body = StartOrchestratorBody(requestId: "r", agent: .claude, model: "opus", effort: nil, advisor: .none,
                                         roles: ["coder": RoleDefault(agent: .codex, model: "gpt-6-astra")])
        XCTAssertEqual(try Fixture.json(SwarmJSON.encode(body)),
                       try Fixture.json(Data(#"{"request_id":"r","agent":"claude","model":"opus","advisor":"none","roles":{"coder":{"agent":"codex","model":"gpt-6-astra"}}}"#.utf8)),
                       "daemon key is \"roles\" (orchestratorRequestBody)")
    }

    func testStartOrchestratorSendsRoles() async throws {
        let session = StubURLProtocol.install { _ in (200, try Fixture.data("agent.json")) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        _ = try await client.startOrchestrator(itemKey: "BUG-7", StartOrchestratorBody(requestId: "r", agent: .claude, model: "opus",
            effort: nil, advisor: .none, roles: ["coder": RoleDefault(agent: .codex, model: "gpt-6-astra", effort: "high")]))
        let body = try XCTUnwrap(StubURLProtocol.seen.last).body
        XCTAssertEqual(try Fixture.json(Data(body.utf8)),
                       try Fixture.json(Data(#"{"request_id":"r","agent":"claude","model":"opus","advisor":"none","roles":{"coder":{"agent":"codex","model":"gpt-6-astra","effort":"high"}}}"#.utf8)))
    }

    func testBoardItemsDecodesFlatListIncludingUnknownTypes() async throws {
        let session = StubURLProtocol.install { _ in (200, try Fixture.data("items.json")) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        let items = try await client.boardItems()
        XCTAssertEqual(StubURLProtocol.seen.last?.path, "/api/items?view=flat")
        XCTAssertEqual(items.count, 9)
        XCTAssertEqual(items.last?.type, "widget")
    }
}

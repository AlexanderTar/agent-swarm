import Foundation
import XCTest
@testable import SwarmBarKit

@MainActor
final class MockDaemonClientTests: XCTestCase {
    func testLoadsFixturesAndRecordsCalls() async throws {
        let mock = try MockDaemonClient(fixtures: Fixture.dir)
        let state = try await mock.state()
        XCTAssertEqual(state.agents.count, 5)
        try await mock.agent("login-form-coder", .pause, scope: .subtree)
        try await mock.agent("docs-fix-coder", .ack, scope: nil)
        let paused = try await mock.pauseAll()
        let firstTwo = try await mock.notifications(limit: 2)
        try await mock.markRead(notificationID: "ntf_05")
        try await mock.readAll()
        try await mock.refreshUsage(agent: .claude)
        try await mock.refreshUsage(agent: nil)
        let catalog = try await mock.catalog()
        let refreshed = try await mock.refreshCatalog()
        let repos = try await mock.repos(query: "endurio")
        let added = try await mock.addRepo(path: "/tmp/notes")
        let scan = try await mock.rescanRepos()
        try await mock.terminalOpened(name: "login-form-coder")
        let pane = try await mock.pane("login-form-coder", lines: 40)
        XCTAssertEqual(paused, 1)
        XCTAssertEqual(firstTwo.count, 2)
        XCTAssertEqual([catalog.count, refreshed.count, repos.recent.count], [4, 4, 1])
        XCTAssertEqual(added.name, "notes")
        XCTAssertEqual(scan, ScanStats(found: 5, missing: 0))
        XCTAssertEqual(pane, try Fixture.decode("pane.json"))
        XCTAssertEqual(mock.calls, [
            "state", "agent pause login-form-coder subtree", "agent ack docs-fix-coder",
            "pause-all", "notifications 2", "read ntf_05", "read-all",
            "usage-refresh claude", "usage-refresh all", "catalog", "catalog-refresh", "repos endurio",
            "repo-add /tmp/notes", "rescan", "terminal-opened login-form-coder", "pane login-form-coder 40",
        ])
    }

    func testFailuresAndSettingsSave() async throws {
        let mock = MockDaemonClient()
        mock.failNext = .unreachable
        do {
            _ = try await mock.state()
            XCTFail("expected unreachable")
        } catch {
            XCTAssertEqual(error as? DaemonError, .unreachable)
        }
        let recovered = try await mock.state()
        XCTAssertEqual(recovered.agents, [])

        var s = Settings.defaults
        s.maxConcurrentAgents = 2
        let saved = try await mock.saveSettings(s)
        let reloaded = try await mock.state()
        XCTAssertEqual([saved.maxConcurrentAgents, reloaded.settings.maxConcurrentAgents], [2, 2])

        let body = CreateSpikeBody(requestId: "r", name: "x", intent: .feature, repos: [], agent: .claude,
                                   model: "opus", effort: nil, advisor: .none, request: nil)
        let created = try await mock.createSpike(body)
        XCTAssertEqual(created.agent.name, "x")
        mock.spikeResult = .failure(.api(status: 409, code: "conflict", message: "This agent name is already in use."))
        do {
            _ = try await mock.createSpike(body)
            XCTFail("expected conflict")
        } catch let e as DaemonError {
            XCTAssertEqual(e.message, "This agent name is already in use.")
        }
        XCTAssertEqual(DaemonError.unreachable.message, "Daemon unavailable.")
        XCTAssertEqual(DaemonError.decoding("bad").message, "bad")
    }
}

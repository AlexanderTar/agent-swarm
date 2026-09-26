import Foundation
import XCTest
@testable import SwarmBarKit

/// Batch 3: the menubar Handoff action, the preview header order, and the
/// optional effort / replacement wire fields.
final class HandoffTests: XCTestCase {
    private func agent(_ state: SessionState? = .running, waiting: Bool = false, stale: Bool = false,
                       role: Role = .coder, agentState: AgentState = .active, preflight: String? = nil,
                       name: String = "a") -> AgentNode {
        AgentNode(name: name, model: "test-model", role: role, state: agentState,
                  session: state.map { SessionInfo(state: $0, waiting: waiting, stale: stale) },
                  preflightError: preflight)
    }

    private func endpoints(_ a: AgentNode, tmux: Bool = true, connected: Bool = true) -> [String] {
        AgentTree.actions(a, tmuxAlive: tmux, connected: connected).map {
            "\($0.endpoint.rawValue):\($0.label)\($0.disabled ? ":disabled" : "")\($0.placement == .menu ? ":menu" : "")"
        }
    }

    // MARK: - wire: effort and replacement decode (Go wire already carries effort)

    func testAgentNodeDecodesOptionalEffortAndReplacement() throws {
        let data = """
        {"id":"agt_1","name":"a","kind":"claude","model":"opus","effort":"high","role":"coder",
         "item_key":"TASK-1","item_title":"T","root_key":"EPIC-1","state":"active",
         "session":{"id":"ses_1","state":"running","attempt":1,"generation":2,"waiting":false,
                    "stale":false,"tmux_alive":true,"started_at":0},
         "replacement":{"operation_id":"op_1","mode":"handoff","phase":"stopping","error":""},
         "children":[],"finished":[]}
        """.data(using: .utf8)!
        let node = try SwarmJSON.decode(AgentNode.self, from: data)
        XCTAssertEqual(node.effort, "high")
        XCTAssertEqual(node.replacement?.operationID, "op_1")
        XCTAssertEqual(node.replacement?.phase, "stopping")
    }

    func testAgentNodeWithoutEffortDecodesAsNil() throws {
        let data = """
        {"id":"agt_1","name":"a","kind":"claude","model":"opus","role":"coder",
         "item_key":"TASK-1","item_title":"T","root_key":"EPIC-1","state":"active",
         "children":[],"finished":[]}
        """.data(using: .utf8)!
        let node = try SwarmJSON.decode(AgentNode.self, from: data)
        XCTAssertNil(node.effort)
        XCTAssertNil(node.replacement)
    }

    // MARK: - preview header order: name, item, agent display, model (effort)

    func testPaneHeaderOrder() {
        XCTAssertEqual(Copy.paneHeader("login-coder", "TASK-101", "Claude", "Opus 4.6", "High"),
                       "login-coder · TASK-101 · Claude · Opus 4.6 (High)")
    }

    func testHumanEffortLabels() {
        XCTAssertEqual(Copy.humanEffort("high"), "High")
        XCTAssertEqual(Copy.humanEffort("xhigh"), "Extra high")
        XCTAssertEqual(Copy.humanEffort("max"), "Max")
    }

    func testPaneHeaderOmitsEmptyEffort() {
        XCTAssertEqual(Copy.paneHeader("a", "T-1", "Claude", "Opus 4.6", nil),
                       "a · T-1 · Claude · Opus 4.6")
        XCTAssertEqual(Copy.paneHeader("a", "T-1", "Claude", "Opus 4.6", ""),
                       "a · T-1 · Claude · Opus 4.6")
    }

    func testUnknownModelFallsBackToID() {
        XCTAssertEqual(CatalogRules.modelLabel(nil, "mystery-9"), "mystery-9")
    }

    // MARK: - Handoff eligibility

    func testHandoffEligibleOnLivePausedInterruptedFailedAndCancelled() {
        for a in [agent(.running), agent(.running, waiting: true), agent(.running, stale: true),
                  agent(.paused), agent(.interrupted), agent(.failed), agent(.crashed),
                  agent(.cancelled)] {
            XCTAssertTrue(endpoints(a).contains("handoff:Handoff:menu"), "\(DisplayState(a)) must offer Handoff")
        }
    }

    func testHandoffDisabledWhileSpawningOrQueued() {
        XCTAssertTrue(endpoints(agent(.spawning)).contains("handoff:Wait for startup:disabled:menu"))
        XCTAssertTrue(endpoints(agent(nil)).contains("handoff:Wait for startup:disabled:menu"))
    }

    func testHandoffAbsentForCompletedCancelledAssignmentsAndPreflightFailures() {
        XCTAssertFalse(endpoints(agent(.completed)).joined().contains("handoff"))
        XCTAssertFalse(endpoints(agent(.cancelled, agentState: .finished)).joined().contains("handoff"))
        XCTAssertFalse(endpoints(agent(nil, preflight: "nope")).joined().contains("handoff"))
        XCTAssertFalse(endpoints(agent(.pauseRequested)).joined().contains("handoff"))
    }

    // MARK: - handoff phase labels

    private func status(_ phase: String, error: String = "") -> String? {
        var a = agent(.running)
        a.replacement = AgentReplacement(operationID: "op_1", mode: "handoff", phase: phase, error: error)
        return AgentTree.handoffStatus(a)
    }

    func testHandoffPhaseLabels() {
        XCTAssertEqual(status("requested"), "Saving handoff…")
        XCTAssertEqual(status("preserving"), "Saving handoff…")
        XCTAssertEqual(status("stopping"), "Stopping…")
        XCTAssertEqual(status("ready"), "Handoff queued")
        XCTAssertEqual(status("queued"), "Handoff queued")
        XCTAssertEqual(status("starting"), "Starting successor…")
        XCTAssertEqual(status("blocked", error: "dirty worktree"), "Handoff blocked: dirty worktree")
        XCTAssertNil(AgentTree.handoffStatus(agent(.running)))
    }

    // The phase label is what the row actually shows on line 2 while a
    // handoff is in flight, in place of the raw session state.
    func testSubtitleRendersHandoffPhase() {
        var a = agent(.pauseRequested)
        a.replacement = AgentReplacement(operationID: "op_1", mode: "handoff", phase: "preserving", error: "")
        XCTAssertTrue(AgentTree.subtitle(a).hasSuffix(" · Saving handoff…"), AgentTree.subtitle(a))
        a.replacement = AgentReplacement(operationID: "op_1", mode: "handoff", phase: "blocked", error: "dirty worktree")
        XCTAssertTrue(AgentTree.subtitle(a).hasSuffix(" · Handoff blocked: dirty worktree"), AgentTree.subtitle(a))
        XCTAssertFalse(AgentTree.subtitle(agent(.running)).contains("handoff"))
    }

    // MARK: - row stability: a replacement never renames, regroups or reselects

    func testRowsStableAcrossReplacement() {
        let plain = agent(.running, name: "w")
        var busy = plain
        busy.replacement = AgentReplacement(operationID: "op_1", mode: "handoff", phase: "stopping", error: "")
        let before = AgentTree.rows([plain], collapsed: [], openFinished: [], openFailed: [])
        let after = AgentTree.rows([busy], collapsed: [], openFinished: [], openFailed: [])
        XCTAssertEqual(before.map(\.id), after.map(\.id))
        XCTAssertEqual(before.map(\.depth), after.map(\.depth))
        XCTAssertEqual(before.map(\.expanded), after.map(\.expanded))
    }
}

@MainActor
final class HandoffModelTests: XCTestCase {
    var client: MockDaemonClient!
    var defaults = MemoryStore()
    var cacheURL: URL!

    override func setUp() async throws {
        client = try MockDaemonClient(fixtures: Fixture.dir)
        cacheURL = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).appendingPathComponent("state.json")
    }

    private func make() -> AppModel {
        let terminals = Terminals(runner: FakeRunner(), script: FakeScript(), ghosttyPIDs: { [] },
                                  fileExists: { $0 == "/opt/homebrew/bin/tmux" })
        return AppModel(client: client,
                        endpoint: DaemonEndpoint(baseURL: URL(string: "http://127.0.0.1:7777")!,
                                                 tokenFile: URL(fileURLWithPath: "/nonexistent")),
                        terminals: terminals, poster: FakePoster(), defaults: defaults,
                        cache: StateCache(url: cacheURL),
                        connect: { _ in throw URLError(.cannotConnectToHost) },
                        now: { fixtureNow }, timeZone: TimeZone(identifier: "UTC")!,
                        openURL: { _ in })
    }

    private func runningCoder(_ m: AppModel) -> AgentNode {
        let all = AgentTree.flatten(m.state.agents)
        guard let a = all.first(where: { DisplayState($0) == .running }) else {
            fatalError("fixture has no running agent")
        }
        return a
    }

    /// The preview's current screen text, or nil when no capture has landed
    /// within the bound. Never awaits the poll loop itself, which only ends
    /// on cancel.
    private func capturedText(_ m: AppModel) async -> String? {
        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline {
            if case let .text(t, _) = m.preview.status { return t }
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        return nil
    }

    // Repeat clicks while a handoff is in flight send exactly one request.
    func testRepeatHandoffClickSendsOnce() async {
        let m = make()
        await m.refresh()
        let a = runningCoder(m)
        guard let handoff = m.actions(a).first(where: { $0.endpoint == .handoff }) else {
            return XCTFail("running fixture agent must offer Handoff")
        }
        client.holdAgent = true
        let first = Task { await m.perform(handoff, on: a) }
        await Task.yield()
        await m.perform(handoff, on: a)
        client.releaseAgent()
        await first.value
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("agent handoff") }.count, 1)
    }

    // A generation change under the hovered agent drops the predecessor's
    // last screen and recaptures from loading. The poll loop never ends on
    // its own (real sleep), so nothing here awaits pollTask.value: bounded
    // spin-waits observe each capture, and holdPane freezes the recapture
    // so the .loading transition asserts deterministically.
    func testPreviewInvalidatesOnGenerationChange() async {
        let m = make()
        await m.refresh()
        let a = runningCoder(m)
        m.preview.hover(a.name, anchor: .none)
        guard await capturedText(m) != nil else {
            return XCTFail("preview never captured, got \(m.preview.status)")
        }
        if case var .success(s) = client.stateResult {
            var agents = s.agents
            func bump(_ nodes: [AgentNode]) -> [AgentNode] {
                nodes.map { n in
                    var n = n
                    if n.name == a.name, var ses = n.session {
                        ses.generation += 1
                        n.session = ses
                    }
                    n.children = bump(n.children)
                    return n
                }
            }
            agents = bump(agents)
            s = StateResponse(agents: agents, requests: s.requests, notifications: s.notifications,
                              usage: s.usage, activeCount: s.activeCount, settings: s.settings)
            client.stateResult = .success(s)
        }
        client.holdPane = [a.name]
        await m.refresh()
        XCTAssertEqual(m.preview.status, .loading)
        // Drain the pane gates until the recapture lands: a release before
        // the recapture parks is a no-op, so repeat it — stale captures
        // from the cancelled pre-refresh loop drop via the generation check
        // and only the recapture flips the status back to text.
        let recaptureDeadline = Date().addingTimeInterval(5)
        while m.preview.status == .loading, Date() < recaptureDeadline {
            client.releasePane(a.name)
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        guard await capturedText(m) != nil else {
            return XCTFail("preview never recaptured after invalidation")
        }
    }

    // One user action keeps one request key: a retry after a timeout or an
    // unreachable daemon replays the same key (the daemon returns the same
    // operation instead of a 409), and the next action after it lands mints a
    // new one.
    func testHandoffRetryReusesTheRequestKey() async {
        let m = make()
        await m.refresh()
        let a = runningCoder(m)
        guard let handoff = m.actions(a).first(where: { $0.endpoint == .handoff }) else {
            return XCTFail("running fixture agent must offer Handoff")
        }
        // The timed-out POST landed: state shows the operation in flight.
        setReplacement(a.name, phase: "preserving")
        client.failNext = .timedOut
        await m.perform(handoff, on: a)
        await m.perform(handoff, on: a)
        await m.perform(handoff, on: a)
        let keys = client.handoffRequestIDs
        XCTAssertEqual(keys.count, 3)
        XCTAssertEqual(keys[0], keys[1], "the retry after a timeout must reuse the request key")
        XCTAssertNotEqual(keys[1], keys[2], "a new action after success gets a new request key")
    }

    /// Sets (or clears, with nil) the in-flight replacement the mock daemon
    /// reports for one agent.
    private func setReplacement(_ name: String, phase: String?) {
        guard case var .success(s) = client.stateResult else { return }
        func edit(_ nodes: [AgentNode]) -> [AgentNode] {
            nodes.map { n in
                var n = n
                if n.name == name {
                    n.replacement = phase.map { AgentReplacement(operationID: "op_1", mode: "handoff", phase: $0, error: "") }
                }
                n.children = edit(n.children)
                return n
            }
        }
        s = StateResponse(agents: edit(s.agents), requests: s.requests, notifications: s.notifications,
                          usage: s.usage, activeCount: s.activeCount, settings: s.settings)
        client.stateResult = .success(s)
    }

    // A key kept after a transport error must not outlive its operation: once
    // state shows that agent's operation settled, the next deliberate Handoff
    // mints a fresh key instead of replaying the old finished operation.
    func testHandoffKeyDroppedOnceItsOperationSettles() async {
        let m = make()
        await m.refresh()
        let a = runningCoder(m)
        guard let handoff = m.actions(a).first(where: { $0.endpoint == .handoff }) else {
            return XCTFail("running fixture agent must offer Handoff")
        }
        setReplacement(a.name, phase: "preserving")
        client.failNext = .unreachable
        await m.perform(handoff, on: a)
        setReplacement(a.name, phase: nil) // the operation finished
        await m.refresh()
        await m.perform(handoff, on: a)
        let keys = client.handoffRequestIDs
        XCTAssertEqual(keys.count, 2)
        XCTAssertNotEqual(keys[0], keys[1], "a settled operation's key must not be replayed")
    }
}

import Foundation
import XCTest
@testable import SwarmBarKit

final class AgentActionsTests: XCTestCase {
    private func agent(_ state: SessionState? = .running, waiting: Bool = false, stale: Bool = false,
                       role: Role = .coder, agentState: AgentState = .active, preflight: String? = nil,
                       children: [AgentNode] = [], name: String = "a") -> AgentNode {
        AgentNode(name: name, model: "test-model", role: role, state: agentState,
                  session: state.map { SessionInfo(state: $0, waiting: waiting, stale: stale) },
                  preflightError: preflight, children: children)
    }

    private func labels(_ a: AgentNode, tmux: Bool = true, connected: Bool = true) -> [String] {
        AgentTree.actions(a, tmuxAlive: tmux, connected: connected).map {
            "\($0.endpoint.rawValue):\($0.label)\($0.disabled ? ":disabled" : "")\($0.placement == .menu ? ":menu" : "")"
        }
    }

    func testDisplayStateLabelAndDotForEverySessionState() {
        let table: [(AgentNode, DisplayState, String?, DotTone)] = [
            (agent(nil, agentState: .queued), .queued, "Queued", .grey),
            (agent(nil), .queued, "Queued", .grey),
            (agent(nil, preflight: "Codex isn't installed on this Mac."), .preflightFailed, "Failed", .red),
            (agent(nil, agentState: .queued, preflight: "x"), .preflightFailed, "Failed", .red),
            (agent(.running), .running, nil, .green),
            (agent(.running, waiting: true), .waiting, "Waiting", .greenHollow),
            (agent(.running, stale: true), .stale, "No activity for 30 min", .amber),
            (agent(.spawning), .spawning, "Starting", .greyPulse),
            (agent(.pauseRequested), .pauseRequested, "Pause requested", .amber),
            (agent(.quiescing), .quiescing, "Finishing current step", .amber),
            (agent(.stopping), .stopping, "Stopping", .amber),
            (agent(.paused), .paused, "Paused", .hollow),
            (agent(.interrupted), .interrupted, "Interrupted", .red),
            (agent(.crashed), .crashed, "Crashed", .red),
            (agent(.failed), .failed, "Failed", .red),
            (agent(.completed), .completed, "Completed", .grey),
            (agent(.cancelled), .cancelled, "Cancelled", .grey),
        ]
        for (a, state, label, tone) in table {
            XCTAssertEqual(DisplayState(a), state)
            XCTAssertEqual(DisplayState(a).label, label, "\(state)")
            XCTAssertEqual(DisplayState(a).tone, tone, "\(state)")
        }
        XCTAssertEqual(DisplayState.allCases.filter(\.isPausing), [.pauseRequested, .quiescing, .stopping])
        XCTAssertEqual(DisplayState.allCases.filter(\.isPausable), [.spawning, .running, .waiting, .stale])
    }

    // §10.7, one test per row.
    func testQueued() { XCTAssertEqual(labels(agent(nil, agentState: .queued)), ["cancel:Cancel:menu"]) }

    func testSpawning() {
        XCTAssertEqual(labels(agent(.spawning)), ["terminal:Open terminal", "cancel:Cancel:menu"])
        XCTAssertEqual(labels(agent(.spawning), tmux: false), ["terminal:Open terminal:disabled", "cancel:Cancel:menu"])
    }

    func testRunningWaitingAndStale() {
        for a in [agent(.running), agent(.running, waiting: true), agent(.running, stale: true)] {
            XCTAssertEqual(labels(a), ["terminal:Open terminal", "pause:Pause", "cancel:Cancel:menu"])
        }
        XCTAssertEqual(AgentTree.actions(agent(.running), tmuxAlive: true, connected: true)[1].scope, .session)
    }

    func testRunningOrchestratorPausesTheGroup() {
        let o = agent(.running, role: .orchestrator)
        XCTAssertEqual(labels(o), ["terminal:Open terminal", "pause:Pause group", "cancel:Cancel:menu"])
        XCTAssertEqual(AgentTree.actions(o, tmuxAlive: true, connected: true)[1].scope, .subtree)
    }

    func testPausingStatesKeepCancelAvailableUntilStopIsRequested() {
        for s: SessionState in [.pauseRequested, .quiescing] {
            XCTAssertEqual(labels(agent(s)), ["terminal:Open terminal", "pause:Pausing…:disabled", "cancel:Cancel:menu"])
        }
        XCTAssertEqual(labels(agent(.stopping)), ["terminal:Open terminal", "pause:Pausing…:disabled", "cancel:Cancel:disabled:menu"])
    }

    func testPaused() { XCTAssertEqual(labels(agent(.paused)), ["resume:Resume", "cancel:Cancel:menu"]) }

    func testInterrupted() {
        XCTAssertEqual(labels(agent(.interrupted)), ["resume:Resume", "ack:Acknowledge:menu", "cancel:Cancel:menu"])
    }

    func testCrashedAndFailed() {
        for s: SessionState in [.crashed, .failed] {
            XCTAssertEqual(labels(agent(s)), ["retry:Retry", "ack:Acknowledge:menu", "terminal:Open terminal"])
            XCTAssertEqual(labels(agent(s), tmux: false), ["retry:Retry", "ack:Acknowledge:menu"])
        }
    }

    func testFailedAtPreflight() {
        XCTAssertEqual(labels(agent(nil, preflight: "x")), ["retry:Retry", "cancel:Cancel:menu"])
        // Contracts §3.2: no session + preflight_error, whatever AgentState the daemon leaves it in.
        XCTAssertEqual(labels(agent(nil, agentState: .queued, preflight: "x")), ["retry:Retry", "cancel:Cancel:menu"])
    }

    func testCompletedCancelledAcknowledgedHaveNone() {
        XCTAssertEqual(labels(agent(.completed, agentState: .finished)), [])
        XCTAssertEqual(labels(agent(.cancelled, agentState: .finished)), [])
        XCTAssertEqual(labels(agent(.crashed, agentState: .acknowledged)), [])
        XCTAssertEqual(labels(agent(.completed)), [])
        XCTAssertEqual(labels(agent(.cancelled)), [])
    }

    func testDaemonDownDisablesEverythingButTheTerminal() {
        XCTAssertEqual(labels(agent(.running), connected: false),
                       ["terminal:Open terminal", "pause:Pause:disabled", "cancel:Cancel:disabled:menu"])
        XCTAssertEqual(labels(agent(.interrupted), connected: false),
                       ["resume:Resume:disabled", "ack:Acknowledge:disabled:menu", "cancel:Cancel:disabled:menu"])
        XCTAssertEqual(labels(agent(.crashed), connected: false),
                       ["retry:Retry:disabled", "ack:Acknowledge:disabled:menu", "terminal:Open terminal"])
    }

    func testCancellingAnOrchestratorAsksFirst() {
        let child = agent(children: [agent(name: "c2")], name: "c1")
        let o = agent(.running, role: .orchestrator, children: [child, agent(name: "c3")], name: "auth-epic-orchestrator")
        XCTAssertEqual(AgentTree.countLive(o), 3)
        XCTAssertEqual(AgentTree.actions(o, tmuxAlive: true, connected: true).first { $0.endpoint == .cancel }?.confirm,
                       "Cancel auth-epic-orchestrator and its 3 agents?")
        XCTAssertNil(AgentTree.actions(agent(.running, role: .orchestrator), tmuxAlive: true, connected: true)
            .first { $0.endpoint == .cancel }?.confirm)
    }

    func testRowsGroupFinishedAgentsUnderTheirParent() throws {
        let state: StateResponse = try Fixture.decode("state.json")
        let ids = { (rows: [AgentTree.Row]) in rows.map { "\($0.depth) \($0.id) \($0.expanded.map { $0 ? "open" : "closed" } ?? "-")" } }
        // docs-fix-coder (crashed) and search-spike-orchestrator (preflight-failed) are both
        // isFailed, so they land in the root "failed" group, not as bare agent rows (§ failed-spoiler).
        XCTAssertEqual(ids(AgentTree.rows(state.agents, collapsed: [], openFinished: [])), [
            "0 agent:auth-epic-orchestrator open",
            "1 agent:login-form-coder -",
            "1 agent:login-review -",
            "1 finished:auth-epic-orchestrator closed",
            "0 agent:crash-debug-orchestrator -",
            "0 agent:billing-spike-orchestrator -",
            "0 failed: closed",
        ])
        XCTAssertEqual(ids(AgentTree.rows(state.agents, collapsed: [], openFinished: ["auth-epic-orchestrator"]))[3...4],
                       ["1 finished:auth-epic-orchestrator open", "1 agent:session-coder -"])
        XCTAssertEqual(ids(AgentTree.rows(state.agents, collapsed: ["auth-epic-orchestrator"], openFinished: [])).first,
                       "0 agent:auth-epic-orchestrator closed")
        // auth-epic (closed) + crash-debug + billing-spike + one failed-group row (docs-fix-coder,
        // search-spike-orchestrator collapsed into it) = 4, down from the pre-grouping 5.
        XCTAssertEqual(AgentTree.rows(state.agents, collapsed: ["auth-epic-orchestrator"], openFinished: []).count, 4)
        XCTAssertEqual(ids(AgentTree.rows(state.agents, collapsed: ["auth-epic-orchestrator"], openFinished: [], openFailed: [""])).last,
                       "0 agent:search-spike-orchestrator -")

        var roots = state.agents
        roots[1].state = .finished
        let rows = AgentTree.rows(roots, collapsed: ["auth-epic-orchestrator"], openFinished: [""])
        // finished group (crash-debug-orchestrator) comes before the failed group (docs-fix-coder,
        // search-spike-orchestrator), closed by default.
        XCTAssertEqual(ids(rows).suffix(3), ["0 finished: open", "0 agent:crash-debug-orchestrator -", "0 failed: closed"])
        guard case let .finished(parent, count) = rows[rows.count - 3].kind else { return XCTFail("finished row") }
        XCTAssertEqual(parent, "")
        XCTAssertEqual(count, 1)
        guard case let .failed(failedParent, failedCount) = rows[rows.count - 1].kind else { return XCTFail("failed row") }
        XCTAssertEqual(failedParent, "")
        XCTAssertEqual(failedCount, 2)
        XCTAssertEqual(AgentTree.flatten(state.agents).count, 8)
    }

    func testRowsGroupFailedAgentsUnderTheirParentAndBelowFinished() {
        let live = agent(.running, name: "live-coder")
        let crashed = agent(.crashed, name: "crashed-coder")
        let preflightFailed = agent(nil, preflight: "x", name: "preflight-coder")
        let o = agent(.running, role: .orchestrator, children: [live, crashed, preflightFailed], name: "o")
        let ids = { (rows: [AgentTree.Row]) in rows.map { "\($0.depth) \($0.id) \($0.expanded.map { $0 ? "open" : "closed" } ?? "-")" } }

        XCTAssertEqual(ids(AgentTree.rows([o], collapsed: [], openFinished: [])), [
            "0 agent:o open",
            "1 agent:live-coder -",
            "1 failed:o closed",
        ])

        let opened = AgentTree.rows([o], collapsed: [], openFinished: [], openFailed: ["o"])
        XCTAssertEqual(ids(opened), [
            "0 agent:o open",
            "1 agent:live-coder -",
            "1 failed:o open",
            "1 agent:crashed-coder -",
            "1 agent:preflight-coder -",
        ])
        guard case let .failed(parent, count) = opened[2].kind else { return XCTFail("failed row") }
        XCTAssertEqual(parent, "o")
        XCTAssertEqual(count, 2)
    }

    func testSubtitle() throws {
        let state: StateResponse = try Fixture.decode("state.json")
        XCTAssertEqual(AgentTree.subtitle(state.agents[0]), "Orchestrator · EPIC-12")
        XCTAssertEqual(AgentTree.subtitle(state.agents[1]), "Orchestrator · SPIKE-3 · Paused")
        XCTAssertEqual(AgentTree.subtitle(state.agents[0].children[1]), "Reviewer · TASK-101 · Waiting")
        let roles: [Role] = [.coder, .reviewer, .uiReviewer, .researcher, .debugger, .mechanical]
        XCTAssertEqual(roles.map(Copy.roleLabel), ["Coder", "Reviewer", "UI reviewer", "Researcher", "Debugger", "Mechanical"])
    }
}

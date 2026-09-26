import Foundation
import XCTest
@testable import SwarmBarKit

@MainActor
final class AppModelTests: XCTestCase {
    var client: MockDaemonClient!
    var runner = FakeRunner()
    var script = FakeScript()
    var poster = FakePoster()
    var defaults = MemoryStore()
    var cacheURL: URL!
    var opened: [String] = []
    var clock = fixtureNow

    override func setUp() async throws {
        client = try MockDaemonClient(fixtures: Fixture.dir)
        cacheURL = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).appendingPathComponent("state.json")
    }

    private func make(connect: @escaping EventStream.Connect = { _ in throw URLError(.cannotConnectToHost) },
                      sleep: @escaping EventStream.Sleep = { _ in try await Task.sleep(for: .seconds(3600)) },
                      tokenFile: URL = URL(fileURLWithPath: "/nonexistent")) -> AppModel {
        let terminals = Terminals(runner: runner, script: script, ghosttyPIDs: { [] }, fileExists: { $0 == "/opt/homebrew/bin/tmux" })
        return AppModel(client: client, endpoint: DaemonEndpoint(baseURL: URL(string: "http://127.0.0.1:7777")!,
                                                                 tokenFile: tokenFile),
                        terminals: terminals, poster: poster, defaults: defaults, cache: StateCache(url: cacheURL),
                        connect: connect, sleep: sleep,
                        now: { [unowned self] in self.clock }, timeZone: TimeZone(identifier: "UTC")!,
                        openURL: { [unowned self] in self.opened.append($0.absoluteString) })
    }

    func testRefreshFillsTheLabelAndHeader() async {
        let m = make()
        XCTAssertEqual(m.label.count, "", "no bare \"?\" before the first refresh")
        await m.refresh()
        XCTAssertTrue(m.connected)
        XCTAssertNil(m.banner)
        XCTAssertEqual(m.label.count, "4")
        XCTAssertEqual(m.label.segments.map(\.text), ["42%", "18%", "6%"])
        XCTAssertEqual(m.activeLine, "4 active")
        XCTAssertEqual(m.catalog.count, 4)
        XCTAssertEqual(m.usageAgent, .claude)
        XCTAssertEqual(m.usagePicker, [.claude, .codex, .agy])
        XCTAssertEqual(m.usageRows.map(\.label), ["5h", "Weekly (all models)", "Fable weekly"])
    }

    func testDaemonDownKeepsCachedStateAndChecksTmuxDirectly() async {
        let first = make()
        await first.refresh()
        client.stateResult = .failure(.unreachable)
        clock = fixtureNow.addingTimeInterval(600)

        let m = make()
        XCTAssertEqual(m.state.agents.count, 5, "cached state loads at launch")
        runner.status = 0
        await m.refresh()
        XCTAssertFalse(m.connected)
        XCTAssertEqual(m.banner, "Daemon unavailable. Showing last known state from 13:32.")
        XCTAssertEqual(m.label.count, "", "no bare \"?\" while the daemon is down")
        XCTAssertEqual(m.activeLine, "? active")
        XCTAssertEqual(runner.calls.count, 6, "every agent with a session")
        let coder = m.state.agents[0].children[0]
        XCTAssertTrue(m.tmuxAlive(coder))
        XCTAssertEqual(m.actions(coder).map(\.disabled), [false, true, true, true])
        await m.perform(m.actions(coder)[1], on: coder)
        await m.pauseAll()
        await m.readAll()
        await m.refreshUsage()
        XCTAssertEqual(client.calls.filter { $0 != "state" && $0 != "catalog" }, [], "no mutation while down")
        XCTAssertTrue(m.pauseAllDisabled)

        let never = AppModel(client: client, endpoint: m.endpoint, terminals: m.terminals, poster: poster,
                             defaults: defaults, cache: StateCache(url: cacheURL.appendingPathExtension("none")),
                             connect: { _ in throw URLError(.cannotConnectToHost) }, openURL: { _ in })
        await never.refresh()
        XCTAssertEqual(never.banner, "Daemon unavailable.")
    }

    func testRetryConnection() async {
        client.stateResult = .failure(.unreachable)
        let m = make()
        await m.refresh()
        XCTAssertFalse(m.connected)
        client.stateResult = .success(try! Fixture.decode("state.json"))
        await m.retryConnection()
        XCTAssertTrue(m.connected)
        m.stop()
    }

    func testReconnectedStreamLeavesTheDaemonDownState() async throws {
        client.stateResult = .failure(.unreachable)
        let opened = OpenCount()
        let m = make(connect: { _ in
            // The first attempt fails; the second stays open, like a healthy daemon with no events.
            guard opened.next() > 1 else { throw URLError(.cannotConnectToHost) }
            return AsyncThrowingStream { _ in }
        }, sleep: { _ in }, tokenFile: try tempEndpoint().tokenFile)
        await m.start()
        XCTAssertFalse(m.connected)
        client.stateResult = .success(try Fixture.decode("state.json"))
        for _ in 0..<100 where !m.connected {
            try await Task.sleep(for: .milliseconds(20))
        }
        XCTAssertTrue(m.connected, "an open stream refetches state and clears the banner without any event")
        XCTAssertNil(m.banner)
        m.stop()
    }

    func testCancelledRefreshIsNotAnOutage() async {
        let m = make()
        await m.refresh()
        client.failNextWith = CancellationError()
        await m.refresh()
        XCTAssertTrue(m.connected)
    }

    func testSectionsStartOpenExceptNotificationsAndAreRemembered() async {
        let m = make()
        XCTAssertEqual(AppModel.Section.allCases.map(m.isOpen), [true, true, true, false])
        m.setSection(.needsYou, open: false)
        m.setSection(.notifications, open: true)
        let again = make()
        XCTAssertEqual(AppModel.Section.allCases.map(again.isOpen), [false, true, true, true])
    }

    func testNeedsYouOpensOncePerNewRequest() async throws {
        let m = make()
        m.setSection(.needsYou, open: false)
        await m.refresh()
        XCTAssertTrue(m.isOpen(.needsYou), "unseen requests open the section")
        m.setSection(.needsYou, open: false)
        await m.refresh()
        XCTAssertFalse(m.isOpen(.needsYou), "the same requests don't reopen it")

        var s: StateResponse = try Fixture.decode("state.json")
        s.requests.append(SwarmRequest(id: "req_new", kind: .approveSection, prompt: "More?", createdAt: .minutes(-1)))
        client.stateResult = .success(s)
        await m.handle(.requestOpened(s.requests.last!))
        XCTAssertTrue(m.isOpen(.needsYou), "a new approval opens Needs you too, because it's in Needs you now")
        m.setSection(.needsYou, open: false)
        let relaunched = make()
        await relaunched.refresh()
        XCTAssertFalse(relaunched.isOpen(.needsYou), "seen requests survive a relaunch")
    }

    func testNeedsYouRowsAndViewAll() async throws {
        let m = make()

        // A native-pending approval is the one exclusion from Needs you (spec 2.2.1); every other
        // open kind, HITL or not, appears.
        var six: StateResponse = try Fixture.decode("state.json")
        six.requests = [
            SwarmRequest(id: "req_question", kind: .question, isHITL: true, agentName: "login-form-coder",
                         prompt: "Which validation library?", createdAt: Timestamp(ms: 1), terminalAgent: "login-form-coder"),
            SwarmRequest(id: "req_prompt", kind: .prompt, isHITL: true, prompt: "Trust directory", createdAt: Timestamp(ms: 2)),
            SwarmRequest(id: "req_blocker", kind: .blocker, isHITL: true, prompt: "Need token", createdAt: Timestamp(ms: 3)),
            SwarmRequest(id: "req_section", kind: .approveSection, isHITL: false, prompt: "Approve section", createdAt: Timestamp(ms: 4)),
            SwarmRequest(id: "req_epic", kind: .acceptEpic, isHITL: false, prompt: "Accept epic", createdAt: Timestamp(ms: 5)),
            SwarmRequest(id: "req_plan", kind: .approvePlan, isHITL: false, prompt: "Approve plan", createdAt: Timestamp(ms: 6),
                         nativePending: true),
        ]
        client.stateResult = .success(six)
        await m.refresh()
        XCTAssertEqual(m.openRequests.map(\.id), ["req_question", "req_prompt", "req_blocker", "req_section", "req_epic"],
                       "every open request except the native-pending approval")
        XCTAssertEqual(m.label.badge, .yellow, "an open request wins over the active-agent green")
        XCTAssertEqual(m.visibleRequests.map(\.id), ["req_question", "req_prompt", "req_blocker"])
        XCTAssertEqual(m.viewAllRequests, "View all 5 requests")
        XCTAssertEqual(m.visibleRequests.map(RequestLine.text), ["Which validation library?", "Trust directory", "Need token"])
        XCTAssertEqual(m.requestTarget(m.visibleRequests[0]), .terminal("login-form-coder"))

        let lines = [RequestKind.approveReport, .acceptEpic, .acceptFix, .confirmRepos, .closeSpike, .prompt, .blocker]
            .map { RequestLine.text(SwarmRequest(id: "r", kind: $0, prompt: "Sample prompt")) }
        XCTAssertEqual(lines, ["Approve report", "Accept epic", "Accept fix", "Confirm repositories", "Close spike?", "Sample prompt", "Sample prompt"])
        let repos = SwarmRequest(id: "r", kind: .confirmRepos, proposedRepos: 2)
        XCTAssertEqual(RequestLine.text(repos), "Confirm 2 repositories")
        XCTAssertEqual(RequestLine.text(SwarmRequest(id: "r", kind: .approveSection, prompt: "Fallback")), "Approve \"Fallback\"")
        m.review(SwarmRequest(id: "req_section", kind: .approveSection))
        m.openInbox()
        XCTAssertEqual(opened, ["http://127.0.0.1:7777/#/inbox?req=req_section", "http://127.0.0.1:7777/#/inbox"])

        var three: StateResponse = try Fixture.decode("state.json")
        three.requests = [
            SwarmRequest(id: "req_1", kind: .question, isHITL: true, prompt: "Question 1", createdAt: Timestamp(ms: 1)),
            SwarmRequest(id: "req_2", kind: .prompt, isHITL: true, prompt: "Trust directory", createdAt: Timestamp(ms: 2)),
            SwarmRequest(id: "req_3", kind: .blocker, isHITL: true, prompt: "Need token", createdAt: Timestamp(ms: 3)),
        ]
        client.stateResult = .success(three)
        await m.refresh()
        XCTAssertNil(m.viewAllRequests)
    }

    func testNeedsYouRowsAreReadOnlyAndOpenTheOrchestratorTerminal() async {
        let m = make()
        await m.refresh()
        let r = SwarmRequest(id: "req_o", kind: .question, isHITL: true, agentName: "login-form-coder", prompt: "Which?",
                             terminalAgent: "login-form-coder")
        XCTAssertEqual(m.requestTarget(r), .terminal("login-form-coder"))
        await m.openRequest(r)
        XCTAssertEqual(script.sources.count, 1, "the terminal opened")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("answer") || $0.hasPrefix("resolve") }, [], "no answer or resolve call exists")

        // An approval with a terminal_agent targets that terminal too: the isHITL guard is dropped.
        let approval = SwarmRequest(id: "req_a", kind: .approvePlan, isHITL: false, terminalAgent: "login-form-coder")
        XCTAssertEqual(m.requestTarget(approval), .terminal("login-form-coder"))

        // With no terminal_agent, the target is nil and openRequest falls back to the board (review).
        XCTAssertNil(m.requestTarget(SwarmRequest(id: "req_b", kind: .approvePlan, terminalAgent: nil)))
        await m.openRequest(SwarmRequest(id: "req_b", kind: .approvePlan, terminalAgent: nil))
        XCTAssertEqual(opened, ["http://127.0.0.1:7777/#/inbox?req=req_b"])
    }

    func testPromptRowTargetsTheAskingAgentsTerminal() async {
        let m = make()
        await m.refresh()
        let promptReq = SwarmRequest(id: "req_prompt", kind: .prompt, isHITL: true, agentName: "login-form-coder",
                                     prompt: "Trust folder?", options: ["Enter"], terminalAgent: "login-form-coder")
        XCTAssertEqual(m.requestTarget(promptReq), .terminal("login-form-coder"))

        var s = m.state
        s.requests.append(promptReq)
        client.stateResult = .success(s)
        await m.refresh()
        XCTAssertTrue(m.state.requests.contains { $0.id == "req_prompt" })

        await m.openRequest(promptReq)
        XCTAssertEqual(script.sources.count, 1, "the asking agent's terminal opened")
        XCTAssertTrue(m.state.requests.contains { $0.id == "req_prompt" }, "opening the terminal never resolves the row")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("resolve") || $0.hasPrefix("approve") || $0.hasPrefix("answer") }, [],
                       "there is no Approve call")
    }

    func testRequestTargetExplainsAPausedInterruptedOrMissingOrchestrator() async {
        let m = make()
        await m.refresh()
        func target(_ agent: String?, hitl: Bool = true) -> AppModel.RequestTarget? {
            m.requestTarget(SwarmRequest(id: "r", kind: .question, isHITL: hitl, terminalAgent: agent))
        }
        XCTAssertEqual(target("crash-debug-orchestrator"), .unavailable("Orchestrator is paused. Resume it to continue."))
        XCTAssertEqual(target("search-spike-orchestrator"), .unavailable("Orchestrator isn't running."), "no session")
        XCTAssertEqual(target("no-such-agent"), .unavailable("Orchestrator isn't running."))
        XCTAssertNil(target(nil), "no terminal agent, no target")
        XCTAssertEqual(target("login-form-coder", hitl: false), .terminal("login-form-coder"),
                       "an approval with a terminal agent targets it too (isHITL guard dropped)")
        await m.openRequest(SwarmRequest(id: "r", kind: .question, isHITL: true, terminalAgent: "crash-debug-orchestrator"))
        XCTAssertEqual(script.sources.count, 0, "an unavailable target opens nothing")

        var s = m.state
        s.agents[1].session?.state = .interrupted
        client.stateResult = .success(s)
        await m.refresh()
        XCTAssertEqual(target("crash-debug-orchestrator"), .unavailable("Orchestrator is paused. Resume it to continue."),
                       "interrupted reads as paused")
        s.agents[1].session?.state = .crashed
        client.stateResult = .success(s)
        await m.refresh()
        XCTAssertEqual(target("crash-debug-orchestrator"), .unavailable("Orchestrator isn't running."))
    }

    func testAgentRowsAndActions() async {
        let m = make()
        await m.refresh()
        // docs-fix-coder (crashed) and search-spike-orchestrator (preflight-failed) collapse into
        // one root "Failed (n)" row instead of two bare agent rows (§ failed-spoiler), so every
        // count here is one less than before that grouping existed.
        XCTAssertEqual(m.agentRows.count, 7)
        m.toggleAgent("auth-epic-orchestrator")
        XCTAssertEqual(m.agentRows.count, 4)
        m.toggleAgent("auth-epic-orchestrator")
        m.toggleFinished("auth-epic-orchestrator")
        XCTAssertEqual(m.agentRows.count, 8)
        m.toggleFinished("auth-epic-orchestrator")
        XCTAssertEqual(m.agentRows.count, 7)

        let orch = m.state.agents[0]
        let pause = m.actions(orch)[1]
        XCTAssertEqual(pause.label, "Pause group")
        await m.perform(pause, on: orch)
        let paused = m.state.agents[1]
        await m.perform(m.actions(paused)[0], on: paused)
        let crashed = m.state.agents[2]
        XCTAssertEqual(m.actions(crashed).map(\.label), ["Retry", "Acknowledge", "Open terminal", "Handoff"])
        await m.perform(m.actions(crashed)[1], on: crashed)
        await m.perform(m.actions(orch)[0], on: orch)
        XCTAssertEqual(script.sources.count, 1, "terminal opens through Ghostty, not the daemon")
        await m.perform(AgentAction(endpoint: .pause, label: "Pausing…", disabled: true, placement: .button), on: orch)
        client.failNext = .api(status: 409, code: "conflict", message: "Still stopping. Try again in a few seconds.")
        await m.perform(m.actions(paused)[0], on: paused)
        XCTAssertEqual(m.actionError, "Still stopping. Try again in a few seconds.")
        await m.perform(m.actions(paused)[0], on: paused)
        XCTAssertNil(m.actionError)
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("agent") }, [
            "agent pause auth-epic-orchestrator subtree", "agent resume crash-debug-orchestrator",
            "agent ack docs-fix-coder", "agent resume crash-debug-orchestrator", "agent resume crash-debug-orchestrator",
        ])
    }

    /// Starts `perform` and returns once the mock has the request parked in flight.
    private func performInFlight(_ m: AppModel, _ action: AgentAction, on agent: AgentNode) async -> Task<Void, Never> {
        client.holdAgent = true
        let before = client.calls.count
        let task = Task { await m.perform(action, on: agent) }
        for _ in 0..<200 where client.calls.count == before { await Task.yield() }
        XCTAssertGreaterThan(client.calls.count, before, "the request never reached the daemon")
        return task
    }

    func testPauseInFlightDisablesTheButtonUntilTheRefreshLands() async {
        let m = make()
        await m.refresh()
        let orch = m.state.agents[0]
        let before = m.actions(orch)
        XCTAssertEqual(before.map(\.label), ["Open terminal", "Pause group", "Cancel", "Handoff"])
        XCTAssertTrue(m.inFlight.isEmpty)

        let task = await performInFlight(m, before[1], on: orch)
        XCTAssertEqual(m.inFlight[orch.name], .pause)
        let during = m.actions(orch)
        XCTAssertEqual(during.map(\.endpoint), before.map(\.endpoint))
        XCTAssertEqual(during[1].label, Copy.pausing)
        XCTAssertTrue(during[1].disabled)
        XCTAssertEqual([during[0], during[2]], [before[0], before[2]], "the other actions are unchanged")
        XCTAssertEqual(during[1].scope, before[1].scope)

        client.releaseAgent()
        await task.value
        XCTAssertTrue(m.inFlight.isEmpty)
        XCTAssertEqual(m.actions(orch).map(\.label), before.map(\.label))
    }

    func testResumeInFlightDisablesTheButton() async {
        let m = make()
        await m.refresh()
        let paused = m.state.agents[1]
        let before = m.actions(paused)
        XCTAssertEqual(before[0].endpoint, .resume)
        XCTAssertFalse(before[0].disabled)

        let task = await performInFlight(m, before[0], on: paused)
        XCTAssertEqual(m.inFlight[paused.name], .resume)
        let during = m.actions(paused)
        XCTAssertEqual(during[0].label, Copy.resuming)
        XCTAssertEqual(Copy.resuming, "Resuming…")
        XCTAssertTrue(during[0].disabled)
        XCTAssertEqual(Array(during.dropFirst()), Array(before.dropFirst()))

        client.releaseAgent()
        await task.value
        XCTAssertTrue(m.inFlight.isEmpty)
    }

    func testSecondPauseWhileInFlightIsANoOp() async {
        let m = make()
        await m.refresh()
        let orch = m.state.agents[0]
        let pause = m.actions(orch)[1]
        let task = await performInFlight(m, pause, on: orch)
        await m.perform(pause, on: orch) // a stale, still-enabled copy of the button
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("agent pause") }.count, 1)
        client.releaseAgent()
        await task.value
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("agent pause") }.count, 1)
    }

    func testInFlightIsPerAgent() async {
        let m = make()
        await m.refresh()
        let orch = m.state.agents[0], paused = m.state.agents[1]
        let task = await performInFlight(m, m.actions(orch)[1], on: orch)
        XCTAssertEqual(m.actions(paused)[0].label, Copy.resume, "another agent's buttons are untouched")
        XCTAssertFalse(m.actions(paused)[0].disabled)
        client.releaseAgent()
        await task.value
    }

    func testFailedPauseClearsInFlightAndShowsTheError() async {
        let m = make()
        await m.refresh()
        let orch = m.state.agents[0]
        client.failNext = .api(status: 409, code: "conflict", message: "Still stopping. Try again in a few seconds.")
        await m.perform(m.actions(orch)[1], on: orch)
        XCTAssertTrue(m.inFlight.isEmpty)
        XCTAssertEqual(m.actionError, "Still stopping. Try again in a few seconds.")
        XCTAssertEqual(m.actions(orch)[1].label, "Pause group")
        XCTAssertFalse(m.actions(orch)[1].disabled)
    }

    func testOtherActionsAreNotTracked() async {
        let m = make()
        await m.refresh()
        let crashed = m.state.agents[2]
        let ack = m.actions(crashed)[1]
        XCTAssertEqual(ack.endpoint, .ack)
        client.holdAgent = true
        let before = client.calls.count
        let task = Task { await m.perform(ack, on: crashed) }
        for _ in 0..<200 where client.calls.count == before { await Task.yield() }
        XCTAssertTrue(m.inFlight.isEmpty)
        client.releaseAgent()
        await task.value
    }

    func testPauseAllStates() async throws {
        let m = make()
        await m.refresh()
        XCTAssertEqual(m.pauseAllLabel, "Pause all")
        XCTAssertFalse(m.pauseAllDisabled)

        var pausing: StateResponse = try Fixture.decode("state.json")
        pausing.agents[0].session?.state = .pauseRequested
        client.stateResult = .success(pausing)
        await m.pauseAll()
        XCTAssertEqual(m.pauseAllLabel, "Pausing…")
        XCTAssertTrue(m.pauseAllDisabled)
        await m.pauseAll()
        XCTAssertEqual(client.calls.filter { $0 == "pause-all" }.count, 1)

        var settled = pausing
        settled.agents[0].session?.state = .paused
        settled.agents[0].children = []
        settled.agents[2].session = nil
        settled.agents[2].state = .acknowledged
        client.stateResult = .success(settled)
        await m.refresh()
        XCTAssertEqual(m.pauseAllLabel, "Pause all")
        XCTAssertTrue(m.pauseAllDisabled, "nothing is running")

        let fresh = make()
        client.stateResult = .success(try Fixture.decode("state.json"))
        await fresh.refresh()
        client.failNext = .unreachable
        await fresh.pauseAll()
        XCTAssertFalse(fresh.pauseAllPending)
    }

    func testUsageSettingsAndCatalogEventsRefetchInsteadOfPatching() async throws {
        let m = make()
        await m.refresh()
        var fresh: StateResponse = try Fixture.decode("state.json")
        fresh.usage[1].meters[0].usedPct = 55
        client.stateResult = .success(fresh)
        var hint = m.state.usage[1]
        hint.meters[0].usedPct = 99
        await m.handle(.usage(hint))
        XCTAssertEqual(m.label.segments[1].text, "55%", "the daemon's state wins over the event payload")
        m.usageAgent = .codex
        XCTAssertEqual(m.usageRows.first?.used, "55% used")
        await m.refreshUsage()
        XCTAssertEqual(client.calls.last, "usage-refresh codex")

        fresh.settings.enabledAgents = [.agy]
        client.stateResult = .success(fresh)
        await m.handle(.settings(Settings.defaults))
        XCTAssertEqual(m.usageAgent, .agy)
        XCTAssertEqual(m.label.segments.map(\.agent), [.agy])
        client.catalogEntries = []
        await m.handle(.catalog(try Fixture.decode("catalog.json")))
        XCTAssertEqual(m.catalog, [])
    }

    func testNotificationsSectionAndEvents() async throws {
        let m = make()
        await m.refresh()
        XCTAssertEqual(m.notificationsTitle, "Notifications (4 unread)")
        XCTAssertEqual(m.visibleNotifications.count, 5)
        XCTAssertFalse(m.showViewAllNotifications)

        var many: StateResponse = try Fixture.decode("state.json")
        many.notifications.items += many.notifications.items.map { var n = $0; n.id += "b"; return n }
        client.stateResult = .success(many)
        await m.refresh()
        XCTAssertEqual(m.visibleNotifications.count, 5)
        XCTAssertTrue(m.showViewAllNotifications)
        await m.viewAllNotifications()
        XCTAssertEqual(m.visibleNotifications.count, 5, "the mock serves the fixture list")
        XCTAssertFalse(m.showViewAllNotifications)
        await m.readAll()
        XCTAssertTrue(m.showViewAllNotifications)
        XCTAssertTrue(client.calls.contains("read-all"))

        await m.open(m.visibleNotifications[0])
        await m.open(m.visibleNotifications[3])
        await m.open(m.visibleNotifications[4])
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("read ") }, ["read ntf_05", "read ntf_02"], "already-read ones aren't marked again")
        XCTAssertEqual(opened, ["http://127.0.0.1:7777/#/inbox?req=req_question", "http://127.0.0.1:7777/#/hierarchy?item=EPIC-12",
                                "http://127.0.0.1:7777/#/hierarchy?item=TASK-101"])

        let n = many.notifications.items[0]
        await m.handle(.notification(n))
        XCTAssertEqual(poster.posted.map(\.id), ["ntf_05"])
        await m.handleNotificationAction("open_orchestrator", userInfo: ["request": "req_question"], text: nil)
        XCTAssertEqual(script.sources.count, 1, "the question's terminal opened")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("answer") }, [], "a notification never answers")
        await m.handleNotificationAction("open_terminal", userInfo: ["agent": "login-form-coder"], text: nil)
        await m.handleNotificationAction("review", userInfo: ["request": "req_plan"], text: nil)
        XCTAssertEqual(script.sources.count, 2)
        XCTAssertEqual(opened.last, "http://127.0.0.1:7777/#/inbox?req=req_plan")
    }

    /// The "View all" list is its own fetch, separate from `/api/state`'s trimmed `notifications.items`
    /// (the mock even serves it from a different fixture). Once it's expanded, a plain `refresh()` —
    /// not just another "View all" click — must keep it current, or it freezes until "Read all". And
    /// once the section collapses, the cached expanded list must drop, so reopening it never shows a
    /// stale snapshot instead of a fresh one.
    func testExpandedNotificationsRefreshOnStateRefreshAndDropWhenCollapsed() async throws {
        let m = make()
        m.setSection(.notifications, open: true)
        await m.refresh()
        await m.viewAllNotifications()
        XCTAssertEqual(m.visibleNotifications.count, 5)

        client.notificationList.append(SwarmNotification(id: "ntf_new", level: .info, kind: "item.created", createdAt: .minutes(-1)))
        await m.refresh()
        XCTAssertEqual(m.visibleNotifications.count, 6, "a plain refresh must keep the expanded list current, not just \"View all\" again")

        m.setSection(.notifications, open: false)
        await m.refresh()
        XCTAssertEqual(m.visibleNotifications.count, 5, "collapsing drops the cached expanded list, falling back to the state's own trimmed 5")
    }

    /// A request notification (its action button or a banner tap) opens the terminal the daemon named for the
    /// request, and does nothing the user could mistake for an answer.
    func testRequestNotificationActionOpensTheOrchestratorTerminal() async {
        let m = make()
        await m.refresh()
        await m.handleNotificationAction("open_orchestrator",
                                         userInfo: ["kind": "request.question", "request": "req_question"], text: nil)
        XCTAssertEqual(script.sources.count, 1, "the request's terminal_agent opened")

        await m.handleNotificationAction(NotificationAction.default,
                                         userInfo: ["kind": "request.question", "request": "req_question"], text: nil)
        XCTAssertEqual(script.sources.count, 2, "a banner tap does the same")

        await m.handleNotificationAction("open_orchestrator",
                                         userInfo: ["kind": "request.question", "request": "req_gone"], text: nil)
        XCTAssertEqual(script.sources.count, 2, "a request that is no longer open opens nothing")
        XCTAssertTrue(opened.isEmpty, "the board is not opened for a question")

        await m.handleNotificationAction(NotificationAction.default,
                                         userInfo: ["kind": "request.approve_plan", "request": "req_plan"], text: nil)
        XCTAssertEqual(script.sources.count, 2, "approvals never open a terminal")
        XCTAssertEqual(opened, ["http://127.0.0.1:7777/#/inbox?req=req_plan"], "approvals still open the board")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("answer") || $0.hasPrefix("resolve") }, [])
    }

    func testTerminalOpenEventRepliesToTheDaemon() async {
        let m = make()
        await m.handle(.terminalOpen(TerminalOpen(name: "login-form-coder", tmux: "login-form-coder")))
        XCTAssertEqual(script.sources.count, 1)
        XCTAssertEqual(client.calls, ["terminal-opened login-form-coder"])
        script.reply = .failure(Denied())
        await m.handle(.terminalOpen(TerminalOpen(name: "login-review", tmux: "login-review")))
        XCTAssertEqual(runner.calls.last, "/usr/bin/open -na Ghostty --args -e /opt/homebrew/bin/tmux -L swarm attach -t =login-review")
        XCTAssertEqual(client.calls.last, "terminal-opened login-review", "the menubar handled it, so the daemon skips its fallback")
        await m.handle(.reset)
        await m.handle(.changed("item.changed"))
        XCTAssertEqual(client.calls.filter { $0 == "state" }.count, 2)
    }

    func testCompactModeSwitchesOnItsOwnAndPersists() async {
        let m = make()
        await m.refresh()
        m.labelVisible(true)
        XCTAssertFalse(m.compact)
        m.labelVisible(false)
        XCTAssertTrue(m.compact)
        XCTAssertEqual(m.label.segments.map(\.text), ["", "", ""])
        XCTAssertEqual(m.compactNote, "Switched to compact to fit the menu bar.")
        m.popoverShown()
        XCTAssertNil(m.compactNote)
        m.popoverShown()
        let again = make()
        await again.refresh()
        XCTAssertTrue(again.compact, "the override survives a relaunch")

        let settings = again.makeSettings()
        XCTAssertTrue(settings.compact)
        await settings.setCompact(false)
        XCTAssertFalse(again.compact)
        again.labelVisible(false)
        XCTAssertFalse(again.compact, "no automatic switch right after the user turned it off")
    }

    func testWindowFactoriesAndBoard() async {
        let m = make()
        await m.refresh()
        let form = m.makeNewOrchestratorForm()
        XCTAssertTrue(form.connected)
        XCTAssertEqual(form.startLabel, "Queue orchestrator")
        m.openBoard()
        XCTAssertEqual(opened, ["http://127.0.0.1:7777/"])
        await m.start()
        XCTAssertTrue(poster.authorized)
        m.stop()
    }

    func testStateCacheRoundTrip() throws {
        let cache = StateCache(url: cacheURL)
        XCTAssertNil(cache.load())
        let s: StateResponse = try Fixture.decode("state.json")
        cache.save(s, at: fixtureNow)
        let loaded = try XCTUnwrap(cache.load())
        XCTAssertEqual(loaded.0, s)
        XCTAssertEqual(loaded.1, fixtureNow)
    }

    func testNeedsYouRowIsGenericAndNeverShowsThePrompt() {
        XCTAssertEqual(Copy.needsYouMessage, "Waiting for your input")
        XCTAssertEqual(Copy.openAgentTerminal, "Open agent terminal")
        XCTAssertEqual(Copy.openOnBoard, "Open in Swarm board")

        let withAgent = SwarmRequest(id: "r", kind: .question, agentName: "go-migration-agent-debug",
                                      itemKey: "SPIKE-16", itemTitle: "go-migration-agent-debug", prompt: "SECRET")
        XCTAssertEqual(NeedsYouRow.lines(withAgent),
                       ["SPIKE-16 · go-migration-agent-debug", "go-migration-agent-debug", "Waiting for your input"])

        let noAgent = SwarmRequest(id: "r", kind: .acceptEpic, prompt: "SECRET")
        XCTAssertEqual(NeedsYouRow.lines(noAgent)[1], "—")

        for kind in RequestKind.allCases {
            let r = SwarmRequest(id: "r", kind: kind, prompt: "SECRET")
            XCTAssertFalse(NeedsYouRow.lines(r).contains(r.prompt), "\(kind) leaked the prompt")
        }
    }
}

/// Counts stream connection attempts from the EventStream task.
final class OpenCount: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func next() -> Int { lock.withLock { n += 1; return n } }
}

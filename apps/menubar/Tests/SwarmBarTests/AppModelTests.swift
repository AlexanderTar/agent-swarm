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
        XCTAssertEqual(m.label.count, "?")
        await m.refresh()
        XCTAssertTrue(m.connected)
        XCTAssertNil(m.banner)
        XCTAssertEqual(m.label.count, "4")
        XCTAssertEqual(m.label.segments.map(\.text), ["42%", "18%", "63%"])
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
        XCTAssertEqual(m.label.count, "?")
        XCTAssertEqual(m.activeLine, "? active")
        XCTAssertEqual(runner.calls.count, 6, "every agent with a session")
        let coder = m.state.agents[0].children[0]
        XCTAssertTrue(m.tmuxAlive(coder))
        XCTAssertEqual(m.actions(coder).map(\.disabled), [false, true, true])
        await m.perform(m.actions(coder)[1], on: coder)
        await m.pauseAll()
        await m.readAll()
        await m.refreshUsage()
        m.answerDrafts["req_question"] = "zod"
        await m.sendAnswer("req_question")
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
        s.requests.append(SwarmRequest(id: "req_new", kind: .question, prompt: "More?", createdAt: .minutes(-1)))
        client.stateResult = .success(s)
        await m.handle(.requestOpened(s.requests.last!))
        XCTAssertTrue(m.isOpen(.needsYou))
        m.setSection(.needsYou, open: false)
        let relaunched = make()
        await relaunched.refresh()
        XCTAssertFalse(relaunched.isOpen(.needsYou), "seen requests survive a relaunch")
    }

    func testNeedsYouRowsAndViewAll() async throws {
        let m = make()
        await m.refresh()
        XCTAssertEqual(m.visibleRequests.map(\.id), ["req_section", "req_question", "req_plan"])
        XCTAssertEqual(m.viewAllRequests, "View all 4 requests")
        XCTAssertEqual(m.visibleRequests.map(RequestLine.text), [
            "Approve \"Session handling\"", "Which validation library?", "Approve plan",
        ])
        let lines = [RequestKind.approveReport, .acceptEpic, .acceptFix, .confirmRepos, .closeSpike]
            .map { RequestLine.text(SwarmRequest(id: "r", kind: $0)) }
        XCTAssertEqual(lines, ["Approve report", "Accept epic", "Accept fix", "Confirm repositories", "Close spike?"])
        let repos = try XCTUnwrap(m.openRequests.last)
        XCTAssertEqual(RequestLine.text(repos), "Confirm 2 repositories")
        XCTAssertEqual(RequestLine.text(SwarmRequest(id: "r", kind: .approveSection, prompt: "Fallback")), "Approve \"Fallback\"")
        XCTAssertEqual(m.requestTerminal(m.visibleRequests[1]), "login-form-coder")
        XCTAssertNil(m.requestTerminal(m.visibleRequests[0]), "approval rows show Review only")
        m.review(m.visibleRequests[0])
        m.openInbox()
        XCTAssertEqual(opened, ["http://127.0.0.1:7777/#/inbox?req=req_section", "http://127.0.0.1:7777/#/inbox"])

        var three: StateResponse = try Fixture.decode("state.json")
        three.requests.removeLast()
        client.stateResult = .success(three)
        await m.refresh()
        XCTAssertNil(m.viewAllRequests)
    }

    func testAnswerInline() async {
        let m = make()
        await m.refresh()
        m.answering = "req_question"
        m.answerDrafts["req_question"] = "  Use zod. "
        await m.sendAnswer("req_question")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("answer") }, ["answer req_question Use zod."])
        XCTAssertNil(m.answering)
        XCTAssertNil(m.answerDrafts["req_question"])
        await m.sendAnswer("req_question")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("answer") }.count, 1, "empty answers aren't sent")
        m.answerDrafts["req_question"] = "again"
        client.failNext = .api(status: 409, code: "conflict", message: "Already resolved.")
        await m.sendAnswer("req_question")
        XCTAssertEqual(m.actionError, "Already resolved.")
        XCTAssertEqual(m.answerDrafts["req_question"], "again", "unsent text is kept")
    }

    func testAgentRowsAndActions() async {
        let m = make()
        await m.refresh()
        XCTAssertEqual(m.agentRows.count, 8)
        m.toggleAgent("auth-epic-orchestrator")
        XCTAssertEqual(m.agentRows.count, 5)
        m.toggleAgent("auth-epic-orchestrator")
        m.toggleFinished("auth-epic-orchestrator")
        XCTAssertEqual(m.agentRows.count, 9)
        m.toggleFinished("auth-epic-orchestrator")
        XCTAssertEqual(m.agentRows.count, 8)

        let orch = m.state.agents[0]
        let pause = m.actions(orch)[1]
        XCTAssertEqual(pause.label, "Pause group")
        await m.perform(pause, on: orch)
        let paused = m.state.agents[1]
        await m.perform(m.actions(paused)[0], on: paused)
        let crashed = m.state.agents[2]
        XCTAssertEqual(m.actions(crashed).map(\.label), ["Retry", "Acknowledge", "Open terminal"])
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
        await m.handleNotificationAction("answer", userInfo: ["request": "req_question"], text: "zod")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("answer") }, ["answer req_question zod"])
        await m.handleNotificationAction("open_terminal", userInfo: ["agent": "login-form-coder"], text: nil)
        await m.handleNotificationAction("review", userInfo: ["request": "req_plan"], text: nil)
        XCTAssertEqual(script.sources.count, 1)
        XCTAssertEqual(opened.last, "http://127.0.0.1:7777/#/inbox?req=req_plan")
    }

    /// Carry-in: a failed answer's own notification comes back with `kind: "answer.failed"`; before the
    /// popover reopens (whichever action fired it), the typed text must already be sitting in the draft,
    /// or a retry from Notification Center loses it a second time.
    func testAnswerFailedNotificationActionSeedsTheDraft() async {
        let m = make()
        await m.handleNotificationAction(NotificationAction.default,
                                         userInfo: ["kind": "answer.failed", "request": "req_question", "text": "Use zod."],
                                         text: nil)
        XCTAssertEqual(m.answerDrafts["req_question"], "Use zod.")

        // A normal delivered notification (no "answer.failed" kind) never touches the drafts.
        m.answerDrafts["req_plan"] = nil
        await m.handleNotificationAction(NotificationAction.default, userInfo: ["kind": "request.question", "request": "req_plan"], text: nil)
        XCTAssertNil(m.answerDrafts["req_plan"])
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
}

/// Counts stream connection attempts from the EventStream task.
final class OpenCount: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func next() -> Int { lock.withLock { n += 1; return n } }
}

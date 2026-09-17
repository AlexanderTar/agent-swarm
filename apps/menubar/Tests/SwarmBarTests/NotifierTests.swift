import Foundation
import XCTest
@testable import SwarmBarKit

@MainActor
final class FakePoster: NotificationPosting {
    var posted: [PostedNotification] = []
    var registered: [NotificationCategorySpec] = []
    var authorized = false
    func requestAuthorization() async { authorized = true }
    func register(_ categories: [NotificationCategorySpec]) { registered = categories }
    func post(_ notification: PostedNotification) async { posted.append(notification) }
}

@MainActor
final class NotifierTests: XCTestCase {
    var poster = FakePoster()
    var client = MockDaemonClient()
    var opened: [String] = []
    var terminals: [String] = []

    private func make() -> Notifier {
        Notifier(poster: poster, client: client,
                 openBoard: { [unowned self] in self.opened.append($0) },
                 openTerminal: { [unowned self] in self.terminals.append($0) })
    }

    func testCategoriesAndActions() async {
        let n = make()
        await n.start()
        XCTAssertTrue(poster.authorized)
        XCTAssertEqual(poster.registered.map(\.id), ["swarm.info", "swarm.agent", "swarm.question", "swarm.approval", "swarm.item"])
        XCTAssertEqual(poster.registered.map { $0.actions.map(\.title) }, [
            ["Open item"], ["Open terminal", "View agent"], ["Answer", "Open terminal"], ["Review"], ["Start orchestrator", "Open item"],
        ])
        XCTAssertEqual(poster.registered[2].actions.map(\.textInput), [true, false])
    }

    func testEveryKindMapsToItsCategory() {
        let table: [String: String] = [
            "agent.accepted": "swarm.info", "item.completed": "swarm.info", "item.created": "swarm.item",
            "agent.queued": "swarm.info", "agent.paused": "swarm.agent", "agent.interrupted": "swarm.agent",
            "agent.retried": "swarm.info", "agent.failed": "swarm.agent", "agent.crashed": "swarm.agent",
            "agent.stale": "swarm.agent", "agent.undeliverable": "swarm.agent", "agent.preflight_failed": "swarm.info",
            "worktree.retained": "swarm.info", "tmux.unknown": "swarm.info",
            "request.confirm_repos": "swarm.approval", "request.close_spike": "swarm.approval",
            "request.question": "swarm.question", "request.approve_section": "swarm.approval",
            "request.approve_plan": "swarm.approval", "request.approve_report": "swarm.approval",
            "request.accept_epic": "swarm.approval", "request.accept_fix": "swarm.approval",
            "something.new": "swarm.info",
        ]
        for (kind, category) in table {
            XCTAssertEqual(Notifier.category(forKind: kind), category, kind)
        }
    }

    func testDeliveryFollowsTheLevelSettings() async throws {
        let items: [SwarmNotification] = try Fixture.decode("notifications.json")
        var settings: Settings = try Fixture.decode("settings.json")
        let n = make()
        for item in items { await n.deliver(item, settings: settings) }
        XCTAssertEqual(poster.posted.map(\.id), ["ntf_05", "ntf_04", "ntf_03", "ntf_02", "ntf_01"])
        XCTAssertEqual(poster.posted.map(\.sound), [true, true, true, false, false], "info has sound off in the fixture")
        XCTAssertEqual(poster.posted[0], PostedNotification(
            id: "ntf_05", title: "Answer needed", body: "TASK-101: Which validation library?", category: "swarm.question",
            sound: true, userInfo: ["id": "ntf_05", "kind": "request.question", "agent": "login-form-coder",
                                    "item": "TASK-101", "request": "req_question"]))
        XCTAssertEqual(poster.posted[3].userInfo, ["id": "ntf_02", "kind": "item.created", "item": "EPIC-12"])

        settings.notifications["info"] = NotifyPref(center: false, sound: true)
        poster.posted = []
        for item in items { await n.deliver(item, settings: settings) }
        XCTAssertEqual(poster.posted.map(\.id), ["ntf_05", "ntf_04", "ntf_03"])
    }

    func testAnswerActionPostsTheAnswer() async {
        let n = make()
        await n.handle(action: "answer", userInfo: ["request": "req_question"], text: "Use zod.")
        await n.handle(action: "answer", userInfo: ["request": "req_question"], text: "   ")
        await n.handle(action: "answer", userInfo: [:], text: "lost")
        XCTAssertEqual(client.calls, ["answer req_question Use zod."])
    }

    func testOtherActionsOpenTheBoardOrTerminal() async {
        let n = make()
        await n.handle(action: "open_terminal", userInfo: ["agent": "login-form-coder"], text: nil)
        await n.handle(action: "open_terminal", userInfo: [:], text: nil)
        await n.handle(action: "review", userInfo: ["request": "req_section"], text: nil)
        await n.handle(action: "review", userInfo: [:], text: nil)
        await n.handle(action: "open_item", userInfo: ["item": "EPIC-12"], text: nil)
        await n.handle(action: "view_agent", userInfo: ["item": "TASK-101", "agent": "x"], text: nil)
        await n.handle(action: "start_orchestrator", userInfo: ["item": "EPIC-12"], text: nil)
        await n.handle(action: NotificationAction.default, userInfo: ["request": "req_plan", "item": "SPIKE-3"], text: nil)
        await n.handle(action: NotificationAction.default, userInfo: ["item": "TASK-7"], text: nil)
        await n.handle(action: NotificationAction.default, userInfo: [:], text: nil)
        await n.handle(action: "open_item", userInfo: [:], text: nil)
        XCTAssertEqual(terminals, ["login-form-coder"])
        XCTAssertEqual(opened, [
            "/inbox?req=req_section", "/hierarchy?item=EPIC-12", "/hierarchy?item=TASK-101", "/hierarchy?item=EPIC-12",
            "/inbox?req=req_plan", "/hierarchy?item=TASK-7", "", "",
        ])
        XCTAssertEqual(client.calls, [], "no approval or other mutation from a notification")
    }
}

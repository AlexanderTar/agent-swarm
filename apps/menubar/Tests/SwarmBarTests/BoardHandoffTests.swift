import XCTest
@testable import SwarmBarKit

@MainActor
final class BoardHandoffTests: XCTestCase {
    var client: MockDaemonClient!
    var state = StateResponse()
    var catalog: [AgentCatalogEntry] = []

    override func setUp() async throws {
        client = try MockDaemonClient(fixtures: Fixture.dir)
        state = try Fixture.decode("state.json")
        catalog = try Fixture.decode("catalog.json")
    }

    private func form(preselect: String? = nil, connected: Bool = true, max: Int? = nil) async -> BoardHandoffForm {
        var settings = state.settings
        if let max { settings.maxConcurrentAgents = max }
        let f = BoardHandoffForm(client: client, settings: settings, agents: state.agents, connected: connected, preselectAgent: preselect)
        await f.load()
        return f
    }

    func testRowsEligibilityAndOrdering() {
        let rows = BoardHandoffRules.rows(client.boardItemList, agents: state.agents)
        XCTAssertEqual(rows.map(\.id), ["SPIKE-4", "EPIC-12", "BUG-7", "CHORE-3"],
                       "live-orchestrator rows first, then Ready; daemon order within; no story/task/child/done/in-progress-without-orchestrator/unknown type")
        XCTAssertEqual(rows[1].orchestrator?.name, "auth-epic-orchestrator")
        XCTAssertNil(rows[2].orchestrator)
    }

    func testDraftChoreIsListedAfterOrchestratedRows() {
        let items = client.boardItemList + [
            BoardItem(key: "CHORE-9", type: "chore", status: "draft", title: "Tidy"),
            BoardItem(key: "CHORE-10", type: "chore", status: "done", title: "Old"),
            BoardItem(key: "CHORE-11", type: "chore", status: "cancelled", title: "Nope"),
            BoardItem(key: "BUG-30", type: "bug", status: "in_progress", title: "Busy"),
            BoardItem(key: "CHORE-12", type: "chore", status: "draft", title: "Child", parentKey: "EPIC-12"),
        ]
        let rows = BoardHandoffRules.rows(items, agents: state.agents)
        XCTAssertEqual(rows.map(\.id), ["SPIKE-4", "EPIC-12", "BUG-7", "CHORE-3", "CHORE-9"])
        XCTAssertEqual(BoardHandoffRules.detail(rows[4], catalog: catalog), "Chore · Draft")
    }

    func testDraftItemStartsLikeReady() async {
        client.boardItemList = [BoardItem(key: "CHORE-9", type: "chore", status: "draft", title: "Tidy")]
        let f = await form(max: 99)
        XCTAssertEqual(f.selectedKey, "CHORE-9")
        XCTAssertEqual(f.primaryLabel, Copy.startOrchestrator)
    }

    func testFinishedOrchestratorDoesNotCount() {
        var agents = state.agents
        agents[0].state = .finished // auth-epic-orchestrator
        XCTAssertFalse(BoardHandoffRules.rows(client.boardItemList, agents: agents).contains { $0.id == "EPIC-12" })
    }

    func testTitleAndDetail() {
        let rows = BoardHandoffRules.rows(client.boardItemList, agents: state.agents)
        XCTAssertEqual(BoardHandoffRules.title(rows[1]), "EPIC-12 · Authentication")
        let o = rows[1].orchestrator!
        let entry = CatalogRules.entry(catalog, o.kind)
        let parts = [o.name, CatalogRules.modelLabel(entry, o.model), CatalogRules.previewEffortLabel(entry, o.model, o.effort),
                     AgentTree.handoffStatus(o) ?? DisplayState(o).label ?? Copy.runningLabel].compactMap { $0 }
        XCTAssertEqual(BoardHandoffRules.detail(rows[1], catalog: catalog), parts.joined(separator: " · "))
        XCTAssertEqual(BoardHandoffRules.detail(rows[2], catalog: catalog), "Bug · Ready")
        XCTAssertEqual(BoardHandoffRules.detail(rows[3], catalog: catalog), "Chore · Ready")
    }

    func testDetailOmitsUnknownEffort() {
        var o = AgentNode(name: "x-orch", kind: .claude, model: "unknown-model", role: .orchestrator, itemKey: "BUG-7")
        o.effort = nil
        let row = BoardItemRow(item: client.boardItemList[0], orchestrator: o)
        XCTAssertEqual(BoardHandoffRules.detail(row, catalog: catalog).components(separatedBy: " · ").count, 3)
    }

    func testCanHandOff() {
        let flat = AgentTree.flatten(state.agents)
        let running = flat.first { $0.name == "auth-epic-orchestrator" }!
        let queued = flat.first { $0.name == "billing-spike-orchestrator" }!
        XCTAssertTrue(BoardHandoffRules.canHandOff(running, connected: true))
        XCTAssertFalse(BoardHandoffRules.canHandOff(running, connected: false))
        XCTAssertFalse(BoardHandoffRules.canHandOff(queued, connected: true))
        var replacing = running
        replacing.replacement = AgentReplacement(operationID: "op", mode: "handoff", phase: "stopping")
        XCTAssertFalse(BoardHandoffRules.canHandOff(replacing, connected: true))
    }

    func testOffersHandOffToOnlyOnTopLevelOrchestratorsWithEnabledHandoff() {
        let flat = AgentTree.flatten(state.agents)
        let orch = flat.first { $0.name == "auth-epic-orchestrator" }!
        let coder = flat.first { $0.name == "login-form-coder" }!
        XCTAssertTrue(BoardHandoffRules.offersHandOffTo(orch, actions: AgentTree.actions(orch, tmuxAlive: true, connected: true)))
        XCTAssertFalse(BoardHandoffRules.offersHandOffTo(coder, actions: AgentTree.actions(coder, tmuxAlive: true, connected: true)))
        XCTAssertFalse(BoardHandoffRules.offersHandOffTo(orch, actions: []))
    }

    func testDefaultSelectionAndLabels() async {
        let f = await form()
        XCTAssertEqual(f.selectedKey, "SPIKE-4", "first row when nothing is preselected")
        XCTAssertTrue(f.isHandoff)
        XCTAssertEqual(f.primaryLabel, Copy.handOff)
        XCTAssertEqual(f.caption, Copy.handoffUnavailable, "queued orchestrator can't hand off")
        XCTAssertFalse(f.canSubmit)
        f.selectedKey = "EPIC-12"
        XCTAssertNil(f.caption)
        XCTAssertTrue(f.canSubmit)
        // state.json holds a queued orchestrator, so wouldQueue is true until it is gone.
        f.update(agents: state.agents.filter { $0.name != "billing-spike-orchestrator" })
        f.selectedKey = "BUG-7"
        XCTAssertEqual(f.primaryLabel, Copy.startOrchestrator)
    }

    func testQueueLabelAtCapacity() async {
        let f = await form(max: 1)
        f.selectedKey = "BUG-7"
        XCTAssertEqual(f.primaryLabel, Copy.queueOrchestrator)
        XCTAssertEqual(f.caption, Copy.queuedCaption)
    }

    func testPreselectPicksTheOrchestratorsItem() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        XCTAssertEqual(f.selectedKey, "EPIC-12")
    }

    func testHandoffSubmitSendsPicksAndReusesRequestIDOnRetry() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.picker.setAgent("codex")
        client.failNext = .timedOut
        let first = await f.primary()
        XCTAssertFalse(first)
        XCTAssertEqual(f.primaryLabel, Copy.tryAgain)
        XCTAssertTrue(f.failure?.hasPrefix(Copy.handoffFailed) == true)
        let ok = await f.primary()
        XCTAssertTrue(ok)
        XCTAssertEqual(client.handoffRequests.count, 2)
        XCTAssertEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId, "same entries → same id")
        XCTAssertEqual(client.handoffRequests[1].agent, .codex)
        XCTAssertEqual(client.calls.last, "handoff auth-epic-orchestrator codex \(f.picker.choice.model)")
    }

    func testApiErrorMintsNewRequestID() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        client.failNext = .api(status: 409, code: "conflict", message: "A replacement is already in progress: op_1.")
        _ = await f.primary()
        XCTAssertEqual(f.failure, Copy.handoffFailed + " A replacement is already in progress: op_1.")
        _ = await f.primary()
        XCTAssertNotEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId)
    }

    func testReadyStartUsesStartEndpoint() async {
        let f = await form()
        f.selectedKey = "CHORE-3"
        let ok = await f.primary()
        XCTAssertTrue(ok)
        XCTAssertEqual(client.startBodies.count, 1)
        XCTAssertTrue(client.calls.contains { $0.hasPrefix("start CHORE-3 claude") })
    }

    func testLoadFailureShowsTryAgainAndReloads() async {
        let f = BoardHandoffForm(client: client, settings: state.settings, agents: state.agents, connected: true, preselectAgent: nil)
        client.boardItemsError = .unreachable
        await f.load()
        XCTAssertEqual(f.loadError, Copy.boardItemsLoadFailed)
        XCTAssertEqual(f.primaryLabel, Copy.tryAgain)
        XCTAssertTrue(f.canSubmit, "Try again reruns load()")
        client.boardItemsError = nil
        let closed = await f.primary()
        XCTAssertFalse(closed)
        XCTAssertNil(f.loadError)
        XCTAssertEqual(f.rows.count, 4)
    }

    func testEmptyListDisablesPrimary() async {
        client.boardItemList = []
        let f = await form()
        XCTAssertNil(f.selected)
        XCTAssertFalse(f.canSubmit)
    }

    func testUpdateKeepsSelectionWhileEligible() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        var agents = state.agents
        f.update(agents: agents)
        XCTAssertEqual(f.selectedKey, "EPIC-12")
        agents[0].state = .finished
        f.update(agents: agents)
        XCTAssertNotEqual(f.selectedKey, "EPIC-12", "EPIC-12 is in progress without a live orchestrator → gone")
    }

    func testAppModelPreselectIsConsumedOnce() async {
        let model = makeAppModel(client)
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        _ = model.makeBoardHandoffForm()
        XCTAssertNil(model.boardHandoffPreselect)
    }
}

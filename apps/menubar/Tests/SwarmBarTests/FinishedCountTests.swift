import Foundation
import XCTest
@testable import SwarmBarKit

/// /api/state trims finished agents and reports the untrimmed totals in `finished_count`.
final class FinishedCountTests: XCTestCase {
    private func node(_ name: String, state: AgentState = .finished, finished: [AgentNode] = [],
                      finishedCount: Int? = nil) -> AgentNode {
        var n = AgentNode(name: name, model: "m", role: .orchestrator, state: state,
                          session: nil, finished: finished)
        n.finishedCount = finishedCount
        return n
    }

    private func ids(_ rows: [AgentTree.Row]) -> [String] { rows.map { "\($0.depth) \($0.id)" } }

    func testDecodesFinishedCountWhenPresentAndNilWhenAbsent() throws {
        let plain: StateResponse = try Fixture.decode("state.json")
        XCTAssertNil(plain.finishedCount)
        XCTAssertNil(plain.agents[0].finishedCount)

        var obj = try JSONSerialization.jsonObject(with: Fixture.data("state.json")) as! [String: Any]
        var agents = obj["agents"] as! [[String: Any]]
        agents[0]["finished_count"] = 25
        obj["agents"] = agents
        obj["finished_count"] = 40
        let trimmed = try SwarmJSON.decode(StateResponse.self, from: JSONSerialization.data(withJSONObject: obj))
        XCTAssertEqual(trimmed.finishedCount, 40)
        XCTAssertEqual(trimmed.agents[0].finishedCount, 25)
    }

    func testFinishedRowShowsTheTotalAndATrimNoteWhenTrimmed() {
        let shown = [node("a"), node("b")]
        let o = node("orch", state: .active, finished: shown, finishedCount: 25)
        let closed = AgentTree.rows([o], collapsed: [], openFinished: [])
        guard case let .finished(parent, count) = closed.last?.kind else { return XCTFail("finished row") }
        XCTAssertEqual(parent, "orch")
        XCTAssertEqual(count, 25)
        let open = AgentTree.rows([o], collapsed: [], openFinished: ["orch"])
        XCTAssertEqual(ids(open), ["0 agent:orch", "1 finished:orch", "1 agent:a", "1 agent:b", "1 trim:orch"])
    }

    func testNoTrimNoteWhenNothingWasTrimmedOrCountIsAbsent() {
        let shown = [node("a"), node("b")]
        for count in [2, nil] as [Int?] {
            let o = node("orch", state: .active, finished: shown, finishedCount: count)
            let rows = AgentTree.rows([o], collapsed: [], openFinished: ["orch"])
            XCTAssertEqual(ids(rows), ["0 agent:orch", "1 finished:orch", "1 agent:a", "1 agent:b"])
            guard case let .finished(_, n) = rows[1].kind else { return XCTFail("finished row") }
            XCTAssertEqual(n, 2)
        }
    }

    func testTopLevelFinishedUsesTheStateTotal() {
        let roots = [node("live", state: .active), node("done1"), node("done2")]
        let rows = AgentTree.rows(roots, collapsed: [], openFinished: [""], topFinishedCount: 40)
        XCTAssertEqual(ids(rows), ["0 agent:live", "0 finished:", "0 agent:done1", "0 agent:done2", "0 trim:"])
        guard case let .finished(_, n) = rows[1].kind else { return XCTFail("finished row") }
        XCTAssertEqual(n, 40)
    }

    func testTrimNoteCopy() {
        XCTAssertEqual(Copy.showingLatestFinished(10), "Showing latest 10 · open the board for all")
        XCTAssertEqual(Copy.finishedOnBoard, "Open the board to see them")
    }

    /// A kept finished ancestor of a live agent carries finished_count > 0 but no finished nodes.
    func testZeroShownKeepsTheCountAndLinksToTheItem() {
        var kept = node("kept", state: .active, finishedCount: 12)
        kept.itemKey = "TASK-7"
        kept.children = [node("live", state: .active)]
        let rows = AgentTree.rows([kept], collapsed: [], openFinished: ["kept"])
        XCTAssertEqual(ids(rows), ["0 agent:kept", "1 agent:live", "1 finished:kept", "1 trim:kept"])
        guard case let .trimNote(_, shown, itemKey) = rows.last?.kind else { return XCTFail("trim note") }
        XCTAssertEqual(shown, 0)
        XCTAssertEqual(itemKey, "TASK-7")
    }

    func testTopLevelTrimNoteLinksToTheBoardRoot() {
        let rows = AgentTree.rows([node("done")], collapsed: [], openFinished: [""], topFinishedCount: 40)
        guard case let .trimNote(_, _, itemKey) = rows.last?.kind else { return XCTFail("trim note") }
        XCTAssertNil(itemKey)
    }

    /// The daemon always keeps up to 10 finished under a non-finished parent, so a count with
    /// nothing to show never makes a live row disclosable.
    func testCountAloneDoesNotMakeALiveRowDisclosable() {
        let rows = AgentTree.rows([node("live", state: .active, finishedCount: 5)], collapsed: [], openFinished: [])
        XCTAssertNil(rows.first?.expanded)
    }
}

import XCTest
@testable import SwarmBarKit

@MainActor
final class AgentPickerModelTests: XCTestCase {
    func testPrefillApplyAndPayloads() throws {
        let state: StateResponse = try Fixture.decode("state.json")
        let catalog: [AgentCatalogEntry] = try Fixture.decode("catalog.json")
        let p = AgentPickerModel(settings: state.settings)
        XCTAssertEqual(p.choice, AgentChoice(agent: .claude, model: "opus"))
        p.apply(catalog: catalog)
        XCTAssertTrue(p.errors.isValid)
        XCTAssertNil(p.advisorEffortOptions, "native Claude-on-Claude advisor has no separate effort")
        p.setAdvisorAgent("none")
        XCTAssertEqual(p.advisorPayload, .none)
        p.setAgent("codex")
        XCTAssertEqual(p.choice.agent, .codex)
    }
}

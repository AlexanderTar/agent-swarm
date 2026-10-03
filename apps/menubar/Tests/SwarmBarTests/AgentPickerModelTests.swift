import XCTest
@testable import SwarmBarKit

@MainActor
final class AgentPickerModelTests: XCTestCase {
    func testPrefillApplyAndPayloads() throws {
        let state: StateResponse = try Fixture.decode("state.json")
        let catalog: [AgentCatalogEntry] = try Fixture.decode("catalog.json")
        let p = AgentPickerModel(settings: state.settings)
        XCTAssertEqual(p.choice, AgentChoice(agent: .claude, model: "opus"))
        XCTAssertEqual(p.errors, FieldErrors(), "no errors against a catalog that hasn't arrived")
        XCTAssertFalse(p.isValid, "but no submit either")
        p.apply(catalog: catalog)
        XCTAssertTrue(p.errors.isValid)
        XCTAssertTrue(p.isValid)
        XCTAssertNil(p.advisorEffortOptions, "native Claude-on-Claude advisor has no separate effort")
        p.setAdvisorAgent("none")
        XCTAssertEqual(p.advisorPayload, .none)
        p.setAgent("codex")
        XCTAssertEqual(p.choice.agent, .codex)
    }
}

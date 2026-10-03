import XCTest
@testable import SwarmBarKit

final class MarkdownBlocksTests: XCTestCase {
    func testBlockStructureIsKept() {
        let md = "## Reporting (maintainer)\n\nI maintain agent-swarm.\n\nSecond paragraph with `code`.\n\n1. First\n2. Second\n\n- bullet"
        XCTAssertEqual(MarkdownBlocks.parse(md), [
            .heading(level: 2, text: "Reporting (maintainer)"),
            .paragraph("I maintain agent-swarm."),
            .paragraph("Second paragraph with `code`."),
            .listItem(marker: "1.", text: "First"),
            .listItem(marker: "2.", text: "Second"),
            .listItem(marker: "•", text: "bullet"),
        ])
    }

    func testSoftWrappedLinesJoinIntoOneParagraph() {
        XCTAssertEqual(MarkdownBlocks.parse("a\nb"), [.paragraph("a b")])
    }
}

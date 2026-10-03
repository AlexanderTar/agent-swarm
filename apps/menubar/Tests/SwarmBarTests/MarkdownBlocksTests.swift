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

    func testFencedCodeLinesAreKeptVerbatim() {
        let md = "Before\n\n```sh\n# x\n  indented  line\n\n- not a list\n```\n\nAfter"
        XCTAssertEqual(MarkdownBlocks.parse(md), [
            .paragraph("Before"),
            .code("# x\n  indented  line\n\n- not a list"),
            .paragraph("After"),
        ])
    }

    func testUnclosedFenceRunsToTheEnd() {
        XCTAssertEqual(MarkdownBlocks.parse("```\n# x"), [.code("# x")])
    }

    func testNestedListIndentationIsKept() {
        let md = "- top\n  - child\n    1. grandchild\n- next"
        XCTAssertEqual(MarkdownBlocks.parse(md), [
            .listItem(marker: "•", text: "top"),
            .listItem(marker: "•", text: "child", indent: 1),
            .listItem(marker: "1.", text: "grandchild", indent: 2),
            .listItem(marker: "•", text: "next"),
        ])
    }
}

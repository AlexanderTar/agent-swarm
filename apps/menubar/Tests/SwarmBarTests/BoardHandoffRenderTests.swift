import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class BoardHandoffRenderTests: XCTestCase {
    private func render(_ form: BoardHandoffForm) -> NSHostingView<BoardHandoffView> {
        let host = NSHostingView(rootView: BoardHandoffView(form: form, onDone: {}, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 300)
        host.layoutSubtreeIfNeeded()
        return host
    }
    private func popups(_ v: NSView) -> [NSPopUpButton] { ((v as? NSPopUpButton).map { [$0] } ?? []) + v.subviews.flatMap(popups) }

    func testItemPopupIsOneLineWithTwoLineMenuRows() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        let form = model.makeBoardHandoffForm()
        await form.load()
        let host = render(form)
        let item = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == Copy.item })
        XCTAssertLessThan(item.frame.height, 30, "closed Item popup shows one line")
        XCTAssertEqual(item.titleOfSelectedItem, "EPIC-12 · Authentication")
        let rowTitle = try XCTUnwrap(item.itemArray.first?.attributedTitle?.string)
        XCTAssertTrue(rowTitle.contains("\n"), "menu rows carry a second detail line")
        XCTAssertNotNil(popups(host).first { $0.accessibilityLabel() == Copy.agent }, "shared AgentPickerGrid present")
    }

    func testEmptyStateShowsNoItemsCopy() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        client.boardItemList = []
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        let host = render(form)
        XCTAssertNil(popups(host).first { $0.accessibilityLabel() == Copy.item })
        XCTAssertFalse(form.canSubmit)
    }
}

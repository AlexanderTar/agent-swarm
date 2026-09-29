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

    func testItemPopupIsTwoLinesClosedAndOpen() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        let form = model.makeBoardHandoffForm()
        await form.load()
        let host = render(form)
        let item = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == Copy.item })
        XCTAssertGreaterThanOrEqual(item.frame.height, 34, "closed Item popup is one two-line row tall")
        XCTAssertEqual(item.titleOfSelectedItem, "EPIC-12 · Authentication")
        let closed = try XCTUnwrap((item.cell as? NSPopUpButtonCell)?.menuItem?.attributedTitle?.string)
        XCTAssertTrue(closed.hasPrefix("EPIC-12 · Authentication\n"), "closed popup shows title then detail line")
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

    func testMenuRebuildsWhenOnlyDetailLineChanges() throws {
        final class Box { var text = "Running" }
        let box = Box()
        func picker() -> WideOptionPicker {
            WideOptionPicker("Item", options: [PickerOption("a", "A")], value: "a",
                             detail: { _ in NSAttributedString(string: box.text) }) { _ in }
        }
        let host = NSHostingView(rootView: picker())
        host.frame = NSRect(x: 0, y: 0, width: 400, height: 60)
        host.layoutSubtreeIfNeeded()
        let popup = try XCTUnwrap(popups(host).first)
        XCTAssertEqual(popup.itemArray.first?.attributedTitle?.string, "A\nRunning")
        box.text = "Idle"
        host.rootView = picker()
        host.layoutSubtreeIfNeeded()
        XCTAssertEqual(popup.itemArray.first?.attributedTitle?.string, "A\nIdle")
        XCTAssertEqual((popup.cell as? NSPopUpButtonCell)?.menuItem?.attributedTitle?.string, "A\nIdle")
    }

    func testTintedIconIsNotTemplate() {
        XCTAssertFalse(Icons.tinted(IconName(.claude)).isTemplate, "attachment icons must not rely on template tinting")
    }

    func testStartModeShowsWorkerRowsPrefilledFromSettings() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        XCTAssertFalse(form.isHandoff)
        let host = render(form)
        let coder = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == "Coding Agent" }, "worker rows in start mode")
        XCTAssertEqual(coder.titleOfSelectedItem, "Claude")
    }

    func testHandoffModeHidesWorkerRows() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        let form = model.makeBoardHandoffForm()
        await form.load()
        XCTAssertTrue(form.isHandoff)
        let host = render(form)
        XCTAssertNil(popups(host).first { $0.accessibilityLabel() == "Coding Agent" }, "HandoffRequest has no roles")
    }
}

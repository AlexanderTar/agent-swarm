import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

/// TASK-354 dialog chrome: window titles and copy.
final class DialogChromeTests: XCTestCase {
    func testOrchestrateTaskCopy() {
        XCTAssertEqual(Copy.orchestrateBoardItem, "Orchestrate task")
        XCTAssertEqual(Copy.orchestrateBoardItemMenu, "Orchestrate task")
    }

    /// HIG default action: Liquid Glass prominent where the OS has it (macOS 26),
    /// tinted bordered buttons below. Offscreen `cacheDisplay` snapshots blank with
    /// glassProminent (real windows render it fine), so OCR layout tests pin
    /// `prominentStyleOverride` to `.borderedProminent` while capturing.
    func testProminentStyleMatchesOS() {
        DialogChrome.prominentStyleOverride = nil
        if #available(macOS 26, *) {
            XCTAssertEqual(DialogChrome.prominentStyle, .glassProminent)
        } else {
            XCTAssertEqual(DialogChrome.prominentStyle, .borderedProminent)
        }
    }

    func testProminentOverridePinsCaptureStyle() {
        DialogChrome.prominentStyleOverride = .borderedProminent
        defer { DialogChrome.prominentStyleOverride = nil }
        XCTAssertEqual(DialogChrome.prominentStyle, .borderedProminent)
    }

    @MainActor
    func testRepoChooserUsesSubtleScroller() {
        let rows = (0..<10).map { Repo(id: "r\($0)", name: "repo-\($0)", path: "/tmp/repo-\($0)") }
        let host = NSHostingView(rootView: RepoChooser(rows: rows, selection: .constant([])))
        host.frame = NSRect(x: 0, y: 0, width: 500, height: 200)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        func scrolls(in view: NSView) -> [NSScrollView] {
            let own = (view as? NSScrollView).map { [$0] } ?? []
            return own + view.subviews.flatMap(scrolls)
        }
        guard let scroll = scrolls(in: host).first else { return XCTFail("Repo list scroll view missing") }
        XCTAssertEqual(scroll.scrollerStyle, .overlay)
        XCTAssertTrue(scroll.autohidesScrollers)
        XCTAssertEqual(scroll.verticalScroller?.controlSize, .small)
    }

    @MainActor
    func testTranslucentWindowConfig() {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 100, height: 100),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        XCTAssertTrue(window.isOpaque)
        XCTAssertFalse(window.titlebarAppearsTransparent)
        XCTAssertFalse(window.styleMask.contains(.fullSizeContentView))
        DialogChrome.configure(window)
        XCTAssertFalse(window.isOpaque)
        XCTAssertEqual(window.backgroundColor, .clear)
        XCTAssertTrue(window.titlebarAppearsTransparent, "title bar must blend with the material, not stay opaque")
        XCTAssertTrue(window.styleMask.contains(.fullSizeContentView),
                      "content must extend under the title bar so one material covers the whole window, not just below it")
    }

    /// The footer split button is a primary button plus a borderless chevron menu
    /// sharing one row — never one segmented control. The segmented chevron cell
    /// drew its pressed pill ~3 pt below the label segment with the menu open
    /// (screenshot 02), which no Menu modifier can fix, so the control itself
    /// changed; a popup bezel is no replacement (hidden indicator collapses it
    /// to 14 pt, and under glass it rasterizes blank white — both verified with
    /// key-window captures). Fails on the pre-fix implementation (one segmented
    /// control, no popup menu).
    @MainActor
    func testFooterSplitButtonChevronAligned() {
        let host = NSHostingView(rootView: PopoverFooterSplitButton(
            openNewOrchestrator: {}, openBoardHandoff: { _ in }, connected: true)
            .controlSize(.small)
            .glassButtons())
        host.frame = NSRect(x: 0, y: 0, width: 360, height: 60)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        // Shrink the host to the row itself so the row's vertical center is the
        // host's: the chevron is centered in the row iff it is centered here.
        host.frame.size.height = host.fittingSize.height
        host.layoutSubtreeIfNeeded()
        func views(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(views) }
        let all = views(host)
        XCTAssertTrue(all.compactMap({ $0 as? NSSegmentedControl }).isEmpty,
                      "no segmented control: its chevron cell misdrew its pressed pill")
        let chevrons = all.compactMap { $0 as? NSPopUpButton }
        XCTAssertEqual(chevrons.count, 1, "exactly the chevron menu beside the primary button")
        guard let chevron = chevrons.first else { return }
        let box = chevron.convert(chevron.bounds, to: host)
        XCTAssertEqual(box.midY, host.bounds.midY, accuracy: 0.5,
                       "chevron shares the row's vertical center instead of riding high or low: \(box) in \(host.bounds)")
    }

    /// TASK-358 boxed split control: "New orchestrator" + chevron is one shared
    /// 22 pt row (the container hugs the segments, so the row is 22 because
    /// the segments are) with a hairline divider between them and the chevron
    /// optically centered. Fails on the pre-fix implementation: a 20 pt row,
    /// no divider, and a drawn (unmeasurable) label button.
    ///
    /// One measured compromise, documented not hidden: the chevron's AppKit
    /// popup keeps its intrinsic ~14 pt height (no modifier stretches it —
    /// verified), so what matches the label is the chevron *segment*: its
    /// 28×22 layout frame and hit area, with the glyph centered. The test
    /// asserts the segment geometry (zone + centering), not the popup frame.
    @MainActor
    func testFooterSplitBoxedControl() {
        let host = NSHostingView(rootView: PopoverFooterSplitButton(
            openNewOrchestrator: {}, openBoardHandoff: { _ in }, connected: true)
            .controlSize(.small)
            .glassButtons())
        host.frame = NSRect(x: 0, y: 0, width: 360, height: 60)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        host.frame.size = host.fittingSize
        host.layoutSubtreeIfNeeded()
        let row = host.bounds
        XCTAssertEqual(row.height, PopoverFooterSplitButton.rowHeight, accuracy: 0.5,
                       "one shared 22 pt row, not the pre-fix 20 pt: \(row)")
        func views(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(views) }
        let all = views(host)
        func inHost(_ view: NSView) -> NSRect { view.convert(view.bounds, to: host) }
        XCTAssertTrue(all.compactMap({ $0 as? NSSegmentedControl }).isEmpty,
                      "no segmented control: its chevron cell misdrew its pressed pill")
        guard let label = all.first(where: { $0 is NSButton && !($0 is NSPopUpButton) }) else {
            XCTFail("label segment renders drawn, not as an AppKit button")
            return
        }
        let labelBox = inHost(label)
        XCTAssertEqual(labelBox.height, row.height, accuracy: 0.5,
                       "label segment fills the row height: \(labelBox) in \(row)")
        XCTAssertEqual(labelBox.midY, row.midY, accuracy: 0.5, "label vertically centered in row")
        XCTAssertEqual(labelBox.minX, row.minX, accuracy: 1.0, "label starts where the row starts")
        let chevrons = all.compactMap { $0 as? NSPopUpButton }
        XCTAssertEqual(chevrons.count, 1, "exactly the chevron menu beside the primary button")
        guard let chevron = chevrons.first else { return }
        let chevronBox = inHost(chevron)
        XCTAssertEqual(chevronBox.midY, row.midY, accuracy: 0.5,
                       "chevron optically centered in the row: \(chevronBox) in \(row)")
        XCTAssertTrue(row.contains(chevronBox.insetBy(dx: -0.5, dy: -0.5)), "chevron inside the row")
        // The menu item (Copy.orchestrateBoardItemMenu) is preserved by code —
        // the same Button line as pre-fix. It is not asserted here: SwiftUI
        // builds the NSMenu lazily on open, so itemTitles is empty pre-open
        // (measured), and opening it is not testable headlessly.
        let chevronZone = NSRect(x: row.maxX - 28, y: row.minY, width: 28, height: row.height)
        // The popup cell reserves undrawn trailing insets, so the popup frame
        // itself sits left in its slot; what the user sees is the glyph, the
        // popup's single image subview (located structurally, never by its
        // private class name, so an AppKit change fails loudly here).
        XCTAssertEqual(chevron.subviews.count, 1, "chevron popup carries just its glyph")
        guard let glyph = chevron.subviews.first else { return }
        let glyphBox = inHost(glyph)
        XCTAssertEqual(glyphBox.midX, chevronZone.midX, accuracy: 1.0,
                       "chevron glyph centered in its own 28 pt segment: \(glyphBox) in \(chevronZone)")
        XCTAssertEqual(glyphBox.midY, row.midY, accuracy: 0.5, "chevron glyph vertically centered in row")
        let dividers = all.compactMap { $0 as? SplitDividerView }
        XCTAssertEqual(dividers.count, 1, "one hairline divider between the segments")
        guard let divider = dividers.first else { return }
        let dividerBox = inHost(divider)
        XCTAssertLessThanOrEqual(dividerBox.width, 2.0, "hairline thin: \(dividerBox)")
        XCTAssertGreaterThanOrEqual(dividerBox.height, 10.0, "a vertical hairline, not a dot: \(dividerBox)")
        XCTAssertEqual(dividerBox.midY, row.midY, accuracy: 1.0, "divider vertically centered in row")
        XCTAssertTrue(row.contains(dividerBox.insetBy(dx: -0.5, dy: -0.5)), "divider inside the row")
        XCTAssertGreaterThanOrEqual(dividerBox.minX, row.minX + 40.0, "label segment precedes the divider")
        XCTAssertLessThanOrEqual(dividerBox.maxX, chevronBox.minX + 1.0, "divider ends where the chevron starts")
    }
}

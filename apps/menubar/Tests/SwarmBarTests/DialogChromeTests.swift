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
    /// no divider, and a drawn (unmeasurable) label button; and on the
    /// SwiftUI-Menu iteration (commit ab44ab6): no container view, a 24×14
    /// popup centered in its 28 pt slot, dead hit area at the slot edges, and
    /// no label padding. The chevron here is a real NSPopUpButton sized to
    /// its whole 28×22 segment, so popup, slot, and hit area are one thing.
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
        XCTAssertTrue(row.contains(chevronBox.insetBy(dx: 0.5, dy: 0.5)), "chevron inside the row")
        // The menu item (Copy.orchestrateBoardItemMenu) is preserved by code —
        // the same Button line as pre-fix. It is not asserted here: SwiftUI
        // builds the NSMenu lazily on open, so itemTitles is empty pre-open
        // (measured), and opening it is not testable headlessly.
        // The chevron segment fills its 28 pt slot at full row height: the
        // popup itself (not just a computed zone) matches the label segment.
        XCTAssertEqual(chevronBox.height, PopoverFooterSplitButton.rowHeight, accuracy: 0.5,
                       "chevron popup fills the row height like the label: \(chevronBox) in \(row)")
        XCTAssertEqual(chevronBox.width, 28.0, accuracy: 1.0,
                       "chevron popup fills its 28 pt segment: \(chevronBox)")
        // The popup owns its whole slot: same origin and size, so slot,
        // popup, and hit area are one 28×22. (A windowless host.hitTest never
        // descends into AppKit controls — measured on both the old Menu popup
        // and this button — so geometry plus the button's own hitTest below
        // is the oracle, not host.hitTest.)
        XCTAssertEqual(chevronBox.minX, row.maxX - 28.0, accuracy: 1.0,
                       "chevron popup starts where its 28 pt segment starts: \(chevronBox) in \(row)")
        // The button answers its own hits across the slot, edges included.
        for pt in [NSPoint(x: chevronBox.midX, y: chevronBox.midY),
                   NSPoint(x: chevronBox.midX, y: chevronBox.minY + 1),
                   NSPoint(x: chevronBox.midX, y: chevronBox.maxY - 1),
                   NSPoint(x: chevronBox.maxX - 1, y: chevronBox.midY)] {
            let local = chevron.convert(pt, from: host)
            XCTAssertEqual(chevron.hitTest(local), chevron,
                           "chevron popup hit-tests its own slot at \(pt)")
        }
        let chevronZone = NSRect(x: row.maxX - 28, y: row.minY, width: 28, height: row.height)
        // Cell-drawn glyph (no image subview): the popup carries the chevron
        // image and sits centered in its own segment.
        XCTAssertNotNil(chevron.image, "chevron popup shows its glyph")
        XCTAssertEqual(chevronBox.midX, chevronZone.midX, accuracy: 1.0,
                       "chevron popup centered in its own 28 pt segment: \(chevronBox) in \(chevronZone)")
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
        // Label breathing room: 8 pt padding each side pushes the divider past
        // the pre-fix ~111 pt label width.
        XCTAssertGreaterThanOrEqual(dividerBox.minX - row.minX, 120.0,
                                    "label segment has horizontal padding before the divider")
        // One shared container: a real background view (like SplitDividerView)
        // so deleting the chrome modifier fails loudly. It must equal the row
        // and span both segments.
        let boxes = all.compactMap { $0 as? SplitBoxContainerView }
        XCTAssertEqual(boxes.count, 1, "one shared container background for the boxed control")
        guard let boxView = boxes.first else { return }
        let boxFrame = inHost(boxView)
        XCTAssertEqual(boxFrame.height, PopoverFooterSplitButton.rowHeight, accuracy: 0.5,
                       "container is the 22 pt row: \(boxFrame) in \(row)")
        XCTAssertEqual(boxFrame.minX, row.minX, accuracy: 1.0, "container starts where the row starts")
        XCTAssertEqual(boxFrame.maxX, row.maxX, accuracy: 1.0, "container ends where the row ends")
        XCTAssertEqual(boxFrame.midY, row.midY, accuracy: 0.5, "container vertically centered on row")
        XCTAssertTrue(boxFrame.contains(labelBox.insetBy(dx: 0.5, dy: 0.5)), "label inside the container")
        XCTAssertTrue(boxFrame.contains(dividerBox.insetBy(dx: -0.5, dy: -0.5)), "divider inside the container")
        XCTAssertTrue(boxFrame.contains(chevronBox.insetBy(dx: 0.5, dy: 0.5)), "chevron inside the container")
    }
}

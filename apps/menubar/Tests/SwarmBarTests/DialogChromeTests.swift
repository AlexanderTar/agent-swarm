import AppKit
import SwiftUI
import Vision
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
        XCTAssertFalse(scroll.drawsBackground)

        // "Show scroll bars: Always" / an attached mouse makes AppKit flip the scroll view
        // back to the legacy style (opaque track) when the preferred style changes.
        scroll.scrollerStyle = .legacy
        NotificationCenter.default.post(name: NSScroller.preferredScrollerStyleDidChangeNotification, object: nil)
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        XCTAssertEqual(scroll.scrollerStyle, .overlay, "picker must stay overlay after the preferred style changes")
    }

    /// CHORE-24: Settings lost its toolbar tabs and title on the macOS 27 SDK. The
    /// accessor configured the window from viewDidMoveToWindow, which AppKit calls
    /// inside -[NSWindow setContentView:]. Clearing the background there drops the
    /// theme frame's backdrop view, the anchor setContentView then inserts the content
    /// above, so in a SwiftUI scene window the content landed on top of the title bar
    /// and its material hid the tabs, title and traffic lights (backtraces from the
    /// live app). A plain test window re-adds a backdrop and hides the flip, so this
    /// pins the cause: no configuration while the content view is being installed.
    @MainActor
    func testTranslucentContentStaysBelowTitlebar() throws {
        let window = NSWindow(contentRect: NSRect(x: 200, y: 200, width: 520, height: 260),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.title = "Chrome Probe"
        let toolbar = NSToolbar(identifier: "chrome-probe")
        let delegate = ProbeToolbar()
        toolbar.delegate = delegate
        toolbar.displayMode = .iconAndLabel
        window.toolbar = toolbar
        // SwiftUI scenes build the hosting view's subtree before installing it, so the
        // accessor joins the window from inside setContentView, as in the app.
        let host = NSHostingView(rootView: Color.clear.frame(width: 520, height: 260)
            .translucentDialogBackground()
            .background(TranslucentWindowAccessor()))
        host.frame = NSRect(x: 0, y: 0, width: 520, height: 260)
        host.layoutSubtreeIfNeeded()
        window.contentView = host
        XCTAssertTrue(window.isOpaque, "accessor must not reconfigure the window inside setContentView")
        window.orderFront(nil)
        defer { window.close() }
        RunLoop.main.run(until: Date().addingTimeInterval(0.3))

        XCTAssertTrue(window.styleMask.contains(.fullSizeContentView), "accessor must still make the window translucent")
        let theme = try XCTUnwrap(window.contentView?.superview)
        let content = try XCTUnwrap(theme.subviews.firstIndex { $0 === window.contentView })
        let titlebar = try XCTUnwrap(theme.subviews.firstIndex { String(describing: type(of: $0)) == "NSTitlebarContainerView" })
        XCTAssertLessThan(content, titlebar, "content view must sit below the title bar, not cover it")

        let url = FileManager.default.temporaryDirectory.appendingPathComponent("chrome-\(UUID().uuidString).png")
        defer { try? FileManager.default.removeItem(at: url) }
        let capture = Process()
        capture.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        capture.arguments = ["-x", "-o", "-l", "\(window.windowNumber)", url.path]
        try capture.run()
        capture.waitUntilExit()
        let bitmap = try XCTUnwrap(NSBitmapImageRep(data: Data(contentsOf: url)))
        let request = VNRecognizeTextRequest()
        try VNImageRequestHandler(cgImage: XCTUnwrap(bitmap.cgImage), options: [:]).perform([request])
        let text = (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: "\n")
        XCTAssertTrue(text.contains("Chrome Probe"), "window title must render; OCR saw: \(text)")
        XCTAssertTrue(text.contains("Probe Tab"), "toolbar tab label must render; OCR saw: \(text)")
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

    /// Orchestrate task opens as a small spinner, then grows to the form; the window
    /// must stay centered through that resize, not keep the spinner's origin.
    @MainActor
    func testWindowCenterStaysCenteredAcrossResize() throws {
        let screen = try XCTUnwrap(NSScreen.main).visibleFrame
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 480, height: 200),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        let host = NSHostingView(rootView: Color.clear.background(WindowCenterAccessor()))
        window.contentView = host
        window.setFrameOrigin(NSPoint(x: 3, y: 5))
        window.orderFront(nil)
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        XCTAssertEqual(window.frame.midX, screen.midX, accuracy: 1)
        XCTAssertEqual(window.frame.midY, screen.midY, accuracy: 1)
        window.setContentSize(NSSize(width: 820, height: 300))
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        XCTAssertEqual(window.frame.midX, screen.midX, accuracy: 1)
        XCTAssertEqual(window.frame.midY, screen.midY, accuracy: 1)
        window.close()
    }

    /// Centering is for the open and the spinner-to-form grow only: a later resize or a
    /// user drag must not snap the window back.
    @MainActor
    func testWindowCenterStopsAfterTheFormGrow() throws {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 480, height: 200),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: Color.clear.background(WindowCenterAccessor()))
        window.orderFront(nil)
        defer { window.close() }
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        window.setContentSize(NSSize(width: 820, height: 300))
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        window.setFrameOrigin(NSPoint(x: 3, y: 5))
        let top = window.frame.maxY
        window.setContentSize(NSSize(width: 700, height: 280))
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        // AppKit keeps the top-left corner fixed on a resize, so compare that, not the origin.
        XCTAssertEqual(window.frame.minX, 3, accuracy: 1, "a later resize must not recenter")
        XCTAssertEqual(window.frame.maxY, top, accuracy: 1, "a later resize must not recenter")
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
        // The menu the user sees: a pullsDown popup uses item 0 as its button
        // title and hides it in the opened menu, so the action item must sit
        // at index 1 — with a single item the menu opens empty (measured via
        // menuWillOpen: item isHidden, no menu window). The menu is built
        // eagerly in AppKit here, so it is asserted directly.
        guard let menu = chevron.menu else {
            XCTFail("chevron popup has no menu")
            return
        }
        XCTAssertGreaterThanOrEqual(menu.numberOfItems, 2,
                                    "pull-down title item plus the action item: \(menu.items.map(\.title))")
        let actionIndex = menu.items.firstIndex(where: { $0.title == Copy.orchestrateBoardItemMenu })
        XCTAssertEqual(actionIndex, 1,
                       "action item sits after the pull-down title item: \(menu.items.map(\.title))")
        if let actionIndex {
            let actionItem = menu.items[actionIndex]
            XCTAssertFalse(actionItem.isHidden, "action item is listed in the opened menu")
            var picked = false
            let firing = SplitChevronPopUpButton(frame: NSRect(x: 0, y: 0, width: 28, height: 22),
                                                 onPick: { _ in picked = true })
            firing.menu?.performActionForItem(at: 1)
            XCTAssertTrue(picked, "menu action item fires onPick")
        }
        XCTAssertEqual(chevron.accessibilityLabel(), Copy.moreStartOptions,
                       "chevron popup carries its own AppKit accessibility label")
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

/// One labelled toolbar item, standing in for the Settings tab strip.
private final class ProbeToolbar: NSObject, NSToolbarDelegate {
    let id = NSToolbarItem.Identifier("probe-tab")
    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] { [id] }
    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] { [id] }
    func toolbar(_ toolbar: NSToolbar, itemForItemIdentifier itemIdentifier: NSToolbarItem.Identifier,
                 willBeInsertedIntoToolbar flag: Bool) -> NSToolbarItem? {
        let item = NSToolbarItem(itemIdentifier: itemIdentifier)
        item.label = "Probe Tab"
        item.image = NSImage(systemSymbolName: "gearshape", accessibilityDescription: nil)
        return item
    }
}

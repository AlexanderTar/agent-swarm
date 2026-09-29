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
}

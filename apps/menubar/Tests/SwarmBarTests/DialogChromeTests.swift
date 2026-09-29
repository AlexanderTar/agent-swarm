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

    /// The footer split button must stay ONE segmented control (label segment plus
    /// chevron segment share a single frame by construction) so the chevron can never
    /// drift off its box the way two adjacent buttons could.
    @MainActor
    func testFooterSplitButtonRendersOneAlignedControl() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let host = NSHostingView(rootView: PopoverFooterSplitButton(
            openNewOrchestrator: {}, openBoardHandoff: { _ in }, connected: m.connected))
        host.frame = NSRect(x: 0, y: 0, width: 360, height: 60)
        host.layoutSubtreeIfNeeded()
        try await Task.sleep(for: .milliseconds(100))
        host.layoutSubtreeIfNeeded()
        func segmented(in view: NSView) -> [NSSegmentedControl] {
            let own = (view as? NSSegmentedControl).map { [$0] } ?? []
            return own + view.subviews.flatMap(segmented)
        }
        let controls = segmented(in: host)
        XCTAssertEqual(controls.count, 1, "footer split button is a single control, never two buttons")
        guard let control = controls.first else { return }
        XCTAssertEqual(control.segmentCount, 2, "label segment plus chevron segment")
    }

    /// The footer split button in its real PopoverView context: one 2-segment small
    /// control whose chevron segment (the trailing 20 pt) shares the control's full
    /// height inside its bounds, so the chevron can never drift off its box the way
    /// two adjacent buttons could. (Locks PopoverFooterSplitButton, new this task —
    /// at the base revision the component and this geometry both fail to build.)
    @MainActor
    func testFooterSplitButtonChevronAlignedInPopover() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let host = NSHostingView(rootView: PopoverView(model: m, openNewOrchestrator: {},
                                                       openBoardHandoff: { _ in }, openSettings: {}))
        host.frame = NSRect(x: 0, y: 0, width: 360, height: 600)
        host.layoutSubtreeIfNeeded()
        try await Task.sleep(for: .milliseconds(100))
        host.layoutSubtreeIfNeeded()
        func segmented(in view: NSView) -> [NSSegmentedControl] {
            let own = (view as? NSSegmentedControl).map { [$0] } ?? []
            return own + view.subviews.flatMap(segmented)
        }
        // The Usage section owns the popover's other segmented control (its range
        // picker), so pick the footer split out by its label segment.
        let controls = segmented(in: host).filter {
            $0.segmentCount == 2 && $0.label(forSegment: 0) == Copy.newOrchestrator
        }
        XCTAssertEqual(controls.count, 1, "footer split button is a single control, never two buttons")
        guard let control = controls.first else { return }
        let frame = control.convert(control.bounds, to: host)
        XCTAssertEqual(frame.height, 20, accuracy: 0.5, "small control height")
        let chevronWidth = control.width(forSegment: 1)
        XCTAssertEqual(chevronWidth, 20, accuracy: 0.5, "small chevron segment width")
        let chevron = NSRect(x: control.bounds.maxX - chevronWidth, y: control.bounds.minY,
                             width: chevronWidth, height: control.bounds.height)
        XCTAssertTrue(control.bounds.contains(chevron), "chevron segment fills the control's full height inside its bounds: \(chevron) in \(control.bounds)")
    }

    @MainActor
    func testTranslucentWindowConfig() {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 100, height: 100),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        XCTAssertTrue(window.isOpaque)
        XCTAssertFalse(window.titlebarAppearsTransparent)
        DialogChrome.configure(window)
        XCTAssertFalse(window.isOpaque)
        XCTAssertEqual(window.backgroundColor, .clear)
        XCTAssertTrue(window.titlebarAppearsTransparent, "title bar must blend with the material, not stay opaque")
    }
}

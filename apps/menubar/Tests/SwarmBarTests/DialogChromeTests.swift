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

    func testProminentStyleMatchesOS() {
        if #available(macOS 26, *) {
            XCTAssertEqual(DialogChrome.prominentStyle, .glassProminent)
        } else {
            XCTAssertEqual(DialogChrome.prominentStyle, .borderedProminent)
        }
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

    @MainActor
    func testTranslucentWindowConfig() {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 100, height: 100),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        XCTAssertTrue(window.isOpaque)
        DialogChrome.configure(window)
        XCTAssertFalse(window.isOpaque)
        XCTAssertEqual(window.backgroundColor, .clear)
    }
}

import AppKit
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
    func testTranslucentWindowConfig() {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 100, height: 100),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        XCTAssertTrue(window.isOpaque)
        DialogChrome.configure(window)
        XCTAssertFalse(window.isOpaque)
        XCTAssertEqual(window.backgroundColor, .clear)
    }
}

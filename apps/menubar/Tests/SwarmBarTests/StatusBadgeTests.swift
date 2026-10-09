import AppKit
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

/// The corner dot is drawn on the status item's button, because MenuBarExtra drops any
/// SwiftUI overlay on its label (CHORE-54: no dot ever showed in the real menu bar).
@MainActor
final class StatusBadgeTests: XCTestCase {
    private func button() -> NSButton {
        let b = NSButton(frame: NSRect(x: 0, y: 0, width: 80, height: 22))
        b.isBordered = false
        b.image = NSImage(size: NSSize(width: 60, height: 16))
        b.imagePosition = .imageOnly
        return b
    }

    func testYellowDotSitsAtTopRightOfTheSwarmGlyph() throws {
        let b = button()
        StatusBadge.apply(.yellow, to: b)
        let dot = try XCTUnwrap(StatusBadge.view(in: b))
        XCTAssertEqual(dot.layer?.backgroundColor, NSColor.systemYellow.cgColor)
        XCTAssertEqual(dot.frame.size, NSSize(width: MenuBarLabelView.badgeSize, height: MenuBarLabelView.badgeSize))
        let image = b.cell!.imageRect(forBounds: b.bounds)
        XCTAssertEqual(dot.frame.minX, image.minX + MenuBarLabelView.badgeOffset.x, accuracy: 0.5)
        // Top edge of the dot meets the top edge of the label image (flipped or not).
        let top = b.isFlipped ? dot.frame.minY : b.bounds.height - dot.frame.maxY
        let imageTop = b.isFlipped ? image.minY : b.bounds.height - image.maxY
        XCTAssertEqual(top, imageTop + MenuBarLabelView.badgeOffset.y, accuracy: 0.5)
        XCTAssertTrue(b.bounds.contains(dot.frame), "dot must not be clipped by the button")
    }

    func testBadgeRecoloursAndClears() throws {
        let b = button()
        StatusBadge.apply(.green, to: b)
        XCTAssertEqual(StatusBadge.view(in: b)?.layer?.backgroundColor, NSColor.systemGreen.cgColor)
        StatusBadge.apply(.yellow, to: b)
        XCTAssertEqual(b.subviews.filter { $0 === StatusBadge.view(in: b) }.count, 1, "reuses one dot view")
        XCTAssertEqual(StatusBadge.view(in: b)?.layer?.backgroundColor, NSColor.systemYellow.cgColor)
        StatusBadge.apply(.none, to: b)
        XCTAssertNil(StatusBadge.view(in: b))
    }
}

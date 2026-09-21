import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class PanePreviewPanelRenderTests: XCTestCase {
    /// Drives `model` through exactly one capture (real Task.sleep is never used here — see
    /// PanePreviewTests for why a fake, bounded sleep is the safe way to end the poll loop) and
    /// returns it parked on the resulting status.
    private func model(client: MockDaemonClient, connected: @escaping @MainActor () -> Bool = { true }) async -> PanePreviewModel {
        let rec = BoundedSleep(stopAfter: 2)
        let m = PanePreviewModel(client: client, connected: connected, sleep: rec.sleep)
        m.hover("login-coder", anchor: .none)
        await m.pollTask?.value
        return m
    }

    func testLoadingStateRendersAtTheSpecSize() async {
        // stopAfter: 1 means the very first sleep call (the 400 ms delay) throws immediately,
        // so no capture ever runs and the model is left in its initial .loading status.
        let rec = BoundedSleep(stopAfter: 1)
        let client = MockDaemonClient()
        let m = PanePreviewModel(client: client, connected: { true }, sleep: rec.sleep)
        m.hover("login-coder", anchor: .none)
        await m.pollTask?.value
        XCTAssertEqual(m.status, .loading)
        let size = renderedSize(PanePreviewPanel(preview: m))
        XCTAssertEqual(size, PanePreviewPanel.size)
    }

    func testLoadedStateRendersAtTheSpecSize() async {
        let client = MockDaemonClient()
        client.paneResult = .success(PaneCapture(text: "$ swift test\nAll tests passed\n", tmuxAlive: true, lines: 40))
        let m = await model(client: client)
        guard case .text = m.status else { return XCTFail("expected .text, got \(m.status)") }
        XCTAssertEqual(renderedSize(PanePreviewPanel(preview: m)), PanePreviewPanel.size)
    }

    func testDeadPaneStateRendersAtTheSpecSize() async {
        let client = MockDaemonClient()
        client.paneResult = .success(PaneCapture(text: "error: 1 test failed\n", tmuxAlive: false, lines: 40))
        let m = await model(client: client)
        guard case .text(_, let alive) = m.status, alive == false else { return XCTFail("expected a dead-pane capture") }
        XCTAssertEqual(renderedSize(PanePreviewPanel(preview: m)), PanePreviewPanel.size)
    }

    func testPanelIsWideAndLongLinesDoNotChangeItsSize() async {
        XCTAssertEqual(PanePreviewPanel.size, CGSize(width: 760, height: 320))
        let client = MockDaemonClient()
        client.paneResult = .success(PaneCapture(text: String(repeating: "x", count: 400), tmuxAlive: true, lines: 40))
        let m = await model(client: client)
        guard case .text = m.status else { return XCTFail("expected .text, got \(m.status)") }
        XCTAssertEqual(renderedSize(PanePreviewPanel(preview: m)), PanePreviewPanel.size)
    }

    /// Where `text` draws inside the panel, in points from its top-left: the bounding box of the
    /// pixels that differ from a panel showing a blank pane. ImageRenderer draws no ScrollView
    /// content and exposes no text origin, so this hosts the panel in a real NSHostingView and
    /// diffs bitmaps (the header and glass are identical in both, so only the pane text differs).
    private func textBox(_ text: String) async throws -> CGRect? {
        func bitmap(_ text: String) async throws -> (px: [UInt8], w: Int, h: Int) {
            let client = MockDaemonClient()
            client.paneResult = .success(PaneCapture(text: text, tmuxAlive: true, lines: 40))
            let m = await model(client: client)
            let host = NSHostingView(rootView: PanePreviewPanel(preview: m))
            host.frame = CGRect(origin: .zero, size: PanePreviewPanel.size)
            let win = NSWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
            win.contentView = host
            win.alphaValue = 0
            win.orderFront(nil)
            defer { win.orderOut(nil) }
            host.layoutSubtreeIfNeeded()
            try await Task.sleep(for: .milliseconds(500)) // let SwiftUI's scroll view lay out and anchor its content
            host.layoutSubtreeIfNeeded()
            let rep = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
            host.cacheDisplay(in: host.bounds, to: rep)
            let img = try XCTUnwrap(rep.cgImage)
            var buf = [UInt8](repeating: 0, count: img.width * img.height * 4)
            let ctx = CGContext(data: &buf, width: img.width, height: img.height, bitsPerComponent: 8, bytesPerRow: img.width * 4,
                                space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
            ctx.draw(img, in: CGRect(x: 0, y: 0, width: img.width, height: img.height))
            return (buf, img.width, img.height)
        }
        let blank = try await bitmap(" ")
        let shown = try await bitmap(text)
        XCTAssertEqual(blank.w, shown.w)
        let scale = CGFloat(shown.w) / PanePreviewPanel.size.width // the bitmap is at backing scale
        var minX = Int.max, minY = Int.max, maxX = 0, maxY = 0
        for y in 0..<shown.h {
            for x in 0..<shown.w {
                let i = (y * shown.w + x) * 4
                if blank.px[i..<i + 4] != shown.px[i..<i + 4] { minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y) }
            }
        }
        if minX == Int.max { return nil }
        return CGRect(x: CGFloat(minX) / scale, y: CGFloat(minY) / scale,
                      width: CGFloat(maxX - minX + 1) / scale, height: CGFloat(maxY - minY + 1) / scale)
    }

    /// A ScrollView centres content smaller than its viewport. A short pane must still start at the
    /// 12pt left padding and sit at the bottom, where the newest lines are.
    func testShortPaneIsNotCentred() async throws {
        let drawn = try await textBox("XXXXXXXXXXXX\nXXXXXXXX\nXXXX")
        let box = try XCTUnwrap(drawn, "the short pane drew nothing")
        XCTAssertLessThan(box.minX, 16, "text starts at the left padding, not centred: \(box)")
        XCTAssertGreaterThan(box.maxY, PanePreviewPanel.size.height - 30, "text sits at the bottom of the panel: \(box)")
    }

    /// A 400-char line stays on one line (no wrap) and runs past the panel's right edge, with its
    /// left edge showing (the anchor is bottom-leading).
    func testLongLineDoesNotWrapAndShowsItsLeadingEdge() async throws {
        let drawn = try await textBox(String(repeating: "x", count: 400))
        let box = try XCTUnwrap(drawn, "the long pane drew nothing")
        XCTAssertLessThan(box.minX, 16, "left edge visible: \(box)")
        XCTAssertGreaterThan(box.maxX, PanePreviewPanel.size.width - 40, "the line fills the width: \(box)")
        XCTAssertLessThan(box.height, 20, "one line, not wrapped: \(box)")
    }

    func testFailedStateRendersAtTheSpecSize() async {
        let client = MockDaemonClient()
        client.failNext = .unreachable
        let m = await model(client: client)
        XCTAssertEqual(m.status, .failed(DaemonError.unreachable.message))
        XCTAssertEqual(renderedSize(PanePreviewPanel(preview: m)), PanePreviewPanel.size)
    }
}

/// A `EventStream.Sleep` fake that throws once `stopAfter` calls have been recorded, ending
/// `PanePreviewModel`'s poll loop deterministically with no real wall-clock wait (same technique
/// as `PanePreviewTests.SleepRecorder`, duplicated here rather than shared across test files for
/// one field: the render tests only ever need `stopAfter`, never the recorded durations).
final class BoundedSleep: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0
    let stopAfter: Int
    init(stopAfter: Int) { self.stopAfter = stopAfter }
    func sleep(_ d: Duration) async throws {
        try Task.checkCancellation()
        let n = lock.withLock { count += 1; return count }
        if n >= stopAfter { throw CancellationError() }
    }
}

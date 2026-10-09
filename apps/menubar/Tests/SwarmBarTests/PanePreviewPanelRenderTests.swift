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

    private struct Bitmap {
        var px: [UInt8], w: Int, h: Int
        var scale: CGFloat { CGFloat(w) / PanePreviewPanel.size.width } // the bitmap is at backing scale
        /// RGB of the pixel at a point (top-left origin, in points).
        func rgb(atX x: CGFloat, y: CGFloat) -> [Int] {
            let i = (Int(y * scale) * w + Int(x * scale)) * 4
            return [Int(px[i]), Int(px[i + 1]), Int(px[i + 2])]
        }
    }

    /// The hosted panel for a capture, drawn to pixels. ImageRenderer draws no ScrollView content,
    /// so this hosts the panel in a real NSHostingView (in an invisible window, so SwiftUI lays the
    /// scroll view out and applies its anchor) and snapshots it.
    private func bitmap(_ capture: PaneCapture) async throws -> Bitmap {
        let client = MockDaemonClient()
        client.paneResult = .success(capture)
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
        return Bitmap(px: buf, w: img.width, h: img.height)
    }

    /// Where `capture`'s text draws inside the panel, in points from its top-left: the bounding box
    /// of the pixels that differ from a panel showing a blank pane (the header and glass are
    /// identical in both, so only the pane text differs). nil when nothing differs.
    private func textBox(_ capture: PaneCapture) async throws -> CGRect? {
        let blank = try await bitmap(PaneCapture(text: " "))
        let shown = try await bitmap(capture)
        XCTAssertEqual(blank.w, shown.w)
        var minX = Int.max, minY = Int.max, maxX = 0, maxY = 0
        for y in 0..<shown.h {
            for x in 0..<shown.w {
                let i = (y * shown.w + x) * 4
                if blank.px[i..<i + 4] != shown.px[i..<i + 4] { minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y) }
            }
        }
        if minX == Int.max { return nil }
        let scale = shown.scale
        return CGRect(x: CGFloat(minX) / scale, y: CGFloat(minY) / scale,
                      width: CGFloat(maxX - minX + 1) / scale, height: CGFloat(maxY - minY + 1) / scale)
    }

    /// A ScrollView centres content smaller than its viewport. A short pane must still start at the
    /// 12pt left padding and sit at the bottom, where the newest lines are.
    func testShortPaneIsNotCentred() async throws {
        let drawn = try await textBox(PaneCapture(text: "XXXXXXXXXXXX\nXXXXXXXX\nXXXX"))
        let box = try XCTUnwrap(drawn, "the short pane drew nothing")
        XCTAssertLessThan(box.minX, 24, "text starts at the left padding, not centred: \(box)")
        XCTAssertGreaterThan(box.maxY, PanePreviewPanel.size.height - 30, "text sits at the bottom of the panel: \(box)")
    }

    /// A 400-char line stays on one line (no wrap) and runs past the panel's right edge, with its
    /// left edge showing (the anchor is bottom-leading).
    func testLongLineDoesNotWrapAndShowsItsLeadingEdge() async throws {
        let drawn = try await textBox(PaneCapture(text: String(repeating: "x", count: 400)))
        let box = try XCTUnwrap(drawn, "the long pane drew nothing")
        XCTAssertLessThan(box.minX, 24, "left edge visible: \(box)")
        XCTAssertGreaterThan(box.maxX, PanePreviewPanel.size.width - 40, "the line fills the width: \(box)")
        XCTAssertLessThan(box.height, 20, "one line, not wrapped: \(box)")
    }

    func testAnsiCaptureRendersAtTheSpecSize() async {
        let client = MockDaemonClient()
        client.paneResult = .success(PaneCapture(text: "red", ansi: "\u{1B}[31mred\u{1B}[0m", tmuxAlive: true, lines: 40))
        let m = await model(client: client)
        XCTAssertEqual(m.status, .text("\u{1B}[31mred\u{1B}[0m", tmuxAlive: true))
        XCTAssertEqual(renderedSize(PanePreviewPanel(preview: m)), PanePreviewPanel.size)
    }

    /// The escapes must be parsed, not drawn: the coloured capture takes exactly the room its
    /// visible characters take.
    func testEscapesAreNotDrawn() async throws {
        let plain = try await textBox(PaneCapture(text: "XXXX"))
        let coloured = try await textBox(PaneCapture(text: "XXXX", ansi: "\u{1B}[31mXX\u{1B}[0mXX\u{1B}[?25l\u{1B}]0;title\u{07}"))
        let p = try XCTUnwrap(plain), c = try XCTUnwrap(coloured)
        XCTAssertEqual(c.width, p.width, accuracy: 1, "plain \(p) vs coloured \(c)")
        XCTAssertEqual(c.minX, p.minX, accuracy: 1)
    }

    /// SGR colour reaches the pixels: a red run puts strongly red pixels on the dark fill.
    func testSGRColourReachesThePixels() async throws {
        let b = try await bitmap(PaneCapture(text: "\u{2588}\u{2588}\u{2588}\u{2588}", ansi: "\u{1B}[31m\u{2588}\u{2588}\u{2588}\u{2588}\u{1B}[0m"))
        var red = 0
        for i in stride(from: 0, to: b.px.count, by: 4) where b.px[i] > 150 && b.px[i + 1] < 90 && b.px[i + 2] < 90 { red += 1 }
        XCTAssertGreaterThan(red, 50, "no #CD3131 pixels in the render")
    }

    /// TUI palettes assume a dark screen: the text area is #1E1E1E in light and dark mode.
    func testTextAreaHasTheDarkFill() async throws {
        let b = try await bitmap(PaneCapture(text: " "))
        for (x, y) in [(60, 90), (380, 160), (700, 290)] {
            let c = b.rgb(atX: CGFloat(x), y: CGFloat(y))
            XCTAssertTrue(c.allSatisfy { abs($0 - 0x1E) <= 2 }, "pixel (\(x),\(y)) is \(c), expected #1E1E1E")
        }
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

@MainActor
final class PanePreviewContextRenderTests: XCTestCase {
    private func loaded() async -> PanePreviewModel {
        let client = MockDaemonClient()
        client.paneResult = .success(PaneCapture(text: "$ swift test\nAll tests passed\n", tmuxAlive: true, lines: 40))
        let rec = BoundedSleep(stopAfter: 2)
        let m = PanePreviewModel(client: client, connected: { true }, sleep: rec.sleep)
        m.hover("login-coder", anchor: .none)
        await m.pollTask?.value
        return m
    }

    /// The header text, bar and tint are asserted on `ContextReadout` and `Copy.paneHeader`; here the panel
    /// must keep its fixed size with and without the segment and bar (OCR can't read 11 pt glass text).
    func testContextHeaderKeepsTheSpecSize() async {
        let m = await loaded()
        for (tokens, window) in [(120_000, 1_000_000), (96_000, nil), (950_000, 1_000_000)] as [(Int, Int?)] {
            let header = AgentHeader(kind: .claude, model: "opus", effort: "high", itemKey: "TASK-101",
                                     contextTokens: tokens, contextWindow: window)
            XCTAssertEqual(renderedSize(PanePreviewPanel(preview: m, lookup: { _ in header })), PanePreviewPanel.size)
        }
        let none = AgentHeader(kind: .claude, model: "opus", effort: "high", itemKey: "TASK-101")
        XCTAssertEqual(renderedSize(PanePreviewPanel(preview: m, lookup: { _ in none })), PanePreviewPanel.size)
    }

    /// Evidence shots (light/dark, default and largest text, three tints) when SWARM_NATIVE_POLISH_EVIDENCE_DIR is set.
    func testCaptureContextHeaderEvidence() async throws {
        guard let dir = ProcessInfo.processInfo.environment["SWARM_NATIVE_POLISH_EVIDENCE_DIR"] else {
            throw XCTSkip("set SWARM_NATIVE_POLISH_EVIDENCE_DIR to capture")
        }
        let m = await loaded()
        let cases: [(String, Int, Int?)] = [("ok", 120_000, 1_000_000), ("amber", 700_000, 1_000_000),
                                            ("red", 258_000, 258_400), ("nowindow", 96_000, nil)]
        for dark in [false, true] {
            for large in [false, true] {
                for (name, tokens, window) in cases {
                    let header = AgentHeader(kind: .claude, model: "opus", effort: "high", itemKey: "TASK-101",
                                             contextTokens: tokens, contextWindow: window)
                    let panel = PanePreviewPanel(preview: m, lookup: { _ in header })
                        .dynamicTypeSize(large ? .accessibility5 : .large)
                    let host = NSHostingView(rootView: panel)
                    host.frame = NSRect(origin: .zero, size: PanePreviewPanel.size)
                    host.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
                    // cacheDisplay (not screencapture): works headless, omits only the glass material behind the text.
                    host.layoutSubtreeIfNeeded()
                    let rep = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
                    host.cacheDisplay(in: host.bounds, to: rep)
                    let png = try XCTUnwrap(rep.representation(using: .png, properties: [:]))
                    try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(
                        "pane-context-\(name)-\(dark ? "dark" : "light")-\(large ? "xxl" : "default").png"))
                }
            }
        }
    }
}

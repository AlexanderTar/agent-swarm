import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class NativeFormPolishTests: XCTestCase {
    func descendants(_ view: NSView) -> [NSView] {
        [view] + view.subviews.flatMap(descendants)
    }

    func testRequestHasRoundedTranslucentSurface() throws {
        let host = NSHostingView(rootView: RequestEditor(text: .constant("")))
        host.frame = NSRect(x: 0, y: 0, width: 400, height: 140)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: bitmap)
        // A square border paints the corner; a rounded field leaves it clear.
        let corner = try XCTUnwrap(bitmap.colorAt(x: 0, y: 0))
        XCTAssertLessThan(corner.alphaComponent, 0.1)
        XCTAssertGreaterThan(try XCTUnwrap(bitmap.colorAt(x: 200, y: 70)).alphaComponent, 0.2,
                             "the editor needs a translucent field fill, like Name")
        let editor = try XCTUnwrap(descendants(host).compactMap { $0 as? NSTextView }.first)
        XCTAssertFalse(editor.drawsBackground)
        XCTAssertTrue(editor.isEditable)
    }

    func testRepositorySelectionStaysBlueWithoutKeyboardFocus() throws {
        let host = NSHostingView(rootView: RepoChooser(rows: [
            Repo(id: "a", name: "repo-a", path: "/tmp/a"),
            Repo(id: "b", name: "repo-b", path: "/tmp/b")
        ], selection: .constant(["a", "b"])))
        host.frame = NSRect(x: 0, y: 0, width: 500, height: 60)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        let window = NSWindow(contentRect: host.bounds, styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = host
        window.orderFront(nil)
        defer { window.close() }
        RunLoop.main.run(until: Date().addingTimeInterval(0.15))
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("repo-\(UUID().uuidString).png")
        defer { try? FileManager.default.removeItem(at: url) }
        let capture = Process()
        capture.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        capture.arguments = ["-x", "-o", "-l", "\(window.windowNumber)", url.path]
        try capture.run()
        capture.waitUntilExit()
        XCTAssertEqual(capture.terminationStatus, 0)
        let bitmap = try XCTUnwrap(NSBitmapImageRep(data: Data(contentsOf: url)))
        try Data(contentsOf: url).write(to: URL(fileURLWithPath: "/tmp/task435-repo.png"))
        let colors = (0..<bitmap.pixelsHigh).flatMap { y in
            (0..<bitmap.pixelsWide).compactMap { x in bitmap.colorAt(x: x, y: y)?.usingColorSpace(.deviceRGB) }
        }
        XCTAssertGreaterThan(colors.filter { $0.blueComponent > $0.redComponent + 0.25 && $0.blueComponent > $0.greenComponent + 0.1 }.count,
                             5000, "both selected rows need a persistent blue surface")
    }

    func testIntentUsesNativeAutomaticSegmentedControl() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let form = makeAppModel(client).makeNewOrchestratorForm()
        await form.load()
        let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 790)
        host.layoutSubtreeIfNeeded()
        let control = try XCTUnwrap(descendants(host).compactMap { $0 as? NSSegmentedControl }.first)
        XCTAssertEqual(control.segmentStyle, .automatic, "automatic AppKit styling adopts native glass on macOS 26+")
        if #available(macOS 26, *) { XCTAssertEqual(control.borderShape, .capsule) }
        control.selectedSegment = 2
        control.sendAction(control.action, to: control.target)
        XCTAssertEqual(form.intent, .debug)
        XCTAssertEqual(control.accessibilityLabel(), Copy.intent)
    }
}

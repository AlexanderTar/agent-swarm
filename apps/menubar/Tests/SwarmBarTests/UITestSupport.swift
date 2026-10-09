import AppKit
import SwiftUI
import Vision
import XCTest
@testable import SwarmBarKit

/// Shared by the native render smoke tests. AppKit hosting includes text fields,
/// pickers and tab views; real-window captures separately verify composited glass.
@MainActor
func makeAppModel(_ client: MockDaemonClient) -> AppModel {
    let terminals = Terminals(runner: FakeRunner(), script: FakeScript(), ghosttyPIDs: { [] })
    return AppModel(client: client,
                    endpoint: DaemonEndpoint(baseURL: URL(string: "http://127.0.0.1:7777")!, tokenFile: URL(fileURLWithPath: "/nonexistent")),
                    terminals: terminals, poster: FakePoster(), defaults: MemoryStore(),
                    cache: StateCache(url: FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)),
                    connect: { _ in throw URLError(.cannotConnectToHost) }, now: { fixtureNow }, openURL: { _ in })
}

@MainActor
func renderedSize<V: View>(_ view: V) -> CGSize {
    let host = NSHostingView(rootView: view)
    let size = host.fittingSize
    guard size.width > 0, size.height > 0 else { return .zero }
    host.frame = NSRect(origin: .zero, size: size)
    host.layoutSubtreeIfNeeded()
    guard let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { return .zero }
    host.cacheDisplay(in: host.bounds, to: bitmap)
    return bitmap.cgImage == nil ? .zero : size
}

/// Capture AppKit/SwiftUI composited pixels. cacheDisplay omits layer-backed List rows
/// and Liquid Glass, so these assertions need an actual window on the test desktop.
@MainActor
func captureNativeWindow(_ host: NSView, name: String) throws -> NSBitmapImageRep {
    let window = NSWindow(contentRect: host.bounds, styleMask: [.titled], backing: .buffered, defer: false)
    window.isReleasedWhenClosed = false
    window.contentView = host
    window.orderFront(nil)
    defer { window.close() }
    RunLoop.main.run(until: Date().addingTimeInterval(0.2))
    let data = try screencaptureWindow(window.windowNumber, prefix: "native")
    if let dir = ProcessInfo.processInfo.environment["SWARM_NATIVE_POLISH_EVIDENCE_DIR"] {
        try data.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }
    guard let bitmap = NSBitmapImageRep(data: data) else { throw CocoaError(.fileReadCorruptFile) }
    return bitmap
}

/// Screenshot scratch directory. `FileManager.temporaryDirectory` ignores $TMPDIR, and agent
/// sandboxes may deny writes to /var/folders/.../T, so prefer $TMPDIR, then the package's .build.
func screenshotDirectory() -> URL {
    let fm = FileManager.default
    var candidates: [URL] = []
    if let tmp = ProcessInfo.processInfo.environment["TMPDIR"], !tmp.isEmpty {
        candidates.append(URL(fileURLWithPath: tmp, isDirectory: true))
    }
    let package = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    candidates.append(package.appendingPathComponent(".build", isDirectory: true))
    candidates.append(fm.temporaryDirectory)
    // access(2) misses sandbox denials, so probe with a real write; non-atomic, because an
    // atomic write (and createFile) stages through the very temporary directory being avoided.
    return candidates.first { dir in
        let probe = dir.appendingPathComponent(".write-probe-\(UUID().uuidString)")
        guard (try? Data().write(to: probe)) != nil else { return false }
        try? fm.removeItem(at: probe)
        return true
    } ?? fm.temporaryDirectory
}

struct ScreencaptureError: Error, CustomStringConvertible {
    let description: String
}

/// PNG bytes of one window via /usr/sbin/screencapture, which exits 0 even when it cannot write
/// its output file; a missing file fails with the path and screencapture's stderr.
func screencaptureWindow(_ windowNumber: Int, prefix: String) throws -> Data {
    let url = screenshotDirectory().appendingPathComponent("\(prefix)-\(UUID().uuidString).png")
    defer { try? FileManager.default.removeItem(at: url) }
    let capture = Process()
    let stderr = Pipe()
    capture.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
    capture.arguments = ["-x", "-o", "-l", "\(windowNumber)", url.path]
    capture.standardError = stderr
    try capture.run()
    capture.waitUntilExit()
    let message = String(decoding: stderr.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        .trimmingCharacters(in: .whitespacesAndNewlines)
    guard capture.terminationStatus == 0, FileManager.default.fileExists(atPath: url.path) else {
        throw ScreencaptureError(description: "screencapture (exit \(capture.terminationStatus)) wrote no file at \(url.path); stderr: \(message.isEmpty ? "<empty>" : message)")
    }
    return try Data(contentsOf: url)
}

/// Fragments of every red picker validation message (`CatalogRules.validate`).
let pickerErrorFragments = ["installed on this Mac", "signed in", "superpowers plugin", "no longer offered",
                            "Choose a model", "Choose an agent"]

/// OCR of a hosted view: plain SwiftUI `Text` has no backing `NSView` to inspect.
@MainActor
func ocrText(_ host: NSView) throws -> String {
    host.layoutSubtreeIfNeeded()
    let rep = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
    host.cacheDisplay(in: host.bounds, to: rep)
    let request = VNRecognizeTextRequest()
    try VNImageRequestHandler(cgImage: try XCTUnwrap(rep.cgImage), options: [:]).perform([request])
    return (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: " ")
}

/// Asserts no picker validation error is visible in `text`.
func assertNoPickerErrors(_ text: String, _ state: String, file: StaticString = #filePath, line: UInt = #line) {
    for fragment in pickerErrorFragments where text.localizedCaseInsensitiveContains(fragment) {
        XCTFail("\(state): red validation error \"\(fragment)\" visible in: \(text)", file: file, line: line)
    }
}

/// Waits (by yielding) until the mock has recorded `call`.
@MainActor
func waitForCall(_ client: MockDaemonClient, _ call: String) async {
    for _ in 0..<1000 where !client.calls.contains(call) { await Task.yield() }
}

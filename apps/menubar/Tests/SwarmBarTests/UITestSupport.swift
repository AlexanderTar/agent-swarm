import AppKit
import SwiftUI
@testable import SwarmBarKit

/// Shared by the render smoke tests. Views are excluded from the coverage gate; these tests render
/// each surface in its main states so a crash or an empty layout fails the run. ImageRenderer draws
/// AppKit-backed controls (text fields, pickers, borderless buttons) as placeholders, so the look of
/// the app is checked by hand (plan Task 20).
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
    let r = ImageRenderer(content: view)
    return r.nsImage?.size ?? .zero
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
    let url = FileManager.default.temporaryDirectory.appendingPathComponent("native-\(UUID().uuidString).png")
    defer { try? FileManager.default.removeItem(at: url) }
    let capture = Process()
    capture.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
    capture.arguments = ["-x", "-o", "-l", "\(window.windowNumber)", url.path]
    try capture.run()
    capture.waitUntilExit()
    guard capture.terminationStatus == 0 else { throw CocoaError(.fileReadUnknown) }
    let data = try Data(contentsOf: url)
    if let dir = ProcessInfo.processInfo.environment["SWARM_NATIVE_POLISH_EVIDENCE_DIR"] {
        try data.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }
    guard let bitmap = NSBitmapImageRep(data: data) else { throw CocoaError(.fileReadCorruptFile) }
    return bitmap
}

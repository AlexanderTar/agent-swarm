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

import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

/// Writes light/dark × default/largest-text captures of the popover leaf toggle (cacheDisplay can't draw the Settings checkbox offscreen) to
/// `SWARM_NATIVE_POLISH_EVIDENCE_DIR`; skipped when it is unset.
@MainActor
final class LowTokenEvidenceTests: XCTestCase {
    private func shoot<V: View>(_ view: V, width: CGFloat, _ name: String) throws {
        for (scheme, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
            for (size, label) in [(DynamicTypeSize.large, "default"), (.accessibility3, "large")] {
                let host = NSHostingView(rootView: view.environment(\.dynamicTypeSize, size).frame(width: width))
                host.appearance = NSAppearance(named: appearance)
                host.wantsLayer = true
                host.frame = NSRect(origin: .zero, size: host.fittingSize)
                host.layoutSubtreeIfNeeded()
                let bitmap = try XCTUnwrap(host.bitmapImageRepForCachingDisplay(in: host.bounds))
                host.cacheDisplay(in: host.bounds, to: bitmap)
                let dir = try XCTUnwrap(ProcessInfo.processInfo.environment["SWARM_NATIVE_POLISH_EVIDENCE_DIR"])
                try XCTUnwrap(bitmap.representation(using: .png, properties: [:]))
                    .write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(scheme)-\(label).png"))
            }
        }
    }

    func testCaptureLowTokenStates() async throws {
        try XCTSkipUnless(ProcessInfo.processInfo.environment["SWARM_NATIVE_POLISH_EVIDENCE_DIR"] != nil)
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        try shoot(AgentsSection(model: m, cap: 400), width: 360, "popover-header-on")
        var off: StateResponse = try Fixture.decode("state.json")
        off.settings.lowTokenMode = false
        client.stateResult = .success(off)
        await m.refresh()
        try shoot(AgentsSection(model: m, cap: 400), width: 360, "popover-header-off")
        client.stateResult = .failure(.unreachable)
        await m.refresh()
        try shoot(AgentsSection(model: m, cap: 400), width: 360, "popover-disconnected")
    }
}

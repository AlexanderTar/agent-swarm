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

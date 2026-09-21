import Foundation
import XCTest
@testable import SwarmBarKit

/// A controllable fake for `EventStream.Sleep`: records every requested duration and, once
/// `stopAfter` calls have been recorded, throws `CancellationError` to end the poll loop
/// deterministically — the same technique `EventStreamTests` uses for `EventStream.run()`.
final class SleepRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var _durations: [Duration] = []
    var stopAfter = Int.max

    var durations: [Duration] { lock.withLock { _durations } }

    func sleep(_ d: Duration) async throws {
        // A real Task.sleep throws immediately for an already-cancelled task; a fake that
        // didn't check this would let a cancelled hover's delay "complete" anyway and
        // record a spurious duration (agent-hover-preview spec, decision 6's swap test).
        try Task.checkCancellation()
        let n = lock.withLock { _durations.append(d); return _durations.count }
        if n >= stopAfter { throw CancellationError() }
    }
}

@MainActor
final class PanePreviewModelTests: XCTestCase {
    private func anchor(_ x: CGFloat) -> PanePreviewModel.Anchor {
        PanePreviewModel.Anchor(row: CGRect(x: x, y: 0, width: 1, height: 1), host: .zero, screen: .zero)
    }

    /// Runs one hover to completion of exactly its first capture (the fake sleep throws on its
    /// second call, right after the first capture lands, ending the loop) and returns the
    /// resulting status.
    private func statusAfterOneCapture(client: MockDaemonClient,
                                       connected: @escaping @MainActor () -> Bool = { true }) async -> PanePreviewModel.Status {
        let rec = SleepRecorder()
        rec.stopAfter = 2
        let model = PanePreviewModel(client: client, connected: connected, sleep: rec.sleep)
        model.hover("agent-1", anchor: .none)
        await model.pollTask?.value
        return model.status
    }

    // MARK: - debounce / cancel / swap

    func testHoverCancelledBeforeTheFirstDelayElapsesPerformsZeroCalls() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("a", anchor: anchor(0))
        let t = model.pollTask
        model.leave("a") // cancels the task before its Task body ever runs
        await t?.value // model.pollTask is already nil after leave(); await the captured task itself
        XCTAssertEqual(mock.calls, [])
        XCTAssertNil(model.agent)
    }

    /// Uses the fake sleep (never a real wall-clock wait) and bounds the loop with `stopAfter`,
    /// the same technique `EventStreamTests` uses for `EventStream.run()`: a hover held through
    /// the delay and two poll intervals captures three times.
    func testHeldHoverCapturesOncePerPollIntervalUntilStopped() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        rec.stopAfter = 4
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("running-agent", anchor: anchor(0))
        await model.pollTask?.value
        XCTAssertEqual(mock.calls.filter { $0.hasPrefix("pane ") }.count, 3)
        XCTAssertEqual(rec.durations, [PanePreviewModel.firstCaptureDelay, PanePreviewModel.pollInterval,
                                       PanePreviewModel.pollInterval, PanePreviewModel.pollInterval])
    }

    func testHoveringANewRowWhileAnEarlierHoverIsPendingSwapsWithoutTheInitialDelay() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("a", anchor: anchor(0))
        model.hover("b", anchor: anchor(5))
        XCTAssertEqual(model.agent, "b")
        XCTAssertEqual(model.anchor, anchor(5))
        XCTAssertEqual(model.status, .loading, "a's text is cleared in the same tick hover(b) is called")

        rec.stopAfter = 1 // let exactly b's first capture happen, then stop the loop
        await model.pollTask?.value
        XCTAssertEqual(mock.calls, ["pane b 40"], "a's pending hover never captured; b did, with no delay")
        XCTAssertEqual(rec.durations, [PanePreviewModel.pollInterval], "no firstCaptureDelay: a hover was already active")
    }

    /// Hovering A, then B, then A again reuses the name "A", so a name-only guard on a stale
    /// in-flight response from the *first* A hover would wrongly pass once the cursor returns
    /// to A. The mock parks that first capture at a gate; by the time it's released, a second
    /// A hover has already started and completed its own capture — the generation guard must
    /// keep the stale one from overwriting it (agent-hover-preview spec review, item 3).
    func testStaleResponseFromAnEarlierHoverOfTheSameAgentNeverOverwritesALaterOne() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)

        mock.paneResult = .success(PaneCapture(text: "stale-a", tmuxAlive: true, lines: 40))
        mock.holdPane = ["a"]
        model.hover("a", anchor: anchor(0))
        let staleTask = model.pollTask
        while mock.calls.isEmpty { await Task.yield() } // wait until the first a's capture is parked at the gate
        XCTAssertEqual(mock.calls, ["pane a 40"])

        mock.holdPane = [] // only that in-flight call is held; later pane calls resolve immediately
        model.hover("b", anchor: anchor(5))
        mock.paneResult = .success(PaneCapture(text: "fresh-a", tmuxAlive: true, lines: 40))
        rec.stopAfter = 2 // stop the second a hover's loop right after its own capture
        model.hover("a", anchor: anchor(10)) // revisits "a" while the first a's response is still parked
        await model.pollTask?.value
        XCTAssertEqual(model.status, .text("fresh-a", tmuxAlive: true), "the second a hover's own capture")

        mock.releasePane("a") // now let the stale first-a response land
        await staleTask?.value
        XCTAssertEqual(model.status, .text("fresh-a", tmuxAlive: true),
                       "a stale response from a superseded hover generation must not overwrite the current one")
    }

    func testLeaveIsANoOpWhenItArrivesForARowThatIsNoLongerHovered() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("a", anchor: anchor(0))
        model.hover("b", anchor: anchor(5))
        model.leave("a") // stale leave for the row the cursor already left
        XCTAssertEqual(model.agent, "b", "leave(\"a\") must not cancel b's hover")
        model.cancel()
        await model.pollTask?.value
    }

    // MARK: - error mapping (decision 9's table)

    func testDaemonErrorUnreachableShowsDaemonUnavailable() async {
        let mock = MockDaemonClient()
        mock.failNext = .unreachable
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .failed(DaemonError.unreachable.message))
    }

    func testDisconnectedShowsDaemonUnavailableEvenWithoutAThrownError() async {
        let mock = MockDaemonClient()
        let status = await statusAfterOneCapture(client: mock, connected: { false })
        XCTAssertEqual(status, .failed(DaemonError.unreachable.message))
    }

    func testTimedOutShowsThePaneSlowCopy() async {
        let mock = MockDaemonClient()
        mock.failNext = .timedOut
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .failed(Copy.paneSlow))
    }

    func test404ShowsUnknownAgentCopy() async {
        let mock = MockDaemonClient()
        mock.failNext = .api(status: 404, code: "not_found", message: "No agent named agent-1.")
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .failed(Copy.paneUnknownAgent))
    }

    func test409ShowsNoSessionCopy() async {
        let mock = MockDaemonClient()
        mock.failNext = .api(status: 409, code: "conflict", message: "That agent has no session.")
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .failed(Copy.paneNoSession))
    }

    func test502TmuxUnreachableShowsCantReachTmuxCopy() async {
        let mock = MockDaemonClient()
        mock.failNext = .api(status: 502, code: "tmux_unreachable", message: "Can't reach tmux.")
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .failed(Copy.paneTmuxUnreachable))
    }

    func testAnyOtherAPIErrorShowsTheDaemonsOwnMessage() async {
        let mock = MockDaemonClient()
        mock.failNext = .api(status: 500, code: "internal", message: "Something went wrong.")
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .failed("Something went wrong."))
    }

    func testCancellationErrorDropsSilentlyWithoutOverwritingStatus() async {
        let mock = MockDaemonClient()
        mock.failNextWith = CancellationError()
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .loading, "a cancelled capture must never surface as an error")
    }

    func testSuccessfulCaptureShowsTextAndTmuxAlive() async {
        let mock = MockDaemonClient()
        mock.paneResult = .success(PaneCapture(text: "hello", tmuxAlive: false, lines: 40))
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .text("hello", tmuxAlive: false))
    }

    func testCaptureWithAnsiShowsTheRawAnsi() async {
        let mock = MockDaemonClient()
        mock.paneResult = .success(PaneCapture(text: "plain", ansi: "\u{1B}[31mred\u{1B}[0m"))
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .text("\u{1B}[31mred\u{1B}[0m", tmuxAlive: true))
    }

    func testCaptureWithoutAnsiFallsBackToPlainText() async {
        let mock = MockDaemonClient()
        mock.paneResult = .success(PaneCapture(text: "plain", ansi: nil))
        let status = await statusAfterOneCapture(client: mock)
        XCTAssertEqual(status, .text("plain", tmuxAlive: true))
    }

    func testPaneCaptureDecodesWithAndWithoutTheAnsiField() throws {
        let old = try SwarmJSON.decode(PaneCapture.self, from: Data(#"{"text":"a","tmux_alive":true,"lines":40}"#.utf8))
        XCTAssertNil(old.ansi)
        let new = try SwarmJSON.decode(PaneCapture.self, from: Data(#"{"text":"a","ansi":"\u001b[1ma","tmux_alive":true,"lines":40}"#.utf8))
        XCTAssertEqual(new.ansi, "\u{1B}[1ma")
    }

    // MARK: - teardown

    func testCancelStopsThePollLoopAndClearsTheHover() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("a", anchor: anchor(0))
        let t = model.pollTask
        model.cancel()
        await t?.value // model.pollTask is already nil after cancel(); await the captured task itself
        XCTAssertNil(model.agent)
        XCTAssertEqual(model.anchor, .none)
        XCTAssertEqual(mock.calls, [])
    }

    // MARK: - panel hover & dismiss delay

    func testLeaveWhileLoadedDelaysDismissal() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        rec.stopAfter = 2
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("a", anchor: anchor(0))
        await model.pollTask?.value
        XCTAssertEqual(model.agent, "a")

        // Leave while loaded: dismissTask is started, agent is NOT immediately cleared
        rec.stopAfter = Int.max
        model.leave("a")
        XCTAssertEqual(model.agent, "a")
        XCTAssertNotNil(model.dismissTask)

        // Once dismissTask finishes, hover is cleared
        await model.dismissTask?.value
        XCTAssertNil(model.agent)
        XCTAssertEqual(model.anchor, .none)
    }

    func testEnterPanelCancelsDismissAndLeavesPanelRestartsDismiss() async throws {
        let mock = MockDaemonClient()
        let rec = SleepRecorder()
        rec.stopAfter = 2
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("a", anchor: anchor(0))
        await model.pollTask?.value

        // Leave row: dismiss started
        rec.stopAfter = Int.max
        model.leave("a")
        XCTAssertNotNil(model.dismissTask)

        // Enter panel: dismiss cancelled
        model.enterPanel()
        XCTAssertTrue(model.isInsidePanel)
        XCTAssertNil(model.dismissTask)
        XCTAssertEqual(model.agent, "a")

        // Leave panel: dismiss restarted
        model.leavePanel()
        XCTAssertFalse(model.isInsidePanel)
        XCTAssertNotNil(model.dismissTask)

        await model.dismissTask?.value
        XCTAssertNil(model.agent)
    }

    func testHoverSameAgentWhileLoadedPreservesStatusAndCancelsDismiss() async throws {
        let mock = MockDaemonClient()
        mock.paneResult = .success(PaneCapture(text: "capture for a", tmuxAlive: true, lines: 40))
        let rec = SleepRecorder()
        rec.stopAfter = 2
        let model = PanePreviewModel(client: mock, connected: { true }, sleep: rec.sleep)
        model.hover("a", anchor: anchor(0))
        await model.pollTask?.value
        XCTAssertEqual(model.status, .text("capture for a", tmuxAlive: true))

        rec.stopAfter = Int.max
        model.leave("a")
        XCTAssertNotNil(model.dismissTask)

        // Re-hovering same agent cancels dismiss and preserves status
        model.hover("a", anchor: anchor(10))
        XCTAssertNil(model.dismissTask)
        XCTAssertEqual(model.anchor, anchor(10))
        XCTAssertEqual(model.status, .text("capture for a", tmuxAlive: true))
    }
}

final class PanePreviewGeometryTests: XCTestCase {
    let size = CGSize(width: 520, height: 320)
    let screen = CGRect(x: 0, y: 0, width: 1440, height: 900)

    func testDefaultsLeftOfThePopoverAlignedToTheRowsTopEdge() {
        let a = PanePreviewModel.Anchor(row: CGRect(x: 800, y: 500, width: 40, height: 40),
                                        host: CGRect(x: 800, y: 400, width: 300, height: 200),
                                        screen: screen)
        let f = PanePreviewGeometry.frame(a, size: size, gap: 8)
        XCTAssertEqual(f.origin.x, 800 - 8 - 520)
        XCTAssertEqual(f.origin.y, 540 - 320) // row.maxY (500 + 40) - size.height
        XCTAssertEqual(f.size, size)
    }

    func testFlipsRightWhenTheLeftPlacementWouldGoOffTheLeftEdge() {
        let a = PanePreviewModel.Anchor(row: CGRect(x: 10, y: 500, width: 40, height: 40),
                                        host: CGRect(x: 10, y: 400, width: 300, height: 200),
                                        screen: screen)
        let f = PanePreviewGeometry.frame(a, size: size, gap: 8)
        XCTAssertEqual(f.origin.x, 10 + 300 + 8, "flips to host.maxX + gap")
    }

    func testClampsToTheRightEdgeEvenAfterFlipping() {
        let a = PanePreviewModel.Anchor(row: CGRect(x: 10, y: 500, width: 40, height: 40),
                                        host: CGRect(x: 10, y: 400, width: 1420, height: 200),
                                        screen: screen)
        let f = PanePreviewGeometry.frame(a, size: size, gap: 8)
        XCTAssertEqual(f.origin.x, screen.maxX - size.width)
    }

    func testClampsToTheBottomOfVisibleFrame() {
        let a = PanePreviewModel.Anchor(row: CGRect(x: 800, y: 10, width: 40, height: 40),
                                        host: CGRect(x: 800, y: 0, width: 300, height: 200),
                                        screen: screen)
        let f = PanePreviewGeometry.frame(a, size: size, gap: 8)
        XCTAssertEqual(f.origin.y, screen.minY)
    }

    func testClampsToTheTopOfVisibleFrame() {
        let a = PanePreviewModel.Anchor(row: CGRect(x: 800, y: 890, width: 40, height: 40),
                                        host: CGRect(x: 800, y: 800, width: 300, height: 100),
                                        screen: screen)
        let f = PanePreviewGeometry.frame(a, size: size, gap: 8)
        XCTAssertEqual(f.origin.y, screen.maxY - size.height)
    }
}

/// Suspends the current task for `duration`; throws `CancellationError` if it is cancelled first.
///
/// Use this, never `Task.sleep(for:)` or a clock's `sleep`, anywhere in the menu bar package. With
/// Swift 6.3 (Xcode 26.6, CI's toolchain) each module emits its own specialization of the generic
/// `Task.sleep(for:tolerance:clock:)`, the linker keeps one, and a task whose frame was sized by
/// another aborts in `swift_task_dealloc` with "freed pointer was not the last allocation"
/// (swiftlang/swift#86204). `Task.sleep(nanoseconds:)` is not generic, so it has no such copies.
/// `make test-menubar` fails on any `.sleep(for:` or `.sleep(until:` call; see docs/ci.md.
public func taskSleep(_ duration: Duration) async throws {
    let (seconds, attoseconds) = duration.components
    let nanoseconds = UInt64(max(seconds, 0)) * 1_000_000_000 + UInt64(max(attoseconds, 0) / 1_000_000_000)
    try await Task.sleep(nanoseconds: nanoseconds)
}

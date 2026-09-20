import Foundation
import Observation
#if canImport(CoreGraphics)
import CoreGraphics
#endif

/// The hover preview's state and its poll loop. Owned by AppModel; the
/// floating panel in SwarmBar observes `agent`/`status`/`anchor` and nothing else.
@MainActor
@Observable
public final class PanePreviewModel {
    public enum Status: Equatable, Sendable {
        case loading
        case text(String, tmuxAlive: Bool)
        case failed(String) // already-localized copy, straight from Copy
    }

    /// Delay before the first capture of a fresh hover, so a cursor crossing
    /// the list never spends a single capture. Skipped while a hover is
    /// already active (decision 6).
    public static let firstCaptureDelay: Duration = .milliseconds(400)
    public static let dismissDelay: Duration = .milliseconds(300)
    public static let pollInterval: Duration = .seconds(1)
    public static let lines = 40

    public private(set) var agent: String?
    public private(set) var status: Status = .loading
    /// Where to put the panel: the hovered row, its host window and its screen,
    /// all in screen points, captured at hover time. `.zero` means no hover.
    public private(set) var anchor: Anchor = .none
    public private(set) var isInsidePanel = false

    public struct Anchor: Equatable, Sendable {
        public var row: CGRect
        public var host: CGRect
        public var screen: CGRect
        public static let none = Anchor(row: .zero, host: .zero, screen: .zero)

        public init(row: CGRect, host: CGRect, screen: CGRect) {
            self.row = row
            self.host = host
            self.screen = screen
        }
    }

    private let client: DaemonClient
    private let connected: @MainActor () -> Bool
    private let sleep: EventStream.Sleep

    /// The current hover's poll loop. Internal (not private) so tests can await
    /// it directly, the same reason `EventStream.run()` is public.
    private(set) var pollTask: Task<Void, Never>?
    private(set) var dismissTask: Task<Void, Never>?

    /// Bumped on every hover start/end. A response is only applied if the generation it was
    /// issued under still matches — the name alone isn't enough: hovering A, then B, then A
    /// again reuses the name "A", and a stale response from the *first* A hover would pass a
    /// name-only guard even though a newer A hover has since started (agent-hover-preview
    /// spec review, item 3).
    private var generation = 0

    public init(client: DaemonClient, connected: @escaping @MainActor () -> Bool,
                sleep: @escaping EventStream.Sleep = { try await Task.sleep(for: $0) }) {
        self.client = client
        self.connected = connected
        self.sleep = sleep
    }

    /// Hover began (or moved) onto `name`. All three rects are screen points,
    /// measured when the hover fired.
    public func hover(_ name: String, anchor: Anchor) {
        dismissTask?.cancel()
        dismissTask = nil
        if agent == name && status != .loading {
            self.anchor = anchor
            return
        }
        let skipDelay = agent != nil // decision 6: a hover already active means the panel is on screen
        pollTask?.cancel()
        agent = name
        self.anchor = anchor
        status = .loading
        generation += 1
        let gen = generation
        pollTask = Task { [weak self] in
            guard let self else { return }
            if !skipDelay {
                do { try await self.sleep(Self.firstCaptureDelay) } catch { return }
            }
            await self.pollLoop(for: name, generation: gen)
        }
    }

    /// Hover left this row. A no-op when `name` is no longer the hovered agent,
    /// so SwiftUI's out-of-order onHover(false) for the row you just left
    /// cannot cancel the row you just entered.
    public func leave(_ name: String) {
        guard agent == name else { return }
        if status == .loading {
            clear()
            return
        }
        if isInsidePanel { return }
        startDismiss()
    }

    public func enterPanel() {
        dismissTask?.cancel()
        dismissTask = nil
        isInsidePanel = true
    }

    public func leavePanel() {
        isInsidePanel = false
        startDismiss()
    }

    private func startDismiss() {
        dismissTask?.cancel()
        dismissTask = Task { [weak self] in
            guard let self else { return }
            do { try await self.sleep(Self.dismissDelay) } catch { return }
            self.clear()
        }
    }

    /// Popover closed, or the app is tearing down.
    public func cancel() {
        clear()
    }

    private func clear() {
        pollTask?.cancel()
        pollTask = nil
        dismissTask?.cancel()
        dismissTask = nil
        agent = nil
        anchor = .none
        status = .loading
        isInsidePanel = false
        generation += 1
    }

    private func pollLoop(for name: String, generation: Int) async {
        while !Task.isCancelled {
            await capture(name, generation: generation)
            if Task.isCancelled { return }
            do { try await sleep(Self.pollInterval) } catch { return }
        }
    }

    /// A response only lands if `generation` (the one active when this call was issued) is
    /// still the model's current generation, on top of the name check — see `generation`'s doc.
    private func capture(_ name: String, generation: Int) async {
        guard connected() else {
            if agent == name, self.generation == generation {
                let next = Status.failed(DaemonError.unreachable.message)
                if status != next { status = next }
            }
            return
        }
        do {
            let cap = try await client.pane(name, lines: Self.lines)
            if agent == name, self.generation == generation {
                let next = Status.text(cap.text, tmuxAlive: cap.tmuxAlive)
                if status != next { status = next }
            }
        } catch is CancellationError {
            // The hover moved on; drop it silently (decision 9's error table).
        } catch let e as DaemonError {
            if agent == name, self.generation == generation {
                let next = Status.failed(Self.copy(for: e))
                if status != next { status = next }
            }
        } catch {
            if agent == name, self.generation == generation {
                let next = Status.failed(DaemonError.unreachable.message)
                if status != next { status = next }
            }
        }
    }

    private static func copy(for e: DaemonError) -> String {
        switch e {
        case .unreachable: return DaemonError.unreachable.message
        case .timedOut: return Copy.paneSlow
        case let .api(status, code, message):
            if status == 404 { return Copy.paneUnknownAgent }
            if status == 409 { return Copy.paneNoSession }
            if status == 502, code == "tmux_unreachable" { return Copy.paneTmuxUnreachable }
            return message
        case let .decoding(detail):
            return detail
        }
    }
}

/// Where the panel goes, as pure CGRect math so it is testable with no window
/// server. Lives in SwarmBarKit, not in the app target, for the same reason.
public enum PanePreviewGeometry {
    /// Left of the popover by default — status items live at the right of the
    /// menu bar. Flipped right when the popover is near the left screen edge,
    /// clamped on every edge, and aligned to the hovered row's top so the panel
    /// tracks the cursor down the list.
    public static func frame(_ a: PanePreviewModel.Anchor, size: CGSize, gap: CGFloat = 8) -> CGRect {
        var x = a.host.minX - gap - size.width
        if x < a.screen.minX { x = a.host.maxX + gap }
        x = min(x, a.screen.maxX - size.width)
        x = max(x, a.screen.minX)
        var y = min(a.row.maxY - size.height, a.screen.maxY - size.height)
        y = max(y, a.screen.minY)
        return CGRect(x: x, y: y, width: size.width, height: size.height)
    }
}

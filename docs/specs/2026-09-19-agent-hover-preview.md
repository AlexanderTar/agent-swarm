# Agent hover preview: live tmux pane snapshot beside the menubar popover

> **AWAITING HUMAN APPROVAL, NOT YET IMPLEMENTED.**
> Design only. No Swift or Go implementation code has been written. Do not
> start Task 1 of the implementation order below until a human approves this
> document.

## Context

Hovering an agent row in the menubar popover's **Agents** section should show
a live preview of that agent's tmux pane in a translucent panel next to the
popover, so you can see what an agent is actually doing without opening
Ghostty. Hovering the row's action buttons (terminal / pause / resume / retry
/ cancel / ack) must *not* trigger it.

Affected code, all in the primary checkout (branch `main`):

- `apps/menubar/Sources/SwarmBarUI/Popover/AgentsSection.swift` —
  `AgentRowView` is the row; its hover target has to be scoped.
- `apps/menubar/Sources/SwarmBarKit/` — new `PanePreviewModel`, one new
  `DaemonClient` method, new `Copy` strings, new wire type.
- `apps/menubar/Sources/SwarmBar/App.swift` — the floating panel host lives
  here (AppKit).
- `internal/httpapi/` — one new route.

### Premises in the brief that the code contradicts

1. **There is no `NSPopover`.** `App.swift` uses
   `MenuBarExtra { … } .menuBarExtraStyle(.window)`. The container is an
   AppKit window SwiftUI owns and does not expose. Everything about
   positioning and dismissal in this spec is written against that, not against
   `NSPopover`.
2. **Real Liquid Glass is available.** `Package.swift` pins
   `platforms: [.macOS(.v14)]` and `Info.plist` sets
   `LSMinimumSystemVersion 14.0`, but the toolchain is Xcode 26.0.1 / macOS 26
   SDK / Swift 6.2, and `Components.swift:167` already ships the exact pattern
   for this:

   ```swift
   @ViewBuilder public func glassButtons() -> some View {
       if #available(macOS 26, *) { buttonStyle(.glass) } else { self }
   }
   ```

   So: `.glassEffect(...)` under `#available(macOS 26, *)`, `.ultraThinMaterial`
   below it. The deployment target is not a blocker and does not need to move.
3. **The daemon does not expose pane content over HTTP today.** The full route
   table (`server.go:160`, `config.go:50`, `runtime.go:636`, `items.go:90`,
   `spawn.go:16`, `requests.go:17`, `agentio.go:18`, `dev.go:27`) has no
   capture route. `internal/spawn.Spawner.Capture` exists and is reachable as
   `s.RT.Tmux.Capture`; it is used only internally by
   `internal/runtime/agents.go`. One new route is required.
4. **The daemon is the authority on the tmux socket.** `spawn.SocketFromEnv`
   is "the one place the tmux socket name is decided" and the daemon already
   publishes it (`terminalWire.TmuxSocket` from `s.Deps.RT.TmuxSocket()`).
   `Terminals.swift` hardcodes `socket = "swarm"` — pre-existing debt this
   feature must not extend.

### Caveats needing a scope call

- **Colour is dropped in v1** (see decision 5). If the reviewer wants the
  preview to look like the terminal rather than like a log, that is a second
  round of work, not a tweak.
- **Whether a non-activating `NSPanel` ordered front dismisses the
  `MenuBarExtra` window is unverified.** It cannot be settled by reading
  code; SwiftUI's dismissal logic for `.menuBarExtraStyle(.window)` is
  private. Task 1 of the implementation order is a spike that answers it, and
  a "no" invalidates decision 3.

## Locked decisions

1. **Pane content comes from a new daemon route, polled while hovered.**
   `GET /api/agents/{name}/pane?lines=40`. Never `tmux capture-pane` from the
   Swift side: the daemon owns the socket name and the agent-name → tmux-name
   mapping.
2. **Poll cadence: 400 ms delay before the first capture, then every 1 s while
   the hover holds.** Rejected alternatives in the section below.
3. **The panel is a borderless non-activating `NSPanel`, mouse-transparent,
   positioned left of the popover window with a right-side flip.** Hosted in
   the `SwarmBar` executable target (AppKit), driven by state in
   `SwarmBarKit`.
4. **The hover target is the icon + name/subtitle group only**, via
   `.contentShape(Rectangle()).onHover` on a `Group` that wraps exactly those
   two children. The chevron `Button`, the `Spacer`, and the trailing
   `IconButton`s sit outside that group, so they are excluded by construction
   rather than by hit-test arithmetic.
5. **Plain text, not ANSI.** The daemon strips escapes with the already
   exported `adapter.StripANSI` (`internal/adapter/adapter.go:127`) before
   returning. No Swift ANSI renderer is written — a grep for
   `ansi|escape|x1b` across `apps/menubar/Sources/` returns nothing, and one
   would be ~80 lines of SGR parsing plus a `NSAttributedString` bridge for a
   preview panel.
6. **Moving between rows swaps immediately; it does not re-debounce.** Once
   the panel is on screen the 400 ms delay is skipped, so dragging down the
   list feels like a cursor rather than a slideshow. A fresh hover after the
   panel has closed pays the 400 ms again.
7. **No daemon-down fallback to local `tmux capture-pane`.** `Terminals`
   has a local runner and §16.2 lets *recovery actions* work with the daemon
   down, but a preview is not a recovery action. Daemon down shows copy
   (decision 9), not a second capture path with its own socket assumption.
8. **The row's existing `.help(agent.name)` tooltip is removed.** It fires
   roughly a second into the same hover and would sit on top of the panel.
   The name is already the row's visible text and stays in the
   `accessibilityLabel`; truncation is `.middle`, so the tooltip was the only
   way to read a long name in full — that loss is accepted and noted for the
   reviewer.
9. **Every failure is copy in the panel, never an empty panel and never a
   stale capture.** Text is cleared the moment the hovered agent changes.
10. **A dead pane still previews.** `spawn.TmuxConf` sets
    `remain-on-exit on`, so a crashed agent's last screen is still
    capturable. That is the most useful moment to hover, so the route does
    not gate on `tmux_alive`; it reports it and the panel labels it.

### Option comparison for freshness (decision 2)

| | A. One-shot on hover | B. Poll while hovered **(chosen)** | C. Per-agent SSE stream |
|---|---|---|---|
| Freshness | Frozen at hover time | ≤1 s old | Sub-second |
| Cost per hover | 1 `capture-pane` | 1 + 1/s | 1 stream open/close + N pushes |
| New daemon surface | 1 GET | 1 GET | 1 SSE route + a per-pane watcher goroutine + a change-detect diff |
| Menubar work | one call | `Task` with cancel | second `SSEParser` consumer, reconnect policy, teardown on hover-off |
| Failure story | reuses `DaemonError` | reuses `DaemonError` | needs its own, distinct from `EventStream`'s |

B. A is the feature the brief warns against — "a live terminal preview that
never updates". C loses to B on effort by a wide margin for a 1–3 second
interaction: `EventStream.swift` is a broadcast consumer of `/api/events`
with 70 s idle timeouts and exponential backoff, all of it wrong for an
open-and-close-per-hover stream, and pane text must not go on `/api/events`
anyway — that feed is broadcast to every client including the web board, so a
hover in the menubar would push kilobytes of terminal scrollback to browsers
that never asked. B's waste is bounded and obvious: at most one `capture-pane`
per second, only while a cursor is resting on a row.

`lines=40` is the panel's height in rows; 1 s matches how fast a coding
agent's pane actually changes.

## Model / API types

### New Go route

`internal/httpapi/runtime.go`, appended to `runtimeRoutes()`:

```go
{"GET", "/api/agents/{name}/pane", authDaemon, s.agentPane},
```

```go
// paneWire is GET /api/agents/{name}/pane. Text is ANSI-stripped
// (adapter.StripANSI): the menubar renders it as plain monospaced text.
type paneWire struct {
	Text      string `json:"text"`
	TmuxAlive bool   `json:"tmux_alive"`
	Lines     int    `json:"lines"`
}

// defaultPaneLines is the preview panel's height in rows; maxPaneLines caps
// what a caller can ask for, so one query parameter cannot pull 20 000 lines
// of scrollback (history-limit in spawn.TmuxConf) through the daemon.
const (
	defaultPaneLines = 40
	maxPaneLines     = 200
)

func (s *Server) agentPane(w http.ResponseWriter, r *http.Request) {}
```

Handler contract:

| Condition | Response |
|---|---|
| `s.RT == nil \|\| s.RT.Tmux == nil` | 500 `internal` "The daemon isn't fully wired yet." (`s.notWired`) |
| unknown agent name | whatever `s.RT.Agent` already returns → 404 `not_found` |
| agent has no session row | 409 `conflict` "That agent has no session." |
| `Tmux.Capture` fails | 502 `tmux_unreachable` "Can't reach tmux." |
| otherwise | 200 `paneWire` |

Implementation shape (for the plan, not code to copy blindly): reuse
`s.loadAgentStatus(ctx, name)` exactly as `s.terminal` does — it returns
`(runtime.Agent, *runtime.Session, error)` and a nil session for "no session
yet" — then `s.RT.Tmux.Capture(ctx, ses.TmuxName, lines)`,
`adapter.StripANSI`, and `s.livePanes(ctx)[ses.TmuxName]` for `tmux_alive`.
`lines` is parsed with `strconv.Atoi` and clamped to `1…maxPaneLines`,
defaulting to `defaultPaneLines` on absent or unparseable input (no 400: a bad
query parameter on a read is not worth an error envelope here).
`internal/httpapi` importing `internal/adapter` introduces no cycle —
`go list -deps ./internal/adapter` covers only `db`, `events`, `execx`,
`kinds`, `catalog`.

### New Swift types

`apps/menubar/Sources/SwarmBarKit/Wire.swift`:

```swift
public struct PaneCapture: Codable, Sendable, Equatable {
    public var text: String
    public var tmuxAlive: Bool
    public var lines: Int

    enum CodingKeys: String, CodingKey {
        case text, lines
        case tmuxAlive = "tmux_alive"
    }

    public init(text: String, tmuxAlive: Bool = true, lines: Int = 40)
}
```

`apps/menubar/Sources/SwarmBarKit/DaemonClient.swift` — one protocol method,
implemented in `HTTPDaemonClient` and `MockDaemonClient`:

```swift
public protocol DaemonClient: Sendable {
    // … existing members unchanged …
    func pane(_ name: String, lines: Int) async throws -> PaneCapture
}
```

`HTTPDaemonClient` uses its existing `call` helper with a 5 s timeout (shorter
than the default 10 s: a preview that takes longer than the poll interval is
already useless):

```swift
public func pane(_ name: String, lines: Int) async throws -> PaneCapture {
    try await call("GET", "/api/agents/\(name)/pane?lines=\(lines)", timeout: 5)
}
```

`apps/menubar/Sources/SwarmBarKit/PanePreview.swift` (new file — this is the
logic under the 80 % coverage gate, which is why it lives in `SwarmBarKit`
with both its clock and its client injected):

```swift
/// The hover preview's state and its poll loop. Owned by AppModel; the
/// floating panel in SwarmBar observes `agent`/`text`/`status` and nothing else.
@MainActor
@Observable
public final class PanePreviewModel {
    public enum Status: Equatable, Sendable {
        case loading
        case text(String, tmuxAlive: Bool)
        case failed(String)   // already-localized copy, straight from Copy
    }

    /// Delay before the first capture of a fresh hover, so a cursor crossing
    /// the list never spends a single capture. Skipped while the panel is
    /// already showing (decision 6).
    public static let firstCaptureDelay: Duration = .milliseconds(400)
    public static let pollInterval: Duration = .seconds(1)
    public static let lines = 40

    public private(set) var agent: String?
    public private(set) var status: Status = .loading
    /// Where to put the panel: the hovered row, its host window and its screen,
    /// all in screen points, captured at hover time. `.zero` means no hover.
    public private(set) var anchor: Anchor = .none

    public struct Anchor: Equatable, Sendable {
        public var row: CGRect
        public var host: CGRect
        public var screen: CGRect
        public static let none = Anchor(row: .zero, host: .zero, screen: .zero)
    }

    public init(client: DaemonClient,
                connected: @escaping @MainActor () -> Bool,
                sleep: @escaping EventStream.Sleep = { try await Task.sleep(for: $0) })

    /// Hover began (or moved) onto `name`. All three rects are screen points,
    /// measured when the hover fired.
    public func hover(_ name: String, anchor: Anchor)
    /// Hover left this row. A no-op when `name` is no longer the hovered agent,
    /// so SwiftUI's out-of-order onHover(false) for the row you just left
    /// cannot cancel the row you just entered.
    public func leave(_ name: String)
    /// Popover closed, or the app is tearing down.
    public func cancel()
}

/// Where the panel goes, as pure CGRect math so it is testable with no window
/// server. Lives in SwarmBarKit, not in the app target, for the same reason.
public enum PanePreviewGeometry {
    /// Left of the popover by default — status items live at the right of the
    /// menu bar. Flipped right when the popover is near the left screen edge,
    /// clamped on every edge, and aligned to the hovered row's top so the panel
    /// tracks the cursor down the list.
    public static func frame(_ a: PanePreviewModel.Anchor,
                             size: CGSize, gap: CGFloat = 8) -> CGRect
}
```

`AppModel` gains one stored property. It is declared the way `AppModel`
already declares `notifier` and `stream` — `!` and assigned after the `let`s,
because `connected:` captures `self` and a `let` initialized in the property
list cannot:

```swift
public private(set) var preview: PanePreviewModel!
```

assigned in `AppModel.init` from the same `client` it already holds, with
`connected: { [weak self] in self?.connected ?? false }` and the same injected
`sleep` the `EventStream` gets, so tests drive it with a fake clock.

### Views

`apps/menubar/Sources/SwarmBarUI/PanePreviewPanel.swift` (new):

```swift
/// The preview's content. The window around it lives in SwarmBar.
public struct PanePreviewPanel: View {
    public init(preview: PanePreviewModel)
    public static let size = CGSize(width: 520, height: 320)
}

/// Liquid Glass where the OS has it, a vibrancy material where it doesn't —
/// the same availability shape as Components.glassButtons().
extension View {
    @ViewBuilder func glassPanel(cornerRadius: CGFloat) -> some View
}
```

`apps/menubar/Sources/SwarmBarUI/Popover/AgentsSection.swift` — `AgentRowView`
wraps its two hoverable children and reports its own screen rect:

```swift
@State private var anchor = ScreenAnchor()   // reads its rects on demand
…
Group {
    AgentIcon(agent.kind)
    VStack(alignment: .leading, spacing: 2) { /* name + StateDot + subtitle */ }
}
.contentShape(Rectangle())
.background(ScreenAnchorReader(anchor: anchor))
.onHover { inside in
    if inside, let a = anchor.measure() { model.preview.hover(agent.name, anchor: a) }
    else { model.preview.leave(agent.name) }
}
```

`ScreenAnchorReader` is a zero-size `NSViewRepresentable` whose only job is to
hold on to the backing `NSView`; `ScreenAnchor.measure()` then does the AppKit
conversion **at hover time**, not at layout time. That ordering matters:
`AgentRowView` sits inside `SectionBody(cap:)`, which is a `ScrollView`
(`Components.swift:153`), and SwiftUI does not re-run `updateNSView` when that
scrolls — a rect cached during layout would put the panel at the row's
pre-scroll position. SwiftUI has no screen coordinate space at all
(`GeometryReader`'s `.global` is window coordinates), so this has to go
through AppKit:

```swift
/// Measures the row, its host window and its screen in screen points, on demand.
/// No NSWindow crosses into SwarmBarKit — only three CGRects.
@MainActor final class ScreenAnchor {
    fileprivate weak var view: NSView?
    func measure() -> PanePreviewModel.Anchor? {
        guard let view, let win = view.window, let screen = win.screen else { return nil }
        return .init(row: win.convertToScreen(view.convert(view.bounds, to: nil)),
                     host: win.frame, screen: screen.visibleFrame)
    }
}

struct ScreenAnchorReader: NSViewRepresentable {
    let anchor: ScreenAnchor
}
```

`PanePreviewWindow` reads `model.preview.anchor` and calls
`PanePreviewGeometry.frame(_:size:gap:)`. Nothing hands it an `NSWindow`.

`apps/menubar/Sources/SwarmBar/App.swift` — the window:

```swift
/// The preview's floating window: borderless, non-activating, mouse-transparent,
/// above the MenuBarExtra window, never key. Ordered front with orderFront(nil);
/// makeKeyAndOrderFront would dismiss the popover it is meant to sit beside.
@MainActor
final class PanePreviewWindow {
    init(preview: PanePreviewModel)
    /// Moves and orders front using PanePreviewGeometry.frame(anchor, …).
    /// `anchor.screen` is the popover's own screen: NSScreen.main is the screen
    /// with keyboard focus, which on a two-display setup is regularly not the
    /// one the menu bar popover is on.
    func show(_ anchor: PanePreviewModel.Anchor)
    func hide()
}
```

Window configuration, exactly:

```
NSPanel(contentRect:, styleMask: [.borderless, .nonactivatingPanel],
        backing: .buffered, defer: true)
  isFloatingPanel      = true
  level                = one above the MenuBarExtra window's own level, read at
                         show() time — the exact value is confirmed by the
                         Task 1 spike, not assumed to be .statusBar. Too low
                         and a right-flip that overlaps puts the panel behind
                         the popover.
  isOpaque             = false
  backgroundColor      = .clear              // SwiftUI draws the glass
  hasShadow            = true
  ignoresMouseEvents   = true                // read-only: never steals hover or clicks
  hidesOnDeactivate    = false
  becomesKeyOnlyIfNeeded = true
  collectionBehavior   = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
  animationBehavior    = .utilityWindow
  contentView          = NSHostingView(rootView: PanePreviewPanel(preview:))
```

`PanePreviewGeometry.frame` is a pure function — the whole edge-awareness story
is unit-testable without a window server:

```
x = host.minX - gap - size.width
if x < screen.minX { x = host.maxX + gap }            // flip right
x = min(x, screen.maxX - size.width)                   // and still clamp
y = min(row.maxY - size.height, screen.maxY - size.height)
y = max(y, screen.minY)                                // clamp to the bottom
```

Vertical alignment is to the hovered row's top edge, so the panel tracks the
cursor down the list instead of sitting statically beside the popover.

### Teardown

- `PopoverView` gains `.onDisappear { model.preview.cancel() }`. The
  `MenuBarExtra` window closing must take the panel with it; a mouse-
  transparent panel left behind would be unclosable.
- `.contextMenu` or `.confirmationDialog` opening while hovered: the pointer
  leaves the row, `leave(_:)` fires, the panel hides. That is the intended
  behaviour, not a bug to work around.

## Screens

Size: 520 × 320 pt, 12 pt corner radius, 11 pt monospaced body.

**Loaded (the normal case)**

```
                                    ┌─────────────────────────────┐
                                    │  Agent Swarm      ● Live    │
┌──────────────────────────────────┐├─────────────────────────────┤
│ login-coder · claude · TASK-14  ⟳││ Needs you                   │
├──────────────────────────────────┤│ ─────────────────────────── │
│ ● Running verification…          ││ Agents            ● Pause…  │
│                                  ││  ▸ ◆ s0-orch          ⌨ ⏸  │
│ $ swift test                     ││  ▸ ◆ login-coder  ●   ⌨ ⏸  │  ← hovered
│ Test Suite 'All tests' started   ││      claude · TASK-14       │
│ Test Case '-[AppModelTests …     ││  ▸ ◆ s0-review-2  ○   ⌨ ⏸  │
│   passed (0.004 seconds)         ││ ─────────────────────────── │
│ Test Case '-[EventStreamTests…   ││ Usage                       │
│   passed (0.011 seconds)         ││ ▓▓▓▓▓▓▓▓░░░░ Weekly  62 %   │
│                                  ││ ─────────────────────────── │
│ ·Hyperspacing… (esc to interrupt)││ Notifications               │
│                                  │├─────────────────────────────┤
│ ▌                                ││ + New orchestrator     ⊞    │
└──────────────────────────────────┘└─────────────────────────────┘
 520 pt, glass, mouse-transparent    360 pt MenuBarExtra window
```

Header line: `<agent name> · <kind> · <item key>`, 11 pt semibold, with a
spinner on the right while a capture is in flight after the first one (the
first capture shows the loading state instead). Body: the last 40 pane rows,
bottom-aligned, `.lineLimit(nil)`, horizontally clipped — no scrolling, no
selection, no interaction of any kind.

**Loading (first 400 ms + the request)**

```
┌──────────────────────────────────┐
│ login-coder · claude · TASK-14   │
├──────────────────────────────────┤
│                                  │
│              ◌                   │
│      Reading the pane…           │
│                                  │
└──────────────────────────────────┘
```

**Dead pane (`tmux_alive: false`, `remain-on-exit` kept the screen)**

```
┌──────────────────────────────────┐
│ login-coder · claude · TASK-14   │
├──────────────────────────────────┤
│ ⚠ Session ended. Last screen.    │  ← 10 pt, .orange
│                                  │
│ error: 1 test failed             │
│ ✗ testResumeAfterPause           │
│ $                                │
└──────────────────────────────────┘
```

**Failure (one line, centred, `.secondary`)**

```
┌──────────────────────────────────┐
│ login-coder · claude · TASK-14   │
├──────────────────────────────────┤
│                                  │
│        Daemon unavailable.       │
│                                  │
└──────────────────────────────────┘
```

**Deliberately not on this panel:** scrollbars, text selection, a copy
button, a close button, a pin/detach control, ANSI colour, a "jump to
terminal" button (the row's own ⌨ button is 20 pt away), and any keyboard
focus whatsoever.

## All user-facing copy

New entries in `apps/menubar/Sources/SwarmBarKit/Copy.swift`:

```swift
// hover preview
public static let paneLoading = "Reading the pane…"
public static let paneNoSession = "No session yet."
public static let paneDead = "Session ended. Last screen."
public static let paneTmuxUnreachable = "Can't reach tmux."
public static let paneUnknownAgent = "That agent is gone."
public static let paneSlow = "The pane didn't answer in time."
public static func paneHeader(_ name: String, _ kind: String, _ itemKey: String) -> String {
    "\(name) · \(kind) · \(itemKey)"
}
```

Daemon-down reuses the existing `DaemonError.unreachable.message`
("Daemon unavailable."). Error mapping in `PanePreviewModel`:

| Source | Panel text |
|---|---|
| `DaemonError.unreachable`, or `connected == false` | `"Daemon unavailable."` |
| `DaemonError.timedOut` | `Copy.paneSlow` |
| `.api(404, …)` | `Copy.paneUnknownAgent` |
| `.api(409, …)` | `Copy.paneNoSession` |
| `.api(502, "tmux_unreachable", …)` | `Copy.paneTmuxUnreachable` |
| any other `.api` | the daemon's own `message` |
| `CancellationError` | nothing — the hover moved on, drop it silently |

Empty capture text (a pane that exists but has printed nothing) shows the
header and an empty body, not an error.

New Go-side string: `"That agent has no session."` (409, in
`internal/httpapi/runtime.go`) and `"Can't reach tmux."` (502).

## File list

**New**

| Path | What |
|---|---|
| `apps/menubar/Sources/SwarmBarKit/PanePreview.swift` | `PanePreviewModel` (hover state, debounce, poll loop, error mapping) and `PanePreviewGeometry` |
| `apps/menubar/Sources/SwarmBarUI/PanePreviewPanel.swift` | `PanePreviewPanel`, `glassPanel`, `ScreenAnchor` + `ScreenAnchorReader` |
| `apps/menubar/Tests/Fixtures/pane.json` | `GET /api/agents/{name}/pane` response fixture |
| `apps/menubar/Tests/SwarmBarTests/PanePreviewTests.swift` | debounce / cancel / swap / error-mapping / `PanePreviewGeometry.frame` |

**Changed**

| Path | Change |
|---|---|
| `internal/httpapi/runtime.go` | `agentPane` handler, `paneWire`, the two line constants, one `runtimeRoutes()` entry |
| `internal/httpapi/runtime_test.go` | route tests: 200, 404, 409, 502, `lines` clamping, ANSI stripped |
| `apps/menubar/Sources/SwarmBarKit/Wire.swift` | `PaneCapture` |
| `apps/menubar/Sources/SwarmBarKit/DaemonClient.swift` | `pane(_:lines:)` on the protocol **and on `MockDaemonClient`** (it conforms; adding a protocol method breaks it otherwise), plus `pane.json` in the fixture `convenience init` |
| `apps/menubar/Sources/SwarmBarKit/HTTPDaemonClient.swift` | `pane(_:lines:)` |
| `apps/menubar/Sources/SwarmBarKit/AppModel.swift` | `public private(set) var preview: PanePreviewModel!`, assigned in `init` after `super`-style setup, like `notifier`/`stream` |
| `apps/menubar/Sources/SwarmBarKit/Copy.swift` | the seven strings above |
| `apps/menubar/Sources/SwarmBarUI/Popover/AgentsSection.swift` | hover `Group` + `ScreenAnchorReader` in `AgentRowView`; **remove** `.help(agent.name)` from the name `Text` |
| `apps/menubar/Sources/SwarmBarUI/Popover/PopoverView.swift` | `.onDisappear { model.preview.cancel() }` |
| `apps/menubar/Sources/SwarmBar/App.swift` | `PanePreviewWindow`; `AppDelegate` creates one and observes `model.preview` |
| `apps/menubar/Tests/Fixtures/contract.json` | a `pane.json` entry, so `internal/httpapi/contract_test.go` checks the shape both ways |
| `internal/httpapi/contract_test.go` | one `case "response GET /api/agents/{name}/pane":` arm in the fixture-path → wire-type switch (line ~98); the fixture is skipped silently without it |

**Reused unchanged:** `internal/spawn.Spawner.Capture`, `adapter.StripANSI`,
`Server.loadAgentStatus`, `Server.livePanes`, `Server.notWired`, `apiErr`,
`writeJSON`, `HTTPDaemonClient.call`, `EventStream.Sleep` (the injected-clock
typealias), `Components.glassButtons`'s availability pattern.

**Deleted:** nothing.

## Verification plan

Implementation order — **Task 1 gates the rest**:

1. **Spike, ~20 lines, throwaway:** in `AppDelegate`, create the `NSPanel`
   configured as above, `orderFront(nil)` it five seconds after launch, open
   the menu bar popover, and watch whether the popover survives. If it does
   not, stop and re-open decision 3 before writing any polling code.
2. Go route, TDD: `internal/httpapi/runtime_test.go` first.
3. `PaneCapture` + `pane(_:lines:)` + `pane.json` + the `contract.json` entry
   + the `contract_test.go` switch arm; `go test ./internal/httpapi` proves the
   two shapes agree.
4. `PanePreviewModel` and `PanePreviewGeometry` with a fake clock, TDD. This is
   the coverage-gated part.
5. `PanePreviewPanel` render tests.
6. Row wiring and the real window last.

Commands, in order:

```
go test ./internal/httpapi ./internal/spawn
make test-menubar                 # swift build -c release && swift test && scripts/cover.sh
make test                         # full gate
```

Automated checks that must exist:

- `runtime_test.go`: 200 with escape-stripped text; 404 unknown agent; 409
  agent with no session; 502 when the fake tmux's `Capture` errors;
  `?lines=9999` clamps to 200 and `?lines=abc` falls back to 40.
- `contract_test.go` decodes `pane.json` into `paneWire` with
  `DisallowUnknownFields` (it already walks every `contract.json` entry).
- `PanePreviewTests`: a hover cancelled before 400 ms performs **zero**
  client calls; a hover held 2.5 s performs 3; `leave` cancels the in-flight
  task; `hover(b)` while `a` is showing swaps without re-delaying and clears
  `a`'s text in the same tick; `leave("a")` arriving after `hover("b")` does
  not cancel `b`; each `DaemonError` maps to the copy in the table above;
  `PanePreviewGeometry.frame` flips right when `host.minX - gap - width < screen.minX` and
  clamps at the top, bottom and right edges.
- `PopoverRenderTests`: `PanePreviewPanel` renders at 520 × 320 in loading,
  loaded, dead-pane and failed states (the existing `renderedSize` helper).

End-to-end scenarios, by hand, against a live daemon with two agents running:

1. Rest on a row → panel appears left of the popover after ~0.4 s, text
   updates about once a second, matches what Ghostty shows.
2. Sweep the cursor across all rows fast → no panel, and the daemon log shows
   no `capture-pane`.
3. Move from row A to row B slowly → panel swaps instantly, no flash of A's
   text under B's header.
4. Hover the ⌨ / ⏸ buttons → no panel; click ⌨ → Ghostty opens, popover
   behaves exactly as before.
5. Right-click the row → context menu opens, panel hides, menu is usable.
6. Click outside the popover → popover and panel both close.
7. `launchctl stop` the daemon, hover → "Daemon unavailable." Restart it,
   hover again → text returns with no app restart.
8. `tmux -L swarm kill-session -t =<agent>` → 409 "No session yet." on the
   next poll (or the last screen with the ⚠ banner if `remain-on-exit` kept
   the pane).
9. Drag the menu bar item to the far left of the menu bar (or move the popover
   to a left-edge display), hover → the panel flips to the right of the
   popover and stays fully on screen.
10. Hover the bottom row of a long list → panel clamps to the bottom of
    `visibleFrame`, no off-screen overhang.
11. Hover a row, then hover a row on a second display's menu bar → the panel
    lands on the popover's screen, not on the focused one.
12. Two agents, hover each in turn for ten seconds → Activity Monitor shows no
    sustained CPU climb in `SwarmBar` or `swarm`.

## Explicitly out of scope

- **ANSI colour.** The upgrade path, if it is ever wanted: `?ansi=1` on the
  route to skip `StripANSI`, plus an SGR → `AttributedString` parser in
  `SwarmBarKit`. `ponytail:` comment goes on the `StripANSI` call naming it.
- **`-J`'s line joining.** `Spawner.Capture` passes `-J`, which joins wrapped
  lines; a long line will render differently from Ghostty. Cosmetic;
  `ponytail:` comment, upgrade path is a separate un-joined capture mode.
- **Scrolling, selecting, or copying from the panel.** It is mouse-transparent
  on purpose.
- **Pinning / detaching the preview into a real window.**
- **Preview from the web board** (`web/`). Different surface, different
  problem, and the route is `authDaemon` so the board could consume it later
  without further daemon work.
- **Previewing anything but agent rows** — no preview for `Needs you`,
  `Usage`, or the `Finished (n)` collapse row.
- **A keyboard path to the preview.** Hover only; the panel takes no focus, so
  there is nothing to tab to. VoiceOver keeps reading the row's existing
  `accessibilityLabel`, unchanged.
- **Daemon-down local `tmux capture-pane`** (decision 7).
- **Fixing `Terminals.swift`'s hardcoded `socket = "swarm"`.** Pre-existing;
  this feature routes through the daemon and does not add to it.
- **Moving the deployment target off macOS 14.** Not needed; the
  `#available(macOS 26, *)` pattern already in `Components.swift` covers it.

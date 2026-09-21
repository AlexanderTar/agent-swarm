# Pane preview: wide scrolling view, original colours, in-flight pause/resume buttons

## Context

Three changes, one branch (`feat/pane-preview-ux`, worktree `agent-swarm--pane-preview-ux`).

1. **Wide pane preview.** The hover panel is 520x320 and wraps text. The daemon captures panes at 220 columns (`internal/spawn/tmux.go:90`, `capture-pane -J`), so full-width TUI lines (input box borders, status lines) wrap into noise. Fix: wider panel, no wrapping, horizontal scroll styled like the vertical one.
2. **Original colours.** The daemon strips ANSI before sending (`internal/httpapi/runtime.go`, `adapter.StripANSI`). Fix: also send the raw capture and render SGR colours in the panel.
3. **Pause/resume in-flight state.** The daemon already returns a disabled "Pausing…" action for `pause_requested|quiescing|stopping`. The gap is client-side, between click and refreshed state: menubar `AppModel.perform` has no in-flight guard; web `useMutation` clears `pending` before the refetch lands.

Considered and rejected: stripping the harness footer (input box, mode line). Fixtures show a stable marker only for claude and agy (top border), none for codex and cursor; permission modes other than bypass are unverified; dialogs replace the input box; the daemon strips ANSI so dim/placeholder cues are lost. Cost and misfire risk are high and a wrong cut hides the prompt the user hovers to see.

Collision warning: tasks 1 and 3 both edit `apps/menubar/Sources/SwarmBarUI/PanePreviewPanel.swift`; run 1 before 3. Task 2 is independent.

## Locked decisions

- Panel stays a floating `NSPanel`; no per-agent parsing of TUI chrome.
- `text` on the pane wire stays ANSI-stripped (old clients, existing tests). New field `ansi` carries the raw capture. Always sent (no query param); about 20KB per 1s poll on localhost.
- The text area has a fixed dark background in light and dark mode. TUI palettes assume a dark screen.
- Only SGR (`ESC[...m`) is interpreted. Every other escape sequence is dropped.
- Pause/resume disabling is a client overlay. The daemon contract does not change.

## DB models

None.

## Model / API types

Go (`internal/httpapi/runtime.go`):

```go
type paneWire struct {
	Text      string `json:"text"`       // ANSI-stripped, unchanged
	ANSI      string `json:"ansi"`       // raw capture, SGR kept
	TmuxAlive bool   `json:"tmux_alive"`
	Lines     int    `json:"lines"`
}
```

Swift (`SwarmBarKit/Wire.swift`):

```swift
public struct PaneCapture: Codable, Sendable, Equatable {
    public var text: String
    public var ansi: String?          // decodeIfPresent; nil from old daemons
    public var tmuxAlive: Bool
    public var lines: Int
    public init(text: String, ansi: String? = nil, tmuxAlive: Bool = true, lines: Int = 40)
}
```

`PanePreviewModel.capture` stores `Status.text(cap.ansi ?? cap.text, tmuxAlive:)`. `Status` is unchanged.

Swift parser (new, `SwarmBarKit/AnsiText.swift`, pure, no AppKit):

```swift
public enum AnsiText {
    /// SGR-aware conversion. Plain text passes through unchanged.
    public static func attributed(_ raw: String) -> AttributedString
}
```

Supported SGR: 0 reset; 1 bold; 2 dim; 3 italic; 4 underline; 7 reverse; 22/23/24/27 resets; 30-37, 90-97 foreground; 40-47, 100-107 background; 39/49 default; `38;5;n`, `48;5;n` (256-colour: 0-15 palette, 16-231 cube, 232-255 grey ramp); `38;2;r;g;b`, `48;2;r;g;b`. Default foreground `#D4D4D4`, default background clear (the panel's dark fill shows through). Dim renders as 50% alpha foreground. Reverse swaps foreground and background (default fg/bg substitute for missing sides).

Menubar in-flight (`SwarmBarKit/AppModel.swift`):

```swift
public private(set) var inFlight: [String: AgentEndpoint] = [:]   // agent name -> pause|resume
public func actions(_ a: AgentNode) -> [AgentAction]              // overlays inFlight
```

While `inFlight[name] == .pause`: the `.pause` action becomes `label: Copy.pausing, disabled: true`. While `.resume`: the `.resume` action becomes `label: Copy.resuming, disabled: true`. Set at the start of `perform` for pause/resume, cleared after `refresh()` returns (also on error).

Web (`AgentRow.tsx`): `const [requested, setRequested] = useState<AgentEndpoint | null>(null)`. Set on pause/resume click; cleared in an effect when `displayState(agent)` changes, or on error. While set, the matching button is disabled with label `C.pausing` / `C.resuming`.

## Screens

Hover panel (was 520x320, now 760x320):

```
+--------------------------------------------------------------+
| login-coder · Coder · TASK-101                               |
|--------------------------------------------------------------|
| dark #1E1E1E fill, 11pt monospaced, no wrap                  |
| ...content lines, SGR colours...                             |
| ─────────────────────────────────────────────────────────>>> |
| ❯ █                                                          |
| ─────────────────────────────────────────────────────────>>> |
|  [overlay vertical scroller right] [overlay horiz scroller ▁▁]|
+--------------------------------------------------------------+
```

- Anchor: bottom-leading (newest lines, left edge visible).
- Both scrollers: overlay style, auto-hide, small.
- Empty/edge: `.loading` and `.failed` states unchanged (centred, on the same dark fill only for `.text`). Dead tmux warning line stays above the scroll area.
- Not on the screen: no footer stripping, no column ruler, no font-size control.

Agent row buttons: pause icon and resume icon become disabled (dimmed) while in flight; the row subtitle is unchanged. Context menu items follow the same overlay.

## All user-facing copy

- `Copy.resuming = "Resuming…"` (`Copy.swift`) and `C.resuming = "Resuming…"` (`web/src/copy.ts`).
- Existing: `Pausing…` (`Copy.pausing`, `C.pausing`).
- No other new copy.

## File list

Changed:
- `internal/httpapi/runtime.go` (+ its test)
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`, `PanePreview.swift`, `AppModel.swift`, `Copy.swift`
- `apps/menubar/Sources/SwarmBarUI/PanePreviewPanel.swift`, `Components.swift` (horizontal scroller)
- `web/src/components/AgentRow.tsx`, `web/src/copy.ts`
- Tests: `PanePreviewTests`, `PanePreviewPanelRenderTests`, `AppModelTests`, `MockDaemonClientTests` (as needed), `web/src/components/AgentRow.test.tsx`, Go pane handler test

New: `apps/menubar/Sources/SwarmBarKit/AnsiText.swift`, `apps/menubar/Tests/SwarmBarTests/AnsiTextTests.swift`, Swift fixture copies of the `pane-*-ansi.txt` panes under the menubar test resources if the tests cannot read `internal/adapter/testdata`.

Unchanged: daemon action contract, `AgentTree.actions`, `SubtleScrollerConfig` semantics for the vertical scroller.

Deleted: none. No existing test is removed; tests that assert `PanePreviewPanel.size` keep working through the constant.

## Verification

Order: `make test-go`, `make test-web`, `make test-menubar`.

Scenarios:
1. Hover an agent with a claude pane: borders render on one line, horizontal scroll reveals the right edge, vertical anchor at bottom.
2. Colours: cursor pane input box shows its dark background; claude prompt cursor block renders reversed; agy status line dim.
3. Old daemon (no `ansi` field): panel falls back to plain `text`, no crash.
4. Non-SGR escapes (`ESC[?25l`, `ESC[2K`, OSC title) leave no visible garbage.
5. Click Pause: button disables and reads `Pausing…` immediately; a second click is a no-op; it stays disabled through `pause_requested`, `quiescing`, `stopping`, then the row shows Resume.
6. Click Resume: button disables and reads `Resuming…`; row moves to `spawning`.
7. Error path: daemon returns 409 on pause; button re-enables and the error shows.
8. Disconnect path: `connected == false` still disables everything except terminal.

## Explicitly out of scope

- Stripping harness chrome from the pane.
- Font size control, wrap toggle, column ruler.
- Colour in the web board (it has no pane preview).
- Daemon-side pause/resume state changes.
- Light theme for the terminal area.

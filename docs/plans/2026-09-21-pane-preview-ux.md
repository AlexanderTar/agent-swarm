# Pane preview UX: implementation plan

Spec: `docs/specs/2026-09-21-pane-preview-ux.md`. Worktree: `../agent-swarm--pane-preview-ux`, branch `feat/pane-preview-ux`.
Order: Task A, then Task B (same Swift file as A), Task C anytime after A (C touches different files; run it after B to avoid concurrent `swift build` in one worktree).
Each step: failing test, run and watch it fail, minimal code, run and watch it pass, commit with explicit paths (never `git add -A`, never `--amend`).

Commands (from worktree root):
- Go: `go test ./internal/httpapi/ -run Pane -race`
- Web: `cd web && pnpm test -- AgentRow`
- Swift: `cd apps/menubar && swift test --filter <TestClass>`

## Task A: wide panel, no wrap, horizontal scroll

Files: `apps/menubar/Sources/SwarmBarUI/PanePreviewPanel.swift`, `apps/menubar/Sources/SwarmBarUI/Components.swift`, `apps/menubar/Tests/SwarmBarTests/PanePreviewPanelRenderTests.swift`.

1. Test: in `PanePreviewPanelRenderTests`, add a case rendering `.text(String(repeating: "x", count: 400), tmuxAlive: true)` and assert `renderedSize(...) == PanePreviewPanel.size` and `PanePreviewPanel.size == CGSize(width: 760, height: 320)`. Add a second case with a pane whose longest line is under 100 chars (codex idle fixture text) and assert the rendered text's leading edge is at the left padding, not centred (a ScrollView centres content narrower than its viewport along the scroll axis; the `minWidth` frame below prevents that). If the render harness cannot expose the text origin, snapshot-render both panes and assert the left 12pt gutter column of the short pane contains text pixels. Run: fails.
2. `PanePreviewPanel.size = CGSize(width: 760, height: 320)`.
3. In the `.text` branch replace the scroll view with:

```swift
ScrollView([.vertical, .horizontal]) {
    Text(text)
        .font(.system(size: 11, design: .monospaced))
        .fixedSize(horizontal: true, vertical: false)
        .frame(minWidth: Self.size.width - 24, alignment: .leading)
        .textSelection(.enabled)
        .background(SubtleScrollerConfig())
}
.scrollIndicators(.automatic)
.defaultScrollAnchor(.bottomLeading)
```

4. `Components.swift` `SubtleScrollerConfig`: in both closures add `scrollView.horizontalScroller?.controlSize = .small` next to the vertical line.
5. Run `swift test --filter PanePreviewPanelRenderTests`; passes. Commit `feat(menubar): wide pane preview with horizontal scroll`.

## Task B: original colours

Files: `internal/httpapi/runtime.go`, `internal/httpapi/*_test.go` (the existing pane handler test file), `apps/menubar/Sources/SwarmBarKit/{Wire.swift,PanePreview.swift,AnsiText.swift}`, `apps/menubar/Sources/SwarmBarUI/PanePreviewPanel.swift`, tests `AnsiTextTests.swift`, `PanePreviewTests.swift`, `PanePreviewPanelRenderTests.swift`.

B1. Daemon.
1. Test (existing pane handler test): fake tmux capture returns `"\x1b[31mred\x1b[0m plain"`; assert JSON `text == "red plain"` and `ansi == "\x1b[31mred\x1b[0m plain"`. Run: fails (no `ansi`).
2. `paneWire` gains `ANSI string \`json:"ansi"\``; handler sets `ANSI: capture`. Update the struct doc comment (text stays stripped, ansi is raw). Run: passes. Commit `feat(httpapi): send raw ANSI capture beside stripped pane text`.

B2. Swift wire and model.
1. Test in `PanePreviewTests`: client returns `PaneCapture(text: "plain", ansi: "\u{1B}[31mred\u{1B}[0m")`; after one poll `status == .text("\u{1B}[31mred\u{1B}[0m", tmuxAlive: true)`. Second test: `ansi == nil` gives `.text("plain", ...)`. Run: fails to compile.
2. `PaneCapture`: add `public var ansi: String?` and the `ansi` CodingKey. Synthesized `Decodable` already uses `decodeIfPresent` for optionals, so no custom `init(from:)`. Keep the explicit memberwise `init(text:ansi:tmuxAlive:lines:)` with `ansi` defaulting to nil so existing call sites compile.
3. `PanePreviewModel.capture`: `Status.text(cap.ansi ?? cap.text, tmuxAlive: cap.tmuxAlive)`. Run: passes. Commit.

B3. Parser (`AnsiText.swift`).
1. Tests (`AnsiTextTests`), each asserting on `AttributedString` runs:
   - plain passthrough: `"hello\nworld"` -> characters equal, single run, no explicit colours.
   - `"\u{1B}[31mred\u{1B}[0m ok"` -> `"red"` has foreground `#CD3131`-family red, `" ok"` default.
   - 256-colour `"\u{1B}[38;5;174mX"` -> foreground equals cube colour 174 (`#D78787`).
   - truecolor bg `"\u{1B}[48;2;21;21;21m "` -> background `rgb(21,21,21)`.
   - reverse `"\u{1B}[7m \u{1B}[0m"` -> foreground/background swapped against defaults.
   - dim `"\u{1B}[2mtext"` -> foreground alpha 0.5.
   - junk dropped: `"a\u{1B}[?25lb\u{1B}[2Kc\u{1B}]0;title\u{07}d"` -> `"abcd"`.
   - malformed/truncated: `"a\u{1B}[3"` -> no crash, text `"a"`.
   - real fixtures (tracked files only): read `internal/adapter/testdata/{cursor/pane-idle-ansi.txt,claude/pane-busy-ansi.txt}` via `#filePath`-relative path; assert no `\u{1B}` remains in `String(attr.characters)`. Do not reference the untracked `pane-idle-cursor-*.txt` files (another agent's WIP).
   Run: fails (type missing).
2. Implement. Skeleton:

```swift
import Foundation
import SwiftUI

public enum AnsiText {
    struct Style { var fg: Color?; var bg: Color?; var bold = false, dim = false, italic = false, underline = false, reverse = false }
    static let defaultFG = Color(red: 0xD4/255, green: 0xD4/255, blue: 0xD4/255)

    public static func attributed(_ raw: String) -> AttributedString {
        var out = AttributedString()
        var style = Style()
        var buf = ""
        func flush() { /* append buf with style attributes, clear buf */ }
        var it = raw.unicodeScalars.makeIterator()
        // loop: on ESC, read next scalar: '[' -> CSI: collect params until final byte 0x40...0x7E;
        // final 'm' -> apply SGR to style (flush first); other finals -> drop.
        // ']' -> OSC: consume until BEL or ESC '\'. Any other ESC x -> drop x.
        // Truncated sequence at end of input -> drop.
        return out
    }
    static func apply(sgr params: [Int], to style: inout Style) { /* per spec table */ }
    static func palette256(_ n: Int) -> Color { /* 0-15 xterm base, 16-231 cube (0,95,135,175,215,255), 232-255 grey 8+10*(n-232) */ }
}
```

   Reverse video (`SGR 7`) swaps foreground and background; a missing side substitutes the panel fill `#1E1E1E` (for background) or `defaultFG` (for foreground), so claude's `ESC[7m ` cursor block renders as a light block.
   Use xterm base 16 colours: black 000000, red CD3131, green 0DBC79, yellow E5E510, blue 2472C8, magenta BC3FBC, cyan 11A8CD, white E5E5E5; bright: 666666, F14C4C, 23D18B, F5F543, 3B8EEA, D670D6, 29B8DB, FFFFFF.
   Run: passes. Commit `feat(menubar): SGR-aware AnsiText parser`.

B4. Panel.
1. Test in `PanePreviewPanelRenderTests`: `.text("\u{1B}[31mred\u{1B}[0m", tmuxAlive: true)` renders at `PanePreviewPanel.size` (no crash). Run: passes trivially, so also assert via a small `AnsiText` call the string has no ESC (already covered by B3). Keep the render test as a regression guard.
2. In the `.text` branch use `Text(AnsiText.attributed(text))`, `.foregroundStyle(AnsiText.defaultFG)` on the Text, and give the scroll area a dark fill:

```swift
.background(Color(red: 0x1E/255, green: 0x1E/255, blue: 0x1E/255), in: RoundedRectangle(cornerRadius: 6))
```

   Keep the dead-tmux warning outside the dark area.
3. `swift test` full menubar suite. Commit `feat(menubar): render pane preview in original colours`.

## Task C: in-flight pause/resume buttons

Files: `Copy.swift`, `AppModel.swift`, `AgentsSection.swift` (only if a signature changes), `AppModelTests.swift`, `web/src/copy.ts`, `web/src/components/AgentRow.tsx`, `web/src/components/AgentRow.test.tsx`.

C1. Menubar.
1. Tests in `AppModelTests` (use the existing mock client; make the mock's `agent(...)` suspend on a continuation so the request is observably in flight):
   - `perform(pause)` in flight: `model.actions(agent)` contains a `.pause` action with `disabled == true` and `label == Copy.pausing`; other actions unchanged.
   - `perform(resume)` on a paused agent in flight: `.resume` disabled, label `Copy.resuming`.
   - After completion (state refreshed): `inFlight` empty.
   - Error path: mock throws `DaemonError.api(status: 409, ...)`; `inFlight` cleared, `actionError` set.
   - Second `perform(pause)` while in flight is a no-op (mock call count stays 1).
   Run: fails.
2. `Copy.swift`: `public static let resuming = "Resuming…"`.
3. `AppModel`: add `public private(set) var inFlight: [String: AgentEndpoint] = [:]`. In `perform`, after the disabled/terminal guards:

```swift
let tracked = action.endpoint == .pause || action.endpoint == .resume
if tracked {
    guard inFlight[agent.name] == nil else { return }
    inFlight[agent.name] = action.endpoint
}
defer { if tracked { inFlight[agent.name] = nil } }
```

   (`defer` runs after `await refresh()`.) `actions(_:)` maps the base actions: for each action whose endpoint equals `inFlight[a.name]`, set `disabled = true` and `label = endpoint == .pause ? Copy.pausing : Copy.resuming`. Run: passes. Commit `feat(menubar): disable pause/resume buttons while the request is in flight`.

C2. Web.
1. Tests in `AgentRow.test.tsx` (existing harness): click Pause on a running agent with a mocked API that resolves but leaves the agent state unchanged; assert the pause button is disabled and reads `Pausing…`; then re-render the agent with `state: "pause_requested"`; assert still disabled. Same for Resume/`Resuming…`. Error test: API rejects; button re-enabled and toast shown. Run: fails.
2. `copy.ts`: `resuming: "Resuming…"` next to `pausing` in `C`.
3. `AgentRow.tsx`: add `requested` state; `onAction` sets it for `pause`/`resume` before `await act.run`, clears in the catch; `useEffect(() => setRequested(null), [displayState(agent)])`; when mapping actions, if `a.endpoint === requested`, render `disabled` and label `requested === "pause" ? C.pausing : C.resuming`. Run: passes. `pnpm test`, `pnpm lint` if defined. Commit `feat(web): keep pause/resume disabled until the agent state changes`.

## Final gate

`make test-go && make test-web && make test-menubar`. Then Sonnet review (repo policy), then publish the spec to Notion (Endurio HQ, Document Hub, less-claudish register, per the standing guidelines).

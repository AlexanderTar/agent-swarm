# Menubar Muse Support Specification

## Context

`apps/menubar` (Swift, SwiftPM package `SwarmBar`) is a standalone macOS
menubar app, built and installed independently from the Go daemon (`make
install-app`, not `make install-daemon`). It currently supports 4 agent
kinds — claude, codex, agy, cursor — and never got muse added when muse
support shipped on the daemon side (`internal/kinds/kinds.go` already has
`Muse AgentKind = "muse"`, `AgentKinds = []AgentKind{Claude, Codex, Agy,
Cursor, Muse}`, `Display() → "Muse"`). This spec adds muse as the 5th kind
on the menubar side to match.

Investigation (read-only, this session): `AgentKind.selectable`
(`Wire.swift`) is the single source almost everything else in the app
iterates over — `SettingsModel.agentRows`/`setEnabled`/`disableNotice`,
`CatalogRules.agentOptions`/`advisorOptions`, `MenuLabel.make`,
`UsageSection.pickerAgents`/`.selection` all derive from it. Two files have
their own exhaustive (`default`-free) switches over `AgentKind` that fail to
compile once a case is added without a corresponding arm:
`Copy.agentLabel`/`Copy.loginCommand` (`Copy.swift`) and
`IconName.init(_ kind:)` (`AgentIcon.swift`). Three more places hardcode a
literal icon-filename list that needs `"muse"` added:
`apps/menubar/scripts/bundle.sh`, `apps/menubar/scripts/check-bundle.sh`,
`Tests/SwarmBarTests/IconAssetsTests.swift`.

**Login command, confirmed against the actual Go source** (the
investigation's own guess was wrong — corrected here): `internal/adapter/
muse.go`'s `AuthOK` literally returns `"muse isn't signed in. Run `muse
login` in a terminal."` — so `Copy.loginCommand(.muse)` must be `"muse
login"`, matching the `"<binary> login"` shape codex/cursor already use
(not the bare-binary shape claude/agy happen to use for unrelated reasons).

**Icon — corrected mid-investigation, this is the load-bearing finding of
this spec.** The existing `web/src/assets/agents/muse.svg` (added in
commit `21f156a`, a hand-drawn four-point sparkle, `<title>Muse</title>`)
is the **wrong icon** — it was never sourced from anything, just invented.
The real answer: LobeHub's icon pack — `@lobehub/icons-static-svg`, the
exact same MIT-licensed source this repo already uses for `codex.svg` and
`agy.svg` — added official Muse Spark model-icon support in
[lobehub/lobe-icons#406](https://github.com/lobehub/lobe-icons/pull/406)
("fix: use Meta infinity icon for Muse Spark models"). Muse Spark has no
bespoke logo of its own; Meta attributes it to Meta's own brand mark (the
"infinity" glyph), the same way `gpt-4`-family models get OpenAI's icon
rather than a separate "GPT" icon. The correct source file is
`packages/static-svg/icons/meta.svg` in that repo (plain single-color
variant, not `meta-color.svg`/`meta-brand.svg` — the latter is a 103×24
wordmark, wrong aspect ratio for a square agent icon), fetched via
`gh api repos/lobehub/lobe-icons/contents/packages/static-svg/icons/meta.svg`
and confirmed to already be in the exact raw shape `codex.svg`/`agy.svg`
were sourced from: `<svg fill="currentColor" fill-rule="evenodd"
height="1em" style="flex:none;line-height:1" viewBox="0 0 24 24"
width="1em" xmlns="http://www.w3.org/2000/svg"><title>Meta</title><path
d="...">` (one path, no arc commands, so `normalize-icons.pl`'s arc-flag
fixup is a no-op on it).

This means the web app's `muse.svg` needs fixing too (out of the menubar's
own directory but the same underlying mistake, and the user explicitly
asked for the correct icon "applied everywhere") — `web/src/assets/agents/
muse.svg` should hold the *raw* LobeHub `meta.svg` content verbatim
(byte-identical convention to `codex.svg`/`agy.svg` there — confirmed by
reading both: same `1em`/`currentColor`/`style="flex:none;line-height:1"`
shape, untouched from upstream). `web/src/components/icons.tsx` needs no
change — it already imports `muse.svg` by path (`import museUrl from
"../assets/agents/muse.svg"`, from commit `21f156a`) and has no
per-icon logic that inspects the SVG's own `<title>` content, so swapping
the file's bytes is sufficient. A repo-wide grep for every other file
referencing `agy.svg`/`codex.svg`/`cursor.svg` by name found exactly two
hits: `web/src/components/icons.tsx` and `assets/icons/LICENSES.md` — the
menubar's own three hardcoded-list files (`bundle.sh`, `check-bundle.sh`,
`IconAssetsTests.swift`) were already found separately above. No other
"everywhere" exists in this repo.

`assets/icons/muse.svg` (the menubar's own copy) still needs the same
normalization every other menubar icon there already has: every existing
menubar icon (see `assets/icons/claude.svg`) has been run through
`apps/menubar/scripts/normalize-icons.pl` down to bare `width="24"
height="24" viewBox="0 0 24 24" xmlns="...">` with **no** `fill`/`style`
attribute at all on the `<svg>` tag (color comes from AppKit's template-
image tinting, not CSS `currentColor`, which NSImage's SVG renderer may not
honor the same way a browser does). `normalize-icons.pl` only strips
`role`/`style`/`width`/`height` from the `<svg>` tag — it does **not**
strip `fill`/`fill-rule`, so running the script alone on the web asset
would leave `fill="currentColor" fill-rule="evenodd"` in place,
inconsistent with every sibling icon's shape.

### Affected files

- `apps/menubar/Sources/SwarmBarKit/Wire.swift`
- `apps/menubar/Sources/SwarmBarKit/Copy.swift`
- `apps/menubar/Sources/SwarmBarUI/AgentIcon.swift`
- `apps/menubar/scripts/bundle.sh`, `apps/menubar/scripts/check-bundle.sh`
- `assets/icons/muse.svg` (new), `assets/icons/LICENSES.md`
- `apps/menubar/Tests/SwarmBarTests/IconAssetsTests.swift`,
  `CatalogRulesTests.swift`
- `web/src/assets/agents/muse.svg` (content fix, same wrong-icon mistake,
  same session)

### Collision warning

None known — this branch is cut from `335e255` (main); the parallel
`feat/inbox-notice-v2` branch touches only `internal/runtime`,
`internal/hook`, and `skills/` — no file overlap with this branch.

## Locked decisions

1. **`AgentKind.selectable` gains `.muse`, appended after `.cursor`** —
   matches the Go side's `AgentKinds` order exactly (`Claude, Codex, Agy,
   Cursor, Muse`). `AgentKind`'s raw-value enum gains `case muse` before
   `case fake` (matches Go's const block order; `Codable` round-trips the
   `"muse"` wire value automatically, no custom serialization needed).
2. **`Copy.agentLabel(.muse)` → `"Muse"`**, **`Copy.loginCommand(.muse)` →
   `"muse login"`** (confirmed against `internal/adapter/muse.go`'s
   `AuthOK`, not guessed).
3. **`IconName` gains `case muse`**, `IconName.init(_ kind:)` gains `case
   .muse: self = .muse`.
4. **New `assets/icons/muse.svg`**: sourced from LobeHub's `meta.svg`
   (`packages/static-svg/icons/meta.svg`, MIT), re-wrapped in the exact
   normalized shape every other menubar icon uses — run through
   `normalize-icons.pl` (a no-op on the arc-flag fixup, this path has no
   arc commands), then manually strip the `fill="currentColor"
   fill-rule="evenodd"` attributes the script doesn't touch, so the final
   tag matches `claude.svg`'s shape byte-for-byte in structure (`<svg
   width="24" height="24" viewBox="0 0 24 24"
   xmlns="http://www.w3.org/2000/svg"><title>Meta</title><path d="...">`
   — the `<title>` stays "Meta", unedited from upstream, matching
   `agy.svg`'s own precedent of keeping the source's title even though the
   local filename differs (`agy.svg`'s title is "Antigravity", not "Agy").
   `web/src/assets/agents/muse.svg` gets the same LobeHub `meta.svg`
   content, unnormalized (raw, matching `codex.svg`/`agy.svg`'s own
   as-is-from-upstream convention in that directory).
5. **`assets/icons/LICENSES.md` gets a `muse.svg` row**, same shape as the
   existing `codex.svg`/`agy.svg` rows: `| muse.svg | LobeHub Icons,
   `@lobehub/icons-static-svg` `meta.svg` (https://github.com/lobehub/
   lobe-icons) | MIT, Copyright (c) 2023 LobeHub |`.
6. **Three hardcoded icon-name lists gain `"muse"`**, inserted in the same
   `claude codex agy cursor` → `claude codex agy cursor muse` position
   (before `swarm`, which is always last — it's the app's own icon, not an
   agent kind) in `bundle.sh`, `check-bundle.sh`, and
   `IconAssetsTests.swift`'s `names` array.
7. **No change to `CatalogRules.advisorOptions`'s `kind == .claude`
   check** — "only Claude models can be an advisor" is a deliberate
   product rule unrelated to this task; muse simply appears in the
   non-advisor agent list once it's in `selectable`, same as codex/agy/
   cursor already do.

### Assumptions

- `SettingsModel.swift` and `MenuLabel.swift` need **no direct edits** —
  neither hardcodes a 4-kind list; both already derive from
  `AgentKind.selectable`, so adding `.muse` there propagates automatically.
  Verified by the investigation (no `default`-free exhaustive switch over
  `AgentKind` in either file).
- `UsageSection.selection`'s `.claude`-fallback special case is unrelated
  and untouched.
- Muse parity coverage in `SettingsModelTests.swift`,
  `NewOrchestratorRenderTests.swift`, `NewOrchestratorFormTests.swift` is
  optional (none of them hardcode an exhaustive kind list that would fail
  to compile without a muse case) — out of scope here; only the two files
  that genuinely need edits to keep compiling/passing
  (`IconAssetsTests.swift`, `CatalogRulesTests.swift`) are in the plan.

## DB models

None — this is a client-only app with no local persistence beyond
`UserDefaults` (`SettingsModel`), unaffected by adding an enum case.

## Model / API types

`apps/menubar/Sources/SwarmBarKit/Wire.swift`:
```swift
public enum AgentKind: String, Codable, Sendable, CaseIterable {
    case claude, codex, agy, cursor, muse, fake

    /// The five agents a user can enable, in settings order (claude first).
    public static let selectable: [AgentKind] = [.claude, .codex, .agy, .cursor, .muse]
}
```

`apps/menubar/Sources/SwarmBarKit/Copy.swift`:
```swift
public static func agentLabel(_ k: AgentKind) -> String {
    switch k {
    case .claude: return "Claude"
    case .codex: return "Codex"
    case .agy: return "Antigravity"
    case .cursor: return "Cursor"
    case .muse: return "Muse"
    case .fake: return "Fake"
    }
}

public static func loginCommand(_ k: AgentKind) -> String {
    switch k {
    case .claude: return "claude"
    case .codex: return "codex login"
    case .agy: return "agy"
    case .cursor: return "cursor-agent login"
    case .muse: return "muse login"
    case .fake: return "true"
    }
}
```

`apps/menubar/Sources/SwarmBarUI/AgentIcon.swift`:
```swift
public enum IconName: String, Sendable {
    case claude, codex, agy, cursor, muse, swarm

    public init(_ kind: AgentKind) {
        switch kind {
        case .claude, .fake: self = .claude
        case .codex: self = .codex
        case .agy: self = .agy
        case .cursor: self = .cursor
        case .muse: self = .muse
        }
    }
}
```

## Screens

No new screens. Existing settings rows, agent pickers, and menu labels
gain a 5th "Muse" entry wherever they already list claude/codex/agy/cursor
— no layout change, same components.

## All user-facing copy

- Agent label: `"Muse"`
- Login command shown in the not-signed-in hint: `"muse login"`

No other new copy.

## File list

Changed:
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`
- `apps/menubar/Sources/SwarmBarKit/Copy.swift`
- `apps/menubar/Sources/SwarmBarUI/AgentIcon.swift`
- `apps/menubar/scripts/bundle.sh`
- `apps/menubar/scripts/check-bundle.sh`
- `apps/menubar/Tests/SwarmBarTests/IconAssetsTests.swift`
- `apps/menubar/Tests/SwarmBarTests/CatalogRulesTests.swift`
- `assets/icons/LICENSES.md`
- `web/src/assets/agents/muse.svg` (content replaced with the correct
  LobeHub `meta.svg`, raw)

Added:
- `assets/icons/muse.svg`

Reused unchanged: `web/src/components/icons.tsx` (already wires `muse.svg`
by path since commit `21f156a`, no per-icon logic to touch),
`SettingsModel.swift`, `MenuLabel.swift`,
`CatalogRules.swift`'s own logic (only its tests gain an assertion),
`SettingsModelTests.swift`, `NewOrchestratorRenderTests.swift`,
`NewOrchestratorFormTests.swift`.

## Verification

1. `cd apps/menubar && swift build -c release` — confirms both exhaustive
   switches (`Copy.agentLabel`/`Copy.loginCommand`, `IconName.init`) compile
   with the new case added (they have no `default:`, so a missing arm is a
   compile error, not a silent gap).
2. `swift test --package-path apps/menubar` (or `make test-menubar` from
   the repo root) — `IconAssetsTests.testEveryIconLoadsAt24Points` must
   pass for `muse.svg` (24×24, loads via `NSImage(contentsOf:)`);
   `CatalogRulesTests`'s `loginCommand`-array assertion must include
   `"muse login"` in the right position.
3. `apps/menubar/scripts/bundle.sh` then `apps/menubar/scripts/
   check-bundle.sh <built .app path>` — confirms `muse.svg` actually ships
   inside the built bundle's `Contents/Resources/`.
4. Visually confirm the web board renders the Meta infinity glyph for
   muse agents (`npm run dev` in `web/`, or check any existing muse-kind
   fixture data in the board UI) — same icon as the menubar now shows.
5. Manual, after `make install-app`: open the Settings window, confirm a
   "Muse" row appears in the agent-enable list with the right icon, and
   that disabling/enabling it round-trips through `UserDefaults` like the
   other four.

## Explicitly out of scope

- Muse-specific test scenarios in `SettingsModelTests.swift`,
  `NewOrchestratorRenderTests.swift`, `NewOrchestratorFormTests.swift` —
  none are required for correctness (per Assumptions above); adding them
  is a reasonable follow-up, not blocking.
- Any change to `CatalogRules.advisorOptions`'s Claude-only advisor rule.
- Any web-side test coverage beyond confirming the file swap (no
  `IconAssetsTests`-equivalent exists in the web package today; not
  introducing one here).

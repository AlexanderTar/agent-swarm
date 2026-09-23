# Menubar Muse Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (or execute inline if dispatched as a single implementer) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `muse` as a 5th agent kind to the menubar app (`apps/menubar`)
and fix the wrong placeholder muse icon in both the menubar and the web
board, replacing it with the correct icon (Meta's own "infinity" mark,
which is what Muse Spark models are actually represented by upstream).

**Architecture:** Additive enum case + two exhaustive switch arms +
literal-list edits in Swift; a plain SVG file-content swap on the web side.
No architecture changes.

**Spec:** `docs/specs/2026-09-23-menubar-muse-support.md` — read it first,
especially the "Icon" section in Context and Locked decisions 4-5. This
plan does not repeat the reasoning.

## Global Constraints

- Exact copy: `Copy.agentLabel(.muse)` → `"Muse"`,
  `Copy.loginCommand(.muse)` → `"muse login"` (confirmed against
  `internal/adapter/muse.go`'s `AuthOK`, not a guess).
- `.muse` is always positioned after `.cursor`, before `.fake`, matching
  the Go side's `AgentKinds` order and the existing enum/array ordering
  convention.
- The icon is Meta's own "infinity" glyph (LobeHub's `meta.svg`), not a
  bespoke Muse logo — see spec Context for why.
- A reference copy of the exact raw icon bytes is at
  `meta-icon-raw.svg.reference` in this worktree's root (fetched via `gh
  api repos/lobehub/lobe-icons/contents/packages/static-svg/icons/meta.svg`
  and base64-decoded). Use it directly — do not re-fetch or retype the
  path data by hand. Delete this reference file in Task 1's own commit
  once its content has been copied into the two real destinations (it is
  scratch, not a repo asset).

---

### Task 1: Add and normalize the muse icon (both web and menubar)

**Files:**
- Add: `assets/icons/muse.svg` (menubar, normalized)
- Modify: `web/src/assets/agents/muse.svg` (web, raw content swap)
- Modify: `assets/icons/LICENSES.md`
- Delete: `meta-icon-raw.svg.reference` (scratch, worktree root)

**Interfaces:**
- Produces: `assets/icons/muse.svg` — consumed by Task 2's `AgentIcon.swift`
  change, `apps/menubar/scripts/bundle.sh`/`check-bundle.sh` (Task 4), and
  `IconAssetsTests.swift` (Task 5).

- [ ] **Step 1: Replace the web icon with the raw content**

```bash
cp meta-icon-raw.svg.reference web/src/assets/agents/muse.svg
```

Confirm the result is byte-identical in shape to `web/src/assets/agents/
codex.svg`/`agy.svg` (same `1em`/`currentColor`/`style="flex:none;
line-height:1"` attributes on the `<svg>` tag, `<title>Meta</title>`, one
`<path>`). Do not edit the title or path data.

- [ ] **Step 2: Create the normalized menubar icon**

```bash
cp meta-icon-raw.svg.reference assets/icons/muse.svg
perl apps/menubar/scripts/normalize-icons.pl assets/icons/muse.svg
```

The script strips `role`/`style`/`width`/`height` and sets
`width="24" height="24"`, and fixes packed arc flags (a no-op here — this
path has no arc commands). It does **not** strip `fill`/`fill-rule`, so
after running it, open `assets/icons/muse.svg` and manually remove
` fill="currentColor" fill-rule="evenodd"` from the `<svg>` tag (two
attributes, one edit) so the tag exactly matches `assets/icons/
claude.svg`'s shape: `<svg width="24" height="24" viewBox="0 0 24 24"
xmlns="http://www.w3.org/2000/svg"><title>Meta</title><path d="...">`.

Verify: `diff <(head -c 60 assets/icons/claude.svg) <(head -c 60 assets/icons/muse.svg)` — the `<svg ...>` opening tag shape (everything up to `<title>`) should differ only in nothing (both should read `<svg width="24" height="24" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">`).

- [ ] **Step 3: Add the LICENSES.md row**

In `assets/icons/LICENSES.md`, add a row to the existing table (matching
the `codex.svg`/`agy.svg` rows' exact format), inserted after the
`cursor.svg` row and before the `swarm.svg` row:

```markdown
| `muse.svg` | LobeHub Icons, `@lobehub/icons-static-svg` `meta.svg` (https://github.com/lobehub/lobe-icons) | MIT, Copyright (c) 2023 LobeHub |
```

- [ ] **Step 4: Clean up and commit**

```bash
rm meta-icon-raw.svg.reference
git add assets/icons/muse.svg assets/icons/LICENSES.md web/src/assets/agents/muse.svg
git commit -m "fix(icons): use the correct muse icon (Meta's mark, per LobeHub #406), not the invented sparkle"
```

---

### Task 2: `Wire.swift` — add `.muse` to the enum and `selectable`

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarKit/Wire.swift`

**Interfaces:**
- Produces: `AgentKind.muse`, `AgentKind.selectable` now includes it —
  consumed by every downstream file that iterates `selectable` (no other
  code change needed for those, per spec Assumptions).

- [ ] **Step 1: Edit**

```swift
public enum AgentKind: String, Codable, Sendable, CaseIterable {
    case claude, codex, agy, cursor, muse, fake

    /// The five agents a user can enable, in settings order (claude first).
    public static let selectable: [AgentKind] = [.claude, .codex, .agy, .cursor, .muse]
}
```

- [ ] **Step 2: Confirm it does NOT compile yet**

```bash
swift build --package-path apps/menubar -c release 2>&1 | head -30
```

Expected: compile errors in `Copy.swift` (`agentLabel`/`loginCommand`, no
`default:` case) and `AgentIcon.swift` (`IconName.init`, no `default:`
case) — this is expected and correct (exhaustive switches catching the
new case), fixed in Tasks 3-4.

- [ ] **Step 3: Commit**

```bash
git add apps/menubar/Sources/SwarmBarKit/Wire.swift
git commit -m "feat(menubar): add muse to AgentKind and selectable"
```

(This commit intentionally does not build on its own — Tasks 3-4 land in
the same PR before anyone needs `main` to build at this exact commit. If
your workflow requires every commit to build standalone, squash Tasks 2-4
into one commit instead; either is fine here since this branch is never
merged commit-by-commit, only as a whole.)

---

### Task 3: `Copy.swift` — add the `.muse` arms

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarKit/Copy.swift`
- Test: `apps/menubar/Tests/SwarmBarTests/CatalogRulesTests.swift`

**Interfaces:**
- Consumes: `AgentKind.muse` (Task 2).
- Produces: `Copy.agentLabel(.muse)`, `Copy.loginCommand(.muse)`.

- [ ] **Step 1: Edit `Copy.swift`**

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

- [ ] **Step 2: Update the failing test**

In `CatalogRulesTests.swift`, find:
```swift
XCTAssertEqual(["claude", "codex login", "agy", "cursor-agent login"], AgentKind.selectable.map(Copy.loginCommand))
```
Replace with:
```swift
XCTAssertEqual(["claude", "codex login", "agy", "cursor-agent login", "muse login"], AgentKind.selectable.map(Copy.loginCommand))
```

- [ ] **Step 3: Run to verify it passes**

```bash
swift test --package-path apps/menubar --filter CatalogRulesTests
```

- [ ] **Step 4: Commit**

```bash
git add apps/menubar/Sources/SwarmBarKit/Copy.swift apps/menubar/Tests/SwarmBarTests/CatalogRulesTests.swift
git commit -m "feat(menubar): add muse label and login command"
```

---

### Task 4: `AgentIcon.swift` — add the `.muse` icon arm

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarUI/AgentIcon.swift`

**Interfaces:**
- Consumes: `AgentKind.muse` (Task 2), `assets/icons/muse.svg` (Task 1).
- Produces: `IconName.muse`, resolved by `Icons.image(.muse)`.

- [ ] **Step 1: Edit**

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

- [ ] **Step 2: Confirm the package builds**

```bash
swift build --package-path apps/menubar -c release
```

Expected: clean build now (both exhaustive switches from Tasks 3-4 are
complete).

- [ ] **Step 3: Commit**

```bash
git add apps/menubar/Sources/SwarmBarUI/AgentIcon.swift
git commit -m "feat(menubar): add muse icon mapping"
```

---

### Task 5: Icon-name lists — `bundle.sh`, `check-bundle.sh`, `IconAssetsTests.swift`

**Files:**
- Modify: `apps/menubar/scripts/bundle.sh`
- Modify: `apps/menubar/scripts/check-bundle.sh`
- Modify: `apps/menubar/Tests/SwarmBarTests/IconAssetsTests.swift`

**Interfaces:**
- Consumes: `assets/icons/muse.svg` (Task 1).

- [ ] **Step 1: Write the failing test**

In `IconAssetsTests.swift`, find:
```swift
let names = ["claude", "codex", "agy", "cursor", "swarm"]
```
Replace with:
```swift
let names = ["claude", "codex", "agy", "cursor", "muse", "swarm"]
```

- [ ] **Step 2: Run to verify it fails**

```bash
swift test --package-path apps/menubar --filter IconAssetsTests
```

Expected: FAIL if `assets/icons/muse.svg` from Task 1 has any shape
mismatch (24×24, loads via `NSImage`); should actually PASS immediately if
Task 1 was done correctly — if so, this step confirms Task 1's icon is
already correct rather than catching a new bug. Either outcome is fine;
the point is running it now, with the new name in the list, actually
exercises the file.

- [ ] **Step 3: Edit the shell scripts**

`apps/menubar/scripts/bundle.sh`:
```bash
for icon in claude codex agy cursor muse swarm; do
  cp "$root/assets/icons/$icon.svg" "$out/Contents/Resources/$icon.svg"
done
```

`apps/menubar/scripts/check-bundle.sh`:
```bash
for icon in claude codex agy cursor muse swarm; do
  [ -f "$app/Contents/Resources/$icon.svg" ] || fail "missing $icon.svg"
done
```

- [ ] **Step 4: Run to verify everything passes**

```bash
swift test --package-path apps/menubar --filter IconAssetsTests
apps/menubar/scripts/bundle.sh /tmp/muse-check-Swarm.app
apps/menubar/scripts/check-bundle.sh /tmp/muse-check-Swarm.app
rm -rf /tmp/muse-check-Swarm.app
```

- [ ] **Step 5: Commit**

```bash
git add apps/menubar/scripts/bundle.sh apps/menubar/scripts/check-bundle.sh apps/menubar/Tests/SwarmBarTests/IconAssetsTests.swift
git commit -m "feat(menubar): ship muse.svg in the built bundle"
```

---

### Task 6: Full verification

- [ ] **Step 1: Full menubar test suite**

```bash
cd apps/menubar && swift build -c release && swift test
```

Or from the repo root: `make test-menubar`.

- [ ] **Step 2: Full bundle round-trip**

```bash
make app   # builds apps/menubar/.build/Swarm.app via bundle.sh
apps/menubar/scripts/check-bundle.sh apps/menubar/.build/Swarm.app
```

- [ ] **Step 3: Web sanity check**

```bash
cd web && npm run build
```

Confirms the SVG import still resolves cleanly (no test suite exists for
web icons specifically, per spec's explicitly-out-of-scope note — a clean
build is the bar here).

- [ ] **Step 4: Final report**

DONE / DONE_WITH_CONCERNS / BLOCKED, commit SHAs per task, one-line test
summary. Do NOT merge, push, or run `make install-app` — the user reviews
and handles deployment themselves (matching this session's established
pattern for the parallel `feat/inbox-notice-v2` branch too).

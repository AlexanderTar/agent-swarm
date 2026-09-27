# New Orchestrator Dialog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan unit by unit. Follow `superpowers:test-driven-development` for each behavior change and `skills/swarm-batching/SKILL.md` when delegating packages. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the New orchestrator dialog full width, fit its controls without normal window scrolling, and show each verified main repository once in a native macOS multi-select list.

**Architecture:** The Go repository service classifies primary checkouts and filters stored linked worktrees before the API returns them. The macOS form flattens `ReposResponse.all`, owns selected IDs, and presents them in a bounded native list. Advisor agent and model become separate controls; the existing spike payload and effort semantics remain.

**Tech Stack:** Go, Swift 6, SwiftUI/AppKit, XCTest, Swift Package Manager.

**Spec:** [New orchestrator dialog](../specs/2026-09-26-new-orchestrator-dialog.md)

## Global constraints

- macOS deployment floor remains 14. No new UI dependency.
- The window defaults near 820 × 790 pt, has a 760 pt minimum width, and shows at most eight repository rows.
- Repository rows use native multiple selection, not checkboxes. Only the repository list normally scrolls.
- Request is at least five text lines high. Agent and Advisor each use `Agent [menu] Model [menu]` on one line.
- Do not classify a repository from its folder name or remote alone. A separate clone with its own Git metadata remains eligible.
- Keep the existing `/api/repos` response and `POST /api/spikes` payload shapes; the 2026-09-27 follow-up below revises visible effort behavior.
- For every unit: read `test-driven-development/writing-good-tests.md`, name the break its test catches, write the smallest failing test, run it and record the expected RED, implement only enough to pass, run GREEN, refactor only while green, then commit that unit. Never write production code first.

## File map

| File | Responsibility |
| --- | --- |
| `internal/repos/git.go`, `git_test.go` | Decide whether one path is a primary checkout from resolved Git metadata. |
| `internal/repos/walk.go`, `walk_test.go`, `service.go`, `service_test.go` | Apply the decision during scan and manual add; filter stored worktrees. |
| `internal/httpapi/config_test.go` | Prove `/api/repos` excludes stored worktrees in Recent, Groups, and All. |
| `apps/menubar/Sources/SwarmBarKit/RepoPicker.swift`, `RepoPickerTests.swift` | Produce one sorted, present repository row per path from `all`. |
| `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift`, `CatalogRules.swift`, `NewOrchestratorFormTests.swift` | Reconcile selected IDs and expose separate advisor agent/model choices. |
| `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`, `Components.swift`, `NewOrchestratorRenderTests.swift` | Native repository list, subtle scroller, aligned controls, and five-line editor. |
| `apps/menubar/Sources/SwarmBar/App.swift` | Window default size and minimum width. |

## Review focus

- Stored worktree before rescan: `/api/repos` omits it from All, Recent, and Groups (Unit 3).
- Same remote, different clone: both independent primary checkouts stay (Unit 1).
- Missing selected path: chooser drops it, selection is reconciled, and user sees one notice (Unit 4).
- Advisor saved model vanished: choose a valid fallback or show an error; never submit an invalid pair (Unit 6).
- Large text or failure banner: footer remains reachable; reduce repository rows before outer scrolling (Unit 7).

## Swarm work packages

| Package | Units | Workflow | Done when | Dependency |
| --- | --- | --- | --- | --- |
| **A. Repository identity** | 1–3 | `tdd-reviewed` (coder → reviewer) | Scan, manual add, and API output exclude linked worktrees while preserving independent clones. | First; defines API input. |
| **B. macOS dialog** | 4–7 | `ui-tdd-reviewed` (coder → reviewer + `ui_reviewer`) | Default dialog matches the spec sketch and native selection works. | After A; consumes verified API data. |

Each package has one coder assignment and one review boundary. Its coder executes units in order, records RED/GREEN evidence per unit, and makes one commit per unit. Run package verification once after its units. Do not split RED, GREEN, or review into separate swarm tasks. If the same unit collects review findings twice, move it to a follow-up package instead of repeating the whole package.

---

### Package A · Unit 1: Identify primary checkouts

**Files:** `internal/repos/git.go`, `internal/repos/git_test.go`

**Interface:** Produce `PrimaryRepo(ctx context.Context, run execx.Runner, path string) bool`. It accepts a root only when Git's resolved directory and common directory identify that root's own `.git`. Keep `IsRepo` as a cheap prefilter.

- [ ] **RED test:** Add `TestPrimaryRepoIdentity` using real temporary Git repos: main checkout, linked worktree, worktree with `.git` directory and `commondir`, independent clone sharing the remote, invalid `.git` directory, and main checkout named `worktree-project`. Assert literal true/false for each path.
- [ ] **Verify RED:** Run `go test ./internal/repos -run '^TestPrimaryRepoIdentity$' -count=1`. Record a failure caused by the missing identity check, not setup or syntax.
- [ ] **GREEN:** Implement `PrimaryRepo` using resolved `--absolute-git-dir`, `--git-common-dir`, and `--show-toplevel` paths (or equivalent Git metadata), with path normalization. Do not compare remotes or folder names.
- [ ] **Verify GREEN:** Run the focused test, then `go test ./internal/repos`. Both pass; refactor only while green.
- [ ] **Commit:** Stage these two files and commit `fix(repos): identify primary checkouts`.

### Package A · Unit 2: Use identity in discovery and manual add

**Files:** `internal/repos/walk.go`, `walk_test.go`, `service.go`, `service_test.go`

**Interface:** Consume `PrimaryRepo`. Preserve `Walk(home, excludes)` for callers; thread the runner through its private scanner or another testable seam. `AddManual` rejects linked worktrees with `ErrNotRepo`.

- [ ] **RED test:** Add `TestWalkOnlyPrimaryRepositories` with a main repo, linked worktree, and independent clone. Assert exactly two primary paths. Add `TestAddManualRejectsLinkedWorktree`, asserting `ErrNotRepo` and no new row.
- [ ] **Verify RED:** Run `go test ./internal/repos -run 'TestWalkOnlyPrimaryRepositories|TestAddManualRejectsLinkedWorktree' -count=1`; record behavior failures.
- [ ] **GREEN:** Call `PrimaryRepo` from scan and `AddManual`, retaining symlink and exclusion rules. Replace fake `.git` fixtures only where they cannot satisfy the real Git check.
- [ ] **Verify GREEN:** Run the focused tests and `go test ./internal/repos`. Refactor only while green.
- [ ] **Commit:** Stage the four files and commit `fix(repos): skip linked worktrees during discovery`.

### Package A · Unit 3: Filter old rows from the API

**Files:** `internal/repos/service.go`, `service_test.go`, `internal/httpapi/config_test.go`

**Interface:** Keep the wire format. Filter existing linked-worktree rows from service methods used to build All, Recent, and Groups; leave database rows untouched. Missing paths may remain in the API for other consumers, but the dialog omits them.

- [ ] **RED test:** Add `TestReposRouteOmitsStoredWorktree` by seeding a worktree row before GET `/api/repos`. Assert its ID is absent from `all`, `recent`, and every `groups[].repos`, while main and independent-clone IDs remain. Add `TestAllOmitsStoredWorktree` for pre-rescan service behavior.
- [ ] **Verify RED:** Run `go test ./internal/repos ./internal/httpapi -run 'TestReposRouteOmitsStoredWorktree|TestAllOmitsStoredWorktree' -count=1`; capture the returned worktree ID as the expected failure.
- [ ] **GREEN:** Filter existing paths with `PrimaryRepo` before building API sections. Keep missing rows in the general API and avoid deleting persisted data.
- [ ] **Verify GREEN:** Run the focused tests, then `go test ./internal/repos ./internal/httpapi`.
- [ ] **Commit:** Stage the three files and commit `fix(repos): hide stored worktrees from repository API`.

**Package A verification:** Run `go test ./internal/repos ./internal/httpapi`. Reviewer checks each unit's test and commit, especially independent clones and stored rows.

### Package B · Unit 4: Flatten repository state

**Files:** `apps/menubar/Sources/SwarmBarKit/RepoPicker.swift`, `NewOrchestratorForm.swift`, `apps/menubar/Tests/SwarmBarTests/RepoPickerTests.swift`, `NewOrchestratorFormTests.swift`

**Interface:** Produce `RepoPicker.rows(_ response: ReposResponse) -> [Repo]` from `response.all` only. Reconcile selected IDs on load, rescan, and Add folder.

- [ ] **RED test:** In `RepoPickerTests`, assert one row per canonical path from repeated IDs and groups, distinct rows for same-name paths, name/path order, and omission of `missing`. In `NewOrchestratorFormTests`, select two IDs, rescan with one removed, and assert the surviving ID and one removal notice.
- [ ] **Verify RED:** Run `cd apps/menubar && swift test --filter 'RepoPickerTests|NewOrchestratorFormTests'`; record failing assertions.
- [ ] **GREEN:** Implement `rows` and selection reconciliation. Remove dialog-only query/search/sections/select-all state while keeping empty-query load, rescan, and Add folder. Keep `selection: [String]` for the payload; adapt to `Set<String>` at the list boundary.
- [ ] **Verify GREEN:** Run focused tests and `cd apps/menubar && swift test`.
- [ ] **Commit:** Stage the four files and commit `fix(menubar): flatten repository choices`.

### Package B · Unit 5: Native multi-select list

**Files:** `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`, `Components.swift`, `apps/menubar/Tests/SwarmBarTests/NewOrchestratorRenderTests.swift`

**Interface:** Bind SwiftUI `List(selection:)` to selected IDs. Use `NSTableView` only if a native run shows SwiftUI cannot deliver modifier selection within the height cap.

- [ ] **RED test:** Add render assertions for no checkbox rows, eight visible row heights at 12+ repos, bounded empty state, and path text. Each assertion names an observable break.
- [ ] **Verify RED:** Run `cd apps/menubar && swift test --filter NewOrchestratorRenderTests`; record expected geometry/semantics failure.
- [ ] **GREEN:** Replace `RepoRow` toggles with the native list. Set 30–32 pt row height, shortened full-path subtitle, full-path accessibility label/tooltip, `min(8, max(1, count))` visible rows, and existing `SubtleScrollerConfig`.
- [ ] **Verify GREEN:** Run focused render tests. In the app, check plain click, Command-click, Shift-click, arrows, Command-A, focus, and VoiceOver; capture 12+ repos. Record any SwiftUI limitation before choosing AppKit.
- [ ] **Commit:** Stage the three files and commit `feat(menubar): add native repository multi-selection`.

### Package B · Unit 6: Separate advisor Agent and Model

**Files:** `apps/menubar/Sources/SwarmBarKit/CatalogRules.swift`, `NewOrchestratorForm.swift`, `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`, `apps/menubar/Tests/SwarmBarTests/NewOrchestratorFormTests.swift`

**Interface:** Expose advisor agent and model options separately. Keep `AdvisorChoice.none` and `.pair(AgentKind, String)`. The follow-up below revises advisor effort selection.

- [ ] **RED test:** Test switching advisor agent, selecting a model, No advisor, a removed saved model, empty catalog, and payload effort. Assert a valid `.pair` or `.none` and no invalid pair in the body.
- [ ] **Verify RED:** Run `cd apps/menubar && swift test --filter NewOrchestratorFormTests`; record failing advisor assertion.
- [ ] **GREEN:** Add advisor-agent/model options and setters. Render aligned `Agent [menu] Model [menu]` rows; disable advisor Model for No advisor. The original implementation removed visible Effort; the 2026-09-27 follow-up restores it conditionally.
- [ ] **Verify GREEN:** Run focused tests and `cd apps/menubar && swift test`.
- [ ] **Commit:** Stage the four files and commit `feat(menubar): split advisor agent and model`.

### Package B · Unit 7: Fit the entire form

**Files:** `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`, `apps/menubar/Sources/SwarmBar/App.swift`, `apps/menubar/Tests/SwarmBarTests/NewOrchestratorRenderTests.swift`

**Interface:** Enforce minimum 760 pt width, default near 820 × 790 pt, 20–24 pt side padding, pinned footer, five-line Request, and adaptive list height before last-resort outer scrolling.

- [ ] **RED test:** Add render assertions for minimum width, five-line editor height, both menu rows fitting without wrap, and footer reachable with failure banner or larger text. Measure rendered geometry, not `.frame` source text.
- [ ] **Verify RED:** Run `cd apps/menubar && swift test --filter NewOrchestratorRenderTests`; record expected width/height failure.
- [ ] **GREEN:** Expand form to window width, set size in `App.swift`, pin footer, and allocate vertical space to list after fixed controls. Keep a last-resort subtle outer scroller for small displays or large text.
- [ ] **Verify GREEN:** Run focused test. Capture normal, 12+ repo, empty, error, and large-text app screenshots. Confirm no normal outer scrollbar, no clipped footer, and single-line selectors at 760 pt.
- [ ] **Commit:** Stage the three files and commit `fix(menubar): fit new orchestrator form`.

**Package B verification:** Run `cd apps/menubar && swift build -c release && swift test`, then `go test ./internal/repos ./internal/httpapi`. Reviewer checks all four commits; `ui_reviewer` checks native screenshots and modifier selection. Report every failing test by name, including failures outside changed files.

## Completion check

All seven units have recorded RED and GREEN runs and one commit each. Both package workflows pass, the full Swift and relevant Go suites pass, and native screenshots and interaction checks satisfy every acceptance condition in the spec.

## Follow-up: effort controls and advisor mode (2026-09-27)

The user requested primary Effort again and Advisor Effort whenever advice runs as a separate sub-agent. The existing `POST /api/spikes` wire shape already has both effort fields. Native mode is an advisor-capable Claude model advising a Claude primary agent; it uses Claude's built-in mechanism and omits advisor effort. All other selected advisor pairings use simulated mode and retain a valid selected advisor effort.

1. **RED Swift form tests:** Cover primary effort selection, native Claude/Claude omission despite a saved effort, Claude/Codex and Codex/Claude simulated advisor effort, No advisor, and normalization when the advisor model changes. Verify the failures before editing production Swift code.
2. **GREEN Swift form and view:** Restore the primary Effort menu; derive Advisor Effort options from the advisor model; show the advisor menu only in simulated mode when that model supports effort. Keep selected effort valid on agent/model changes. Fit the controls without hiding Request or the footer, and add native render assertions for both layouts. The 2026-09-27 correction below puts each Effort control on its Agent/Model line.
3. **RED then GREEN backend:** Test `resolveAdvisor` with native and simulated modes, including an explicit or Settings-provided effort. Clear effort for resolved native mode; retain it for simulated mode so `advisor_effort` reaches the separate advisor launch. For slug-encoded advisors, resolve the selected effort through the catalog before building the read-only command. Keep the existing advisor mode rule and wire shape.
4. **Verify and review:** Run focused RED/GREEN tests, then the Swift release build and full Swift suite, relevant Go suites, and code/visual reviews. Record screenshots and checkpoint evidence in `.superpowers/sdd/2026-09-26-new-orchestrator-dialog/progress.md`.

## Layout correction: inline Effort grid (2026-09-27)

The user clarified that the separate Effort rows are wrong. Match Settings Defaults: each role is one aligned row with `Agent [menu] Model [menu] Effort [menu]`. Keep blank Effort cells for unsupported or native advisor cases so both rows share column positions.

1. **RED:** Add native 760/820 pt geometry assertions that every label and picker in each role shares one vertical row and corresponding columns align between Agent and Advisor. The current separate Effort rows must fail this test.
2. **GREEN:** Replace the two HStacks and separate Effort rows with one six-column grid. Fit all columns at 760 pt; allow Model to widen above minimum width. Restore a near-790 pt default height if five Request lines and pinned footer still fit.
3. **Verify:** Run focused render tests, full Swift tests and release build. Inspect a native window capture at 760/820 pt, then request code and visual reviews.

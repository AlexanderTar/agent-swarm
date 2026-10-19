# Continuous integration

`.github/workflows/ci.yml` runs every check the repo has on pull requests to `main`, on pushes to `main` and on manual dispatch (`workflow_dispatch`). `main` requires a pull request for everyone but the repository owner, so a change reaches `main` through a PR whose checks are green. The job names below are the required status checks: rename a job only together with the branch protection.

## What runs where

| Job (status check name) | Runner | Commands |
|---|---|---|
| Go format and vet | ubuntu | `make web-build`, `make fmt vet` |
| Go tests and coverage | macOS | `make web-build`, `brew install tmux gnupg`, `go test -race -timeout 30m ./...`, `scripts/cover.sh` |
| Web tests and typecheck | ubuntu | `pnpm typecheck`, `pnpm test` (vitest with coverage), in `web/` |
| Web e2e (Playwright, mock daemon) | ubuntu | `pnpm exec playwright install --with-deps chromium`, `pnpm e2e`, in `web/` |
| Skills mirror is in sync | ubuntu | `make skills-sync`, then `git diff --exit-code -- internal/install/skills` and no untracked files there |
| Menu bar app tests and coverage | macOS | `make test-menubar` (`swift build -c release`, `swift test`, `apps/menubar/scripts/cover.sh`) |
| End-to-end (fake adapter) | macOS | `make web-build`, then `scripts/e2e.sh` with `SWARM_E2E_PATH` set |

The workflow sets `GO=go`, because the Makefile defaults `GO` to `/opt/homebrew/bin/go`. `make build` and the install targets codesign and never run in CI. The shared web setup (Node 22, pnpm 10, frozen-lockfile install, cached pnpm store) is `.github/actions/setup-web`.

## Reproduce a job locally

Run the job's commands from the table at the repo root with `GO=go` (or your own Go) in the environment. Each one runs unchanged on a Mac with the toolchain from the README.

`make test-go` is the local superset of the two Go jobs: it runs vet, the gofmt check, the race tests and the coverage gate in one go. CI splits it into two jobs so they run in parallel; if you change `test-go` in the Makefile, change the workflow with it.

`web/biome.json` exists, but `@biomejs/biome` is not a dependency and the code is not biome-clean, so CI does not run biome.

## Traps

- **Go packages embed `web/dist`.** `go vet ./...` and `go test` fail to compile until `make web-build` has run. `web/dist` is git-ignored, so a fresh checkout always needs the build first.
- **`scripts/e2e.sh` pins `PATH`** (`/opt/homebrew/bin:/usr/bin:/bin`) so the daemon can never find a real agent CLI. A toolchain installed elsewhere (CI's `setup-go`) is not on that PATH; set `SWARM_E2E_PATH` to the full list, with the Go directory first: `SWARM_E2E_PATH="$(dirname "$(command -v go)"):/opt/homebrew/bin:/usr/bin:/bin" scripts/e2e.sh`.
- **The Go tests run on macOS, not Linux.** The daemon only targets macOS. In a Linux container (non-root user, no git identity) `TestSlowClientDoesNotBlockOthers` fails every time because it depends on macOS socket-buffer behaviour, and `TestRenameWindowSetsTheWindowName` fails unless `LANG` is a UTF-8 locale (tmux replaces emoji with `_` otherwise; the macOS jobs set `LANG=en_US.UTF-8`). Linux is fine for `gofmt` and `go vet` (`GOOS=linux go vet ./...` is clean). Running the suite as root also fails the permission tests (`TestTokenEmptyIsReplacedUnreadableFails`, `TestCreateSucceedsWhenTheExcludeIsUnwritable`, `TestReclaimCodexHomesLogsARemoveFailureAndKeepsSweeping`), because root ignores file modes.
- **The e2e job runs on macOS because it needs tmux 3.6+.** Observed with Debian's tmux 3.3a (Docker, `golang:1.27-bookworm`): the pause and crash scenarios (`TestScenario08`, `09`, `10`) fail, with "tmux: server exited unexpectedly" in the daemon log. Ubuntu's apt tmux is older than 3.6 as well.
- **e2e scenarios must follow the daemon's current contract.** They had drifted (print-then-ask approvals, finishing checkpoints, waiver text in gate errors) and failed on `main` before CI existed. When you change a daemon message or flow, run `scripts/e2e.sh` and update the scenarios in the same change. Do not write a scenario that depends on winning a race with the daemon's 5s reconcile tick (`TestScenario08b` did, and flaked): put the freeze in place first, then free the slot.
- **Menubar window-capture tests need a live display.** Several `SwarmBarTests` (e.g. `DialogChromeTests`, `NativeFormPolishTests`, `NativeLayoutPolishTests`) render a real window and run `screencapture` on it. On an unattended Mac whose display has slept or locked, captures come back black or fail ("could not create image from window", or a PNG that "couldn't be removed" because it was never written), and different tests fail on each run. Check with `screencapture -x /tmp/probe.png` (an all-black image means the display is off), then run the suite with the display held awake for the whole test process: `cd apps/menubar && caffeinate -d -i -u swift test`. A backgrounded `caffeinate -u &` is not enough. Never skip these tests to get past it.
- **Registering fixture repos in e2e:** `h.finishRepo` renames the repo after the test, because the catalog is keyed by directory name and `t.TempDir()` leaves are all `001`.

## Failures that only show up on a runner

These passed on a developer Mac and failed on GitHub until their cause was found. Each is a test that assumed something a Mac gives for free, not a product bug. If one comes back, check the cause below before touching the test or loosening a tolerance.

### Menu bar OCR tests (macOS runner, `NewOrchestratorRenderTests`, `PopoverRenderTests`)

- **Symptom:** `testAgentAndAdvisorSelectorsShareAlignedSixColumnRows` (`Effort label midY 329 vs picker 358`, `minX 232 vs 222`) and `testEmptyChooserExplainsHowToAddRepositories` (OCR does not find "No repositories found"); sometimes another OCR test (`testDefaultsTabShowsLowTokenCheckboxAndCaption`) instead, because it is a coin flip.
- **Cause:** the tests OCR a transparent `cacheDisplay` bitmap. Vision reads transparent pixels as black. In dark mode the text is light and readable; in light mode (the runner's default) secondary grey text is invisible. Locally forcing `NSAppearance.aqua` reproduces it (`ocr count=0`). Separately, Vision on macOS 26 segments label boxes differently from macOS 27: it merges the popup's edge glyph into the label box and reads `-` where macOS 27 reads `•`, so label positions jump by 6 to 10 pt. The picker frames themselves are identical on 26.6 and 27.0.1 (checked to the point), so there is no layout bug.
- **Fix:** OCR input goes through `ocrBitmap` / `ocrCapture` / `ocrText` in `apps/menubar/Tests/SwarmBarTests/UITestSupport.swift`: dark appearance, opaque window background, 2x. Use them for any new OCR test; never OCR `host.cacheDisplay` into a bare bitmap. A tolerance was not loosened.
- **Trap:** a negative OCR assertion ("no red error is visible") passes vacuously when the text is invisible to Vision. Do not trust one that was not first shown to find the text it is looking for.
- **Reproduce:** `cd apps/menubar && caffeinate -d -i -u swift test --filter 'NewOrchestratorRenderTests|PopoverRenderTests|BoardHandoffRenderTests'` locally (dark Mac); to see the old failure, make the test host `NSAppearance(named: .aqua)` with a bare transparent bitmap. macOS 26 behaviour can only be checked on the `macos-latest` runner (macOS 26.6, Xcode 26.6). The `xcode-27` preview image has no usable window server and fails 15 tests; do not use it.

### Web e2e (Playwright) on a slow runner

- **Symptom:** `10 request images do not overflow` (`Received: 375.00003`, or 430.00001 / 768.00003), `hierarchy, filters and view switching` at tablet width (the `Priority` combobox "intercepts pointer events" for the Kanban tab click), `touch targets are at least 44x44 below md` (`agents-count present`, `Received: 0`), `artifact wraps long text` (`Expected: > 20, Received: 20`), and any test that opens New orchestrator (`waiting for getByRole('menuitem', { name: 'New orchestrator' })`). Locally all pass, which is why none of it showed up before CI.
- **Cause:** each acts or measures before the page is ready, and one has a false premise.
  - The sheet slides in over 500 ms (`sheet.tsx`); mid-animation its box is a float-rounded transform that lands either side of the viewport width. `reducedMotion: "reduce"` does not stop it (`data-[state=open]:animate-in` outranks `motion-reduce:animate-none`).
  - `evaluateAll` does not auto-wait, so on a slow runner it ran before the list rendered.
  - The artifact scroller is visible while its body is still loading.
  - Below 1100 px the non-modal Details sheet is full width on purpose, so it covers the view tabs: the old test only passed when its click beat the slide-in. This is the designed layout, not a bug users hit; they close Details first (which drops the selection).
- **Fix:** `settled(locator)` in `web/e2e/helpers.ts` waits for the enter animation before measuring; `expect44` waits for the first match; the artifact test waits for the last line; the tablet flow asserts that Details covers the tab, closes it, then switches view; `openNewOrchestrator` picks the header button or the New item menu from the viewport width (`isVisible()` does not wait, so it used to take the phone path on a slow runner and wait 30 s for a menu item that does not exist). Do not raise Playwright timeouts or set `reducedMotion`.
- **Reproduce:** the runner is just slower, so throttle the CPU. Copy `web/e2e/mock.spec.ts`, swap its first-line import for a `test` that applies `Emulation.setCPUThrottlingRate` (rate 4) through `page.context().newCDPSession(page)` in a `page` fixture, and run it with `pnpm exec playwright test --project=mock --project=mobile --project=tablet`. Before the fix this fails the same tests with the same messages; busy-loop CPU hogs alone do not (a 12-core Mac has cores to spare). Do not commit the copy.
- **Trap:** `web/playwright.config.ts` falls back to the installed Chrome when the bundled Chromium is missing, so a Mac runs a newer browser than CI's pinned Chromium. If a failure will not reproduce, run `pnpm exec playwright install chromium` first. If `pnpm` itself fails in a fresh shell (corepack resolves a pnpm with no `pnpm.cjs`), run `node ~/.cache/node/corepack/v1/pnpm/10.25.0/bin/pnpm.cjs` (the version CI pins).

### End-to-end pause and crash scenarios (`TestScenario08`, `08b`, `10`)

- **Symptom:** `session = stopping, want paused within 6s` (08), `second spawn ... want queued:true` (08b, a knock-on of 08: its slots free up while the stuck agents resolve), `TestScenario10Crash` timing out. It passes or fails run to run on the same commit.
- **Cause:** the daemon detects a gone pane on its next reconcile tick, `ReconcileLoop` (every 5 s), and the scenarios allow 6 s (spec 23.2). A tick does per-pane work. The fake agent sleeps forever and the suite used to leave every agent alive, so by scenario 10 there were dozens of live panes (peak 48) and a tick took about 1 s on a 3-vCPU runner (0.15 s with the panes cleaned up). The loop also slept `every` after the tick instead of measuring from its start, so the gap was 5 s plus the tick, twice.
- **Fix:** `newHarness` registers `endLeftoverAgents`, which kills every live pane when the test ends, and `ReconcileLoop` measures the period from the tick start (`TestReconcileLoopMeasuresThePeriodFromTheTickStart`). The 6 s waits are the spec and were not widened. A scenario that starts agents must leave them to this cleanup, not rely on them staying alive for a later scenario.
- **Also a runner-timing fix:** the handoff scenario (`scripts/e2e/handoff_test.go`) polls up to 10 s for the successor's `first_sync_at` instead of reading it once. The boot sync is the successor process's own call, so on a loaded machine it lands after the session row exists.
- **Reproduce and check:** run `scripts/e2e.sh` and sample `tmux -L swarm-e2e list-panes -a | wc -l` every few seconds from another shell: the count should fall back to zero after each test. To stress it, run 24 to 48 busy loops (`for i in $(seq 48); do (while :; do :; done) & done`, then `kill $(jobs -p)`) while the suite runs.
- **Trap:** `scripts/e2e.sh -count=N` is not a stability check: several scenarios (epic approval, repository scope, usage, stale acceptance) cannot run twice against one daemon and fail on the second pass for unrelated reasons. Run the whole script several times instead; each run starts a fresh daemon.
- **Follow-up (not fixed here):** the reconcile tick's cost grows with the number of live agents, which in production stretches crash and pause detection past the spec's 6 s once a user has a few dozen agents. Profiling and batching the per-pane tmux calls in `Reconcile` is a separate chore.

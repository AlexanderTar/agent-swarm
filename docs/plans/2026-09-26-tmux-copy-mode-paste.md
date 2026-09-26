# Plan: guard tmux pastes against copy-mode swallowing Enter

Companion to `docs/specs/2026-09-26-tmux-copy-mode-paste.md`. Branch
`fix/tmux-copy-mode-paste`, worktree
`/Users/alexandertar/GitHub/agent-swarm-copymode-paste`.

## Task 1 — `cancelCopyModeIfNeeded` guard (spawn package)

1. Write failing tests in `internal/spawn/tmux_test.go`:
   - `TestCancelCopyModeIfNeededCancelsWhenPaneIsInCopyMode` — fake runner,
     `display -p -t sess #{pane_in_mode}` → `"1\n"`, expect
     `send-keys -t sess -X cancel` issued after it (assert via `f.Calls()`).
   - `TestCancelCopyModeIfNeededDoesNothingWhenPaneIsNotInCopyMode` — same,
     `→ "0\n"`, expect only the display call.
   - `TestPasteLineCancelsCopyModeSoTheLineIsSubmitted` — real tmux
     (`newSpawner`), force copy-mode with `copy-mode -t <name>`, then call
     `PasteLine` and assert the line is still delivered whole.
   Run: `go test ./internal/spawn/... -run TestCancelCopyModeIfNeeded` and
   the third test name — confirm compile failure (method doesn't exist yet)
   / timeout failure once compiling against a stub.
2. Implement `cancelCopyModeIfNeeded` in `internal/spawn/tmux.go`: query
   `#{pane_in_mode}` via `display -p -t <target>`; if `"1"`, run
   `send-keys -t <target> -X cancel`; swallow/log errors, never fail the
   paste.
3. Call it at the top of `pasteViaTempFile`, before the chunk loop.
4. Run: `go test ./internal/spawn/... -run TestCancelCopyModeIfNeeded|TestPasteLineCancelsCopyModeSoTheLineIsSubmitted -v` — confirm pass.
5. Commit: `internal/spawn/tmux.go`, `internal/spawn/tmux_test.go`.

## Task 1b — move the guard into `Keys` (review follow-up)

Opus review: `reconcile.go:830` (prompt auto-answer), `pause.go:330`
(interrupt), `agents.go:1280,1380`, `checkpoint.go:1519` (startup dialogs,
interrupt-before-kill) all call `s.Tmux.Keys` directly and hit the same
copy-mode swallow. `pasteViaTempFile` already ends with
`s.Keys(ctx, name, "Enter")`, so moving the guard into `Keys` keeps the paste
covered while fixing every other call site from one place.

1. Write failing tests: `TestKeysCancelsCopyModeBeforeSendingKeys` (fake
   runner, `pane_in_mode=1` → cancel issued before the actual keys) and
   `TestKeysDoesNotCancelCopyModeWhenPaneIsNotInCopyMode`. Run, confirm they
   fail against the current `Keys` (no guard there yet).
2. Move the `cancelCopyModeIfNeeded` call from the top of `pasteViaTempFile`
   into `Keys`, before its `send-keys` call.
3. Rename the misleading wait message in
   `TestPasteLineCancelsCopyModeSoTheLineIsSubmitted` ("the shell to start
   reading" → "the pane to exist"; the check only confirms `Capture`
   succeeds).
4. Run `go test ./internal/spawn/... -v` — confirm all pass, including the
   pre-existing `cancelCopyModeIfNeeded`-direct and paste regression tests.
5. Commit.

## Task 2 — log the quota-reset idle skip (runtime package)

1. Write failing test in `internal/runtime/wake_test.go`:
   `TestWakeOnQuotaResetLogsSkipWhenPaneNotIdle` — seed a Fake session with a
   non-idle capture, override `s.Log` to collect lines, call
   `WakeOnQuotaReset`, assert a line naming the agent and session mentions
   "not idle". Run and confirm it fails (no such log line exists yet).
2. Implement in `internal/runtime/wake.go`: add `a.name` to the
   `WakeOnQuotaReset` row query and scan; log
   `"wake: quota-reset skip for %s (session %s): pane not idle"` in the
   `else` branch where `ad.Idle(capture)` is false (and no capture error).
3. Run the test — confirm pass.
4. Commit: `internal/runtime/wake.go`, `internal/runtime/wake_test.go`.

## Task 2b — capture-failure log and skip-log throttle (review follow-up)

1. `wake.go:541`: log when `s.Tmux.Capture` itself errors, alongside the
   existing non-idle skip log — currently a capture failure is silent.
2. Throttle the non-idle skip log to once per session per cutoff:
   `checkQuotaResets` (`cmd/swarm/daemon.go`) calls `WakeOnQuotaReset` every
   minute for up to an hour after a cutoff, so an unthrottled log would write
   up to ~60 lines for one stuck session. Add an in-memory
   `quotaSkipLogged map[string]int64` (sessionID → cutoff millis already
   logged) next to the other `bookkeepingMu`-guarded maps in `model.go`;
   only log if this session hasn't been logged for this exact cutoff yet.
3. Tests in `wake_test.go`: capture-failure log line asserts the error is
   included; throttle test calls `WakeOnQuotaReset` twice with the same
   cutoff and non-idle capture, asserts exactly one skip log line.
4. Commit.

## Task 3 — recovery check (no code, corrected)

Investigate: does a pane stuck by this bug recover without intervention?
Verified live with `tmux list-keys -T copy-mode` that `Escape` is bound to
`cancel` there but `Enter` has no binding at all.

**Correction from the first pass:** the original claim — "the next paste
attempt after this fix ships unsticks the pane" — is wrong. `tryPaste`
(`wake.go:268`) and `WakeOnQuotaReset` (`wake.go:541`) both check
`ad.Idle(capture)` *before* calling `PasteLine`, and the stuck pane's
leftover unsent text is exactly what makes `Idle` false. So no wake path
ever reaches the (now-guarded) paste call for an already-stuck session —
guarding `Keys`/`PasteLine` doesn't matter for a pane that's already stuck,
because nothing sends it any keys at all anymore. Recovery requires manual
intervention: a deploy note (below) covers checking live panes once, after
deploy, for stuck unsent text and pressing Enter by hand. No input-clearing
mechanism is added — out of scope per the spec. Recorded in the spec's
Verification section — no code change beyond the deploy note itself.

## Task 4 — docs

Write and commit this plan and the spec first (this commit), before the
checks in Task 5.

## Task 5 — full verification

Run, in order:
1. `make web-build` (only if `TestBoardServedAtRoot` 503s below)
2. `go test ./... -count=1`
3. `go vet ./...`
4. `test -z "$(gofmt -l .)"`

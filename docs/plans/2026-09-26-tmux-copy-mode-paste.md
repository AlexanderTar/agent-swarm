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

## Task 3 — recovery check (no code)

Investigate (already done while writing the spec): does a pane stuck by this
bug recover without intervention? Verified live with `tmux list-keys -T
copy-mode` that `Escape` is bound to `cancel` there but `Enter` has no
binding at all. Nothing in the idle-wake path sends `Escape`, so the pane
does not self-heal; the next paste attempt after this fix ships is what
finally supplies a working Enter. Recorded in the spec's Verification
section — no code change.

## Task 4 — docs

Write and commit this plan and the spec first (this commit), before the
checks in Task 5.

## Task 5 — full verification

Run, in order:
1. `make web-build` (only if `TestBoardServedAtRoot` 503s below)
2. `go test ./... -count=1`
3. `go vet ./...`
4. `test -z "$(gofmt -l .)"`

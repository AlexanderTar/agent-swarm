# Spec: guard tmux pastes against copy-mode swallowing Enter

## Context

`TmuxConf()` (`internal/spawn/tmux.go:~38-50`) sets `mouse on` so a wheel
scroll lets an operator scroll conversation history in an agent pane, per
`docs/plans/2026-09-22-tmux-mouse-scroll.md`. A side effect: a wheel scroll
also puts that pane into tmux copy-mode.

`pasteViaTempFile` (`internal/spawn/tmux.go`) is the sole implementation
behind `PasteLine`, used to relay inbox notices and idle wake tokens into an
agent's pane. It loads a buffer, runs `paste-buffer` (chunked), then sends an
`Enter` keypress via `send-keys`. `paste-buffer` writes straight to the pane's
tty and is not affected by pane mode, but `send-keys Enter` goes through
tmux's key-table dispatch, and copy-mode's key table (`bind-key -T copy-mode`,
confirmed live with `tmux list-keys -T copy-mode`) has no binding for `Enter`
at all — it is silently dropped, never reaching the program underneath.
`Escape` *is* bound there, to `send-keys -X cancel` (exits copy-mode), but
nothing in the normal paste path ever sends `Escape`.

Incident: on 2026-09-25 at 22:27Z a relay notice for Muse agent
`go-migration-agent-debug` was pasted into its pane while the pane was in
copy-mode. The text landed in the input box but the Enter was swallowed, so
it sat there unsent. `internal/adapter/muse.go:~347`'s idle check requires an
empty `❯` line, which then failed on every subsequent poll (the box was no
longer empty), so `internal/runtime/wake.go:~272`'s `tryPaste` logged "pane
not idle" from then on — including the quota-reset wake at 23:47:11Z.
`WakeOnQuotaReset` (`internal/runtime/wake.go:~509-547`) skipped that
non-idle session with no log line at all, which hid the whole chain: nothing
in the daemon's logs pointed at the stuck pane.

## Locked decisions

- The guard lives in one place: `pasteViaTempFile`, the only implementation
  behind `PasteLine`. `Keys()` (the other `send-keys` call site) is used for
  interrupts (`InterruptKeys`) and confirm actions elsewhere in
  `internal/runtime`; those are out of scope — the incident and the fix are
  about paste delivery, not about every keypress sent into a pane.
- The check is best-effort: a failure to query `#{pane_in_mode}` or to
  cancel must not block the paste itself. A stuck pane is better than a
  daemon that stops delivering pastes because a diagnostic query failed.
- `WakeOnQuotaReset` gets exactly one added log line, naming the agent and
  session, when it skips a candidate because the pane isn't idle. No other
  behavior in that function changes.
- No fix for the already-stuck-text case beyond what falls out of (1): the
  next paste attempt after this fix ships will cancel copy-mode and send a
  real Enter, unsticking the pane, but the old unsent text and the new
  paste land as one concatenated line (see Verification/recovery below).
  Cleaning that up is out of scope.

## Design

`cancelCopyModeIfNeeded(ctx, name)` on `*Spawner`:
1. `tmux display -p -t <name> #{pane_in_mode}`.
2. If the trimmed output isn't `"1"`, or the query errored, return (no-op).
3. Otherwise `tmux send-keys -t <name> -X cancel`, logging (not failing) on
   error.

Called once at the top of `pasteViaTempFile`, before the chunk loop.

`WakeOnQuotaReset`: the row query now also selects `a.name` (same join
pattern `wakeCandidates` already uses). When the idle-paste fallback finds
`ad.Idle(capture) == false` (and no error), it logs
`"wake: quota-reset skip for %s (session %s): pane not idle"`.

## Files

- `internal/spawn/tmux.go` — add `cancelCopyModeIfNeeded`; call it from
  `pasteViaTempFile`.
- `internal/spawn/tmux_test.go` — fake-runner unit tests for
  `cancelCopyModeIfNeeded` (in-mode cancels, not-in-mode doesn't); a
  real-tmux regression test that forces copy-mode and asserts `PasteLine`
  still submits.
- `internal/runtime/wake.go` — `WakeOnQuotaReset` selects `a.name`, logs the
  non-idle skip.
- `internal/runtime/wake_test.go` — test asserting the skip log names the
  agent and session.

## Verification

- `go test ./... -count=1`
- `go vet ./...`
- `test -z "$(gofmt -l .)"`
- Fail→pass: `TestPasteLineCancelsCopyModeSoTheLineIsSubmitted` times out
  waiting for the pasted line without the fix (confirmed by temporarily
  reverting `tmux.go`'s change and re-running); passes with it.
- Recovery finding: a pane stuck by this bug does **not** self-heal. Nothing
  in the idle-wake path sends any key bound to `cancel` in copy-mode.
  `Escape` (sent by unrelated quiescing/pause interrupt paths,
  `internal/runtime/agents.go`, `checkpoint.go`, `pause.go`) is bound to
  `cancel` and would exit copy-mode if it happened to fire, but Escape is
  consumed by copy-mode's key table too, so it would not submit the
  already-stuck text — only exit copy-mode, leaving the text still unsent
  until an actual Enter reaches the pane. The fix in this spec is what
  finally supplies that Enter, on the next paste attempt.

## Explicitly out of scope

- Guarding `Keys()`'s other call sites (interrupts, confirm actions).
- Cleaning up or de-duplicating text left over from a paste that occurred
  before this fix shipped.
- Any change to `TmuxConf()`'s `mouse on` setting.

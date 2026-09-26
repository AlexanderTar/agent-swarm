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

- The guard lives in `Spawner.Keys`, the one function every `send-keys` call
  in the codebase routes through: prompt auto-answers (`reconcile.go:830`),
  pause interrupts (`pause.go:330`), startup dialogs (`agents.go:1280`),
  interrupt-before-kill (`agents.go:1380`, `checkpoint.go:1519`), and
  `pasteViaTempFile`'s own trailing `Enter`. All of these are swallowed by
  copy-mode exactly the way the paste's Enter was — an auto-answered prompt's
  Enter is dropped and Down only moves the copy-mode cursor; an interrupt's
  Escape/C-c is consumed by copy-mode's own `cancel` binding for Escape
  instead of reaching the program. Putting the guard in `Keys` covers all of
  them from one place and shrinks the race window for the paste case from
  "up to ~500ms + the paste itself" (query done before the paste started) to
  the few milliseconds between the query and the `send-keys` call it guards.
- The check is best-effort: a failure to query `#{pane_in_mode}` or to
  cancel must not block the keys that follow. A stuck pane is better than a
  daemon that stops sending keys because a diagnostic query failed.
- `WakeOnQuotaReset` gets exactly one added log line, naming the agent and
  session, when it skips a candidate because the pane isn't idle. No other
  behavior in that function changes.
- **Correction:** an earlier draft of this spec claimed the next paste
  attempt after this fix ships would unstick an already-stuck pane. That is
  wrong and is not what this fix does. `tryPaste` (`wake.go:268`) and
  `WakeOnQuotaReset` (`wake.go:541`) both gate on `ad.Idle(capture)` *before*
  ever calling `PasteLine` — and the stuck, unsent text in the pane's input
  box is exactly what makes `Idle` return false. No wake path ever pastes
  into an already-stuck pane, guard or no guard: it is filtered out before
  the paste call it would guard. See Verification/recovery below for what
  actually clears a stuck pane.
- No cleanup of text already left over from a paste that occurred before
  this fix shipped, and no new mechanism to clear a pane's input line —
  out of scope; see the deploy note below instead.

## Design

`cancelCopyModeIfNeeded(ctx, name)` on `*Spawner`:
1. `tmux display -p -t <name> #{pane_in_mode}`.
2. If the trimmed output isn't `"1"`, or the query errored, return (no-op).
3. Otherwise `tmux send-keys -t <name> -X cancel`, logging (not failing) on
   error.

Called from `Keys(ctx, name, keys...)`, before the `send-keys` call it
guards. `pasteViaTempFile` needs no separate call: its final step is already
`s.Keys(ctx, name, "Enter")`.

`WakeOnQuotaReset`: the row query now also selects `a.name` (same join
pattern `wakeCandidates` already uses). When the idle-paste fallback finds
`ad.Idle(capture) == false` (and no error), it logs
`"wake: quota-reset skip for %s (session %s): pane not idle"`.

## Files

- `internal/spawn/tmux.go` — add `cancelCopyModeIfNeeded`; call it from
  `Keys`, the shared `send-keys` primitive.
- `internal/spawn/tmux_test.go` — fake-runner unit tests for
  `cancelCopyModeIfNeeded` directly (in-mode cancels, not-in-mode doesn't)
  and for `Keys` (cancel precedes the actual keys, only when in copy-mode);
  a real-tmux regression test that forces copy-mode and asserts `PasteLine`
  still submits.
- `internal/runtime/wake.go` — `WakeOnQuotaReset` selects `a.name`, logs the
  non-idle skip (throttled to once per session per cutoff) and a `Capture`
  failure.
- `internal/runtime/wake_test.go` — tests asserting the skip log names the
  agent and session, is throttled, and that a capture failure is logged too.

## Verification

- `go test ./... -count=1`
- `go vet ./...`
- `test -z "$(gofmt -l .)"`
- Fail→pass: `TestPasteLineCancelsCopyModeSoTheLineIsSubmitted` times out
  waiting for the pasted line without the fix (confirmed by temporarily
  reverting `tmux.go`'s change and re-running); passes with it.
  `TestKeysCancelsCopyModeBeforeSendingKeys` fails the same way before the
  guard moves into `Keys`.
- **Recovery finding (corrected):** a pane already stuck by this bug does
  **not** self-heal, and this fix does not unstick it either. The stuck,
  unsent text in the input box makes `ad.Idle(capture)` return false, and
  both wake paths (`tryPaste`, `WakeOnQuotaReset`) check `Idle` *before*
  calling `PasteLine` — so a stuck session is filtered out of every future
  wake attempt before it ever reaches the (now-guarded) paste call. Nothing
  in the normal wake/idle-check path sends any key at all to a session it
  has already decided is non-idle, so the pane's copy-mode state and its
  stuck text are never touched again automatically.
  **Deploy note:** after this fix ships, check live panes for unsent input
  text left over from before the fix (e.g. via `swarm attach` or a pane
  capture) and press Enter by hand in any that are stuck. This is a one-time
  cleanup for pre-existing damage, not an ongoing operational step — panes
  affected after this fix ships get the guard automatically and never reach
  this state.

## Explicitly out of scope

- Clearing or de-duplicating text already sitting in a pane's input line
  (no new mechanism to reset/clear a pane's input; the deploy note above is
  a manual, one-time check, not automation).
- Any change to `TmuxConf()`'s `mouse on` setting.
- Making `tryPaste`/`WakeOnQuotaReset` retry a non-idle pane differently, or
  otherwise changing the `Idle` gate itself.

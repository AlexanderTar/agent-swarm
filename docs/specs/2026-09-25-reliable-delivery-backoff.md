# 2026-09-25 — Reliable delivery with exponential backoff (native + tmux paste)

## Context

Live correlation 2026-09-25 (swarm DB + muse transcripts + daemon logs):

- Latest muse agents (all `kind=muse`): `go-migration-railway-orchestrator` (orchestrator,
  `01a0d824-…`), `go-migration-deployment`, `repoint-api-worker-to-coder`,
  `verify-preconditions-coder`, `deploy-api-and-worker-coder` (`01a0d889-…`),
  `post-green-cleanup-coder`, `add-migration-000009-for-coder`,
  `remove-stale-eve-status-coder` (`01a0d8c4-…`). All `sessions.state=completed`.
- Muse transcripts (`~/.local/share/muse/sessions/2026/09/25/<uuid>/session.jsonl`):
  daemon pastes arrive as `runtime.user_intent.accepted` with
  `refill_blocks=[{text:"[Pasted Content N chars]"}]` and
  `model_messages=[{content:"[swarm] Durable runtime events …"}]` — proven working
  (5 paste deliveries in orchestrator transcript; coder `01a0d889-…` line 3056
  `LEN=1376` intact). Interspersed manual nudges from the user in the same
  sessions: `"check your inbox"`, `"check inbox"`, `"confirmed"`,
  `"give me a repo confirmation cli command"` — the user coordinating because
  auto-delivery stalled.
- Latencies (DB `messages.created_at → delivered_at`): assignments 18–24 s
  (expected: `pasteDelay` 20 s + 5 s tick); one `finding` to
  `deploy-api-and-worker-coder` 13:00:12 → 13:03:51 (3 m 39 s, busy pane);
  three relays to the orchestrator 13:17/13:19/13:20 → 13:32:01 (12–14 min,
  7-pending batch, `agent.undeliverable` fired at 13:22:48). Long busy stretches
  pile up; paste only fires when idle.
- Daemon log: `2026/09/24 18:16:54 wake: pane command "" for
  s13-2-ts-deletion-coder matches no ProcessNames pattern, skipping idle paste`
  (muse coder, empty `pane_current_command` — transient during start/stop).
- Architecture: `Muse.Wake` returns `(false, nil)` always
  (`internal/adapter/muse.go:304`); `Cursor.Wake` likewise. Native probe
  (`muse session-message send`) fails `external_agent_ingress_closed`
  (`muse_wake_probe_test.go:57`). So muse/cursor are paste-only by design.
  Claude native is honest (`PublishWake` returns subscriber count);
  Codex/Agy claim success on CLI-accept, not on live-thread surfacing.

Root cause (systematic-debugging Phase 1–3): delivery is best-effort with a
fixed 30 s spacing, memory-only counters, and no per-session isolation —
not a single transport bug. Three defects at the source:

1. No exponential backoff. `pasteRetry = 30 s` fixed; `pasteAttempts` /
   `lastPasteAttemptAt` in-memory only (`wake.go:296`), lost on restart.
2. One bad pane aborts the tick. `WakeDue` returns on any per-session
   `tryPaste` error (`wake.go:198`), skipping every later session that tick;
   native `markWoken` error likewise.
3. No unified retry across transports. Native errors are logged and fall
   through, paste skips are logged and counted, but neither feeds a common
   consecutive-failure counter with backoff — busy, empty-command, capture
   and paste errors all behave differently.

## Locked decisions

1. **One delivery path, two transports, one backoff.** Each `WakeDue` pass per
   session: try native (unless `NativeTried` for this batch), then paste (when
   delay/idle gates pass). Any failure — native error, `delivered=false`,
   pane-command mismatch, not-idle, capture error, paste error — increments
   the same consecutive-failure counter and schedules the next attempt with
   exponential backoff. Any success (`markWoken`, native or paste) resets it.
2. **Backoff shape:** `wakeFailBase = 5 s`, `wakeFailCap = 5 min`,
   `backoff(n) = min(base * 2^n, cap)` for `n = consecutive failures`
   (5, 10, 20, 40, 80, 160, 300, 300 …). Deterministic (no jitter) so tests
   pin exact ticks. The existing success cooldown (`pasteRetry` 30 s /
   `wakeGap` 5 s for control) is unchanged — backoff gates retries after
   *failures*, cooldown gates re-wakes after *success*.
3. **In-memory, same maps.** Reuse `pasteAttempts` / `lastPasteAttemptAt`
   (rename semantics to wake-failures in comments only, no schema change, no
   migration). Restart resets to base — accepted: the 5-minute
   `agent.undeliverable` alert is DB-derived and survives restarts; backoff
   state does not need to.
4. **Per-session error isolation.** `tryPaste` capture/paste errors and
   `markWoken` errors are logged with the agent name and counted as failures;
   the loop continues to the next session. Only a global `Panes()` failure
   (no pane data at all) still aborts the tick (existing
   `TestWakeDuePropagatesAPanesError` pinned).
5. **Empty/mismatched pane command is transient.** Keep current behavior
   (log + count as failure) but now with exponential spacing instead of fixed
   30 s. No new native transport for muse/cursor in this change (probe still
   red); the unified retry makes paste-only reliable.

### Assumptions

- Tick stays 5 s (`WakeLoop`). Backoff values are multiples of it.
- `undeliverableAfter = 5 min` unchanged; backoff cap equals it so the alert
  and the longest retry spacing coincide.
- Muse idle regex and chunked paste are correct as-is (e2e-verified 1491 B
  intact); no adapter change in this fix.

## DB models

No schema change, no migration. Reuses `sessions.last_wake_at` (success) and
the in-memory `pasteAttempts` / `lastPasteAttemptAt` maps (failures).

## Model / API types

`internal/runtime/wake.go` only (plus tests):

```go
const wakeFailBase = 5 * time.Second
const wakeFailCap = 5 * time.Minute // == undeliverableAfter

// backoffForFailures returns base * 2^n capped at cap (n = consecutive failures).
func backoffForFailures(n int) time.Duration

// recordWakeFailure increments the per-session failure count (shared
// pasteAttempts map) and stamps lastPasteAttemptAt = now.
func (s *Store) recordWakeFailure(ctx context.Context, sessionID string, at time.Time)

// wakeBackoffDue reports whether the failure backoff has expired for r.
func (s *Store) wakeBackoffDue(r wakeRow, now time.Time) bool
```

`WakeDue` per-row flow (order preserved, only failure handling changes):

1. Alert (unchanged, DB-derived).
2. Success cooldown (unchanged).
3. **Failure backoff:** if `r.PasteAttempts > 0 && LastPasteAttemptAt != nil`
   and `now - LastPasteAttemptAt < backoffForFailures(PasteAttempts)` → skip.
   (Replaces the fixed `pasteRetry` check at old line 195.)
4. Native wake (unchanged attempt); on `err` log + `recordWakeFailure` and
   fall through to paste gates (do not `return`); on `delivered` → `markWoken`
   (on `markWoken` error: log + `recordWakeFailure`, `continue`, do not return).
5. Paste gates (delays unchanged); `tryPaste` never returns an error to the
   caller — it logs per-session failures, records success via `markWoken` or
   failure via `recordWakeFailure`, and always returns nil. `WakeDue` ignores
   its (nil) error and continues.

`tryPaste` signature stays `(ctx, ad, r, notice) error` returning nil always
(keeps call sites stable); its former error returns become
log + `recordWakeFailure`.

## Screens

None. No UI, no notification copy change.

## All user-facing copy

None. Log lines keep their text (`wake: pane command %q … skipping idle paste`
gains `fail=%d backoff=%s` suffix; `wake: native wake for %s: %v` gains
`fail=%d backoff=%s` suffix). No macOS banner change.

## File list

Changed:

- `internal/runtime/wake.go` (backoff consts + func, failure recording,
  `WakeDue` gating + error isolation, `tryPaste` always-nil)
- `internal/runtime/wake_test.go` (new tests; existing
  `TestWakeDuePropagatesAPanesError` kept as-is for the global Panes case)

Reused unchanged: `internal/adapter/*` (all kinds), `internal/spawn/tmux.go`
(chunked paste), `internal/runtime/inbox.go`, `internal/mcpserver/tools.go`,
`skills/swarm/SKILL.md`, `notifications` / `agent.undeliverable`.

Deleted: nothing.

## Verification

Command order (from worktree root):

1. `go test ./internal/runtime/ -run 'Wake|Undeliverable|Paste|Backoff' -v`
   (red first on new tests, then green)
2. `go test ./internal/runtime/ ./internal/adapter/ ./internal/spawn/`
3. `go vet ./...` + `gofmt -l .` (must be empty)
4. Full `go test ./...` (or at minimum `./internal/...`) before done.

Scenarios (each an automated test unless marked live):

- **Exponential spacing:** fail counts 0→1→2→3 gate next attempts at
  5/10/20/40 s; cap at 5 min (fail=10 still 5 min). Deterministic clock.
- **Reset on success:** 3 failures then `markWoken` → next failure backs off
  from 5 s again, not 40 s.
- **Per-session isolation:** session A `Capture` errors every tick while
  session B is idle with pending → B still pasted on its tick; `WakeDue`
  returns nil; A retried with backoff.
- **Native error falls through with backoff:** native `Wake` errors, paste
  gates not yet satisfied (delay) → failure recorded, no paste, next tick
  spaced by backoff; when delay expires and idle, paste attempted.
- **Empty pane command backs off:** `PaneCommand=""` repeatedly → attempts at
  5, 10, 20 … not every 30 s fixed; still exactly one `agent.undeliverable`
  at 5 min (live rule unchanged).
- **Live (manual, after deploy):** orchestrator + muse coder exchange over
  30 min shows zero manual `"check inbox"` nudges in muse transcripts and
  zero new `agent.undeliverable` for live sessions with only busy-pane delays
  under 5 min.

## Explicitly out of scope

- Native wake for muse/cursor (`session-message` ingress still closed;
  re-probe separately; no adapter change here).
- Persisting backoff across restarts (accepted reset; alert is DB-derived).
- Jitter, per-message (vs per-session) backoff, tuning base/cap.
- Muse idle-regex suggestion handling (no live capture proving the gap).
- `Codex.Wake` / `Agy.Wake` delivery-confirmation upgrades.
- Changing `Inbox()` content, `pasteDelay`, `pasteRetry`, `wakeGap`,
  `undeliverableAfter`, or any user-visible copy.

# Specification: Suppress Duplicate Undeliverable Notifications & Enforce Paste Retry Backoff

- **Date**: 2026-09-20
- **Status**: Draft
- **Target Repository**: `agent-swarm` (`internal/runtime`)

---

## 1. Context

In `agent-swarm`, the daemon runs `WakeDue` every 5 seconds to wake live sessions that have un-acknowledged messages with `wake_class = 'immediate'`. When an agent is busy in a turn, waiting on rate limits, or otherwise unpasteable, `tryPaste` fails and increments `PasteAttempts`.

Two distinct bugs cause severe notification spam:

1. **Unthrottled Failed Paste Retries**:
   - `pasteRetry` is defined as `30 * time.Second`.
   - In `WakeDue`:
     ```go
     if r.PasteAttempts > 0 && r.LastWakeAt != nil && s.Now().Sub(*r.LastWakeAt) < pasteRetry {
         continue
     }
     ```
   - `LastWakeAt` is only populated upon a *successful* wake via `markWoken`. When paste attempts fail, `LastWakeAt` remains `nil`.
   - Because `r.LastWakeAt != nil` is false, `WakeDue` never waits for `pasteRetry`.
   - Instead, `tryPaste` executes every 5 seconds. `maxPasteAttempts` (10) is exhausted in only 50 seconds instead of ~5 minutes.

2. **Continuous Notification Loop**:
   - Once `PasteAttempts >= maxPasteAttempts` (10), `tryPaste` calls `s.notify(ctx, nil, NotifyInput{Kind: "agent.undeliverable", ...})` on every single 5-second tick.
   - In `internal/notify/notify.go`, `dedupWindow` is 30 seconds.
   - Consequently, every 30 seconds, `notify.Service` permits a new notification row, persists it to SQLite, broadcasts `notification.created` over SSE, and triggers a macOS desktop alert.
   - An unresponsive agent generates ~120 notifications per hour.

---

## 2. Locked Decisions

1. **Single Notification per Undeliverable Message Batch**:
   - `agent.undeliverable` must fire at most **once** for a given batch of un-acknowledged messages.
   - A batch is defined by `r.AgentID`, `r.OldestMessageAt` (`MIN(created_at)` of un-acked immediate messages), and `r.StartedAt` (the session start time).
   - A helper `alreadyNotifiedUndeliverable(ctx, agentID, since)` checks:
     ```sql
     SELECT COUNT(*) FROM notifications
     WHERE agent_id = ? AND kind = 'agent.undeliverable' AND created_at >= ?
     ```
     where `since = max(r.OldestMessageAt, r.StartedAt)`.
   - If a notification has already been recorded since `since`, subsequent `agent.undeliverable` notifications are suppressed.

2. **Test Environment Compatibility**:
   - In unit tests where `Notifier` is a `fakeNotifier` that does not write to the SQLite `notifications` table, `tryPaste` records a synthetic row in `notifications` (matching the pattern used by `alreadyNotifiedStale` in `internal/runtime/reconcile.go`).

3. **Enforce 30s `pasteRetry` on Failed Attempts**:
   - Track `lastPasteAttemptAt` alongside `pasteAttempts` in `Store`'s in-memory bookkeeping:
     ```go
     type pasteAttemptState struct {
         count int
         at    time.Time
     }
     ```
   - In `WakeDue`, if `r.PasteAttempts > 0 && s.Now().Sub(r.LastPasteAttemptAt) < pasteRetry`, skip `tryPaste`.
   - This spaces paste retries by 30 seconds. 10 attempts span ~4.5 minutes, providing adequate buffer for long tool calls or turn completions.

---

## 3. DB Models & Migrations

No database migration required. The `notifications` table schema already contains `agent_id`, `kind`, and `created_at`.

---

## 4. Model / API Types & Signatures

In `internal/runtime/wake.go`:

```go
type wakeRow struct {
    SessionID, AgentID, AgentName, ItemKey, TmuxName, ProviderID string
    Kind                                                         AgentKind
    Pending                                                      int
    HasControl                                                   bool
    OldestMessageAt                                              time.Time
    LastSeenAt, LastWakeAt                                       *time.Time
    LastPasteAttemptAt                                           *time.Time
    StartedAt                                                    time.Time
    PaneCommand                                                  string
    PasteAttempts                                                int
    NativeTried                                                  bool
}

func (s *Store) alreadyNotifiedUndeliverable(ctx context.Context, agentID string, since time.Time) (bool, error)
func (s *Store) recordPasteAttempt(ctx context.Context, sessionID string, n int, at time.Time) error
func (s *Store) getPasteAttempts(sessionID string) (int, *time.Time)
```

---

## 5. User-Facing Copy

Unchanged. The existing §17.5 notification rule in `internal/notifyrules/notifyrules.go`:
- **Kind**: `agent.undeliverable`
- **Level**: `attention`
- **Title**: `Couldn't deliver messages`
- **Body**: `{name} hasn't picked up {N} message(s).`

---

## 6. File List

- `internal/runtime/wake.go`: Implement `alreadyNotifiedUndeliverable`, update `wakeCandidates`, update `recordPasteAttempt`/`getPasteAttempts`, update `WakeDue` retry check, update `tryPaste` notification suppression.
- `internal/runtime/wake_test.go`: Add `TestUndeliverableNotificationOnlyFiresOncePerBatch` and `TestPasteRetryIntervalEnforced`.

---

## 7. Verification Scenarios

1. **Notification Dedup Scenario**:
   - Seed an agent with an un-acked immediate message and an unpasteable pane (`zsh`).
   - Advance time through 15 cycles of 31 seconds (total > 450 seconds).
   - Verify that exactly **one** `agent.undeliverable` notification was raised, not 6+.
2. **Retry Interval Scenario**:
   - Seed an agent with an un-acked immediate message and an unpasteable pane.
   - Advance time by 5 seconds (1 daemon tick).
   - Verify `PasteAttempts` is 1.
   - Call `WakeDue` again after 5 seconds (< 30s `pasteRetry`).
   - Verify `PasteAttempts` remains 1 (attempt was skipped).
   - Advance time by 26 seconds (> 30s `pasteRetry`).
   - Call `WakeDue`.
   - Verify `PasteAttempts` increments to 2.
3. **Full Suite**:
   - Run `go test ./internal/...` and verify all tests pass.

---

## 8. Explicitly Out of Scope

- Modifying notification template copy or menubar display categories.
- Changing `maxPasteAttempts` (10) or `pasteRetry` (30s) duration constants.
- Changing native wake mechanisms for Claude channel bridge.

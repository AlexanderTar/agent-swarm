# Message Delivery Reliability Specification

Follow-up to `docs/specs/2026-09-21-claude-idle-wake-and-pause-reaper.md` (merged as e9e14fd, which fixed trailing whitespace in `claudeIdle`) and `docs/specs/2026-09-20-suppress-undeliverable-notification-spam.md` (merged as 6c5ac03, which stopped the repeat-notification flood). This spec covers what those two left broken. It does not redo either.

## Context

The user kept getting macOS notifications such as "Couldn't deliver messages: s3-fix-c-providers hasn't picked up 2 message(s)" and suspected the orchestrator-to-agent delivery path is unreliable. A read-only investigation (one researcher, one advisor pass) found two different things under that one symptom:

1. **A false-positive alert.** The undeliverable detector counts messages the agent has already read.
2. **A real delivery defect.** A live, idle Claude agent is never woken, so a message sits `pending` indefinitely.

### Evidence (all read-only, 2026-09-21)

| Finding | Evidence |
|---|---|
| Idle Claude 2.1.278 is not recognised as idle | The empty prompt renders as `\x1b[39m❯\u00a0\x1b[7m \x1b[0m` (NBSP, then a reverse-video cursor cell). `claudeIdle` (`internal/adapter/claude.go:109`) only allows an optional SGR-2 dim suggestion after `❯ `, so `idle()` returns false and `tryPaste` never pastes. Captured live with `tmux -L swarm capture-pane -p -e -J` from `s3-merge-d`, `s4-fix-a-insights` and `s4-fix-b-agent`. The three captures are saved under `internal/adapter/testdata/claude/pane-idle-cursor-*.txt`. |
| A message is stuck right now | `msg_01M31G7G6PT7G7DKKPH91EZK5H` for `s3-merge-d` has been `pending` since 09:09:58 (delivery_count 0). The pane has been idle at its prompt since 09:09:23. Still pending when this spec was written (09:21). The 09:14:50 `agent.undeliverable` for it was a true positive. |
| Same mechanism, longer | The orchestrator had 10 immediate messages unread for about 9 h (22:12 on 09-20 to 07:27 on 09-21), with one alert at 22:12:49 and then silence (the `alreadyNotifiedUndeliverable` guard). Causal link to the idle regex is plausible, not proven. |
| Latency is otherwise fine | Created → first sync, immediate class, post-redeploy (n=45): p50 22 s, p95 63 s, max 310 s. Pre-redeploy (n=64): p50 34 s, p95 about 9 h (the orchestrator batch above). No case found of a message read and then ignored. |
| The four alerts the user saw on 09-21 | `s3-fix-e-seed` 08:12:41 (1 msg), `s3-fix-a-v1` 08:36:10 (1), `s3-fix-d-integrations` 08:42:17 (3), `s3-fix-c-providers` 08:46:23 (2). `wakeCandidates` counts `state != 'acked'`, which includes `delivered` (read) messages. Verified from the Claude transcripts for `s3-fix-d-integrations`: it synced two messages at 08:32:44 and all three at 08:41:05, 72 s before the alert, and never acked until 09:04:24. |
| Disagreement, not settled by the fix set | For `s3-fix-c-providers` the delivery researcher found one message read and unacked plus one 59 s old; the stop-hook advisor read the DB as both messages unread until 08:46:40 (busy pane, 6 min). `messages.delivered_at` is overwritten on every redelivery, so it cannot show an earlier read; the transcripts are the authority, and the delivery advisor confirmed the four alerts as false positives from them. Nothing below depends on which reading is right. |
| Nudge noise | `s3-fix-d-integrations` received about 20 PostToolUse "[swarm] 3 new message(s)" nudges (08:36 to 09:03) for messages it had already read, plus one Stop-hook block at 09:04:17. The third message waited 310 s for its first sync (08:35:57 → 08:41:05); the agent had learned to ignore the nudges. |
| Flood already fixed | 1,548 `agent.undeliverable` rows on 19–20 Sep (`s1-lane-a` 955 at exactly 30 s spacing, `full-go-api-migration-orchestrator` 592). Commit 6c5ac03 (`alreadyNotifiedUndeliverable`, `wake.go:174-179`) stopped it; 4 rows on 09-21, one per agent. |
| Agents rarely ack | `s3-fix-d-integrations`: 0 of 10 checkpoints used `processed`. `s3-fix-c-providers`: 3 of 17. Read messages stay `delivered`, come back on every sync, and keep feeding the nudges. |
| `assignment` noise | The daemon-origin `assignment` duplicates the kickoff brief. `s3-merge` read it, did not ack it, and got one Stop-hook block 2.5 min later (`msg_01M31FM5QP…`, delivery_count 4, acked 09:01:59). `skills/swarm/SKILL.md` rule 1 already makes the agent write an `accepted` checkpoint immediately after its first sync. |

### Affected files and collisions

- `internal/adapter/claude.go`, `internal/adapter/claude_test.go`, `internal/adapter/testdata/claude/*`
- `internal/runtime/wake.go`, `internal/runtime/wake_test.go`
- `internal/runtime/inbox.go`, `internal/runtime/inbox_test.go`, `internal/runtime/checkpoint.go`, `internal/runtime/checkpoint_test.go`
- `internal/hook/handler.go`, `internal/hook/handler_test.go`
- `internal/mcpserver/tools.go` (sync response), `skills/swarm/SKILL.md`
- Collision warning: the untracked "Needs you" determinism spec being written in parallel touches `internal/runtime/reconcile.go`, `internal/runtime/requests.go` and `internal/hook/handler.go` (PreToolUse/PostToolUse/PermissionRequest cases). This spec touches only the `load` query and the `Stop`/`PostToolUse` notice lines in `handler.go`; rebase carefully if both land.
- Stale-cache blank notification icon (separate spec) is unrelated.

## Locked decisions

1. **Idle detection accepts the reverse-video cursor cell.** `claudeIdle` accepts `❯` + NBSP/space + optional SGR run + `\x1b[7m` + optional SGR run + one optional space/NBSP + optional SGR run. A typed draft (`❯ half typed`, cursor on the first character, cursor after text) must still fail.
2. **`agent.undeliverable` is defined on database facts**, not on paste-attempt counts: a live session (`sessions.state` in spawning/running/pause_requested/quiescing/stopping) with at least one message that is `state = 'pending'` AND `wake_class = 'immediate'` AND older than 5 minutes (`undeliverableAfter = 5 * time.Minute`). One notification per batch, deduplicated on the oldest pending message. The in-memory attempt counter no longer drives the alert.
3. **Only `state = 'pending'` counts as "new".** `wakeCandidates` (`Pending`, `HasControl`, oldest message) and `hook.load`'s pending count use `state = 'pending'`. A message the agent has synced is `delivered` and does not nudge, does not block Stop, and does not appear in the alert's N. The existing wording "N new message(s)" becomes accurate; `PendingNotice` is not changed.
4. **Paste cooldown after a successful wake.** After a wake (native or paste) the session is not woken again for `pasteRetry` (30 s; `wakeGap` = 5 s when a control message is pending) unless a pending immediate message newer than `last_wake_at` exists.
5. **Bounded redelivery, explicit ack stays.** `swarm_sync` returns a `delivered`, non-control message in full while `delivery_count < maxFullDeliveries` (3). At 3 deliveries it leaves `messages` and appears only in a new `unacked` list as `{msg_id, seq, kind, delivery_count}` (capped at 50) and `delivery_count` stops increasing. Control messages are always returned in full. `ack` and checkpoint `processed` are unchanged and still the only ways to reach `acked`.
6. **Auto-ack `assignment` on the `accepted` checkpoint.** Writing `swarm_checkpoint kind:"accepted"` acks every `kind = 'assignment'` message for that agent that is `delivered` (never `pending`, so a message the agent has not read is not acked). `assignment_update` is not touched.

### Assumptions (open questions, decided here)

- **Delivered is not lost.** After a sync the agent has the message text; a compaction between sync and action is covered by the existing `CompactionNotice` ("call swarm_sync") and by the message coming back on the next sync while `delivery_count < 3`. Nothing nudges for a delivered message any more; that is the accepted trade-off of decision 3.
- **Pause path change.** Today a `control` (pause) message the agent synced but did not act on keeps being re-pasted (`HasControl`, 5 s delay). After decision 3, `HasControl` and `OldestMessageAt` are pending-only, so a session whose only outstanding message is a delivered control message drops out of `wakeCandidates` and is no longer pasted. Enforcement moves entirely to the Stop hook (`Pausing() && !HasHandoff` → block, unchanged) and `TickPause`'s deadline reaper (unchanged). The parallel `2026-09-21-claude-idle-wake-and-pause-reaper` spec relies on the same two mechanisms; it is not affected, but anyone changing the pause deadline must know this re-paste no longer exists.
- **`owesNothing` (`reconcile.go:673`) and `PendingCount` (`inbox.go:416`) keep `state <> 'acked'`.** They answer "has this agent processed everything before it may finish?", which is a different question from "was it woken?". `PendingCount` has no non-test caller.
- **Deferred pending messages still count in N.** They are folded into a digest on the next sync, so the agent does have something waiting. Only immediate messages start the 5-minute alert clock.
- The 5-minute constant is kept as agreed; tuning it (for long tool runs) is out of scope.

## DB models

**No schema change and no migration.**

- `messages.delivery_count INTEGER NOT NULL DEFAULT 0` already exists (`internal/db/schema/0001_init.sql:229`) and is already incremented in `envelopes` (`inbox.go:320`).
- The new predicates (`state = 'pending'`, `state = 'delivered' AND kind <> 'control' AND delivery_count >= 3`) filter on `to_agent_id` first and are served by the existing `messages_inbox(to_agent_id, state, priority, seq)` index.
- `notifications` is unchanged; `agent.undeliverable` rows keep their current columns and `dedup_key` (`agent.undeliverable:<name>:`).

## Model / API types

`internal/adapter/claude.go` (replace line 109):

```go
// claudeIdle: the empty prompt, then optionally a dim "next action" suggestion
// (SGR 2) or the reverse-video cursor cell Claude 2.1.278 draws when idle with
// no suggestion (SGR 7 + one space, SGR runs around it). The cell must be a
// space, so a draft with the cursor on a typed character never matches.
claudeIdle = regexp.MustCompile("(?m)^(?:\x1b\\[[0-9;]*m)*\u276f[\u00a0 ]" +
	"(?:\x1b\\[2m.*|\x1b\\[7m(?:\x1b\\[[0-9;]*m)*[ \u00a0]?(?:\x1b\\[[0-9;]*m)*)?\\s*$")
```

`internal/runtime/wake.go`:

```go
const wakeGap = 5 * time.Second
const pasteDelay = 20 * time.Second
const controlPasteDelay = 5 * time.Second
const pasteRetry = 30 * time.Second
const undeliverableAfter = 5 * time.Minute // replaces maxPasteAttempts

type wakeRow struct { // existing fields kept; one added
	// ...
	OldestMessageAt time.Time // oldest PENDING immediate message
	NewestPendingAt time.Time // newest PENDING immediate message (new)
	// ...
}

// raiseUndeliverable raises agent.undeliverable once per batch: a notification
// for this agent created at or after the oldest pending message (or the
// session start, whichever is later) means it was already raised.
func (s *Store) raiseUndeliverable(ctx context.Context, r wakeRow) error
```

`internal/runtime/inbox.go`:

```go
const maxFullDeliveries = 3
const maxUnackedRefs = 50

// UnackedRef is a delivered message that has used up its full deliveries.
type UnackedRef struct {
	MsgID         string      `json:"msg_id"`
	Seq           int64       `json:"seq"`
	Kind          MessageKind `json:"kind"`
	DeliveryCount int         `json:"delivery_count"`
}

type SyncResult struct {
	Messages     []Envelope
	Unacked      []UnackedRef // new
	More         bool
	SessionState SessionState
}

// staleUnackedFor lists delivered non-control messages with delivery_count >=
// maxFullDeliveries, oldest first, at most maxUnackedRefs.
func (s *Store) staleUnackedFor(ctx context.Context, tx *sql.Tx, agentID string) ([]UnackedRef, error)
```

`unackedFor` gains `AND NOT (state = 'delivered' AND kind <> 'control' AND delivery_count >= ?)` bound to `maxFullDeliveries`, so stale messages neither consume `limit` slots nor get their `delivery_count` bumped.

`swarm_sync` result shape (`internal/mcpserver/tools.go`, `syncTool`):

```json
{
  "messages": [ { "v": 1, "msg_id": "msg_…", "kind": "answer", "payload": {} } ],
  "unacked":  [ { "msg_id": "msg_…", "seq": 41, "kind": "question", "delivery_count": 3 } ],
  "more": false,
  "session_state": "running"
}
```

`unacked` is always present (`[]` when empty), like `messages`.

`internal/hook/handler.go` line 216: `state <> 'acked'` becomes `state = 'pending'`.

`internal/runtime/checkpoint.go`, inside `case Accepted:` after the transition:

```go
if _, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'acked', acked_at = ?
	WHERE to_agent_id = ? AND kind = 'assignment' AND state = 'delivered'`,
	db.Millis(s.Now()), a.ID); err != nil {
	return err
}
```

`wakeCandidates` queries (only the predicates change):

```sql
(SELECT COUNT(*) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending'),
(SELECT COUNT(*) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.kind = 'control'),
(SELECT MIN(m.created_at) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.wake_class = 'immediate'),
(SELECT MAX(m.created_at) FROM messages m WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.wake_class = 'immediate')
```

`WakeDue`, per row, in this order:

1. Alert: `if s.Now().Sub(r.OldestMessageAt) >= undeliverableAfter { raiseUndeliverable }` (runs before any gate, so a busy or paste-gated session is still reported).
2. Cooldown: `cool := pasteRetry; if r.HasControl { cool = wakeGap }`; skip if `r.LastWakeAt != nil && now-*LastWakeAt < cool && !r.NewestPendingAt.After(*r.LastWakeAt)`. This replaces the existing 5-second `wakeGap` check.
3. Native wake, paste delays and `tryPaste` as today. `tryPaste` no longer raises the alert; it still records failed attempts, which now only space the retries (`pasteRetry`).

## Screens

No UI surface changes. The only user-visible text is what agents and the macOS banner already show.

Notification (unchanged copy, `internal/notifyrules/notifyrules.go:37`):

```
Couldn't deliver messages
{name} hasn't picked up {N} message(s).
```

It now fires only when N pending immediate messages have gone unsynced for 5 minutes on a live session.

Agent-visible text, before → after:

| Where | Before | After |
|---|---|---|
| PostToolUse nudge (`handler.go:477`) | `[swarm] 3 new message(s) for X (KEY). Call swarm_sync. …` repeated every 60 s while any message is delivered but unacked | Same text, sent only while a message is `pending`. Silent once the agent has synced. |
| Stop block reason (`handler.go:503`) | Same text, up to 3 blocks, also for read-but-unacked messages | Same text, only for `pending` messages. |
| Stop hook line in Claude Code | `Stop hook error: [swarm] …` | Unchanged. That label is Claude Code's rendering of `decision:"block"`; see Out of scope. |
| Idle paste (`text.go:17`) | `swarm: inbox (call swarm_sync)` never arrives for a cursor-style idle prompt | Arrives after the 20 s delay. |
| `swarm_sync` result | `messages`, `more`, `session_state` | Adds `unacked`. |

## All user-facing copy

New or changed strings (everything else is unchanged):

`skills/swarm/SKILL.md` rule 2, replace with:

> 2. When you see a `[swarm]` notice or the line `swarm: inbox (call swarm_sync)`, call `swarm_sync`. Handle messages in order. As soon as you have handled a message, acknowledge it: pass its `msg_id` in `ack` on your next `swarm_sync`, or in `processed` on your next checkpoint. An unacknowledged message comes back on every sync; after three deliveries it stops coming back in full and appears only in the `unacked` list (id and kind), so acknowledge it.

`swarm_sync` tool description (`tools.go:37`), replace with:

> Acknowledge handled messages (`ack`) and fetch the agent's inbox: assignments, questions, control notices and advice, newest-priority first. Messages delivered three times without an ack are listed in `unacked` by id and kind only.

No other copy: notification title/body, `PendingNotice`, `IdleToken` are unchanged.

## File list

Changed:
- `internal/adapter/claude.go` (regex)
- `internal/adapter/claude_test.go` (live-capture cases)
- `internal/runtime/wake.go` (predicates, `NewestPendingAt`, cooldown, `raiseUndeliverable`, drop `maxPasteAttempts`)
- `internal/runtime/wake_test.go` (see Verification; `TestUndeliverableAfterTenRetries` is rewritten to the 5-minute rule, not removed)
- `internal/runtime/inbox.go` (`UnackedRef`, `staleUnackedFor`, `unackedFor` predicate, `Sync`)
- `internal/runtime/inbox_test.go`
- `internal/runtime/checkpoint.go`, `internal/runtime/checkpoint_test.go`
- `internal/hook/handler.go` (line 216), `internal/hook/handler_test.go`
- `internal/mcpserver/tools.go`, and its test file if one asserts the sync response keys
- `skills/swarm/SKILL.md`

Added (already saved, read-only captures of live idle panes, trimmed to the input box and status line):
- `internal/adapter/testdata/claude/pane-idle-cursor-default-fg.txt` (`s3-merge-d`: `\x1b[39m❯\xa0\x1b[7m `)
- `internal/adapter/testdata/claude/pane-idle-cursor-grey-prompt.txt` (`s4-fix-a-insights`: `\x1b[38;5;246m❯\xa0\x1b[7m\x1b[39m `)
- `internal/adapter/testdata/claude/pane-idle-cursor-reset-fg.txt` (`s4-fix-b-agent`, same prompt shape as the previous one; kept as a second agent's capture)

Reused unchanged: `alreadyNotifiedUndeliverable`, `markWoken`, `tryPaste`'s paste path, `notifyrules`, `internal/notify`, `reconcile.go` (including its own `no_recipient` detector, which already filters `state = 'pending'`).

Deleted: `maxPasteAttempts` and the alert block inside `tryPaste`.

## Verification

Command order:

1. `go test ./internal/adapter/... -run 'TestClaudeIdle'` (red first, then green)
2. `go test ./internal/runtime/... -run 'Wake|Undeliverable|Sync|Accepted|Paste'`
3. `go test ./internal/hook/... -run 'Stop|PostToolUse'`
4. `go test ./...` and `go vet ./...`
5. Redeploy the daemon (`make` per `swarm-local-deploy`; Node 22 requirement applies to the web build only), then the live scenarios below.

Scenarios (each is an automated test unless marked live):

- **Idle cursor prompt gets pasted.** `pane-idle-cursor-*.txt` all report `Idle == true`; `pane-input-nonempty.txt`, `pane-busy.txt`, a draft `❯ half typed`, a draft with the cursor on its first character, and a draft with the cursor after the text all stay `false`. Runtime: with such a capture and a `pending` immediate message older than 20 s, `WakeDue` pastes `IdleToken` once.
- **Busy agent with a read-but-unacked message: no alert, no nudge.** Enqueue, `Sync` without ack, pane `zsh`, advance 11 × 31 s, `WakeDue` each time: zero `agent.undeliverable`; the PostToolUse hook returns no context; Stop is allowed.
- **Never-synced pending for over 5 minutes: exactly one alert.** No alert at 299 s; one at 301 s; still one after 15 × 31 s. `N` equals the pending count. A second, newer pending message does not raise a second alert; acking the first batch and enqueueing a new one after the alert time raises a second alert once it is 5 minutes old.
- **Attempt counter no longer matters.** 9 failed paste attempts on message 1, ack it, send message 2: no alert until message 2 itself is 5 minutes old.
- **Daemon restart.** Clear `s.pasteAttempts` / `s.lastPasteAttemptAt` mid-run: the alert still fires exactly once (the state is in the database).
- **Paste cooldown.** After one successful paste, `WakeDue` at +6 s, +20 s and +29 s pastes nothing; at +31 s it pastes again; a new pending message created after the paste is woken within `wakeGap`. A control message uses the 5 s gap.
- **Bounded redelivery.** Sync 1–3 return the message in full (delivery_count 1, 2, 3); sync 4 returns it only in `unacked` and `delivery_count` stays 3; `ack` on sync 5 removes it. A `control` message is returned in full on every sync. With 25 stale unacked messages and one new pending message, the new one is still returned (no `limit` starvation).
- **Assignment auto-ack.** `Sync`, then `WriteCheckpoint{Kind: Accepted}`: the `assignment` row is `acked`. `Accepted` without a prior `Sync` leaves a `pending` assignment untouched. An `assignment_update` is not acked.
- **Live (manual, after redeploy):** the stuck message `msg_01M31G7G6PT7G7DKKPH91EZK5H` to `s3-merge-d` is pasted within about 25 s (or was already delivered by manual nudge); `SELECT state FROM messages WHERE id='msg_01M31G7G6PT7G7DKKPH91EZK5H'` becomes `delivered` then `acked`. Over the next hour no `agent.undeliverable` row is created for a message the agent had synced: `SELECT n.created_at, a.name FROM notifications n JOIN agents a ON a.id = n.agent_id WHERE n.kind='agent.undeliverable' AND n.created_at > <deploy time>` should be empty or list only agents with genuinely unsynced messages.

Immediate operational note: `s3-merge-d`'s message stays stuck until the regex fix is deployed or someone nudges the agent by hand. This spec changes no code by itself.

## Explicitly out of scope

- **Native wake once per batch** (`NativeTried` in `wake.go:79`). The channel wake on an idle Claude session is unproven: the orchestrator's wakes at 02:21 and 07:21 started no turn until the user typed at 07:27. Needs a controlled test before any change.
- **Changing the "Stop hook error" label** to `hookSpecificOutput.additionalContext`. Verdict: normal behaviour, not a bug. The Claude Code hooks documentation confirms `additionalContext` would keep the agent going the same way and would only change the transcript label; ship it only if the label confuses people.
- **Implicit ack on the next sync.** Rejected: it breaks after compaction and crashes and removes the orchestrator's only signal that a message was actually handled.
- **Tuning the 5-minute threshold** (a long tool run can exceed it while the agent is genuinely busy). Only the constant is introduced.
- **Persisting the paste-attempt counter.** Not needed once the alert is database-derived.
- `owesNothing`, `PendingCount`, the `no_recipient` detector, deferred-relay digests, and the "Needs you" queue.

## Implementation notes (2026-09-21, as built)
- All eight plan tasks landed as written; no schema change. `TestUndeliverableAfterTenRetries` was renamed `TestUndeliverableAfterFiveMinutesUnsynced` with unchanged assertions.
- Fresh git worktrees have no built board (`web/dist`), so `TestBoardServedAtRoot` fails there regardless of code; copy `web/dist` from the primary checkout before running the full Go suite.
- The installed skill copies (`~/.claude`, `~/.codex`, `~/.cursor`, `~/.gemini`) only refresh on `swarm install`, not `make install-daemon`; the 2026-09-21 deploy overwrote them directly after taking a backup.
- Live result on deploy: two messages sent right after the restart were delivered and acked within about a minute, with 0 new `agent.undeliverable` rows.

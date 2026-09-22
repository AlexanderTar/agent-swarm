# Detect and break a progress-checkpoint deadlock

## Context

Third follow-up in the TASK-107/s11-seams incident chain, after:
1. `fix/deferred-message-ack-escalation` (merged `bcb2ac3`) — every relay
   checkpoint now wakes its parent immediately instead of sitting in a
   deferred digest.
2. `fix/orchestrator-ack-escalation` (merged `0685836`) — a message the
   orchestrator never *acks* after 3 deliveries now escalates.

Both of those fixed delivery. Neither fixes what actually happened live
today: s11-seams wrote a `progress` checkpoint whose `next` field read
"contract_db_test's 8 remaining failing paths need an explicit scope
decision from the orchestrator", then ended its turn. The relay was
delivered and acked (both prior fixes worked as designed) — the
orchestrator's pane shows it read the checkpoint content verbatim. But
it chose to passively wait ("Waiting for s11-seams to send its completed
checkpoint") rather than resolve the embedded ask. s11-seams sat idle
67+ minutes with nothing telling it, or its orchestrator, that the wait
would never end on its own. This repeated twice in one hour on the same
session (a human manually typed "proceed" into s11-seams's pane at
~16:56 to unstick it the first time; the second time, at 17:16, was
still unresolved when the user reported it).

Root cause: `resolveAlive` (`internal/runtime/reconcile.go:576`)
computes `waiting := idle && owesNothing` and returns immediately when
`waiting` is true — "M6: a waiting session is never stale"
(`reconcile.go:616-618`). `owesNothing` only looks at *formal* signals
(unacked messages, open requests it raised). A child that ends its turn
right after a `progress` checkpoint — not `completed`/`blocked`/
`handoff`/`failed`, all of which already have defined handling — looks
identical to a healthy "done working, awaiting new assignment" session.
Nothing in the daemon ever notices; nothing in `skills/swarm-orchestrator/
SKILL.md` tells the orchestrator that acking a relay is not the same as
resolving what it says.

Affected repo: `agent-swarm`, worktree
`~/GitHub/agent-swarm--progress-deadlock-nudge`, branch
`fix/progress-checkpoint-deadlock-nudge`, based on current `main`
(includes both prior fixes above). No other worktree touches
`internal/runtime/reconcile.go`, `internal/notifyrules/notifyrules.go`,
or `skills/swarm-orchestrator/SKILL.md` at time of writing (verify with
`git worktree list` before starting, in case a new one appeared).

## Locked decisions (user-approved, do not reopen)

- **Trigger condition**, all of:
  - session is `waiting` (idle + owes nothing, exactly today's existing
    computation — unchanged)
  - `ParentAgentID != ""` (a top-level orchestrator has nobody to relay
    to; matches `notifyNoAck`/`OnDepUnblocked`'s own guard)
  - the agent's most recent checkpoint this attempt (across any item —
    do NOT scope to `a.item_id`; an assignment can cover several tasks,
    and the deadlock can happen on any of them) has `kind = 'progress'`
    — not `accepted`/`blocked`/`handoff`/`completed`/`failed`/
    `integrated`. Only `progress` is in scope; the others either
    already have their own handling (`blocked` → request/notify,
    `handoff`/`failed`/`completed` are legitimate turn-enders) or
    aren't the observed failure mode. A session with **no** checkpoint
    at all yet is `notifyNoAck`'s territory, not this one — skip if
    there is no checkpoint.
  - elapsed time since that checkpoint's `created_at` is **≥ 5
    minutes** (user-chosen; short enough to catch a live stall
    quickly, long enough that it won't fire on the normal ~5-8.5 min
    checkpoint cadence the wake-immediately fix's own comment
    documents — `inbox.go:20-24`).
- **Action**: relay once to the nearest live ancestor (`s.
  nearestLiveAncestor`, same as `notifyNoAck`/`OnDepUnblocked`) — no
  daemon-authored notify to the human this time (unlike
  `agent.message_unacked`): the orchestrator is the one meant to act,
  and it's already live and watching. New relay `event`:
  `"progress_deadlock"`. Payload carries enough for the orchestrator to
  act without a `swarm_read`: `{event, agent, item, checkpoint:
  {summary, next}}` — same shape `WriteCheckpoint`'s own relay-to-parent
  already builds (`checkpoint.go:426-439`), reuse it, don't invent a
  new shape.
- **Dedup**: keyed to the specific checkpoint, not the session or a
  time window — reuse `alreadyRelayedForMessage`'s exact query shape
  (`reconcile.go:923-928`, `SELECT COUNT(*) FROM messages WHERE kind =
  'relay' AND reply_to = ?`), passing the *checkpoint's own id* as the
  parameter and setting the new relay message's `ReplyTo` to that same
  id. This is deliberately unlike `alreadyRelayed`'s payload-`LIKE`
  scan (`reconcile.go:261-267`) — a checkpoint id is a precise,
  natural key; no scan needed. Confirm whether renaming
  `alreadyRelayedForMessage` to something id-agnostic (it already reads
  generically — the "Message" in its name is about the record shape,
  not necessarily its only caller) is cleaner than adding a
  near-duplicate function, given it will now have two very different
  callers.
- Fires **at most once per checkpoint**: once relayed, a later
  `resolveAlive` tick for the same still-waiting session with the same
  "last checkpoint" must not relay again. If the agent later writes
  *another* `progress` checkpoint and stalls again, that's a new
  checkpoint id and gets its own relay.

## Where this hooks in

`liveSessionRows` (`reconcile.go:73-`) needs the agent's last checkpoint
this attempt (id, kind, created_at) alongside the existing
`CompletedCheckpointAt` correlated subquery. Recommended shape (verify
against SQLite's actual query planner behavior on this table before
committing to it — this is illustrative):

```sql
FROM sessions ses
JOIN agents a ON a.id = ses.agent_id
JOIN items i ON i.id = a.item_id
LEFT JOIN checkpoints lc ON lc.id = (
    SELECT c.id FROM checkpoints c
    WHERE c.agent_id = a.id AND c.attempt = ses.attempt
    ORDER BY c.created_at DESC, c.rowid DESC LIMIT 1
)
```
then add `lc.id`, `lc.kind`, `lc.created_at` to the outer `SELECT` list
(nullable — a fresh session with zero checkpoints has no `lc` row) and
three new fields on `liveRow`: `LastCheckpointID, LastCheckpointKind
string`, `LastCheckpointAt *time.Time` (mirror how `LastSeenAt`/
`CompletedCheckpointAt` are already scanned as nullable a few lines
below). Read the existing `CompletedCheckpointAt` subquery and its scan
code completely before changing this — match its null-handling exactly
rather than inventing a different convention for the new columns.

`resolveAlive` (`reconcile.go:576-`), the `if waiting { ... }` block at
line 616-618 currently just returns. Change to check the new condition
first:

```go
if waiting {
    if err := s.checkProgressDeadlock(ctx, r); err != nil {
        return err
    }
    return nil // M6: a waiting session is never stale
}
```

New function, sibling to `notifyNoAck`:

```go
// checkProgressDeadlock relays once to the nearest live ancestor when a
// child has gone idle-and-owes-nothing right after a progress checkpoint
// -- the daemon-visible signature of a child waiting on an answer it
// never formally asked for (a "next" note aimed at the orchestrator,
// not a swarm_send question or a blocked checkpoint). See docs/specs/
// 2026-09-22-progress-checkpoint-deadlock-nudge.md.
func (s *Store) checkProgressDeadlock(ctx context.Context, r liveRow) error {
    if r.ParentAgentID == "" || r.LastCheckpointID == "" || r.LastCheckpointKind != string(Progress) {
        return nil
    }
    if s.Now().Sub(*r.LastCheckpointAt) < progressDeadlockTimeout {
        return nil
    }
    already, err := s.alreadyRelayedForMessage(ctx, r.LastCheckpointID) // rename? see spec
    if err != nil || already {
        return err
    }
    return s.tx(ctx, func(tx *sql.Tx) error {
        // fetch the checkpoint's summary/next — see checkpoint.go:426-439
        // for the exact payload shape to reuse
        ancestor, ok, err := s.nearestLiveAncestor(ctx, r.AgentID)
        if err != nil || !ok {
            return err
        }
        payload, err := json.Marshal(map[string]any{"event": "progress_deadlock",
            "agent": r.AgentName, "item": r.ItemKey,
            "checkpoint": map[string]any{"summary": summary, "next": next}})
        if err != nil {
            return err
        }
        _, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon",
            ToAgentID: ancestor.ID, RootItemID: r.RootItemID, ItemID: r.ItemID,
            ReplyTo: r.LastCheckpointID, Payload: payload})
        return err
    })
}

const progressDeadlockTimeout = 5 * time.Minute
```

This is illustrative, not literal:

- `checkpoints.summary`/`next_json` for `r.LastCheckpointID` need a
  fetch — either add columns to the `liveSessionRows` join above (most
  consistent with the rest of this function's data already coming from
  that one query) or a small follow-up lookup. Prefer extending the
  join if it doesn't make the query unreadable; read how
  `WriteCheckpoint`'s own relay builds its payload
  (`checkpoint.go:426-439`) for the exact field names/JSON shape to
  match (`next` is `[]string`, already `jsonArray`-encoded — decide
  whether to decode-and-re-encode or pass the raw stored JSON through).
- `CheckpointKind` is a defined string type (`model.go:85-91`) — decide
  whether `LastCheckpointKind` on `liveRow` should be typed
  `CheckpointKind` instead of `string` for a cleaner comparison
  (`r.LastCheckpointKind != Progress`), matching how the rest of the
  package types checkpoint kinds elsewhere, rather than stringly-typing
  this one field.
- Whether `checkProgressDeadlock` needs its own transaction or can run
  outside one (it only reads before its single write) — `notifyNoAck`
  wraps its entire body in `s.tx`; match that precedent unless there's
  a concrete reason not to.
- Confirm `alreadyRelayedForMessage`'s `reply_to = ?` query has no
  hidden assumption tying it to `msg_`-prefixed ids (read it fully —
  it's already shown above, it doesn't, but verify no caller elsewhere
  relies on that).

## Skill update

`skills/swarm-orchestrator/SKILL.md` — add a bullet immediately after
the existing `no_ack`/`dependency_added` rows (~line 19-20), same
voice and format:

> A relay with `event: "progress_deadlock"` means a child ended its
> turn after a `progress` checkpoint and has been idle 5+ minutes with
> nothing formally open — acking this relay is not enough, and neither
> is re-reading it and continuing to wait. Read the checkpoint's
> `summary`/`next` in the payload and resolve it directly with
> `swarm_send`: answer whatever it's asking, or if it reads as
> finished, tell it plainly to write its `completed` checkpoint now.
> Don't wait for a `completed` checkpoint that will never come without
> this.

Keep it to one bullet, matching the existing terseness of that section
— don't add a worked example unless the existing `dependency_added`
bullet's own one-line example convention is clearly expected (it has a
short `swarm_send` example inline; match that same inline-example
style, don't add a separate paragraph).

## Verification

New tests in `internal/runtime/reconcile_test.go`, alongside
`TestNoAckAfterTwoMinutesWithNoCheckpointNotifiesAndRelaysOnce` (read it
completely first — same `clockStore`/`worker`/`panes`/`at.Advance`/
`notified` fixtures apply here, minus the notify assertions since this
event raises no notification):

1. **Fires after 5 min idle following a `progress` checkpoint with a
   parent**: `worker(t, s)`, write a `progress` checkpoint, leave the
   pane capture empty/idle (default, per
   `TestWaitingIsSetAndClearedAndNeverStale`), advance 4:59 → no relay;
   advance past 5:00 → exactly one `relay` message lands in the
   orchestrator's inbox with `payload.event == "progress_deadlock"`
   and `reply_to` equal to the checkpoint's id.
2. **Never fires for `accepted`/`blocked`/`handoff`/`completed`/
   `failed`/`integrated`** — one sub-case per kind (table-driven is
   fine), same idle+timeout setup, confirm zero `progress_deadlock`
   relays.
3. **Never fires with no checkpoint yet** — a session that's never
   checkpointed at all stays `notifyNoAck`'s territory; confirm no
   `progress_deadlock` relay even past 5 minutes idle.
4. **Never fires for a top-level orchestrator** (no `ParentAgentID`) —
   confirm no relay, no error.
5. **Fires at most once per checkpoint**: after the first relay, further
   `Reconcile` ticks on the same still-waiting session must not add a
   second `progress_deadlock` relay for the same checkpoint id.
6. **A new `progress` checkpoint after the first stall gets its own
   relay** if it also stalls 5+ minutes — confirms the dedup is keyed
   to the checkpoint, not the session.
7. **Payload carries the checkpoint's actual summary/next** — assert
   the relayed payload's `checkpoint.summary`/`checkpoint.next` match
   what was written, not empty/placeholder values.
8. Full `go test ./internal/runtime/...` green — no regression to
   `TestWaitingIsSetAndClearedAndNeverStale`,
   `TestOrchestratorWithALiveChildIsNeverWaiting`,
   `TestNoAckAfterTwoMinutesWithNoCheckpointNotifiesAndRelaysOnce`, or
   any other `resolveAlive`/`liveSessionRows` test.
9. `go build ./... && go vet ./...` clean; `gofmt -l` empty on every
   touched file.
10. Manual: read `skills/swarm-orchestrator/SKILL.md`'s diff once more
    after editing — confirm it reads naturally alongside the existing
    `no_ack`/`dependency_added` bullets, no duplicated guidance.

## File list

- `internal/runtime/reconcile.go` — changed (`liveSessionRows`,
  `liveRow`, `resolveAlive`, new `checkProgressDeadlock`,
  `progressDeadlockTimeout`; possibly renaming/generalizing
  `alreadyRelayedForMessage`).
- `internal/runtime/reconcile_test.go` — changed, new tests added.
- `skills/swarm-orchestrator/SKILL.md` — changed (one new bullet).
- `internal/install/skills/swarm-orchestrator/SKILL.md` — changed,
  identically to `skills/swarm-orchestrator/SKILL.md`. Verified: this is
  a hand-duplicated copy (`go:embed` in `internal/install/skills.go`
  embeds this exact path, it is not generated from the other file at
  build time), currently byte-identical to the source skill — apply the
  same one-bullet edit to both so they stay identical.
- `docs/specs/2026-09-22-progress-checkpoint-deadlock-nudge.md`,
  `docs/plans/2026-09-22-progress-checkpoint-deadlock-nudge.md` —
  committed alongside the fix.

## Explicitly out of scope

- Any daemon-authored notify/push to the human for this event (unlike
  `agent.message_unacked`) — the orchestrator is live and expected to
  act; this is a relay-only fix.
- Extending the deadlock check to `accepted` or any other non-terminal
  kind — `progress` is the observed and specified failure mode.
- Changing `staleAfter`, `ackTimeout`, or any other existing timing
  constant.
- A daemon-side auto-nudge directly to the *child* (bypassing the
  orchestrator) — the locked design routes through the orchestrator,
  matching every existing relay-based recovery path in this file.
- Repairing the live s11-seams session itself — handled manually
  (typing directly into its pane), independent of this fix landing.
- Any change to `WriteCheckpoint`'s own parent-relay
  (`checkpoint.go:426-439`) — that relay already fires correctly per
  checkpoint; this spec adds a second, distinct relay only when the
  first one goes unanswered for 5+ minutes.

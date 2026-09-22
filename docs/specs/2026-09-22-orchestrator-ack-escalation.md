# Escalate a message the orchestrator never acks

## Context

Follow-up to the TASK-107/s11-seams incident, after
`fix/deferred-message-ack-escalation` (merged as `bcb2ac3`) fixed why the
orchestrator wasn't woken at all — every relay checkpoint now wakes
immediately. Remaining gap the user flagged: even once delivered, nothing
forces the recipient to actually process (explicitly ack) a message. A
message can be redelivered up to `maxFullDeliveries` (3,
`internal/runtime/inbox.go:183`) times, then silently demoted to a bare
`{id, kind}` ref in `SyncResult.Unacked` (`staleUnackedFor`,
`inbox.go:253`) — zero daemon-level escalation. This is the unhandled
mirror of the already-solved child→parent direction: `notifyNoAck`
(`internal/runtime/reconcile.go:269`) raises a real notification AND
relays to the parent when a *child* goes silent on its own first
checkpoint. Nothing plays that role when the *parent* (orchestrator) goes
silent on a message it's already seen three times.

Affected repo: `agent-swarm`, worktree
`~/GitHub/agent-swarm--orchestrator-ack-escalation`, branch
`fix/orchestrator-ack-escalation`, based on updated `main` (includes the
wake-immediately fix). No other worktree touches `internal/runtime/inbox.go`
or `internal/notifyrules/notifyrules.go` at time of writing.

## Locked decisions (user-approved, do not reopen)

- **Trigger**: the instant a message's `delivery_count` crosses
  `maxFullDeliveries` (goes from 2 to 3) while still unacked. Reuse the
  existing threshold — no new timeout constant, no new background scan,
  no new reconcile step.
- **Action**: both
  1. raise a daemon notification (new kind `agent.message_unacked`,
     registered in `internal/notifyrules/notifyrules.go`'s `Rules` map,
     same mechanism `raiseUndeliverable`/`notifyNoAck` already use via
     `s.notify`), and
  2. relay to the nearest live ancestor of the non-acking agent (via the
     existing `s.nearestLiveAncestor`, same pattern as
     `notifyNoAck`/`OnDepUnblocked` in `reconcile.go`). For a top-level
     orchestrator with no ancestor, this is a silent no-op — only (1)
     fires.
- Fires **exactly once per message** — a structural property of where
  it's hooked in, not something requiring its own dedup table (see
  "Where this hooks in").

## Where this hooks in

`internal/runtime/inbox.go`'s `envelopes()` (~line 350) is what increments
`delivery_count` and sets `delivered_at`, called once per `Sync()`
(`inbox.go:201`) for every message it hands back. `Sync()` already applies
`ack` *before* computing `unackedFor`'s row set (`inbox.go:215-231`), so a
message acked in the very same call that would have hit its 3rd delivery
is excluded from `rows` entirely — `envelopes()` never touches it, and the
escalation never fires for it. That's why no explicit dedup/suppression
logic is needed: a message's `delivery_count` can only cross
`maxFullDeliveries` once, because `unackedFor`'s own WHERE clause
(`inbox.go:285`, `NOT (state = 'delivered' AND kind <> 'control' AND
delivery_count >= maxFullDeliveries)`) excludes it from ever being
re-selected — and therefore re-incremented — once it's there.

Current signature: `func (s *Store) envelopes(ctx context.Context, tx
*sql.Tx, to Agent, rows []Message) ([]Envelope, error)`. `to` is the
recipient `Agent` (the orchestrator syncing its own inbox) — it has `.ID`,
`.Name`, `.RootItemID` (see `internal/runtime/model.go:94-110`'s `Agent`
struct; there is no `.ItemID` relevant here, use the *message's* own
`m.ItemID`/`m.RootItemID` for the item key, exactly as the existing
envelope-building code just below already does via `s.itemKey`).

Add, inside the existing `for _, m := range rows` loop in `envelopes()`,
right after the `UPDATE ... delivery_count = delivery_count + 1 ...` and
before (or after — order doesn't matter, both are in the same tx) building
`e`:

```go
var newCount int
if err := tx.QueryRowContext(ctx, `SELECT delivery_count FROM messages WHERE id = ?`, m.ID).Scan(&newCount); err != nil {
    return nil, err
}
if m.Kind != "control" && newCount == maxFullDeliveries {
    itemKey, _ := s.itemKey(ctx, tx, m.ItemID) // best-effort; empty is fine, matches e.Item's own handling just below
    if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.message_unacked",
        AgentName: to.Name, ItemKey: itemKey,
        Args: map[string]string{"name": to.Name, "KEY": itemKey, "kind": string(m.Kind)}}); err != nil {
        return nil, err
    }
    if ancestor, ok, err := s.nearestLiveAncestor(ctx, to.ID); err != nil {
        return nil, err
    } else if ok {
        payload, err := json.Marshal(map[string]any{"event": "message_unacked", "agent": to.Name, "message_kind": string(m.Kind)})
        if err != nil {
            return nil, err
        }
        if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon",
            ToAgentID: ancestor.ID, RootItemID: to.RootItemID, ItemID: m.ItemID, Payload: payload}); err != nil {
            return nil, err
        }
    }
}
```

This is illustrative, not literal — before writing it for real:

- Read `notifyNoAck` and `OnDepUnblocked` (`reconcile.go`) completely and
  match their exact call shapes (they're the two closest precedents in
  this codebase for "notify + relay to whoever's above the silent
  party").
- Confirm whether re-querying `delivery_count` with a fresh `SELECT` after
  the `UPDATE` is the right move, or whether it's cheaper/cleaner to
  compute `newCount := m.DeliveryCount + 1` directly from the `Message`
  struct already in hand (check `unackedFor`'s SELECT — it already scans
  `delivery_count` into `m.DeliveryCount`, so this is almost certainly
  available without a second query; prefer that if so, it's simpler and
  avoids an extra read in the same transaction).
- `control`-kind messages are explicitly exempt from `maxFullDeliveries`
  capping everywhere else in this file (see `unackedFor`'s and
  `staleUnackedFor`'s own `kind <> 'control'` clauses) — this escalation
  must respect the same exemption, or it will fire on messages the rest of
  the system deliberately never considers "stale."
- `s.itemKey` can fail (returns an error) for a message with no
  `ItemID` (root-item-only messages, or the daemon's own synthetic
  relays) — look at how `envelopes()` already handles this for `e.Item`
  just a few lines below (it only sets the field on success and ignores
  the error) and match that same best-effort handling, don't propagate
  the error and abort the whole `Sync` call over a cosmetic item-key
  lookup failure.

## Notify registration

Add to `internal/notifyrules/notifyrules.go`'s `Rules` map, alongside the
other `agent.*` attention-level rules:

```go
"agent.message_unacked": {"attention", "Message unacknowledged", "{name} hasn't acknowledged a {kind} message on {KEY} after 3 deliveries.", "swarm.agent"},
```

Match the existing map's alignment/formatting exactly (gofmt handles the
column alignment; just place it near `agent.no_ack`/`agent.undeliverable`
for readability, exact position doesn't matter functionally).

Check `internal/notify/notify_test.go` — it appears to independently
mirror parts of this `Rules` table for its own assertions (already seen
`"agent.no_ack"` and `"agent.undeliverable"` duplicated there in a test
map). If so, add the matching entry there too so that test's own
completeness check (if it has one — read the test to see what it actually
asserts) doesn't fail or silently under-cover the new kind.

## Verification

All new tests in `internal/runtime/inbox_test.go` (where `TestSyncLimitAndMore`
and friends already live), using the existing `fakeNotifier`/`notified`/
`notifiedCount` helpers from `wake_test.go` (same package, already visible)
and the `worker(t, s)` parent+child fixture from `checkpoint_test.go` (used
by the sibling `fix/deferred-message-ack-escalation` branch's new test —
read that fixture's real shape, don't guess).

1. **Escalates at the 3rd delivery**: a message delivered exactly
   `maxFullDeliveries` times without ever being acked raises exactly one
   `agent.message_unacked` notification. Drive this by calling `Sync`
   `maxFullDeliveries` times in a row for the same recipient session,
   never passing the message's id in `ack`.
2. **Relays to a live ancestor when one exists**: same scenario, recipient
   has a live parent above it (e.g. a sub-orchestrator under a top
   orchestrator) — confirm exactly one relay message lands in that
   ancestor's inbox with `payload.event == "message_unacked"`.
3. **No relay for a top-level orchestrator**: same scenario, recipient has
   no `ParentAgentID` — confirm no relay is enqueued (no error), and the
   notification still fires.
4. **Acking before the 3rd delivery suppresses it entirely**: ack the
   message on/before its 2nd delivery — no notification, no relay, ever.
5. **`control`-kind messages never trigger this** — mirror the existing
   `kind <> 'control'` exemption; a control message delivered 3+ times
   must not raise `agent.message_unacked`.
6. **Fires exactly once**: after the escalating 3rd delivery, further
   `Sync` calls for the same still-unacked message (now surfaced only via
   `SyncResult.Unacked`, per existing `staleUnackedFor` behavior) must not
   raise a second notification or a second relay.
7. Full `go test ./internal/runtime/...` green — no regression to
   `TestUndeliverableNotificationOnlyFiresOncePerBatch`,
   `TestReadButUnackedMessageIsNeverUndeliverable`, or any existing
   `Sync`/`staleUnackedFor`/`unackedFor` test.
8. `go build ./... && go vet ./...` clean; `gofmt -l` empty on every
   touched file.

## File list

- `internal/runtime/inbox.go` — changed (`envelopes()`).
- `internal/notifyrules/notifyrules.go` — changed (new `Rules` entry).
- `internal/notify/notify_test.go` — changed only if it independently
  mirrors the `Rules` table (verify, don't assume).
- `internal/runtime/inbox_test.go` — changed, new tests added.
- `docs/specs/2026-09-22-orchestrator-ack-escalation.md`,
  `docs/plans/2026-09-22-orchestrator-ack-escalation.md` — committed
  alongside the fix.

## Explicitly out of scope

- Any change to `maxFullDeliveries`'s value (stays 3).
- Any change to `staleUnackedFor`/`SyncResult.Unacked` — that passive
  signal stays as a courtesy to the caller; this adds an *active*
  escalation alongside it, doesn't replace it.
- Any further change to `WakeClassFor`/the wake-immediately fix already
  merged.
- Repairing the live TASK-107 message itself — it's already been acted on
  manually or will self-resolve once the orchestrator next syncs; no
  production DB repair script needed for this fix.

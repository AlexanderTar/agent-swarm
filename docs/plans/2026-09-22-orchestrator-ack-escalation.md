# Plan: escalate a message the orchestrator never acks

Spec: `docs/specs/2026-09-22-orchestrator-ack-escalation.md`
Worktree: `~/GitHub/agent-swarm--orchestrator-ack-escalation`
Branch: `fix/orchestrator-ack-escalation`

## Task 0 — read before writing anything

- `internal/runtime/reconcile.go`: `notifyNoAck` (~line 269) and
  `OnDepUnblocked` (~line 295-380, especially the `nearestLiveAncestor`
  call and the relay-enqueue right after it). These are the two closest
  precedents for "notify + relay to whoever's above a silent party" in
  this codebase — match their shape, don't invent a new one.
- `internal/runtime/inbox.go`: `Sync`, `unackedFor`, `staleUnackedFor`,
  `envelopes`, `enqueue` in full. Confirm exactly what fields `Message`
  carries after `unackedFor`'s scan (does it already have `DeliveryCount`
  populated pre-increment? the spec's illustrative code assumes yes —
  verify against the real struct/scan before deciding whether to
  re-`SELECT` or just add 1 to the in-memory value).
- `internal/notifyrules/notifyrules.go` in full (it's short) — the `Rules`
  map format.
- `internal/notify/notify_test.go` — check whether it independently
  mirrors `Rules` entries for its own test table; if so this plan's Task 3
  must add a matching entry there too.
- `internal/runtime/checkpoint_test.go`'s `worker(t, s)` fixture (the
  parent+child spawn helper used by the sibling
  `fix/deferred-message-ack-escalation` branch's
  `TestProgressCheckpointWakesImmediately` test, already merged into
  `main`) — this is the established two-agent fixture shape to reuse for
  Tasks 1-2's tests below, not a new one.

## Task 1 — failing test: escalation fires at the 3rd delivery

File: `internal/runtime/inbox_test.go`

Write a test (name it something like
`TestUnackedMessageEscalatesAtThirdDelivery`) that:

1. Spawns a parent + child via the `worker(t, s)` fixture (or whatever
   Task 0's reading shows is the real established pattern).
2. Gets the parent's session, clears its kickoff assignment (ack it) so
   only the message under test remains pending — same idiom
   `TestProgressCheckpointWakesImmediately` (in `wake_test.go`, already
   merged) already uses for exactly this reason.
3. Writes a checkpoint from the child (`WriteCheckpoint` with
   `Kind: Progress` or any non-control kind) so a real relay message lands
   in the parent's inbox — don't use the synthetic `enq()` test helper for
   this one, since the real path through `WriteCheckpoint` → `enqueue` is
   what actually sets `Kind: "relay"`, and the escalation logic keys off
   `m.Kind != "control"`, so using the real path is closer to the real
   incident and avoids a fixture mismatch.
4. Calls `s.Sync(ctx, parentSessionID, nil, 20)` three times in a row,
   never passing the message's id in the `ack` slice.
5. After the 3rd call, asserts via the existing `notifiedCount`/`notified`
   helpers (`wake_test.go`, same package) that exactly one
   `agent.message_unacked` notification was raised, and that it names the
   right agent/item.

Run: `go test ./internal/runtime/... -run TestUnackedMessageEscalatesAtThirdDelivery -v`
Confirm it fails (no such notification kind is raised yet — likely fails
either on the notify-count assertion, or your test may need to add the
`agent.message_unacked` case to `fakeNotifier`/`notified` helpers if those
use a fixed allowlist; check before assuming they need changes).

## Task 2 — implement the escalation in `envelopes()`

File: `internal/runtime/inbox.go`

Follow the spec's "Where this hooks in" section. Concretely:

1. Inside `envelopes()`'s loop, after the delivery-count UPDATE, determine
   the post-increment count (prefer computing it from `m.DeliveryCount + 1`
   if that field is already populated on `m` per Task 0's verification,
   else add a `SELECT delivery_count FROM messages WHERE id = ?` read-back
   — pick whichever Task 0 showed is correct, don't guess).
2. Gate on `m.Kind != "control" && newCount == maxFullDeliveries`.
3. Raise `NotifyInput{Kind: "agent.message_unacked", AgentName: to.Name,
   ItemKey: <best-effort item key from m.ItemID via s.itemKey, matching
   how e.Item is already computed just below in this same function>,
   Args: map[string]string{"name": to.Name, "KEY": itemKey, "kind":
   string(m.Kind)}}` via `s.notify(ctx, tx, ...)`.
4. Look up `s.nearestLiveAncestor(ctx, to.ID)`; if found, enqueue a
   `relay` message to it with a JSON payload
   `{"event": "message_unacked", "agent": to.Name, "message_kind":
   string(m.Kind)}`, `RootItemID: to.RootItemID`, `ItemID: m.ItemID`.
5. Propagate real errors (notify failure, enqueue failure,
   nearestLiveAncestor failure) — do not swallow them; only the item-key
   lookup is best-effort (matches the existing `e.Item` handling pattern
   right below in the same function).

Run: `go build ./internal/runtime/...` clean.

Run: `go test ./internal/runtime/... -run TestUnackedMessageEscalatesAtThirdDelivery -v`
Confirm it now passes.

## Task 3 — register the notify kind

File: `internal/notifyrules/notifyrules.go`

Add:
```go
"agent.message_unacked": {"attention", "Message unacknowledged", "{name} hasn't acknowledged a {kind} message on {KEY} after 3 deliveries.", "swarm.agent"},
```

If `internal/notify/notify_test.go` independently mirrors `Rules` entries
(check per Task 0), add the matching row there too so its own coverage
check doesn't regress.

Run: `go test ./internal/notify/... ./internal/notifyrules/...` clean.

## Task 4 — remaining coverage from the spec's Verification section

Add tests 2, 3, 4, 5, 6 from the spec's "Verification" list (ancestor
relay case, top-level-orchestrator no-relay case, ack-before-3rd-delivery
suppression, control-kind exemption, fires-exactly-once). Each is a
variant of Task 1's fixture — reuse it, don't rebuild from scratch for
each one. Name them descriptively, e.g.
`TestUnackedMessageEscalationRelaysToLiveAncestor`,
`TestUnackedMessageEscalationSkipsRelayForTopLevelOrchestrator`,
`TestAckingBeforeThirdDeliverySuppressesEscalation`,
`TestControlMessagesNeverEscalate`,
`TestUnackedEscalationFiresExactlyOnce`.

Run: `go test ./internal/runtime/... -run TestUnacked -v` and
`-run TestAckingBefore -v` and `-run TestControlMessages -v`, confirm all
pass.

## Task 5 — full verification

1. `go build ./... && go vet ./...`
2. `go test ./...` from the worktree root (if `web/dist` is empty and
   `TestBoardServedAtRoot` 503s, run `cd web && pnpm install
   --frozen-lockfile && pnpm build` first — known fresh-worktree
   environmental gap, not caused by this change).
3. `gofmt -l internal/runtime/inbox.go internal/runtime/inbox_test.go internal/notifyrules/notifyrules.go` (and `internal/notify/notify_test.go` if touched) — must be empty.

## Task 6 — commit

One commit, explicit paths (no `git add -A`):

```
git add internal/runtime/inbox.go internal/runtime/inbox_test.go internal/notifyrules/notifyrules.go [internal/notify/notify_test.go if touched] docs/specs/2026-09-22-orchestrator-ack-escalation.md docs/plans/2026-09-22-orchestrator-ack-escalation.md
git commit -m "fix(runtime): escalate a message the orchestrator never acks

A message redelivered maxFullDeliveries (3) times without being acked
was silently demoted to a bare ref in SyncResult.Unacked -- nothing
told anyone. This is the unhandled mirror of notifyNoAck's child->parent
direction: now the 3rd unacked delivery raises agent.message_unacked
and relays to the nearest live ancestor, same pattern notifyNoAck
already uses for a silent child."
```

## Report back

Summarize: files changed with a one-line diff summary each, full test
output (pass/fail counts) for `go test ./internal/runtime/...`,
`go test ./internal/notify/... ./internal/notifyrules/...`, and
`go test ./...`, the exact commit hash, and explicit callouts of anything
where Task 0's reading revealed the spec's illustrative code was wrong
(e.g. `Message.DeliveryCount` field availability, `notify_test.go`
mirroring behavior) and how you actually resolved it.

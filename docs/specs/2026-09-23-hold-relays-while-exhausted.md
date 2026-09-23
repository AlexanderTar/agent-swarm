# 2026-09-23 — Hold daemon relays while target kind is exhausted

## Context

Live incident: `full-go-api-migration-orchestrator-2` (kind `claude`, `~/.swarm/swarm.db`)
accumulated **309 daemon `relay` rows acked + 9 `digest`** while Claude's headline
`five_hour` meter sat at **100%** (reset `2026-09-24T00:00:00+01:00`).
Breakdown of the relay pileup by payload `event`: `accepted` 70, `no_ack` 62,
`completed` 53, `progress_deadlock` 10, `progress` 4, `failed` 1, `no_recipient` 1.
Every one of those was enqueued to a parent that could not read it, then rendered
into `InboxNotice` / delivered envelopes on wakeup — pure usage burn.

Root cause: no daemon send path checks the usage gate before enqueueing to an
agent. `usagegate.Gate.Exhausted` (`internal/usagegate/usagegate.go:93`) exists but
is only wired to the spawn/retry fallback (`Store.Usage`, `internal/runtime/model.go:347`).
These paths enqueue unconditionally:

- checkpoint relay to parent — `internal/runtime/checkpoint.go:586`
- `notifyNoAck` relay — `internal/runtime/reconcile.go:318`
- `checkProgressDeadlock` relay — `internal/runtime/reconcile.go:373`
- `escalateUnacked` relay — `internal/runtime/inbox.go:408`
- `OnDepUnblocked` / undelivered-escalation relays — `internal/runtime/reconcile.go:459,571,622,1240`
- preflight-failed relay — `internal/runtime/agents.go:1127`
- `foldDigest` output + `DeliverAdvice` — `internal/runtime/inbox.go:343`, `agents.go:1812`
- `WakeDue` native wake + paste every 5 s — `internal/runtime/wake.go:121`
  (waking a quota-dead session is pointless and feeds the `undeliverableAfter` escalations)

Affected repo: `agent-swarm`. Worktree `../agent-swarm--hold-relays-exhausted`,
branch `feat/hold-relays-while-exhausted`. No collisions: no other branch touches
`wake.go`'s reset path, `inbox.go`'s enqueue sites, or adds a `0009` migration
(worktree list checked 2026-09-23; highest schema file is `0008_allow_muse_agent_kind.sql`).

## Locked decisions

- Option **B** (user-approved 2026-09-23): **hold + single digest on quota reset**,
  not drop. Rationale: `no_ack` / `progress_deadlock` signals only exist in relay
  payloads; dropping them leaves no record the child ever waited.
- "Exhausted" means exactly `Store.Usage.Exhausted(ctx, kind)` (headline meter ≥ 100%,
  fresh, no fetch error, reset not passed). No new threshold, no per-model scoping.
  `Store.Usage == nil` (tests, `SWARM_USAGE` unset) means never exhausted — fail open.
- Suppression applies to daemon `relay` / `digest` / `advice` only. Exempt, always
  enqueued: `control`, pause lifecycle, HITL/requests, `user_action`, `repos_confirmed`,
  agent-origin `swarm_send`.
- `WakeDue` skips both native wake and paste for sessions whose agent kind is
  exhausted. `WakeOnQuotaReset` is the single wakeup path and also flushes the digest.
- Assumption: one digest per agent per outage is sufficient; per-child detail beyond
  counts + one sample summary per event is recovered from the `checkpoints` table.

## DB models

Migration `internal/db/schema/0009_hold_relays_while_exhausted.sql` (plain `CREATE TABLE`,
no rebuild needed):

```sql
CREATE TABLE suppressed_relays (
  agent_id   TEXT NOT NULL REFERENCES agents(id),
  event      TEXT NOT NULL,
  count      INTEGER NOT NULL DEFAULT 0,
  first_at   INTEGER NOT NULL,
  last_at    INTEGER NOT NULL,
  sample_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (agent_id, event)
);
CREATE INDEX suppressed_relays_agent ON suppressed_relays(agent_id);
```

Semantics: one row per `(to_agent, event)` per outage window. `event` is the relay
payload's `event` field (`no_ack`, `progress_deadlock`, checkpoint kind string, …);
digest/advice suppressions use events `digest` / `advice`. `sample_json` is the first
suppressed payload, truncated to 500 bytes at write time. Rows are deleted when flushed.
No changes to `messages`, `agents`, `sessions`, `usage_snapshots`.

## Model / API types

No new exported types. One new unexported helper on `*runtime.Store`:

```go
// holdIfExhausted reports whether a daemon relay/digest/advice to toAgentID must
// be held: target kind confirmed exhausted via Store.Usage. When holding, it upserts
// suppressed_relays and returns true (caller skips enqueue). Nil Usage never holds.
func (s *Store) holdIfExhausted(ctx context.Context, tx *sql.Tx, toAgentID, event string, sample json.RawMessage) (bool, error)
```

The gate lives in one place — `enqueue` itself — not at each call site: when
`m.Origin == "daemon"`, `m.Kind` is `relay`/`digest`/`advice`, and the target's kind
is exhausted, `enqueue` holds (upserting `suppressed_relays` with the payload's
`event`, falling back to the kind string) and returns a zero `Message` with nil
error instead of inserting. No daemon-relay caller reads the returned ID (all `_`;
only agent-origin `Send` does, and it never matches the gate). Two paths bypass
the gate via `enqueueRaw` (the ungated insert body): `foldDigest`, whose deferred
rows predate the outage and whose digest compresses rather than adds load, and the
quota-reset flush itself. `alreadyRelayed` / `alreadyRelayedForCheckpoint` also
consult `suppressed_relays` so a held signal still counts as "already handled" for
this session/checkpoint and the 5 s tick stays quiet.

`WakeOnQuotaReset(ctx, kind, cutoff)` keeps its signature `(int, error)` and gains the
flush: before waking sessions, for every agent of `kind` with `suppressed_relays` rows,
enqueue one `digest` (`origin daemon`) with payload
`{"lines": ["<event> x<count>: <sample summary>"], "suppressed": true}`, delete the rows,
then proceed with the existing wake. No HTTP/MCP shape changes.

## Screens

No UI changes. Deliberately not on any screen: menubar, web dashboard, and MCP tool
shapes are untouched. The only user-visible effect is inbox content (one digest instead
of hundreds of relays) after a quota reset.

## All user-facing copy

Digest lines (exact format): `<event> x<count> while <kind> exhausted: <sample>`,
e.g. `no_ack x14 while claude exhausted: s1-lane-a on TASK-9`.
Flush wake notice stays exactly `[swarm] Quota reset window passed. Resuming.`
(a second notice is not added; the digest precedes it in the same batch).
No i18n keys, no notification kind changes, no empty-state copy.

## File list

Changed:
- `internal/runtime/inbox.go` — `holdIfExhausted` helper, central gate in `enqueue`,
  `enqueueRaw` bypass used by `foldDigest`
- `internal/runtime/wake.go` — `WakeDue` skip when exhausted; `WakeOnQuotaReset` flush + wake
- `internal/runtime/reconcile.go` — `alreadyRelayed` / `alreadyRelayedForCheckpoint`
  also consult `suppressed_relays` (no per-site gates: `enqueue` covers notifyNoAck,
  progressDeadlock, OnDepUnblocked, interrupted/crashed/no_recipient relays)
- `internal/db/schema/0009_hold_relays_while_exhausted.sql` — new table
- Tests beside each: `inbox_test.go`, `wake_test.go`, `reconcile_test.go`,
  `checkpoint` coverage via existing checkpoint tests, `agents_test.go`, `db_test.go` migration check

Reused unchanged: `internal/usagegate/usagegate.go` (no threshold change),
`cmd/swarm/daemon.go` (`quotaResetLoop` already calls `WakeOnQuotaReset`; no change),
`internal/usage/*`, web/menubar, skills.

Deleted: nothing.

## Verification

Order:
1. `go test -race ./internal/runtime/... ./internal/usagegate/... ./internal/db/...`
   (new tests: exhausted target → 0 enqueues + 1 marker row; repeat tick → still 1 row;
   reset → exactly 1 digest + wake; `Usage == nil` → unchanged behavior; exempt kinds
   still enqueue).
2. `go vet ./...` + `gofmt -l .` (Makefile `test-go` gates on both; `web-build` untouched).
3. Live check post-redeploy: `sqlite3 ~/.swarm/swarm.db` pending-relay count for the
   orchestrator-2 agent stays flat while `five_hour` is 100%; after reset exactly one
   `suppressed:true` digest arrives.
4. Skip/error paths: `SWARM_USAGE` unset (nil Usage) behaves exactly as today;
   fetch-error snapshots never suppress (gate's own fail-open).

## Explicitly out of scope

- Changing the exhaustion threshold or headline-meter semantics.
- Per-model / per-pool suppression (weekly Opus cap still wakes Sonnet agents — existing behavior kept).
- Capping or TTL-expiring non-exhausted inboxes.
- Agent-origin `swarm_send` gating (sender already gets the liveness error).
- Coalescing across quota resets (one digest per reset; leftover rows flush on the next reset).
- Menubar/web surfacing of suppressed counts.

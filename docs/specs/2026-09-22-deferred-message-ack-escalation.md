# Relay checkpoints always wake the orchestrator

## Context

Live incident (2026-09-22): child agent `s11-seams` filed
`swarm_checkpoint kind:"progress"` on TASK-107 with a summary that read like
a finished report. `progress` is a deferred-wake-class relay
(`internal/runtime/inbox.go`'s `ImmediateRelayEvents`), so it never entered
the `WakeDue` pipeline (`internal/runtime/wake.go`) — it just sat
`state='pending'` in the `messages` table. The orchestrator
(`full-go-api-migration-orchestrator-2`) never called `swarm_sync` on its own
initiative, so the message was never folded into a digest and never seen.
Confirmed directly against the live DB: `msg_01M34ZJGTSPD12Q0ZSAZS7KJP7`
stayed `pending`/`deferred` indefinitely.

Root cause: deferred messages have no timeout backstop, and the digest
mechanism only fires when the recipient proactively calls `swarm_sync`. A
deferred message can sit forever if the recipient never happens to sync.

An initial design (escalate a stale deferred message to `wake_class`
`'immediate'` after a 2-minute timeout, mirroring `notifyNoAck`'s
child→parent ack-timeout pattern in `reconcile.go`) was superseded after
pulling empirical checkpoint-cadence data from the live DB (873 checkpoints /
167 sessions):

- Gap between consecutive checkpoints in the same session: median 5 min,
  p25 77s, p90 ~20 min.
- The single heaviest session on record (146 checkpoints) still averages
  8.5 min between `progress` checkpoints.
- Typical orchestrator fan-out is single digits to low tens of children.

This data shows the deferred/digest split's original rationale (avoid
spamming the orchestrator with frequent progress pings) doesn't hold in
practice — checkpoints are never frequent enough for immediate wakes to be
disruptive, even in the worst observed case. So instead of adding a new
escalation-timeout mechanism, this spec removes the deferred classification
for relay messages entirely: every checkpoint-driven relay wakes the
recipient immediately, same as `accepted`/`completed`/`failed`/etc. already
do.

Affected repo: `agent-swarm` (this repo), worktree
`~/GitHub/agent-swarm--deferred-ack-escalation`, branch
`fix/deferred-message-ack-escalation`. No other worktree touches
`internal/runtime/inbox.go` or `wake.go` at time of writing.

## Locked decisions

- `WakeClassFor` (`internal/runtime/inbox.go`): a `kind == "relay"` message
  is always `"immediate"`, regardless of its `event` payload field. The
  `relayEvent` parameter and the `ImmediateRelayEvents` var become dead and
  are deleted, along with the payload-sniffing code in `enqueue` that
  existed only to feed `relayEvent` into `WakeClassFor`.
- `WakeClassFor`'s remaining logic (non-relay `MessageKind` checked against
  `ImmediateKinds`) is unchanged — e.g. `digest` stays deferred. Only the
  relay branch changes.
- No new timeout, no new DB column writes, no new reconcile step. This is a
  pure classification change at enqueue time.
- Out of scope: the `digest`/`foldDigest` machinery in `inbox.go` is not
  removed, even though relay messages will no longer produce deferred rows
  for it to fold (checkpoints were the only realistic source of deferred
  relays). Leaving it in place costs nothing and isn't part of this fix;
  removing it would be a separate, unrequested cleanup.
- Out of scope: no change to what `ImmediateKinds` covers, no change to
  `undeliverableAfter` (stays 5 min — the earlier plan to lower it to 2 min
  was tied to the superseded escalation-timeout design and no longer
  applies), no change to `WakeDue`/`wakeCandidates` themselves.

## Behavior change

`internal/runtime/inbox.go`:

```go
// WakeClassFor: every relay now wakes immediately (2026-09-22: empirical
// checkpoint cadence — median 5 min, worst observed case 8.5 min between
// progress checkpoints — showed the deferred/digest split's anti-spam
// rationale never held in practice, and it was silently losing real
// completion reports like TASK-107's).
func WakeClassFor(kind MessageKind) WakeClass {
	if kind == "relay" {
		return "immediate"
	}
	if slices.Contains(ImmediateKinds, kind) {
		return "immediate"
	}
	return "deferred"
}
```

- Delete `ImmediateRelayEvents` var.
- In `enqueue`, delete the `relayEvent` extraction block (the
  `json.Unmarshal` into `p.Event`) and call `WakeClassFor(m.Kind)` (one
  arg).

## DB models

No schema change.

## Verification

1. `go test ./internal/runtime/...` green, including:
   - `TestWakeClassFor` (`inbox_test.go`): move `{"relay","progress"}` and
     `{"relay","handoff"}` from the deferred cases into the immediate
     cases (drop the `event` field from the table since it's no longer a
     parameter — or keep it as a documentation-only field naming which
     relay event was historically deferred, implementer's call, as long as
     the assertions match `WakeClassFor`'s new one-arg signature). `digest`
     stays in the deferred case.
   - `TestDeferredMessagesNeverWake` (`wake_test.go`, I19 invariant): the
     test currently enqueues a `progress` relay directly via the `enq`
     test helper (bypassing `WriteCheckpoint`) and asserts nothing gets
     pasted. Since a `progress` relay is now immediate, this test's premise
     is gone for relay messages — rewrite it to cover what's actually still
     deferred (a `digest` message, via `enq(..., "digest", ...)`), keeping
     the I19 "a deferred message never wakes anyone" comment/intent for the
     kind that's still deferred, not deleting the coverage.
   - Add a new test asserting a `progress` checkpoint written via
     `WriteCheckpoint` produces an `immediate`-wake-class relay and that
     `WakeDue` pastes for it under the same conditions
     `TestNativeWakeSkipsThePaste`/similar already use — this is the direct
     regression test for the TASK-107 incident.
2. `go build ./... && go vet ./...` clean (parameter removal touches a
   public function signature — confirm no other caller exists beyond
   `inbox.go`'s own `enqueue` and the test file; already confirmed via grep
   during investigation).

## File list

- `internal/runtime/inbox.go` — changed (`WakeClassFor` signature + body,
  `enqueue`'s relayEvent extraction removed, `ImmediateRelayEvents` var
  removed).
- `internal/runtime/inbox_test.go` — changed (`TestWakeClassFor`).
- `internal/runtime/wake_test.go` — changed (`TestDeferredMessagesNeverWake`
  retargeted to `digest`), new test for the progress-checkpoint-wakes case.
- Everything else: unchanged.

## Explicitly out of scope

- Removing the now-mostly-unreachable digest/foldDigest machinery.
- Any change to `undeliverableAfter`, `ackTimeout`, or `WakeDue`'s own
  cooldown/paste logic.
- Any change to what counts as a valid checkpoint `kind` at the
  `swarm_checkpoint` tool level.

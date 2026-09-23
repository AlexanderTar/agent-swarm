# 2026-09-23 — Hold relays while exhausted: implementation plan

Strict TDD order. Each task: failing test → run, watch fail → minimal implementation →
run, verify pass → commit. Stage explicit file paths only; never `git add -A`.

## Task 1 — migration + `holdIfExhausted` helper + unit tests

Failing test first in `internal/runtime/inbox_test.go` (new `TestHoldIfExhaustedSuppressesRelay`):
fake `UsageReader` returning true for `claude`, false otherwise; call
`holdIfExhausted` twice for the same `(agent, event)`; assert second call still true
and only one `suppressed_relays` row with `count == 2`. Second test with nil `Usage`
asserts false (fail open). Run: `go test -race ./internal/runtime/ -run TestHoldIfExhausted`.

Implement:
- `internal/db/schema/0009_hold_relays_while_exhausted.sql` (exact SQL from spec).
- `internal/runtime/inbox.go`: `holdIfExhausted(ctx, tx, toAgentID, event, sample)` —
  resolve kind via `agentByIDTx`, check `s.Usage.Exhausted(ctx, kind)`, upsert marker,
  return held bool. Consumes `(ctx, tx, toAgentID, event, sample)`; produces `(bool, error)`.
- Wire `dbtest`/migration list if it enumerates files (check `internal/db/db.go` glob first).

Verify: `go test -race ./internal/runtime/ -run 'TestHoldIfExhausted|TestFoldDigest'`
plus `go test ./internal/db/`. Commit: `git add docs/specs/2026-09-23-hold-relays-while-exhausted.md docs/plans/2026-09-23-hold-relays-while-exhausted.md internal/db/schema/0009_hold_relays_while_exhausted.sql internal/runtime/inbox.go internal/runtime/inbox_test.go` then commit.

## Task 2 — central gate in `enqueue` + guard extensions

The gate lives in `enqueue`, not at each call site (every daemon relay already
funnels through it; no caller of a daemon relay reads the returned ID).

Failing tests first (fake exhausted `Fake` kind via `fakeUsage`):
- `TestCheckpointRelayHeldWhileExhausted` (`checkpoint_test.go`): child checkpoint
  with exhausted parent kind → 0 new `messages` rows to the parent, 1
  `suppressed_relays` row with the checkpoint-kind event.
- `TestNoAckHeldWhileExhausted` (`reconcile_test.go`, canonical no_ack setup):
  notify still fires once, 0 `no_ack` relays, 1 suppressed row; second tick changes nothing.
- `TestProgressDeadlockHeldWhileExhausted` (`reconcile_test.go`, deadlock setup):
  0 `progress_deadlock` relays, 1 suppressed row.
- `TestEscalateUnackedHeldWhileExhausted` (`inbox_test.go`, direct `escalateUnacked`
  call against the worker tree): 0 `message_unacked` relays, 1 suppressed row.
- `TestFoldDigestStillDeliversWhileExhausted` (`inbox_test.go`): deferred rows fold
  into a real `digest` even when exhausted (fold compresses pre-existing load).

Implement in `internal/runtime/inbox.go`:
- Split `enqueue` into gated `enqueue` + ungated `enqueueRaw` (current insert body).
  `enqueue` holds when `m.Origin == "daemon"`, `m.Kind ∈ {relay, digest, advice}`,
  and the target kind is exhausted (event = payload `event` field, fallback kind
  string); returns zero `Message`, nil error. `foldDigest` calls `enqueueRaw`.
- `internal/runtime/reconcile.go`: `alreadyRelayed` also returns true when a
  matching `suppressed_relays` row (same agent, event, `last_at >= sinceStartedAt`)
  exists; `alreadyRelayedForCheckpoint` likewise for `progress_deadlock` rows in the
  same root with `last_at >=` the checkpoint's `created_at`.

Verify: `go test -race ./internal/runtime/`. Commit runtime + tests only (explicit paths).

## Task 3 — `WakeDue` skip + `WakeOnQuotaReset` flush

Failing tests in `internal/runtime/wake_test.go`:
- `TestWakeDueSkipsExhaustedKind`: live session + pending immediate message, fake
  reader exhausted for its kind → `WakeDue` performs no native wake, no paste, no
  `markWoken`; message stays pending.
- `TestWakeOnQuotaResetFlushesOneDigest`: seed 3 suppressed rows (2 events) for a
  `claude` agent, call `WakeOnQuotaReset(ctx, claude, cutoff)` → exactly 1 `digest`
  with `"suppressed": true` and 2 lines, rows deleted, session woken.
- Non-exhausted control: same setup with reader false → today's behavior unchanged.

Implement in `internal/runtime/wake.go`:
- `WakeDue`: after loading `rows`, `continue` when `s.Usage != nil &&
  s.Usage.Exhausted(ctx, r.Kind)` (before `raiseUndeliverable` so exhaustion doesn't
  feed undeliverable escalations either).
- `WakeOnQuotaReset`: first query `suppressed_relays` joined to agents of `kind`,
  enqueue one digest per agent (cap lines, 1500-byte budget mirroring `foldDigest`),
  delete flushed rows, then existing wake logic unchanged.

Verify: `go test -race ./internal/runtime/ -run 'TestWake|TestQuotaReset|TestHold'`,
then full `go test -race ./internal/runtime/... ./internal/usagegate/... ./internal/db/...`,
`go vet ./...`, `gofmt -l .`. Commit `wake.go` + `wake_test.go`.

## Task 4 — merge, push, redeploy

Preconditions: all of Task 3 green in this session.
1. `git log feat/hold-relays-while-exhausted --oneline` vs `origin/main`; rebase only
   this branch if behind (`git rebase origin/main` inside the worktree — private branch,
   no sharing, so no amend/shared-tree issue).
2. Fast-forward `main` to the branch, push `main` to `origin`, prune worktree:
   `git worktree remove ../agent-swarm--hold-relays-exhausted` from the main checkout
   after confirming merge.
3. Redeploy daemon: `make install-daemon` (builds `bin/swarm`, installs to
   `~/.swarm/bin`), daemon restarts via launchd; confirm with
   `bin/swarm status` and the sqlite pending-relay check from the spec.
4. If any relevant test fails at merge time: stop, fix on the branch, re-run — never
   narrow the run (`-run` excludes) for the final gate.

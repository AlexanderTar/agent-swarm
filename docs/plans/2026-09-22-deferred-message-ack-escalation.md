# Plan: relay checkpoints always wake the orchestrator

Spec: `docs/specs/2026-09-22-deferred-message-ack-escalation.md`
Worktree: `~/GitHub/agent-swarm--deferred-ack-escalation`
Branch: `fix/deferred-message-ack-escalation`

## Task 1 — failing test: a `progress` checkpoint wakes immediately

File: `internal/runtime/wake_test.go`

Add (near `TestNativeWakeSkipsThePaste`):

```go
// 2026-09-22 live incident: s11-seams filed a progress checkpoint reading
// like a finished report (TASK-107) and its orchestrator never got pinged
// -- progress was deferred-wake-class, so it just sat pending until
// somebody happened to sync. Checkpoint cadence data (873 checkpoints /
// 167 sessions, median gap 5 min, worst observed case 8.5 min) showed the
// anti-spam rationale for deferring progress never held in practice, so
// every relay checkpoint now wakes immediately, same as completed/failed/etc.
func TestProgressCheckpointWakesImmediately(t *testing.T) {
	s, tm, fa := newStore(t)
	fa.WakeOK = true
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "ProgressWake", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Progress, Summary: "midway report"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	var wakeAt *int64
	s.DB.QueryRowContext(ctx, `SELECT last_wake_at FROM sessions WHERE id = ?`, ses.ID).Scan(&wakeAt)
	if wakeAt == nil {
		t.Fatal("a progress checkpoint's relay must wake its parent immediately, not wait for a sync")
	}
}
```

Check `StartSpike`'s fixture actually gives the spawned agent a
`ParentAgentID` (WriteCheckpoint's relay-to-parent block at
`checkpoint.go:426` is gated on `a.ParentAgentID != ""`) — if `StartSpike`
spawns a root-level agent with no parent, switch the fixture to whatever
existing helper spawns a child with a parent (grep `ParentAgentID:` in
`*_test.go` under `internal/runtime` for the established pattern used by
e.g. `TestNativeWakeSkipsThePaste`-adjacent child-spawn tests, or
`checkpoint_test.go`'s own child-with-parent setup). Match that pattern
exactly rather than inventing a new one. The session under test here must
be the *parent's* session (`ses`), and the checkpoint call must come from
the *child's* session — read `checkpoint_test.go` for the two-agent
(parent + child) fixture shape it already uses for its own relay
assertions before writing this test's exact setup.

Run: `go test ./internal/runtime/... -run TestProgressCheckpointWakesImmediately -v`
Confirm it fails (the relay for this checkpoint is still `deferred`, so
`last_wake_at` stays NULL).

## Task 2 — implement: `WakeClassFor` always treats relay as immediate

File: `internal/runtime/inbox.go`

1. Delete the `ImmediateRelayEvents` var (lines ~20-23).
2. Replace `WakeClassFor`:
   ```go
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
3. In `enqueue`, delete the block that unmarshals `m.Payload` into
   `relayEvent` (the `var relayEvent string` / `if m.Kind == "relay" { ... }`
   lines just above `m.WakeClass = WakeClassFor(...)`), and change the call
   to `m.WakeClass = WakeClassFor(m.Kind)`.
4. `goimports`/check whether `encoding/json` is still used elsewhere in the
   file before removing any import — it almost certainly is (other message
   payloads), so don't blindly strip the import.

Run: `go build ./internal/runtime/...` — must compile clean (no leftover
reference to `ImmediateRelayEvents` or the removed local var).

Run: `go test ./internal/runtime/... -run TestProgressCheckpointWakesImmediately -v`
Confirm it now passes.

## Task 3 — fix the tests this change breaks

Run: `go test ./internal/runtime/... 2>&1 | grep -i FAIL`

Expect at least:

- `TestWakeClassFor` (`inbox_test.go`): currently calls
  `WakeClassFor(c.kind, c.event)` — update every call site to the new
  one-arg signature. Move `{"relay", "progress"}` and `{"relay", "handoff"}`
  out of the "deferred" table into the "immediate" table (or just assert
  `WakeClassFor("relay")` once for the relay case generically, since the
  event no longer matters — reviewer's call on which reads better, but
  keep asserting `digest` is still deferred).
- `TestDeferredMessagesNeverWake` (`wake_test.go`): currently enqueues a
  `relay`/`progress` message via `enq(...)` and asserts it never pastes.
  That relay is now immediate, so this exact scenario is gone. Rewrite it
  to use `enq(t, s, a.ID, a.RootItemID, "digest", ..., 1)` instead (digest
  is still deferred — confirmed unaffected by this change) so the I19
  "a deferred message never wakes anyone" coverage is preserved for the
  kind that's actually still deferred, not deleted. Keep the rest of the
  test (advance 120s, assert zero pastes) as-is.

Do not delete either test — both get edited to match the new contract,
per the "no test deletion for a mismatch, only genuine behavior removal"
rule. Progress/handoff being deferred *was* the behavior; it was
deliberately removed by this change, so retargeting these two tests is the
correct move, not a violation — but say so explicitly in the commit
message (which behavior went away and why) rather than doing it silently.

## Task 4 — full verification

1. `go build ./... && go vet ./...`
2. `go test ./...` from the worktree root (build `web/dist` first if
   `TestBoardServedAtRoot` 503s — known fresh-worktree gap, run
   `cd web && pnpm install --frozen-lockfile && pnpm build` first; this is
   environmental, not caused by this change).
3. `gofmt -l internal/runtime/inbox.go internal/runtime/wake_test.go internal/runtime/inbox_test.go` — must be empty.

## Task 5 — commit

One commit, explicit paths (no `git add -A`):

```
git add internal/runtime/inbox.go internal/runtime/inbox_test.go internal/runtime/wake_test.go docs/specs/2026-09-22-deferred-message-ack-escalation.md docs/plans/2026-09-22-deferred-message-ack-escalation.md
git commit -m "fix(runtime): every relay checkpoint wakes its parent immediately

progress/handoff checkpoints were deferred-wake-class, silently folded
into a digest only when the recipient proactively synced. Live incident
2026-09-22: s11-seams's progress checkpoint on TASK-107 sat unseen
because its orchestrator never synced. Checkpoint cadence data (873
checkpoints / 167 sessions, median 5 min gap, worst observed case 8.5
min) shows the deferred split's anti-spam rationale never held in
practice, so every relay now wakes immediately like completed/failed
already did."
```

## Report back

Summarize: files changed, test run output (pass counts), anything from
Task 1's fixture note that required deviating from this plan (e.g. if
`checkpoint_test.go`'s actual parent/child fixture shape differs from what's
described here — use what's really there, not this plan's guess).

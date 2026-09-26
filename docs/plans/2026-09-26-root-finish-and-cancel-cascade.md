# Root-done finish and cancel cascade: implementation plan

Spec: `docs/specs/2026-09-26-root-finish-and-cancel-cascade.md`. Read it first. Its copy, locked decisions and scenarios R1-R7 and C1-C6 are normative.
Worktree: `/Users/alexandertar/GitHub/agent-swarm-root-finish`, branch `feat/root-finish-cancel-cascade`. Work only there.

Rules for every task:

- Strict TDD: write the test, run it and watch it fail for the stated reason, write the minimal code, run it and watch it pass, then commit.
- Stage explicit paths only. Never `git add -A`, never `--amend`, and never delete a test (port it).
- End every commit message with:

```
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
```

- Never touch the primary checkout, `~/.swarm`, the live daemon, launchd, `tmux -L swarm` or the user's real `~/.claude*`.
- `make e2e` uses its own port (17778) and socket (`swarm-e2e`), so it is safe.

## Batches

| Batch | Scope | Tasks | Depends on |
|---|---|---|---|
| **1** | Items: `RootDone` hook and cancel cascade. Runtime: `OnRootDone`, Cancel on a done root, handoff refusal, daemon wiring | 1.1-1.7 | none |
| **2** | Reconcile pass for work on cancelled items, skills and `make skills-sync`, e2e, full verification | 2.1-2.5 | Batch 1 |

Each batch gets one implementer and one review loop.

New runtime tests go in one new file, `internal/runtime/finish_test.go` (package `runtime`). New runtime code goes in one new file, `internal/runtime/finish.go`.

---

## Batch 1

### 1.1 `RootDone` fires only when a root reaches Done

**Consumes:** `setStatus` (`internal/items/transition.go:447`), and the test helpers `tree`, `setStatus`, `seedCheckpoint`, `later`, `wantStatus`, `exec`, `gitJSON` (package `items_test`).
**Produces:** `Store.RootDone func(ctx context.Context, tx *sql.Tx, rootID string) error`.

1. Append to `internal/items/hooks_test.go`:

```go
// RootDone fires exactly when a top-level item reaches Done (spec R7): a
// story deriving Done is not a root, an accepted epic is.
func TestRootDoneFiresOnlyWhenARootReachesDone(t *testing.T) {
	st := newStore(t)
	var got []string
	st.RootDone = func(ctx context.Context, tx *sql.Tx, id string) error {
		got = append(got, id)
		return nil
	}
	e, story, task := tree(t, st)
	seedCheckpoint(t, st.DB, e, "accepted", 1, later(st), "")
	if err := st.Reconcile(ctx, e.Key); err != nil {
		t.Fatal(err)
	}
	setStatus(t, st, task, items.Done)
	if err := st.Reconcile(ctx, task.Key); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, st, story.Key, items.Done)
	if len(got) != 0 {
		t.Fatalf("RootDone fired for a non-root: %v", got)
	}
	seedCheckpoint(t, st.DB, e, "integrated", 1, later(st), gitJSON)
	if err := st.Reconcile(ctx, e.Key); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, st, e.Key, items.InReview)
	exec(t, st.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ?`, e.ID)
	if err := st.Reconcile(ctx, e.Key); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, st, e.Key, items.Done)
	if len(got) != 1 || got[0] != e.ID {
		t.Fatalf("RootDone calls = %v, want [%s]", got, e.ID)
	}
}
```

2. Run `go test ./internal/items/ -run TestRootDoneFiresOnlyWhenARootReachesDone -count=1`. It must fail to compile with `st.RootDone undefined`.
3. `internal/items/store.go`: add after the `DepUnblocked` field (line 36):

```go
	// RootDone fires when a top-level item (id == root_id) reaches Done: an
	// accepted epic or bug, a closed or materialized spike, a done chore.
	// rootID is that item's id; the hook finishes every agent still on the
	// root. nil does nothing, same as the other hooks.
	RootDone func(ctx context.Context, tx *sql.Tx, rootID string) error
```

4. `internal/items/transition.go`, in `setStatus`, insert just before `if (to == Done || to == Cancelled) && s.DepUnblocked != nil {`:

```go
	if to == Done && it.ID == it.RootID && s.RootDone != nil {
		if err := s.RootDone(ctx, tx, it.ID); err != nil {
			return err
		}
	}
```

5. Run the test again. It passes. Also run `go test ./internal/items/ -count=1`, which passes.
6. Commit `internal/items/store.go internal/items/hooks_test.go internal/items/transition.go`: `feat(items): RootDone hook fires when a top-level item reaches Done`.

### 1.2 Cancel cascades to unfinished descendants

**Consumes:** `TransitionTx` (`transition.go:38`), `setStatus`, `staleAccepts`, `getByID`.
**Produces:** `func (s *Store) cancelDescendants(ctx context.Context, tx *sql.Tx, parentID string) error`.

1. Append to `internal/items/transition_test.go`:

```go
// Spec C1, C4: cancelling a parent cancels every unfinished descendant,
// nested story -> task included; Done work stays Done; reopening the parent
// leaves the children cancelled.
func TestCancelCascadesToUnfinishedDescendants(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	setStatus(t, s, task, items.InProgress)
	done := mk(t, s, items.Task, st.Key, "Already done")
	setStatus(t, s, done, items.Done)
	blocked := mk(t, s, items.Task, st.Key, "Waiting")
	setStatus(t, s, blocked, items.Blocked)
	st2 := mk(t, s, items.Story, e.Key, "Second") // Draft
	nested := mk(t, s, items.Task, st2.Key, "Nested")
	setStatus(t, s, nested, items.Ready)

	if err := move(t, s, e.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	cascaded := []string{st.Key, task.Key, blocked.Key, st2.Key, nested.Key}
	for _, k := range cascaded {
		wantStatus(t, s, k, items.Cancelled)
	}
	wantStatus(t, s, done.Key, items.Done)

	if err := move(t, s, e.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, e.Key, items.Ready)
	for _, k := range cascaded {
		wantStatus(t, s, k, items.Cancelled)
	}
}

// Spec C2: an orchestrator's story cancel reaches the story's tasks and
// leaves the epic alone.
func TestOrchestratorStoryCancelCascadesToItsTasks(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	if err := move(t, s, st.Key, items.Cancelled, items.Orchestrator("agt_o", e.ID)); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, task.Key, items.Cancelled)
	wantStatus(t, s, e.Key, items.Ready)
}
```

2. Run `go test ./internal/items/ -run 'TestCancelCascades|TestOrchestratorStoryCancel' -count=1`. Both fail: `STORY-1 status = ready, want cancelled` and `TASK-1 status = ready, want cancelled`.
3. `internal/items/transition.go`, in `TransitionTx`, directly after the `s.setStatus(ctx, tx, &it, to)` error check:

```go
	if to == Cancelled {
		if err := s.cancelDescendants(ctx, tx, it.ID); err != nil {
			return Item{}, err
		}
	}
```

   Add after `staleAccepts`:

```go
// cancelDescendants cancels every descendant of parentID that is not Done or
// Cancelled yet (root-finish spec, locked decision 4). It runs wherever the
// parent's cancel was allowed, so there is no per-child check(). Archived
// children are included: archiving hides an item, it does not finish it.
func (s *Store) cancelDescendants(ctx context.Context, tx *sql.Tx, parentID string) error {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE d(id) AS (
			SELECT id FROM items WHERE parent_id = ?
			UNION ALL SELECT i.id FROM items i JOIN d ON i.parent_id = d.id)
		SELECT id FROM items WHERE id IN (SELECT id FROM d) AND status NOT IN ('done', 'cancelled')`, parentID)
	if err != nil {
		return err
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range pending {
		c, err := s.getByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.setStatus(ctx, tx, &c, Cancelled); err != nil {
			return err
		}
		if err := s.staleAccepts(ctx, tx, c); err != nil {
			return err
		}
	}
	return nil
}
```

   (The variable is `pending`, not `ids`, because `transition.go` imports the `ids` package.)
4. Run both tests. They pass. Then run `go test ./internal/items/ -count=1` (this includes `TestCancelRules`, `TestDepUnblockedFiresOnlyOnDoneOrCancelled`, `TestCancelStalesTheOpenAcceptRequest` and `TestStaleAcceptsCoversApprovalKinds`), which passes.
5. Commit `internal/items/transition.go internal/items/transition_test.go`: `feat(items): cancelling an item cancels its unfinished descendants`.

### 1.3 `OnRootDone`: live agents get a daemon `completed`, and the reaper ends them after the grace

**Consumes:** `cancelAgentOperationsTx` (`replacement.go:295`), `publishAgentChanged` (`agents.go:2307`), `ids.New`, `db.Millis`, and the test helpers `clockStore`, `worker`, `panes`, `spawnGracePeriod`.
**Produces:** `rootDoneSummary`, `errRootAcceptedHandoff`, `func (s *Store) OnRootDone(ctx, tx, rootID string) error`, `func (s *Store) rootIsDone(ctx, q txQuerier, rootID string) (bool, error)`.

1. Create `internal/runtime/finish_test.go`:

```go
package runtime

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"
)

// finishRoot moves key to Done the raw way and fires OnRootDone in the same
// tx, exactly as items.setStatus does once the root is accepted.
func finishRoot(t *testing.T, s *Store, key string) {
	t.Helper()
	ctx := context.Background()
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE id = ?`, it.ID); err != nil {
			return err
		}
		return s.OnRootDone(ctx, tx, it.ID)
	}); err != nil {
		t.Fatal(err)
	}
}

// daemonCompleted counts agentID's daemon-written completed checkpoints on its own item.
func daemonCompleted(t *testing.T, s *Store, agentID string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM checkpoints c
		JOIN agents a ON a.id = c.agent_id WHERE c.agent_id = ? AND c.item_id = a.item_id
		AND c.kind = 'completed' AND c.daemon_written = 1`, agentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Spec R1.
func TestRootDoneLiveAgentsEndCompletedAfterGrace(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	finishRoot(t, s, "EPIC-1")

	var summary string
	var attempt int
	if err := s.DB.QueryRowContext(ctx, `SELECT summary, attempt FROM checkpoints
		WHERE agent_id = ? AND item_id = ? AND kind = 'completed' AND daemon_written = 1`,
		orch.ID, orch.ItemID).Scan(&summary, &attempt); err != nil {
		t.Fatal(err)
	}
	if summary != "EPIC-1 is done; Swarm finished this agent." || attempt != orchSes.Attempt {
		t.Fatalf("checkpoint = %q at attempt %d, want the root-done summary at %d", summary, attempt, orchSes.Attempt)
	}
	if daemonCompleted(t, s, w.ID) != 1 {
		t.Fatal("the live worker got no daemon completed checkpoint")
	}

	panes(tm, Pane{Session: orchSes.TmuxName, Command: "swarm-fake-agent"},
		Pane{Session: wSes.TmuxName, Command: "swarm-fake-agent"})
	tm.env[orchSes.TmuxName] = map[string]string{"SWARM_SESSION": orchSes.ID}
	tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("killed inside the grace: %v", tm.killed[before:])
	}
	at.Advance(61 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tm.killed[before:], orchSes.TmuxName) {
		t.Fatalf("killed = %v, want %s after the grace", tm.killed[before:], orchSes.TmuxName)
	}
	panes(tm) // both panes are gone now
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Agent{orch, w} {
		ses, _ := s.LatestSession(ctx, a.ID)
		got, _ := s.Agent(ctx, a.Name)
		if ses.State != Completed || got.State != AgentFinished {
			t.Fatalf("%s: session %s, agent %s; want completed/finished", a.Name, ses.State, got.State)
		}
	}
}
```

2. Run `go test ./internal/runtime/ -run TestRootDoneLiveAgentsEndCompletedAfterGrace -count=1`. It fails to compile with `s.OnRootDone undefined`.
3. Create `internal/runtime/finish.go` with the live path only. 1.4 and 1.5 add the other branches, each driven by its own failing test:

```go
package runtime

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// rootDoneSummary is the daemon-written completed checkpoint's summary; %s is
// the root key (docs/specs/2026-09-26-root-finish-and-cancel-cascade.md).
const rootDoneSummary = "%s is done; Swarm finished this agent."

// errRootAcceptedHandoff is WriteCheckpoint's handoff refusal on a done root.
const errRootAcceptedHandoff = "Root accepted; write completed."

// rootIsDone reports whether the top-level item rootID is Done. q is s.DB or a tx.
func (s *Store) rootIsDone(ctx context.Context, q txQuerier, rootID string) (bool, error) {
	var st string
	err := q.QueryRowContext(ctx, `SELECT status FROM items WHERE id = ?`, rootID).Scan(&st)
	return st == string(items.Done), err
}

// OnRootDone is items.Store.RootDone (spec locked decision 1): rootID just
// reached Done, so every agent still queued or active on it finishes. A
// spawning/running session gets a daemon-written completed checkpoint on its
// own item and attempt; resolveAlive kills the pane killCompletedAfter later
// and resolveDeadInner ends it completed, which leaves the orchestrator a
// minute to read approval_result and post its final message.
func (s *Store) OnRootDone(ctx context.Context, tx *sql.Tx, rootID string) error {
	var rootKey, rootType string
	if err := tx.QueryRowContext(ctx, `SELECT key, type FROM items WHERE id = ?`, rootID).Scan(&rootKey, &rootType); err != nil {
		return err
	}
	_ = rootType // the spike guard (1.5) reads it
	type agentRow struct {
		id, name, itemID, sesID string
		state                   SessionState
		attempt                 int
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.name, a.item_id, COALESCE(ses.id, ''),
		COALESCE(ses.state, ''), COALESCE(ses.attempt, 0)
		FROM agents a LEFT JOIN sessions ses ON ses.id = (SELECT id FROM sessions
			WHERE agent_id = a.id ORDER BY generation DESC, attempt DESC LIMIT 1)
		WHERE a.root_item_id = ? AND a.state IN ('queued', 'active')`, rootID)
	if err != nil {
		return err
	}
	var todo []agentRow
	for rows.Next() {
		var r agentRow
		var st string
		if err := rows.Scan(&r.id, &r.name, &r.itemID, &r.sesID, &st, &r.attempt); err != nil {
			rows.Close()
			return err
		}
		r.state = SessionState(st)
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := db.Millis(s.now())
	for _, r := range todo {
		if r.state.Live() {
			if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
				attempt, summary, daemon_written, created_at) VALUES (?, ?, ?, ?, 'completed', ?, ?, 1, ?)`,
				ids.New("ckp"), r.sesID, r.id, r.itemID, r.attempt, fmt.Sprintf(rootDoneSummary, rootKey), now); err != nil {
				return err
			}
		}
	}
	return nil
}
```

   `rootType`, `errRootAcceptedHandoff` and `rootIsDone` are not used until 1.4 to 1.6. The `_ = rootType` line keeps the unused local compiling until 1.5 deletes it. Go allows unused package-level constants and methods.
4. Run the test. It passes.
5. Commit `internal/runtime/finish.go internal/runtime/finish_test.go`: `feat(runtime): OnRootDone writes a daemon completed for live agents on a done root`.

### 1.4 `OnRootDone`: paused and pausing agents finish at once, and in-flight operations are cancelled

1. Append to `internal/runtime/finish_test.go`, and add `"github.com/AlexanderTar/agent-swarm/internal/db"` to its imports:

```go
// Spec R2.
func TestRootDoneFinishesPausedAndPausingAgentsAtOnce(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', ended_at = ? WHERE id = ?`, now, orchSes); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, wSes.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_root_done', ?, 'handoff', 'ready', 'k1', ?, 1, ?, ?)`, orch.ID, orchSes, now, now); err != nil {
		t.Fatal(err)
	}
	finishRoot(t, s, "EPIC-1")
	for _, a := range []Agent{orch, w} {
		ses, _ := s.LatestSession(ctx, a.ID)
		got, _ := s.Agent(ctx, a.Name)
		if ses.State != Completed || got.State != AgentFinished {
			t.Fatalf("%s: session %s, agent %s; want completed/finished", a.Name, ses.State, got.State)
		}
		if n := daemonCompleted(t, s, a.ID); n != 0 {
			t.Fatalf("%s got %d daemon checkpoints on the direct path, want 0", a.Name, n)
		}
	}
	var phase string
	s.DB.QueryRowContext(ctx, `SELECT phase FROM agent_operations WHERE id = 'op_root_done'`).Scan(&phase)
	if phase != "cancelled" {
		t.Fatalf("operation phase = %q, want cancelled", phase)
	}
}
```

2. Run `-run TestRootDoneFinishesPausedAndPausingAgentsAtOnce`. It fails with the paused orchestrator untouched (`session paused, agent active`). Once that branch exists, it fails on the `stopping` worker getting a checkpoint, and then on `operation phase = "ready"`.
3. In `OnRootDone`, replace the loop body with:

```go
	for _, r := range todo {
		// ponytail: in-tx, so no lockAgentOperations; a driver already past
		// its launch commit could still start a successor (its handoff is
		// refused and a close records completed). Take the lock post-commit
		// if that ever shows up.
		if err := s.cancelAgentOperationsTx(ctx, tx, r.id); err != nil {
			return err
		}
		if r.state.Live() && !r.state.Pausing() {
			if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
				attempt, summary, daemon_written, created_at) VALUES (?, ?, ?, ?, 'completed', ?, ?, 1, ?)`,
				ids.New("ckp"), r.sesID, r.id, r.itemID, r.attempt, fmt.Sprintf(rootDoneSummary, rootKey), now); err != nil {
				return err
			}
			continue
		}
		// resolveDeadInner would end a pausing session paused, not completed,
		// so everything that is not spawning/running finishes here; a leftover
		// pane goes on Reconcile's next finished-agent pass.
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'completed', ended_at = COALESCE(ended_at, ?)
			WHERE id = ? AND state IN ('pause_requested', 'quiescing', 'stopping', 'paused', 'interrupted')`,
			now, r.sesID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`, now, r.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ?
			WHERE agent_id = ? AND released_at IS NULL`, now, r.id); err != nil {
			return err
		}
		if err := s.publishAgentChanged(ctx, tx, r.name, rootID); err != nil {
			return err
		}
	}
```

4. Run the 1.3 and 1.4 tests. Both pass.
5. Commit `internal/runtime/finish.go internal/runtime/finish_test.go`: `feat(runtime): OnRootDone finishes paused and pausing agents directly`.

### 1.5 Spike close and materialize paths stay unchanged

1. Append to `internal/runtime/finish_test.go`, and add `"github.com/AlexanderTar/agent-swarm/internal/items"` to its imports:

```go
// Spec R5: close_spike approved -> spike Done; its orchestrator already wrote
// completed + resolution and ends through that, not a daemon checkpoint.
func TestRootDoneLeavesTheLiveSpikeOrchestratorAlone(t *testing.T) {
	s, _, _ := newStore(t)
	s.Items.RootDone = s.OnRootDone
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Nothing", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, a.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: CompletedCkp,
		Summary: "nothing to build", Resolution: "no_change"}); err != nil {
		t.Fatal(err)
	}
	var reqID string
	s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE kind = 'close_spike'`).Scan(&reqID)
	if _, err := s.Approve(ctx, reqID, ApproveInput{Via: "board"}); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Items.Get(ctx, "SPIKE-1"); it.Status != items.Done {
		t.Fatalf("spike status = %s, want done", it.Status)
	}
	if n := daemonCompleted(t, s, a.ID); n != 0 {
		t.Fatalf("%d daemon checkpoints for the spike orchestrator, want 0", n)
	}
	got, _ := s.Agent(ctx, a.Name)
	latest, _ := s.LatestSession(ctx, a.ID)
	if got.State != AgentActive || !latest.State.Live() {
		t.Fatalf("spike orchestrator %s / session %s, want active and live", got.State, latest.State)
	}
}

// Spec R6: swarm_materialize moves the spike to Done inside its own call;
// the calling orchestrator must not be finished under it.
func TestMaterializeDoesNotFinishTheSpikeOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	s.Items.RootDone = s.OnRootDone
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", ""); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Items.Get(ctx, "SPIKE-1"); it.Status != items.Done {
		t.Fatalf("spike status = %s, want done", it.Status)
	}
	if n := daemonCompleted(t, s, ses.AgentID); n != 0 {
		t.Fatalf("%d daemon checkpoints for the materializing orchestrator, want 0", n)
	}
	a, _ := s.agentByID(ctx, ses.AgentID)
	latest, _ := s.LatestSession(ctx, a.ID)
	if a.State != AgentActive || !latest.State.Live() {
		t.Fatalf("orchestrator %s / session %s, want active and live", a.State, latest.State)
	}
}
```

2. Run `-run 'TestRootDoneLeavesTheLiveSpike|TestMaterializeDoesNotFinish'`. Both fail with `1 daemon checkpoints for the ... orchestrator, want 0`.
3. In `OnRootDone`, delete `_ = rootType`, and add as the first statement of the loop body (before the `ponytail:` comment):

```go
		// The spike's own live orchestrator is inside swarm_materialize, or it
		// already wrote completed with a resolution (close_spike); it ends
		// through that checkpoint, not a daemon one (spec decision 1c).
		if rootType == string(items.Spike) && r.itemID == rootID && r.state.Live() {
			continue
		}
```

4. Run both tests plus the 1.3 and 1.4 tests. All pass.
5. Commit `internal/runtime/finish.go internal/runtime/finish_test.go`: `feat(runtime): spike close and materialize keep their own finish`.

### 1.6 Cancel on a done root records Completed, and a late handoff is refused

1. Append to `internal/runtime/finish_test.go`, and add `"errors"` to its imports:

```go
// Spec R4: a root Done before RootDone existed (no hook ran); closing its
// agents records completed, live or paused.
func TestCancelOnADoneRootRecordsCompleted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, wSes.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE key = 'EPIC-1'`); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Agent{orch, w} {
		if _, err := s.Cancel(ctx, a.Name, "", ""); err != nil {
			t.Fatal(err)
		}
		ses, _ := s.LatestSession(ctx, a.ID)
		got, _ := s.Agent(ctx, a.Name)
		if ses.State != Completed || got.State != AgentFinished {
			t.Fatalf("%s: session %s, agent %s; want completed/finished", a.Name, ses.State, got.State)
		}
	}
}

// Spec R3.
func TestHandoffRefusedOnceTheRootIsDone(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	ses := mustSessionID(t, s, orch.ID)
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE key = 'EPIC-1'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Handoff, Summary: "saving my place"})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict || ie.Message != "Root accepted; write completed." {
		t.Fatalf("err = %v, want conflict %q", err, "Root accepted; write completed.")
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: CompletedCkp, Summary: "all done"}); err != nil {
		t.Fatalf("completed after acceptance: %v", err)
	}
}
```

2. Run `-run 'TestCancelOnADoneRoot|TestHandoffRefusedOnce'`. Both fail: `session cancelled` for the orchestrator (`paused` for the worker), and `err = <nil>` for the handoff.
3. `internal/runtime/agents.go`, in `Cancel`, replace:

```go
	ses, err := s.LatestSession(ctx, a.ID)
	if err == nil && ses.State.Live() {
		ad := s.Adapters[a.Kind]
		if ad != nil {
			_ = s.Tmux.Keys(ctx, ses.TmuxName, ad.InterruptKeys()...)
		}
		_ = s.Tmux.Kill(ctx, ses.TmuxName)
		_ = s.SetSessionState(ctx, ses.ID, Cancelled)
	}
```

   with:

```go
	// Root-finish spec decision 2: closing an agent whose root is already
	// Done records the work as completed, not cancelled. A paused or
	// interrupted session on a Done root is closed too; on any other root it
	// stays as it is, so a cancelled agent remains resumable.
	rootDone, err := s.rootIsDone(ctx, s.DB, a.RootItemID)
	if err != nil {
		return Agent{}, err
	}
	end := Cancelled
	if rootDone {
		end = Completed
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err == nil && ses.State.Live() {
		ad := s.Adapters[a.Kind]
		if ad != nil {
			_ = s.Tmux.Keys(ctx, ses.TmuxName, ad.InterruptKeys()...)
		}
		_ = s.Tmux.Kill(ctx, ses.TmuxName)
		_ = s.SetSessionState(ctx, ses.ID, end)
	} else if err == nil && rootDone && (ses.State == Paused || ses.State == Interrupted) {
		_ = s.SetSessionState(ctx, ses.ID, Completed)
	}
```

4. `internal/runtime/checkpoint.go`, in `WriteCheckpoint`, insert right after the `if !ses.State.Live() { ... "This session is not live." ... }` block (line 1114) and before the `ses.State.Pausing()` check:

```go
		if in.Kind == Handoff {
			done, err := s.rootIsDone(ctx, tx, a.RootItemID)
			if err != nil {
				return err
			}
			if done {
				return &items.Error{Code: items.CodeConflict, Message: errRootAcceptedHandoff}
			}
		}
```

5. Run both tests. They pass. Then run `go test -race ./internal/runtime/ -count=1`, which passes. If a test that cancels an agent now sees `completed`, it is on a root the test left Done; port its expectation in place and do not delete it.
6. Commit `internal/runtime/agents.go internal/runtime/checkpoint.go internal/runtime/finish_test.go`: `feat(runtime): a done root closes as completed and refuses a late handoff`.

### 1.7 Wire the hook in the daemon

1. `cmd/swarm/daemon.go`: after `it.StoryReadyForReview = rt.OnStoryReadyForReview` (line 287), add:

```go
	it.RootDone = rt.OnRootDone                       // finish every agent on a root that just reached Done
```

2. Run `go build ./... && go vet ./cmd/swarm/ && go test ./cmd/swarm/ -count=1`, which passes. (The wiring is exercised end to end by the e2e tail in 2.4. `cmd/swarm` has no unit seam for hooks.)
3. Commit `cmd/swarm/daemon.go`: `feat(daemon): wire items RootDone to runtime OnRootDone`.

**Batch 1 exit:** `go test -race ./internal/items/ ./internal/runtime/ ./internal/httpapi/ ./internal/mcpserver/ ./cmd/swarm/ -count=1` is green.

---

## Batch 2

### 2.1 Reconcile cancels workflows and agents on cancelled items

**Consumes:** `CancelWorkflow` (`workflow.go:1917`), `Cancel`, `queryIDs` (`reconcile.go:39`), and the test helpers `seedEpicWithTwoTasks`, `seedWorkflowRun`.
**Produces:** `func (s *Store) cancelWorkOnCancelledItems(ctx context.Context) error`, called from `Reconcile`.

1. Append to `internal/runtime/finish_test.go`:

```go
// Spec C3, C6: after a story cancel, one Reconcile tick cancels the workflow
// and the coder on the cascaded task; the Done sibling's coder and the
// orchestrator are untouched; a second tick finds nothing left.
func TestReconcileCancelsWorkOnCancelledItems(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	spawn := func(key string) Agent {
		w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: key, Role: RoleCoder, Kind: Fake,
			Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "build it"}})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	w1, w2 := spawn("TASK-1"), spawn("TASK-2")
	t1, _ := s.Items.Get(ctx, "TASK-1")
	wfID, runID := seedWorkflowRun(t, s, t1.ID, t1.RootID, orch.ID, w1.ID, "build", "coder", 1)
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE key = 'TASK-2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Items.Transition(ctx, "STORY-1", items.Cancelled, items.User("board")); err != nil {
		t.Fatal(err)
	}

	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var wfState, runState string
	s.DB.QueryRowContext(ctx, `SELECT state FROM workflows WHERE id = ?`, wfID).Scan(&wfState)
	s.DB.QueryRowContext(ctx, `SELECT state FROM workflow_runs WHERE id = ?`, runID).Scan(&runState)
	if wfState != "cancelled" || runState != "cancelled" {
		t.Fatalf("workflow %s / run %s, want cancelled/cancelled", wfState, runState)
	}
	ses1, _ := s.LatestSession(ctx, w1.ID)
	got1, _ := s.Agent(ctx, w1.Name)
	if ses1.State != Cancelled || got1.State != AgentFinished {
		t.Fatalf("TASK-1 coder: session %s, agent %s; want cancelled/finished", ses1.State, got1.State)
	}
	if !slices.Contains(tm.killed[before:], ses1.TmuxName) {
		t.Fatalf("killed = %v, want %s", tm.killed[before:], ses1.TmuxName)
	}
	for _, a := range []Agent{w2, orch} {
		if got, _ := s.Agent(ctx, a.Name); got.State != AgentActive {
			t.Fatalf("%s = %s, want active (not on a cancelled item)", a.Name, got.State)
		}
	}
	if it, _ := s.Items.Get(ctx, "TASK-1"); it.Status != items.Cancelled {
		t.Fatalf("TASK-1 = %s, want cancelled (CancelWorkflow's Ready move is denied)", it.Status)
	}
	after := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != after {
		t.Fatalf("second tick killed %v, want nothing", tm.killed[after:])
	}
}
```

2. Run `-run TestReconcileCancelsWorkOnCancelledItems`. It fails with `workflow running / run active, want cancelled/cancelled`.
3. Append to `internal/runtime/finish.go`:

```go
// cancelWorkOnCancelledItems is Reconcile's cascade pass (spec locked
// decision 5): work still running on a cancelled item stops. Workflows go
// first, because Spawn has no cancelled-item guard and a running workflow row
// could respawn a run. Then every queued or active agent assigned to a
// cancelled item goes through the same Cancel the board uses. One failure is
// logged, never returned, so it cannot stall the rest of Reconcile.
func (s *Store) cancelWorkOnCancelledItems(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT i.key, i.root_id FROM workflows w JOIN items i ON i.id = w.item_id
		WHERE i.status = 'cancelled' AND w.state IN ('running', 'escalated')
		AND w.created_at = (SELECT MAX(created_at) FROM workflows WHERE item_id = w.item_id)`)
	if err != nil {
		return err
	}
	var wfs [][2]string
	for rows.Next() {
		var key, rootID string
		if err := rows.Scan(&key, &rootID); err != nil {
			rows.Close()
			return err
		}
		wfs = append(wfs, [2]string{key, rootID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, wf := range wfs {
		// CancelWorkflow only reads the caller's root for its scope check.
		if _, err := s.CancelWorkflow(ctx, Agent{RootItemID: wf[1]}, wf[0], "", ""); err != nil {
			s.logf("reconcile: cancel workflow on cancelled %s: %v", wf[0], err)
		}
	}
	names, err := s.queryIDs(ctx, `SELECT a.name FROM agents a JOIN items i ON i.id = a.item_id
		WHERE i.status = 'cancelled' AND a.state IN ('queued', 'active')`)
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err := s.Cancel(ctx, name, "", ""); err != nil {
			s.logf("reconcile: cancel %s on a cancelled item: %v", name, err)
		}
	}
	return nil
}
```

4. `internal/runtime/reconcile.go`, in `Reconcile`, directly before `if err := s.TickPause(ctx); err != nil {` (line 228):

```go
	// Root-finish spec decision 5: stop workflows and agents on cancelled
	// items before DrainQueue could admit a queued one.
	if err := s.cancelWorkOnCancelledItems(ctx); err != nil {
		return err
	}
```

5. Run the test. It passes. Then run `go test -race ./internal/runtime/ ./internal/httpapi/ ./internal/mcpserver/ -count=1`, which passes. Any test that cancels an item and then expects its agent or workflow to keep running gets ported in place with a note, not deleted.
6. Commit `internal/runtime/finish.go internal/runtime/finish_test.go internal/runtime/reconcile.go`: `feat(runtime): reconcile cancels workflows and agents on cancelled items`.

### 2.2 Skills

1. `skills/swarm-orchestrator/SKILL.md`, line 20: replace `when deciding the final branch integration and user handoff.` with `when deciding the final branch integration and what to report to the user.`
2. Line 52: replace

   ``On `approval_result` `approved` the daemon has already moved the item to done: write `completed`.``

   with

   ``On `approval_result` `approved` the daemon has already moved the item to done and ends your session about a minute later: post a short final summary in chat, write `completed` if you want it on record, and stop. Never write `handoff` after acceptance; Swarm refuses it (`Root accepted; write completed.`).``
3. After line 51 (`  - \`swarm_control cancel\`: …`), insert a line at the same two-space sub-bullet indent:

   ``  - Cancelling a story or task (`swarm_items update status: "cancelled"`) also cancels every descendant that isn't Done, and Swarm cancels their agents and workflows within a few seconds. Reopening it later doesn't reopen those children.``
4. Run `make skills-sync`, then `git status --short internal/install/skills`. Only `internal/install/skills/swarm-orchestrator/SKILL.md` changes, and `diff skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md` is empty.
5. Run `go test ./internal/install/ ./internal/runtime/ -run 'Skill' -count=1`, which passes. (`workflows_skill_test.go` and the install tests read the skill text. If one asserts the old line-52 sentence, port its expected string to the new copy.)
6. Commit `skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md`: `docs(skills): orchestrators stop after acceptance; cancel cascades`.

### 2.3 e2e: root finish after accept (extend the epic-lane scenario in place)

1. `scripts/e2e/epicapproval_test.go`: add `"strings"` to the imports. At the end of `TestScenarioEpicApprovalLane`, after the final `epic status = done` check, append:

```go
	// Root finish (docs/specs/2026-09-26-root-finish-and-cancel-cascade.md
	// R1, R3): the daemon wrote the orchestrator's completed on its own item,
	// a late handoff is refused, and once the pane goes (killPane stands in
	// for the 60 s reaper) the session ends completed.
	var daemonCompleted int
	h.db(t).QueryRow(`SELECT COUNT(*) FROM checkpoints c JOIN agents a ON a.id = c.agent_id
		WHERE a.name = ? AND c.item_id = a.item_id AND c.kind = 'completed' AND c.daemon_written = 1`,
		orch).Scan(&daemonCompleted)
	if daemonCompleted != 1 {
		t.Fatalf("%d daemon completed checkpoints for %s, want 1", daemonCompleted, orch)
	}
	err := h.tool(t, orch, "swarm_checkpoint", map[string]any{"kind": "handoff", "summary": "saving my place"})
	if err == nil || !strings.Contains(err.Error(), "Root accepted; write completed.") {
		t.Fatalf("late handoff err = %v, want the root-accepted refusal", err)
	}
	h.killPane(t, orch)
	if !h.waitForSessionState(t, orch, "completed", 30*time.Second) {
		t.Fatalf("orchestrator session = %s, want completed", h.sessionState(t, orch))
	}
	if got := h.agentState(t, orch); got != "finished" {
		t.Fatalf("orchestrator agent = %s, want finished", got)
	}
```

   (If `err` is already declared in that scope, use `herr :=`.)
2. Run `make e2e` (or `scripts/e2e.sh -run TestScenarioEpicApprovalLane` if the script forwards `-run`). The test passes; before 1.3-1.7 it would fail at the checkpoint count.
3. Commit `scripts/e2e/epicapproval_test.go`: `test(e2e): accepted epic finishes its orchestrator as completed`.

### 2.4 e2e: cancel cascade

1. Create `scripts/e2e/cancelcascade_test.go`:

```go
//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Cancel cascade (docs/specs/2026-09-26-root-finish-and-cancel-cascade.md
// C1, C3, C4): cancelling the epic on the board cancels its story and task,
// the reconciler cancels the worker and the orchestrator, and reopening the
// epic leaves the children cancelled.
func TestScenarioCancelCascade(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "ready", "revision": h.itemRevision(t, epic)}, nil)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting the epic"})
	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")

	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "cancelled", "revision": h.itemRevision(t, epic)}, nil)
	if got := h.itemStatus(t, task); got != "cancelled" {
		t.Fatalf("task status = %s, want cancelled", got)
	}
	for _, name := range []string{worker, orch} {
		if !h.waitForAgentState(t, name, "finished", 20*time.Second) {
			t.Fatalf("%s agent = %s, want finished", name, h.agentState(t, name))
		}
		if got := h.sessionState(t, name); got != "cancelled" {
			t.Fatalf("%s session = %s, want cancelled", name, got)
		}
	}

	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "ready", "revision": h.itemRevision(t, epic)}, nil)
	if got := h.itemStatus(t, epic); got != "ready" {
		t.Fatalf("epic status = %s, want ready", got)
	}
	if got := h.itemStatus(t, task); got != "cancelled" {
		t.Fatalf("task status after reopen = %s, want cancelled", got)
	}
}
```

2. Run `make e2e`. The scenario passes and every existing scenario stays green.
3. Commit `scripts/e2e/cancelcascade_test.go`: `test(e2e): cancelling an epic cascades to its tree and agents`.

### 2.5 Full verification (spec command order)

1. `go test ./internal/items/ -run 'TestRootDone|TestCancelCascades|TestOrchestratorStoryCancel|TestCancelRules|TestDepUnblocked' -race -count=1`
2. `go test ./internal/runtime/ -run 'TestRootDone|TestMaterializeDoesNotFinish|TestCancelOnADoneRoot|TestHandoffRefusedOnce|TestReconcileCancelsWork' -race -count=1`
3. `go test -race ./internal/items/ ./internal/runtime/ ./internal/httpapi/ ./internal/mcpserver/ ./cmd/swarm/ -count=1`
4. `make skills-sync && git status --short internal/install/skills` (clean after 2.2's commit)
5. `make vet fmt`, then `go test -race ./...`
6. `make e2e`

Nothing to commit unless `make fmt` changed a file. If it did, commit exactly those paths: `style: gofmt`.

## Consumes / produces summary

| Symbol | File | Kind |
|---|---|---|
| `items.Store.RootDone func(ctx, tx, rootID string) error` | `internal/items/store.go` | new field |
| `(*items.Store).cancelDescendants(ctx, tx, parentID string) error` | `internal/items/transition.go` | new |
| `rootDoneSummary`, `errRootAcceptedHandoff` | `internal/runtime/finish.go` | new consts |
| `(*runtime.Store).OnRootDone(ctx, tx, rootID string) error` | `internal/runtime/finish.go` | new, exported (daemon wiring) |
| `(*runtime.Store).rootIsDone(ctx, q txQuerier, rootID string) (bool, error)` | `internal/runtime/finish.go` | new |
| `(*runtime.Store).cancelWorkOnCancelledItems(ctx) error` | `internal/runtime/finish.go` | new |
| `Cancel`, `WriteCheckpoint`, `Reconcile`, `TransitionTx`, `setStatus` | existing | body changes only |

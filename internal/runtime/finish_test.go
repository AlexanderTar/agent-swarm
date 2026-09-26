package runtime

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
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

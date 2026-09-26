package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// CRITICAL 2 (plan: TestStartRecoversSameOrchestrator's cancel case): user
// Cancel on an orchestrator with two finished children keeps a recoverable
// identity. The next Start restarts the same agent -- same id and name, never
// build-it-orchestrator-2 -- with its children still attached.
// auto_restart only gates daemon-driven restarts, not an explicit Start.
func TestCancelThenStartRecoversSameOrchestrator(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kids []Agent
	for _, key := range []string{"TASK-1", "TASK-2"} {
		k, _, err := s.Spawn(ctx, SpawnInput{ItemKey: key, Role: RoleCoder, Kind: Fake, Model: "fake-1",
			ParentAgentID: orch.ID, Brief: BriefInput{Objective: "build " + key}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`,
			db.Millis(s.Now()), k.ID); err != nil {
			t.Fatal(err)
		}
		kids = append(kids, k)
	}
	if _, err := s.Cancel(ctx, orch.Name, "", ""); err != nil {
		t.Fatal(err)
	}

	again, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil || queued {
		t.Fatalf("start after cancel: err = %v queued = %v", err, queued)
	}
	if again.ID != orch.ID || again.Name != orch.Name {
		t.Fatalf("start after cancel = %s/%q, want the same agent %s/%q (never a suffixed one)",
			again.ID, again.Name, orch.ID, orch.Name)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE role = 'orchestrator'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("orchestrator rows = %d, want 1", n)
	}
	for _, k := range kids {
		got, err := s.AgentByID(ctx, k.ID)
		if err != nil || got.ParentAgentID != orch.ID {
			t.Fatalf("child %s parent = %q (%v), want %s", k.Name, got.ParentAgentID, err, orch.ID)
		}
	}
	after, err := s.AgentByID(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != AgentActive {
		t.Fatalf("recovered orchestrator state = %s, want active", after.State)
	}
	succ, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.ID == orchSes.ID || succ.Generation != orchSes.Generation+1 {
		t.Fatalf("successor = %s gen %d, want a new session at gen %d", succ.ID, succ.Generation, orchSes.Generation+1)
	}
	// IMPORTANT 2: the restart rides the recovery path -- a durable recover
	// operation and the recovery kickoff, not the fresh-assignment one.
	var mode, phase string
	if err := s.DB.QueryRowContext(ctx, `SELECT mode, phase FROM agent_operations WHERE agent_id = ?
		ORDER BY created_at DESC LIMIT 1`, orch.ID).Scan(&mode, &phase); err != nil {
		t.Fatalf("no replacement operation recorded for the restart: %v", err)
	}
	if mode != "recover" || phase != "succeeded" {
		t.Fatalf("operation = %s/%s, want recover/succeeded", mode, phase)
	}
	if k := fa.LastSpec.Kickoff; !strings.Contains(k, "continuing in a fresh session after interrupted recovery") {
		t.Fatalf("restart kickoff is not the recovery kickoff:\n%s", k)
	}
}

// IMPORTANT 2: Start never races an in-flight replacement: it refuses naming
// the operation instead of launching beside it.
func TestStartRefusesWhileReplacementInFlight(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_start_guard', ?, 'recover', 'queued', 'k1', ?, ?, ?, ?)`,
		orch.ID, ses.ID, ses.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict || !strings.Contains(ie.Message, "op_start_guard") {
		t.Fatalf("err = %v, want 409 naming op_start_guard", err)
	}
	var sessions int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, orch.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("sessions = %d, want no launch beside the in-flight operation", sessions)
	}
}

// An explicit replacement request (POST /handoff, CLI, swarm_control) on a
// user-cancelled agent re-enables auto_restart, so a walk that parks is
// resumed by the reconciler instead of stranded behind the Cancel flag.
func TestReplacementAfterCancelIsResumedByReconcile(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if _, err := s.Cancel(ctx, w.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	// The cancelled pane is still on its way out: the walk parks in stopping.
	panes(tm, Pane{Session: wSes.TmuxName})
	tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "after-cancel", "")
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase != PhaseStopping {
		t.Fatalf("phase = %q, want parked in stopping", op.Phase)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.getOperation(ctx, op.ID); got.Phase != PhaseSucceeded {
		t.Fatalf("phase after reconcile = %q (%s), want succeeded", got.Phase, got.Error)
	}
}

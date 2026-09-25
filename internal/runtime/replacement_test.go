package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// TestReplacementRequestReplay pins the request-identity contract: replaying
// the same request key returns the same durable operation without side
// effects, while a different key against an in-flight operation is a 409
// naming the active operation.
func TestReplacementRequestReplay(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_1', ?, 'recover', 'queued', 'k1', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	sessions := func() int {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := sessions(); n != 1 {
		t.Fatalf("sessions = %d, want 1 before replay", n)
	}

	// Duplicate key returns the same operation: no new row, no new session,
	// no error.
	op, err := s.RequestReplacement(ctx, w.ID, ModeRecover, "k1", "")
	if err != nil {
		t.Fatalf("replay err = %v", err)
	}
	if op.ID != "op_1" {
		t.Fatalf("replay op = %q, want op_1", op.ID)
	}
	if n := sessions(); n != 1 {
		t.Fatalf("sessions = %d after replay, want 1 (no side effects)", n)
	}

	// Different key while one is in flight: 409 naming the active operation.
	_, err = s.RequestReplacement(ctx, w.ID, ModeRecover, "k2", "")
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
		t.Fatalf("err = %v, want 409 conflict", err)
	}
	if !strings.Contains(ie.Message, "op_1") {
		t.Fatalf("conflict message %q does not name the active operation", ie.Message)
	}
	if n := sessions(); n != 1 {
		t.Fatalf("sessions = %d after refused intent, want 1 (intent never persisted)", n)
	}

	// Unknown mode is a bad request, also without side effects.
	if _, err := s.RequestReplacement(ctx, w.ID, ReplacementMode("restart"), "k3", ""); err == nil {
		t.Fatal("expected bad request for unknown mode")
	} else if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest {
		t.Fatalf("err = %v, want bad request", err)
	}
}

// TestReplacementRecoverRoundTrip drives a full recover operation: intent
// persisted before side effects, predecessor stopped, successor launched by
// SWARM_SESSION identity (never pane name), predecessor credentials revoked
// before the successor activates, and the canonical agent row untouched.
func TestReplacementRecoverRoundTrip(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	oldToken := tokenPath(t, s, wSes.ID)

	// The predecessor pane is still alive (identified by its SWARM_SESSION
	// env, not its name), so the driver must park at stopping after
	// persisting intent and stopping the predecessor.
	panes(tm, Pane{Session: wSes.TmuxName})
	op, err := s.RequestReplacement(ctx, w.ID, ModeRecover, "rk", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	if op.Phase != PhaseStopping {
		t.Fatalf("phase = %q, want stopping (predecessor still alive)", op.Phase)
	}
	pre, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.ID != wSes.ID || pre.State != Interrupted {
		t.Fatalf("predecessor = %s/%s, want %s/interrupted", pre.ID, pre.State, wSes.ID)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sessions = %d, want 1 (no successor while predecessor alive)", n)
	}

	// The pane is gone: a later tick (or a daemon restart) resumes from the
	// durable stopping phase and launches the successor.
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	if _, pending, err := s.PendingOperation(ctx, w.ID); err != nil {
		t.Fatal(err)
	} else if pending {
		t.Fatal("operation still pending after resume")
	}
	var phase string
	if err := s.DB.QueryRowContext(ctx, `SELECT phase FROM agent_operations WHERE id = ?`, op.ID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != string(PhaseSucceeded) {
		t.Fatalf("phase = %q, want succeeded", phase)
	}
	succ, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.ID == wSes.ID {
		t.Fatal("no successor session started")
	}
	if succ.Generation != wSes.Generation+1 {
		t.Fatalf("successor generation = %d, want %d", succ.Generation, wSes.Generation+1)
	}
	// agents.id is canonical: same row, same name, only the session moved.
	after, err := s.AgentByID(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != w.Name {
		t.Fatalf("agent renamed %q -> %q", w.Name, after.Name)
	}
	// Predecessor credentials were revoked before the successor activated.
	if _, err := tokenStat(oldToken); err == nil {
		t.Fatalf("predecessor token still present at %s", oldToken)
	}
	notified(t, s, "agent.retried")
}

// TestReconcileSkipsSessionUnderReplacement proves the operation driver
// owns its sessions: a live session with no pane and an in-flight operation
// is left alone by Reconcile, and only resolves as crashed once the
// operation is gone.
func TestReconcileSkipsSessionUnderReplacement(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	old := s.Now().Add(-time.Hour).UnixMilli()
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, old, wSes.ID); err != nil {
		t.Fatal(err)
	}
	// A known-dead pane skips the spawn grace window deterministically.
	panes(tm, Pane{Session: wSes.TmuxName, Dead: true, DeadStatus: 1})
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_owned', ?, 'recover', 'stopping', 'k', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile err = %v", err)
	}
	ses, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Running {
		t.Fatalf("session under operation resolved to %q, want untouched running", ses.State)
	}
	for _, k := range s.Notify.(*fakeNotifier).kinds() {
		if k == "agent.crashed" {
			t.Fatal("bogus crash notification for a session owned by an operation")
		}
	}
	// Operation gone: the same tick now resolves the dead session as crashed.
	if _, err := s.DB.ExecContext(ctx, `UPDATE agent_operations SET phase = 'succeeded' WHERE id = 'op_owned'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile err = %v", err)
	}
	ses, err = s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ses.State != Crashed {
		t.Fatalf("session = %q after operation cleared, want crashed", ses.State)
	}
}

func tokenPath(t *testing.T, s *Store, sessionID string) string {
	t.Helper()
	return filepath.Join(s.Home, "run", "tokens", sessionID)
}

func tokenStat(path string) (os.FileInfo, error) { return os.Stat(path) }

// TestStartRecoversSameOrchestrator pins the lifecycle contract: a
// stop/crash with an unfinished assignment stays recoverable, and a later
// StartOrchestrator for the exact same assignment restarts the existing
// logical orchestrator in place -- same canonical id, same name (never a
// suffixed second agent), new session generation. An active orchestrator
// still conflicts.
func TestStartRecoversSameOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil || queued {
		t.Fatalf("start err = %v, queued = %v", err, queued)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Active orchestrator: starting again conflicts, no new row.
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"}); err == nil {
		t.Fatal("expected conflict while orchestrator is active")
	}
	// Crash with the assignment unfinished: still recoverable.
	if err := s.SetSessionState(ctx, ses.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	again, queued, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil || queued {
		t.Fatalf("recover err = %v, queued = %v", err, queued)
	}
	if again.ID != orch.ID {
		t.Fatalf("recovered id = %s, want %s (same canonical agent)", again.ID, orch.ID)
	}
	if again.Name != orch.Name {
		t.Fatalf("recovered name = %q, want %q (never a suffixed agent)", again.Name, orch.Name)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents
		WHERE role = 'orchestrator' AND root_item_id = ?`, orch.RootItemID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("orchestrator rows = %d, want 1 (no suffixed second agent)", n)
	}
	succ, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.ID == ses.ID || succ.Generation != ses.Generation+1 {
		t.Fatalf("successor = %s gen %d, want new session at gen %d",
			succ.ID, succ.Generation, ses.Generation+1)
	}
}

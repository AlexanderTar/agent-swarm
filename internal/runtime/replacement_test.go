package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func tokenPath(t *testing.T, s *Store, sessionID string) string {
	t.Helper()
	return filepath.Join(s.Home, "run", "tokens", sessionID)
}

func tokenStat(path string) (os.FileInfo, error) { return os.Stat(path) }

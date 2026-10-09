package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func conflictOrNotFound(err error, code string) bool {
	var ie *items.Error
	return errors.As(err, &ie) && ie.Code == code
}

func TestCancelOperationEndsInFlightOnceAndLeavesTerminalAlone(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, Stopping); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, w.Name, "queued", orchSes.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	op, ok, err := s.PendingOperation(ctx, w.ID)
	if err != nil || !ok {
		t.Fatalf("pending = %v, %v; want the queued retry", ok, err)
	}

	// The same session's retry key dedups: a second retry adds no second operation.
	if _, err := s.Retry(ctx, w.Name, "another", orchSes.ID, "r2"); err != nil {
		t.Fatalf("second retry err = %v, want a no-op", err)
	}
	if err := wantCount(s, `SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, 1, w.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.CancelOperation(ctx, op.ID)
	if err != nil || got.Phase != PhaseCancelled {
		t.Fatalf("cancel = %+v err=%v, want cancelled", got, err)
	}
	if _, ok, _ := s.PendingOperation(ctx, w.ID); ok {
		t.Fatal("cancelled operation still pending")
	}
	again, err := s.CancelOperation(ctx, op.ID)
	if err != nil || again.Phase != PhaseCancelled {
		t.Fatalf("second cancel = %+v err=%v, want the terminal row unchanged", again, err)
	}
	if _, err := s.CancelOperation(ctx, "op_missing"); !conflictOrNotFound(err, items.CodeNotFound) {
		t.Fatalf("unknown op err = %v, want not found", err)
	}
	if _, err := s.getOperation(ctx, "op_missing"); !conflictOrNotFound(err, items.CodeNotFound) {
		t.Fatalf("getOperation err = %v, want not found", err)
	}
}

func TestRetryDuringStoppingConflictsWithOtherInFlightOperation(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, Stopping); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_other', ?, 'handoff', 'preserving', 'user-key', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	_, err = s.Retry(ctx, w.Name, "late", orchSes.ID, "r1")
	if !conflictOrNotFound(err, items.CodeConflict) || !strings.Contains(err.Error(), "op_other") {
		t.Fatalf("retry err = %v, want a conflict naming op_other", err)
	}
	if err := wantCount(s, `SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, 1, w.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRequestHandoffToRefusesWorkersAndFinishedOrchestrators(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	sw := AgentSwitch{Kind: Fake, Model: "fake-1"}

	if _, err := s.RequestHandoffTo(ctx, "agt_missing", "k0", sw); err == nil {
		t.Fatal("handoff of an unknown agent succeeded")
	}
	if _, err := s.RequestHandoffTo(ctx, w.ID, "k1", sw); err == nil || err.Error() != errSwitchNotOrchestrator {
		t.Fatalf("worker handoff err = %v, want %q", err, errSwitchNotOrchestrator)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestHandoffTo(ctx, orch.ID, "k2", sw); err == nil || err.Error() != errSwitchNotLive {
		t.Fatalf("finished orchestrator err = %v, want %q", err, errSwitchNotLive)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("operations = %d err=%v, want none after refused handoffs", n, err)
	}
}

func TestResumeOperationsSkipsTerminalAndPropagatesQueryErrors(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("no operations = %v, want nil", err)
	}
	if err := s.ResumeOperations(canceledCtx()); err == nil {
		t.Fatal("ResumeOperations swallowed a DB error")
	}
}

package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Batch 3: Pause, Resume and Retry consult the replacement coordinator. While
// an operation is in flight the operation driver owns every session
// transition, so direct control calls refuse naming the active operation
// instead of racing it. (Retry-during-stopping queueing itself is pinned in
// replacement_test.go's TestRetryDuringStoppingQueuesIntent.)
func TestPauseRefusesWhileReplacementInFlight(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_pause_guard', ?, 'handoff', 'stopping', 'k1', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.pause(ctx, w.Name, "session"); err == nil {
		t.Fatal("Pause during an in-flight handoff must be refused")
	} else {
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeConflict || !strings.Contains(ie.Message, "op_pause_guard") {
			t.Fatalf("err = %v, want 409 naming op_pause_guard", err)
		}
	}
}

func TestResumeRefusesWhileReplacementInFlight(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Paused); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_resume_guard', ?, 'handoff', 'queued', 'k1', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resume(ctx, w.Name, "", ""); err == nil {
		t.Fatal("Resume during an in-flight handoff must be refused")
	} else {
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeConflict || !strings.Contains(ie.Message, "op_resume_guard") {
			t.Fatalf("err = %v, want 409 naming op_resume_guard", err)
		}
	}
}

func TestRetryConsultsCoordinatorBeforeStateGuard(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_retry_guard', ?, 'handoff', 'preserving', 'k1', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, w.Name, "", "", ""); err == nil {
		t.Fatal("Retry during an in-flight handoff must be refused")
	} else {
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeConflict || !strings.Contains(ie.Message, "op_retry_guard") {
			t.Fatalf("err = %v, want 409 naming op_retry_guard", err)
		}
	}
}

// ControlsAgent is the swarm_control handoff authority check: an agent
// controls itself and its own subtree (every descendant), nothing else.
func TestControlsAgentFollowsTheParentChain(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	mk := func(parentID string) Agent {
		a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder,
			Kind: Fake, Model: "fake-1", ParentAgentID: parentID, Brief: BriefInput{Objective: "x"}})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	mid := mk(orch.ID)
	leaf := mk(mid.ID)
	for _, tc := range []struct {
		caller, target string
		want           bool
	}{
		{orch.ID, orch.ID, true},
		{orch.ID, mid.ID, true},
		{orch.ID, leaf.ID, true},
		{mid.ID, leaf.ID, true},
		{leaf.ID, orch.ID, false},
		{mid.ID, orch.ID, false},
		{leaf.ID, mid.ID, false},
	} {
		if got, err := s.ControlsAgent(ctx, tc.caller, tc.target); err != nil || got != tc.want {
			t.Errorf("ControlsAgent(%q, %q) = %v, %v; want %v", tc.caller, tc.target, got, err, tc.want)
		}
	}
	if _, err := s.ControlsAgent(ctx, orch.ID, "agt_missing"); err == nil {
		t.Error("unknown target must error, not read as uncontrolled")
	}
}

// Batch 3 cycle 5: a handoff requested while the predecessor is pausing
// parks in preserving instead of killing it, so the predecessor's handoff
// checkpoint can still bind the manifest. The pane stays up, the session
// stays pausing and live, and no successor starts yet.
func TestHandoffParksInPreservingWhilePredecessorCanSave(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, PauseRequested); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: wSes.TmuxName})
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "park1", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	if op.Phase != PhasePreserving {
		t.Fatalf("phase = %q, want preserving (predecessor still able to save)", op.Phase)
	}
	pre, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.ID != wSes.ID || !pre.State.Pausing() || !pre.State.Live() {
		t.Fatalf("predecessor = %s/%s, want %s still pausing and live", pre.ID, pre.State, wSes.ID)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sessions = %d, want 1 (no successor before preservation binds)", n)
	}
}

// The park ends without a manifest when there is nothing left to wait for:
// a pausing session whose pane is already gone proceeds manifest-less (the
// successor ships the broken-predecessor warning instead of hanging).
func TestHandoffProceedsManifestLessWhenPaneGone(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, PauseRequested); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "park2", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	if op.Phase == PhasePreserving {
		t.Fatalf("phase = %q, want the walk to proceed without a live pane to wait for", op.Phase)
	}
}

// The guards only fire while an operation is actually in flight: a finished
// handoff leaves Pause/Resume/Retry exactly as they were.
func TestGuardsClearAfterOperationSettles(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_done', ?, 'handoff', 'succeeded', 'k1', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.pause(ctx, w.Name, "session"); err != nil {
		t.Fatalf("Pause after a settled operation must work: %v", err)
	}
}

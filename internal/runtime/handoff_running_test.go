package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// CRITICAL 1: POST /handoff on a running agent (the menubar/CLI path, no
// pause first) must not kill it. The walk moves the session to
// pause_requested, delivers the HANDOFF notice through the pause delivery
// path, parks in preserving, and stops the predecessor only after its handoff
// checkpoint binds the manifest.
func TestHandoffOnRunningAgentPreservesBeforeStopping(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: wSes.TmuxName})
	tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.killed = nil // spawn's own stale-pane cleanup is not a stop

	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "run1", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	if op.Phase != PhasePreserving {
		t.Fatalf("phase = %q, want preserving", op.Phase)
	}
	if slices.Contains(tm.killed, wSes.TmuxName) {
		t.Fatal("predecessor pane killed before it could preserve its work")
	}
	pre, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.ID != wSes.ID || pre.State != PauseRequested || pre.PauseDeadlineAt == nil {
		t.Fatalf("predecessor = %s/%s deadline %v, want pause_requested with a deadline", pre.ID, pre.State, pre.PauseDeadlineAt)
	}
	var controls int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ?
		AND kind = 'control' AND state = 'pending' AND json_extract(payload_json, '$.action') = 'handoff'`,
		w.ID).Scan(&controls); err != nil {
		t.Fatal(err)
	}
	if controls != 1 {
		t.Fatalf("pending handoff control messages = %d, want 1", controls)
	}

	// A reconcile tick while the predecessor is still saving keeps it parked.
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.getOperation(ctx, op.ID); got.Phase != PhasePreserving {
		t.Fatalf("phase after tick = %q, want still preserving", got.Phase)
	}

	// The handoff checkpoint binds the manifest; only then does the walk stop
	// the predecessor and launch exactly one successor.
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "saved"}); err != nil {
		t.Fatalf("handoff checkpoint: %v", err)
	}
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tm.killed, wSes.TmuxName) {
		t.Fatal("predecessor pane never stopped after preservation bound")
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.getOperation(ctx, op.ID)
	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %q (%s), want succeeded", got.Phase, got.Error)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("sessions = %d, want predecessor plus one successor", n)
	}
}

// The preserving park ends at the pause deadline even when the pane never
// died: the walk then stops the predecessor itself (manifest-less, so the
// successor gets the broken-predecessor warning).
func TestHandoffPreservingStopsAtDeadline(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: wSes.TmuxName})
	tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.killed = nil
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "dl1", "")
	if err != nil || op.Phase != PhasePreserving {
		t.Fatalf("request = %+v, %v; want preserving", op, err)
	}
	tm.clk.Advance(time.Duration(defaultPauseDeadlineSec+1) * time.Second)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tm.killed, wSes.TmuxName) {
		t.Fatal("predecessor never stopped after the preservation deadline")
	}
	if got, _ := s.getOperation(ctx, op.ID); got.Phase == PhasePreserving {
		t.Fatal("operation still parked in preserving past the deadline")
	}
}

// The notice a handoff delivers is HANDOFF, not PAUSE: wake renders the
// normative HANDOFF template plus the preservation checklist.
func TestWakeRendersHandoffNoticeForHandoffControl(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: wSes.TmuxName, Command: "swarm-fake-agent"})
	tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
	if _, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "wk1", ""); err != nil {
		t.Fatal(err)
	}
	tm.clk.Advance(time.Minute)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	want := HandoffPreservationNotice(w.Name, "TASK-1")
	for _, p := range tm.pasted {
		if strings.HasSuffix(p, "|"+want) {
			return
		}
	}
	t.Fatalf("pasted = %q, want the HANDOFF preservation notice", tm.pasted)
}

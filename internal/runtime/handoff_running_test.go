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

// One handoff is one relay to the parent: event "handoff" (carrying the
// handoff summary), never a "paused" relay followed by an "interrupted" one.
func TestHandoffRelaysOnceToParent(t *testing.T) {
	for _, saves := range []bool{true, false} {
		s, tm, _ := newStore(t)
		ctx := context.Background()
		orch, w, wSes := worker(t, s)
		if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
			t.Fatal(err)
		}
		panes(tm, Pane{Session: wSes.TmuxName})
		tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
		if _, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "relay1", ""); err != nil {
			t.Fatal(err)
		}
		if saves {
			if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "saved unit 1"}); err != nil {
				t.Fatal(err)
			}
		} else {
			tm.clk.Advance(time.Duration(defaultPauseDeadlineSec+1) * time.Second)
		}
		if err := s.ResumeOperations(ctx); err != nil {
			t.Fatal(err)
		}
		panes(tm)
		if err := s.ResumeOperations(ctx); err != nil {
			t.Fatal(err)
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT json_extract(payload_json, '$.event') FROM messages
			WHERE kind = 'relay' AND to_agent_id = ? AND json_extract(payload_json, '$.agent') = ?`, orch.ID, w.Name)
		if err != nil {
			t.Fatal(err)
		}
		var events []string
		for rows.Next() {
			var e string
			rows.Scan(&e)
			events = append(events, e)
		}
		rows.Close()
		if len(events) != 1 || events[0] != "handoff" {
			t.Fatalf("saves=%v: relays to parent = %v, want exactly [handoff]", saves, events)
		}
	}
}

// Handoff of an agent that is already paused keeps the pause's save: the
// paused session's handoff checkpoint binds the manifest (ready gate runs),
// the session stays paused (no rewrite, no kill), and the successor gets no
// incomplete-recovery warning.
func TestHandoffOfPausedAgentBindsItsSave(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, PauseRequested); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "paused at unit 2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, Paused); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	tm.killed, tm.keys = nil, nil
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "paused1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.getOperation(ctx, op.ID)
	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %q (%s), want succeeded", got.Phase, got.Error)
	}
	var manifest string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(manifest_path, '') FROM agent_operations WHERE id = ?`, op.ID).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest == "" {
		t.Fatal("paused save never bound: manifest_path empty")
	}
	var state string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM sessions WHERE id = ?`, wSes.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(Paused) {
		t.Fatalf("predecessor state rewritten to %s, want paused", state)
	}
	for _, k := range tm.keys {
		if strings.HasPrefix(k, wSes.TmuxName+"|") {
			t.Fatalf("interrupt keys sent to the paused session's pane: %v", tm.keys)
		}
	}
	if strings.Contains(fa.LastSpec.Kickoff, "no usable manifest") {
		t.Fatalf("successor kickoff carries the incomplete-recovery warning:\n%s", fa.LastSpec.Kickoff)
	}
}

// The handoff checkpoint's relay says why it was written: mode "pause" for a
// plain Pause (the agent parks, it is not being replaced), mode "handoff"
// while a handoff operation is in flight.
func TestHandoffCheckpointRelayCarriesMode(t *testing.T) {
	for _, handoff := range []bool{false, true} {
		s, tm, _ := newStore(t)
		ctx := context.Background()
		orch, w, wSes := worker(t, s)
		if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
			t.Fatal(err)
		}
		panes(tm, Pane{Session: wSes.TmuxName})
		tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
		want := "pause"
		if handoff {
			want = "handoff"
			if _, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "mode1", ""); err != nil {
				t.Fatal(err)
			}
		} else if _, err := s.Pause(ctx, w.Name, "session"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "saved"}); err != nil {
			t.Fatal(err)
		}
		var mode string
		if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload_json, '$.mode'), '') FROM messages
			WHERE kind = 'relay' AND to_agent_id = ? AND json_extract(payload_json, '$.event') = 'handoff'`,
			orch.ID).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != want {
			t.Fatalf("handoff=%v: relay mode = %q, want %q", handoff, mode, want)
		}
	}
}

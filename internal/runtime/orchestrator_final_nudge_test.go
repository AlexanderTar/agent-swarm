package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// CHORE-171 U2: an idle orchestrator on a Done root with no final note gets a
// daemon nudge (5 min idle, then 15 min, 30 min, then hourly), never more than
// one pending, with one attention notification on the first nudge.

func finalNoteNudgeCount(s *Store, agentID string) int {
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update' AND origin = 'daemon'
		AND payload_json LIKE '%"event":"final_note_nudge"%'`, agentID).Scan(&n)
	return n
}

func doneRootOrchestrator(t *testing.T) (*Store, *fakeTmux, *testClock, Agent, Session, string) {
	t.Helper()
	s, tm, at := clockStore(t)
	ctx := context.Background()
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: ses.TmuxName, Command: "swarm-fake-agent"})
	finishRoot(t, s, key)
	return s, tm, at, orch, ses, key
}

func deliverFinalNoteNudges(t *testing.T, s *Store, agentID string) {
	t.Helper()
	mustExec(t, s.DB, `UPDATE messages SET state = 'delivered' WHERE to_agent_id = ?`, agentID)
}

func TestFinalNoteNudgeBacksOffAndSendsOneAttention(t *testing.T) {
	s, _, at, orch, _, key := doneRootOrchestrator(t)
	ctx := context.Background()

	at.Advance(4 * time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 0 {
		t.Fatalf("4 min idle: %d nudges, want 0", n)
	}
	at.Advance(time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 1 {
		t.Fatalf("5 min idle: %d nudges, want 1", n)
	}
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages WHERE to_agent_id = ?
		AND payload_json LIKE '%"event":"final_note_nudge"%'`, orch.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, key) || !strings.Contains(payload, "completed") {
		t.Fatalf("nudge payload = %s, want the root key and the completed note", payload)
	}
	if n := notifiedCount(s, "agent.final_note"); n != 1 {
		t.Fatalf("first nudge: %d attention notifications, want 1", n)
	}

	// A second tick while the first is still pending sends nothing.
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 1 {
		t.Fatalf("pending nudge: %d nudges, want 1", n)
	}

	// Then 15 min, 30 min, then hourly gaps.
	deliverFinalNoteNudges(t, s, orch.ID)
	at.Advance(14 * time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 1 {
		t.Fatalf("14 min after the first nudge: %d nudges, want 1", n)
	}
	at.Advance(time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 2 {
		t.Fatalf("15 min after the first nudge: %d nudges, want 2", n)
	}
	deliverFinalNoteNudges(t, s, orch.ID)
	at.Advance(29 * time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 2 {
		t.Fatalf("29 min after the second nudge: %d nudges, want 2", n)
	}
	at.Advance(time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 3 {
		t.Fatalf("30 min after the second nudge: %d nudges, want 3", n)
	}
	deliverFinalNoteNudges(t, s, orch.ID)
	at.Advance(59 * time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 3 {
		t.Fatalf("59 min after the third nudge: %d nudges, want 3", n)
	}
	at.Advance(time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 4 {
		t.Fatalf("60 min after the third nudge: %d nudges, want 4", n)
	}
	if n := notifiedCount(s, "agent.final_note"); n != 1 {
		t.Fatalf("four nudges: %d attention notifications, want 1", n)
	}
}

// A busy orchestrator on a Done root is still working, not waiting: no nudge.
func TestFinalNoteNudgeSkipsBusyOrchestrators(t *testing.T) {
	s, tm, at, orch, ses, _ := doneRootOrchestrator(t)
	showPane(tm, ses.TmuxName, paneBusyFooter)
	s.ToolStarted(ses.ID)
	at.Advance(6 * time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 0 {
		t.Fatalf("busy orchestrator: %d nudges, want 0", n)
	}
}

// Once the orchestrator writes its own completed, the grace + idle path owns
// it: no more nudges.
func TestFinalNoteNudgeStopsAfterOwnCompleted(t *testing.T) {
	s, _, at, orch, ses, _ := doneRootOrchestrator(t)
	ctx := context.Background()
	at.Advance(5 * time.Minute)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 1 {
		t.Fatalf("5 min idle: %d nudges, want 1", n)
	}
	deliverFinalNoteNudges(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "post-merge work finished"}); err != nil {
		t.Fatal(err)
	}
	at.Advance(time.Hour)
	reconcileOnce(t, s)
	if n := finalNoteNudgeCount(s, orch.ID); n != 1 {
		t.Fatalf("after its own completed: %d nudges, want 1", n)
	}
}

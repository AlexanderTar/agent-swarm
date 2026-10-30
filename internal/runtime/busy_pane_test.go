package runtime

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
)

const (
	paneBusyFooter = "✻ Thinking… (12s · esc to interrupt)\n"
	paneIdlePrompt = "─────\n❯ \n─────\n"
)

// showPane scripts what the session's pane looks like from the next capture on.
func showPane(tm *fakeTmux, name, text string) {
	tm.captures[name] = []string{text}
	tm.n[name] = 0
}

func reconcileOnce(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A kind with no hooks (muse) is busy when its pane shows the adapter's busy
// footer: the reconcile tick samples it, and the clock starts at the first sample.
func TestPaneBusyFooterReadsBusy(t *testing.T) {
	s, tm, at, sid := busySession(t, "PaneBusy")
	name := tm.panes[0].Session
	showPane(tm, name, paneBusyFooter)
	reconcileOnce(t, s)
	first, ok := s.BusySince(sid)
	if !ok {
		t.Fatal("pane shows the busy footer but the session reads idle")
	}
	at.Advance(2 * time.Minute)
	reconcileOnce(t, s)
	if since, ok := s.BusySince(sid); !ok || !since.Equal(first) {
		t.Fatalf("BusySince after a second busy sample = %v, %v; want %v (clock must not restart)", since, ok, first)
	}
	at.Advance(busyCeiling)
	reconcileOnce(t, s)
	if _, ok := s.BusyNow(sid); ok {
		t.Fatal("pane busy past the ceiling must read as stuck, not busy")
	}
}

// Back at the idle prompt, the pane evidence is gone.
func TestPaneIdlePromptClearsBusy(t *testing.T) {
	s, tm, _, sid := busySession(t, "PaneIdle")
	name := tm.panes[0].Session
	showPane(tm, name, paneBusyFooter)
	reconcileOnce(t, s)
	if _, ok := s.BusySince(sid); !ok {
		t.Fatal("setup: pane busy should read busy")
	}
	showPane(tm, name, paneIdlePrompt)
	reconcileOnce(t, s)
	if _, ok := s.BusySince(sid); ok {
		t.Fatal("pane at its idle prompt still reads busy")
	}
}

// A footer that sits next to a question or permission dialog is a worker waiting
// for a person, not working.
func TestPaneBusyWithDialogIsNotBusy(t *testing.T) {
	s, tm, _, sid := busySession(t, "PaneDialog")
	name := tm.panes[0].Session
	s.Adapters[Fake].(*adapter.Fake).PromptMatchers = []adapter.PromptMatcher{
		{Match: regexp.MustCompile(`Allow this command\?`), Title: "permission"},
	}
	showPane(tm, name, paneBusyFooter+"Allow this command? (y/n)\n")
	reconcileOnce(t, s)
	if _, ok := s.BusySince(sid); ok {
		t.Fatal("busy footer over a permission dialog reads busy")
	}
}

// Hook and leader evidence keep their own clock: a pane sample does not move it.
func TestPaneBusyDoesNotOverrideToolEvidence(t *testing.T) {
	s, tm, at, sid := busySession(t, "PaneAndTool")
	name := tm.panes[0].Session
	s.ToolStarted(sid)
	tool, _ := s.BusySince(sid)
	at.Advance(time.Minute)
	showPane(tm, name, paneBusyFooter)
	reconcileOnce(t, s)
	if since, ok := s.BusySince(sid); !ok || !since.Equal(tool) {
		t.Fatalf("BusySince = %v, %v; want the tool clock %v", since, ok, tool)
	}
	s.ToolFinished(sid)
	if _, ok := s.BusySince(sid); !ok {
		t.Fatal("tool finished but the pane still shows busy: want busy")
	}
}

// agent.undeliverable: the wake tick alone (no reconcile before it) looks at the
// pane before it calls a worker unreachable.
func TestPaneBusySuppressesUndeliverable(t *testing.T) {
	s, tm, at, _ := busySession(t, "PaneUndeliverable")
	showPane(tm, tm.panes[0].Session, paneBusyFooter)
	at.Advance(10 * time.Minute)
	if err := s.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("pane busy: %d undeliverable alerts, want 0", n)
	}
	showPane(tm, tm.panes[0].Session, paneIdlePrompt)
	if err := s.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("pane idle again: %d undeliverable alerts, want 1", n)
	}
}

func TestPaneBusySuppressesStale(t *testing.T) {
	s, tm, at, _ := busySession(t, "PaneStale")
	showPane(tm, tm.panes[0].Session, paneBusyFooter)
	at.Advance(45 * time.Minute)
	reconcileOnce(t, s)
	if n := notifiedCount(s, "agent.stale"); n != 0 {
		t.Fatalf("pane busy at 45 min: %d stale alerts, want 0", n)
	}
	at.Advance(busyCeiling)
	reconcileOnce(t, s)
	if n := notifiedCount(s, "agent.stale"); n != 1 {
		t.Fatalf("pane busy past the ceiling: %d stale alerts, want 1", n)
	}
}

// The board label follows pane busy like any other evidence: plain running until
// busyShownAfter, then busy.
func TestPaneBusyDrivesTheBoardLabel(t *testing.T) {
	s, tm, at, sid := busySession(t, "PaneLabel")
	showPane(tm, tm.panes[0].Session, paneBusyFooter)
	reconcileOnce(t, s)
	if _, ok := s.BusyShown(sid); ok {
		t.Fatal("a fresh pane-busy sample must not be shown yet")
	}
	at.Advance(busyShownAfter)
	reconcileOnce(t, s)
	if _, ok := s.BusyShown(sid); !ok {
		t.Fatal("pane busy past the threshold must be shown")
	}
}

// progress_deadlock relays a worker that went quiet after a progress checkpoint;
// a worker the daemon sees working is not quiet.
func TestBusyWorkerGetsNoProgressDeadlockRelay(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	panes(tm, Pane{Session: w.Name, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[w.Name] = map[string]string{"SWARM_SESSION": wSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	tm.captures[orch.Name] = []string{"working…\n"}
	s.Sync(ctx, wSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, w.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway"}); err != nil {
		t.Fatal(err)
	}
	relays := func() int {
		var n int
		s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
			AND payload_json LIKE '%"event":"progress_deadlock"%'`, orch.ID).Scan(&n)
		return n
	}
	// the pane reads idle between spinner frames, but a tool call is open
	s.ToolStarted(wSes.ID)
	at.Advance(6 * time.Minute)
	reconcileOnce(t, s)
	if n := relays(); n != 0 {
		t.Fatalf("busy worker: %d progress_deadlock relays, want 0", n)
	}
	s.ToolFinished(wSes.ID)
	reconcileOnce(t, s)
	if n := relays(); n != 1 {
		t.Fatalf("idle worker: %d progress_deadlock relays, want 1", n)
	}
}

func nudgeCount(s *Store, agentID string) int {
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update' AND origin = 'daemon'
		AND payload_json LIKE '%"event":"progress_nudge"%'`, agentID).Scan(&n)
	return n
}

// A worker that stays busy for progressNudgeAfter without a checkpoint is asked
// for one, once per window.
func TestBusyWorkerWithoutCheckpointGetsOneNudgePerWindow(t *testing.T) {
	s, _, at, sid := busySession(t, "NudgeMe")
	_, agentID := busyLiveRow(t, s, sid)
	s.ToolStarted(sid)

	at.Advance(progressNudgeAfter - time.Minute)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 0 {
		t.Fatalf("nudge before the window: %d, want 0", n)
	}
	at.Advance(time.Minute)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 1 {
		t.Fatalf("nudge at the window: %d, want 1", n)
	}
	reconcileOnce(t, s)
	at.Advance(progressNudgeAfter - time.Minute)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 1 {
		t.Fatalf("second tick in the same window: %d nudges, want 1", n)
	}
	mustExec(t, s.DB, `UPDATE messages SET state = 'delivered' WHERE to_agent_id = ?`, agentID)
	at.Advance(time.Minute)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 2 {
		t.Fatalf("next window: %d nudges, want 2", n)
	}
}

// A worker inside one long tool call never reaches a tool boundary, so it cannot
// ack its reminder; a new one must not pile up behind the unacked one.
func TestNudgeWaitsForThePendingOneToBeRead(t *testing.T) {
	s, _, at, sid := busySession(t, "NudgePileUp")
	_, agentID := busyLiveRow(t, s, sid)
	s.ToolStarted(sid)
	for i := 0; i < 3; i++ {
		at.Advance(progressNudgeAfter)
		reconcileOnce(t, s)
	}
	if n := nudgeCount(s, agentID); n != 1 {
		t.Fatalf("45 min in one tool call: %d nudges, want 1", n)
	}
	// the tool call ends at a boundary: the reminder is delivered and the next call starts
	mustExec(t, s.DB, `UPDATE messages SET state = 'delivered' WHERE to_agent_id = ?`, agentID)
	s.ToolFinished(sid)
	s.ToolStarted(sid)
	at.Advance(progressNudgeAfter)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 2 {
		t.Fatalf("after the first was delivered: %d nudges, want 2", n)
	}
}

// A checkpoint restarts the clock, and an idle worker is never nudged.
func TestNudgeWaitsForSilenceAndBusy(t *testing.T) {
	s, _, at, sid := busySession(t, "NudgeQuiet")
	ctx := context.Background()
	_, agentID := busyLiveRow(t, s, sid)

	at.Advance(progressNudgeAfter)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 0 {
		t.Fatalf("idle worker: %d nudges, want 0", n)
	}
	s.ToolStarted(sid)
	at.Advance(progressNudgeAfter - time.Minute)
	if _, err := s.WriteCheckpoint(ctx, sid, CheckpointInput{Kind: Progress, Summary: "halfway"}); err != nil {
		t.Fatal(err)
	}
	at.Advance(2 * time.Minute)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 0 {
		t.Fatalf("2 min after a checkpoint: %d nudges, want 0", n)
	}
}

// No reminder while the worker is being paused or its kind is out of usage.
func TestNudgeSkipsPausedAndExhausted(t *testing.T) {
	s, _, at, sid := busySession(t, "NudgePaused")
	_, agentID := busyLiveRow(t, s, sid)
	s.ToolStarted(sid)
	at.Advance(progressNudgeAfter)

	mustExec(t, s.DB, `UPDATE sessions SET state = 'pause_requested' WHERE id = ?`, sid)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 0 {
		t.Fatalf("pausing worker: %d nudges, want 0", n)
	}
	mustExec(t, s.DB, `UPDATE sessions SET state = 'running' WHERE id = ?`, sid)
	s.noteUsageLimit(Fake, at.Now().Add(time.Hour))
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 0 {
		t.Fatalf("exhausted kind: %d nudges, want 0", n)
	}
}

// An agent that went idle for hours and wakes with its first tool call starts a
// fresh working stretch: the nudge clock restarts at the wake, not at the last
// checkpoint from before the idle gap.
func TestNudgeRestartsAtTheCurrentWorkingStretch(t *testing.T) {
	s, _, at, sid := busySession(t, "NudgeWake")
	ctx := context.Background()
	_, agentID := busyLiveRow(t, s, sid)

	s.ToolStarted(sid)
	if _, err := s.WriteCheckpoint(ctx, sid, CheckpointInput{Kind: Progress, Summary: "halfway"}); err != nil {
		t.Fatal(err)
	}
	s.TurnEnded(sid)
	at.Advance(6 * time.Hour)
	s.ToolStarted(sid)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 0 {
		t.Fatalf("first tool call after 6h idle: %d nudges, want 0", n)
	}
	at.Advance(progressNudgeAfter)
	reconcileOnce(t, s)
	if n := nudgeCount(s, agentID); n != 1 {
		t.Fatalf("15 min into the new stretch: %d nudges, want 1", n)
	}
}

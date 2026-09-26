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

// TestRecoverSuccessorKickoffCarriesContinuityText pins the section-4
// wiring: a recover successor launches with the SuccessorKickoff template
// (not the fresh-assignment Kickoff). With no manifest from the
// predecessor, the broken-predecessor warning rides along: inspect first,
// never invent results.
func TestRecoverSuccessorKickoffCarriesContinuityText(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: wSes.TmuxName})
	if _, err := s.RequestReplacement(ctx, w.ID, ModeRecover, "rkick", ""); err != nil {
		t.Fatalf("request err = %v", err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	got := fa.LastSpec.Kickoff
	for _, want := range []string{
		"continuing in a fresh session after interrupted recovery",
		"Call swarm_sync first",
		"the predecessor left no usable manifest",
		"never reset/clean",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("successor kickoff missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "to get your assignment. ") {
		t.Fatalf("successor kickoff uses the fresh-assignment Kickoff:\n%s", got)
	}
}

// TestHandoffSuccessorNamesLiveChildren pins the orchestrator addition:
// a handoff leaves children running, so the orchestrator successor's
// kickoff names the still-live child and its reconcile duty -- never an
// invented checkpoint. (No manifest was saved here, so the broken
// predecessor warning rides along too.)
func TestHandoffSuccessorNamesLiveChildren(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, orchSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: orchSes.TmuxName})
	if _, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "hok", ""); err != nil {
		t.Fatalf("request err = %v", err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	got := fa.LastSpec.Kickoff
	for _, want := range []string{
		"continuing in a fresh session after handoff",
		"Your children keep running",
		w.Name,
		"or fabricate their checkpoints",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("orchestrator successor kickoff missing %q:\n%s", want, got)
		}
	}
}

// TestHandoffSuccessorWithManifestOmitsBrokenWarning pins the other half:
// when the predecessor saved (manifest recorded), the handoff successor
// carries no broken-predecessor warning.
func TestHandoffSuccessorWithManifestOmitsBrokenWarning(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: wSes.TmuxName})
	op, err := s.RequestReplacement(ctx, w.ID, ModeHandoff, "hm", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, PauseRequested); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff, Summary: "saving"}); err != nil {
		t.Fatalf("handoff checkpoint err = %v", err)
	}
	var manifestPath string
	if err := s.DB.QueryRowContext(ctx, `SELECT manifest_path FROM agent_operations WHERE id = ?`,
		op.ID).Scan(&manifestPath); err != nil || manifestPath == "" {
		t.Fatalf("manifest_path = %q, err = %v (want recorded)", manifestPath, err)
	}
	// The predecessor pane dies after saving: the handoff successor admits
	// only once the session settles into a retryable state.
	if err := s.SetSessionState(ctx, wSes.ID, Interrupted); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	got := fa.LastSpec.Kickoff
	if !strings.Contains(got, "continuing in a fresh session after handoff") {
		t.Fatalf("successor kickoff missing handoff continuity:\n%s", got)
	}
	if strings.Contains(got, "left no usable manifest") {
		t.Fatalf("successor kickoff warns of a broken predecessor despite the manifest:\n%s", got)
	}
}

// TestReviewerRecoverySuccessorCarriesVerdictReminder pins the reviewer
// addition: a recovering reviewer's kickoff re-reads the review target and
// the recorded verdict evidence before setting a verdict.
func TestReviewerRecoverySuccessorCarriesVerdictReminder(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	rev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleReviewer, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "review it"}})
	if err != nil {
		t.Fatal(err)
	}
	revSes, err := s.LatestSession(ctx, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, revSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: revSes.TmuxName})
	if _, err := s.RequestReplacement(ctx, rev.ID, ModeRecover, "rrev", ""); err != nil {
		t.Fatalf("request err = %v", err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	got := fa.LastSpec.Kickoff
	for _, want := range []string{
		"continuing in a fresh session after interrupted recovery",
		"`swarm-reviewer`",
		"Re-read the review target",
		"never passes on memory alone",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reviewer successor kickoff missing %q:\n%s", want, got)
		}
	}
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

// TestRetryDuringStoppingQueuesIntent pins the stopping race: Retry against
// a session that is still stopping must not fail or double-launch. It
// persists the intent as a durable queued recover operation (idempotent per
// request key) and returns the agent unchanged; once the session settles,
// the next resume executes the retry exactly once, delivering the note.
func TestRetryDuringStoppingQueuesIntent(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, Stopping); err != nil {
		t.Fatal(err)
	}
	out, err := s.Retry(ctx, w.Name, "retry-note", orchSes.ID, "r1")
	if err != nil {
		t.Fatalf("retry during stopping err = %v, want queued intent", err)
	}
	if out.ID != w.ID {
		t.Fatalf("retry returned %s, want canonical agent %s", out.ID, w.ID)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sessions = %d, want 1 (no launch while stopping)", n)
	}
	op, ok, err := s.PendingOperation(ctx, w.ID)
	if err != nil || !ok {
		t.Fatalf("pending op = %+v, %v; want queued recover intent", op, err)
	}
	if op.Mode != ModeRecover || op.Phase != PhaseQueued || op.Note != "retry-note" {
		t.Fatalf("intent = %+v, want queued recover with note", op)
	}
	// Same request key replays the same intent: still one row, still no session.
	if _, err := s.Retry(ctx, w.Name, "retry-note", orchSes.ID, "r1"); err != nil {
		t.Fatalf("replay err = %v", err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("operations = %d, want 1 (idempotent intent)", n)
	}
	// The session settles: the queued intent executes exactly once.
	if err := s.SetSessionState(ctx, wSes.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("sessions = %d, want 2 (one successor)", n)
	}
	succ, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.Generation != wSes.Generation+1 {
		t.Fatalf("successor generation = %d, want %d", succ.Generation, wSes.Generation+1)
	}
	var notes int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ?
		AND kind = 'assignment_update' AND payload_json LIKE '%retry-note%'`, w.ID).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != 1 {
		t.Fatalf("queued notes delivered = %d, want 1", notes)
	}
}

// TestCancelWinsOverPendingLaunch pins the Cancel side of continuity: user
// Cancel marks every in-flight operation cancelled, disables auto-restart
// and keeps the agent row -- and no later resume may launch the successor
// the cancelled operation was parking.
func TestCancelWinsOverPendingLaunch(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, Stopping); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, w.Name, "doomed retry", orchSes.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.PendingOperation(ctx, w.ID); err != nil || !ok {
		t.Fatalf("pending = %v, %v; want queued intent", ok, err)
	}
	cancelled, err := s.Cancel(ctx, w.Name, orchSes.ID, "")
	if err != nil {
		t.Fatalf("cancel err = %v", err)
	}
	if cancelled.State != AgentFinished {
		t.Fatalf("agent state = %q, want finished", cancelled.State)
	}
	if s.autoRestart(ctx, w.ID) {
		t.Fatal("auto-restart still enabled after user Cancel")
	}
	if _, ok, err := s.PendingOperation(ctx, w.ID); err != nil || ok {
		t.Fatalf("pending = %v, %v; want no in-flight operation", ok, err)
	}
	// The session settles dead: nothing may launch anymore.
	if err := s.SetSessionState(ctx, wSes.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sessions = %d, want 1 (cancelled intent never launches)", n)
	}
}

// TestSendHeldForAgentUnderOperation pins the inbox half of survival: a
// message to an agent with no live session but an in-flight operation is
// accepted into its canonical inbox (delivered to the successor), while the
// same message to an operation-less dead agent is still refused.
func TestSendHeldForAgentUnderOperation(t *testing.T) {
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
	if _, err := s.Retry(ctx, w.Name, "", orchSes.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	id, err := s.Send(ctx, orchSes.ID, w.Name, "question", "are you there?", "", "")
	if err != nil {
		t.Fatalf("send to agent under operation err = %v, want held", err)
	}
	if id == "" {
		t.Fatal("empty message id")
	}
	// (The spawn assignment message is already pending; the held send adds one.)
	n, err := s.PendingCount(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pending = %d, want 2 (assignment + held message)", n)
	}
	// Control: no operation, dead session -- still refused.
	w2, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "control"}})
	if err != nil {
		t.Fatal(err)
	}
	wSes2, err := s.LatestSession(ctx, w2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes2.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, orchSes.ID, w2.Name, "question", "are you there?", "", ""); err == nil {
		t.Fatal("send to dead agent without operation succeeded, want refusal")
	} else if !strings.Contains(err.Error(), "has no live session") {
		t.Fatalf("refusal = %v, want no-live-session", err)
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

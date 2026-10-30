package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
)

// CHORE-171 U1: on a root that goes Done, a live orchestrator gets no daemon
// completed checkpoint, its operations are not cancelled, and its pane is
// never killed. Coders still finish exactly as before.
func TestRootDoneKeepsLiveOrchestratorsWithoutDaemonCompleted(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_keep', ?, 'handoff', 'ready', 'k1', ?, 1, ?, ?)`, orch.ID, orchSes.ID, now, now); err != nil {
		t.Fatal(err)
	}
	finishRoot(t, s, "EPIC-1")

	if n := daemonCompleted(t, s, orch.ID); n != 0 {
		t.Fatalf("orchestrator got %d daemon completed checkpoints, want 0", n)
	}
	if n := daemonCompleted(t, s, w.ID); n != 1 {
		t.Fatalf("coder got %d daemon completed checkpoints, want 1", n)
	}
	var phase string
	if err := s.DB.QueryRowContext(ctx, `SELECT phase FROM agent_operations WHERE id = 'op_keep'`).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "ready" {
		t.Fatalf("orchestrator operation phase = %q, want ready (not cancelled)", phase)
	}
	if got, _ := s.Agent(ctx, orch.Name); got.State != AgentActive {
		t.Fatalf("orchestrator agent = %s, want active", got.State)
	}
	if ses, _ := s.LatestSession(ctx, orch.ID); !ses.State.Live() {
		t.Fatalf("orchestrator session = %s, want live", ses.State)
	}

	panes(tm, Pane{Session: orchSes.TmuxName, Command: "swarm-fake-agent"},
		Pane{Session: wSes.TmuxName, Command: "swarm-fake-agent"})
	tm.env[orchSes.TmuxName] = map[string]string{"SWARM_SESSION": orchSes.ID}
	tm.env[wSes.TmuxName] = map[string]string{"SWARM_SESSION": wSes.ID}
	// Spawn records an idempotent kill-before-start; only kills after this
	// point count.
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("killed inside the grace: %v", tm.killed[before:])
	}
	at.Advance(61 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tm.killed[before:], wSes.TmuxName) {
		t.Fatalf("killed = %v, want the coder pane after the grace", tm.killed[before:])
	}
	if slices.Contains(tm.killed[before:], orchSes.TmuxName) {
		t.Fatalf("killed = %v, the idle orchestrator pane must never be killed", tm.killed[before:])
	}
	// Past the 2 h busy cap the orchestrator still has no completed
	// checkpoint, so the cap cannot arm either.
	at.Advance(2 * time.Hour)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(tm.killed[before:], orchSes.TmuxName) {
		t.Fatalf("killed = %v, the orchestrator pane must survive the busy cap", tm.killed[before:])
	}
	if got, _ := s.Agent(ctx, orch.Name); got.State != AgentActive {
		t.Fatalf("orchestrator agent = %s, want active", got.State)
	}
	if ses, _ := s.LatestSession(ctx, orch.ID); !ses.State.Live() {
		t.Fatalf("orchestrator session = %s, want live", ses.State)
	}
}

// CHORE-171 U1: an orchestrator's own completed is refused while the approved
// finish still needs a finishing checkpoint, but accepted once its root is
// Done, and the session then ends on the normal 60 s grace + idle path.
func TestOrchestratorCompletedAcceptedOnceRootIsDone(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, remote_url, remote_owner, default_branch, source, created_at, updated_at)
		VALUES ('repo_proj', 'proj', '/tmp/proj', 'https://github.com/o/proj.git', 'o', 'main', 'manual', 1, 1)`)
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "bumping"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:          []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "3f9c2ab0000"}},
		Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	var reqID string
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`,
		mustItemID(t, s, key)).Scan(&reqID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE requests SET state = 'approved', agent_id = ?, responded_at = 2,
		binding_json = json_set(binding_json, '$.merge', 'custom') WHERE id = ?`, orch.ID, reqID)

	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: CompletedCkp, Summary: "all done"}); err == nil ||
		!strings.Contains(err.Error(), "finishing") {
		t.Fatalf("completed before Done: err = %v, want the finishing refusal", err)
	}
	mustExec(t, s.DB, `UPDATE items SET status = 'done' WHERE key = ?`, key)

	res, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: CompletedCkp, Summary: "post-merge work finished"})
	if err != nil {
		t.Fatalf("completed on a Done root: %v", err)
	}
	if res.CheckpointID == "" {
		t.Fatal("completed on a Done root wrote no checkpoint")
	}

	latest, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: latest.TmuxName, Command: "swarm-fake-agent"})
	tm.env[latest.TmuxName] = map[string]string{"SWARM_SESSION": latest.ID}
	// Spawn records an idempotent kill-before-start; only kills after this
	// point count.
	before := len(tm.killed)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("killed inside the grace: %v", tm.killed[before:])
	}
	at.Advance(61 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tm.killed[before:], latest.TmuxName) {
		t.Fatalf("killed = %v, want the orchestrator pane after the grace", tm.killed[before:])
	}
	panes(tm)
	at.Advance(spawnGracePeriod + time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ses2, _ := s.LatestSession(ctx, orch.ID)
	got, _ := s.Agent(ctx, orch.Name)
	if ses2.State != Completed || got.State != AgentFinished {
		t.Fatalf("orchestrator session %s / agent %s; want completed/finished", ses2.State, got.State)
	}
}

// CHORE-171 review: the required-artifact gate (an epic's plan, a bug's debug
// report) no longer applies once the root is Done, so the final note of an
// epic orchestrator without a plan is accepted instead of leaving it to be
// nudged forever.
func TestOrchestratorFinalNoteSkipsRequiredArtifactOnDoneRoot(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, orchSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "early"}); err == nil ||
		!strings.Contains(err.Error(), "registered plan") {
		t.Fatalf("completed before Done: err = %v, want the required plan refusal", err)
	}
	finishRoot(t, s, "EPIC-1")
	if _, err := s.WriteCheckpoint(ctx, orchSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "post-merge work finished"}); err != nil {
		t.Fatalf("final note on a Done epic without a plan: %v", err)
	}
}

// CHORE-171 review: an orchestrator still queued for a slot (no session yet)
// on a root that goes Done never started, so it finishes as before instead of
// launching later onto a Done root only to be nudged.
func TestRootDoneFinishesQueuedOrchestrator(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `DELETE FROM sessions WHERE agent_id = ?`, orch.ID)
	mustExec(t, s.DB, `UPDATE agents SET state = 'queued' WHERE id = ?`, orch.ID)
	finishRoot(t, s, "EPIC-1")
	if got, _ := s.Agent(ctx, orch.Name); got.State != AgentFinished {
		t.Fatalf("queued orchestrator agent = %s, want finished", got.State)
	}
}

// CHORE-171 final review: an open question of the orchestrator's own no longer
// blocks its final note once the root is Done, so it cannot be left to be
// nudged forever.
func TestOrchestratorFinalNoteSkipsOpenQuestionsOnDoneRoot(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id, prompt, state, created_at)
		VALUES ('req_open_q', 'question', 1, ?, ?, ?, 'Which way?', 'open', 1)`, orch.ID, orchSes.ID, orch.ItemID)
	finishRoot(t, s, "EPIC-1")
	if _, err := s.WriteCheckpoint(ctx, orchSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "post-merge work finished"}); err != nil {
		t.Fatalf("final note on a Done root with an open question: %v", err)
	}
}

package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// buildReviewSpec is the tdd-reviewed shape used across P9's own engine
// tests, resolved so every Loop default (max_rounds, fix, on_exhausted) is
// filled the way items.Store.CreateTx would have left it.
func buildReviewSpec(t *testing.T) workflow.Spec {
	t.Helper()
	spec, err := workflow.Resolve(workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder"},
		{ID: "review", Review: []string{"reviewer"}, Of: "build"},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// seedWorkflowTask seeds an epic/story/task tree (ready), writes spec onto
// TASK-1's workflow_json and starts an orchestrator on the epic. P9's engine
// owns CreateTx's own Resolve+role_hint path (spec B3, delivered by P7); the
// tests here only need a workflow already resolved and sitting on the item.
func seedWorkflowTask(t *testing.T, s *Store, spec workflow.Spec) (orch Agent, taskKey string) {
	t.Helper()
	ctx := context.Background()
	// The engine's own step spawns (spawnRunAgent) leave Kind/Model empty --
	// same role-default resolution any orchestrator-driven swarm_spawn
	// already goes through (Spawn's own defaulting) -- so, unlike every
	// other Spawn call in this package's tests, they can't just pass
	// Kind: Fake explicitly. newStore's fixture enables "claude" first,
	// which isn't installed on a dev Mac; narrow it to fake-only here so
	// that same defaulting path resolves to an installed adapter.
	if _, err := s.DB.ExecContext(ctx, `UPDATE settings SET value_json = '["fake"]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	s.Events.Notify()
	ep := seedEpicWithTask(t, s)
	setItemWorkflow(t, s, "TASK-1", spec)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	return orch, "TASK-1"
}

// seedOwnedRepoWorktree gives owner an active, real-git 'rw' worktree row it
// owns (StartWorkflow's own validation checks ownership against the
// worktrees table, and the review worktree the engine later creates needs a
// real repo to `git worktree add --detach` against).
func seedOwnedRepoWorktree(t *testing.T, s *Store, owner Agent) (wtID, repoID, dir, head string) {
	t.Helper()
	ctx := context.Background()
	dir = gitRepoNoSigning(t)
	commitFile(t, dir, "a.txt", "hi")
	head = strings.TrimSpace(gitOutput(t, dir, "rev-parse", "HEAD"))
	repoID = ids.New("repo")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos (id, path, name, default_branch, source, created_at, updated_at)
		VALUES (?, ?, 'proj', 'main', 'manual', 1, 1)`, repoID, dir); err != nil {
		t.Fatal(err)
	}
	wtID = seedRWWorktreeAt(t, s, repoID, owner.ID, owner.RootItemID, dir)
	return wtID, repoID, dir, head
}

// gatedBuildReviewSpec is buildReviewSpec plus a commit gate on the build
// step (unit 9.3's tests need a real, gated sha so the review step actually
// gets one to review) and a caller-chosen loop ceiling.
func gatedBuildReviewSpec(t *testing.T, maxRounds int) workflow.Spec {
	t.Helper()
	spec, err := workflow.Resolve(workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder", Gates: []workflow.Gate{workflow.GateCommit}},
		{ID: "review", Review: []string{"reviewer"}, Of: "build",
			Loop: &workflow.Loop{Fix: "build", MaxRounds: maxRounds, OnExhausted: "escalate"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func agentIDForStep(t *testing.T, s *Store, workflowID, stepID string) string {
	t.Helper()
	var id string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT agent_id FROM workflow_runs
		WHERE workflow_id = ? AND step_id = ? ORDER BY round DESC LIMIT 1`, workflowID, stepID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func agentSessionForStep(t *testing.T, s *Store, workflowID, stepID string) Session {
	t.Helper()
	ses, err := s.LatestSession(context.Background(), agentIDForStep(t, s, workflowID, stepID))
	if err != nil {
		t.Fatal(err)
	}
	return ses
}

// relayEvents is every "relay"-kind message's "event" field delivered to
// toAgentID, in seq order -- what an orchestrator's inbox actually saw.
func relayEvents(t *testing.T, s *Store, toAgentID string) []string {
	t.Helper()
	rows, err := s.DB.QueryContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' ORDER BY seq`, toAgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Event)
	}
	return out
}

func countEvent(events []string, want string) int {
	n := 0
	for _, e := range events {
		if e == want {
			n++
		}
	}
	return n
}

func TestWorkflowHappyPath(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}

	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}

	reviewerSes := agentSessionForStep(t, s, st.ID, "review")
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "lgtm",
		Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}

	var wfState string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM workflows WHERE id = ?`, st.ID).Scan(&wfState); err != nil {
		t.Fatal(err)
	}
	if wfState != "succeeded" {
		t.Fatalf("workflow state = %s, want succeeded", wfState)
	}

	item, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != items.Done {
		t.Fatalf("task status = %s, want done", item.Status)
	}

	events := relayEvents(t, s, orch.ID)
	if n := countEvent(events, "workflow_succeeded"); n != 1 {
		t.Fatalf("workflow_succeeded relays = %d (events=%v), want exactly 1", n, events)
	}
}

func TestWorkflowFixRound(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderAgentID := agentIDForStep(t, s, st.ID, "build")
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}

	oldReviewWt := ""
	if err := s.DB.QueryRowContext(ctx, `SELECT review_worktree_id FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'review'`, st.ID).Scan(&oldReviewWt); err != nil {
		t.Fatal(err)
	}
	// In the real system, by the time a reviewer finishes and requests
	// changes, reconcile's async pane-close (resolveAlive/resolveDead) has
	// already retired the builder's now-idle session to 'completed' --
	// RetryFix's own Retry() call requires exactly that (retryableStates).
	// Simulate that elapsed time directly rather than pull reconcile's
	// whole polling loop into this synchronous test.
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, coderAgentID); err != nil {
		t.Fatal(err)
	}
	reviewerSes := agentSessionForStep(t, s, st.ID, "review")
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "needs work",
		Verdict:  "changes_requested",
		Findings: []workflow.Finding{{Severity: "major", File: "a.go", Line: 3, Summary: "off by one"}}}); err != nil {
		t.Fatal(err)
	}

	var round int
	if err := s.DB.QueryRowContext(ctx, `SELECT round FROM workflows WHERE id = ?`, st.ID).Scan(&round); err != nil {
		t.Fatal(err)
	}
	if round != 2 {
		t.Fatalf("round = %d, want 2", round)
	}
	item, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != items.InProgress {
		t.Fatalf("task status = %s, want in_progress", item.Status)
	}

	var round2Agent, round2State string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(agent_id, ''), state FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'build' AND round = 2`, st.ID).Scan(&round2Agent, &round2State); err != nil {
		t.Fatal(err)
	}
	if round2Agent != coderAgentID || round2State != "active" {
		t.Fatalf("round 2 build run = agent %s state %s, want %s/active", round2Agent, round2State, coderAgentID)
	}

	var noteCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE to_agent_id = ? AND kind = 'assignment_update' AND payload_json LIKE '%off by one%'`, coderAgentID).
		Scan(&noteCount); err != nil {
		t.Fatal(err)
	}
	if noteCount != 1 {
		t.Fatalf("assignment_update carrying the finding = %d, want 1", noteCount)
	}

	var oldWtState string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM worktrees WHERE id = ?`, oldReviewWt).Scan(&oldWtState); err != nil {
		t.Fatal(err)
	}
	if oldWtState == "active" {
		t.Fatalf("finished round's review worktree still active, want removed/retained")
	}
}

func TestWorkflowEscalatesWhenRoundsExhausted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 1))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}
	reviewerSes := agentSessionForStep(t, s, st.ID, "review")
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "nope",
		Verdict:  "changes_requested",
		Findings: []workflow.Finding{{Severity: "major", File: "a.go", Summary: "bug"}}}); err != nil {
		t.Fatal(err)
	}

	var wfState, escalation string
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(escalation, '') FROM workflows WHERE id = ?`, st.ID).
		Scan(&wfState, &escalation); err != nil {
		t.Fatal(err)
	}
	if wfState != "escalated" {
		t.Fatalf("workflow state = %s, want escalated", wfState)
	}
	want := "review rounds exhausted (1/1)"
	if escalation != want {
		t.Fatalf("escalation = %q, want %q", escalation, want)
	}

	events := relayEvents(t, s, orch.ID)
	if n := countEvent(events, "workflow_escalated"); n != 1 {
		t.Fatalf("workflow_escalated relays = %d (events=%v), want exactly 1", n, events)
	}

	n := notified(t, s, "workflow.escalated")
	if n.Args["KEY"] != taskKey || n.Args["reason"] != want {
		t.Fatalf("workflow.escalated notify Args = %v, want KEY=%s reason=%q", n.Args, taskKey, want)
	}
}

func TestWorkflowAutoRetryOnCrash(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderAgentID := agentIDForStep(t, s, st.ID, "build")
	coderSes := agentSessionForStep(t, s, st.ID, "build")

	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: FailedCkp, Summary: "crashed"}); err != nil {
		t.Fatal(err)
	}

	var state, agentID string
	var autoRetries int
	if err := s.DB.QueryRowContext(ctx, `SELECT state, auto_retries, COALESCE(agent_id, '')
		FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&state, &autoRetries, &agentID); err != nil {
		t.Fatal(err)
	}
	if state != "active" || autoRetries != 1 || agentID != coderAgentID {
		t.Fatalf("build run after auto-retry = state %s retries %d agent %s, want active/1/%s",
			state, autoRetries, agentID, coderAgentID)
	}
	newSes, err := s.LatestSession(ctx, coderAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if newSes.ID == coderSes.ID {
		t.Fatalf("expected Retry to start a new session, got the same one back")
	}
}

// TestWorkflowRelaysSuppressed exercises spec B4's Relays rule end to end: a
// workflow agent's accepted/progress/completed checkpoints never reach the
// orchestrator's inbox as raw relays (the engine owns those), but blocked
// still does, alongside the engine's own workflow_succeeded.
func TestWorkflowRelaysSuppressed(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: Progress, Summary: "midway"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: BlockedCkp, Summary: "stuck",
		Blockers: []string{"need a decision"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: Progress, Summary: "unstuck"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}
	reviewerSes := agentSessionForStep(t, s, st.ID, "review")
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "lgtm",
		Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}

	events := relayEvents(t, s, orch.ID)
	want := []string{"blocked", "workflow_succeeded"}
	if len(events) != len(want) {
		t.Fatalf("relay events = %v, want %v", events, want)
	}
	for i, e := range want {
		if events[i] != e {
			t.Fatalf("relay events = %v, want %v", events, want)
		}
	}
}

func TestStartWorkflowValidates(t *testing.T) {
	ctx := context.Background()

	t.Run("no workflow", func(t *testing.T) {
		s, _, _ := newStore(t)
		ep := seedEpicWithTask(t, s)
		orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "TASK-1"})
		if err == nil || err.Error() != "TASK-1 has no workflow." {
			t.Fatalf("err = %v, want \"TASK-1 has no workflow.\"", err)
		}
	})

	t.Run("already running", func(t *testing.T) {
		s, _, _ := newStore(t)
		orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
		wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
		in := StartWorkflowInput{ItemKey: taskKey, Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}}
		if _, err := s.StartWorkflow(ctx, orch, in); err != nil {
			t.Fatal(err)
		}
		_, err := s.StartWorkflow(ctx, orch, in)
		if err == nil || err.Error() != "TASK-1 already has a running workflow." {
			t.Fatalf("err = %v, want \"TASK-1 already has a running workflow.\"", err)
		}
	})

	t.Run("no rw worktree", func(t *testing.T) {
		s, _, _ := newStore(t)
		orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
		_, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey})
		if err == nil || err.Error() != "Start needs a read-write worktree you own." {
			t.Fatalf("err = %v, want \"Start needs a read-write worktree you own.\"", err)
		}
	})

	t.Run("dependencies open", func(t *testing.T) {
		s, _, _ := newStore(t)
		orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
		task, err := s.Items.Get(ctx, taskKey)
		if err != nil {
			t.Fatal(err)
		}
		blocker, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: task.ParentKey,
			Title: "blocker"}, items.User("board"))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Items.AddDep(ctx, taskKey, blocker.Key, items.User("board")); err != nil {
			t.Fatal(err)
		}
		wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
		_, err = s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
			Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
		want := "dependencies_open: " + blocker.Key
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
}

func TestWorkflowSpawnsBuilderWithSharedWorktree(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, dir, _ := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "running" || st.Round != 1 {
		t.Fatalf("state = %+v, want running round 1", st)
	}

	var role, state, agentID string
	if err := s.DB.QueryRowContext(ctx, `SELECT role, state, COALESCE(agent_id, '') FROM workflow_runs WHERE step_id = 'build'`).
		Scan(&role, &state, &agentID); err != nil {
		t.Fatal(err)
	}
	if role != "coder" || state != "active" || agentID == "" {
		t.Fatalf("build run = role %q state %q agent %q, want coder/active/non-empty", role, state, agentID)
	}

	var shared int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktree_reservations
		WHERE worktree_id = ? AND agent_id = ? AND mode = 'rw' AND released_at IS NULL`, wtID, agentID).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if shared != 1 {
		t.Fatalf("rw worktree share count = %d, want 1", shared)
	}

	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Role != RoleCoder || a.ParentAgentID != orch.ID {
		t.Fatalf("builder agent = role %s parent %s, want coder/%s", a.Role, a.ParentAgentID, orch.ID)
	}
	if !strings.Contains(a.Brief, dir) {
		t.Errorf("brief missing the shared worktree's path header:\n%s", a.Brief)
	}
	if !strings.Contains(a.Brief, "## Workflow") || !strings.Contains(a.Brief, `step "build"`) {
		t.Errorf("brief missing its Workflow section:\n%s", a.Brief)
	}
}

func TestWorkflowSpawnsReviewersOnReviewWorktree(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	// The build step "completed" with sha=head (bypassing the checkpoint
	// flow, which isn't wired to advance until unit 9.3): this unit only
	// tests the Spawn half of advance.
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'completed', sha = ?
		WHERE workflow_id = ? AND step_id = 'build'`, head, st.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.advance(ctx, st.ID); err != nil {
		t.Fatal(err)
	}

	var role, state, agentID, reviewWt string
	if err := s.DB.QueryRowContext(ctx, `SELECT role, state, COALESCE(agent_id, ''), COALESCE(review_worktree_id, '')
		FROM workflow_runs WHERE step_id = 'review'`).Scan(&role, &state, &agentID, &reviewWt); err != nil {
		t.Fatal(err)
	}
	if role != "reviewer" || state != "active" || agentID == "" || reviewWt == "" {
		t.Fatalf("review run = role %q state %q agent %q reviewWt %q", role, state, agentID, reviewWt)
	}

	var detached, wtState string
	if err := s.DB.QueryRowContext(ctx, `SELECT detached_sha, state FROM worktrees WHERE id = ?`, reviewWt).
		Scan(&detached, &wtState); err != nil {
		t.Fatal(err)
	}
	if detached != head || wtState != "active" {
		t.Fatalf("review worktree detached_sha=%q state=%q, want %q/active", detached, wtState, head)
	}

	var roShared int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktree_reservations
		WHERE worktree_id = ? AND agent_id = ? AND mode = 'ro' AND released_at IS NULL`, reviewWt, agentID).Scan(&roShared); err != nil {
		t.Fatal(err)
	}
	if roShared != 1 {
		t.Fatalf("ro worktree share count = %d, want 1", roShared)
	}
}

func TestWorkflowParallelReviewers(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	spec, err := workflow.Resolve(workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder"},
		{ID: "review", Review: []string{"reviewer", "ui_reviewer"}, Of: "build"},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	orch, taskKey := seedWorkflowTask(t, s, spec)
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'completed', sha = ?
		WHERE workflow_id = ? AND step_id = 'build'`, head, st.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.advance(ctx, st.ID); err != nil {
		t.Fatal(err)
	}

	rows, err := s.DB.QueryContext(ctx, `SELECT role, state, COALESCE(review_worktree_id, '')
		FROM workflow_runs WHERE step_id = 'review' ORDER BY role`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var roles []string
	var reviewWts = map[string]bool{}
	for rows.Next() {
		var role, state, wt string
		if err := rows.Scan(&role, &state, &wt); err != nil {
			t.Fatal(err)
		}
		if state != "active" || wt == "" {
			t.Fatalf("role %s: state=%s wt=%q, want active/non-empty", role, state, wt)
		}
		roles = append(roles, role)
		reviewWts[wt] = true
	}
	if len(roles) != 2 || roles[0] != "reviewer" || roles[1] != "ui_reviewer" {
		t.Fatalf("roles = %v, want [reviewer ui_reviewer]", roles)
	}
	if len(reviewWts) != 1 {
		t.Fatalf("review worktree ids = %v, want exactly one shared worktree", reviewWts)
	}

	var wtCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktrees WHERE detached_sha = ?`, head).Scan(&wtCount); err != nil {
		t.Fatal(err)
	}
	if wtCount != 1 {
		t.Fatalf("worktrees at %s = %d, want exactly 1 (shared, not duplicated per reviewer)", head, wtCount)
	}
}

func TestAdvanceIsIdempotent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}

	countRuns := func() int {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs WHERE workflow_id = ?`, st.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	countAgents := func() int {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE item_id = (SELECT item_id FROM workflows WHERE id = ?)`, st.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	runsBefore, agentsBefore := countRuns(), countAgents()
	if runsBefore == 0 || agentsBefore == 0 {
		t.Fatalf("setup: runs=%d agents=%d, want both non-zero before the idempotency check", runsBefore, agentsBefore)
	}

	// A repeated advance (daemon restart, duplicate trigger) must be a
	// complete no-op: same run count, same agent count, same build agent id.
	var before string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(agent_id, '') FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.advance(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.advance(ctx, st.ID); err != nil {
		t.Fatal(err)
	}

	if got := countRuns(); got != runsBefore {
		t.Fatalf("run count after repeated advance = %d, want %d (unchanged)", got, runsBefore)
	}
	if got := countAgents(); got != agentsBefore {
		t.Fatalf("agent count after repeated advance = %d, want %d (no double-spawn)", got, agentsBefore)
	}
	var after string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(agent_id, '') FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("build run's agent id changed: %q -> %q", before, after)
	}
}

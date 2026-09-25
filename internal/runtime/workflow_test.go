package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
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

// setMaxSubagents sets max_concurrent_subagents directly (mirrors
// limits_test.go's setLimits for the shared max_concurrent_agents pool).
func setMaxSubagents(t *testing.T, s *Store, n int) {
	t.Helper()
	now := s.now().UnixMilli()
	if _, err := s.DB.ExecContext(context.Background(), `INSERT INTO settings (key, value_json, updated_at)
		VALUES ('max_concurrent_subagents', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		fmt.Sprintf("%d", n), now); err != nil {
		t.Fatal(err)
	}
	s.Events.Notify()
}

// TestCheckpointTriggersAdvance is unit 9.4's dedicated pin for spec B4's
// first trigger: WriteCheckpoint itself, with no direct s.advance() call
// anywhere in this test, must move a finished build step straight into a
// spawned review step.
func TestCheckpointTriggersAdvance(t *testing.T) {
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

	var role, state, agentID string
	if err := s.DB.QueryRowContext(ctx, `SELECT role, state, COALESCE(agent_id, '')
		FROM workflow_runs WHERE workflow_id = ? AND step_id = 'review'`, st.ID).Scan(&role, &state, &agentID); err != nil {
		t.Fatal(err)
	}
	if role != "reviewer" || state != "active" || agentID == "" {
		t.Fatalf("review run = role %q state %q agent %q, want reviewer/active/non-empty -- "+
			"WriteCheckpoint's own post-commit trigger should have spawned it", role, state, agentID)
	}
}

// TestCrashTriggersAdvance is spec B4's second trigger: a session dying
// with NO checkpoint at all (a genuine crash, via reconcile) marks the
// workflow run failed and advances its workflow, same as an explicit
// FailedCkp would -- here landing on AutoRetry (default retries: 1) since
// nothing has exhausted it yet.
func TestCrashTriggersAdvance(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderAgentID := agentIDForStep(t, s, st.ID, "build")
	coder, err := s.agentByID(ctx, coderAgentID)
	if err != nil {
		t.Fatal(err)
	}
	coderSes := agentSessionForStep(t, s, st.ID, "build")

	panes(tm, Pane{Session: coder.Name, Dead: true, DeadStatus: 1, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[coder.Name] = map[string]string{"SWARM_SESSION": coderSes.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	var state, agentID string
	var autoRetries int
	if err := s.DB.QueryRowContext(ctx, `SELECT state, auto_retries, COALESCE(agent_id, '')
		FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&state, &autoRetries, &agentID); err != nil {
		t.Fatal(err)
	}
	if state != "active" || autoRetries != 1 || agentID != coderAgentID {
		t.Fatalf("build run after crash = state %s retries %d agent %s, want active/1/%s",
			state, autoRetries, agentID, coderAgentID)
	}
	newSes, err := s.LatestSession(ctx, coderAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if newSes.ID == coderSes.ID {
		t.Fatalf("expected the crash to start a new session via Retry, got the same one back")
	}
}

// TestSlotReleaseSpawnsWaitingRun is spec B4's third trigger: any child of
// the owner finishing frees a subagent slot that lets a SIBLING workflow's
// budget-blocked run start. TASK-1's build step has Retries:0, so its crash
// escalates straight away instead of re-occupying the slot with an
// auto-retry -- the freed slot must go to TASK-2's still-waiting build run.
func TestSlotReleaseSpawnsWaitingRun(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE settings SET value_json = '["fake"]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	setMaxSubagents(t, s, 1)

	ep := seedEpicWithTwoTasks(t, s)
	zero := 0
	spec1, err := workflow.Resolve(workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder"},
		{ID: "review", Review: []string{"reviewer"}, Of: "build"},
	}, Retries: &zero}, false)
	if err != nil {
		t.Fatal(err)
	}
	setItemWorkflow(t, s, "TASK-1", spec1)
	setItemWorkflow(t, s, "TASK-2", buildReviewSpec(t))

	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	wt1, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	wt2, _, _, _ := seedOwnedRepoWorktree(t, s, orch)

	st1, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "TASK-1",
		Worktrees: []WorkflowWorktree{{WorktreeID: wt1, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: "TASK-2",
		Worktrees: []WorkflowWorktree{{WorktreeID: wt2, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}

	var state, agentID string
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(agent_id, '')
		FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st2.ID).Scan(&state, &agentID); err != nil {
		t.Fatal(err)
	}
	if state != "waiting" || agentID != "" {
		t.Fatalf("TASK-2 build run = state %s agent %q, want waiting/empty (TASK-1 already holds the one slot)", state, agentID)
	}

	coder1AgentID := agentIDForStep(t, s, st1.ID, "build")
	coder1, err := s.agentByID(ctx, coder1AgentID)
	if err != nil {
		t.Fatal(err)
	}
	coder1Ses := agentSessionForStep(t, s, st1.ID, "build")

	panes(tm, Pane{Session: coder1.Name, Dead: true, DeadStatus: 1, Command: "swarm-fake-agent"},
		Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[coder1.Name] = map[string]string{"SWARM_SESSION": coder1Ses.ID}
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	var wf1State string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM workflows WHERE id = ?`, st1.ID).Scan(&wf1State); err != nil {
		t.Fatal(err)
	}
	if wf1State != "escalated" {
		t.Fatalf("TASK-1 workflow state = %s, want escalated", wf1State)
	}

	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(agent_id, '')
		FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st2.ID).Scan(&state, &agentID); err != nil {
		t.Fatal(err)
	}
	if state != "active" || agentID == "" {
		t.Fatalf("TASK-2 build run after the slot freed = state %s agent %q, want active/non-empty", state, agentID)
	}
}

// TestRecoverStalledWorkflow is spec B4's fourth trigger: a workflow whose
// runs are all already terminal, but whose workflows row hasn't moved in
// over 30s, is a daemon-restart gap (a checkpoint committed, but the
// advance() that should have followed it never ran) -- the reconcile
// loop's stall scan must recover it on its own, with no checkpoint or
// direct s.advance() call in this test at all.
func TestRecoverStalledWorkflow(t *testing.T) {
	s, tm, clk := clockStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)

	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderAgentID := agentIDForStep(t, s, st.ID, "build")

	// Hand-seed both steps already finished, entirely via direct SQL --
	// never through WriteCheckpoint or s.advance -- simulating a daemon
	// that crashed right after the reviewer's own commit, before the
	// advance() call that should have followed it.
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'completed', sha = ?
		WHERE workflow_id = ? AND step_id = 'build'`, head, st.ID); err != nil {
		t.Fatal(err)
	}
	reviewer, _, err := s.Spawn(ctx, SpawnInput{ItemKey: taskKey, Role: RoleReviewer, Kind: Fake, Model: "fake-1",
		ParentAgentID: orch.ID, Brief: BriefInput{Objective: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, agent_id, state, verdict, sha, created_at)
		VALUES (?, ?, 'review', 1, 'reviewer', ?, 'completed', 'pass', ?, ?)`,
		ids.New("wfr"), st.ID, reviewer.ID, head, db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}
	// Neither agent's session ever got a real checkpoint (bypassed above):
	// retire both sessions directly so reconcile's per-session dead-pane
	// scan leaves them alone and only the stall scan below touches this
	// workflow.
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed'
		WHERE agent_id IN (?, ?)`, coderAgentID, reviewer.ID); err != nil {
		t.Fatal(err)
	}
	clk.Advance(31 * time.Second)
	if _, err := s.DB.ExecContext(ctx, `UPDATE workflows SET updated_at = ?
		WHERE id = ?`, db.Millis(s.Now().Add(-31*time.Second)), st.ID); err != nil {
		t.Fatal(err)
	}

	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": mustSessionID(t, s, orch.ID)}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	var wfState string
	if err := s.DB.QueryRowContext(ctx, `SELECT state FROM workflows WHERE id = ?`, st.ID).Scan(&wfState); err != nil {
		t.Fatal(err)
	}
	if wfState != "succeeded" {
		t.Fatalf("workflow state = %s, want succeeded (recovered by the stall scan)", wfState)
	}
	item, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != items.Done {
		t.Fatalf("task status = %s, want done", item.Status)
	}
}

// TestOrchestratorCannotMarkWorkflowTaskDone is spec B5's Done rule: an
// orchestrator's own swarm_items-style transition attempt is refused, no
// workflows row required -- it.Workflow != nil alone is enough to gate it.
func TestOrchestratorCannotMarkWorkflowTaskDone(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	_, err := s.Items.Transition(ctx, taskKey, items.Done, items.Orchestrator(orch.ID, orch.RootItemID))
	want := fmt.Sprintf("%s is finished by its workflow. It moves to Done when the workflow succeeds; "+
		"use swarm_workflow resume to accept or fail it.", taskKey)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// escalateViaRoundsExhausted drives a workflow through build -> completed,
// review -> changes_requested with a one-round loop, landing it escalated
// (spec B4 "rounds exhausted"), and returns the coder's agent id for
// callers that need it.
func escalateViaRoundsExhausted(t *testing.T, s *Store, orch Agent, taskKey string, wfID string, head string) (coderAgentID string) {
	t.Helper()
	ctx := context.Background()
	coderAgentID = agentIDForStep(t, s, wfID, "build")
	coderSes := agentSessionForStep(t, s, wfID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}
	// Same real-elapsed-time simulation TestWorkflowFixRound uses: by the
	// time a reviewer resumes/retries the builder, reconcile has already
	// retired its now-idle session.
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, coderAgentID); err != nil {
		t.Fatal(err)
	}
	reviewerSes := agentSessionForStep(t, s, wfID, "review")
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "nope",
		Verdict:  "changes_requested",
		Findings: []workflow.Finding{{Severity: "major", File: "a.go", Summary: "off by one"}}}); err != nil {
		t.Fatal(err)
	}
	return coderAgentID
}

func TestResumeAccept(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 1))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	escalateViaRoundsExhausted(t, s, orch, taskKey, st.ID, head)

	escalated, ok, err := s.WorkflowFor(ctx, taskKey)
	if err != nil || !ok || escalated.State != "escalated" {
		t.Fatalf("setup: state = %+v ok=%v err=%v, want escalated", escalated, ok, err)
	}

	final, err := s.ResumeWorkflow(ctx, orch, taskKey, "accept", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "succeeded" {
		t.Fatalf("workflow state = %s, want succeeded", final.State)
	}
	item, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != items.Done {
		t.Fatalf("task status = %s, want done", item.Status)
	}
}

func TestResumeFail(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 1))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	escalateViaRoundsExhausted(t, s, orch, taskKey, st.ID, head)

	final, err := s.ResumeWorkflow(ctx, orch, taskKey, "fail", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "failed" {
		t.Fatalf("workflow state = %s, want failed", final.State)
	}
	item, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != items.Ready {
		t.Fatalf("task status = %s, want ready", item.Status)
	}
}

func TestResumeRefusedWhenNotEscalated(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, buildReviewSpec(t))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	if _, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ResumeWorkflow(ctx, orch, taskKey, "retry", "", "")
	want := fmt.Sprintf("%s's workflow isn't waiting on you (state: running).", taskKey)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestResumeRetryGrantsExtraRound(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 1))
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	coderAgentID := escalateViaRoundsExhausted(t, s, orch, taskKey, st.ID, head)

	final, err := s.ResumeWorkflow(ctx, orch, taskKey, "retry", "please double check the edge case", "")
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "running" {
		t.Fatalf("workflow state = %s, want running", final.State)
	}
	if final.ExtraRounds != 1 {
		t.Fatalf("extra_rounds = %d, want 1", final.ExtraRounds)
	}
	if final.Round != 2 {
		t.Fatalf("round = %d, want 2 (RetryFix's own bump, extra_rounds alone made room for it)", final.Round)
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
		WHERE to_agent_id = ? AND kind = 'assignment_update' AND payload_json LIKE '%double check the edge case%'`,
		coderAgentID).Scan(&noteCount); err != nil {
		t.Fatal(err)
	}
	if noteCount != 1 {
		t.Fatalf("orchestrator's resume note delivered = %d, want 1", noteCount)
	}
}

func TestCancelWorkflow(t *testing.T) {
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

	final, err := s.CancelWorkflow(ctx, orch, taskKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "cancelled" {
		t.Fatalf("workflow state = %s, want cancelled", final.State)
	}
	item, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != items.Ready {
		t.Fatalf("task status = %s, want ready", item.Status)
	}
	coder, err := s.agentByID(ctx, coderAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if coder.State != AgentFinished {
		t.Fatalf("builder agent state = %s, want finished", coder.State)
	}
}

// --- Fix round 1 ---

// TestDesignThenBuildStatusFlow is the interaction flagged in the P9 brief
// (from P8's own review): completedCurrent counts a designer's completion
// the same as a coder's, so a design step's CompletedCkp can flip the task
// to InReview before the build step has even started -- applySpawn's
// markInProgress (spec B4: "the task moves InReview <-> InProgress") must
// bring it back to InProgress once build actually spawns. It also exercises
// two fix-round-1 fixes together: a review step over a step with no commit
// gate (design, gated on artifact:design instead) must spawn with no sha to
// check a worktree out at (finding 3), and the build step's brief must see
// the registered design artifact's path in its Context (finding 4, spec
// B6).
func TestDesignThenBuildStatusFlow(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	spec, err := workflow.Resolve(workflow.Spec{Steps: []workflow.Step{
		{ID: "design", Run: "designer", Gates: []workflow.Gate{workflow.GateArtifactDesign}},
		{ID: "review-design", Review: []string{"ui_reviewer"}, Of: "design"},
		{ID: "build", Run: "coder", Gates: []workflow.Gate{workflow.GateCommit}},
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

	task, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	designDir := filepath.Join(s.Home, "designs", task.RootKey)
	if err := os.MkdirAll(designDir, 0o755); err != nil {
		t.Fatal(err)
	}
	designPath := filepath.Join(designDir, "flow.md")
	if err := os.WriteFile(designPath, []byte("# Design\n\nDo the thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	designerSes := agentSessionForStep(t, s, st.ID, "design")
	if _, err := s.WriteCheckpoint(ctx, designerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "designed",
		Artifacts: []string{designPath}}); err != nil {
		t.Fatal(err)
	}

	// The design step's own completion (a build role, per completedCurrent)
	// already flips the task to InReview -- before build has even spawned.
	afterDesign, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if afterDesign.Status != items.InReview {
		t.Fatalf("status after design completes = %s, want in_review", afterDesign.Status)
	}

	// The design review spawned with no sha (design has no commit gate) --
	// finding 3: this must not have errored advance() out before it could
	// reach fillWaitingRuns.
	var reviewRole, reviewState, reviewAgent string
	if err := s.DB.QueryRowContext(ctx, `SELECT role, state, COALESCE(agent_id, '')
		FROM workflow_runs WHERE workflow_id = ? AND step_id = 'review-design'`, st.ID).
		Scan(&reviewRole, &reviewState, &reviewAgent); err != nil {
		t.Fatal(err)
	}
	if reviewRole != "ui_reviewer" || reviewState != "active" || reviewAgent == "" {
		t.Fatalf("review-design run = role %q state %q agent %q, want ui_reviewer/active/non-empty", reviewRole, reviewState, reviewAgent)
	}

	reviewerSes := agentSessionForStep(t, s, st.ID, "review-design")
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "looks good",
		Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}

	// Build just spawned: markInProgress must have brought the task back.
	afterBuildSpawn, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	if afterBuildSpawn.Status != items.InProgress {
		t.Fatalf("status after build spawns = %s, want in_progress", afterBuildSpawn.Status)
	}
	var buildAgent string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&buildAgent); err != nil {
		t.Fatal(err)
	}
	if buildAgent == "" {
		t.Fatal("build run has no agent")
	}
	builder, err := s.agentByID(ctx, buildAgent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(builder.Brief, designPath) {
		t.Errorf("builder's brief missing the design artifact's path in Context:\n%s", builder.Brief)
	}

	// Finish the loop so the fixture doesn't leak a dangling test assertion
	// about a workflow that never resolves.
	if _, err := s.WriteCheckpoint(ctx, agentSessionForStep(t, s, st.ID, "build").ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "done", Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: false}}}); err != nil {
		t.Fatal(err)
	}
}

// TestResumeRetryAfterBlocked is fix round 1, finding 2: a blocked verdict
// escalates unconditionally in internal/workflow.Next (no round/budget
// check at all -- unlike changes_requested), so extra_rounds alone can
// never un-escalate it. resumeBumpsRound must bump the round for a blocked
// escalation too, landing on a fresh Spawn at the new round (matching
// internal/workflow/next_test.go's own blocked-resume cases).
func TestResumeRetryAfterBlocked(t *testing.T) {
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
	if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "blocked on a decision",
		Verdict: "blocked"}); err != nil {
		t.Fatal(err)
	}

	escalated, ok, err := s.WorkflowFor(ctx, taskKey)
	if err != nil || !ok || escalated.State != "escalated" {
		t.Fatalf("setup: state = %+v ok=%v err=%v, want escalated", escalated, ok, err)
	}

	final, err := s.ResumeWorkflow(ctx, orch, taskKey, "retry", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "running" {
		t.Fatalf("workflow state = %s, want running (not re-escalated)", final.State)
	}
	if final.Round != 2 {
		t.Fatalf("round = %d, want 2", final.Round)
	}
	if final.ExtraRounds != 1 {
		t.Fatalf("extra_rounds = %d, want 1", final.ExtraRounds)
	}

	var round2State, round2Agent string
	if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = 'build' AND round = 2`, st.ID).Scan(&round2State, &round2Agent); err != nil {
		t.Fatal(err)
	}
	if round2State != "active" || round2Agent == "" {
		t.Fatalf("round 2 build run = state %s agent %q, want active/non-empty (a fresh spawn)", round2State, round2Agent)
	}
}

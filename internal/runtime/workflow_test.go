package runtime

import (
	"context"
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

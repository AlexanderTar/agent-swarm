package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// panes sets the fake tmux's pane list for a test (Task 21; Task 22's wake
// tests use it too).
func panes(tm *fakeTmux, p ...Pane) { tm.panes = p }

// sweptCount is how many worktrees the sweep has finished with. `removed` is the
// happy path and `retained` is a dirty or unmerged tree the sweep deliberately
// kept (§12.2), so both count as swept — the question these tests ask is whether
// the sweep ran at all, not whether git let it delete.
func sweptCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM worktrees WHERE state IN ('removed', 'retained')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedSectionApproval writes a spike, an artifact with one section and an open
// approve_section request, without needing Task 18's registrar. The sha256 is the
// real hash of the section body, because Approve compares it and a placeholder
// would make every approval a conflict.
func seedSectionApproval(t *testing.T, s *Store) (Request, Session, Artifact) {
	t.Helper()
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Design the flow",
		Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	const body = "The flow is three screens.\n"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	artID, now := ids.New("art"), db.Millis(s.Now())
	sections := fmt.Sprintf(`[{"id":"overview","heading":"Overview","sha256":%q,"approval_state":"none"}]`, sum)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifacts
		(id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES (?, ?, 'spec', 'docs/specs/flow.md', 1, ?, ?)`, artID, it.ID, a.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifact_revisions
		(artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES (?, 1, ?, ?, ?, ?)`, artID, sum, "## Overview\n"+body, sections, now); err != nil {
		t.Fatal(err)
	}
	reqID := ids.New("req")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, agent_id, session_id, item_id, artifact_id, section_id, section_sha256,
		 prompt, state, artifact_revision, created_at)
		VALUES (?, 'approve_section', ?, ?, ?, ?, 'overview', ?, 'Approve the overview.', 'open', 1, ?)`,
		reqID, a.ID, ses.ID, it.ID, artID, sum, now); err != nil {
		t.Fatal(err)
	}
	// The real flow reaches awaiting_approval through Ask + ReconcileTx; this
	// helper writes the request directly (Task 18's registrar doesn't exist yet),
	// so it forces the same status by hand.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'awaiting_approval', updated_at = ?
		WHERE id = ?`, now, it.ID); err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestByID(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	// There is no ArtifactByID in the ledger and this task does not add one: the
	// helper already knows every value it just wrote, so it fills the struct.
	art := Artifact{ID: artID, ItemID: it.ID, Kind: "spec", Path: "docs/specs/flow.md",
		HeadRevision: 1, Revision: 1,
		Sections: []ArtifactSection{{ID: "overview", Title: "Overview", SHA256: sum}}}
	return req, ses, art
}

func seedEpicWithTask(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	// `ready` directly, not through Transition: the daemon's Ready->InProgress hop
	// needs an accepted checkpoint, which these tests are not about.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?)`,
		ep.ID, story.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

func seedEpicWithTwoTasks(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t1, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Implement the fix"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?, ?)`,
		ep.ID, story.ID, t1.ID, t2.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

func seedEpicWithThreeTasks(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t1, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Implement the fix"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t3, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Refactor"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?, ?, ?)`,
		ep.ID, story.ID, t1.ID, t2.ID, t3.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

// startSessionForTest exposes the unexported startSession to other test files
// in the package (Task 15: TDD evidence survives a pause into a new generation).
func (s *Store) startSessionForTest(ctx context.Context, a Agent, attempt, generation int) (Session, error) {
	return s.startSession(ctx, a, attempt, generation, false, "")
}

// seedRepo inserts a repos row pointing at a temp git repo and returns its id.
func seedRepo(t *testing.T, s *Store, name string) string {
	t.Helper()
	ctx := context.Background()
	id, now := ids.New("repo"), db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos
		(id, path, name, default_branch, source, created_at, updated_at)
		VALUES (?, ?, ?, 'main', 'manual', ?, ?)`,
		id, gitRepoNoSigning(t), name, now, now); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedWorktreeReservation gives repoID one active worktree with an unreleased
// reservation held by agentID, which is what blocks dropping that repo.
// root_item_id and owner_agent_id are real foreign keys, so both must exist.
func seedWorktreeReservation(t *testing.T, s *Store, repoID, agentID, rootItemID string) string {
	t.Helper()
	ctx := context.Background()
	wtID, now := ids.New("wt"), db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktrees
		(id, repo_id, path, branch, base_ref, base_sha, owner_agent_id, root_item_id, state, created_at)
		VALUES (?, ?, ?, 'task/x', 'main', 'abc1234', ?, ?, 'active', ?)`,
		wtID, repoID, t.TempDir(), agentID, rootItemID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktree_reservations
		(worktree_id, agent_id, mode, created_at) VALUES (?, ?, 'rw', ?)`, wtID, agentID, now); err != nil {
		t.Fatal(err)
	}
	return wtID
}

// planBody is a plan artifact with one valid swarm-tree block (I10). Tasks 18
// and 19 both parse this same tree, so it lives here rather than being copied.
const planBody = "# Plan\n\n## Work breakdown\n\n" +
	"```swarm-tree\n" +
	`{"root":{"type":"epic","title":"Ship auth","brief":"","acceptance":["It works."]},
 "children":[{"ref":"s1","type":"story","title":"Server","brief":"","acceptance":[],
   "children":[{"ref":"t1","type":"task","title":"Session cookie","brief":"","acceptance":[],"role_hint":"coder","tdd_exempt":null,"repos":["chat"]},
               {"ref":"t2","type":"task","title":"Login route","brief":"","acceptance":[],"role_hint":"coder","tdd_exempt":null,"repos":["chat"]}]}],
 "deps":[{"item":"t2","blocked_by":"t1"}]}` + "\n```\n\n## Verification\n\ngo test ./...\n"

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// setItemWorkflow writes a resolved workflow.Spec straight to an item's
// workflow_json (P9's engine, which would normally resolve and store this
// via items.Store.CreateTx/PatchTx, doesn't exist yet -- P8's tests seed the
// column directly, same pattern as the tdd_exempt UPDATE above).
func setItemWorkflow(t *testing.T, s *Store, key string, spec workflow.Spec) {
	t.Helper()
	ctx := context.Background()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET workflow_json = ? WHERE key = ?`,
		string(b), key); err != nil {
		t.Fatal(err)
	}
}

// seedWorkflowRun inserts a workflows row and one workflow_runs row directly
// (P9's engine, which would normally own these inserts, doesn't exist yet):
// P8's gates and sibling-closing logic read workflow_runs, so its tests seed
// rows by hand. Returns both ids.
func seedWorkflowRun(t *testing.T, s *Store, itemID, rootItemID, ownerAgentID, agentID, stepID, role string, round int) (workflowID, runID string) {
	t.Helper()
	ctx := context.Background()
	now := db.Millis(s.Now())
	workflowID = ids.New("wf")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO workflows
		(id, item_id, root_item_id, owner_agent_id, state, round, worktrees_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'running', ?, '[]', ?, ?)`,
		workflowID, itemID, rootItemID, ownerAgentID, round, now, now); err != nil {
		t.Fatal(err)
	}
	runID = ids.New("wfr")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`,
		runID, workflowID, stepID, round, role, agentID, now); err != nil {
		t.Fatal(err)
	}
	return workflowID, runID
}

// setItemUnits writes it.Units directly (batched task, spec C4). The
// content of each unit's own Steps doesn't matter to the gates under test.
func setItemUnits(t *testing.T, s *Store, key string, titles ...string) {
	t.Helper()
	units := make([]items.Unit, len(titles))
	for i, title := range titles {
		units[i] = items.Unit{Title: title, Steps: []string{"do it"}}
	}
	b, err := json.Marshal(units)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE items SET units_json = ? WHERE key = ?`,
		string(b), key); err != nil {
		t.Fatal(err)
	}
}

// setItemVerify writes it.Verify directly (declared verify commands).
func setItemVerify(t *testing.T, s *Store, key string, cmds ...string) {
	t.Helper()
	b, err := json.Marshal(cmds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE items SET verify_json = ? WHERE key = ?`,
		string(b), key); err != nil {
		t.Fatal(err)
	}
}

// seedReviewFindings inserts a reviewer's workflow_runs row for workflowID at
// round with the given findings, for tests exercising the tdd gate's
// fix-round scope (spec B5/ruling-tdd-followups.md).
func seedReviewFindings(t *testing.T, s *Store, workflowID string, round int, findings []workflow.Finding) {
	t.Helper()
	b, err := json.Marshal(findings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, state, verdict, findings_json, created_at)
		VALUES (?, ?, 'review', ?, 'reviewer', 'completed', 'changes_requested', ?, ?)`,
		ids.New("wfr"), workflowID, round, string(b), db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}
}

func gitRepoNoSigning(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

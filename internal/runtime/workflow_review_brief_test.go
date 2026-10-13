package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// reviewBriefFits renders step's first-round brief for role the way the
// engine does and reports whether it fits the cap.
func reviewBriefFits(t *testing.T, s *Store, orch Agent, taskKey, stepID, role, wtID, reviewWT string) bool {
	t.Helper()
	ctx := context.Background()
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	wtJSON, _ := json.Marshal([]WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}})
	wf := wfRow{OwnerAgentID: orch.ID, WorktreesJSON: string(wtJSON), ContextJSON: "[]"}
	brief, _, err := s.stepBrief(ctx, wf, it, wfRunRow{StepID: stepID, Role: role, Round: 1, ReviewWorktreeID: reviewWT})
	if err != nil {
		t.Fatal(err)
	}
	name, err := defaultName(Role(role), it.Title)
	if err != nil {
		t.Fatal(err)
	}
	brief.Key, brief.Title, brief.Name, brief.Role = it.Key, it.Title, name, Role(role)
	brief.RootKey, brief.ParentName = it.RootKey, orch.Name
	_, err = RenderBrief(brief)
	return err == nil
}

// BUG-79: a later step's brief carries things the first step's doesn't: the
// reviewer's verdict contract and review-worktree line, and a Context pointer
// once an artifact is registered while the coder works. A brief the item
// create and the coder spawn accept can then overflow at the review spawn.
// Start must refuse it up front, when every step's brief is known, instead of
// escalating after the build finished.
func TestStartWorkflowRefusesBriefOnlyTheReviewerOverflows(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, repoID, dir, head := seedOwnedRepoWorktree(t, s, orch)
	// The review tree the engine would create once the build completes.
	rev, err := s.Worktree.Review(ctx, worktree.CreateInput{RepoID: repoID, RepoPath: dir,
		OwnerAgentID: orch.ID, RootItemID: orch.RootItemID}, head)
	if err != nil {
		t.Fatal(err)
	}
	reviewWT := rev.ID
	setBrief := func(n int) {
		t.Helper()
		if _, err := s.DB.ExecContext(ctx, `UPDATE items SET brief = ? WHERE key = ?`,
			strings.Repeat("x", n), taskKey); err != nil {
			t.Fatal(err)
		}
	}

	// The largest brief the coder step still renders.
	n := 5000
	for ; n < 7000; n++ {
		setBrief(n + 1)
		if !reviewBriefFits(t, s, orch, taskKey, "build", "coder", wtID, "") {
			break
		}
	}
	setBrief(n)
	if !reviewBriefFits(t, s, orch, taskKey, "build", "coder", wtID, "") {
		t.Fatalf("setup: coder brief of %d chars should fit", n)
	}
	// The coder registers a design artifact while it works; the reviewer's
	// Context then gets the (truncated) pointer line the coder's never had.
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	artID := ids.New("art")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES (?, ?, 'design', 'docs/design/alpha.md', 1, ?, 1)`, artID, it.ID, orch.ID); err != nil {
		t.Fatal(err)
	}
	if reviewBriefFits(t, s, orch, taskKey, "review", "reviewer", wtID, reviewWT) {
		t.Fatalf("setup: reviewer brief of %d chars plus a design artifact should overflow the cap", n)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, artID); err != nil {
		t.Fatal(err)
	}

	_, err = s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest {
		t.Fatalf("err = %v, want items bad_request", err)
	}
	if want := taskKey + ": " + ErrBriefTooLong; ie.Message != want {
		t.Fatalf("message = %q, want %q", ie.Message, want)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("workflows rows = %d, want 0", count)
	}
}

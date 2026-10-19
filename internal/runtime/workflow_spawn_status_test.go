package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A workflow task stays Ready until its first step's agent accepts; the engine's
// own spawn-time attempt to move it used to be denied and logged as "stays put".
func TestWorkflowSpawnDoesNotLogADeniedMoveToInProgress(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	var logs []string
	s.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	var before string
	s.DB.QueryRowContext(ctx, `SELECT status FROM items WHERE key = ?`, taskKey).Scan(&before)
	coderSes := agentSessionForStep(t, s, st.ID, "build")
	if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if before != "ready" {
		t.Fatalf("status before accepted = %s, want ready", before)
	}
	var after string
	s.DB.QueryRowContext(ctx, `SELECT status FROM items WHERE key = ?`, taskKey).Scan(&after)
	if after != "in_progress" {
		t.Errorf("status after accepted = %s, want in_progress", after)
	}
	for _, l := range logs {
		if strings.Contains(l, "stays put") {
			t.Errorf("logged %q (status %s -> %s)", l, before, after)
		}
	}
}

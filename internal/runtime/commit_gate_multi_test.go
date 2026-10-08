package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// seedTwoTreesOneRepo gives coder two rw worktrees of the same repo "proj" on
// different branches with different HEADs (BUG-63): the repo checkout itself
// on task/x, and a linked worktree on task/two one commit ahead.
func seedTwoTreesOneRepo(t *testing.T, s *Store, coder Agent) (dir1, head1, dir2, head2 string) {
	t.Helper()
	ctx := context.Background()
	dir1, head1 = seedCommitRepo(t, s, coder)
	dir2 = filepath.Join(t.TempDir(), "two")
	gitOutput(t, dir1, "worktree", "add", "-q", "-b", "task/two", dir2)
	commitFile(t, dir2, "b.txt", "two")
	head2 = strings.TrimSpace(gitOutput(t, dir2, "rev-parse", "HEAD"))
	var repoID string
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM repos WHERE path = ?`, dir1).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	wt2 := seedRWWorktreeAt(t, s, repoID, coder.ID, coder.RootItemID, dir2)
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET branch = 'task/two' WHERE id = ?`, wt2); err != nil {
		t.Fatal(err)
	}
	return dir1, head1, dir2, head2
}

func TestCommitGateChecksEachWorktreeOfOneRepo(t *testing.T) {
	ctx := context.Background()
	for _, swap := range []bool{false, true} {
		t.Run(fmt.Sprintf("swap=%v", swap), func(t *testing.T) {
			s, _, _ := newStore(t)
			coder, coderSes, workflowID := buildOnly(t, s, workflow.GateCommit)
			_, head1, _, head2 := seedTwoTreesOneRepo(t, s, coder)
			git := []GitRef{{Repo: "proj", Branch: "task/x", SHA: head1}, {Repo: "proj", Branch: "task/two", SHA: head2}}
			if swap {
				git[0], git[1] = git[1], git[0]
			}
			if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
				Git: git}); err != nil {
				t.Fatalf("one clean git entry per worktree should pass the commit gate: %v", err)
			}
			var stored string
			if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(sha, '') FROM workflow_runs WHERE workflow_id = ?`,
				workflowID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != head1 {
				t.Fatalf("workflow_runs.sha = %q, want the first-shared tree's HEAD %q", stored, head1)
			}
		})
	}
}

func TestCommitGateRefusesDirtyOrMismatchedSecondWorktree(t *testing.T) {
	ctx := context.Background()
	t.Run("dirty", func(t *testing.T) {
		s, _, _ := newStore(t)
		coder, coderSes, _ := buildOnly(t, s, workflow.GateCommit)
		_, head1, dir2, head2 := seedTwoTreesOneRepo(t, s, coder)
		if err := os.WriteFile(filepath.Join(dir2, "c.txt"), []byte("uncommitted"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
			Git: []GitRef{{Repo: "proj", Branch: "task/x", SHA: head1}, {Repo: "proj", Branch: "task/two", SHA: head2}}})
		if want := "Commit your work before completing: proj is dirty." + hintCopy; err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		s, _, _ := newStore(t)
		coder, coderSes, _ := buildOnly(t, s, workflow.GateCommit)
		_, head1, _, head2 := seedTwoTreesOneRepo(t, s, coder)
		_, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
			Git: []GitRef{{Repo: "proj", Branch: "task/x", SHA: head1}, {Repo: "proj", Branch: "task/two", SHA: head1}}})
		want := fmt.Sprintf("Commit your work before completing: proj HEAD is %s, checkpoint says %s. Pass the full sha of your committed HEAD."+hintCopy, head2, head1)
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
}

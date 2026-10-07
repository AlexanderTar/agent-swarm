package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/events"
)

// retainedUnmerged makes a tree with one commit not on main, retained as
// unmerged, the shape BUG-52 left lingering.
func retainedUnmerged(t *testing.T) (*Service, Worktree, string) {
	t.Helper()
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/keep",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", "new.txt")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "unlanded")
	if err := os.WriteFile(filepath.Join(wt.Path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, wt.ID, "agt_1"); err != nil {
		t.Fatal(err)
	}
	wt, err = s.ReclaimOne(ctx, wt)
	if err != nil || wt.State != "retained" {
		t.Fatalf("reclaim = %+v, %v; want retained", wt, err)
	}
	return s, wt, repo
}

func setRoot(t *testing.T, s *Service, status string) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE items SET status = ? WHERE id = 'itm_1'`, status); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardRemovesRetainedTreeOfDoneRootAndKeepsTheBranch(t *testing.T) {
	s, wt, repo := retainedUnmerged(t)
	ctx := context.Background()
	s.Events = events.New(s.DB, s.Now)
	setRoot(t, s, "done")
	for _, ref := range []string{wt.Path, wt.ID} {
		got, n, err := s.Discard(ctx, ref)
		if ref == wt.Path {
			if err != nil || got.State != "removed" || n != 1 || got.Branch != "task/keep" {
				t.Fatalf("Discard(path) = %+v, %d, %v", got, n, err)
			}
			if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
				t.Fatalf("path still exists: %v", err)
			}
			if out := run(t, repo, "branch", "--list", "task/keep"); !strings.Contains(out, "task/keep") {
				t.Fatalf("branch gone: %q", out)
			}
			var ev int
			if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE type = 'worktree.discarded'`).Scan(&ev); err != nil || ev != 1 {
				t.Fatalf("events = %d, %v; want 1", ev, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "already removed") {
			t.Fatalf("second discard by id = %v, want already removed", err)
		}
	}
}

func TestDiscardRefusals(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	ctx := context.Background()
	if _, _, err := s.Discard(ctx, wt.Path); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("active root err = %v, want it to name the root status", err)
	}
	if _, _, err := s.Discard(ctx, "/nope/wt"); err == nil || !strings.Contains(err.Error(), "Unknown worktree") {
		t.Fatalf("unknown path err = %v", err)
	}
	setRoot(t, s, "cancelled")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktree_reservations (worktree_id, agent_id, mode, created_at)
		VALUES (?, 'agt_2', 'rw', 1)`, wt.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Discard(ctx, wt.Path); err == nil || !strings.Contains(err.Error(), "coder") {
		t.Fatalf("held tree err = %v, want it to name the live holder", err)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("refused discard removed the tree: %v", err)
	}
}

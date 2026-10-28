package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/events"
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
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
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

func TestDiscardAllowsAnInProgressRootWhenNobodyLiveHoldsIt(t *testing.T) {
	s, wt, repo := retainedUnmerged(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	got, n, err := s.Discard(ctx, wt.Path)
	if err != nil || got.State != "removed" || n != 1 || got.Branch != "task/keep" {
		t.Fatalf("Discard = %+v, %d, %v; want the tree removed and the branch kept", got, n, err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("path still exists: %v", err)
	}
	if out := run(t, repo, "branch", "--list", "task/keep"); !strings.Contains(out, "task/keep") {
		t.Fatalf("branch gone: %q", out)
	}
}

func TestDiscardRefusesALiveOwnerEvenWhenTheRootIsInProgress(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	_, _, err := s.Discard(context.Background(), wt.Path)
	if err == nil || !strings.Contains(err.Error(), "orch") {
		t.Fatalf("err = %v, want it to name the live owner", err)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("refused discard removed the tree: %v", err)
	}
}

func TestDiscardRefusesATrailingSlashPathOfALiveOwner(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	_, _, err := s.Discard(context.Background(), wt.Path+"/")
	if err == nil || !strings.Contains(err.Error(), "orch") {
		t.Fatalf("err = %v, want it to name the live owner", err)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("trailing-slash discard removed the tree: %v", err)
	}
	var state string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT state FROM worktrees WHERE id = ?`, wt.ID).Scan(&state); err != nil || state != "retained" {
		t.Fatalf("state = %s, %v; want retained", state, err)
	}
}

func TestDiscardRefusesASymlinkedSpellingOfALiveOwnerTree(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	link := wt.Path + "-link"
	if err := os.Symlink(wt.Path, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Discard(context.Background(), link)
	if err == nil || !strings.Contains(err.Error(), "recorded as "+wt.Path) {
		t.Fatalf("err = %v, want a refusal that names the recorded path", err)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("symlinked discard removed the tree: %v", err)
	}
	var state string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT state FROM worktrees WHERE id = ?`, wt.ID).Scan(&state); err != nil || state != "retained" {
		t.Fatalf("state = %s, %v; want retained", state, err)
	}
}

func TestDiscardRefusesAFinishedOwnerWhoseSessionIsStillQuiescing(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_1', 'agt_1', 1, 1, 'tok', 'tmux', ?, 'quiescing', 'worktree', 1)`, wt.Path); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Discard(ctx, wt.Path)
	if err == nil || !strings.Contains(err.Error(), "orch") {
		t.Fatalf("err = %v, want it to name the owner whose session is still quiescing", err)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("quiescing owner discard removed the tree: %v", err)
	}
}

func TestDiscardRemovesAPathWithNoSwarmRow(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/stray",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM worktree_reservations WHERE worktree_id = ?`, wt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM worktrees WHERE id = ?`, wt.ID); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Discard(ctx, wt.Path)
	if err != nil || got.State != "removed" || got.Path != wt.Path {
		t.Fatalf("Discard = %+v, %v; want the untracked path removed", got, err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("path still exists: %v", err)
	}
	if out := run(t, repo, "branch", "--list", "task/stray"); !strings.Contains(out, "task/stray") {
		t.Fatalf("branch gone: %q", out)
	}
}

func TestDiscardRefusals(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	ctx := context.Background()
	if _, _, err := s.Discard(ctx, "/nope/wt"); err == nil || !strings.Contains(err.Error(), "Unknown worktree") {
		t.Fatalf("unknown path err = %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
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

func TestRemoveByNonOwnerNamesStateAndPointsToDiscardForFinishedRoot(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	ctx := context.Background()
	_, err := s.Remove(ctx, wt.ID, "agt_2")
	if err == nil || !strings.Contains(err.Error(), "only the owner") ||
		!strings.Contains(err.Error(), "retained") || !strings.Contains(err.Error(), "dirty") ||
		!strings.Contains(err.Error(), "swarm cleanup --discard "+wt.Path) {
		t.Fatalf("active root err = %v, want owner-only, state/reason, and the discard command", err)
	}
	setRoot(t, s, "done")
	_, err = s.Remove(ctx, wt.ID, "agt_2")
	if err == nil || !strings.Contains(err.Error(), "swarm cleanup --discard "+wt.Path) {
		t.Fatalf("done root err = %v, want the discard command", err)
	}
}

package worktree

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDirtyReview(t *testing.T) (*Service, Worktree, string) {
	t.Helper()
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	sha := strings.TrimSpace(run(t, repo, "rev-parse", "HEAD"))
	wt, err := s.Review(context.Background(), CreateInput{RepoID: repoID, RepoPath: repo,
		OwnerAgentID: "agt_1", RootItemID: "itm_1"}, sha)
	if err != nil {
		t.Fatal(err)
	}
	// Build-tooling dirt: one modified tracked file, one untracked.
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "next-env.d.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s, wt, repo
}

func TestReclaimOneDiscardsToolingDirtInAReviewTreeAtItsSHA(t *testing.T) {
	s, wt, _ := newDirtyReview(t)
	ctx := context.Background()
	notified := 0
	s.OnRetained = func(context.Context, *sql.Tx, Worktree) error { notified++; return nil }
	if ok, reason := s.WouldRemove(ctx, wt); !ok {
		t.Fatalf("WouldRemove = false (%s), want true", reason)
	}
	out, err := s.ReclaimOne(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" || out.RetainedReason != "" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	if notified != 0 {
		t.Fatalf("OnRetained called %d times, want 0", notified)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("path still exists: %v", err)
	}
}

func TestReclaimOneStillRetainsADirtyBranchTree(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/d",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt.Path, "x"), []byte("y"), 0o644)
	if ok, _ := s.WouldRemove(ctx, wt); ok {
		t.Fatal("WouldRemove = true for a dirty branch tree")
	}
	out, err := s.ReclaimOne(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty", out)
	}
}

func finishOwner(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE agents SET state = 'finished' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
}

func churnTree(t *testing.T) (*Service, Worktree) {
	t.Helper()
	repo := gitRepo(t)
	withOrigin(t, repo)
	s, repoID := newService(t, repo)
	wt := branchWorktree(t, s, repo, repoID, "task/beta")
	finishOwner(t, s)
	return s, wt
}

func TestReclaimOneKeepsTrackedModificationsWhileTheOwnerIsLive(t *testing.T) {
	repo := gitRepo(t)
	withOrigin(t, repo)
	s, repoID := newService(t, repo)
	wt := branchWorktree(t, s, repo, repoID, "task/live-beta")
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReclaimOne(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty while the owner is still active", out)
	}
}

func TestReclaimOneKeepsTrackedEditsWhileTheRootIsInProgress(t *testing.T) {
	s, wt := churnTree(t)
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReclaimOne(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty: a finished owner on an in-progress root can still be retried", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("in-progress tree must stay: %v", err)
	}
}

func TestReclaimOneDiscardsTrackedModificationsWithNoOwnCommits(t *testing.T) {
	s, wt := churnTree(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE id = 'itm_1'`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReclaimOne(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed: tracked edits and no commits beyond origin/main are build churn", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("path still exists: %v", err)
	}
}

func TestReclaimOneKeepsUntrackedFilesAsDirty(t *testing.T) {
	s, wt := churnTree(t)
	if err := os.WriteFile(filepath.Join(wt.Path, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReclaimOne(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("untracked tree must stay: %v", err)
	}
}

func TestReclaimOneKeepsADeletionAsDirty(t *testing.T) {
	s, wt := churnTree(t)
	if err := os.Remove(filepath.Join(wt.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReclaimOne(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty for a deletion", out)
	}
}

func TestReclaimOneKeepsOwnCommitsEvenWhenOnlyTrackedFilesChanged(t *testing.T) {
	s, wt := churnTree(t)
	commitFile(t, wt.Path, "own.txt")
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReclaimOne(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty when the branch has its own commit", out)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "own.txt")); err != nil {
		t.Fatalf("own commit must stay on disk: %v", err)
	}
}

func TestReclaimOneKeepsADirtyReviewTreeThatMovedOffItsSHA(t *testing.T) {
	s, wt, _ := newDirtyReview(t)
	ctx := context.Background()
	run(t, wt.Path, "add", "next-env.d.ts")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-m", "local note")
	if ok, _ := s.WouldRemove(ctx, wt); ok {
		t.Fatal("WouldRemove = true for a tree that moved off its sha")
	}
	out, err := s.ReclaimOne(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason == "" {
		t.Fatalf("worktree = %+v, want retained", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("retained tree must still exist: %v", err)
	}
}

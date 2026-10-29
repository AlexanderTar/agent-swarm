package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Unit 1 red: a clean merged tree whose `git worktree remove` is SIGKILLed
// mid-delete (the 30 s execx cap, daemon.err.log "git: signal: killed") must
// end removed, not retained. Today it ends retained/remove_failed.
func TestRemoveSurvivesKilledWorktreeRemove(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/survive-kill",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	real := s.Run
	var removes int
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 3 && args[2] == "worktree" && args[3] == "remove" {
			removes++
			// Simulate the SIGKILL landing mid-delete: one tracked file is
			// already gone, so porcelain would show a deletion.
			_ = os.Remove(filepath.Join(wt.Path, "README.md"))
			return nil, errors.New("git: signal: killed")
		}
		return real(ctx, name, args...)
	}
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed (a SIGKILLed delete must not strand the tree)", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
	if removes != 0 {
		t.Fatalf("bulk delete ran through %d capped `git worktree remove` calls, want 0 (Go-side delete)", removes)
	}
}

// Unit 2 red: a row stranded retained/remove_failed by an interrupted delete
// (porcelain shows only tracked deletions, plus watchman cookies) must be
// removed on retry when the branch is otherwise merged.
func TestRemoveRetriesAnInterruptedDelete(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/interrupted",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	// Half-deleted shape: a tracked file is gone, .git is intact.
	if err := os.Remove(filepath.Join(wt.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".watchman-cookie-host-1-2"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'retained', retained_reason = 'remove_failed' WHERE id = ?`, wt.ID); err != nil {
		t.Fatal(err)
	}
	wt.State, wt.RetainedReason = "retained", "remove_failed"
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed (interrupted delete must be retried)", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
}

// Unit 2 guard: a remove_failed row whose dirt is real work (a modification,
// not just deletions) must still be retained.
func TestRemoveKeepsRealWorkOnARemoveFailedRetry(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/failed-dirty",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("real work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'retained', retained_reason = 'remove_failed' WHERE id = ?`, wt.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "dirty" {
		t.Fatalf("worktree = %+v, want retained/dirty (real work must never be deleted)", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("a retained worktree must still exist: %v", err)
	}
}

// Unit 3 red: Discard force-removes a tree even when the capped
// `git worktree remove --force` would be SIGKILLed mid-delete.
func TestDiscardSurvivesKilledWorktreeRemove(t *testing.T) {
	s, wt, _ := retainedUnmerged(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	real := s.Run
	var removes int
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 3 && args[2] == "worktree" && args[3] == "remove" {
			removes++
			return nil, errors.New("git: signal: killed")
		}
		return real(ctx, name, args...)
	}
	got, _, err := s.Discard(ctx, wt.Path)
	if err != nil {
		t.Fatalf("Discard = %v, want success (a SIGKILLed delete must not strand the tree)", err)
	}
	if got.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", got)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
	if removes != 0 {
		t.Fatalf("discard ran through %d capped `git worktree remove` calls, want 0 (Go-side delete)", removes)
	}
}

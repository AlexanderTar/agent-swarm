package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Unit 1: remove never shells out to the capped `git worktree remove` for the
// bulk delete (the 30 s execx cap SIGKILLed it mid-delete, daemon.err.log
// "git: signal: killed", stranding the tree as retained/remove_failed). The
// Go-side delete has no cap, so a clean merged tree always ends removed.
func TestRemoveDeletesWithoutWorktreeRemoveSubprocess(t *testing.T) {
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

// Unit 3: Discard force-removes a tree without the capped
// `git worktree remove --force` (same SIGKILL stranding as unit 1).
func TestDiscardDeletesWithoutWorktreeRemoveSubprocess(t *testing.T) {
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

// A direct Remove that runs while a reclaim pass is open must still drop the
// repo's admin entry for the deleted tree. Skipping the prune inside a pass
// leaves `git worktree list` naming the path, so a later add on the same path
// or branch fails with "already registered".
func TestRemovePrunesWhilePassOpen(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/pass-open-prune",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	s.BeginPass(false)
	defer s.EndPass()
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	list, err := s.git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if worktreeRegistered(string(list), wt.Path) {
		t.Fatalf("registration for %s left behind: prune was skipped while a pass was open", wt.Path)
	}
}

// worktreeRegistered reports whether porcelain names path. Unlike
// worktreeListed it also matches when the folder is already gone: git spells
// a missing worktree resolved (/private/var/...) while the recorded path may
// use the unresolved spelling (/var/... under t.TempDir()), so the parent
// (which still exists) is resolved for a second candidate spelling.
func worktreeRegistered(porcelain, path string) bool {
	cands := map[string]bool{filepath.Clean(path): true}
	if real, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		cands[filepath.Join(real, filepath.Base(path))] = true
	}
	for _, line := range strings.Split(porcelain, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && cands[filepath.Clean(p)] {
			return true
		}
	}
	return false
}

// A row stranded retained/remove_failed whose `.git` file was already deleted
// by the interrupted delete (git's own list still registers the path) must be
// removed on retry when the branch is merged judged from the repo side.
func TestRemoveRetriesRemoveFailedWithoutGit(t *testing.T) {
	repo := gitRepo(t)
	// A second tracked file committed on main before the tree exists, so the
	// branch is merged and a leftover remains once `.git` is deleted.
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "other.txt")
	run(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "other")
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/no-git-retry",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	// Half-deleted shape with `.git` gone first: the kill landed after the
	// `.git` file was removed, with tracked files still on disk.
	if err := os.Remove(filepath.Join(wt.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(wt.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'retained', retained_reason = 'remove_failed' WHERE id = ?`, wt.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed (interrupted delete without .git must be retried)", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
}

// Guard: a remove_failed row without `.git` whose branch holds its own
// unmerged commit must still be retained, folder untouched.
func TestRemoveKeepsUnmergedTreeWithoutGit(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/no-git-unmerged",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", "new.txt")
	run(t, wt.Path, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "unlanded")
	if err := os.Remove(filepath.Join(wt.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'retained', retained_reason = 'remove_failed' WHERE id = ?`, wt.ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "not_a_worktree" {
		t.Fatalf("worktree = %+v, want retained/not_a_worktree (unmerged work must never be deleted)", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("a retained worktree must still exist: %v", err)
	}
}

// undoAdd deletes the folder Go-side and drops the repo's registration of it,
// the same delete-plus-prune every other call site shares.
func TestUndoAddDeletesAndPrunesRegistration(t *testing.T) {
	repo := gitRepo(t)
	s, _ := newService(t, repo)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(s.Home, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Home, "worktrees", "proj--orphan-x")
	if out, err := s.git(ctx, repo, "worktree", "add", "-b", "task/orphan-x", path, "main"); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	s.undoAdd(repo, path, true, "task/orphan-x")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
	list, err := s.git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if worktreeRegistered(string(list), path) {
		t.Fatalf("registration for %s left behind: undoAdd did not prune", path)
	}
	if out, err := s.git(ctx, repo, "branch", "--list", "task/orphan-x"); err != nil || string(out) != "" {
		t.Fatalf("branch left behind: %q, %v", out, err)
	}
}

package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// watchmanRace stands in for a running watchman: watch-del is recorded, and the
// first `git worktree remove` fails the way git does when a cookie file lands
// in the folder mid-delete.
type watchmanRace struct {
	mu        sync.Mutex
	watchDels []string
	removes   int
}

func (w *watchmanRace) runner(real func(context.Context, string, ...string) ([]byte, error), path string) func(context.Context, string, ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if name == "watchman" {
			w.watchDels = append(w.watchDels, strings.Join(args, " "))
			return nil, nil
		}
		if slices.Contains(args, "worktree") && slices.Contains(args, "remove") {
			w.removes++
			if w.removes == 1 {
				_ = os.WriteFile(filepath.Join(path, ".watchman-cookie-host-4242-7"), nil, 0o644)
				return []byte("error: failed to delete '" + path + "': Directory not empty"),
					errors.New("exit status 255")
			}
		}
		return real(ctx, name, args...)
	}
}

func TestRemoveSurvivesWatchmanCookiesAppearingMidDelete(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/cookies",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	// A cookie from before the remove must not read as dirt.
	if err := os.WriteFile(filepath.Join(wt.Path, ".watchman-cookie-host-4242-1"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w := &watchmanRace{}
	s.Run = w.runner(s.Run, wt.Path)

	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
	if !slices.Contains(w.watchDels, "--no-spawn watch-del "+wt.Path) {
		t.Fatalf("watchman watch-del never ran for the path: %v", w.watchDels)
	}
	// The bulk delete is Go-side (no 30 s capped subprocess): even with a
	// cookie present, no `git worktree remove` runs; cookies are cleared
	// around os.RemoveAll instead.
	if w.removes != 0 {
		t.Fatalf("git worktree remove ran %d times, want 0 (Go-side delete)", w.removes)
	}
}

// strand leaves wt.Path the way a half-finished `git worktree remove` does: no
// .git, the tracked files gone, and only the named files left behind.
func strand(t *testing.T, path string, left ...string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(path, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range left {
		if err := os.WriteFile(filepath.Join(path, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoveDeletesANonGitFolderHoldingOnlyWatchmanCookies(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/stray-cookies",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	strand(t, wt.Path, ".watchman-cookie-host-4242-3")

	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
	if list := run(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, wt.Path) {
		t.Fatalf("git still lists the worktree after prune:\n%s", list)
	}
}

func TestRemoveRetainsANonGitFolderWithOtherFilesAsNotAWorktree(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/stray-files",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	strand(t, wt.Path, ".watchman-cookie-host-4242-3", "notes.txt")

	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "not_a_worktree" {
		t.Fatalf("worktree = %+v, want retained/not_a_worktree", out)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "notes.txt")); err != nil {
		t.Fatalf("a retained folder must keep its files: %v", err)
	}
	if ok, reason := s.WouldRemove(ctx, wt); ok || reason != "not_a_worktree" {
		t.Fatalf("WouldRemove = %v, %q, want false, not_a_worktree", ok, reason)
	}
}

func TestDiscardRemovesANotAWorktreeFolder(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/discard-stray",
		OwnerAgentID: "agt_2", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	strand(t, wt.Path, "notes.txt")
	if _, err := s.Remove(ctx, wt.ID, "agt_2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = 1 WHERE id = 'agt_2'`); err != nil {
		t.Fatal(err)
	}

	got, _, err := s.Discard(ctx, wt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", got)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
}

// raceAfterRealRemove is the real-git failure: git removes the admin entry and
// the tracked files, then a cookie lands and the final rmdir fails.
func TestRemoveEndsRemovedWhenGitDroppedTheWorktreeBeforeFailing(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/real-race",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	real := s.Run
	removes := 0
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && slices.Contains(args, "worktree") && slices.Contains(args, "remove") {
			removes++
			if removes == 1 {
				out, err := real(ctx, name, args...)
				if err != nil {
					return out, err
				}
				_ = os.MkdirAll(wt.Path, 0o755)
				_ = os.WriteFile(filepath.Join(wt.Path, ".watchman-cookie-host-4242-9"), nil, 0o644)
				return []byte("error: failed to delete '" + wt.Path + "': Directory not empty"),
					errors.New("exit status 255")
			}
		}
		return real(ctx, name, args...)
	}

	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
	if list := run(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, wt.Path) {
		t.Fatalf("git still lists the worktree:\n%s", list)
	}
}

// prune drops the repo's admin entry for the worktree, leaving the folder's
// .git file pointing at nothing, as after a failed forced remove.
func prune(t *testing.T, repo, name string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(repo, ".git", "worktrees", name)); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRetainsADanglingGitfileFolderAsNotAWorktree(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/dangling",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	prune(t, repo, filepath.Base(wt.Path))

	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "retained" || out.RetainedReason != "not_a_worktree" {
		t.Fatalf("worktree = %+v, want retained/not_a_worktree", out)
	}
}

func TestRemoveDeletesADanglingGitfileFolderHoldingOnlyCookies(t *testing.T) {
	repo := gitRepo(t)
	s, repoID := newService(t, repo)
	ctx := context.Background()
	wt, err := s.Create(ctx, CreateInput{RepoID: repoID, RepoPath: repo, Branch: "task/dangling-cookies",
		OwnerAgentID: "agt_1", RootItemID: "itm_1"})
	if err != nil {
		t.Fatal(err)
	}
	gitfile, err := os.ReadFile(filepath.Join(wt.Path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	strand(t, wt.Path, ".watchman-cookie-host-4242-3")
	if err := os.WriteFile(filepath.Join(wt.Path, ".git"), gitfile, 0o644); err != nil {
		t.Fatal(err)
	}
	prune(t, repo, filepath.Base(wt.Path))

	out, err := s.Remove(ctx, wt.ID, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed", out)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("folder left behind: %v", err)
	}
}

func TestReclaimOneDiscardsBuildChurnThatAlsoHoldsAWatchmanCookie(t *testing.T) {
	s, wt := churnTree(t)
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE id = 'itm_1'`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".watchman-cookie-host-4242-5"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.ReclaimOne(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "removed" {
		t.Fatalf("worktree = %+v, want removed: a cookie is not work", out)
	}
}

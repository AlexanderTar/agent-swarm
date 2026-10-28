package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// strandWorktree leaves path as a half-finished `git worktree remove` does: no
// .git, no tracked files, only the named files.
func strandWorktree(t *testing.T, path string, left ...string) {
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

// BUG-132: git deleted .git and the tracked files, watchman kept writing
// cookies, and every later pass kept the row as dirty.
func TestReclaimClearsAStuckDirtyRowWhoseFolderHoldsOnlyWatchmanCookies(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	finishReclaimAgent(t, s, "owner_a", s.Now())
	repoID := seedRepo(t, s, "proj")
	repoPath := repoPathFor(t, s, repoID)
	stuck := seedReclaimWorktree(t, s, repoID, repoPath, "task/stuck-cookies", "owner_a", ep.ID)
	other := seedReclaimWorktree(t, s, repoID, repoPath, "task/stuck-files", "owner_a", ep.ID)
	strandWorktree(t, stuck.Path, ".watchman-cookie-host-4242-9")
	strandWorktree(t, other.Path, ".watchman-cookie-host-4242-9", "notes.txt")
	for _, id := range []string{stuck.ID, other.ID} {
		if _, err := s.DB.ExecContext(ctx,
			`UPDATE worktrees SET state = 'retained', retained_reason = 'dirty' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	at.Advance(2 * time.Hour)

	res, err := s.ReclaimWorktreesWith(ctx, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := resultFor(res, stuck.Path); !ok || r.Action != "removed" {
		t.Fatalf("cookie-only result = %+v, want removed", r)
	}
	if st, _ := reclaimWorktreeState(t, s, stuck.ID); st != "removed" {
		t.Fatalf("cookie-only row state = %s, want removed", st)
	}
	if _, err := os.Stat(stuck.Path); !os.IsNotExist(err) {
		t.Fatalf("cookie-only folder left behind: %v", err)
	}
	if r, ok := resultFor(res, other.Path); !ok || r.Action != "kept" || r.Reason != "not_a_worktree" {
		t.Fatalf("other-files result = %+v, want kept/not_a_worktree", r)
	}
	if st, reason := reclaimWorktreeState(t, s, other.ID); st != "retained" || reason != "not_a_worktree" {
		t.Fatalf("other-files row = %s/%s, want retained/not_a_worktree", st, reason)
	}
}

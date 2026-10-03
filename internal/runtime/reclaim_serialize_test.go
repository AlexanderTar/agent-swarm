package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// BUG-30: `swarm cleanup` is its own process, so the per-worktree in-process
// mutex cannot stop it racing the daemon's loop; two `git worktree remove`s on
// one tree leave it half deleted. Passes serialize on a file lock in Home.
func TestReclaimPassWaitsForAnotherPassHoldingTheLock(t *testing.T) {
	s, _, _ := clockStore(t)
	f, err := os.OpenFile(filepath.Join(s.Home, "reclaim.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.ReclaimWorktreesWith(context.Background(), CleanupOptions{})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("a reclaim pass ran while another pass held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pass never ran after the lock was released")
	}
}

// A `git worktree remove` that deletes part of the tree and then fails must be
// reported kept, with the error, and the row stays retained -- never removed.
func TestReclaimReportsAPartialRemoveAsKeptWithTheError(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	finishReclaimAgent(t, s, "owner_a", s.Now())
	repoID := seedRepo(t, s, "proj")
	repoPath := repoPathFor(t, s, repoID)
	wt := seedReclaimWorktree(t, s, repoID, repoPath, "task/partial", "owner_a", ep.ID)
	real := s.Worktree.Run
	s.Worktree.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 3 && args[2] == "worktree" && args[3] == "remove" {
			os.Remove(filepath.Join(wt.Path, "README.md")) // part of the tree goes, then it fails
			return []byte("error: unlinkat failed"), errors.New("exit status 1")
		}
		return real(ctx, name, args...)
	}
	at.Advance(2 * time.Hour)
	res, err := s.ReclaimWorktreesWith(ctx, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := resultFor(res, wt.Path)
	if !ok || r.Action != "kept" || !strings.Contains(r.Reason, "remove_failed") || !strings.Contains(r.Reason, "unlinkat failed") {
		t.Fatalf("result = %+v, want kept with remove_failed and the git error", r)
	}
	if st, reason := reclaimWorktreeState(t, s, wt.ID); st != "retained" || reason != "remove_failed" {
		t.Fatalf("row = %s/%s, want retained/remove_failed", st, reason)
	}
}

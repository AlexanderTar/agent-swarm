package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resultFor(rs []CleanupResult, path string) (CleanupResult, bool) {
	for _, r := range rs {
		if r.Path == path {
			return r, true
		}
	}
	return CleanupResult{}, false
}

func TestReclaimPrunesOncePerRepoAndReportsUntrackedDirs(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	repoID := seedRepo(t, s, "proj")
	repoPath := repoPathFor(t, s, repoID)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	finishReclaimAgent(t, s, "owner_a", s.Now())
	wt1 := seedReclaimWorktree(t, s, repoID, repoPath, "task/prune-1", "owner_a", ep.ID)
	wt2 := seedReclaimWorktree(t, s, repoID, repoPath, "task/prune-2", "owner_a", ep.ID)
	stray := filepath.Join(s.Worktree.Home, "worktrees", "stray-dir")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	at.Advance(2 * time.Hour)
	calls := recordingGitCalls(s)
	res, err := s.ReclaimWorktreesWith(ctx, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, wt := range []string{wt1.Path, wt2.Path} {
		if r, ok := resultFor(res, wt); !ok || r.Action != "removed" {
			t.Fatalf("%s result = %+v, want removed", wt, r)
		}
	}
	prunes := 0
	for _, c := range *calls {
		if strings.Contains(c, "worktree prune") {
			prunes++
		}
	}
	if prunes != 1 {
		t.Fatalf("git worktree prune ran %d times, want once per repo: %v", prunes, *calls)
	}
	if r, ok := resultFor(res, stray); !ok || r.Action != "untracked" {
		t.Fatalf("stray dir result = %+v, want untracked", r)
	}
	if _, err := os.Stat(filepath.Join(stray, "keep.txt")); err != nil {
		t.Fatalf("an untracked dir must never be deleted: %v", err)
	}
}

func TestReclaimDryRunDeletesNothing(t *testing.T) {
	s, _, at := clockStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	repoID := seedRepo(t, s, "proj")
	repoPath := repoPathFor(t, s, repoID)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	seedReclaimAgent(t, s, "rev", ep.ID, "")
	finishReclaimAgent(t, s, "owner_a", s.Now())
	finishReclaimAgent(t, s, "rev", s.Now())
	merged := seedReclaimWorktree(t, s, repoID, repoPath, "task/dry-merged", "owner_a", ep.ID)
	mustExec(t, s.DB, `INSERT INTO worktree_reservations (worktree_id, agent_id, mode, created_at) VALUES (?, 'rev', 'ro', 1)`, merged.ID)
	unmerged := seedReclaimWorktree(t, s, repoID, repoPath, "task/dry-unmerged", "owner_a", ep.ID)
	commitFile(t, unmerged.Path, "f.txt", "work")
	at.Advance(2 * time.Hour)
	calls := recordingGitCalls(s)
	res, err := s.ReclaimWorktreesWith(ctx, CleanupOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := resultFor(res, merged.Path); r.Action != "would_remove" {
		t.Fatalf("merged result = %+v, want would_remove", r)
	}
	if r, _ := resultFor(res, unmerged.Path); r.Action != "kept" || r.Reason != "unmerged" {
		t.Fatalf("unmerged result = %+v, want kept (unmerged)", r)
	}
	for _, c := range *calls {
		if strings.Contains(c, "worktree remove") || strings.Contains(c, "worktree prune") {
			t.Fatalf("dry run must not delete: %s", c)
		}
	}
	for _, wt := range []string{merged.ID, unmerged.ID} {
		if state, _ := reclaimWorktreeState(t, s, wt); state != "active" {
			t.Fatalf("%s state = %s, want active after a dry run", wt, state)
		}
	}
	if reservationReleased(t, s, merged.ID, "rev") {
		t.Fatal("a dry run must not release reservations")
	}
	if _, err := os.Stat(merged.Path); err != nil {
		t.Fatalf("dry run removed the directory: %v", err)
	}
}

func TestReclaimNoGraceIgnoresTheGracePeriod(t *testing.T) {
	s, _, _ := clockStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	repoID := seedRepo(t, s, "proj")
	repoPath := repoPathFor(t, s, repoID)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	finishReclaimAgent(t, s, "owner_a", s.Now()) // finished just now: inside the grace window
	wt := seedReclaimWorktree(t, s, repoID, repoPath, "task/grace", "owner_a", ep.ID)
	res, err := s.ReclaimWorktreesWith(ctx, CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resultFor(res, wt.Path); ok {
		t.Fatal("inside the grace window the worktree must not be a candidate")
	}
	res, err = s.ReclaimWorktreesWith(ctx, CleanupOptions{NoGrace: true})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := resultFor(res, wt.Path); r.Action != "removed" {
		t.Fatalf("result = %+v, want removed with NoGrace", r)
	}
}

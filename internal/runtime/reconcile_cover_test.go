package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func TestSetItemReposValidatesThenVersionsTheRootScope(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA, repoB := seedRepo(t, s, "chat"), seedRepo(t, s, "api")
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Root"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key, Title: "Child"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetItemRepos(ctx, story.Key, []string{repoA}, 0); !isBadRequest(err, "belongs to the root item") {
		t.Fatalf("child item err = %v", err)
	}
	if err := s.SetItemRepos(ctx, ep.Key, nil, 0); !isBadRequest(err, "at least one repository") {
		t.Fatalf("empty set err = %v", err)
	}
	if err := s.SetItemRepos(ctx, ep.Key, []string{repoA}, 7); !conflictOrNotFound(err, items.CodeConflict) {
		t.Fatalf("stale version err = %v, want conflict", err)
	}
	if err := s.SetItemRepos(ctx, ep.Key, []string{"repo_missing"}, 0); err == nil {
		t.Fatal("unknown repo accepted")
	}
	if got, _ := s.Items.Get(ctx, ep.Key); got.ReposVersion != 0 || len(got.Repos) != 0 {
		t.Fatalf("refused calls wrote scope %v v%d", got.Repos, got.ReposVersion)
	}

	if err := s.SetItemRepos(ctx, ep.Key, []string{repoA, repoB}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := s.Items.Get(ctx, ep.Key)
	if err != nil || got.ReposVersion != 1 || len(got.Repos) != 2 {
		t.Fatalf("scope = %v v%d err=%v, want both repos at version 1", got.Repos, got.ReposVersion, err)
	}
	// The version moved on, so replaying the same stale write is refused.
	if err := s.SetItemRepos(ctx, ep.Key, []string{repoA}, 0); !conflictOrNotFound(err, items.CodeConflict) {
		t.Fatalf("replay err = %v, want conflict", err)
	}
}

func TestReclaimLaunchDirsKeepsLiveAndRecentRemovesStale(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	// A second worker whose session ended long ago.
	reviewer, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleReviewer, Kind: Fake, Model: "fake-1",
		ParentAgentID: w.ParentAgentID, Brief: BriefInput{Objective: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	doneSes, err := s.LatestSession(ctx, reviewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed', ended_at = 1 WHERE id = ?`, doneSes.ID); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(s.Home, "run", "launch")
	mk := func(name string, age time.Duration) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	liveDir := mk(wSes.ID, 0)
	endedDir := mk(doneSes.ID, 0)
	oldOrphan := mk("ses_orphan_old", 30*24*time.Hour)
	newOrphan := mk("ses_orphan_new", time.Minute)
	stray := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.reclaimLaunchDirs(ctx); err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if !exists(liveDir) || !exists(newOrphan) || !exists(stray) {
		t.Fatalf("kept: live=%v newOrphan=%v stray=%v, want all kept", exists(liveDir), exists(newOrphan), exists(stray))
	}
	if exists(endedDir) || exists(oldOrphan) {
		t.Fatalf("removed: ended=%v oldOrphan=%v, want both removed", !exists(endedDir), !exists(oldOrphan))
	}

	// Nothing to do without a launch root or a home.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := s.reclaimLaunchDirs(ctx); err != nil {
		t.Fatalf("missing launch root = %v, want nil", err)
	}
	home := s.Home
	s.Home = ""
	if err := s.reclaimLaunchDirs(ctx); err != nil {
		t.Fatalf("no home = %v, want nil", err)
	}
	s.Home = home
}

func TestPauseAllReportsOnlyWhatItPausedAndLiveDescendantsCount(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if n, err := s.PauseAll(ctx); err != nil || n != 0 {
		t.Fatalf("PauseAll on an idle store = %d, %v; want 0", n, err)
	}
	_, w, _ := worker(t, s)
	n, err := s.PauseAll(ctx)
	if err != nil || n == 0 {
		t.Fatalf("PauseAll = %d, %v; want the live agents paused", n, err)
	}
	ses, err := s.LatestSession(ctx, w.ID)
	if err != nil || ses.State != PauseRequested {
		t.Fatalf("worker session = %v err=%v, want pause_requested", ses.State, err)
	}
	// Everything is already pausing: a second sweep transitions nothing.
	if again, err := s.PauseAll(ctx); err != nil || again != 0 {
		t.Fatalf("second PauseAll = %d, %v; want 0", again, err)
	}
	if _, err := s.PauseAll(canceledCtx()); err == nil {
		t.Fatal("PauseAll swallowed a DB error")
	}

	ds := []descendantRole{{role: roleNone}, {role: roleIdle}, {role: roleMember}}
	if got := liveDescendants(ds); got != 2 {
		t.Fatalf("liveDescendants = %d, want 2", got)
	}
	if liveDescendants(nil) != 0 {
		t.Fatal("liveDescendants(nil) != 0")
	}
}

func TestReclaimWorktreesLoopReturnsOnceTheContextIsCanceled(t *testing.T) {
	s, _, _ := newStore(t)
	// A tick that is always ready: the loop only ever exits through ctx.
	tick := make(chan time.Time)
	close(tick)
	s.After = func(time.Duration) <-chan time.Time { return tick }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		s.ReclaimWorktreesLoop(ctx, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ReclaimWorktreesLoop kept running after its context was canceled")
	}
}

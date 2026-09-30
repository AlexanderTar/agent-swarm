package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkWorkDir creates <home>/work/<name> holding one scratch file.
func mkWorkDir(t *testing.T, s *Store, name string) string {
	t.Helper()
	dir := filepath.Join(s.Home, "work", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scratch.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestReclaimWorkDirsRemovesFinishedAgentPastGrace(t *testing.T) {
	s, _, at := clockStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	finishReclaimAgent(t, s, "owner_a", s.Now())
	dir := mkWorkDir(t, s, "owner_a")
	at.Advance(2 * time.Hour)
	res, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Fatalf("%s should be removed", dir)
	}
	if len(res) != 1 || res[0].Path != dir || res[0].Action != "removed" {
		t.Fatalf("results = %+v", res)
	}
}

func TestReclaimWorkDirsKeepsLiveSessionWithinGraceAndSymlink(t *testing.T) {
	s, _, at := clockStore(t)
	ep := seedEpicWithTask(t, s)
	for _, id := range []string{"live", "fresh", "linked"} {
		seedReclaimAgent(t, s, id, ep.ID, "")
	}
	finishReclaimAgent(t, s, "live", s.Now())
	seedReclaimSession(t, s, "ses_live", "live", Running)
	live := mkWorkDir(t, s, "live")
	at.Advance(2 * time.Hour)
	finishReclaimAgent(t, s, "fresh", s.Now()) // within grace
	fresh := mkWorkDir(t, s, "fresh")
	finishReclaimAgent(t, s, "linked", s.Now().Add(-2*time.Hour))
	outside := t.TempDir()
	link := filepath.Join(s.Home, "work", "linked")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{live, fresh, link, filepath.Join(outside, "keep")} {
		if !exists(p) {
			t.Fatalf("%s must be kept", p)
		}
	}
	// NoGrace makes the fresh one eligible; live and symlink still kept.
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if exists(fresh) || !exists(live) || !exists(link) {
		t.Fatalf("NoGrace: fresh=%v live=%v link=%v", exists(fresh), exists(live), exists(link))
	}
}

func TestReclaimWorkDirsDryRunDeletesNothing(t *testing.T) {
	s, _, at := clockStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "owner_a", ep.ID, "")
	finishReclaimAgent(t, s, "owner_a", s.Now())
	dir := mkWorkDir(t, s, "owner_a")
	at.Advance(2 * time.Hour)
	res, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(dir) || len(res) != 1 || res[0].Action != "would_remove" {
		t.Fatalf("dry run: exists=%v res=%+v", exists(dir), res)
	}
}

func TestReclaimWorkDirsRemovesOldOrphansKeepsFreshOnes(t *testing.T) {
	s, _, at := clockStore(t)
	old := mkWorkDir(t, s, "ghost_old")
	// dirs' mtimes come from the real clock; age the old one explicitly.
	aged := s.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatal(err)
	}
	fresh := mkWorkDir(t, s, "ghost_fresh")
	if err := os.Chtimes(fresh, s.Now(), s.Now()); err != nil {
		t.Fatal(err)
	}
	_ = at
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	if exists(old) || !exists(fresh) {
		t.Fatalf("old exists=%v (want false), fresh exists=%v (want true)", exists(old), exists(fresh))
	}
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if exists(fresh) {
		t.Fatal("NoGrace must remove the fresh orphan too")
	}
}

// seedCwdSession is seedReclaimSession with an explicit cwd.
func seedCwdSession(t *testing.T, s *Store, id, agentID string, state SessionState, cwd string) {
	t.Helper()
	seedReclaimSession(t, s, id, agentID, state)
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE sessions SET cwd = ? WHERE id = ?`, cwd, id); err != nil {
		t.Fatal(err)
	}
}

func TestReclaimWorkDirsKeepsRenamedRunningAgentCwd(t *testing.T) {
	s, _, at := clockStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "auto-clean-swarm-work", ep.ID, "")
	dir := mkWorkDir(t, s, "clean-up-task-directories")
	seedCwdSession(t, s, "ses_r", "auto-clean-swarm-work", Running, dir)
	at.Advance(2 * time.Hour)
	if err := os.Chtimes(dir, s.Now().Add(-3*time.Hour), s.Now().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if !exists(dir) {
		t.Fatal("live session cwd must be kept even though agent name differs")
	}
}

func TestReclaimWorkDirsKeepsNonFinishedAgentCwdWithoutLiveSession(t *testing.T) {
	s, _, _ := clockStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "renamed", ep.ID, "") // state 'active'
	dir := mkWorkDir(t, s, "old-dir-name")
	seedCwdSession(t, s, "ses_e", "renamed", Completed, dir)
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if !exists(dir) {
		t.Fatal("cwd of a non-finished agent must be kept")
	}
}

func TestReclaimWorkDirsRemovesFinishedAgentOldSessionCwd(t *testing.T) {
	s, _, at := clockStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "renamed", ep.ID, "")
	finishReclaimAgent(t, s, "renamed", s.Now())
	dir := mkWorkDir(t, s, "old-dir-name")
	seedCwdSession(t, s, "ses_f", "renamed", Completed, dir)
	at.Advance(2 * time.Hour)
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Fatal("finished agent's old session cwd should be removed")
	}
}

func TestReclaimWorkDirsRefusesSymlinkedRoot(t *testing.T) {
	s, _, _ := clockStore(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Home, "work")); err != nil {
		t.Fatal(err)
	}
	old := s.Now().Add(-5 * time.Hour)
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if !exists(victim) {
		t.Fatal("symlinked work root must be refused")
	}
}

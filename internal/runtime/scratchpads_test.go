package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkScratchpad creates <ScratchRoot>/<slug(workDir)>/<uuid>/scratchpad/x.
func mkScratchpad(t *testing.T, s *Store, workDirName string) string {
	t.Helper()
	return mkScratchpadAt(t, s, filepath.Join(s.Home, "work", workDirName))
}

func mkScratchpadAt(t *testing.T, s *Store, cwd string) string {
	t.Helper()
	dir := filepath.Join(s.ScratchRoot, scratchSlug(cwd))
	if err := os.MkdirAll(filepath.Join(dir, "uuid-1", "scratchpad"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uuid-1", "scratchpad", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// setSessionCwd points agentID's seeded session at cwd.
func setSessionCwd(t *testing.T, s *Store, sessionID, cwd string) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE sessions SET cwd = ? WHERE id = ?`, cwd, sessionID); err != nil {
		t.Fatal(err)
	}
}

func scratchStore(t *testing.T) (*Store, *testClock) {
	t.Helper()
	s, _, at := clockStore(t)
	s.ScratchRoot = t.TempDir()
	return s, at
}

func TestScratchSlugMatchesRealLayout(t *testing.T) {
	got := scratchSlug("/Users/x/.swarm/work/foo-bar")
	if want := "-Users-x--swarm-work-foo-bar"; got != want {
		t.Fatalf("slug = %q, want %q", got, want)
	}
}

func TestReclaimScratchpadsRemovesFinishedPastGraceKeepsLiveAndFresh(t *testing.T) {
	s, at := scratchStore(t)
	ep := seedEpicWithTask(t, s)
	for _, id := range []string{"gone", "live", "fresh"} {
		seedReclaimAgent(t, s, id, ep.ID, "")
		seedReclaimSession(t, s, "ses_"+id, id, Completed)
		setSessionCwd(t, s, "ses_"+id, filepath.Join(s.Home, "work", id))
	}
	finishReclaimAgent(t, s, "gone", s.Now())
	finishReclaimAgent(t, s, "live", s.Now())
	if err := s.SetSessionState(context.Background(), "ses_live", Running); err != nil {
		t.Fatal(err)
	}
	gone, live := mkScratchpad(t, s, "gone"), mkScratchpad(t, s, "live")
	at.Advance(2 * time.Hour)
	finishReclaimAgent(t, s, "fresh", s.Now())
	fresh := mkScratchpad(t, s, "fresh")

	res, err := s.ReclaimScratchpads(context.Background(), CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exists(gone) || !exists(live) || !exists(fresh) {
		t.Fatalf("gone=%v live=%v fresh=%v", exists(gone), exists(live), exists(fresh))
	}
	var removed int
	for _, r := range res {
		if r.Action == "removed" && r.Path == gone {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("results = %+v", res)
	}
	// NoGrace makes the fresh one eligible; live stays.
	if _, err := s.ReclaimScratchpads(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	if exists(fresh) || !exists(live) {
		t.Fatalf("after no-grace: fresh=%v live=%v", exists(fresh), exists(live))
	}
}

func TestReclaimScratchpadsDoesNotConfuseFooWithFoo2(t *testing.T) {
	s, at := scratchStore(t)
	ep := seedEpicWithTask(t, s)
	for _, id := range []string{"foo", "foo-2"} {
		seedReclaimAgent(t, s, id, ep.ID, "")
		seedReclaimSession(t, s, "ses_"+id, id, Completed)
		setSessionCwd(t, s, "ses_"+id, filepath.Join(s.Home, "work", id))
	}
	finishReclaimAgent(t, s, "foo", s.Now())
	if err := s.SetSessionState(context.Background(), "ses_foo-2", Running); err != nil {
		t.Fatal(err)
	}
	foo, foo2 := mkScratchpad(t, s, "foo"), mkScratchpad(t, s, "foo-2")
	at.Advance(2 * time.Hour)
	if _, err := s.ReclaimScratchpads(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	if exists(foo) || !exists(foo2) {
		t.Fatalf("foo=%v foo-2=%v", exists(foo), exists(foo2))
	}
}

func TestReclaimScratchpadsOrphanByMtimeAndForeignUntouched(t *testing.T) {
	s, at := scratchStore(t)
	old := mkScratchpad(t, s, "ghost")
	fresh := mkScratchpad(t, s, "newghost")
	foreign := mkScratchpadAt(t, s, "/Users/someone/code/project")
	otherHome := mkScratchpadAt(t, s, filepath.Join(filepath.Dir(s.Home), "elsewhere", "work", "ghost"))
	past := s.Now().Add(-3 * time.Hour)
	for _, p := range []string{old, foreign, otherHome} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	at.Advance(time.Minute)
	if _, err := s.ReclaimScratchpads(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	if exists(old) {
		t.Fatal("old orphan should go")
	}
	for name, p := range map[string]string{"fresh": fresh, "foreign": foreign, "otherHome": otherHome} {
		if !exists(p) {
			t.Fatalf("%s must be kept", name)
		}
	}
}

func TestReclaimScratchpadsNeverFollowsSymlinksOrTouchesFiles(t *testing.T) {
	s, _ := scratchStore(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.ScratchRoot, scratchSlug(filepath.Join(s.Home, "work", "linked")))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(s.ScratchRoot, scratchSlug(filepath.Join(s.Home, "work", "afile")))
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := s.Now().Add(-3 * time.Hour)
	_ = os.Chtimes(file, past, past)
	if _, err := s.ReclaimScratchpads(context.Background(), CleanupOptions{NoGrace: true}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, file, filepath.Join(outside, "keep")} {
		if !exists(p) {
			t.Fatalf("%s must be kept", p)
		}
	}
}

func TestReclaimScratchpadsDryRunRemovesNothing(t *testing.T) {
	s, at := scratchStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "done", ep.ID, "")
	seedReclaimSession(t, s, "ses_done", "done", Completed)
	setSessionCwd(t, s, "ses_done", filepath.Join(s.Home, "work", "done"))
	finishReclaimAgent(t, s, "done", s.Now())
	dir := mkScratchpad(t, s, "done")
	at.Advance(2 * time.Hour)
	res, err := s.ReclaimScratchpads(context.Background(), CleanupOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(dir) || len(res) != 1 || res[0].Action != "would_remove" || res[0].Path != dir {
		t.Fatalf("exists=%v res=%+v", exists(dir), res)
	}
}

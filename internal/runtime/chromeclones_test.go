package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkClone creates <root>/<app>.code_sign_clone/code_sign_clone.<id>/bundle,
// last modified age before the store's clock.
func mkClone(t *testing.T, s *Store, root, app, id string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, app+".code_sign_clone", "code_sign_clone."+id)
	if err := os.MkdirAll(filepath.Join(dir, "Chrome.app"), 0o700); err != nil {
		t.Fatal(err)
	}
	when := s.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
	return dir
}

func cloneStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, _ := scratchStore(t)
	s.CloneRoot = t.TempDir()
	s.OpenPaths = func() ([]string, error) { return nil, nil }
	(&fakeProcs{}).install(s)
	return s, s.CloneRoot
}

func cloneResult(rs []CleanupResult, path string) *CleanupResult {
	for i := range rs {
		if rs[i].Path == path {
			return &rs[i]
		}
	}
	return nil
}

func TestReclaimChromeClonesRemovesStaleUnheldKeepsHeldAndFresh(t *testing.T) {
	s, root := cloneStore(t)
	stale := mkClone(t, s, root, "com.google.Chrome", "AAA", 2*time.Hour)
	comet := mkClone(t, s, root, "ai.perplexity.comet", "BBB", 2*time.Hour)
	fresh := mkClone(t, s, root, "com.google.Chrome", "CCC", time.Minute)
	heldFile := mkClone(t, s, root, "com.google.Chrome", "DDD", 2*time.Hour)
	heldProc := mkClone(t, s, root, "com.google.Chrome", "EEE", 2*time.Hour)
	s.OpenPaths = func() ([]string, error) {
		return []string{"/etc/hosts", filepath.Join(heldFile, "Chrome.app/Contents/MacOS/Chrome")}, nil
	}
	(&fakeProcs{procs: []OSProc{{PID: 9, Args: filepath.Join(heldProc, "Chrome.app/Contents/MacOS/Chrome") + " --headless"}}}).install(s)
	other := filepath.Join(root, "notes")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}

	res, err := s.ReclaimChromeClones(context.Background(), CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exists(stale) || exists(comet) {
		t.Fatalf("stale unheld clones should be removed: %v %v", exists(stale), exists(comet))
	}
	for _, p := range []string{fresh, heldFile, heldProc, other} {
		if !exists(p) {
			t.Fatalf("%s must be kept", p)
		}
	}
	if r := cloneResult(res, stale); r == nil || r.Action != "removed" {
		t.Fatalf("stale result = %+v (all %+v)", r, res)
	}
	for _, p := range []string{fresh, heldFile, heldProc} {
		if r := cloneResult(res, p); r == nil || r.Action != "kept" || r.Reason == "" {
			t.Fatalf("%s result = %+v", p, r)
		}
	}
	if cloneResult(res, other) != nil {
		t.Fatalf("non-clone dir reported: %+v", res)
	}
}

func TestReclaimChromeClonesDryRunAndNoGrace(t *testing.T) {
	s, root := cloneStore(t)
	young := mkClone(t, s, root, "com.google.Chrome", "AAA", time.Minute)
	res, err := s.ReclaimChromeClones(context.Background(), CleanupOptions{DryRun: true, NoGrace: true})
	if err != nil || !exists(young) {
		t.Fatalf("dry run deleted: err=%v", err)
	}
	if r := cloneResult(res, young); r == nil || r.Action != "would_remove" {
		t.Fatalf("result = %+v", r)
	}
	if _, err := s.ReclaimChromeClones(context.Background(), CleanupOptions{NoGrace: true}); err != nil || exists(young) {
		t.Fatalf("no-grace should remove an unheld young clone: err=%v", err)
	}
}

func TestReclaimChromeClonesKeepsEverythingWhenOpenFilesUnknown(t *testing.T) {
	s, root := cloneStore(t)
	stale := mkClone(t, s, root, "com.google.Chrome", "AAA", 2*time.Hour)
	s.OpenPaths = func() ([]string, error) { return nil, os.ErrPermission }
	res, err := s.ReclaimChromeClones(context.Background(), CleanupOptions{})
	if err == nil || !exists(stale) {
		t.Fatalf("err=%v exists=%v: must not delete when lsof fails", err, exists(stale))
	}
	_ = res
}

func TestReclaimChromeClonesMissingRootIsNoop(t *testing.T) {
	s, _ := cloneStore(t)
	s.CloneRoot = filepath.Join(s.CloneRoot, "nope")
	res, err := s.ReclaimChromeClones(context.Background(), CleanupOptions{})
	if err != nil || len(res) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

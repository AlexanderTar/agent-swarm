package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNestedSwarmHome builds a fake user home containing a fake swarm home
// with only one shared dir pre-created; linkSwarmShared must create the
// missing real dirs itself (otherwise an absent real dir is skipped and the
// agent's mkdir -p recreates the bug inside the isolated HOME).
func fakeNestedSwarmHome(t *testing.T) (userHome, swarmHome string) {
	t.Helper()
	userHome = t.TempDir()
	swarmHome = filepath.Join(userHome, ".swarm")
	if err := os.MkdirAll(filepath.Join(swarmHome, "screenshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(swarmHome, "screenshots", "seed.png"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	return userHome, swarmHome
}

func quietLog(string, ...any) {}

// TestLinkSwarmSharedLinksAllowlistedDirs: every agent-facing subdir of the
// real swarm home resolves from the isolated HOME to the real dir (real dirs
// created when missing), while run/, db files and config.json never appear
// there, and the isolated .swarm itself is a real directory, not a link.
func TestLinkSwarmSharedLinksAllowlistedDirs(t *testing.T) {
	userHome, swarmHome := fakeNestedSwarmHome(t)
	isolatedHome := t.TempDir()

	if err := linkSwarmShared(isolatedHome, userHome, swarmHome, quietLog); err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(isolatedHome, ".swarm")
	if fi, err := os.Lstat(base); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf(".swarm base = %v, %v; want a real directory", fi, err)
	}
	for _, sub := range []string{"screenshots", "specs", "plans", "research", "designs", "kb"} {
		dst := filepath.Join(base, sub)
		got, err := filepath.EvalSymlinks(dst)
		if err != nil {
			t.Errorf("%s: EvalSymlinks: %v", sub, err)
			continue
		}
		// Both sides are evaluated: on macOS t.TempDir() sits under /var,
		// itself a symlink to /private/var.
		want, err := filepath.EvalSymlinks(filepath.Join(swarmHome, sub))
		if err != nil {
			t.Errorf("%s: EvalSymlinks(real): %v", sub, err)
			continue
		}
		if got != want {
			t.Errorf("%s resolves to %q, want %q", sub, got, want)
		}
		if fi, err := os.Stat(filepath.Join(swarmHome, sub)); err != nil || !fi.IsDir() {
			t.Errorf("real %s dir missing after link: %v %v", sub, fi, err)
		}
	}
	// The seed file written before the link must still read through it.
	if _, err := os.Stat(filepath.Join(base, "screenshots", "seed.png")); err != nil {
		t.Errorf("seed file lost through the link: %v", err)
	}
	for _, absent := range []string{"run", "config.json", "swarm.db"} {
		if _, err := os.Lstat(filepath.Join(base, absent)); !os.IsNotExist(err) {
			t.Errorf("isolated .swarm must not carry %s, got err=%v", absent, err)
		}
	}
}

// TestLinkSwarmSharedIsIdempotent: setupEnv reruns against the same launchDir
// on Resume, so a second call must succeed and keep every link.
func TestLinkSwarmSharedIsIdempotent(t *testing.T) {
	userHome, swarmHome := fakeNestedSwarmHome(t)
	isolatedHome := t.TempDir()

	for i := 0; i < 2; i++ {
		if err := linkSwarmShared(isolatedHome, userHome, swarmHome, quietLog); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	for _, sub := range []string{"screenshots", "specs", "plans", "research", "designs", "kb"} {
		got, err := filepath.EvalSymlinks(filepath.Join(isolatedHome, ".swarm", sub))
		want, werr := filepath.EvalSymlinks(filepath.Join(swarmHome, sub))
		if err != nil || werr != nil || got != want {
			t.Errorf("%s resolves to %q, %v after rerun", sub, got, err)
		}
	}
}

// TestLinkSwarmSharedReplacesStaleLinks: a dangling or wrong-pointing link
// (a past layout) is replaced, never left behind.
func TestLinkSwarmSharedReplacesStaleLinks(t *testing.T) {
	userHome, swarmHome := fakeNestedSwarmHome(t)
	isolatedHome := t.TempDir()
	base := filepath.Join(isolatedHome, ".swarm")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("nowhere"), filepath.Join(base, "screenshots")); err != nil {
		t.Fatal(err)
	}

	if err := linkSwarmShared(isolatedHome, userHome, swarmHome, quietLog); err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(filepath.Join(base, "screenshots"))
	want, werr := filepath.EvalSymlinks(filepath.Join(swarmHome, "screenshots"))
	if err != nil || werr != nil || got != want {
		t.Fatalf("stale link now resolves to %q, %v", got, err)
	}
}

// TestLinkSwarmSharedPreservesRealContent: a real directory the agent itself
// created (e.g. mkdir -p before this fix) is never removed -- only empty
// real dirs are taken over, and real content always survives.
func TestLinkSwarmSharedPreservesRealContent(t *testing.T) {
	userHome, swarmHome := fakeNestedSwarmHome(t)
	isolatedHome := t.TempDir()
	legacy := filepath.Join(isolatedHome, ".swarm", "screenshots")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "agent-shot.png"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := linkSwarmShared(isolatedHome, userHome, swarmHome, quietLog); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("a real dir holding agent content must not become a symlink")
	}
	if _, err := os.Stat(filepath.Join(legacy, "agent-shot.png")); err != nil {
		t.Fatalf("agent content lost: %v", err)
	}
}

// TestLinkSwarmSharedOutsideUserHomeIsNoop: a swarm home that does not sit
// under the user home leaves the isolated HOME untouched (no regression for
// custom --home layouts).
func TestLinkSwarmSharedOutsideUserHomeIsNoop(t *testing.T) {
	userHome := t.TempDir()
	swarmHome := t.TempDir()
	isolatedHome := t.TempDir()

	if err := linkSwarmShared(isolatedHome, userHome, swarmHome, quietLog); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(isolatedHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("isolated HOME must stay empty, got %v", entries)
	}
}

// TestLinkSwarmSharedHasNoCycle: no symlink under the isolated .swarm may
// resolve back inside the isolated HOME (the run/ cycle this fix removes).
func TestLinkSwarmSharedHasNoCycle(t *testing.T) {
	userHome, swarmHome := fakeNestedSwarmHome(t)
	isolatedHome := t.TempDir()

	if err := linkSwarmShared(isolatedHome, userHome, swarmHome, quietLog); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(isolatedHome, ".swarm")
	err := filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		got, err := filepath.EvalSymlinks(p)
		if err != nil {
			return err
		}
		if got == isolatedHome || strings.HasPrefix(got, isolatedHome+string(os.PathSeparator)) {
			t.Errorf("%s resolves back inside the isolated HOME: %q", p, got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

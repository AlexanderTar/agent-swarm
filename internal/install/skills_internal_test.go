package install

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// A cycle must be skipped before any partial copy of that entry is created;
// independent siblings still get copied.
func TestCopyTreeDetectsASymlinkCycle(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(a, "b")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, filepath.Join(b, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "sibling.md"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := copyTree(a, dst); err != nil {
		t.Fatalf("cycle should be skipped: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "b", "loop")); !os.IsNotExist(err) {
		t.Errorf("cycle entry should be absent, got %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "b", "sibling.md")); err != nil || string(got) != "keep" {
		t.Errorf("sibling = %q, %v; want keep", got, err)
	}
}

func TestCopyTreeSkipsSelfReferentialLink(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(src, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sibling.md"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("self-referential link should be skipped: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "loop")); !os.IsNotExist(err) {
		t.Errorf("loop entry should be absent: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "sibling.md")); err != nil || string(got) != "keep" {
		t.Errorf("sibling = %q, %v; want keep", got, err)
	}
}

// Review round 1, Major 4: syncSkills is the fs.FS-generic primitive behind
// SyncSkills (bound to the real embedded tree); tested here against a
// synthetic fstest.MapFS so its shape -- flattening a vendored skill, and the
// scripts/ exec bit -- is exercised without touching the real embed.
func TestSyncSkillsPrimitiveFlattensVendoredSkillsAndSetsExecBits(t *testing.T) {
	fsys := fstest.MapFS{
		"vendor/x/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: x\ndescription: d\n---\nbody\n")},
		"y/SKILL.md":        &fstest.MapFile{Data: []byte("---\nname: y\ndescription: d\n---\nbody\n")},
		"y/scripts/run.py":  &fstest.MapFile{Data: []byte("#!/usr/bin/env python3\n")},
		"y/LICENSE":         &fstest.MapFile{Data: []byte("MIT\n")},
		"y/data/a/b.csv":    &fstest.MapFile{Data: []byte("a,b\n")},
	}
	dst := t.TempDir()
	if _, err := syncSkills(fsys, dst); err != nil {
		t.Fatal(err)
	}

	// The vendored skill "x" (embedded at vendor/x) lands flattened, directly
	// under dst/x, not dst/vendor/x.
	if _, err := os.Stat(filepath.Join(dst, "x", "SKILL.md")); err != nil {
		t.Errorf("vendored skill was not flattened to dst/x: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "vendor")); !os.IsNotExist(err) {
		t.Errorf("a vendor/ path leaked into dst: %v", err)
	}

	for path, wantMode := range map[string]os.FileMode{
		filepath.Join(dst, "y", "scripts", "run.py"):  0o755,
		filepath.Join(dst, "y", "LICENSE"):            0o644,
		filepath.Join(dst, "y", "data", "a", "b.csv"): 0o644,
		filepath.Join(dst, "y", "SKILL.md"):           0o644,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if fi.Mode().Perm() != wantMode {
			t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), wantMode)
		}
	}

	// Every extracted skill dir is marked managed.
	for _, name := range []string{"x", "y"} {
		if _, err := os.Stat(filepath.Join(dst, name, ManagedMarker)); err != nil {
			t.Errorf("%s: missing marker: %v", name, err)
		}
	}
}

// Copy mode (applyLink/copyTreeSynced) must preserve the exec bit syncSkills
// gave a scripts/ file when mirroring it into a kind's own skills root, not
// silently reset it to the default 0644.
func TestCopyModePreservesTheExecBitFromSkillsHome(t *testing.T) {
	fsys := fstest.MapFS{
		"y/SKILL.md":       &fstest.MapFile{Data: []byte("---\nname: y\ndescription: d\n---\nbody\n")},
		"y/scripts/run.py": &fstest.MapFile{Data: []byte("#!/usr/bin/env python3\n")},
	}
	skillsHome := t.TempDir()
	if _, err := syncSkills(fsys, skillsHome); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	dstY := filepath.Join(dst, "y")
	if _, err := applyLink(dstY, filepath.Join(skillsHome, "y"), Copy); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dstY, "scripts", "run.py"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("copied scripts/run.py mode = %v, want 0755", fi.Mode().Perm())
	}
}

// A skill that is renamed or retired must not linger forever under a synced
// root just because fsys no longer embeds it (review round 1, Minor 6).
func TestSyncSkillsPrimitivePrunesARetiredSkill(t *testing.T) {
	dst := t.TempDir()
	fsys1 := fstest.MapFS{
		"old/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: old\ndescription: d\n---\nbody\n")},
	}
	if _, err := syncSkills(fsys1, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "old", "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	fsys2 := fstest.MapFS{
		"new/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: new\ndescription: d\n---\nbody\n")},
	}
	if _, err := syncSkills(fsys2, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "old")); !os.IsNotExist(err) {
		t.Errorf("the retired skill directory survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "new", "SKILL.md")); err != nil {
		t.Errorf("the new skill was not synced: %v", err)
	}
}

// Review round 1, Minor 10: a `*.swarm-tmp` file is WriteIfChanged's own
// in-flight write-then-rename step. A concurrent sync (the daemon and a
// `swarm install` can run at the same time) must never delete it out from
// under that rename.
func TestPruneUnkeptSkipsInFlightTempFiles(t *testing.T) {
	dst := t.TempDir()
	tmp := filepath.Join(dst, "SKILL.md.swarm-tmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pruneUnkept(dst, map[string]bool{dst: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("pruneUnkept removed an in-flight .swarm-tmp file: %v", err)
	}
}

package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// seedKindSkills writes swarm, swarm-coder, swarm-reviewer and a personal
// skill under dir, the way `swarm install` plus the user leave a kind's real
// skills root.
func seedKindSkills(t *testing.T, dir string) {
	t.Helper()
	for _, n := range []string{"swarm", "swarm-coder", "swarm-reviewer", "my-skill"} {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n, "SKILL.md"), []byte("# "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// wantCoderSkillsDir: a real directory (not a whole-dir symlink) holding the
// coder's swarm skills and the personal one, but no other role's.
func wantCoderSkillsDir(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Fatalf("%s must be a real directory of per-entry links: %v", dir, err)
	}
	for _, n := range []string{"swarm", "swarm-coder", "my-skill"} {
		if _, err := os.Stat(filepath.Join(dir, n, "SKILL.md")); err != nil {
			t.Errorf("%s missing: %v", n, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "swarm-reviewer")); err == nil {
		t.Errorf("swarm-reviewer linked for a coder")
	}
}

func TestCodexLaunchLinksFilteredSkillsPerEntry(t *testing.T) {
	d := testDeps(t)
	seedKindSkills(t, filepath.Join(d.UserHome, ".codex", "skills"))
	s := codexSpec(t)
	s.Role = "coder"
	l, err := newCodex(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	wantCoderSkillsDir(t, filepath.Join(l.Env["CODEX_HOME"], "skills"))
}

func TestAgyLaunchLinksFilteredSkillsPerEntry(t *testing.T) {
	d := testDeps(t)
	seedKindSkills(t, filepath.Join(d.UserHome, ".gemini", "config", "skills"))
	s := agySpec(t)
	s.Role = "coder"
	l, err := newAgy(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	wantCoderSkillsDir(t, filepath.Join(l.Env["HOME"], ".gemini", "config", "skills"))
}

func TestMuseLaunchLinksOnlyItsRoleSwarmSkills(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	s := museSpec(t)
	s.Role = "coder"
	l, err := newMuse(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Env["XDG_CONFIG_HOME"], "muse", "skills")
	if _, err := os.Lstat(filepath.Join(dir, "swarm-coder")); err != nil {
		t.Errorf("swarm-coder missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "swarm-reviewer")); err == nil {
		t.Error("swarm-reviewer linked for a coder")
	}
}

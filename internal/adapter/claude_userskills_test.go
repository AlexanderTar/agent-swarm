package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func seedUserSkill(t *testing.T, userHome, name, body string) string {
	t.Helper()
	src := filepath.Join(userHome, ".claude", "skills", name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestClaudeLaunchLinksUserSkills(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	src := seedUserSkill(t, d.UserHome, "my-skill", "# Mine\n")
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.Cwd, ".claude", "skills", "my-skill")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("expected user skill link in the session cwd: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", link)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != src {
		t.Fatalf("%s -> %s, want %s", link, target, src)
	}
	got, err := os.ReadFile(filepath.Join(link, "SKILL.md"))
	if err != nil || string(got) != "# Mine\n" {
		t.Fatalf("user skill body = %q, err = %v", got, err)
	}
}

func TestClaudeLaunchSkipsSwarmRegisteredUserSkillNames(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	seedUserSkill(t, d.UserHome, "swarm", "# User impostor\n")
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.Cwd, ".claude", "skills", "swarm")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(d.Home, "skills", "swarm")
	if target != want {
		t.Fatalf("swarm skill -> %s, want swarm-owned %s (swarm names win)", target, want)
	}
	got, err := os.ReadFile(filepath.Join(link, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(install.SkillBody("swarm")) {
		t.Fatalf("swarm skill body was overwritten by the user entry")
	}
}

func TestClaudeLaunchUserSkillLinkIsIdempotent(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	src := seedUserSkill(t, d.UserHome, "my-skill", "# Mine\n")
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatalf("relaunch with the same cwd failed: %v", err)
	}
	link := filepath.Join(s.Cwd, ".claude", "skills", "my-skill")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != src {
		t.Fatalf("after relaunch %s -> %s, want %s", link, target, src)
	}
}

func TestClaudeLaunchSurvivesNonDirUserSkills(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	skillsPath := filepath.Join(d.UserHome, ".claude", "skills")
	if err := os.MkdirAll(filepath.Dir(skillsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillsPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatalf("Launch with a non-dir user skills root failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Cwd, ".claude", "skills", "swarm", "SKILL.md")); err != nil {
		t.Fatalf("expected the swarm skill despite a non-dir user root: %v", err)
	}
}

func TestClaudeLaunchSurvivesUnreadableUserSkills(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	seedUserSkill(t, d.UserHome, "my-skill", "# Mine\n")
	skillsPath := filepath.Join(d.UserHome, ".claude", "skills")
	if err := os.Chmod(skillsPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(skillsPath, 0o755) })
	if err := os.WriteFile(filepath.Join(skillsPath, ".probe"), []byte("x"), 0o644); err == nil {
		os.Remove(filepath.Join(skillsPath, ".probe"))
		t.Skip("unreadable-dir probe needs a non-privileged user")
	}
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatalf("Launch with an unreadable user skills root failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Cwd, ".claude", "skills", "swarm", "SKILL.md")); err != nil {
		t.Fatalf("expected the swarm skill despite an unreadable user root: %v", err)
	}
}

func TestClaudeLaunchSkipsUnlinkableUserSkillEntry(t *testing.T) {
	d := testDeps(t)
	seedSkillsHome(t, d.Home)
	seedUserSkill(t, d.UserHome, "good-skill", "# Good\n")
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	goodLink := filepath.Join(s.Cwd, ".claude", "skills", "good-skill")
	if _, err := os.Lstat(goodLink); err != nil {
		t.Fatalf("expected the good skill linked after the first launch: %v", err)
	}
	skillsRoot := filepath.Join(s.Cwd, ".claude", "skills")
	if err := os.Chmod(skillsRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(skillsRoot, 0o755) })
	probe := filepath.Join(skillsRoot, ".probe")
	if err := os.Symlink("x", probe); err == nil {
		os.Remove(probe)
		t.Skip("read-only-dir probe needs a non-privileged user")
	}
	seedUserSkill(t, d.UserHome, "bad-skill", "# Bad\n")
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatalf("relaunch with an unlinkable user skill failed: %v", err)
	}
	target, err := os.Readlink(goodLink)
	if err != nil {
		t.Fatalf("expected the good skill still linked: %v", err)
	}
	if want := filepath.Join(d.UserHome, ".claude", "skills", "good-skill"); target != want {
		t.Fatalf("good skill -> %s, want %s", target, want)
	}
}

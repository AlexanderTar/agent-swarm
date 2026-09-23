package install_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func TestSkillBodyCarriesTheSpecFrontmatterAndLastRule(t *testing.T) {
	agent := string(install.SkillBody("swarm"))
	for _, want := range []string{
		"name: swarm\n",
		"description: Rules for any agent spawned by Agent Swarm (SWARM_SESSION is set).",
		"# Working as a Swarm agent",
		"Never add `Co-Authored-By`, \"Generated with\", session links, emoji signatures or agent names",
		"Reviewers: check that the tests cover each acceptance criterion and would fail without the change.",
	} {
		if !strings.Contains(agent, want) {
			t.Errorf("skills/swarm/SKILL.md is missing %q", want)
		}
	}
	orch := string(install.SkillBody("swarm-orchestrator"))
	for _, want := range []string{
		"name: swarm-orchestrator\n",
		"# Orchestrating with Swarm",
		"superpowers:brainstorming",
		"superpowers:systematic-debugging",
		"Then write `completed`, remove your worktrees with `swarm_worktree` `op: \"remove\"`, and stop.",
	} {
		if !strings.Contains(orch, want) {
			t.Errorf("skills/swarm-orchestrator/SKILL.md is missing %q", want)
		}
	}
	// The fence lines must not have been copied in with the body.
	for name, body := range map[string]string{"swarm": agent, "swarm-orchestrator": orch} {
		if strings.Contains(body, "~~~") {
			t.Errorf("%s still carries a spec fence line", name)
		}
		if !strings.HasPrefix(body, "---\n") {
			t.Errorf("%s does not start with frontmatter: %.20q", name, body)
		}
	}
}

func TestSkillBodyPanicsOnAnUnknownName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic: a typo'd skill name must fail at the call site, not install an empty file")
		}
	}()
	install.SkillBody("swarm-nope")
}

// §18: both skills are installed for every agent, at the four listed roots.
func TestWriteSkillsInstallsBothForEveryAgentAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	for _, k := range install.Kinds {
		changed, err := install.WriteSkills(c, k)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if len(changed) != 2 {
			t.Errorf("%s changed %v, want both skill files", k, changed)
		}
		for _, name := range []string{"swarm", "swarm-orchestrator"} {
			p := filepath.Join(c.SkillsDir(k), name, "SKILL.md")
			body, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			if !strings.HasPrefix(string(body), "---\n") {
				t.Errorf("%s: %.20q", p, body)
			}
		}
		again, err := install.WriteSkills(c, k)
		if err != nil || len(again) != 0 {
			t.Errorf("%s second call changed %v, %v; must be a no-op", k, again, err)
		}
	}
}

func TestWriteSkillsStaysInsideTheGivenHome(t *testing.T) {
	home := t.TempDir()
	c := install.Config{UserHome: home, Home: filepath.Join(home, ".swarm")}
	if _, err := install.WriteSkills(c, install.KindClaude); err != nil {
		t.Fatal(err)
	}
	// S-5: nothing may land outside the fake home.
	err := filepath.WalkDir(home, func(p string, _ os.DirEntry, err error) error { return err })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "swarm", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedSkillsMatchTheCanonicalFiles(t *testing.T) {
	for _, name := range install.SkillNames {
		want, err := os.ReadFile(filepath.Join("..", "..", "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(install.SkillBody(name)) != string(want) {
			t.Errorf("internal/install/skills/%s/SKILL.md has drifted from skills/%s/SKILL.md; run make skills-sync", name, name)
		}
	}
}

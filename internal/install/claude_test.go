package install_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func TestWriteClaudeInstallsSkillsAndTouchesNothingElse(t *testing.T) {
	c := fakeHome(t)
	changed, err := install.WriteClaude(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %v, want the two skill files only", changed)
	}
	for _, name := range install.SkillNames {
		if _, err := os.Stat(c.Claude("skills", name, "SKILL.md")); err != nil {
			t.Error(err)
		}
	}
	// §11.5: trust lives only in ~/.claude.json, which Swarm never edits.
	if _, err := os.Stat(filepath.Join(c.UserHome, ".claude.json")); !os.IsNotExist(err) {
		t.Error("~/.claude.json must never be created or edited by Swarm (§11.5)")
	}
	// §11.1: claude's MCP and hooks are per launch, so install writes no global file.
	for _, p := range []string{c.Claude("mcp.json"), c.Claude("settings.json"), c.Claude("hooks.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("install must not write %s; claude is configured per launch (§11.1)", p)
		}
	}
	again, err := install.WriteClaude(c)
	if err != nil || len(again) != 0 {
		t.Fatalf("second WriteClaude changed %v, %v", again, err)
	}
}

// §20: the v1 link is removed as a link. Cursor reads this folder too, so a stale
// swarm-status skill here would still be loaded.
func TestRemoveLegacyClaudeRemovesTheV1SkillLinkAsALink(t *testing.T) {
	c := fakeHome(t)
	target := filepath.Join(c.Home, "app", "current", "plugin")
	if err := os.MkdirAll(filepath.Join(target, "skills", "swarm-status"), 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(target, "skills", "swarm-status", "SKILL.md")
	if err := os.WriteFile(canary, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := c.Claude("skills", "swarm")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	changed, err := install.RemoveLegacyClaude(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != link {
		t.Fatalf("changed = %v, want [%s]", changed, link)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("the link survived")
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatal("the symlink target was followed and deleted (§20)")
	}
}

// After WriteClaude the real skill directory exists; removal must not delete it.
func TestRemoveLegacyClaudeLeavesTheNewSkillDirectoryAlone(t *testing.T) {
	c := fakeHome(t)
	if _, err := install.WriteClaude(c); err != nil {
		t.Fatal(err)
	}
	changed, err := install.RemoveLegacyClaude(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %v; a real skills/swarm folder written by v2 is not legacy", changed)
	}
	if _, err := os.Stat(c.Claude("skills", "swarm", "SKILL.md")); err != nil {
		t.Fatal("the v2 skill was deleted")
	}
}

func TestCheckClaudeReportsTheSkillsAndTheLeftoverLink(t *testing.T) {
	c := fakeHome(t)
	run := (&execx.Fake{Responses: map[string]execx.Result{}}).Runner()
	if ch := findCheck(t, install.CheckClaude(context.Background(), c, run), "Claude skills"); ch.OK {
		t.Error("Claude skills must fail before install")
	}
	if _, err := install.WriteClaude(c); err != nil {
		t.Fatal(err)
	}
	ch := findCheck(t, install.CheckClaude(context.Background(), c, run), "Claude skills")
	if !ch.OK || !strings.Contains(ch.Detail, "skills") {
		t.Errorf("Claude skills = %+v", ch)
	}
}

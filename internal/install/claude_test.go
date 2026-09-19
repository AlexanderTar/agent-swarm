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

func claudeMCPFake(bin string) *execx.Fake {
	return &execx.Fake{Responses: map[string]execx.Result{
		"claude mcp remove swarm -s user":                 {Out: "removed"},
		"claude mcp add swarm -s user -- " + bin + " mcp": {Out: "added"},
	}}
}

func TestWriteClaudeInstallsSkillsAndTouchesNoLocalFile(t *testing.T) {
	c := fakeHome(t)
	run := claudeMCPFake(c.Bin).Runner()
	changed, err := install.WriteClaude(context.Background(), c, run)
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
	// §11.5: trust lives only in ~/.claude.json, which Swarm never edits directly
	// (the global MCP registration goes through the claude CLI itself, never a
	// hand-written edit to the file).
	if _, err := os.Stat(filepath.Join(c.UserHome, ".claude.json")); !os.IsNotExist(err) {
		t.Error("~/.claude.json must never be created or edited directly by Swarm (§11.5)")
	}
	// §11.1: a spawned session's MCP and hooks are still built per launch, so
	// install writes no local global file for those.
	for _, p := range []string{c.Claude("mcp.json"), c.Claude("settings.json"), c.Claude("hooks.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("install must not write %s; a spawned claude session is configured per launch (§11.1)", p)
		}
	}
	again, err := install.WriteClaude(context.Background(), c, run)
	if err != nil || len(again) != 0 {
		t.Fatalf("second WriteClaude changed %v, %v", again, err)
	}
}

// §11.1, M3: the global swarm MCP server is registered via the CLI (remove-then-add,
// so a stale binary path from an older build is replaced, not left alongside a new one).
func TestWriteClaudeRegistersTheGlobalMCPServer(t *testing.T) {
	c := fakeHome(t)
	f := claudeMCPFake(c.Bin)
	if _, err := install.WriteClaude(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(f.Calls(), "\n")
	for _, want := range []string{
		"claude mcp remove swarm -s user",
		"claude mcp add swarm -s user -- " + c.Bin + " mcp",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing call %q; calls =\n%s", want, calls)
		}
	}
}

// WriteClaude must remove a leftover v1 symlink before writing the v2 skills:
// the two occupy the same path, and WriteIfChanged's MkdirAll/WriteFile/Rename
// would otherwise transparently follow the stale link into the v1 release target.
func TestWriteClaudeRemovesALeftoverV1LinkBeforeWriting(t *testing.T) {
	c := fakeHome(t)
	target := filepath.Join(c.Home, "app", "current", "plugin", "skills")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(target, "canary")
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

	if _, err := install.WriteClaude(context.Background(), c, claudeMCPFake(c.Bin).Runner()); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("skills/swarm is still a symlink; the v1 link was not replaced")
	}
	if !fi.IsDir() {
		t.Error("skills/swarm is not a real directory")
	}
	if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
		t.Error(err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "canary" {
		t.Errorf("v1 target dir = %v, want only the canary file untouched", entries)
	}
	again, err := install.RemoveLegacyClaude(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("RemoveLegacyClaude changed %v after WriteClaude, want none left to remove", again)
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
	if _, err := install.WriteClaude(context.Background(), c, claudeMCPFake(c.Bin).Runner()); err != nil {
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
	if _, err := install.WriteClaude(context.Background(), c, claudeMCPFake(c.Bin).Runner()); err != nil {
		t.Fatal(err)
	}
	ch := findCheck(t, install.CheckClaude(context.Background(), c, run), "Claude skills")
	if !ch.OK || !strings.Contains(ch.Detail, "skills") {
		t.Errorf("Claude skills = %+v", ch)
	}
}

package install_test

import (
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// S-5: a zero UserHome must not fall back to the operator's real home. Every path
// here is derived from the fields, so a test can only ever touch its own temp dir.
func TestConfigPathsDeriveFromUserHomeOnly(t *testing.T) {
	c := install.Config{UserHome: "/fake/home", Home: "/fake/home/.swarm", Bin: "/fake/bin/swarm"}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"codex config", c.Codex("config.toml"), "/fake/home/.codex/config.toml"},
		{"codex hooks", c.Codex("hooks.json"), "/fake/home/.codex/hooks.json"},
		{"cursor mcp", c.Cursor("mcp.json"), "/fake/home/.cursor/mcp.json"},
		{"cursor local plugin", c.Cursor("plugins", "local", "swarm"), "/fake/home/.cursor/plugins/local/swarm"},
		{"claude skills link", c.Claude("skills", "swarm"), "/fake/home/.claude/skills/swarm"},
		{"gemini agy mcp", c.Gemini("antigravity", "mcp_config.json"), "/fake/home/.gemini/antigravity/mcp_config.json"},
		{"gemini md", c.Gemini("GEMINI.md"), "/fake/home/.gemini/GEMINI.md"},
		{"gemini hooks", c.Gemini("config", "hooks.json"), "/fake/home/.gemini/config/hooks.json"},
		{"local bin", c.LocalBin(), "/fake/home/.local/bin/swarm"},
		{"work", c.Work(), "/fake/home/.swarm/work"},
		{"worktrees", c.Worktrees(), "/fake/home/.swarm/worktrees"},
		{"vendor", c.Vendor("superpowers"), "/fake/home/.swarm/vendor/plugins/superpowers"},
		{"logs", c.Logs("install.log"), "/fake/home/.swarm/logs/install.log"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if root := c.Codex(); root != "/fake/home/.codex" {
		t.Errorf("no-arg form = %q", root)
	}
}

// §18: both skills are installed for every agent, at the four listed roots.
//
// P0 (2026-09-23): agy's was wrong -- ~/.gemini/skills/ isn't a directory agy
// itself ever reads skills from. Per antigravity.google/docs/skills/ (and
// confirmed live: a spawned agy's own reported skill listing never included
// "swarm" while it did include agy's built-in ones), agy's real global
// skills directory is ~/.gemini/antigravity-cli/skills/ -- nested inside the
// same antigravity-cli directory the onboarding-isolation fix already
// symlinks whole, so a corrected path needs no separate isolation glue.
func TestSkillsDirPerAgent(t *testing.T) {
	c := install.Config{UserHome: "/fake/home"}
	want := map[install.Kind]string{
		install.KindClaude: "/fake/home/.claude/skills",
		install.KindCodex:  "/fake/home/.codex/skills",
		install.KindAgy:    "/fake/home/.gemini/antigravity-cli/skills",
		install.KindCursor: "/fake/home/.cursor/skills",
		install.KindMuse:   "/fake/home/.config/muse/skills",
	}
	for k, w := range want {
		if got := c.SkillsDir(k); got != w {
			t.Errorf("SkillsDir(%s) = %q, want %q", k, got, w)
		}
	}
}

// §17 / Copy.swift agentLabel: agy is lowercase, the other three are capitalised.
func TestKindDisplayMatchesTheCopyTable(t *testing.T) {
	want := map[install.Kind]string{
		install.KindClaude: "Claude", install.KindCodex: "Codex",
		install.KindAgy: "agy", install.KindCursor: "Cursor",
		install.KindMuse: "Muse",
	}
	if len(install.Kinds) != 5 {
		t.Fatalf("Kinds = %v; L2 lists exactly five installable agents", install.Kinds)
	}
	for _, k := range install.Kinds {
		if got := k.Display(); got != want[k] {
			t.Errorf("%s.Display() = %q, want %q", k, got, want[k])
		}
	}
}

func TestPlistPathStillUsesLaunchAgentsDir(t *testing.T) {
	// P1's behaviour must not regress when the methods land next to it.
	c := install.Config{LaunchAgentsDir: "/fake/home/Library/LaunchAgents"}
	if got, want := install.PlistPath(c), filepath.Join(c.LaunchAgentsDir, "dev.swarm.daemon.plist"); got != want {
		t.Errorf("PlistPath = %q, want %q", got, want)
	}
}

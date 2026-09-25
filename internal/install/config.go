package install

import "path/filepath"

// Kind is the set of agents swarm install writes configuration for (L2). The
// `fake` kind exists only in tests and gets no installed configuration, so it is
// deliberately absent. internal/kinds (P2) holds the runtime's copy of this list;
// agents_test.go asserts the two agree once P2 lands.
type Kind string

const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
	KindAgy    Kind = "agy"
	KindCursor Kind = "cursor"
	KindMuse   Kind = "muse"
)

// Kinds is in §11.1 column order.
var Kinds = []Kind{KindClaude, KindCodex, KindAgy, KindCursor, KindMuse}

// Display is the name shown to the user (§17.3 "{Agent} isn't installed…").
func (k Kind) Display() string {
	switch k {
	case KindClaude:
		return "Claude"
	case KindCodex:
		return "Codex"
	case KindAgy:
		return "agy"
	case KindCursor:
		return "Cursor"
	case KindMuse:
		return "Muse"
	}
	return string(k)
}

// The agent configuration roots. Each one is UserHome-relative: S-5 forbids a
// fallback, so a zero Config yields relative paths a test cannot mistake for real
// ones, never the operator's home.
func (c Config) Claude(rest ...string) string { return c.under(".claude", rest) }
func (c Config) Codex(rest ...string) string  { return c.under(".codex", rest) }
func (c Config) Cursor(rest ...string) string { return c.under(".cursor", rest) }
func (c Config) Gemini(rest ...string) string { return c.under(".gemini", rest) }

// Muse is the muse config root ($CONFIG_DIR for the muse CLI, probed
// 2026-09-23: `muse skills install --scope user` lands in skills/ here and the
// entry appears in `muse skills list --json`). XDG_CONFIG_HOME overrides are
// out of scope: S-5 forbids guessing the operator's real dirs.
func (c Config) Muse(rest ...string) string {
	return c.under(".config", append([]string{"muse"}, rest...))
}

// MuseData is the muse data root (plugin cache, model catalog, session store).
func (c Config) MuseData(rest ...string) string {
	return filepath.Join(append([]string{c.UserHome, ".local", "share", "muse"}, rest...)...)
}

func (c Config) under(dir string, rest []string) string {
	return filepath.Join(append([]string{c.UserHome, dir}, rest...)...)
}

// LocalBin is the ~/.local/bin/swarm link swarm install creates (§19).
func (c Config) LocalBin() string { return filepath.Join(c.UserHome, ".local", "bin", "swarm") }

// Work is the neutral-folder parent codex is asked to trust (§11.5).
func (c Config) Work() string { return filepath.Join(c.Home, "work") }

// Worktrees is the centralized worktree parent codex is asked to trust (§11.5, §12.1).
func (c Config) Worktrees() string { return filepath.Join(c.Home, "worktrees") }

// Vendor holds the cursor plugin copies (§12.4).
func (c Config) Vendor(rest ...string) string {
	return filepath.Join(append([]string{c.Home, "vendor", "plugins"}, rest...)...)
}

func (c Config) Logs(rest ...string) string {
	return filepath.Join(append([]string{c.Home, "logs"}, rest...)...)
}

// SkillsDir is where both skills are installed for k (§18).
func (c Config) SkillsDir(k Kind) string {
	switch k {
	case KindClaude:
		return c.Claude("skills")
	case KindCodex:
		return c.Codex("skills")
	case KindAgy:
		// A7 (2026-09-25): agy 1.2.10 reads skills from $HOME/.gemini/config/skills
		// and migrates ~/.gemini/antigravity-cli/skills away from on first run in a
		// fresh HOME, so ~/.gemini/antigravity-cli/skills is not durable (see
		// docs/plans/2026-09-24-skill-symlink-probe.md, "Follow-up"). This is the
		// location agy actually reads and migrates to.
		return c.Gemini("config", "skills")
	case KindCursor:
		return c.Cursor("skills")
	case KindMuse:
		return c.Muse("skills")
	}
	return ""
}

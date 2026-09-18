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
)

// Kinds is in §11.1 column order.
var Kinds = []Kind{KindClaude, KindCodex, KindAgy, KindCursor}

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

func (c Config) under(dir string, rest []string) string {
	return filepath.Join(append([]string{c.UserHome, dir}, rest...)...)
}

// LocalBin is the ~/.local/bin/swarm link swarm install creates (§19).
func (c Config) LocalBin() string { return filepath.Join(c.UserHome, ".local", "bin", "swarm") }

// Work is the neutral-folder parent codex is asked to trust (§11.5).
func (c Config) Work() string { return filepath.Join(c.Home, "work") }

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
		return c.Gemini("skills")
	case KindCursor:
		return c.Cursor("skills")
	}
	return ""
}

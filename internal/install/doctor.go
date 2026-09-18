package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Doctor struct {
	Run             execx.Runner
	Home            string // swarm home
	UserHome        string
	LaunchAgentsDir string
	DaemonURL       string
	OllamaCheck     func(context.Context) error // kb.NewOllama().Check in production
	GhosttyApps     []string
	LookPath        func(string) (string, error)
	HTTP            *http.Client
	Cfg             Config                       // P5: the per-agent checks derive every path from this (S-5)
	Installed       func(context.Context) []Kind // P5: which agents to check; InstalledKinds in production
}

var tmuxVersion = regexp.MustCompile(`tmux (\d+)\.(\d+)`)

// Checks runs every prerequisite check in a fixed order: P1's eight first (their
// order is asserted), then the per-agent blocks, superpowers, and the one legacy
// item §11.1 says must fail.
func (d Doctor) Checks(ctx context.Context) []Check {
	out := []Check{d.tmux(ctx), d.ghostty(), d.ollama(ctx), d.agents(), d.signing(ctx),
		d.launchAgent(), d.daemon(ctx), d.data()}
	for _, k := range d.installedKinds(ctx) {
		switch k {
		case KindClaude:
			out = append(out, CheckClaude(ctx, d.Cfg, d.Run)...)
		case KindCodex:
			out = append(out, CheckCodex(ctx, d.Cfg, d.Run)...)
		case KindCursor:
			out = append(out, CheckCursor(ctx, d.Cfg, d.Run)...)
		case KindAgy:
			out = append(out, CheckAgy(ctx, d.Cfg, d.Run)...)
		}
	}
	return append(out, d.superpowers(ctx)...)
}

func (d Doctor) installedKinds(ctx context.Context) []Kind {
	if d.Installed == nil {
		return nil
	}
	return d.Installed(ctx)
}

// superpowers is §12.4's usability check plus its "both variants" warning (I17).
func (d Doctor) superpowers(ctx context.Context) []Check {
	var out []Check
	for _, k := range d.installedKinds(ctx) {
		name := k.Display() + " superpowers"
		if bothSuperpowersVariants(d.Cfg, k) {
			out = append(out, Check{name, false,
				"Both superpowers and superpowers-dev are installed for " + k.Display() + ". Remove one."})
			continue
		}
		ok, detail := SuperpowersOK(d.Cfg, k)
		out = append(out, Check{name, ok, detail})
	}
	return out
}

// bothSuperpowersVariants reports whether an agent has superpowers and
// superpowers-dev at the same time (§12.4: they conflict).
func bothSuperpowersVariants(c Config, k Kind) bool {
	var roots []string
	switch k {
	case KindClaude:
		roots = []string{c.Claude("plugins", "cache", "*", "%s", "*")}
	case KindCodex:
		roots = []string{c.Codex("plugins", "cache", "*", "%s", "*")}
	case KindAgy:
		roots = []string{c.Gemini("config", "plugins", "%s")}
	case KindCursor:
		roots = []string{c.Cursor("plugins", "cache", "*", "%s", "*"), c.Cursor("plugins", "local", "%s")}
	}
	present := func(name string) bool {
		for _, pattern := range roots {
			hits, _ := filepath.Glob(fmt.Sprintf(pattern, name))
			if len(hits) > 0 {
				return true
			}
		}
		return false
	}
	return present("superpowers") && present("superpowers-dev")
}

// LegacyChecks is `swarm doctor --legacy` (§25). Its details are diagnostics, not
// §17 copy.
func (d Doctor) LegacyChecks(ctx context.Context) []Check {
	left := Leftovers(d.Cfg)
	if len(left) == 0 {
		return []Check{{"Agent Swarm 1.x", true, "No Agent Swarm 1.x state left."}}
	}
	var lines []string
	for _, l := range left {
		lines = append(lines, l.What+" at "+l.Path)
	}
	return []Check{{"Agent Swarm 1.x", false, strings.Join(lines, "; ")}}
}

func (d Doctor) tmux(ctx context.Context) Check {
	out, err := d.Run(ctx, "tmux", "-V")
	if err != nil {
		return Check{"tmux", false, "tmux isn't installed. Install it: brew install tmux"}
	}
	v := strings.TrimSpace(string(out))
	m := tmuxVersion.FindStringSubmatch(v)
	if m == nil {
		return Check{"tmux", false, v + " is too old. Install tmux 3.6 or later: brew install tmux"}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 3 || (major == 3 && minor < 6) {
		return Check{"tmux", false, v + " is too old. Install tmux 3.6 or later: brew install tmux"}
	}
	return Check{"tmux", true, v}
}

func (d Doctor) ghostty() Check {
	for _, app := range d.GhosttyApps {
		if _, err := os.Stat(app); err == nil {
			return Check{"Ghostty", true, app}
		}
	}
	return Check{"Ghostty", false, "Ghostty isn't installed. Install it: brew install --cask ghostty"}
}

func (d Doctor) ollama(ctx context.Context) Check {
	if err := d.OllamaCheck(ctx); err != nil {
		return Check{"Ollama", false, err.Error()}
	}
	return Check{"Ollama", true, "The embedding model is ready."}
}

func (d Doctor) agents() Check {
	var found []string
	for _, bin := range []string{"claude", "codex", "agy", "cursor-agent"} {
		if _, err := d.LookPath(bin); err == nil {
			found = append(found, bin)
		}
	}
	if len(found) == 0 {
		return Check{"Agents", false, "Install at least one agent CLI: claude, codex, agy or cursor-agent."}
	}
	return Check{"Agents", true, strings.Join(found, ", ")}
}

func (d Doctor) signing(ctx context.Context) Check {
	out, err := d.Run(ctx, "git", "config", "--global", "commit.gpgsign")
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return Check{"Commit signing", false, "Commit signing is off. Enable it in git config."}
	}
	return Check{"Commit signing", true, "commit.gpgsign is true."}
}

func (d Doctor) launchAgent() Check {
	path := filepath.Join(d.LaunchAgentsDir, Label+".plist")
	body, err := os.ReadFile(path)
	if err != nil {
		return Check{"Launch agent", false, "Not installed. Run swarm install."}
	}
	s := string(body)
	if !strings.Contains(s, "<string>daemon</string>") || !strings.Contains(s, "<key>PATH</key>") {
		return Check{"Launch agent", false, "The launch agent is from Agent Swarm 1.x. Run swarm install."}
	}
	return Check{"Launch agent", true, path}
}

func (d Doctor) daemon(ctx context.Context) Check {
	down := Check{"Daemon", false, "The daemon isn't running. Run swarm install."}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.DaemonURL+"/api/health", nil)
	if err != nil {
		return down
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return down
	}
	defer resp.Body.Close()
	var h struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
		Schema  int    `json:"schema"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&h) != nil || !h.OK {
		return down
	}
	return Check{"Daemon", true, fmt.Sprintf("Running (version %s, schema %d).", h.Version, h.Schema)}
}

func (d Doctor) data() Check {
	if HasLegacyData(filepath.Join(d.Home, "swarm.db")) {
		return Check{"Data", false, db.ErrLegacy.Error()}
	}
	return Check{"Data", true, "No Agent Swarm 1.x data."}
}

package install

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// MarketplaceURL is §12.4's list. S-6: it is a constant here but never a default —
// Plugins.MarketplaceURL has no fallback, so a test cannot dial it by accident.
const MarketplaceURL = "https://raw.githubusercontent.com/obra/superpowers-marketplace/main/.claude-plugin/marketplace.json"

// MarketplaceName is the name the per-agent install commands address it by.
const MarketplaceName = "superpowers-marketplace"

// githubMarketplaceURL is the git-clonable form of the marketplace. cursor's
// and muse's `marketplace add` commands clone a git source directly (unlike
// claude/codex, which take the "owner/repo" shorthand, or Sync's own fetch,
// which needs the raw marketplace.json URL — probed 2026-09-23: a git clone
// of that raw-file URL 404s, the repo URL clones fine).
const githubMarketplaceURL = "https://github.com/obra/superpowers-marketplace"

// pluginSupport is §12.4's matrix as of 2026-09-17. A plugin that is not listed is
// installed for claude only (see the task rationale); superpowers-dev is never
// installed, because it conflicts with superpowers.
var pluginSupport = map[string][]Kind{
	"superpowers":                            {KindClaude, KindCodex, KindAgy, KindCursor, KindMuse},
	"elements-of-style":                      {KindClaude, KindCodex, KindAgy, KindCursor, KindMuse},
	"episodic-memory":                        {KindClaude, KindCodex},
	"superpowers-chrome":                     {KindClaude},
	"superpowers-lab":                        {KindClaude},
	"superpowers-developing-for-claude-code": {KindClaude},
	"claude-session-driver":                  {KindClaude},
	"double-shot-latte":                      {KindClaude},
	"private-journal-mcp":                    {KindClaude},
}

// SkipAlways are plugins swarm never installs (§12.4).
var SkipAlways = map[string]string{"superpowers-dev": "it conflicts with superpowers"}

type MarketplacePlugin struct {
	Name   string       `json:"name"`
	Source PluginSource `json:"source"`
}

// PluginSource is the marketplace's own nested source descriptor. Only
// url-sourced plugins are handled — that is the only kind the real
// marketplace currently publishes for any of §12.4's plugins.
type PluginSource struct {
	Source string `json:"source"`
	URL    string `json:"url"`
}

// ParseMarketplace reads .claude-plugin/marketplace.json.
func ParseMarketplace(body []byte) ([]MarketplacePlugin, error) {
	var f struct {
		Plugins []MarketplacePlugin `json:"plugins"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, err
	}
	if len(f.Plugins) == 0 {
		return nil, fmt.Errorf("the marketplace lists no plugins")
	}
	return f.Plugins, nil
}

// SupportedBy applies §12.4's matrix.
func SupportedBy(m MarketplacePlugin, k Kind) bool {
	if _, never := SkipAlways[m.Name]; never {
		return false
	}
	kinds, known := pluginSupport[m.Name]
	if !known {
		return k == KindClaude
	}
	for _, kk := range kinds {
		if kk == k {
			return true
		}
	}
	return false
}

// PluginResult is one line of swarm install's plugin report.
type PluginResult struct {
	Kind   Kind   `json:"kind"`
	Plugin string `json:"plugin"`
	Action string `json:"action"` // "install" | "update" | "skip" | "marketplace"
	Detail string `json:"detail"`
	Err    error  `json:"-"`
}

type Plugins struct {
	Cfg            Config
	Run            execx.Runner
	HTTP           *http.Client
	MarketplaceURL string // S-6: no default
	Log            io.Writer
}

func (p Plugins) logf(format string, args ...any) {
	if p.Log != nil {
		fmt.Fprintf(p.Log, format+"\n", args...)
	}
}

// run executes one command, logs its argv and output, and returns the error.
func (p Plugins) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := p.Run(ctx, name, args...)
	p.logf("$ %s %s\n%s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	if err != nil {
		p.logf("  error: %v", err)
	}
	return out, err
}

// Sync installs or updates every supported plugin for every installed agent. It is
// idempotent, and a failure for one agent never stops the others (§12.4).
func (p Plugins) Sync(ctx context.Context, installed []Kind) []PluginResult {
	body, err := p.fetch(ctx)
	if err != nil {
		return []PluginResult{{Action: "marketplace", Err: err}}
	}
	list, err := ParseMarketplace(body)
	if err != nil {
		return []PluginResult{{Action: "marketplace", Err: err}}
	}
	var out []PluginResult
	for _, k := range installed {
		out = append(out, p.syncAgent(ctx, k, list)...)
	}
	return out
}

func (p Plugins) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.MarketplaceURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the plugin list returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (p Plugins) syncAgent(ctx context.Context, k Kind, list []MarketplacePlugin) []PluginResult {
	var out []PluginResult
	have := p.alreadyInstalled(ctx, k)
	if res, ok := p.addMarketplace(ctx, k, have); ok {
		out = append(out, res)
	}
	for _, m := range list {
		if why, never := SkipAlways[m.Name]; never {
			out = append(out, PluginResult{Kind: k, Plugin: m.Name, Action: "skip", Detail: why})
			continue
		}
		if !SupportedBy(m, k) {
			detail := "no " + string(k) + " manifest"
			if _, known := pluginSupport[m.Name]; !known {
				detail = "not in the 2026-09-17 support matrix"
			}
			out = append(out, PluginResult{Kind: k, Plugin: m.Name, Action: "skip", Detail: detail})
			p.logf("skipped %s for %s: %s", m.Name, k, detail)
			continue
		}
		// I17: superpowers-dev counts as superpowers; do not install both.
		if m.Name == "superpowers" && have["superpowers-dev"] {
			out = append(out, PluginResult{Kind: k, Plugin: m.Name, Action: "skip",
				Detail: "superpowers-dev is already installed"})
			continue
		}
		action := "install"
		if have[m.Name] {
			action = "update"
		}
		err := p.apply(ctx, k, m, action)
		out = append(out, PluginResult{Kind: k, Plugin: m.Name, Action: action, Err: err})
	}
	return out
}

// alreadyInstalled reads each agent's own plugin list. A failure means "nothing
// known", which makes the next step an install rather than an update.
func (p Plugins) alreadyInstalled(ctx context.Context, k Kind) map[string]bool {
	have := map[string]bool{}
	switch k {
	case KindClaude:
		out, err := p.run(ctx, "claude", "plugin", "list", "--json")
		if err != nil {
			return have
		}
		var f struct {
			Plugins []struct{ Name string } `json:"plugins"`
		}
		if json.Unmarshal(out, &f) == nil {
			for _, pl := range f.Plugins {
				have[pl.Name] = true
			}
		}
	case KindCodex:
		out, err := p.run(ctx, "codex", "plugin", "list")
		if err != nil {
			return have
		}
		for _, line := range strings.Split(string(out), "\n") {
			if name := strings.Fields(line); len(name) > 0 {
				have[name[0]] = true
			}
		}
	case KindAgy:
		out, err := p.run(ctx, "agy", "plugin", "list")
		if err != nil {
			return have
		}
		var f struct {
			Imports []struct{ Name string } `json:"imports"`
		}
		if json.Unmarshal(out, &f) == nil {
			for _, im := range f.Imports {
				have[im.Name] = true
			}
		}
	case KindCursor:
		ents, err := os.ReadDir(p.Cfg.Cursor("plugins", "local"))
		if err != nil {
			return have
		}
		for _, e := range ents {
			// P0-8: only a real folder counts; a symlink is ignored by cursor.
			if e.Type()&os.ModeSymlink == 0 && e.IsDir() {
				have[e.Name()] = true
			}
		}
	case KindMuse:
		out, err := p.run(ctx, "muse", "plugins", "list", "--json")
		if err != nil {
			return have
		}
		var f struct {
			Plugins []struct {
				Record struct {
					ID string `json:"id"`
				} `json:"record"`
			} `json:"plugins"`
		}
		if json.Unmarshal(out, &f) == nil {
			for _, pl := range f.Plugins {
				have[pl.Record.ID] = true
			}
		}
	}
	return have
}

// addMarketplace runs the per-agent marketplace registration, skipping it when the
// agent already knows it (§12.4). agy has none.
func (p Plugins) addMarketplace(ctx context.Context, k Kind, have map[string]bool) (PluginResult, bool) {
	switch k {
	case KindClaude:
		if out, err := p.run(ctx, "claude", "plugin", "marketplace", "list"); err == nil &&
			strings.Contains(string(out), MarketplaceName) {
			return PluginResult{}, false
		}
		_, err := p.run(ctx, "claude", "plugin", "marketplace", "add", "obra/superpowers-marketplace")
		return PluginResult{Kind: k, Action: "marketplace", Err: err}, true
	case KindCodex:
		// §12.4: $CODEX_HOME must exist first.
		if err := os.MkdirAll(p.Cfg.Codex(), 0o755); err != nil {
			return PluginResult{Kind: k, Action: "marketplace", Err: err}, true
		}
		_, err := p.run(ctx, "codex", "plugin", "marketplace", "add", "obra/superpowers-marketplace")
		return PluginResult{Kind: k, Action: "marketplace", Err: err}, true
	case KindCursor:
		_, err := p.run(ctx, "cursor-agent", "plugin", "marketplace", "add", githubMarketplaceURL)
		return PluginResult{Kind: k, Action: "marketplace", Err: err}, true
	case KindMuse:
		// Probed 2026-09-23: unlike claude/codex/cursor, a second `marketplace add`
		// of the same name errors ("already configured") rather than no-opping, so
		// presence must be checked first via `marketplace list --json`.
		if out, err := p.run(ctx, "muse", "plugins", "marketplace", "list", "--json"); err == nil {
			var f struct {
				Marketplaces []struct {
					Name string `json:"name"`
				} `json:"marketplaces"`
			}
			if json.Unmarshal(out, &f) == nil {
				for _, mp := range f.Marketplaces {
					if mp.Name == MarketplaceName {
						return PluginResult{}, false
					}
				}
			}
		}
		_, err := p.run(ctx, "muse", "plugins", "marketplace", "add", MarketplaceName, githubMarketplaceURL)
		return PluginResult{Kind: k, Action: "marketplace", Err: err}, true
	}
	return PluginResult{}, false // agy has no marketplace step
}

// apply installs or updates one plugin with the exact §12.4 command for that agent.
func (p Plugins) apply(ctx context.Context, k Kind, m MarketplacePlugin, action string) error {
	switch k {
	case KindClaude:
		if action == "update" {
			_, err := p.run(ctx, "claude", "plugin", "update", m.Name, "-y")
			return err
		}
		_, err := p.run(ctx, "claude", "plugin", "install", m.Name+"@"+MarketplaceName, "--scope", "user", "-y")
		return err
	case KindCodex:
		// §12.4: superpowers comes from openai-curated-remote, the rest from ours.
		ref := m.Name + "@" + MarketplaceName
		if m.Name == "superpowers" {
			ref = "superpowers@openai-curated-remote"
		}
		if action == "update" {
			if _, err := p.run(ctx, "codex", "plugin", "marketplace", "upgrade"); err != nil {
				return err
			}
		}
		_, err := p.run(ctx, "codex", "plugin", "add", ref)
		return err
	case KindAgy:
		_, err := p.run(ctx, "agy", "plugin", "install", m.Source.URL)
		return err
	case KindCursor:
		return p.vendorForCursor(ctx, m, action)
	case KindMuse:
		if action == "update" {
			// Probed in `muse plugins --help` 2026-09-23 (no network, refreshes
			// the installed local plugin from its source).
			_, err := p.run(ctx, "muse", "plugins", "update", m.Name)
			return err
		}
		// Probed 2026-09-23 (live run, see plugins_test.go): addMarketplace (above)
		// registers githubMarketplaceURL as MarketplaceName once per Sync, before
		// this loop runs; this then installs from that registered snapshot, the
		// same ref-construction pattern as KindCodex.
		_, err := p.run(ctx, "muse", "plugins", "install", m.Name+"@"+MarketplaceName)
		return err
	}
	return nil
}

// vendorForCursor clones the plugin into ~/.swarm/vendor/plugins/<name> and copies
// it into ~/.cursor/plugins/local/<name> as a real folder (P0-8: cursor ignores a
// symlink there). An update pulls and replaces the copy.
func (p Plugins) vendorForCursor(ctx context.Context, m MarketplacePlugin, action string) error {
	vendor := p.Cfg.Vendor(m.Name)
	if _, err := os.Stat(filepath.Join(vendor, ".git")); err == nil {
		if _, err := p.run(ctx, "git", "-C", vendor, "pull", "--ff-only"); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(vendor), 0o755); err != nil {
			return err
		}
		if _, err := p.run(ctx, "git", "clone", "--depth", "1", m.Source.URL, vendor); err != nil {
			return err
		}
	}
	// Verify the manifest before copying: cursor is the one agent where we can.
	manifest := filepath.Join(vendor, ".cursor-plugin", "plugin.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		return fmt.Errorf("no cursor manifest in %s", m.Source.URL)
	}
	var any map[string]any
	if err := json.Unmarshal(body, &any); err != nil {
		return fmt.Errorf("%s: %w", manifest, err)
	}
	local := p.Cfg.Cursor("plugins", "local", m.Name)
	if err := os.RemoveAll(local); err != nil {
		return err
	}
	return copyTree(vendor, local)
}

// copyTree copies src to dst, skipping .git. A symlink anywhere inside the
// tree is DEREFERENCED, not recreated (fix round 3, controller ruling,
// replacing fix round 2's approach of recreating the link itself): a link to
// a file becomes a real file holding the target's content, and a link to a
// directory becomes a real directory holding a recursive copy of the
// target's own contents. This is what keeps two separate contracts true at
// once: cursor's local plugin copy must be a real folder tree with no
// symlink anywhere in it (vendorForCursor's own comment above, P0-8), and
// content this package salvages out of a swarm session's run/launch folder
// (agy.go's repairAgySkillsRoot) must survive that session later being
// reaped -- a recreated symlink pointing back into run/launch would dangle
// the moment the session's folder is deleted, since deleting run/launch
// content is never this package's call to make but a session's own cleanup
// routinely does exactly that.
//
// maxCopyTreeDepth and the visited set together guard against a symlink
// cycle (an entry that links back to one of its own ancestors, directly or
// through another link): each newly dereferenced directory's real
// (EvalSymlinks'd) path is added to visited before recursing into it, and a
// path already in visited errors out immediately rather than recursing
// forever; the depth counter is a second, unconditional backstop.
func copyTree(src, dst string) error {
	return copyTreeGuarded(src, dst, map[string]bool{}, 0)
}

const maxCopyTreeDepth = 32

func copyTreeGuarded(src, dst string, visited map[string]bool, depth int) error {
	if depth > maxCopyTreeDepth {
		return fmt.Errorf("copyTree: max depth (%d) exceeded copying %s: possible symlink cycle", maxCopyTreeDepth, src)
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		// A symlinked entry (WalkDir uses Lstat throughout, so this is the
		// only place that ever sees the link itself rather than what it
		// points at) is dereferenced: EvalSymlinks resolves it (and any
		// further chain), and whether the real target is a file or a
		// directory decides whether it's copied in directly or expanded
		// recursively. A directory target recurses through copyTreeGuarded
		// again (not inline here) so its own visited/depth guards apply to
		// whatever it contains too, including further symlinks.
		if d.Type()&os.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				return err
			}
			realInfo, err := os.Stat(real)
			if err != nil {
				return err
			}
			if !realInfo.IsDir() {
				return CopyFile(real, target)
			}
			if visited[real] {
				return fmt.Errorf("copyTree: symlink cycle detected at %s (resolves to %s, already being copied)", p, real)
			}
			nextVisited := make(map[string]bool, len(visited)+1)
			for k := range visited {
				nextVisited[k] = true
			}
			nextVisited[real] = true
			return copyTreeGuarded(real, target, nextVisited, depth+1)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return CopyFile(p, target)
	})
}

// SuperpowersOK is §12.4's usability check: the listed files must exist.
func SuperpowersOK(c Config, k Kind) (bool, string) {
	var globs []string
	switch k {
	case KindClaude:
		globs = []string{
			c.Claude("plugins", "cache", "*", "superpowers*", "*", "skills", "brainstorming", "SKILL.md"),
			c.Claude("plugins", "cache", "*", "superpowers*", "*", "skills", "test-driven-development", "SKILL.md"),
		}
	case KindCodex:
		globs = []string{c.Codex("plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md")}
	case KindAgy:
		globs = []string{c.Gemini("config", "plugins", "superpowers*", "skills", "brainstorming", "SKILL.md")}
	case KindCursor:
		// §12.4: either the cache or the local copy satisfies cursor.
		for _, g := range []string{
			c.Cursor("plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"),
			c.Cursor("plugins", "local", "superpowers", "skills", "brainstorming", "SKILL.md"),
		} {
			if hits, _ := filepath.Glob(g); len(hits) > 0 {
				return true, g
			}
		}
		return false, "Install the superpowers plugin for " + k.Display() + " to run orchestrators."
	case KindMuse:
		// The cache layout nests the bundle under package/ (probed 2026-09-23);
		// accept the flat layout too in case a future muse version flattens it.
		for _, g := range []string{
			c.MuseData("plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"),
			c.MuseData("plugins", "cache", "*", "superpowers", "*", "package", "skills", "brainstorming", "SKILL.md"),
		} {
			if hits, _ := filepath.Glob(g); len(hits) > 0 {
				return true, g
			}
		}
		return false, "Install the superpowers plugin for " + k.Display() + " to run orchestrators."
	}
	for _, g := range globs {
		hits, _ := filepath.Glob(g)
		if len(hits) == 0 {
			return false, "Install the superpowers plugin for " + k.Display() + " to run orchestrators."
		}
	}
	return true, "superpowers is ready."
}

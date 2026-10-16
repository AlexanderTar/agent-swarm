package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
	"github.com/pelletier/go-toml/v2"
)

// grokHookEvents is every grok lifecycle event Swarm listens to. Each runs
// `<swarm> hook grok <Event>`; the command shape is Claude's matcher-group form,
// which grok reads from [[hooks.<Event>]] tables.
var grokHookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"Stop", "StopFailure", "StopCancelled", "PreCompact", "PostCompact", "Notification"}

// grokAuthFiles are the login files the Swarm grok home shares with ~/.grok.
// grok refreshes its token through both links (probed 2026-10-16, SPIKE-52).
var grokAuthFiles = []string{"auth.json", "auth.json.lock"}

// grokSuperpowersURL is the native install source; grok reads the same repo the
// other agents' superpowers plugins come from.
const grokSuperpowersURL = "https://github.com/obra/superpowers.git"

// isSwarmGrokHook is a hook command this installer wrote, whatever binary path it
// carried, so a moved binary replaces its entry instead of doubling it.
func isSwarmGrokHook(cmd string) bool { return strings.Contains(cmd, " hook grok ") }

// grokRecord is what install added that uninstall must take back: the lock it
// seeded in the operator's own ~/.grok, the plugin names it put in [plugins]
// disabled and the Claude superpowers skills paths it put in [skills] paths.
// Anything already there belongs to the operator and is never listed.
type grokRecord struct {
	SeededLock      bool     `json:"seeded_lock,omitempty"`
	DisabledPlugins []string `json:"disabled_plugins,omitempty"`
	SkillsPaths     []string `json:"skills_paths,omitempty"`
}

func grokRecordPath(c Config) string { return c.Grok("swarm-install.json") }

func loadGrokRecord(c Config) (r grokRecord) {
	if raw, err := os.ReadFile(grokRecordPath(c)); err == nil {
		_ = json.Unmarshal(raw, &r) // unreadable: nothing is claimed, so nothing is removed
	}
	return r
}

// noteGrokAdded records what install just added.
func noteGrokAdded(c Config, seededLock bool, plugins, skillsPaths []string) error {
	r := loadGrokRecord(c)
	old := r
	r.SeededLock = r.SeededLock || seededLock
	r.DisabledPlugins = append(r.DisabledPlugins, plugins...)
	sort.Strings(r.DisabledPlugins)
	r.DisabledPlugins = slices.Compact(r.DisabledPlugins)
	r.SkillsPaths = append(r.SkillsPaths, skillsPaths...)
	sort.Strings(r.SkillsPaths)
	r.SkillsPaths = slices.Compact(r.SkillsPaths)
	if r.SeededLock == old.SeededLock && slices.Equal(r.DisabledPlugins, old.DisabledPlugins) &&
		slices.Equal(r.SkillsPaths, old.SkillsPaths) {
		return nil
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = WriteIfChanged(grokRecordPath(c), body, 0o644)
	return err
}

// ensureGrokHome creates the Swarm grok home 0700 and tightens one an earlier
// install left looser: grok writes session logs with token prefixes into it.
func ensureGrokHome(c Config) error {
	if err := os.MkdirAll(c.Grok(), 0o700); err != nil {
		return err
	}
	return os.Chmod(c.Grok(), 0o700)
}

// errTOMLParse marks a config grok cannot read, which install never overwrites.
var errTOMLParse = errors.New("does not parse")

// WriteGrok writes everything swarm install owns inside the Swarm grok home:
// the swarm MCP server, one hook per event and the skills path merged into
// config.toml (grok's own and the user's keys are kept), the skills links, and
// the auth links into ~/.grok. changed lists the paths written; skipped lists the
// ones left alone with the reason.
//
// Order matters: `grok plugin install` rewrites config.toml, so the superpowers
// install runs first and Swarm's keys are merged after it.
func WriteGrok(ctx context.Context, c Config, run execx.Runner) (changed, skipped []string, err error) {
	if err := ensureGrokHome(c); err != nil {
		return nil, nil, err
	}
	cfg := c.Grok("config.toml")
	if !grokNativeSuperpowers(c) && run != nil {
		if _, err := run(ctx, "env", append(grokhome.EnvArgs(c.Home),
			grokhome.Bin, "plugin", "install", grokSuperpowersURL, "--trust")...); err != nil {
			skipped = append(skipped, "grok superpowers: the native install failed: "+err.Error())
		}
	}
	fallback := ""
	if !grokNativeSuperpowers(c) {
		if fallback = newestClaudeSuperpowers(c); fallback == "" {
			skipped = append(skipped, "grok superpowers: not installed, and no Claude copy to fall back on")
		}
	}
	var disabled []string
	wrote, err := editTOML(cfg, true, func(m map[string]any) error {
		mergeGrokConfig(c, m)
		disabled = refreshGrokPlugins(c, m)
		setGrokSuperpowersFallback(c, m, fallback)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	var skillsPaths []string
	if fallback != "" {
		skillsPaths = []string{fallback}
	}
	if err := noteGrokAdded(c, false, disabled, skillsPaths); err != nil {
		return nil, nil, err
	}
	if wrote {
		changed = append(changed, cfg)
	}
	l, s, err := linkGrokAuth(c)
	changed, skipped = append(changed, l...), append(skipped, s...)
	if err != nil {
		return changed, skipped, err
	}
	sk, sks, err := WriteSkills(c, KindGrok)
	return append(changed, sk...), append(skipped, sks...), err
}

// editTOML applies edit to the table at path and writes it back only when the
// bytes change. A missing file starts empty when create is true and is left
// alone otherwise. A file that does not parse is an error and is never
// overwritten.
func editTOML(path string, create bool, edit func(m map[string]any) error) (bool, error) {
	m := map[string]any{}
	if old, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(old, &m); err != nil {
			return false, fmt.Errorf("%s %w: %v", path, errTOMLParse, err)
		}
	} else if os.IsNotExist(err) && !create {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := edit(m); err != nil {
		return false, err
	}
	body, err := toml.Marshal(m)
	if err != nil {
		return false, err
	}
	return WriteIfChanged(path, body, 0o644)
}

func mergeGrokConfig(c Config, m map[string]any) {
	servers, _ := m["mcp_servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["swarm"] = map[string]any{
		"command": c.Bin,
		"args":    []any{"mcp"},
		// grok passes ${VAR} through from the launch env, which carries the session.
		"env": map[string]any{
			"SWARM_URL": "${SWARM_URL}", "SWARM_SESSION": "${SWARM_SESSION}",
			"SWARM_TOKEN_FILE": "${SWARM_TOKEN_FILE}", "SWARM_AGENT_KIND": "grok",
		},
	}
	m["mcp_servers"] = servers

	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, ev := range grokHookEvents {
		groups, _ := hooks[ev].([]any)
		groups = dropGrokHookGroups(groups)
		groups = append(groups, map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": c.Bin + " hook grok " + ev,
		}}})
		hooks[ev] = groups
	}
	m["hooks"] = hooks

	skills, _ := m["skills"].(map[string]any)
	if skills == nil {
		skills = map[string]any{}
	}
	paths, _ := skills["paths"].([]any)
	if dir := c.SkillsDir(KindGrok); !slices.Contains(paths, any(dir)) {
		paths = append(paths, dir)
	}
	skills["paths"] = paths
	m["skills"] = skills
}

// dropGrokHookGroups removes Swarm's commands from the matcher groups. A group
// the user shares with Swarm keeps its other hooks; one left empty goes.
func dropGrokHookGroups(groups []any) []any {
	var kept []any
	for _, g := range groups {
		obj, _ := g.(map[string]any)
		inner, _ := obj["hooks"].([]any)
		var theirs []any
		for _, h := range inner {
			cmd, _ := h.(map[string]any)["command"].(string)
			if !isSwarmGrokHook(cmd) {
				theirs = append(theirs, h)
			}
		}
		switch {
		case len(theirs) == len(inner):
			kept = append(kept, g)
		case len(theirs) > 0:
			rest := map[string]any{}
			for k, v := range obj {
				rest[k] = v
			}
			rest["hooks"] = theirs
			kept = append(kept, rest)
		}
	}
	return kept
}

// linkGrokAuth points the Swarm home's login files at ~/.grok so one login serves
// both. A missing lock is seeded first (grok writes pid:time into it, so an empty
// file is safe), so the link never dangles on a fresh machine. A real file at the
// link path is the operator's: it is left alone and reported.
func linkGrokAuth(c Config) (changed, skipped []string, err error) {
	if err := os.MkdirAll(c.UserGrok(), 0o700); err != nil {
		return nil, nil, err
	}
	lock := c.UserGrok("auth.json.lock")
	if _, err := os.Lstat(lock); os.IsNotExist(err) {
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			return nil, nil, err
		}
		if err := noteGrokAdded(c, true, nil, nil); err != nil {
			return nil, nil, err
		}
	}
	for _, name := range grokAuthFiles {
		dst, target := c.Grok(name), c.UserGrok(name)
		fi, err := os.Lstat(dst)
		switch {
		case err == nil && fi.Mode()&os.ModeSymlink == 0:
			skipped = append(skipped, dst+": a real file, left alone")
			continue
		case err == nil:
			if cur, _ := os.Readlink(dst); cur == target {
				continue
			}
			if err := os.Remove(dst); err != nil {
				return changed, skipped, err
			}
		case !os.IsNotExist(err):
			return changed, skipped, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return changed, skipped, err
		}
		if err := os.Symlink(target, dst); err != nil {
			return changed, skipped, err
		}
		changed = append(changed, dst)
	}
	return changed, skipped, nil
}

// grokNativeSuperpowers reports whether `grok plugin install` has put
// superpowers under the Swarm grok home.
func grokNativeSuperpowers(c Config) bool {
	hits, _ := filepath.Glob(c.Grok("installed-plugins", "superpowers-*", "skills", "brainstorming", "SKILL.md"))
	return len(hits) > 0
}

// newestClaudeSuperpowers is the skills dir of the most recently written
// ~/.claude superpowers copy, or "" when there is none. It is the fallback for a
// machine where the native install fails.
func newestClaudeSuperpowers(c Config) string {
	hits, _ := filepath.Glob(c.Claude("plugins", "cache", "*", "superpowers", "*", "skills", "brainstorming", "SKILL.md"))
	best, bestAt := "", int64(-1)
	for _, h := range hits {
		skills := filepath.Dir(filepath.Dir(h))
		fi, err := os.Stat(filepath.Dir(skills)) // the version dir
		if err != nil {
			continue
		}
		if at := fi.ModTime().UnixNano(); at > bestAt || (at == bestAt && skills > best) {
			best, bestAt = skills, at
		}
	}
	return best
}

// setGrokSuperpowersFallback keeps [skills] paths free of stale Claude
// superpowers copies and adds fallback (when set) in their place.
func setGrokSuperpowersFallback(c Config, m map[string]any, fallback string) {
	cache := c.Claude("plugins", "cache") + string(filepath.Separator)
	skills, _ := m["skills"].(map[string]any)
	paths, _ := skills["paths"].([]any)
	var kept []any
	for _, p := range paths {
		s, _ := p.(string)
		if strings.HasPrefix(s, cache) && strings.Contains(s, string(filepath.Separator)+"superpowers"+string(filepath.Separator)) {
			continue
		}
		kept = append(kept, p)
	}
	if fallback != "" {
		kept = append(kept, fallback)
	}
	skills["paths"] = kept
}

// RefreshGrokPlugins disables, in the Swarm grok home's config.toml, every
// plugin installed in Claude (grok would otherwise load them through its Claude
// compatibility). It reports whether the file changed. superpowers is never
// disabled: grok's own copy shadows Claude's by name and a disabled entry wins.
// Launch calls it before every grok start, so a plugin added to Claude later is
// covered. There is no allowlist in grok, so a denylist is all there is.
func RefreshGrokPlugins(c Config) (bool, error) {
	if err := ensureGrokHome(c); err != nil {
		return false, err
	}
	var added []string
	wrote, err := editTOML(c.Grok("config.toml"), true, func(m map[string]any) error { added = refreshGrokPlugins(c, m); return nil })
	if err != nil {
		return wrote, err
	}
	return wrote, noteGrokAdded(c, false, added, nil)
}

// refreshGrokPlugins returns the names it added to the disabled list.
func refreshGrokPlugins(c Config, m map[string]any) (added []string) {
	plugins, _ := m["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	set := map[string]bool{}
	old, _ := plugins["disabled"].([]any)
	for _, v := range old {
		if s, ok := v.(string); ok {
			set[s] = true
		}
	}
	for _, n := range claudePluginNames(c) {
		if !set[n] && n != "superpowers" {
			added = append(added, n)
		}
		set[n] = true
	}
	delete(set, "superpowers")
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	disabled := make([]any, len(names))
	for i, n := range names {
		disabled[i] = n
	}
	plugins["disabled"] = disabled
	m["plugins"] = plugins
	return added
}

// claudePluginNames lists every plugin name Claude knows: the install record,
// the enabledPlugins in settings.json and the cache folders, so a half-installed
// or hand-copied plugin is covered too.
func claudePluginNames(c Config) []string {
	seen := map[string]bool{}
	var body struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if raw, err := os.ReadFile(c.Claude("plugins", "installed_plugins.json")); err == nil && json.Unmarshal(raw, &body) == nil {
		for key := range body.Plugins {
			name, _ := splitPluginKey(key)
			seen[name] = true
		}
	}
	for key := range claudeEnabledPluginKeys(c.UserHome) {
		name, _ := splitPluginKey(key)
		seen[name] = true
	}
	dirs, _ := filepath.Glob(c.Claude("plugins", "cache", "*", "*"))
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			seen[filepath.Base(d)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		if n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// loadGrokConfig reads the Swarm grok home's config.toml for a doctor check.
func loadGrokConfig(c Config) (m map[string]any, fail string) {
	p := c.Grok("config.toml")
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, "Not installed. Run swarm install."
	}
	if err := toml.Unmarshal(body, &m); err != nil {
		return nil, "Invalid " + p + ". Run swarm install."
	}
	return m, ""
}

// CheckGrok is doctor's grok block: every file WriteGrok touches, plus A1's
// skills check. (grok superpowers is the shared superpowers check.)
func CheckGrok(_ context.Context, c Config, _ execx.Runner) []Check {
	return []Check{grokMCPCheck(c), grokHooksCheck(c), grokAuthCheck(c), grokPluginsCheck(c),
		grokSkillsPathCheck(c), CheckSkills(c, KindGrok)}
}

func grokMCPCheck(c Config) Check {
	const name = "Grok MCP"
	m, fail := loadGrokConfig(c)
	if fail != "" {
		return Check{name, false, fail}
	}
	servers, _ := m["mcp_servers"].(map[string]any)
	entry, ok := servers["swarm"].(map[string]any)
	p := c.Grok("config.toml")
	if !ok {
		return Check{name, false, "The swarm MCP server is missing from " + p + ". Run swarm install."}
	}
	if cmd, _ := entry["command"].(string); cmd != c.Bin {
		return Check{name, false, "The swarm MCP server points at " + cmd + ". Run swarm install."}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{name, false, "The swarm binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{name, true, p}
}

func grokHooksCheck(c Config) Check {
	const name = "Grok hooks"
	m, fail := loadGrokConfig(c)
	if fail != "" {
		return Check{name, false, fail}
	}
	hooks, _ := m["hooks"].(map[string]any)
	var bad []string
	for _, ev := range grokHookEvents {
		want, found := c.Bin+" hook grok "+ev, false
		groups, _ := hooks[ev].([]any)
		for _, g := range groups {
			inner, _ := g.(map[string]any)["hooks"].([]any)
			for _, h := range inner {
				if cmd, _ := h.(map[string]any)["command"].(string); cmd == want {
					found = true
				}
			}
		}
		if !found {
			bad = append(bad, ev)
		}
	}
	if len(bad) > 0 {
		return Check{name, false, "Missing or stale swarm hooks for " + strings.Join(bad, ", ") + ". Run swarm install."}
	}
	return Check{name, true, c.Grok("config.toml")}
}

// grokAuthCheck wants both login files linked into ~/.grok. A real file there is
// the operator's own login and passes.
func grokAuthCheck(c Config) Check {
	const name = "Grok auth"
	own := false
	for _, f := range grokAuthFiles {
		dst := c.Grok(f)
		fi, err := os.Lstat(dst)
		if err != nil {
			return Check{name, false, dst + " is missing. Run swarm install."}
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			own = true
			continue
		}
		if cur, _ := os.Readlink(dst); cur != c.UserGrok(f) {
			return Check{name, false, dst + " points at " + cur + ", not " + c.UserGrok(f) + ". Run swarm install."}
		}
		if _, err := os.Stat(dst); err != nil {
			return Check{name, false, dst + " points at " + c.UserGrok(f) + ", which does not exist. Run grok login."}
		}
	}
	if own {
		return Check{name, true, "Using its own login file in " + c.Grok() + "."}
	}
	return Check{name, true, "Linked to " + c.UserGrok() + "."}
}

// grokPluginsCheck wants every Claude plugin disabled and superpowers not.
func grokPluginsCheck(c Config) Check {
	const name = "Grok plugins"
	m, fail := loadGrokConfig(c)
	if fail != "" {
		return Check{name, false, fail}
	}
	plugins, _ := m["plugins"].(map[string]any)
	disabled := map[string]bool{}
	list, _ := plugins["disabled"].([]any)
	for _, v := range list {
		s, _ := v.(string)
		disabled[s] = true
	}
	if disabled["superpowers"] {
		return Check{name, false, "superpowers is disabled in " + c.Grok("config.toml") + ". Run swarm install."}
	}
	var open []string
	for _, n := range claudePluginNames(c) {
		if n != "superpowers" && !disabled[n] {
			open = append(open, n)
		}
	}
	if len(open) > 0 {
		return Check{name, false, "Claude plugins not disabled for grok: " + strings.Join(open, ", ") + ". Run swarm install."}
	}
	return Check{name, true, "Claude plugins are disabled."}
}

func grokSkillsPathCheck(c Config) Check {
	const name = "Grok skills path"
	m, fail := loadGrokConfig(c)
	if fail != "" {
		return Check{name, false, fail}
	}
	skills, _ := m["skills"].(map[string]any)
	paths, _ := skills["paths"].([]any)
	if !slices.Contains(paths, any(c.SkillsDir(KindGrok))) {
		return Check{name, false, "[skills] paths in " + c.Grok("config.toml") + " lacks " + c.SkillsDir(KindGrok) + ". Run swarm install."}
	}
	return Check{name, true, c.SkillsDir(KindGrok)}
}

// UninstallGrok removes what install added and nothing else: the swarm MCP
// entry, swarm's hook commands, its skills paths, the auth links, and what the
// install record lists (the lock it seeded in ~/.grok, the plugin names it
// disabled). grok's own and the user's keys, the installed superpowers and the
// rest of ~/.grok are left. A config grok cannot parse is left alone and
// reported in skipped; the links and the lock still go. (Skills are removed by
// Uninstall's per-kind pass.)
func UninstallGrok(c Config) (removed, skipped []string, err error) {
	cfg := c.Grok("config.toml")
	rec := loadGrokRecord(c)
	wrote, err := editTOML(cfg, false, func(m map[string]any) error { dropGrokConfig(c, m, rec); return nil })
	switch {
	case errors.Is(err, errTOMLParse):
		skipped = append(skipped, cfg+": does not parse, left alone")
	case err != nil:
		return nil, nil, err
	default:
		if wrote {
			removed = append(removed, cfg)
		}
		if err := os.Remove(grokRecordPath(c)); err == nil {
			removed = append(removed, grokRecordPath(c))
		}
	}
	for _, f := range grokAuthFiles {
		dst := c.Grok(f)
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if cur, _ := os.Readlink(dst); cur == c.UserGrok(f) {
				if err := os.Remove(dst); err != nil {
					return removed, skipped, err
				}
				removed = append(removed, dst)
			}
		}
	}
	// A seeded lock is empty until grok first takes it (it writes pid:time then),
	// so a lock with content is in use and stays.
	if lock := c.UserGrok("auth.json.lock"); rec.SeededLock {
		if fi, err := os.Stat(lock); err == nil && fi.Size() == 0 {
			if err := os.Remove(lock); err != nil {
				return removed, skipped, err
			}
			removed = append(removed, lock)
		}
	}
	return removed, skipped, nil
}

func dropGrokConfig(c Config, m map[string]any, rec grokRecord) {
	if servers, ok := m["mcp_servers"].(map[string]any); ok {
		delete(servers, "swarm")
		if len(servers) == 0 {
			delete(m, "mcp_servers")
		}
	}
	if hooks, ok := m["hooks"].(map[string]any); ok {
		for ev, v := range hooks {
			groups, _ := v.([]any)
			if kept := dropGrokHookGroups(groups); len(kept) > 0 {
				hooks[ev] = kept
			} else {
				delete(hooks, ev)
			}
		}
		if len(hooks) == 0 {
			delete(m, "hooks")
		}
	}
	if plugins, ok := m["plugins"].(map[string]any); ok && len(rec.DisabledPlugins) > 0 {
		// ponytail: "disabled" is a bare name list, so an entry the operator
		// re-added under a recorded name after install is indistinguishable from
		// ours and is removed too. Telling them apart needs a per-entry marker
		// grok's config does not carry.
		list, _ := plugins["disabled"].([]any)
		list = slices.DeleteFunc(list, func(v any) bool { n, _ := v.(string); return slices.Contains(rec.DisabledPlugins, n) })
		if len(list) > 0 {
			plugins["disabled"] = list
		} else {
			delete(plugins, "disabled")
		}
		if len(plugins) == 0 {
			delete(m, "plugins")
		}
	}
	if skills, ok := m["skills"].(map[string]any); ok {
		dir := c.SkillsDir(KindGrok)
		paths, _ := skills["paths"].([]any)
		// Only the superpowers paths install recorded go; one the operator added
		// stays.
		skills["paths"] = slices.DeleteFunc(paths, func(p any) bool {
			s, _ := p.(string)
			return p == dir || slices.Contains(rec.SkillsPaths, s)
		})
		if len(skills["paths"].([]any)) == 0 {
			delete(skills, "paths")
		}
		if len(skills) == 0 {
			delete(m, "skills")
		}
	}
}

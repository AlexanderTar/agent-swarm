package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// UpdaterLabel is the v1 auto-update job. §21.3: it is booted out if loaded. Its
// plist may not exist on disk while the job is still loaded, so the bootout is
// attempted unconditionally and a not-loaded error is ignored (P1's notLoaded).
const UpdaterLabel = "dev.swarm.updater"

// ConfirmReleaseRemoval is the prompt §21.3's "after confirmation" requires. §17
// gives no copy for it; it is listed in T21's copy audit.
const ConfirmReleaseRemoval = "Remove Agent Swarm 1.x release folders (~/.swarm/app, ~/.swarm/bin/*.sh)? [y/N] "

// DeclinedReleaseRemoval is printed when the user says no or the run is not
// interactive. Declining is not an error.
const DeclinedReleaseRemoval = "Left the Agent Swarm 1.x release folders in place. Run swarm install --yes to remove them."

// legacyShellScripts are the v1 launcher scripts (§21.3). ~/.swarm/bin/swarm is
// deliberately not here: ~/.local/bin/swarm may still point at it until LinkBinary
// repoints the link, and deleting it first would leave a dangling `swarm` on PATH.
//
// Leftovers() also never checks for a stray v1 bin/swarm independently of this
// exclusion. That's only safe because Config.Bin currently *is*
// ~/.swarm/bin/swarm (v1's path) until LinkBinary/an install moves it: if
// Config.Bin's location ever changes, a v1 wrapper left at the old path would
// go permanently unreported. Whoever changes where Config.Bin points needs to
// revisit Leftovers() then.
var legacyShellScripts = []string{"swarmd-start.sh", "swarm-update.sh"}

// RemoveLegacyShared does the parts of §20 step 8 and §21.3 that are not tied to a
// single agent.
func RemoveLegacyShared(ctx context.Context, c Config, run execx.Runner, confirm func(string) bool) ([]string, error) {
	var changed []string

	target := fmt.Sprintf("gui/%d/%s", c.UID, UpdaterLabel)
	if _, err := run(ctx, "launchctl", "bootout", target); err != nil && !notLoaded(err) {
		return changed, fmt.Errorf("launchctl bootout %s: %w", UpdaterLabel, err)
	}

	app := filepath.Join(c.Home, "app")
	scripts := make([]string, 0, len(legacyShellScripts))
	for _, name := range legacyShellScripts {
		p := filepath.Join(c.Home, "bin", name)
		if _, err := os.Lstat(p); err == nil {
			scripts = append(scripts, p)
		}
	}
	_, appErr := os.Lstat(app)
	if appErr != nil && len(scripts) == 0 {
		return changed, nil
	}
	if confirm == nil || !confirm(ConfirmReleaseRemoval) {
		return changed, nil
	}
	if appErr == nil {
		if err := os.RemoveAll(app); err != nil {
			return changed, err
		}
		changed = append(changed, app)
	}
	for _, p := range scripts {
		if err := os.Remove(p); err != nil {
			return changed, err
		}
		changed = append(changed, p)
	}
	return changed, nil
}

// LinkBinary points ~/.local/bin/swarm at Config.Bin (§19). It replaces a symlink
// and refuses to replace a real file: that would be the user's own binary.
func LinkBinary(c Config) (bool, error) {
	link := c.LocalBin()
	fi, err := os.Lstat(link)
	switch {
	case err == nil && fi.Mode()&os.ModeSymlink != 0:
		if cur, err := os.Readlink(link); err == nil && cur == c.Bin {
			return false, nil
		}
		if err := os.Remove(link); err != nil {
			return false, err
		}
	case err == nil:
		return false, fmt.Errorf("%s is a real file, not a link; move it aside and run swarm install again", link)
	case !os.IsNotExist(err):
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return false, err
	}
	return true, os.Symlink(c.Bin, link)
}

// Leftover is one v1 location that is still present (§25, doctor --legacy).
type Leftover struct {
	Path string `json:"path"`
	What string `json:"what"`
}

// Leftovers lists every §20 location that still holds v1 state. The descriptions
// are diagnostics, not §17 copy.
func Leftovers(c Config) []Leftover {
	var out []Leftover
	add := func(path, what string) { out = append(out, Leftover{Path: path, What: what}) }

	claudeSkillsLink := c.Claude("skills", "swarm")
	for _, link := range []string{
		claudeSkillsLink,
		c.Cursor("plugins", "local", "swarm"),
		c.Gemini("config", "plugins", "swarm"),
	} {
		fi, err := os.Lstat(link)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		// A1: v2 legitimately symlinks Claude's own skills root into
		// ~/.swarm/skills; only a link that does not resolve there is v1's.
		if link == claudeSkillsLink {
			if skillsHome, err := SkillsHome(c.Home); err == nil {
				if owned, err := isSwarmOwned(link, skillsHome, true); err == nil && owned { // symlink-only check; adopt is moot here
					continue
				}
			}
		}
		add(link, "Agent Swarm 1.x symlink")
	}
	if body, err := os.ReadFile(c.Codex("config.toml")); err == nil {
		if _, removed := RemoveLegacyCodexMCP(string(body)); removed {
			add(c.Codex("config.toml"), "Agent Swarm 1.x [mcp_servers.swarm] table")
		}
	}
	if body, err := os.ReadFile(c.Gemini("GEMINI.md")); err == nil {
		if _, removed := RemoveMarkedSpan(string(body), "<!-- swarm:start -->", "<!-- swarm:end -->"); removed {
			add(c.Gemini("GEMINI.md"), "Agent Swarm 1.x instruction block")
		}
	}
	for _, p := range []string{c.Gemini("antigravity", "mcp_config.json"), c.Cursor("mcp.json")} {
		if hasLegacyMCPEntry(p, c.Bin) {
			add(p, "Agent Swarm 1.x swarm MCP entry")
		}
	}
	for _, p := range []string{c.Codex("hooks.json"), c.Cursor("hooks.json")} {
		if hasLegacyHook(p) {
			add(p, "Agent Swarm 1.x hook entry")
		}
	}
	if _, err := os.Lstat(filepath.Join(c.Home, "app")); err == nil {
		add(filepath.Join(c.Home, "app"), "Agent Swarm 1.x release folders")
	}
	for _, name := range legacyShellScripts {
		p := filepath.Join(c.Home, "bin", name)
		if _, err := os.Lstat(p); err == nil {
			add(p, "Agent Swarm 1.x launcher script")
		}
	}
	if _, err := os.Stat(filepath.Join(c.Home, "swarm.db")); err == nil && HasLegacyData(filepath.Join(c.Home, "swarm.db")) {
		add(filepath.Join(c.Home, "swarm.db"), "Agent Swarm 1.x database")
	}
	// D-15/decision 15: config.json's fields have no v2 equivalent (port/host are
	// now spec-locked, claimLeaseSeconds/janitor* are removed concepts, the
	// updater fields describe a daemon this migration removes, and internal/kb's
	// Ollama URL/model are hardcoded with no exposed setting). Nothing to import —
	// this only reports that the file exists, so the silence is a stated choice.
	if _, err := os.Stat(filepath.Join(c.Home, "config.json")); err == nil {
		add(filepath.Join(c.Home, "config.json"), "Agent Swarm 1.x settings file (not migrated — its settings have no v2 equivalent)")
	}
	return out
}

// hasLegacyMCPEntry reports a "swarm" MCP entry that is not the one this build
// installs — v1 ran a node script, so any command that is not bin counts.
func hasLegacyMCPEntry(path, bin string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var f struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(body, &f) != nil {
		return false
	}
	entry, ok := f.MCPServers["swarm"]
	return ok && entry.Command != bin
}

// hasLegacyHook reports a hook command that runs v1's node script.
func hasLegacyHook(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), "post-hook.mjs") || strings.Contains(string(body), "swarm-mcp.mjs")
}

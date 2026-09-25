package install

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// musePluginID is this manifest's "name" (native-plugin-contract.md's
// identifier grammar), and doubles as the installed.json key and the
// runtime-capability stable id prefix ("plugin:<id>:hook:<hook id>").
const musePluginID = "swarm"

// museHookEvents is every event Swarm's daemon needs from muse, in manifest
// order. Muse dispatches PreToolUse/PostToolUse for every tool call except
// its own request_user_input (Task 4/6 live probe: no hook ever fires for
// that one tool), and UserPromptSubmit for the human's typed replies -- the
// same three events claude registers per launch (claude.go:30,40-42), just
// wired once at install time instead (see WriteMuse's doc comment on why).
var museHookEvents = []string{"PreToolUse", "PostToolUse", "UserPromptSubmit"}

// musePluginDir is a Swarm-owned scratch directory the plugin manifest is
// written to before `muse plugins install <dir>` copies it into the real
// package cache (Task 4 item 1: installing always writes into the shared,
// non-isolatable XDG_DATA_HOME store, never a per-launch one). Any local
// path works as the install source -- muse's own "superpowers" plugin is
// installed this same way, from a plain local directory, not a workspace.
func musePluginDir(c Config) string { return c.Muse("plugin") }

// musePluginManifest is the .muse-plugin/plugin.json body (native-plugin-
// contract.md's manifest schema): one hook per museHookEvents entry, each
// running `swarm hook muse <event>` (cmd/swarm/hook.go:10), the same
// "swarm hook <agent> <event>" entry point every other adapter's install
// step wires up (agy.go's agyHook, codex's hook registration).
func musePluginManifest(c Config) map[string]any {
	hooks := make([]any, len(museHookEvents))
	for i, ev := range museHookEvents {
		hooks[i] = map[string]any{
			"id":        ev,
			"event":     ev,
			"command":   []string{c.Bin, "hook", "muse", ev},
			"timeoutMs": 10000,
		}
	}
	return map[string]any{
		"schemaVersion": 1,
		"name":          musePluginID,
		"displayName":   "Swarm",
		"version":       "1.0.0",
		"description":   "Swarm daemon hook wiring.",
		"compat":        map[string]any{"source": "native", "manifestDir": ".muse-plugin"},
		"capabilities": map[string]any{
			"skills": []any{}, "commands": []any{}, "hooks": hooks,
			"mcpServers": []any{}, "reminders": []any{},
		},
	}
}

// WriteMuse merges the swarm MCP server into muse's settings.json, installs
// the plugin that wires museHookEvents, and installs the swarm skills into
// the probed user-skills dir ($CONFIG_DIR/skills).
//
// No "env" key is written to settings.json (P0-1): muse takes only literal
// env values, never ${VAR} passthrough (confirmed 2026-09-23, see
// adapter/muse.go's museMCPEnv doc), and SWARM_SESSION/SWARM_TOKEN_FILE
// don't exist yet at install time -- startSession mints them fresh per
// launch. The real env wiring happens per-launch in adapter/muse.go's
// setupEnv, which isolates XDG_CONFIG_HOME and overwrites the swarm entry
// there with that launch's concrete values. This install-time entry is only
// what a manually-run `muse` outside the daemon sees, and stays a harmless
// no-auth optional server for that case.
//
// The plugin install has two steps against the real, shared muse CLI (Task
// 4 item 1: an isolated per-launch data dir can't hold it), both idempotent
// (probed live 2026-09-25/26, scratch plugin, cleaned up after): `muse
// plugins install <dir> --scope user --json` re-installing an unchanged
// manifest only bumps updated_at, and `muse plugins approve
// plugin:<id>:hook:<event>` on an already-trusted hook is a no-op. Approving
// each hook here -- before any muse TUI ever launches -- is what lets
// StartupDialogs stay empty (muse.go:360): the "Review plugin hooks" trust
// dialog (muse.go:377) only exists for a plugin awaiting review, and this
// step clears that state at install time, not first launch. The trust
// record itself lands in settings.json's runtime_capabilities key (probed
// live), which setupEnv's per-launch isolated copy already carries through
// unmodified (it unmarshals the whole real settings.json before touching
// only mcpServers/context) -- no extra plumbing needed for that part.
//
// KNOWN GAP (probed live 2026-09-26, scratch plugin + `muse plugins hook
// test ... --fixture`, cleaned up after): the hook subprocess's own env is
// NOT the invoking process's env passed through -- it is sandboxed to the
// same small fixed allowlist museMCPEnv's doc comment already found for
// muse's MCP subprocesses (HOME, PATH, USER, LANG, TERM, SHELL, PWD,
// LOGNAME, SHLVL, plus muse's own PLUGIN_*/MUSE_PLUGIN_* vars) -- an
// explicit `SWARM_SESSION=x muse plugins hook test ...` did not reach the
// hook script's env at all. `cmd/swarm/hook.go`'s client silently no-ops
// without SWARM_SESSION/SWARM_TOKEN_FILE/SWARM_URL in its own env
// (client.go:29-31, "a hook in a session the user started by hand"), so
// this plugin's hooks are UNREACHABLE end to end today: the manifest is
// installed and trusted correctly (both probed working above), but
// `swarm hook muse <event>` never sees the session's identity when muse
// actually invokes it. Fixing this needs a wrapper that carries the
// session's SWARM_* values through some channel other than env -- e.g. a
// per-launch file keyed by the session's cwd (present in every hook's
// stdin payload), read by a small script this plugin's "command" points at
// instead of the swarm binary directly. That wrapper is out of this task's
// scope (Task 7's file list); Task 9's refusal logic and the handler tests
// in Task 8 are unaffected (they exercise the daemon-side decision, not
// this transport), but a top-level muse agent's swarm_ask kind:"question"
// fallback is real and load-bearing until this is fixed.
func WriteMuse(ctx context.Context, c Config, run execx.Runner) ([]string, error) {
	var changed []string
	p := c.Muse("settings.json")
	wrote, err := EditJSON(p, true, func(m map[string]any) error {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["swarm"] = map[string]any{
			"mode":    "optional",
			"command": c.Bin,
			"args":    []string{"mcp"},
		}
		m["mcpServers"] = servers
		return nil
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, p)
	}

	dir := musePluginDir(c)
	if err := os.MkdirAll(filepath.Join(dir, ".muse-plugin"), 0o755); err != nil {
		return changed, err
	}
	manifest, err := json.MarshalIndent(musePluginManifest(c), "", "  ")
	if err != nil {
		return changed, err
	}
	manifestPath := filepath.Join(dir, ".muse-plugin", "plugin.json")
	manifestWrote, err := WriteIfChanged(manifestPath, append(manifest, '\n'), 0o644)
	if err != nil {
		return changed, err
	}
	if manifestWrote {
		changed = append(changed, manifestPath)
	}
	if _, err := run(ctx, "muse", "plugins", "install", dir, "--scope", "user", "--json"); err != nil {
		return changed, err
	}
	for _, ev := range museHookEvents {
		if _, err := run(ctx, "muse", "plugins", "approve",
			"plugin:"+musePluginID+":hook:"+ev, "--json"); err != nil {
			return changed, err
		}
	}

	skills, _, err := WriteSkills(c, KindMuse)
	return append(changed, skills...), err
}

// CheckMuse is doctor's muse block (plus A1's skills check).
func CheckMuse(ctx context.Context, c Config, run execx.Runner) []Check {
	return []Check{museMCPCheck(c), museHooksCheck(c), CheckSkills(c, KindMuse)}
}

// museHooksCheck reads the shared plugin store's installed.json directly
// (MuseData, not the per-config Muse() dir the manifest source lives under)
// -- the same "read the file the CLI itself trusts" shape as museMCPCheck
// and agyHooksCheck, rather than shelling out to `muse plugins inspect` for
// a hook-trust state this check does not attempt to read (see WriteMuse's
// doc comment: approval happens at install time, not verified again here).
func museHooksCheck(c Config) Check {
	p := c.MuseData("plugins", "installed.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"muse hooks", false, "Not installed. Run swarm install."}
	}
	var f struct {
		Plugins map[string]struct {
			Enabled bool `json:"enabled"`
			Source  struct {
				Path string `json:"path"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"muse hooks", false, "Invalid " + p + ". Run swarm install."}
	}
	entry, ok := f.Plugins[musePluginID]
	if !ok || !entry.Enabled {
		return Check{"muse hooks", false, "The swarm plugin is missing from " + p + ". Run swarm install."}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{"muse hooks", false, "The swarm binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{"muse hooks", true, p}
}

func museMCPCheck(c Config) Check {
	p := c.Muse("settings.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"muse MCP", false, "Not installed. Run swarm install."}
	}
	var f struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"muse MCP", false, "Invalid " + p + ". Run swarm install."}
	}
	entry, ok := f.MCPServers["swarm"]
	if !ok {
		return Check{"muse MCP", false, "The swarm MCP server is missing from " + p + ". Run swarm install."}
	}
	if entry.Command != c.Bin {
		return Check{"muse MCP", false, "The swarm MCP server points at " + entry.Command + ". Run swarm install."}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{"muse MCP", false, "The swarm binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{"muse MCP", true, p}
}

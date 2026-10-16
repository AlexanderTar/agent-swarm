package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
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

// isSwarmGrokHook is a hook command this installer wrote, whatever binary path it
// carried, so a moved binary replaces its entry instead of doubling it.
func isSwarmGrokHook(cmd string) bool { return strings.Contains(cmd, " hook grok ") }

// WriteGrok writes everything swarm install owns inside the Swarm grok home:
// the swarm MCP server, one hook per event and the skills path merged into
// config.toml (grok's own and the user's keys are kept), the skills links, and
// the auth links into ~/.grok. changed lists the paths written; skipped lists the
// ones left alone with the reason.
func WriteGrok(ctx context.Context, c Config, _ execx.Runner) (changed, skipped []string, err error) {
	if err := os.MkdirAll(c.Grok(), 0o755); err != nil {
		return nil, nil, err
	}
	cfg := c.Grok("config.toml")
	wrote, err := editTOML(cfg, func(m map[string]any) error { mergeGrokConfig(c, m); return nil })
	if err != nil {
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

// editTOML applies edit to the table at path (a missing file starts empty) and
// writes it back only when the bytes change. A file that does not parse is an
// error and is never overwritten.
func editTOML(path string, edit func(m map[string]any) error) (bool, error) {
	m := map[string]any{}
	if old, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(old, &m); err != nil {
			return false, fmt.Errorf("%s does not parse: %w", path, err)
		}
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

// dropGrokHookGroups removes the matcher groups whose commands are Swarm's.
func dropGrokHookGroups(groups []any) []any {
	var kept []any
	for _, g := range groups {
		obj, _ := g.(map[string]any)
		inner, _ := obj["hooks"].([]any)
		ours := false
		for _, h := range inner {
			cmd, _ := h.(map[string]any)["command"].(string)
			ours = ours || isSwarmGrokHook(cmd)
		}
		if !ours {
			kept = append(kept, g)
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

// CheckGrok is doctor's grok block. Unit 1 only has the skills check; the rest
// lands with the writer.
func CheckGrok(_ context.Context, c Config, _ execx.Runner) []Check {
	return []Check{CheckSkills(c, KindGrok)}
}

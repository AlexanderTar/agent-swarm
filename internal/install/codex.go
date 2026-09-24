package install

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/pelletier/go-toml/v2"
)

// codexEvents is §11.1's list. The two with a matcher are marked.
var codexEvents = []struct {
	Name    string
	Matcher string
}{
	{"SessionStart", ""},
	{"UserPromptSubmit", ""},
	{"PreToolUse", "*"},
	{"PostToolUse", "*"},
	{"PreCompact", ""},
	{"Stop", ""},
}

// codexHookTimeout is §11.1's 3 s, applied to every hook (§21.4 row 2 ported the
// old SessionEnd-only cap and made it unconditional).
const codexHookTimeout = 3

type codexHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type codexMatcher struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []codexHook `json:"hooks"`
}

// CodexHooks is the swarm half of ~/.codex/hooks.json, for tests and doctor.
func CodexHooks(c Config) []byte {
	hooks := map[string][]codexMatcher{}
	for _, ev := range codexEvents {
		hooks[ev.Name] = []codexMatcher{swarmCodexMatcher(c, ev.Name, ev.Matcher)}
	}
	body, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		panic(err) // a fixed shape cannot fail to marshal
	}
	return append(body, '\n')
}

func swarmCodexMatcher(c Config, event, matcher string) codexMatcher {
	return codexMatcher{Matcher: matcher, Hooks: []codexHook{{
		Type:    "command",
		Command: c.Bin + " hook codex " + event,
		Timeout: codexHookTimeout,
	}}}
}

// isSwarmHookCommand matches both the Go hook and v1's node script, so a stale
// entry is replaced instead of piling up next to the new one.
func isSwarmHookCommand(cmd string) bool {
	return strings.Contains(cmd, " hook codex ") || strings.Contains(cmd, " hook cursor ") ||
		strings.Contains(cmd, " hook agy ") || strings.Contains(cmd, " hook claude ") ||
		strings.Contains(cmd, "post-hook.mjs") || strings.Contains(cmd, "swarm-mcp.mjs")
}

// WriteCodex writes codex's hooks, trust entry and skills. It returns the paths it
// changed, so swarm install can report them and the migration can journal them.
func WriteCodex(c Config) ([]string, error) {
	var changed []string
	hooksPath := c.Codex("hooks.json")
	wrote, err := EditJSON(hooksPath, true, func(m map[string]any) error {
		return mergeCodexHooks(c, m)
	})
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, hooksPath)
	}

	cfgPath := c.Codex("config.toml")
	old, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return changed, err
	}
	text := string(old)
	for _, dir := range []string{c.Work(), c.Worktrees()} {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			real = dir // the folder may not exist yet on a first install
		}
		text, _ = CodexTrust(text, real)
	}
	// §11.1, M3: the MCP server is also global, registered once here just like
	// cursor's and agy's. Strip any existing [mcp_servers.swarm...] table first
	// (a stale binary path, a v1 remnant) and append a fresh one, so this is
	// idempotent and self-healing by construction rather than by diffing.
	text, _ = removeCodexSwarmTables(text)
	text = appendCodexSwarmMCP(text, c.Bin)
	if wrote, err := WriteIfChanged(cfgPath, []byte(text), 0o644); err != nil {
		return changed, err
	} else if wrote {
		changed = append(changed, cfgPath)
	}

	skills, _, err := WriteSkills(c, KindCodex)
	return append(changed, skills...), err
}

// mergeCodexHooks replaces swarm's entry for each §11.1 event and leaves every
// other matcher and every other event exactly as the user had it.
func mergeCodexHooks(c Config, m map[string]any) error {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		m["hooks"] = hooks
	}
	for _, ev := range codexEvents {
		var kept []any
		if list, ok := hooks[ev.Name].([]any); ok {
			for _, entry := range list {
				if !entryIsSwarm(entry) {
					kept = append(kept, entry)
				}
			}
		}
		mine, err := toAny(swarmCodexMatcher(c, ev.Name, ev.Matcher))
		if err != nil {
			return err
		}
		hooks[ev.Name] = append(kept, mine)
	}
	return nil
}

// entryIsSwarm reports whether a matcher entry (in its generic map form) holds a
// swarm hook command.
func entryIsSwarm(entry any) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	list, _ := obj["hooks"].([]any)
	for _, h := range list {
		hm, _ := h.(map[string]any)
		if cmd, _ := hm["command"].(string); isSwarmHookCommand(cmd) {
			return true
		}
	}
	return false
}

// toAny round-trips a typed value into the generic form EditJSON works with, so
// the merged file marshals with stable key order.
func toAny(v any) (any, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(body, &out)
}

// CodexTrust appends [projects."<realPath>"] trust_level = "trusted" when there is
// no entry for that path (§11.5). It is a text append on purpose: a go-toml
// round-trip would drop the user's comments and reorder their tables (rule 4).
func CodexTrust(text, realPath string) (string, bool) {
	header := fmt.Sprintf("[projects.%q]", realPath)
	if strings.Contains(text, header) {
		return text, false
	}
	block := header + "\ntrust_level = \"trusted\"\n"
	if strings.TrimSpace(text) == "" {
		return block, true
	}
	return strings.TrimRight(text, "\n") + "\n\n" + block, true
}

// appendCodexSwarmMCP appends codex's [mcp_servers.swarm] table (§11.1, M3): a
// plain text append, like CodexTrust, so the user's comments and table order
// survive. The caller strips any existing swarm table first.
func appendCodexSwarmMCP(text, bin string) string {
	block := fmt.Sprintf("[mcp_servers.swarm]\ncommand = %q\nargs = [\"mcp\"]\n", bin)
	if strings.TrimSpace(text) == "" {
		return block
	}
	return strings.TrimRight(text, "\n") + "\n\n" + block
}

var codexSwarmTable = regexp.MustCompile(`^\[mcp_servers\.swarm(\.[A-Za-z_][A-Za-z0-9_]*)?\]`)

// RemoveLegacyCodexMCP removes the v1 integration from a config.toml's text: the
// `# swarm:start`…`# swarm:end` span (keeping any non-swarm table that leaked
// inside it, §21.4 row 2) and the [mcp_servers.swarm] / [mcp_servers.swarm.env]
// tables wherever they are. Ported from v1's spliceSwarmTomlBlock (§21.2).
func RemoveLegacyCodexMCP(text string) (string, bool) {
	out, removedSpan := removeCodexSentinelSpan(text)
	out, removedTables := removeCodexSwarmTables(out)
	return out, removedSpan || removedTables
}

// removeCodexSentinelSpan drops the marker lines and everything between them, but
// re-emits any table inside the span whose header is not a swarm one.
func removeCodexSentinelSpan(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	from, to := -1, -1
	for i, l := range lines {
		if from < 0 && strings.HasPrefix(strings.TrimSpace(l), "# swarm:start") {
			from = i
			continue
		}
		if from >= 0 && strings.HasPrefix(strings.TrimSpace(l), "# swarm:end") {
			to = i
			break
		}
	}
	if from < 0 || to < 0 {
		return text, false
	}
	kept, _ := removeCodexSwarmTables(strings.Join(lines[from+1:to], "\n"))
	body := strings.TrimSpace(kept)
	var out []string
	out = append(out, lines[:from]...)
	if body != "" {
		out = append(out, body)
	}
	out = append(out, lines[to+1:]...)
	return blankRun.ReplaceAllString(strings.Join(out, "\n"), "\n\n"), true
}

// removeCodexSwarmTables drops each [mcp_servers.swarm…] table, from its header to
// the line before the next table header (or the end of the text).
func removeCodexSwarmTables(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	var out []string
	removed, skipping := false, false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "[") {
			skipping = codexSwarmTable.MatchString(trimmed)
			if skipping {
				removed = true
				continue
			}
		}
		if skipping {
			continue
		}
		out = append(out, l)
	}
	return blankRun.ReplaceAllString(strings.Join(out, "\n"), "\n\n"), removed
}

// RemoveLegacyCodex removes the v1 codex integration from disk. It never creates a
// file, and never rewrites one that is already clean.
func RemoveLegacyCodex(c Config) ([]string, error) {
	var changed []string
	cfgPath := c.Codex("config.toml")
	old, err := os.ReadFile(cfgPath)
	if err == nil {
		before := map[string]any{}
		parsedBefore := toml.Unmarshal(old, &before) == nil
		next, removed := RemoveLegacyCodexMCP(string(old))
		if removed {
			// Validate rather than round-trip (rule 4): the result must still parse,
			// and every top-level key the user had must still be there.
			after := map[string]any{}
			if err := toml.Unmarshal([]byte(next), &after); err != nil {
				return changed, fmt.Errorf("%s: removing the v1 block would leave invalid TOML: %w", cfgPath, err)
			}
			if parsedBefore {
				for k := range before {
					if _, ok := after[k]; !ok && k != "mcp_servers" {
						return changed, fmt.Errorf("%s: removing the v1 block would drop [%s]", cfgPath, k)
					}
				}
			}
			if _, err := WriteIfChanged(cfgPath, []byte(next), 0o644); err != nil {
				return changed, err
			}
			changed = append(changed, cfgPath)
		}
	} else if !os.IsNotExist(err) {
		return changed, err
	}

	// v1 also merged hooks into ~/.codex/hooks.json. Drop its entries without
	// creating the file if it is absent.
	hooksPath := c.Codex("hooks.json")
	wrote, err := EditJSON(hooksPath, false, dropSwarmHookEntries)
	if err != nil {
		return changed, err
	}
	if wrote {
		changed = append(changed, hooksPath)
	}
	return changed, nil
}

// dropSwarmHookEntries removes every swarm matcher entry from a hooks.json,
// leaving empty event keys out rather than as empty arrays.
func dropSwarmHookEntries(m map[string]any) error {
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	for ev, v := range hooks {
		list, ok := v.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, entry := range list {
			if !entryIsSwarm(entry) {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(hooks, ev)
			continue
		}
		hooks[ev] = kept
	}
	return nil
}

// CheckCodex is doctor's codex block: hooks, the leftover MCP table (§11.1: a
// failure, not a warning) and account attribution (§25).
func CheckCodex(ctx context.Context, c Config, run execx.Runner) []Check {
	return []Check{codexHookCheck(c), codexMCPCheck(c), codexAttributionCheck(c), CheckSkills(c, KindCodex)}
}

func codexHookCheck(c Config) Check {
	p := c.Codex("hooks.json")
	body, err := os.ReadFile(p)
	if err != nil {
		return Check{"Codex hooks", false, "Not installed. Run swarm install."}
	}
	if strings.Contains(string(body), "${PLUGIN_ROOT}") {
		return Check{"Codex hooks", false, p + " still uses ${PLUGIN_ROOT}. Run swarm install."}
	}
	var f struct {
		Hooks map[string][]codexMatcher `json:"hooks"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return Check{"Codex hooks", false, "Invalid " + p + ". Run swarm install."}
	}
	var missing []string
	for _, ev := range codexEvents {
		found := false
		for _, entry := range f.Hooks[ev.Name] {
			for _, h := range entry.Hooks {
				if h.Command != c.Bin+" hook codex "+ev.Name || h.Timeout > codexHookTimeout {
					continue
				}
				found = true
			}
		}
		if !found {
			missing = append(missing, ev.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Check{"Codex hooks", false, "Missing or stale: " + strings.Join(missing, ", ") + ". Run swarm install."}
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Check{"Codex hooks", false, "The hook binary is missing: " + c.Bin + ". Run make install."}
	}
	return Check{"Codex hooks", true, p}
}

func codexMCPCheck(c Config) Check {
	body, err := os.ReadFile(c.Codex("config.toml"))
	if err != nil {
		return Check{"Codex MCP", true, "No Agent Swarm 1.x MCP table."}
	}
	if _, removed := RemoveLegacyCodexMCP(string(body)); removed {
		return Check{"Codex MCP", false,
			"Agent Swarm 1.x [mcp_servers.swarm] is still in " + c.Codex("config.toml") + ". Run swarm install."}
	}
	return Check{"Codex MCP", true, "No Agent Swarm 1.x MCP table."}
}

// codexAttributionCheck reads the newest rollout and warns when the account has
// git attribution on (§25: there is no local switch, so this is all doctor can do).
func codexAttributionCheck(c Config) Check {
	newest, err := newestFile(c.Codex("sessions"), "rollout-", ".jsonl")
	if err != nil || newest == "" {
		return Check{"Codex attribution", true, "No recent codex session to check."}
	}
	f, err := os.Open(newest)
	if err != nil {
		return Check{"Codex attribution", true, "No recent codex session to check."}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"git_attribution":true`) {
			return Check{"Codex attribution", false,
				"Codex attributes commits to the agent on this account. Turn it off in ChatGPT settings; the Swarm hook blocks the trailer meanwhile."}
		}
	}
	return Check{"Codex attribution", true, "Codex does not attribute commits to the agent."}
}

// newestFile walks root and returns the most recently modified file whose name has
// the given prefix and suffix. An absent root is not an error.
func newestFile(root, prefix, suffix string) (string, error) {
	var best string
	var bestMod int64
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), prefix) || !strings.HasSuffix(d.Name(), suffix) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if ms := fi.ModTime().UnixMilli(); ms > bestMod {
			best, bestMod = p, ms
		}
		return nil
	})
	if os.IsNotExist(err) {
		return "", nil
	}
	return best, err
}

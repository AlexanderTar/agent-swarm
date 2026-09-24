package install_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/pelletier/go-toml/v2"
)

// fakeHome builds a Config rooted entirely under t.TempDir() (S-5). Every install
// test in this package uses it; no test ever names a real path.
func fakeHome(t *testing.T) install.Config {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".swarm", "bin", "swarm")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return install.Config{
		Bin: bin, Home: filepath.Join(home, ".swarm"), UserHome: home,
		LaunchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		UID:             501, User: "fake",
	}
}

// seedFile copies a testdata fixture to dst inside the fake home.
func seedFile(t *testing.T, fixture, dst string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// §11.1: the six events, each an absolute path with timeout 3.
func TestCodexHooksCarriesTheSixEventsWithAnAbsolutePathAndTheThreeSecondCap(t *testing.T) {
	c := fakeHome(t)
	var f struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(install.CodexHooks(c), &f); err != nil {
		t.Fatal(err)
	}
	want := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact", "Stop"}
	if len(f.Hooks) != len(want) {
		t.Fatalf("events = %d, want %d: %v", len(f.Hooks), len(want), f.Hooks)
	}
	for _, ev := range want {
		entries, ok := f.Hooks[ev]
		if !ok || len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatalf("%s = %+v", ev, entries)
		}
		h := entries[0].Hooks[0]
		if h.Type != "command" {
			t.Errorf("%s type = %q", ev, h.Type)
		}
		if h.Command != c.Bin+" hook codex "+ev {
			t.Errorf("%s command = %q, want %q", ev, h.Command, c.Bin+" hook codex "+ev)
		}
		if !filepath.IsAbs(strings.Fields(h.Command)[0]) {
			t.Errorf("%s command is not an absolute path: %q", ev, h.Command)
		}
		if h.Timeout != 3 {
			t.Errorf("%s timeout = %d, want 3 for every hook (§21.4)", ev, h.Timeout)
		}
		wantMatcher := ""
		if ev == "PreToolUse" || ev == "PostToolUse" {
			wantMatcher = "*"
		}
		if entries[0].Matcher != wantMatcher {
			t.Errorf("%s matcher = %q, want %q", ev, entries[0].Matcher, wantMatcher)
		}
	}
	if strings.Contains(string(install.CodexHooks(c)), "PLUGIN_ROOT") {
		t.Error("PLUGIN_ROOT must never appear in a generated command (§21.4)")
	}
}

func TestWriteCodexKeepsTheUsersOtherHooksAndIsIdempotent(t *testing.T) {
	c := fakeHome(t)
	p := c.Codex("hooks.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/usr/bin/true","timeout":5}]}],` +
		`"Notification":[{"hooks":[{"type":"command","command":"/usr/bin/say hi","timeout":2}]}]}}`
	if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := install.WriteCodex(c); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	s := string(body)
	if !strings.Contains(s, "/usr/bin/true") {
		t.Error("the user's own SessionStart hook was dropped")
	}
	if !strings.Contains(s, "/usr/bin/say hi") {
		t.Error("the user's Notification hook was dropped")
	}
	if !strings.Contains(s, c.Bin+" hook codex SessionStart") {
		t.Error("swarm's SessionStart hook was not added")
	}
	changed, err := install.WriteCodex(c)
	if err != nil || len(changed) != 0 {
		t.Fatalf("second WriteCodex changed %v, %v; must be a no-op", changed, err)
	}
}

// A stale swarm entry (a different binary path, or the v1 node script) is replaced,
// not duplicated.
func TestWriteCodexReplacesAStaleSwarmHookRatherThanAddingASecond(t *testing.T) {
	c := fakeHome(t)
	p := c.Codex("hooks.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"node /old/plugin/hooks/post-hook.mjs codex Stop","timeout":5}]},` +
		`{"hooks":[{"type":"command","command":"/old/swarm hook codex Stop","timeout":3}]}]}}`
	if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := install.WriteCodex(c); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	s := string(body)
	if strings.Contains(s, "post-hook.mjs") || strings.Contains(s, "/old/swarm") {
		t.Errorf("stale entries survived:\n%s", s)
	}
	if n := strings.Count(s, "hook codex Stop"); n != 1 {
		t.Errorf("Stop has %d swarm hooks, want 1:\n%s", n, s)
	}
}

// §11.5/§12.1: install trusts both the neutral work folder and the centralized
// worktree folder, in the same pass, and a second run adds neither again.
func TestWriteCodexTrustsBothWorkAndWorktreesInOnePass(t *testing.T) {
	c := fakeHome(t)
	if _, err := install.WriteCodex(c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(c.Codex("config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	wantWork := "[projects.\"" + c.Work() + "\"]\ntrust_level = \"trusted\""
	wantWorktrees := "[projects.\"" + c.Worktrees() + "\"]\ntrust_level = \"trusted\""
	if !strings.Contains(s, wantWork) {
		t.Errorf("missing work trust entry:\n%s", s)
	}
	if !strings.Contains(s, wantWorktrees) {
		t.Errorf("missing worktrees trust entry:\n%s", s)
	}
	if c.Work() == c.Worktrees() {
		t.Fatal("Work() and Worktrees() must be distinct paths")
	}
	changed, err := install.WriteCodex(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range changed {
		if p == c.Codex("config.toml") {
			t.Fatalf("second WriteCodex rewrote config.toml; changed = %v", changed)
		}
	}
	body2, _ := os.ReadFile(c.Codex("config.toml"))
	if string(body2) != s {
		t.Error("second WriteCodex must be a no-op for both trust entries")
	}
}

// §11.1, M3: the MCP server is global for codex too, just like cursor's and
// agy's, written directly into config.toml (codex has no `mcp add` CLI command).
func TestWriteCodexAddsTheGlobalMCPTableAndReplacesAStaleOne(t *testing.T) {
	c := fakeHome(t)
	if _, err := install.WriteCodex(c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(c.Codex("config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "[mcp_servers.swarm]\ncommand = \"" + c.Bin + "\"\nargs = [\"mcp\"]"
	if !strings.Contains(string(body), want) {
		t.Fatalf("missing swarm MCP table:\n%s", body)
	}

	// A stale table (an old binary path) must be replaced, not left alongside a new one.
	stale := strings.ReplaceAll(string(body), c.Bin, "/old/path/swarm")
	if err := os.WriteFile(c.Codex("config.toml"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := install.WriteCodex(c); err != nil {
		t.Fatal(err)
	}
	body2, err := os.ReadFile(c.Codex("config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body2), "/old/path/swarm") {
		t.Errorf("stale swarm MCP table survived:\n%s", body2)
	}
	if n := strings.Count(string(body2), "[mcp_servers.swarm]"); n != 1 {
		t.Errorf("[mcp_servers.swarm] appears %d times, want exactly 1:\n%s", n, body2)
	}
}

// Same as above, but with both directories actually on disk: on macOS t.TempDir()
// resolves through /private, so this exercises the EvalSymlinks branch (not just
// its not-yet-existing fallback) and proves the two entries are genuinely
// distinct resolved paths, not a copy-paste of the same one.
func TestWriteCodexResolvesSymlinksForBothWorkAndWorktrees(t *testing.T) {
	c := fakeHome(t)
	if err := os.MkdirAll(c.Work(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Worktrees(), 0o755); err != nil {
		t.Fatal(err)
	}
	realWork, err := filepath.EvalSymlinks(c.Work())
	if err != nil {
		t.Fatal(err)
	}
	realWorktrees, err := filepath.EvalSymlinks(c.Worktrees())
	if err != nil {
		t.Fatal(err)
	}
	if realWork == realWorktrees {
		t.Fatal("resolved Work() and Worktrees() must be distinct paths")
	}
	if _, err := install.WriteCodex(c); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(c.Codex("config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "[projects.\""+realWork+"\"]\ntrust_level = \"trusted\"") {
		t.Errorf("missing resolved work trust entry:\n%s", s)
	}
	if !strings.Contains(s, "[projects.\""+realWorktrees+"\"]\ntrust_level = \"trusted\"") {
		t.Errorf("missing resolved worktrees trust entry:\n%s", s)
	}
}

// §11.5: install trusts the realpath of ~/.swarm/work, once, as a text append.
func TestCodexTrustAppendsOnceAndKeepsExistingEntries(t *testing.T) {
	in := "model = \"gpt-5.5\"\n\n[projects.\"/Users/fake/GitHub/agent-swarm\"]\ntrust_level = \"trusted\"\n"
	out, added := install.CodexTrust(in, "/private/fake/.swarm/work")
	if !added {
		t.Fatal("added = false")
	}
	if !strings.Contains(out, "[projects.\"/private/fake/.swarm/work\"]\ntrust_level = \"trusted\"") {
		t.Errorf("missing entry:\n%s", out)
	}
	if !strings.Contains(out, "[projects.\"/Users/fake/GitHub/agent-swarm\"]") {
		t.Error("the existing trust entry was lost")
	}
	if again, added := install.CodexTrust(out, "/private/fake/.swarm/work"); added || again != out {
		t.Error("a second call must change nothing")
	}
	var m map[string]any
	if err := toml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
}

// TestCodexTrustRecognizesASingleQuotedExistingEntry is the 2026-09-24 fix:
// codex's own writer uses TOML literal (single-quoted) strings for a
// project's table header, not the double-quoted form %q produces. Before
// this fix, a path codex already trusted this way went unrecognized,
// duplicating the table -- which TOML rejects, breaking install's later
// legacy-removal edit on the same file.
func TestCodexTrustRecognizesASingleQuotedExistingEntry(t *testing.T) {
	in := "[projects.'/private/fake/.swarm/work']\ntrust_level = \"trusted\"\n"
	out, added := install.CodexTrust(in, "/private/fake/.swarm/work")
	if added || out != in {
		t.Fatalf("a single-quoted existing entry must be recognized: added = %v, out:\n%s", added, out)
	}
	var m map[string]any
	if err := toml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
}

// §20 step 8 + §21.4 row 2, from the fixture.
func TestRemoveLegacyCodexMCPTakesTheSwarmTablesAndNothingElse(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "codex", "config-v1.toml"))
	if err != nil {
		t.Fatal(err)
	}
	out, removed := install.RemoveLegacyCodexMCP(string(body))
	if !removed {
		t.Fatal("removed = false")
	}
	for _, gone := range []string{"mcp_servers.swarm", "swarm:start", "swarm:end", "swarm-mcp.mjs", "SWARM_REGISTER_SESSION"} {
		if strings.Contains(out, gone) {
			t.Errorf("kept %q:\n%s", gone, out)
		}
	}
	for _, keep := range []string{
		"[hooks.state]",        // §21.4: leaked between the sentinels; must survive
		"[mcp_servers.vercel]", // the user's own server
		"${AUTH_TOKEN}",        // the comment-and-placeholder round-trip hazard (rule 4)
		"[projects.\"/Users/fake/GitHub/agent-swarm\"]",
		"[tui.model_availability_nux]",
		"model = \"gpt-5.5\"",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	var m map[string]any
	if err := toml.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if servers, ok := m["mcp_servers"].(map[string]any); ok {
		if _, bad := servers["swarm"]; bad {
			t.Error("mcp_servers.swarm still parses out of the result")
		}
	}
	if _, removed := install.RemoveLegacyCodexMCP(out); removed {
		t.Error("a second pass must report nothing removed")
	}
}

// The tables can also exist without the sentinel comments (a hand-edited file).
func TestRemoveLegacyCodexMCPHandlesTheTablesWithoutSentinels(t *testing.T) {
	in := "[mcp_servers.swarm]\ncommand = \"node\"\n\n[mcp_servers.swarm.env]\nSWARM_URL = \"x\"\n\n[mcp_servers.other]\ncommand = \"y\"\n"
	out, removed := install.RemoveLegacyCodexMCP(in)
	if !removed {
		t.Fatal("removed = false")
	}
	if strings.Contains(out, "mcp_servers.swarm") {
		t.Errorf("kept the swarm tables:\n%s", out)
	}
	if !strings.Contains(out, "[mcp_servers.other]") {
		t.Errorf("lost the other server:\n%s", out)
	}
}

func TestRemoveLegacyCodexOnACleanFileChangesNothing(t *testing.T) {
	c := fakeHome(t)
	p := c.Codex("config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := "model = \"gpt-5.5\"\n"
	if err := os.WriteFile(p, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	changed, err := install.RemoveLegacyCodex(c)
	if err != nil || len(changed) != 0 {
		t.Fatalf("= %v, %v", changed, err)
	}
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(st.ModTime()) {
		t.Error("an already-clean file must not be rewritten")
	}
}

func TestRemoveLegacyCodexOnAMissingFileIsANoOp(t *testing.T) {
	c := fakeHome(t) // no ~/.codex at all
	changed, err := install.RemoveLegacyCodex(c)
	if err != nil || len(changed) != 0 {
		t.Fatalf("= %v, %v", changed, err)
	}
	if _, err := os.Stat(c.Codex("config.toml")); !os.IsNotExist(err) {
		t.Error("removal created a config file the user never had")
	}
}

// §11.1: plain `swarm doctor` FAILS while a leftover [mcp_servers.swarm] exists.
func TestCheckCodexFailsOnALeftoverMCPTableAndOnABadHookPath(t *testing.T) {
	c := fakeHome(t)
	seedFile(t, filepath.Join("codex", "config-v1.toml"), c.Codex("config.toml"))
	if _, err := install.WriteCodex(c); err != nil {
		t.Fatal(err)
	}
	run := (&execx.Fake{Responses: map[string]execx.Result{}}).Runner()
	byName := func(cs []install.Check, name string) install.Check {
		for _, ch := range cs {
			if ch.Name == name {
				return ch
			}
		}
		t.Fatalf("no check named %q in %+v", name, cs)
		return install.Check{}
	}
	got := install.CheckCodex(context.Background(), c, run)
	if mcp := byName(got, "Codex MCP"); mcp.OK {
		t.Error("Codex MCP must fail while [mcp_servers.swarm] is present (§11.1)")
	}
	if h := byName(got, "Codex hooks"); !h.OK {
		t.Errorf("Codex hooks = %+v, want ok after WriteCodex", h)
	}

	// Remove the legacy table: the MCP check flips to ok.
	if _, err := install.RemoveLegacyCodex(c); err != nil {
		t.Fatal(err)
	}
	if mcp := byName(install.CheckCodex(context.Background(), c, run), "Codex MCP"); !mcp.OK {
		t.Errorf("Codex MCP = %+v after removal", mcp)
	}

	// A hook pointing at a binary that is gone must fail (§21.4 row 2).
	if err := os.Remove(c.Bin); err != nil {
		t.Fatal(err)
	}
	if h := byName(install.CheckCodex(context.Background(), c, run), "Codex hooks"); h.OK {
		t.Error("Codex hooks must fail when the hook binary is missing")
	}
}

// §25 / L23: doctor warns when a recent rollout records "git_attribution":true.
func TestCheckCodexWarnsOnGitAttributionInTheNewestRollout(t *testing.T) {
	c := fakeHome(t)
	dir := c.Codex("sessions", "2026", "09", "18")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	roll := filepath.Join(dir, "rollout-2026-09-18T10-00-00-abc.jsonl")
	if err := os.WriteFile(roll, []byte(`{"type":"context","git_attribution":true}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := (&execx.Fake{Responses: map[string]execx.Result{}}).Runner()
	var found bool
	for _, ch := range install.CheckCodex(context.Background(), c, run) {
		if ch.Name == "Codex attribution" {
			found = true
			if ch.OK {
				t.Errorf("Codex attribution = %+v, want a failure the user can act on", ch)
			}
		}
	}
	if !found {
		t.Fatal("no Codex attribution check")
	}
}

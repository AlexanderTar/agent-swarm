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
)

// §11.1 / L16: every variable is ${env:NAME} because cursor passes no environment;
// SWARM_AGENT_KIND is the literal "cursor".
func TestCursorMCPMapsEveryVariableWithEnvPlaceholders(t *testing.T) {
	c := fakeHome(t)
	entry := install.CursorMCP(c)
	if entry["type"] != "stdio" || entry["command"] != c.Bin {
		t.Fatalf("entry = %+v", entry)
	}
	args, _ := entry["args"].([]string)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", entry["args"])
	}
	env, _ := entry["env"].(map[string]string)
	want := map[string]string{
		"SWARM_URL":        "${env:SWARM_URL}",
		"SWARM_SESSION":    "${env:SWARM_SESSION}",
		"SWARM_TOKEN_FILE": "${env:SWARM_TOKEN_FILE}",
		"SWARM_AGENT_KIND": "cursor",
	}
	if len(env) != len(want) {
		t.Fatalf("env = %+v, want %+v", env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
}

// §11.1: lowerCamelCase events, six of them.
func TestCursorHooksCarriesTheSixLowerCamelCaseEvents(t *testing.T) {
	c := fakeHome(t)
	var f struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(install.CursorHooks(c), &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 {
		t.Errorf("version = %d, want 1", f.Version)
	}
	want := []string{"sessionStart", "beforeSubmitPrompt", "preToolUse", "postToolUse", "preCompact", "stop"}
	if len(f.Hooks) != len(want) {
		t.Fatalf("events = %v", f.Hooks)
	}
	for _, ev := range want {
		list := f.Hooks[ev]
		if len(list) != 1 || list[0].Command != c.Bin+" hook cursor "+ev {
			t.Errorf("%s = %+v, want one command %q", ev, list, c.Bin+" hook cursor "+ev)
		}
	}
}

func TestWriteCursorKeepsTheUsersOtherServersHooksAndConfigKeys(t *testing.T) {
	c := fakeHome(t)
	mcp := c.Cursor("mcp.json")
	hooks := c.Cursor("hooks.json")
	cli := c.Cursor("cli-config.json")
	if err := os.MkdirAll(c.Cursor(), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, seed := range map[string]string{
		mcp:   `{"mcpServers":{"vercel":{"command":"npx"}}}`,
		hooks: `{"version":1,"hooks":{"afterFileEdit":[{"command":"/usr/bin/true"}]}}`,
		cli:   `{"authInfo":{"token":"keep-me"},"attribution":{"attributeCommitsToAgent":true,"attributePRsToAgent":true}}`,
	} {
		if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := install.WriteCursor(c); err != nil {
		t.Fatal(err)
	}

	var m struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	body, _ := os.ReadFile(mcp)
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.MCPServers["vercel"]; !ok {
		t.Error("the user's vercel server was dropped")
	}
	if _, ok := m.MCPServers["swarm"]; !ok {
		t.Error("swarm was not added")
	}

	hb, _ := os.ReadFile(hooks)
	if !strings.Contains(string(hb), "afterFileEdit") {
		t.Errorf("the user's afterFileEdit hook was dropped:\n%s", hb)
	}
	if !strings.Contains(string(hb), c.Bin+" hook cursor stop") {
		t.Errorf("swarm's stop hook was not added:\n%s", hb)
	}

	// L23: attribution off, every other key kept — including auth.
	var cfg struct {
		AuthInfo    map[string]any `json:"authInfo"`
		Attribution struct {
			Commits *bool `json:"attributeCommitsToAgent"`
			PRs     *bool `json:"attributePRsToAgent"`
		} `json:"attribution"`
	}
	cb, _ := os.ReadFile(cli)
	if err := json.Unmarshal(cb, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AuthInfo["token"] != "keep-me" {
		t.Errorf("cli-config.json lost authInfo: %s", cb)
	}
	if cfg.Attribution.Commits == nil || *cfg.Attribution.Commits {
		t.Errorf("attributeCommitsToAgent = %v, want false (L23)", cfg.Attribution.Commits)
	}
	if cfg.Attribution.PRs == nil || *cfg.Attribution.PRs {
		t.Errorf("attributePRsToAgent = %v, want false (L23)", cfg.Attribution.PRs)
	}

	changed, err := install.WriteCursor(c)
	if err != nil || len(changed) != 0 {
		t.Fatalf("second WriteCursor changed %v, %v; must be a no-op", changed, err)
	}
}

// Verified 2026-09-18: ~/.cursor/plugins/local/swarm is a symlink (P0-8 explains
// why cursor never loaded it).
func TestRemoveLegacyCursorRemovesTheLocalPluginLinkAndTheV1Entries(t *testing.T) {
	c := fakeHome(t)
	target := filepath.Join(c.Home, "app", "current", "plugin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "plugin.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := c.Cursor("plugins", "local", "swarm")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// A real vendored plugin next to it must survive.
	sp := c.Cursor("plugins", "local", "superpowers")
	if err := os.MkdirAll(sp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Cursor("mcp.json"), []byte(`{"mcpServers":{"swarm":{"command":"node"},"other":{"command":"y"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Cursor("hooks.json"),
		[]byte(`{"version":1,"hooks":{"stop":[{"command":"node /old/plugin/hooks/post-hook.mjs cursor stop"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := install.RemoveLegacyCursor(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("the v1 local-plugin link survived")
	}
	if _, err := os.Stat(filepath.Join(target, "plugin.json")); err != nil {
		t.Fatal("the symlink target was followed and deleted")
	}
	if _, err := os.Stat(sp); err != nil {
		t.Error("the vendored superpowers plugin was removed; swarm uninstall leaves plugins installed (§12.4)")
	}
	mb, _ := os.ReadFile(c.Cursor("mcp.json"))
	if strings.Contains(string(mb), `"swarm"`) {
		t.Errorf("the v1 mcp.json entry survived:\n%s", mb)
	}
	if !strings.Contains(string(mb), `"other"`) {
		t.Errorf("another server was dropped:\n%s", mb)
	}
	hb, _ := os.ReadFile(c.Cursor("hooks.json"))
	if strings.Contains(string(hb), "post-hook.mjs") {
		t.Errorf("the v1 hook survived:\n%s", hb)
	}
}

func TestRemoveLegacyCursorOnACleanHomeCreatesNothing(t *testing.T) {
	c := fakeHome(t)
	changed, err := install.RemoveLegacyCursor(c)
	if err != nil || len(changed) != 0 {
		t.Fatalf("= %v, %v", changed, err)
	}
	for _, p := range []string{c.Cursor("mcp.json"), c.Cursor("hooks.json"), c.Cursor("cli-config.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("removal created %s", p)
		}
	}
}

// §23.4 item 8's precondition: doctor must notice when attribution is back on.
func TestCheckCursorFailsWhenAttributionIsOnAndWhenTheMCPEntryIsMissing(t *testing.T) {
	c := fakeHome(t)
	run := (&execx.Fake{Responses: map[string]execx.Result{}}).Runner()
	if ch := findCheck(t, install.CheckCursor(context.Background(), c, run), "Cursor MCP"); ch.OK {
		t.Error("Cursor MCP must fail before install")
	}
	if _, err := install.WriteCursor(c); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Cursor MCP", "Cursor hooks", "Cursor attribution"} {
		if ch := findCheck(t, install.CheckCursor(context.Background(), c, run), name); !ch.OK {
			t.Errorf("%s = %+v after WriteCursor", name, ch)
		}
	}
	// The user (or a cursor update) turns attribution back on.
	if err := os.WriteFile(c.Cursor("cli-config.json"),
		[]byte(`{"attribution":{"attributeCommitsToAgent":true,"attributePRsToAgent":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckCursor(context.Background(), c, run), "Cursor attribution"); ch.OK {
		t.Error("Cursor attribution must fail when attributeCommitsToAgent is true (L23)")
	}
}

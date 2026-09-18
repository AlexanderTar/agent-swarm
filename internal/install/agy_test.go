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

// §11.1 + §21.4 row 1: flat commands for Pre/PostInvocation and Stop, a matcher
// wrapper for the two tool events, no PLUGIN_ROOT, and five keys (agy has no
// SessionStart and no compaction event).
func TestAgyHooksUsesFlatHandlersForInvocationAndStopAndMatchersForTools(t *testing.T) {
	c := fakeHome(t)
	var f map[string]map[string]json.RawMessage
	if err := json.Unmarshal(install.AgyHooks(c), &f); err != nil {
		t.Fatal(err)
	}
	named, ok := f["swarm"]
	if !ok {
		t.Fatalf("hooks.json is not keyed by the hook name `swarm`: %v", f)
	}
	if len(named) != 5 {
		t.Errorf("events = %d, want 5: %v", len(named), named)
	}
	for _, ev := range []string{"PreInvocation", "PostInvocation", "Stop"} {
		var flat []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
			Matcher string `json:"matcher"`
			Hooks   []any  `json:"hooks"`
		}
		if err := json.Unmarshal(named[ev], &flat); err != nil {
			t.Fatalf("%s: %v", ev, err)
		}
		if len(flat) != 1 {
			t.Fatalf("%s = %+v", ev, flat)
		}
		if flat[0].Command != c.Bin+" hook agy "+ev {
			t.Errorf("%s command = %q", ev, flat[0].Command)
		}
		if flat[0].Timeout != 3 {
			t.Errorf("%s timeout = %d, want 3", ev, flat[0].Timeout)
		}
		if flat[0].Matcher != "" || len(flat[0].Hooks) != 0 {
			t.Errorf("%s must be a FLAT {type,command,timeout} handler; a nested one fails to parse in agy: %+v", ev, flat[0])
		}
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		var wrapped []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(named[ev], &wrapped); err != nil {
			t.Fatalf("%s: %v", ev, err)
		}
		if len(wrapped) != 1 || wrapped[0].Matcher != "*" || len(wrapped[0].Hooks) != 1 {
			t.Fatalf("%s = %+v, want one matcher \"*\" with one hook", ev, wrapped)
		}
		if wrapped[0].Hooks[0].Command != c.Bin+" hook agy "+ev {
			t.Errorf("%s command = %q", ev, wrapped[0].Hooks[0].Command)
		}
	}
	if _, bad := named["SessionStart"]; bad {
		t.Error("agy has no SessionStart hook (§11.1)")
	}
	if strings.Contains(string(install.AgyHooks(c)), "PLUGIN_ROOT") {
		t.Error("PLUGIN_ROOT must never appear in a generated command (§21.4)")
	}
}

func TestWriteAgyRegistersTheMCPServerAndKeepsOtherHookNames(t *testing.T) {
	c := fakeHome(t)
	p := c.Gemini("config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// The file merges with plugin hooks.json files, so another name must survive.
	if err := os.WriteFile(p, []byte(`{"ponytail":{"Stop":[{"type":"command","command":"/usr/bin/true"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	add := c.Bin + " mcp"
	f := &execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + add: {Out: ""},
	}}
	if _, err := install.WriteAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), "ponytail") {
		t.Errorf("another plugin's hook name was dropped:\n%s", body)
	}
	if !strings.Contains(string(body), c.Bin+" hook agy Stop") {
		t.Errorf("swarm's Stop hook was not added:\n%s", body)
	}
	var sawAdd bool
	for _, call := range f.Calls() {
		if strings.HasPrefix(call, "agy mcp add ") {
			sawAdd = true
			if call != "agy mcp add --type stdio swarm "+add {
				t.Errorf("argv = %q; §11.1 gives `agy mcp add --type stdio swarm $BIN mcp`", call)
			}
		}
	}
	if !sawAdd {
		t.Errorf("agy mcp add was not run; calls = %v", f.Calls())
	}
	again, err := install.WriteAgy(context.Background(), c, f.Runner())
	if err != nil || len(again) != 0 {
		t.Fatalf("second WriteAgy changed %v, %v; must be a no-op", again, err)
	}
}

// §20 step 8: only the marked span goes. The operator's own heading stays.
func TestRemoveLegacyAgyTakesOnlyTheMarkedGeminiSpan(t *testing.T) {
	c := fakeHome(t)
	seedFile(t, filepath.Join("agy", "GEMINI-v1.md"), c.Gemini("GEMINI.md"))
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(c.Gemini("GEMINI.md"))
	s := string(body)
	if !strings.Contains(s, "### Agent Swarm Coordination and Task Hygiene") {
		t.Error("the operator's own heading was removed; it is not v1-installed")
	}
	for _, keep := range []string{"# Fake operator instructions", "## Something else", "## Tail section", "`swarm_handoff`"} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %q:\n%s", keep, s)
		}
	}
	for _, gone := range []string{"swarm:start", "swarm:end", "board at http://127.0.0.1:7777"} {
		if strings.Contains(s, gone) {
			t.Errorf("kept %q:\n%s", gone, s)
		}
	}
}

// Verified 2026-09-18: on this machine the v1 agy plugin is a SYMLINK.
func TestRemoveLegacyAgyRemovesTheV1PluginSymlinkWithoutCallingAgy(t *testing.T) {
	c := fakeHome(t)
	target := filepath.Join(c.Home, "app", "current", "plugin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "plugin.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := c.Gemini("config", "plugins", "swarm")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("the symlink was not removed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("the symlink target was followed and deleted (§20: removed as links, never followed)")
	}
	for _, call := range f.Calls() {
		if strings.Contains(call, "plugin uninstall") {
			t.Errorf("agy plugin uninstall must not run for a symlink; calls = %v", f.Calls())
		}
	}
}

func TestRemoveLegacyAgyUsesAgyThenFallsBackForARealPluginFolder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		uninstall execx.Result
		wantGone  bool
	}{
		{"agy uninstall succeeds", execx.Result{Out: "removed"}, true},
		{"agy uninstall fails, folder removed directly", execx.Result{Err: os.ErrPermission}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeHome(t)
			dir := c.Gemini("config", "plugins", "swarm")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			f := &execx.Fake{Responses: map[string]execx.Result{"agy plugin uninstall swarm": tc.uninstall}}
			if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(dir)
			if tc.wantGone && !os.IsNotExist(err) {
				t.Errorf("the plugin folder survived: %v", err)
			}
			var called bool
			for _, call := range f.Calls() {
				called = called || call == "agy plugin uninstall swarm"
			}
			if !called {
				t.Errorf("agy plugin uninstall was not tried; calls = %v", f.Calls())
			}
		})
	}
}

func TestRemoveLegacyAgyDropsTheMCPConfigEntryAndKeepsOthers(t *testing.T) {
	c := fakeHome(t)
	p := c.Gemini("antigravity", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"mcpServers":{"swarm":{"command":"node","args":["/x/swarm-mcp.mjs"]},"other":{"command":"y"}}}`
	if err := os.WriteFile(p, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if _, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	var m struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	body, _ := os.ReadFile(p)
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if _, bad := m.MCPServers["swarm"]; bad {
		t.Error("the v1 swarm entry survived")
	}
	if _, ok := m.MCPServers["other"]; !ok {
		t.Error("another MCP server was dropped")
	}
}

func TestRemoveLegacyAgyOnACleanHomeIsANoOp(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	changed, err := install.RemoveLegacyAgy(context.Background(), c, f.Runner())
	if err != nil || len(changed) != 0 {
		t.Fatalf("= %v, %v", changed, err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("nothing should be executed on a clean home; calls = %v", f.Calls())
	}
	if _, err := os.Stat(c.Gemini("GEMINI.md")); !os.IsNotExist(err) {
		t.Error("removal created GEMINI.md")
	}
}

func TestCheckAgyFailsOnANestedPreInvocationAndOnAMissingBinary(t *testing.T) {
	c := fakeHome(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	p := c.Gemini("config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// §21.4 row 1: a nested PreInvocation must fail the check.
	nested := `{"swarm":{"PreInvocation":[{"matcher":"*","hooks":[{"type":"command","command":"` + c.Bin + ` hook agy PreInvocation","timeout":3}]}]}}`
	if err := os.WriteFile(p, []byte(nested), 0o644); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); ch.OK {
		t.Error("a nested PreInvocation must fail (§21.4 row 1)")
	}
	// PLUGIN_ROOT must fail too.
	if err := os.WriteFile(p, []byte(`{"swarm":{"PreInvocation":[{"type":"command","command":"node ${PLUGIN_ROOT}/x.mjs"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); ch.OK {
		t.Error("${PLUGIN_ROOT} must fail (§21.4 row 1)")
	}
	// The correct file passes, and then fails once the binary is gone.
	if _, err := install.WriteAgy(context.Background(), c, (&execx.Fake{Responses: map[string]execx.Result{
		"agy mcp add --type stdio swarm " + c.Bin + " mcp": {},
	}}).Runner()); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); !ch.OK {
		t.Errorf("agy hooks = %+v after WriteAgy", ch)
	}
	if err := os.Remove(c.Bin); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckAgy(context.Background(), c, f.Runner()), "agy hooks"); ch.OK {
		t.Error("a missing hook binary must fail (§21.4 row 1)")
	}
}

// findCheck is shared by the four agent test files.
func findCheck(t *testing.T, cs []install.Check, name string) install.Check {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, cs)
	return install.Check{}
}

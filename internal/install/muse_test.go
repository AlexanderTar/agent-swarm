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

// §11.1: the swarm MCP server merges into muse's settings.json, keeping every
// other server and every other key byte-for-byte identical in meaning.
func TestWriteMuseMergesSwarmMCPServerAndKeepsOthers(t *testing.T) {
	c := fakeHome(t)
	p := c.Muse("settings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"provider":"meta","model":"muse-spark-1.3","mcpServers":{` +
		`"notion":{"mode":"optional","url":"https://mcp.notion.com/mcp"}}}`
	if err := os.WriteFile(p, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	f := fakeMusePluginRunner(c)
	changed, err := install.WriteMuse(context.Background(), c, f.Runner())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) == 0 {
		t.Fatal("first install must report changes")
	}
	raw, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	servers, _ := m["mcpServers"].(map[string]any)
	swarm, _ := servers["swarm"].(map[string]any)
	if swarm["command"] != c.Bin {
		t.Errorf("swarm.command = %v, want %q", swarm["command"], c.Bin)
	}
	if args, _ := swarm["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Errorf("swarm.args = %v, want [mcp]", args)
	}
	if _, ok := servers["notion"]; !ok {
		t.Errorf("unrelated server was dropped:\n%s", raw)
	}
	if m["provider"] != "meta" || m["model"] != "muse-spark-1.3" {
		t.Errorf("unrelated keys changed:\n%s", raw)
	}
	// Idempotent: a second run changes nothing (settings.json and the
	// manifest are unchanged; the plugin install/approve calls still run,
	// same as a real re-install, matching the probed-idempotent behavior
	// documented on WriteMuse).
	again, err := install.WriteMuse(context.Background(), c, f.Runner())
	if err != nil || len(again) != 0 {
		t.Errorf("second run changed %v, err %v", again, err)
	}
	// Skills land in the probed user-skills dir.
	for _, name := range []string{"swarm", "swarm-orchestrator"} {
		if _, err := os.Stat(filepath.Join(c.SkillsDir(install.KindMuse), name, "SKILL.md")); err != nil {
			t.Errorf("missing skill %s: %v", name, err)
		}
	}
}

func TestCheckMuseFailsBeforeInstallAndOnAMissingBinary(t *testing.T) {
	c := fakeHome(t)
	f := fakeMusePluginRunner(c)
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse MCP"); ch.OK {
		t.Error("muse MCP must fail before install")
	}
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse hooks"); ch.OK {
		t.Error("muse hooks must fail before install")
	}
	if _, err := install.WriteMuse(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	// WriteMuse's plugin install/approve calls are faked (no real muse CLI
	// in tests), so the store's installed.json museHooksCheck reads is
	// simulated the same way a real `muse plugins install` would leave it.
	writeFakeMuseInstalledJSON(t, c)
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse MCP"); !ch.OK {
		t.Errorf("muse MCP = %+v after WriteMuse", ch)
	}
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse hooks"); !ch.OK {
		t.Errorf("muse hooks = %+v after WriteMuse", ch)
	}
	if err := os.Remove(c.Bin); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse MCP"); ch.OK {
		t.Error("a missing hook binary must fail")
	}
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse hooks"); ch.OK {
		t.Error("a missing hook binary must fail muse hooks too")
	}
}

// TestWriteMusePluginManifestRegistersEveryHookEvent is Task 7: the plugin
// manifest WriteMuse writes to musePluginDir registers PreToolUse,
// PostToolUse and UserPromptSubmit, each with the structured argv
// [Config.Bin, "hook", "muse", "<Event>"] -- the same "swarm hook <agent>
// <event>" entry point claude's settingsJSON hooks use.
func TestWriteMusePluginManifestRegistersEveryHookEvent(t *testing.T) {
	c := fakeHome(t)
	f := fakeMusePluginRunner(c)
	if _, err := install.WriteMuse(context.Background(), c, f.Runner()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.Muse("plugin"), ".muse-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Capabilities struct {
			Hooks []struct {
				Event   string   `json:"event"`
				Command []string `json:"command"`
			} `json:"hooks"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"PreToolUse": true, "PostToolUse": true, "UserPromptSubmit": true}
	if len(manifest.Capabilities.Hooks) != len(want) {
		t.Fatalf("hooks = %+v, want %d events", manifest.Capabilities.Hooks, len(want))
	}
	for _, h := range manifest.Capabilities.Hooks {
		if !want[h.Event] {
			t.Errorf("unexpected event %q", h.Event)
		}
		wantArgv := []string{c.Bin, "hook", "muse", h.Event}
		if len(h.Command) != len(wantArgv) {
			t.Fatalf("command = %v, want %v", h.Command, wantArgv)
		}
		for i, a := range wantArgv {
			if h.Command[i] != a {
				t.Errorf("command[%d] = %q, want %q", i, h.Command[i], a)
			}
		}
	}
	// Both plugin CLI steps ran.
	calls := f.Calls()
	sawInstall, sawApprove := 0, 0
	for _, call := range calls {
		if strings.Contains(call, "plugins install") {
			sawInstall++
		}
		if strings.Contains(call, "plugins approve") {
			sawApprove++
		}
	}
	if sawInstall == 0 {
		t.Error("WriteMuse never ran `muse plugins install`")
	}
	if sawApprove != len(want) {
		t.Errorf("approve calls = %d, want %d (one per hook event)", sawApprove, len(want))
	}
}

// writeFakeMuseInstalledJSON simulates the shared plugin store's
// installed.json after a real `muse plugins install` -- see the probed
// shape in internal/install/muse.go's museHooksCheck doc comment.
func writeFakeMuseInstalledJSON(t *testing.T, c install.Config) {
	t.Helper()
	p := c.MuseData("plugins", "installed.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"plugins":{"swarm":{"id":"swarm","enabled":true,` +
		`"source":{"provenance":"native-local","path":"` + c.Muse("plugin") + `"}}}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeMusePluginRunner answers every `muse plugins install`/`approve`
// invocation WriteMuse can make against fakeHome(t)'s c, keyed the way
// execx.Fake keys them (argv joined by spaces) -- the plugin dir path is
// deterministic under c.Muse("plugin").
func fakeMusePluginRunner(c install.Config) *execx.Fake {
	dir := c.Muse("plugin")
	resp := map[string]execx.Result{
		"muse plugins install " + dir + " --scope user --json": {Out: `{"installed":{"id":"swarm"}}`},
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit"} {
		resp["muse plugins approve plugin:swarm:hook:"+ev+" --json"] = execx.Result{Out: `{"decision":"approve"}`}
	}
	return &execx.Fake{Responses: resp}
}

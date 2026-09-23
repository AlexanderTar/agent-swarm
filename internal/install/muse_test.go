package install_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	changed, err := install.WriteMuse(context.Background(), c, nil)
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
	// Idempotent: a second run changes nothing.
	again, err := install.WriteMuse(context.Background(), c, nil)
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
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse MCP"); ch.OK {
		t.Error("muse MCP must fail before install")
	}
	if _, err := install.WriteMuse(context.Background(), c, nil); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse MCP"); !ch.OK {
		t.Errorf("muse MCP = %+v after WriteMuse", ch)
	}
	if err := os.Remove(c.Bin); err != nil {
		t.Fatal(err)
	}
	if ch := findCheck(t, install.CheckMuse(context.Background(), c, f.Runner()), "muse MCP"); ch.OK {
		t.Error("a missing hook binary must fail")
	}
}

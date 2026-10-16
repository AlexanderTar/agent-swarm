package install_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
	"github.com/pelletier/go-toml/v2"
)

// grokFake answers every command WriteGrok may run with success, so unit tests
// that are not about the plugin install never trip the fake.
func grokFake() *execx.Fake {
	return &execx.Fake{Responses: map[string]execx.Result{}}
}

func readTOML(t *testing.T, p string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := toml.Unmarshal(body, &m); err != nil {
		t.Fatalf("%s does not parse: %v\n%s", p, err, body)
	}
	return m
}

func seedGrokConfig(t *testing.T, c install.Config, body string) {
	t.Helper()
	if err := os.MkdirAll(c.Grok(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Grok("config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

var grokHookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"Stop", "StopFailure", "StopCancelled", "PreCompact", "PostCompact", "Notification"}

func TestWriteGrokMergesConfig(t *testing.T) {
	c := fakeHome(t)
	seedGrokConfig(t, c, `[marketplace]
x = 1

[mcp_servers.other]
command = "other"

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "/usr/local/bin/user-stop"

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "/old/swarm hook grok Stop"

[skills]
paths = ["/user/skills"]
`)
	ctx := context.Background()
	changed, _, err := install.WriteGrok(ctx, c, grokFake().Runner())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) == 0 {
		t.Fatal("first install must report changes")
	}
	first, _ := os.ReadFile(c.Grok("config.toml"))
	m := readTOML(t, c.Grok("config.toml"))

	if mk, _ := m["marketplace"].(map[string]any); mk["x"] != int64(1) {
		t.Errorf("[marketplace] lost: %v", m["marketplace"])
	}
	servers, _ := m["mcp_servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Errorf("user's mcp server dropped: %v", servers)
	}
	sw, _ := servers["swarm"].(map[string]any)
	if sw["command"] != c.Bin {
		t.Errorf("swarm command = %v, want %s", sw["command"], c.Bin)
	}
	if args, _ := sw["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Errorf("swarm args = %v", sw["args"])
	}
	env, _ := sw["env"].(map[string]any)
	for k, v := range map[string]string{
		"SWARM_URL": "${SWARM_URL}", "SWARM_SESSION": "${SWARM_SESSION}",
		"SWARM_TOKEN_FILE": "${SWARM_TOKEN_FILE}", "SWARM_AGENT_KIND": "grok",
	} {
		if env[k] != v {
			t.Errorf("swarm env %s = %v, want %q", k, env[k], v)
		}
	}

	hooks, _ := m["hooks"].(map[string]any)
	for _, ev := range grokHookEvents {
		var cmds []string
		groups, _ := hooks[ev].([]any)
		for _, g := range groups {
			inner, _ := g.(map[string]any)["hooks"].([]any)
			for _, h := range inner {
				cmds = append(cmds, h.(map[string]any)["command"].(string))
			}
		}
		want := c.Bin + " hook grok " + ev
		n := 0
		for _, cmd := range cmds {
			if cmd == want {
				n++
			}
			if strings.Contains(cmd, "/old/swarm") {
				t.Errorf("%s: stale swarm hook kept: %v", ev, cmds)
			}
		}
		if n != 1 {
			t.Errorf("%s: want exactly one %q, got %v", ev, want, cmds)
		}
	}
	var stop []string
	for _, g := range hooks["Stop"].([]any) {
		for _, h := range g.(map[string]any)["hooks"].([]any) {
			stop = append(stop, h.(map[string]any)["command"].(string))
		}
	}
	if stop[0] != "/usr/local/bin/user-stop" {
		t.Errorf("user's Stop hook not kept first: %v", stop)
	}

	paths, _ := m["skills"].(map[string]any)["paths"].([]any)
	var got []string
	for _, p := range paths {
		got = append(got, p.(string))
	}
	if len(got) != 2 || got[0] != "/user/skills" || got[1] != c.SkillsDir(install.KindGrok) {
		t.Errorf("[skills] paths = %v, want user's plus %s", got, c.SkillsDir(install.KindGrok))
	}

	changed2, _, err := install.WriteGrok(ctx, c, grokFake().Runner())
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(c.Grok("config.toml"))
	if !bytes.Equal(first, second) {
		t.Errorf("second run changed config.toml:\n%s\n---\n%s", first, second)
	}
	for _, p := range changed2 {
		if p == c.Grok("config.toml") {
			t.Errorf("second run reported config.toml as changed")
		}
	}
}

func TestWriteGrokLinksAuth(t *testing.T) {
	c := fakeHome(t)
	userGrok := filepath.Join(c.UserHome, ".grok")
	if err := os.MkdirAll(userGrok, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userGrok, "auth.json"), []byte(`{"k":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := install.WriteGrok(context.Background(), c, grokFake().Runner()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "auth.json.lock"} {
		got, err := os.Readlink(c.Grok(name))
		if err != nil || got != filepath.Join(userGrok, name) {
			t.Errorf("%s -> %q (%v), want a symlink to %s", name, got, err, filepath.Join(userGrok, name))
		}
	}
	// A missing lock is seeded, so the link never dangles.
	if _, err := os.Stat(filepath.Join(userGrok, "auth.json.lock")); err != nil {
		t.Errorf("lock not seeded: %v", err)
	}
}

func TestWriteGrokLeavesARealAuthFileAloneAndReportsIt(t *testing.T) {
	c := fakeHome(t)
	if err := os.MkdirAll(c.Grok(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Grok("auth.json"), []byte(`{"own":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, skipped, err := install.WriteGrok(context.Background(), c, grokFake().Runner())
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(c.Grok("auth.json")); string(body) != `{"own":true}` {
		t.Errorf("real auth.json was rewritten: %s", body)
	}
	if fi, _ := os.Lstat(c.Grok("auth.json")); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("real auth.json was replaced by a symlink")
	}
	found := false
	for _, s := range skipped {
		found = found || strings.Contains(s, c.Grok("auth.json"))
	}
	if !found {
		t.Errorf("skipped = %v, want it to name %s", skipped, c.Grok("auth.json"))
	}
	if _, err := os.Readlink(c.Grok("auth.json.lock")); err != nil {
		t.Errorf("the lock link is independent and must still be made: %v", err)
	}
}

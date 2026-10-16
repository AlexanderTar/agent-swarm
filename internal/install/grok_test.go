package install_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
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

func seedClaudePlugins(t *testing.T, c install.Config, installed []string, cached ...string) {
	t.Helper()
	plugins := map[string]string{}
	for _, k := range installed {
		plugins[`"`+k+`"`] = `[{"scope":"user","installPath":"/x"}]`
	}
	var entries []string
	for k, v := range plugins {
		entries = append(entries, k+":"+v)
	}
	dir := c.Claude("plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":2,"plugins":{` + strings.Join(entries, ",") + `}}`
	if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range cached {
		if err := os.MkdirAll(filepath.Join(dir, "cache", "mkt", name, "1.0.0"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func grokDisabled(t *testing.T, c install.Config) []string {
	t.Helper()
	pl, _ := readTOML(t, c.Grok("config.toml"))["plugins"].(map[string]any)
	var out []string
	for _, v := range pl["disabled"].([]any) {
		out = append(out, v.(string))
	}
	return out
}

func TestGrokPluginsDisabledCoversClaudePlugins(t *testing.T) {
	c := fakeHome(t)
	seedClaudePlugins(t, c, []string{"alpha@mkt", "beta@other", "superpowers@mkt"}, "gamma")
	seedGrokConfig(t, c, "[plugins]\ndisabled = [\"mine\", \"superpowers\"]\nenabled = [\"superpowers\"]\n")

	changed, err := install.RefreshGrokPlugins(c)
	if err != nil || !changed {
		t.Fatalf("RefreshGrokPlugins = %v, %v; want changed", changed, err)
	}
	got := grokDisabled(t, c)
	want := []string{"alpha", "beta", "gamma", "mine"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("disabled = %v, want %v (superpowers must never be disabled)", got, want)
	}
	pl := readTOML(t, c.Grok("config.toml"))["plugins"].(map[string]any)
	if en, _ := pl["enabled"].([]any); len(en) != 1 || en[0] != "superpowers" {
		t.Errorf("enabled = %v, want it untouched", pl["enabled"])
	}

	// A plugin installed in Claude later is disabled on the next refresh.
	seedClaudePlugins(t, c, []string{"alpha@mkt", "beta@other", "delta@mkt", "superpowers@mkt"}, "gamma")
	if changed, err := install.RefreshGrokPlugins(c); err != nil || !changed {
		t.Fatalf("second refresh = %v, %v; want changed", changed, err)
	}
	if got := grokDisabled(t, c); strings.Join(got, ",") != "alpha,beta,delta,gamma,mine" {
		t.Errorf("disabled after a new plugin = %v", got)
	}
	if changed, _ := install.RefreshGrokPlugins(c); changed {
		t.Error("a refresh with nothing new must not rewrite config.toml")
	}
}

func TestGrokPluginsRefreshWithoutClaudeStillNeverDisablesSuperpowers(t *testing.T) {
	c := fakeHome(t)
	if _, err := install.RefreshGrokPlugins(c); err != nil {
		t.Fatal(err)
	}
	for _, n := range grokDisabled(t, c) {
		if n == "superpowers" {
			t.Fatal("superpowers disabled")
		}
	}
}

// grokInstallKey is the exact argv the fake runner sees: the native install,
// run through env so GROK_HOME and the compat switches reach grok.
func grokInstallKey(c install.Config) string {
	return "env " + strings.Join(grokhome.EnvArgs(c.Home), " ") +
		" grok plugin install https://github.com/obra/superpowers.git --trust"
}

func TestGrokSuperpowersInstall(t *testing.T) {
	c := fakeHome(t)
	key := grokInstallKey(c)
	if !strings.Contains(key, "GROK_HOME="+c.Grok()+" ") && !strings.Contains(key, "GROK_HOME="+c.Grok()) {
		t.Fatalf("install key lacks GROK_HOME: %s", key)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{key: {Out: "Installed 1 plugin(s)"}}}
	// Real grok rewrites config.toml during the install; Swarm must merge after it.
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := f.Runner()(ctx, name, args...)
		if err == nil {
			seedGrokConfig(t, c, "[plugins]\nenabled = [\"superpowers\"]\n")
			if err := os.MkdirAll(c.Grok("installed-plugins", "superpowers-21e2a56d", "skills", "brainstorming"), 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(c.Grok("installed-plugins", "superpowers-21e2a56d", "skills", "brainstorming", "SKILL.md"), []byte("x"), 0o644)
		}
		return out, err
	}
	if _, _, err := install.WriteGrok(context.Background(), c, run); err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != key {
		t.Fatalf("calls = %v, want exactly %q", calls, key)
	}
	m := readTOML(t, c.Grok("config.toml"))
	if en, _ := m["plugins"].(map[string]any)["enabled"].([]any); len(en) != 1 || en[0] != "superpowers" {
		t.Errorf("grok's [plugins] enabled was lost: %v", m["plugins"])
	}
	if _, ok := m["mcp_servers"].(map[string]any)["swarm"]; !ok {
		t.Error("swarm MCP not merged after the install rewrote config.toml")
	}
	if ok, detail := install.SuperpowersOK(c, install.KindGrok); !ok {
		t.Errorf("SuperpowersOK(grok) = false: %s", detail)
	}

	// Installed already: a second write runs nothing.
	f2 := &execx.Fake{Responses: map[string]execx.Result{}}
	if _, _, err := install.WriteGrok(context.Background(), c, f2.Runner()); err != nil {
		t.Fatal(err)
	}
	if calls := f2.Calls(); len(calls) != 0 {
		t.Errorf("second write ran %v", calls)
	}
}

func TestGrokSuperpowersFallsBackToTheNewestClaudeCopy(t *testing.T) {
	c := fakeHome(t)
	old := c.Claude("plugins", "cache", "m", "superpowers", "5.0.1", "skills")
	newest := c.Claude("plugins", "cache", "m", "superpowers", "5.1.0", "skills")
	for _, d := range []string{old, newest} {
		if err := os.MkdirAll(filepath.Join(d, "brainstorming"), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "brainstorming", "SKILL.md"), []byte("x"), 0o644)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Dir(old), past, past); err != nil {
		t.Fatal(err)
	}
	_, skipped, err := install.WriteGrok(context.Background(), c, grokFake().Runner()) // install fails
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range readTOML(t, c.Grok("config.toml"))["skills"].(map[string]any)["paths"].([]any) {
		paths = append(paths, p.(string))
	}
	if !slices.Contains(paths, newest) || slices.Contains(paths, old) {
		t.Errorf("[skills] paths = %v, want the newest superpowers copy %s only", paths, newest)
	}
	if !strings.Contains(strings.Join(skipped, "\n"), "superpowers") {
		t.Errorf("a failed native install must be reported: %v", skipped)
	}
	if ok, _ := install.SuperpowersOK(c, install.KindGrok); !ok {
		t.Error("the Claude copy should satisfy SuperpowersOK")
	}
}

// The marketplace sync has no grok step: WriteGrok owns grok's superpowers, so
// Sync neither runs commands nor prints a skip line per plugin for it.
func TestPluginsSyncLeavesGrokToWriteGrok(t *testing.T) {
	c := fakeHome(t)
	srv := marketplaceServer(t)
	f := &execx.Fake{Responses: map[string]execx.Result{}}
	p := install.Plugins{Cfg: c, Run: f.Runner(), HTTP: srv.Client(), MarketplaceURL: srv.URL, Log: &bytes.Buffer{}}
	if res := p.Sync(context.Background(), []install.Kind{install.KindGrok}); len(res) != 0 {
		t.Errorf("Sync for grok = %+v, want nothing", res)
	}
	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("Sync for grok ran %v", calls)
	}
}

package adapter

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func grokSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{AgentName: "grok-coder", AgentID: "ag_01", SessionID: "ses_01", Token: "tok",
		TokenFile: "/swarm-home/run/tokens/ses_01", DaemonURL: "http://127.0.0.1:17778",
		Model: "grok-4.7", Effort: "high", Cwd: t.TempDir(),
		Kickoff: "You are swarm agent grok-coder (coder) for TASK-101: x.", Bin: "/usr/local/bin/swarm"}
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestGrokLaunchArgv(t *testing.T) {
	d := testDeps(t)
	a := newGrok(d)
	s := grokSpec(t)
	l, err := a.Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind() != kinds.Grok {
		t.Errorf("Kind() = %s", a.Kind())
	}
	if len(l.Argv) != 11 || !uuidV4.MatchString(l.Argv[9]) {
		t.Fatalf("argv = %q", l.Argv)
	}
	want := []string{"grok", "--model", "grok-4.7", "--reasoning-effort", "high",
		"--always-approve", "--trust", "--no-subagents", "--session-id", l.Argv[9], s.Kickoff}
	if got := append(slices.Clone(l.Argv[:10]), l.Argv[10]); !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", l.Argv, want)
	}
	for _, bad := range []string{"--disallowed-tools", "--debug", "--debug-file", "--fork-session", "--leader", "--rules"} {
		if slices.Contains(l.Argv, bad) {
			t.Errorf("argv must not carry %s: %q", bad, l.Argv)
		}
	}
	for k, v := range grokhome.Env(d.Home) {
		if l.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, l.Env[k], v)
		}
	}
	for k, v := range map[string]string{"SWARM_URL": s.DaemonURL, "SWARM_SESSION": s.SessionID,
		"SWARM_TOKEN_FILE": s.TokenFile, "SWARM_AGENT_KIND": "grok"} {
		if l.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, l.Env[k], v)
		}
	}
	if _, ok := l.Env["GROK_AUTO_COMPACT_THRESHOLD_PERCENT"]; ok {
		t.Error("no compaction threshold without a low-token cap")
	}
	if strings.Contains(strings.Join(l.Argv, " "), s.Token) {
		t.Error("no session secret belongs in argv")
	}
}

func TestGrokLaunchRulesAndLowTokenCap(t *testing.T) {
	a := newGrok(testDeps(t))
	s := grokSpec(t)
	s.Instructions = "Be terse.\nNo emoji."
	s.LowTokenCap = 153600 // 60% of grok's 256k window
	s.Effort = ""
	l, err := a.Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.Argv, "--reasoning-effort") {
		t.Errorf("an unset effort keeps grok's own default: %q", l.Argv)
	}
	i := slices.Index(l.Argv, "--rules")
	if i < 0 || l.Argv[i+1] != s.Instructions || l.Argv[len(l.Argv)-1] != s.Kickoff || i+2 != len(l.Argv)-1 {
		t.Errorf("--rules <text> comes right before the kickoff: %q", l.Argv)
	}
	if got := l.Env["GROK_AUTO_COMPACT_THRESHOLD_PERCENT"]; got != "60" {
		t.Errorf("GROK_AUTO_COMPACT_THRESHOLD_PERCENT = %q, want 60", got)
	}
}

// TestGrokResumeKeepsKickoff: resume accepts the positional prompt and submits
// it (probed 2026-10-16), so unlike muse the kickoff stays.
func TestGrokResumeKeepsKickoff(t *testing.T) {
	a := newGrok(testDeps(t))
	s := grokSpec(t)
	s.ProviderSessionID = "f1b864d7-1f9e-4ca4-9309-d32b9bc755f7"
	s.Instructions = "Be terse."
	l, err := a.Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grok", "--model", "grok-4.7", "--reasoning-effort", "high",
		"--always-approve", "--trust", "--no-subagents", "--resume", s.ProviderSessionID,
		"--rules", "Be terse.", s.Kickoff}
	if !slices.Equal(l.Argv, want) {
		t.Fatalf("argv = %q, want %q", l.Argv, want)
	}
	for _, bad := range []string{"--session-id", "--fork-session", "--disallowed-tools"} {
		if slices.Contains(l.Argv, bad) {
			t.Errorf("resume must not carry %s", bad)
		}
	}
	if l.Env["GROK_HOME"] != grokhome.Dir(a.d.Home) {
		t.Errorf("GROK_HOME = %q", l.Env["GROK_HOME"])
	}
	s.ProviderSessionID = ""
	if _, err := a.Resume(s); err == nil {
		t.Error("resume without a provider session id must fail")
	}
}

func TestGrokAuthOK(t *testing.T) {
	const secretKey, secretRefresh = "xai-SECRET-KEY-0123", "rt-SECRET-REFRESH-4567"
	write := func(t *testing.T, d Deps, body string) {
		t.Helper()
		p := filepath.Join(grokhome.Dir(d.Home), "auth.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, body string
		env        string
		ok         bool
	}{
		{"key and refresh token", `{"https://auth.x.ai::u":{"key":"` + secretKey + `","refresh_token":"` + secretRefresh + `"}}`, "", true},
		{"second entry is enough", `{"a":{"key":""},"b":{"key":"k","refresh_token":"r"}}`, "", true},
		{"no file", "", "", false},
		{"empty fields", `{"a":{"key":"","refresh_token":""}}`, "", false},
		{"key without refresh token", `{"a":{"key":"` + secretKey + `","refresh_token":""}}`, "", false},
		{"refresh token without key", `{"a":{"refresh_token":"` + secretRefresh + `"}}`, "", false},
		{"not json", `nope ` + secretKey, "", false},
		{"empty object", `{}`, "", false},
		{"api key env", "", "xai-from-env", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XAI_API_KEY", tc.env)
			d := testDeps(t)
			if tc.body != "" {
				write(t, d, tc.body)
			}
			err := newGrok(d).AuthOK(context.Background())
			if (err == nil) != tc.ok {
				t.Fatalf("AuthOK = %v, want ok=%v", err, tc.ok)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "grok login") {
					t.Errorf("error should name the fix: %v", err)
				}
				for _, secret := range []string{secretKey, secretRefresh} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("error leaks a credential: %v", err)
					}
				}
			}
		})
	}
}

func TestGrokInstalledRunsWithGrokHome(t *testing.T) {
	d := testDeps(t)
	var gotEnv map[string]string
	var gotCmd []string
	d.RunEnv = func(_ context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
		gotEnv, gotCmd = env, append([]string{name}, args...)
		return []byte("grok 1.0.60 (01a8e1c591c0) [stable]\n"), nil
	}
	v, ok := newGrok(d).Installed(context.Background())
	if !ok || v != "1.0.60" {
		t.Fatalf("Installed = %q, %v", v, ok)
	}
	if !slices.Equal(gotCmd, []string{"grok", "--version"}) || gotEnv["GROK_HOME"] != grokhome.Dir(d.Home) {
		t.Errorf("ran %v with env %v", gotCmd, gotEnv)
	}
}

// Plugins installed in Claude after `swarm install` are disabled in the Swarm
// grok home at the next launch, so grok's Claude compat never loads them.
func TestGrokLaunchRefreshesDisabledPlugins(t *testing.T) {
	d := testDeps(t)
	a := newGrok(d)
	plugins := filepath.Join(d.UserHome, ".claude", "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	install := func(names ...string) {
		t.Helper()
		var sb strings.Builder
		sb.WriteString(`{"version":2,"plugins":{`)
		for i, n := range names {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(`"` + n + `@market":[{"scope":"user"}]`)
		}
		sb.WriteString(`}}`)
		if err := os.WriteFile(filepath.Join(plugins, "installed_plugins.json"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	disabled := func() []string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(grokhome.Dir(d.Home), "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Plugins struct {
				Disabled []string `toml:"disabled"`
			} `toml:"plugins"`
		}
		if err := toml.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.Plugins.Disabled
	}
	install("frontend-design")
	if _, err := a.Launch(grokSpec(t)); err != nil {
		t.Fatal(err)
	}
	if got := disabled(); !slices.Equal(got, []string{"frontend-design"}) {
		t.Fatalf("disabled = %v", got)
	}
	install("frontend-design", "superpowers", "late-plugin")
	s := grokSpec(t)
	s.ProviderSessionID = "11111111-2222-4333-8444-555555555555"
	if _, err := a.Resume(s); err != nil {
		t.Fatal(err)
	}
	if got := disabled(); !slices.Equal(got, []string{"frontend-design", "late-plugin"}) {
		t.Fatalf("disabled = %v, want the late plugin added and superpowers never disabled", got)
	}
}

func TestGrokSuperpowersInstalledDelegates(t *testing.T) {
	d := testDeps(t)
	a := newGrok(d)
	if a.SuperpowersInstalled() {
		t.Fatal("true before any install")
	}
	skill := filepath.Join(grokhome.Dir(d.Home), "installed-plugins", "superpowers-21e2a56d", "skills", "brainstorming", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("# b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !a.SuperpowersInstalled() {
		t.Fatal("false with the native install present")
	}
}

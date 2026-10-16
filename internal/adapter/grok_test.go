package adapter

import (
	"context"
	"net/url"
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
	if len(l.Argv) != 14 || !uuidV4.MatchString(l.Argv[9]) {
		t.Fatalf("argv = %q", l.Argv)
	}
	sock := filepath.Join(d.Home, "run", "grok-leader-ses_01.sock")
	want := []string{"grok", "--model", "grok-4.7", "--reasoning-effort", "high",
		"--always-approve", "--trust", "--no-subagents", "--session-id", l.Argv[9],
		"--leader", "--leader-socket", sock, s.Kickoff}
	if !slices.Equal(l.Argv, want) {
		t.Fatalf("argv = %q, want %q", l.Argv, want)
	}
	for _, bad := range []string{"--disallowed-tools", "--debug", "--debug-file", "--fork-session", "--rules"} {
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
		"--leader", "--leader-socket", filepath.Join(a.d.Home, "run", "grok-leader-ses_01.sock"),
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

func grokPane(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "grok", "grok-tui-"+name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The expected matrix is TASK-1126 §1: grok draws its input box in every state,
// so idleness is read from the footer, not from the prompt glyph.
func TestGrokPaneRules(t *testing.T) {
	a := newGrok(testDeps(t))
	for _, tc := range []struct {
		fixture                 string
		idle, busy, card, limit bool
	}{
		{"idle", true, false, false, false},
		{"after-ctrlc", true, false, false, false},
		{"after-queue", true, false, false, false},
		{"after-rename", true, false, false, false},
		{"multiline-pasted", false, false, false, false}, // a draft in the box
		{"startup-0.5", false, false, false, false},
		{"startup-1.5", false, true, false, false},
		{"startup-3", false, true, false, false},
		{"busy-tool", false, true, false, false},
		{"busy-sendnow", false, true, false, false},
		{"busy-paste-queued", false, true, false, false},
		{"usage-modal", false, false, true, false},
		{"free-usage-limit-card", false, false, true, true},
		{"resume-with-prompt", false, false, true, true},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			raw := grokPane(t, tc.fixture)
			plain := StripANSI(raw)
			if got := a.Idle(raw); got != tc.idle {
				t.Errorf("Idle(raw) = %v, want %v", got, tc.idle)
			}
			if got := a.Idle(plain); got != tc.idle {
				t.Errorf("Idle(plain) = %v, want %v", got, tc.idle)
			}
			if got := a.Busy().MatchString(plain); got != tc.busy {
				t.Errorf("Busy = %v, want %v", got, tc.busy)
			}
			if got := grokCard.MatchString(plain); got != tc.card {
				t.Errorf("card = %v, want %v", got, tc.card)
			}
			if got := grokUsageLimit.MatchString(plain); got != tc.limit {
				t.Errorf("usage limit = %v, want %v", got, tc.limit)
			}
			// A card needs a person: it shows as a Needs-you row and as a startup stall.
			var prompt, startup bool
			for _, m := range a.PromptPatterns() {
				prompt = prompt || m.Match.MatchString(plain)
			}
			for _, d := range a.StartupDialogs() {
				startup = startup || d.Match.MatchString(plain)
			}
			if prompt != tc.card || startup != tc.card {
				t.Errorf("card dialogs: prompt=%v startup=%v, want %v", prompt, startup, tc.card)
			}
		})
	}
}

func TestGrokProcessNamesAndInterrupt(t *testing.T) {
	a := newGrok(testDeps(t))
	match := func(cmd string) bool {
		for _, re := range a.ProcessNames() {
			if re.MatchString(cmd) {
				return true
			}
		}
		return false
	}
	for cmd, want := range map[string]bool{"grok-1.0.60-mac": true, "grok": true, "grok-1.0.60-macos-aarch64": true,
		"zsh": false, "groker": false, "grok-leader": false} {
		if match(cmd) != want {
			t.Errorf("ProcessNames(%q) = %v, want %v", cmd, !want, want)
		}
	}
	if got := a.InterruptKeys(); !slices.Equal(got, []string{"C-c"}) {
		t.Errorf("InterruptKeys = %v", got)
	}
}

func TestGrokRenameCommand(t *testing.T) {
	var r SessionRenamer = newGrok(testDeps(t))
	if line, ok := r.RenameCommand("login-form-coder"); !ok || line != "/rename login-form-coder" {
		t.Errorf("RenameCommand = %q, %v", line, ok)
	}
}

// Swarm mints grok's session id at launch, so discovery just hands it back.
func TestGrokDiscoverSession(t *testing.T) {
	a := newGrok(testDeps(t))
	s := grokSpec(t)
	l, err := a.Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	minted := l.Argv[slices.Index(l.Argv, "--session-id")+1]
	if id, ok := a.DiscoverSession(context.Background(), 4242, s.Cwd); !ok || id != minted {
		t.Fatalf("DiscoverSession = %q, %v; want %q", id, ok, minted)
	}
	if id, ok := a.DiscoverSession(context.Background(), 4242, "/some/other/cwd"); ok || id != "" {
		t.Errorf("an unlaunched workspace discovered %q", id)
	}
	other := grokSpec(t)
	l2, _ := a.Launch(other)
	if id, _ := a.DiscoverSession(context.Background(), 1, other.Cwd); id != l2.Argv[slices.Index(l2.Argv, "--session-id")+1] || id == minted {
		t.Errorf("two workspaces must keep their own ids, got %q", id)
	}
}

func TestGrokForgetFolder(t *testing.T) {
	d := testDeps(t)
	a := newGrok(d)
	path := filepath.Join(grokhome.Dir(d.Home), "trusted_folders.toml")
	ctx := context.Background()
	if err := a.ForgetFolder(ctx, "/work/a"); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ForgetFolder created %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "[folders.\"/work/a\"]\ntrusted = true\ndecided_at = 1792156471\n\n[folders.\"/work/b\"]\ntrusted = true\ndecided_at = 1792156472\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ForgetFolder(ctx, "/work/a"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Folders map[string]struct {
			Trusted   bool  `toml:"trusted"`
			DecidedAt int64 `toml:"decided_at"`
		} `toml:"folders"`
	}
	raw, _ := os.ReadFile(path)
	if err := toml.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, gone := got.Folders["/work/a"]; gone || len(got.Folders) != 1 || got.Folders["/work/b"].DecidedAt != 1792156472 {
		t.Fatalf("folders after forget = %+v", got.Folders)
	}
	// An unknown path leaves the file byte-identical (no rewrite).
	before, _ := os.ReadFile(path)
	if err := a.ForgetFolder(ctx, "/work/unknown"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("file rewritten for an unknown path:\n%s", after)
	}
	// A file that does not parse is left alone.
	if err := os.WriteFile(path, []byte("[folders\nbroken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ForgetFolder(ctx, "/work/b"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != "[folders\nbroken" {
		t.Errorf("unparseable file was modified: %q", after)
	}
}

// grokSessionDir plants the session fixtures where grok writes them:
// <grok home>/sessions/<url-encoded cwd>/<session id>/.
func grokSessionDir(t *testing.T, d Deps, id string, files ...string) string {
	t.Helper()
	dir := filepath.Join(grokhome.Dir(d.Home), "sessions", url.PathEscape("/work/probe"), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join("testdata", "grok", "session", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Hooks hand over the session's updates.jsonl as the transcript path; the
// context numbers live next to it in signals.json.
func TestGrokReadContext(t *testing.T) {
	d := testDeps(t)
	dir := grokSessionDir(t, d, "12fc7ee2-433b-4f17-b1c7-be043d74ba18", "signals.json")
	for _, path := range []string{filepath.Join(dir, "updates.jsonl"), filepath.Join(dir, "signals.json")} {
		s, ok := ReadContext(kinds.Grok, path)
		if !ok || s.Tokens != 28431 || s.Window == nil || *s.Window != 256000 || s.Model != "grok-4.7" {
			t.Errorf("ReadContext(%s) = %+v, %v", filepath.Base(path), s, ok)
		}
	}
	if _, ok := ReadContext(kinds.Grok, filepath.Join(t.TempDir(), "updates.jsonl")); ok {
		t.Error("a session with no signals.json has no sample")
	}
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), []byte(`{"contextTokensUsed":0,"contextWindowTokens":256000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadContext(kinds.Grok, filepath.Join(dir, "updates.jsonl")); ok {
		t.Error("a fresh session (0 tokens used) has no sample")
	}
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadContext(kinds.Grok, filepath.Join(dir, "updates.jsonl")); ok {
		t.Error("garbage has no sample")
	}
}

func TestGrokObserveModel(t *testing.T) {
	d := testDeps(t)
	const id = "12fc7ee2-433b-4f17-b1c7-be043d74ba18"
	dir := grokSessionDir(t, d, id, "summary.json")
	var mo ModelObserver = newGrok(d)
	// By provider session id alone (the reconcile tick has no transcript path).
	if m, e, ok := mo.ObserveModel("", id); !ok || m != "grok-4.7" || e != "low" {
		t.Errorf("ObserveModel(by id) = %q, %q, %v", m, e, ok)
	}
	// By the transcript path a hook hands over.
	if m, e, ok := mo.ObserveModel(filepath.Join(dir, "updates.jsonl"), id); !ok || m != "grok-4.7" || e != "low" {
		t.Errorf("ObserveModel(by path) = %q, %q, %v", m, e, ok)
	}
	if _, _, ok := mo.ObserveModel("", "no-such-session"); ok {
		t.Error("an unknown session observes nothing")
	}
	if _, _, ok := mo.ObserveModel("", ""); ok {
		t.Error("no session id and no path observes nothing")
	}
	// A summary with a model but no effort still reports the model.
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"current_model_id":"grok-4.6"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, e, ok := mo.ObserveModel("", id); !ok || m != "grok-4.6" || e != "" {
		t.Errorf("model-only summary = %q, %q, %v", m, e, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"reasoning_effort":"low"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := mo.ObserveModel("", id); ok {
		t.Error("a summary with no model observes nothing")
	}
}

// The ask_user_question card is a card where a paste must never land (it would
// type into the answer field), but it is not a stall: a bound approval keeps it
// on screen and the bound question row is the Needs-you entry.
func TestGrokQuestionCardBlocksPasteButIsNoDialog(t *testing.T) {
	a := newGrok(Deps{})
	raw := grokPane(t, "question-card")
	plain := StripANSI(raw)
	if a.PasteReady(raw) {
		t.Error("PasteReady = true on the question card, want false")
	}
	if a.Idle(raw) {
		t.Error("Idle = true on the question card, want false")
	}
	for _, m := range a.PromptPatterns() {
		if m.Match.MatchString(plain) && (m.Exclude == nil || !m.Exclude.MatchString(plain)) {
			t.Errorf("prompt pattern %q matches the question card", m.Title)
		}
	}
	for _, d := range a.StartupDialogs() {
		if d.Match.MatchString(plain) && (d.Exclude == nil || !d.Exclude.MatchString(plain)) {
			t.Errorf("startup dialog %q matches the question card", d.Title)
		}
	}
}

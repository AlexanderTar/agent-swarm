package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func codexSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{AgentName: "login-form-coder", SessionID: "ses_01", Token: "tok",
		DaemonURL: "http://127.0.0.1:17778", Model: "gpt-6-astra", Effort: "medium",
		Cwd: t.TempDir(), Kickoff: "kick", Bin: "/usr/local/bin/swarm"}
}

// §11.1 + P0-1: env_vars names the parent variables, so no session value is in argv.
func TestCodexLaunchArgv(t *testing.T) {
	l, err := newCodex(testDeps(t)).Launch(codexSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	for _, want := range []string{
		"codex", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust",
		"--no-alt-screen", "-m gpt-6-astra", `model_reasoning_effort="medium"`,
		`mcp_servers.swarm.command="/usr/local/bin/swarm"`,
		`mcp_servers.swarm.args=["mcp"]`,
		`mcp_servers.swarm.env_vars=["SWARM_URL","SWARM_SESSION","SWARM_TOKEN_FILE","SWARM_AGENT_KIND"]`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	if l.Argv[len(l.Argv)-1] != "kick" {
		t.Fatalf("the kickoff must be the last argument: %v", l.Argv)
	}
	if strings.Contains(joined, "tok") || strings.Contains(joined, "ses_01") {
		t.Fatalf("no session value belongs in argv (§6.4): %s", joined)
	}
}

// Every -c value must be valid TOML on its own (§23.3).
func TestCodexConfigOverridesParseAsTOML(t *testing.T) {
	l, _ := newCodex(testDeps(t)).Launch(codexSpec(t))
	var n int
	for i, v := range l.Argv {
		if v != "-c" {
			continue
		}
		n++
		var m map[string]any
		if err := toml.Unmarshal([]byte(l.Argv[i+1]), &m); err != nil {
			t.Errorf("-c %q is not TOML: %v", l.Argv[i+1], err)
		}
	}
	if n != 4 {
		t.Fatalf("want 4 -c overrides (effort, command, args, env_vars), got %d", n)
	}
}

// UNVERIFIED: §11.1 states `codex resume <thread>`; no probe ran it.
func TestCodexResumeUsesTheThread(t *testing.T) {
	s := codexSpec(t)
	s.ProviderSessionID = "01a0af28-1d53-7ed0-a6e1-5ac92d9d3ac9"
	l, err := newCodex(testDeps(t)).Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	if l.Argv[0] != "codex" || l.Argv[1] != "resume" || l.Argv[2] != s.ProviderSessionID {
		t.Fatalf("argv head = %v", l.Argv[:3])
	}
}

func TestCodexProcessNames(t *testing.T) {
	a := newCodex(testDeps(t))
	if !anyMatch(a.ProcessNames(), "codex") {
		t.Error("codex must be accepted")
	}
	for _, s := range []string{"zsh", "node", "codexx"} {
		if anyMatch(a.ProcessNames(), s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}

// P0-4: a dim placeholder counts as empty; the same text undimmed does not.
func TestCodexIdleUsesTheDimAttribute(t *testing.T) {
	a := newCodex(testDeps(t))
	if !a.Idle(pane(t, "codex", "pane-idle-ansi.txt")) {
		t.Error("the dim placeholder is an empty prompt")
	}
	if a.Idle(pane(t, "codex", "pane-input-nonempty.txt")) {
		t.Error("undimmed typed text is not idle")
	}
	if a.Idle(pane(t, "codex", "pane-busy.txt")) {
		t.Error("the busy pane is not idle")
	}
}

// §11.1: trust is Enter; the retirement dialog is Down,Enter so the model is kept;
// the hook-trust screen fails the spawn.
func TestCodexStartupDialogs(t *testing.T) {
	ds := newCodex(testDeps(t)).StartupDialogs()
	if len(ds) != 3 {
		t.Fatalf("want 3 dialogs, got %d", len(ds))
	}
	byScreen := map[string]Dialog{}
	for _, d := range ds {
		for _, f := range []string{"pane-dialog-trust.txt", "pane-dialog-model-retirement.txt", "pane-dialog-hook-trust.txt"} {
			if d.Match.MatchString(pane(t, "codex", f)) {
				byScreen[f] = d
			}
		}
	}
	if len(byScreen) != 3 {
		t.Fatalf("each screen needs exactly one matching pattern, matched %d", len(byScreen))
	}
	if strings.Join(byScreen["pane-dialog-trust.txt"].Keys, ",") != "Enter" {
		t.Errorf("trust keys = %v", byScreen["pane-dialog-trust.txt"].Keys)
	}
	if strings.Join(byScreen["pane-dialog-model-retirement.txt"].Keys, ",") != "Down,Enter" {
		t.Errorf("retirement keys = %v (Enter alone would switch the model)", byScreen["pane-dialog-model-retirement.txt"].Keys)
	}
	if !byScreen["pane-dialog-hook-trust.txt"].Fail {
		t.Error("the hook-trust dialog must fail the spawn: --dangerously-bypass-hook-trust should have prevented it")
	}
	// no pattern fires on the idle screen
	for _, d := range ds {
		if d.Match.MatchString(pane(t, "codex", "pane-idle.txt")) {
			t.Error("a dialog pattern matched the idle screen")
		}
	}
}

// §11.2: codex context uses claude's shape; PreToolUse and Stop use {"decision":"block"}.
func TestCodexHookOutputShapes(t *testing.T) {
	a := newCodex(testDeps(t))
	ctxOut, _ := a.HookOutput("UserPromptSubmit", HookDecision{Context: "T"})
	if !strings.Contains(string(ctxOut), `"additionalContext":"T"`) ||
		!strings.Contains(string(ctxOut), `"hookEventName":"UserPromptSubmit"`) {
		t.Errorf("context output = %s", ctxOut)
	}
	for _, e := range []string{"PreToolUse", "Stop"} {
		b, _ := a.HookOutput(e, HookDecision{Block: true, Reason: "R"})
		if string(b) != `{"decision":"block","reason":"R"}` {
			t.Errorf("%s block output = %s", e, b)
		}
	}
	// PostCompact must never carry additionalContext (P0-2: the CLI rejects it)
	pc, _ := a.HookOutput("PostCompact", HookDecision{Context: "T"})
	if strings.Contains(string(pc), "additionalContext") {
		t.Errorf("PostCompact output = %s", pc)
	}
}

func TestCodexParseHook(t *testing.T) {
	a := newCodex(testDeps(t))
	in, _ := a.ParseHook("SessionStart", []byte(`{"session_id":"t1","turn_id":"x","source":"resume","cwd":"/w","transcript_path":"/r.jsonl"}`))
	if in.ProviderSessionID != "t1" || in.Source != "resume" || in.TranscriptPath != "/r.jsonl" {
		t.Fatalf("parsed = %+v", in)
	}
	tool, _ := a.ParseHook("PreToolUse", []byte(`{"session_id":"t1","tool_name":"mcp__swarm__swarm_checkpoint"}`))
	if !tool.IsSwarmTool {
		t.Error("mcp__swarm__* is a swarm tool on codex too (P0-2)")
	}
}

// §11.5: one structured TOML entry per folder, skipped when it already exists,
// and every other key in config.toml survives.
func TestCodexTrustFolderIsAStructuredIdempotentEdit(t *testing.T) {
	d := testDeps(t)
	cfg := filepath.Join(d.UserHome, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "model = \"gpt-6-astra\"\n\n[hooks.state]\nkeep = true\n"
	if err := os.WriteFile(cfg, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newCodex(d)
	dir := t.TempDir()
	if err := a.TrustFolder(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfg)
	var m map[string]any
	if err := toml.Unmarshal(b, &m); err != nil {
		t.Fatalf("config.toml no longer parses: %v\n%s", err, b)
	}
	if m["model"] != "gpt-6-astra" {
		t.Errorf("other keys must survive: %s", b)
	}
	if _, ok := m["hooks"]; !ok {
		t.Errorf("[hooks.state] must survive: %s", b)
	}
	real, _ := filepath.EvalSymlinks(dir)
	projects, _ := m["projects"].(map[string]any)
	entry, _ := projects[real].(map[string]any)
	if entry["trust_level"] != "trusted" {
		t.Fatalf("missing trust entry for %s: %s", real, b)
	}
	// second call changes nothing
	before, _ := os.ReadFile(cfg)
	if err := a.TrustFolder(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(cfg)
	if string(before) != string(after) {
		t.Errorf("TrustFolder must be idempotent:\n%s\n---\n%s", before, after)
	}
}

// §11.5: codex entries are never removed.
func TestCodexForgetFolderDoesNothing(t *testing.T) {
	d := testDeps(t)
	a := newCodex(d)
	if err := a.ForgetFolder(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.UserHome, ".codex", "config.toml")); err == nil {
		t.Fatal("ForgetFolder must not create or edit config.toml")
	}
}

func TestCodexChecks(t *testing.T) {
	d := testDeps(t)
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"codex --version":    {Out: "codex-cli 0.154.0\n"},
		"codex login status": {Out: "Logged in using ChatGPT\n"},
	}}
	d.Run = fake.Runner()
	a := newCodex(d)
	if v, ok := a.Installed(context.Background()); !ok || v != "0.154.0" {
		t.Fatalf("Installed = %q, %v", v, ok)
	}
	if err := a.AuthOK(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Responses["codex login status"] = execx.Result{Err: errors.New("exit 1")}
	if err := a.AuthOK(context.Background()); err == nil ||
		err.Error() != "Codex isn't signed in. Run `codex login` in a terminal." {
		t.Fatalf("err = %v", err)
	}
}

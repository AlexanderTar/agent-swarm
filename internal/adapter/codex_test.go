package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func codexSpec(t *testing.T, _ ...Deps) Spec {
	t.Helper()
	return Spec{AgentName: "login-form-coder", AgentID: "ag_01", SessionID: "ses_01", Token: "tok",
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

func TestCodexIsolatedMCPAndInstructions(t *testing.T) {
	d := testDeps(t)
	authPath := filepath.Join(d.UserHome, ".codex", "auth.json")
	_ = os.MkdirAll(filepath.Dir(authPath), 0o755)
	_ = os.WriteFile(authPath, []byte(`{"tokens":"secret"}`), 0o600)

	spec := codexSpec(t, d)
	spec.Instructions = "# Codex Custom Instructions"
	l, err := newCodex(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}

	codexHome := l.Env["CODEX_HOME"]
	if codexHome == "" {
		t.Fatalf("expected CODEX_HOME in Launch.Env")
	}
	symAuth := filepath.Join(codexHome, "auth.json")
	if _, err := os.Stat(symAuth); err != nil {
		t.Fatalf("expected symlinked auth.json at %s", symAuth)
	}

	var hasInstructionsFlag bool
	var instrPath string
	for i, v := range l.Argv {
		if v == "-c" && strings.HasPrefix(l.Argv[i+1], "model_instructions_file=") {
			hasInstructionsFlag = true
			val := l.Argv[i+1]
			instrPath = strings.TrimPrefix(val, "model_instructions_file=")
			instrPath = strings.Trim(instrPath, `"`)
		}
	}
	if !hasInstructionsFlag {
		t.Errorf("expected -c model_instructions_file=... in codex argv")
	}
	if instrPath != "" {
		content, err := os.ReadFile(instrPath)
		if err != nil || string(content) != spec.Instructions {
			t.Fatalf("instruction file content = %q, want %q", string(content), spec.Instructions)
		}
	}
}

func TestCodexInstructionsOmittedWhenUnset(t *testing.T) {
	d := testDeps(t)
	spec := codexSpec(t, d)
	spec.Instructions = ""
	l, err := newCodex(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range l.Argv {
		if v == "-c" && i+1 < len(l.Argv) && strings.HasPrefix(l.Argv[i+1], "model_instructions_file=") {
			t.Errorf("expected no model_instructions_file flag when instructions are empty")
		}
	}
}

func TestCodexResumeIsolatedMCPAndInstructions(t *testing.T) {
	d := testDeps(t)
	authPath := filepath.Join(d.UserHome, ".codex", "auth.json")
	_ = os.MkdirAll(filepath.Dir(authPath), 0o755)
	_ = os.WriteFile(authPath, []byte(`{"tokens":"secret"}`), 0o600)

	spec := codexSpec(t, d)
	spec.ProviderSessionID = "01a0af28-1d53-7ed0-a6e1-5ac92d9d3ac9"
	spec.Instructions = "# Codex Resume Instructions"
	l, err := newCodex(d).Resume(spec)
	if err != nil {
		t.Fatal(err)
	}

	codexHome := l.Env["CODEX_HOME"]
	if codexHome == "" {
		t.Fatalf("expected CODEX_HOME in Resume.Env")
	}
	symAuth := filepath.Join(codexHome, "auth.json")
	if _, err := os.Stat(symAuth); err != nil {
		t.Fatalf("expected symlinked auth.json at %s", symAuth)
	}

	var hasInstructionsFlag bool
	for i, v := range l.Argv {
		if v == "-c" && strings.HasPrefix(l.Argv[i+1], "model_instructions_file=") {
			hasInstructionsFlag = true
		}
	}
	if !hasInstructionsFlag {
		t.Errorf("expected -c model_instructions_file=... in codex resume argv")
	}
}

// P0 (2026-09-23): setupEnv isolated CODEX_HOME to a fresh empty directory and
// symlinked only auth.json into it. ~/.codex/hooks.json (swarm's hook wiring),
// ~/.codex/skills/ (the swarm/swarm-orchestrator skills `swarm install`
// writes there), and ~/.codex/plugins/ (the superpowers marketplace plugin
// cache) were never carried over -- a spawned Codex agent had zero swarm
// protocol awareness and zero superpowers skills. Mirrors agy's fix
// (TestAgyIsolatedHomeCarriesSkillsAndHooks).
func TestCodexIsolatedHomeCarriesHooksSkillsAndPlugins(t *testing.T) {
	d := testDeps(t)

	hooksPath := filepath.Join(d.UserHome, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const hooksBody = `{"hooks":{"SessionStart":[]}}`
	if err := os.WriteFile(hooksPath, []byte(hooksBody), 0o644); err != nil {
		t.Fatal(err)
	}

	skillPath := filepath.Join(d.UserHome, ".codex", "skills", "swarm", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const skillBody = "# Working as a Swarm agent\n..."
	if err := os.WriteFile(skillPath, []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginPath := filepath.Join(d.UserHome, ".codex", "plugins", "cache", "obra",
		"superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const pluginBody = "# Brainstorming\n..."
	if err := os.WriteFile(pluginPath, []byte(pluginBody), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newCodex(d).Launch(codexSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	codexHome := l.Env["CODEX_HOME"]

	gotHooks, err := os.ReadFile(filepath.Join(codexHome, "hooks.json"))
	if err != nil {
		t.Fatalf("expected hooks.json reachable in the isolated home: %v", err)
	}
	if string(gotHooks) != hooksBody {
		t.Errorf("hooks.json = %q, want %q", gotHooks, hooksBody)
	}

	gotSkill, err := os.ReadFile(filepath.Join(codexHome, "skills", "swarm", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the swarm skill reachable in the isolated home: %v", err)
	}
	if string(gotSkill) != skillBody {
		t.Errorf("skill body = %q, want %q", gotSkill, skillBody)
	}

	gotPlugin, err := os.ReadFile(filepath.Join(codexHome, "plugins", "cache", "obra",
		"superpowers", "6.3.0", "skills", "brainstorming", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the superpowers plugin reachable in the isolated home: %v", err)
	}
	if string(gotPlugin) != pluginBody {
		t.Errorf("plugin body = %q, want %q", gotPlugin, pluginBody)
	}
}

// setupEnv must not fail when none of hooks.json/skills/plugins exist yet
// (a machine where `swarm install` hasn't run for codex, or a bare test Deps).
func TestCodexIsolatedHomeToleratesMissingHooksSkillsAndPlugins(t *testing.T) {
	d := testDeps(t)
	if _, err := newCodex(d).Launch(codexSpec(t)); err != nil {
		t.Fatalf("Launch must not fail when hooks/skills/plugins are absent: %v", err)
	}
}

// Since codex 0.157.0 the CLI starts an app-server-control unix socket at
// $CODEX_HOME/app-server-control/app-server-control.sock. macOS caps a unix
// socket path (SUN_LEN) at 104 bytes including the NUL terminator, i.e. 103
// usable bytes. The old CODEX_HOME (<home>/run/launch/<session>/codex-home)
// blows past that on a real machine, so every codex launch failed with
// "path must be shorter than SUN_LEN". CodexHomeDir must stay short even
// for a long swarm home and a long agent id.
func TestCodexHomeDirSocketPathFitsSunLen(t *testing.T) {
	home := "/Users/" + strings.Repeat("a", 32) + "/.swarm"
	agentID := strings.Repeat("a", 30)
	codexHome := CodexHomeDir(home, agentID)
	sock := codexHome + "/app-server-control/app-server-control.sock"
	if len(sock) > 103 {
		t.Fatalf("socket path is %d bytes (max 103): %s", len(sock), sock)
	}
}

// CodexHomeDir must be a pure, deterministic function of (home, agent id):
// Resume must land on the same home Launch used (review round 1: it is keyed
// on the AGENT id, not the session id, precisely so that a pause->resume,
// which mints a new session id for the same agent, lands on the same home
// its earlier codex thread store lives in -- see
// TestCodexResumeLandsOnTheSameHomeAsTheOriginalLaunch), and the reconcile
// sweep (internal/runtime/reconcile.go) must be able to recompute it for
// every resumable agent without touching disk.
func TestCodexHomeDirIsDeterministicAndAgentScoped(t *testing.T) {
	home := t.TempDir()
	a := CodexHomeDir(home, "ag_01")
	b := CodexHomeDir(home, "ag_01")
	c := CodexHomeDir(home, "ag_02")
	if a != b {
		t.Fatalf("CodexHomeDir must be deterministic: %q != %q", a, b)
	}
	if a == c {
		t.Fatalf("CodexHomeDir must be agent-scoped: both agents got %q", a)
	}
}

// P0 (2026-09-26, SUN_LEN): setupEnv now isolates CODEX_HOME under
// <home>/cx/<hash> instead of the per-launch dir, and Launch passes
// --no-daemon (see the flags() comment for why). Both must survive.
func TestCodexLaunchUsesTheShortHomeAndNoDaemon(t *testing.T) {
	d := testDeps(t)
	spec := codexSpec(t, d)
	l, err := newCodex(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := CodexHomeDir(d.Home, spec.AgentID)
	if l.Env["CODEX_HOME"] != want {
		t.Fatalf("CODEX_HOME = %q, want %q", l.Env["CODEX_HOME"], want)
	}
	if !strings.Contains(strings.Join(l.Argv, " "), "--no-daemon") {
		t.Fatalf("argv must include --no-daemon: %v", l.Argv)
	}
}

// Review round 1 (blocking): startSession mints a fresh session id on every
// resume (internal/runtime/pause.go, agents.go), so keying CODEX_HOME on the
// session id put pause->resume in a brand-new, empty home -- `codex resume
// <thread>` would fail with "no rollout found for thread id ..." exactly
// like the pre-fix Wake bug. Only one session per agent is ever live/paused
// at a time, so keying on the AGENT id (stable across generations) instead
// makes Launch, Resume and a continuity successor for the same agent all
// land on the same CODEX_HOME.
func TestCodexResumeLandsOnTheSameHomeAsTheOriginalLaunch(t *testing.T) {
	d := testDeps(t)
	launchSpec := codexSpec(t, d)
	l, err := newCodex(d).Launch(launchSpec)
	if err != nil {
		t.Fatal(err)
	}

	resumeSpec := launchSpec
	resumeSpec.SessionID = "ses_02" // startSession mints a new id on resume
	resumeSpec.ProviderSessionID = "01a0af28-1d53-7ed0-a6e1-5ac92d9d3ac9"
	r, err := newCodex(d).Resume(resumeSpec)
	if err != nil {
		t.Fatal(err)
	}

	if l.Env["CODEX_HOME"] != r.Env["CODEX_HOME"] {
		t.Fatalf("Launch CODEX_HOME = %q, Resume CODEX_HOME = %q; a resume with a new session id must reuse the same agent's home",
			l.Env["CODEX_HOME"], r.Env["CODEX_HOME"])
	}
}

// D8 (batch-2 review, dialog-needs-you spec): resuming an old, long-lived
// agent must refresh CODEX_HOME's mtime, so reclaimCodexHomes's snapshotAt
// guard sees it as recently touched even though its directory is old.
func TestSetupEnvRefreshesCodexHomeMtimeOnResume(t *testing.T) {
	d := testDeps(t)
	spec := codexSpec(t, d)
	if _, err := newCodex(d).Launch(spec); err != nil {
		t.Fatal(err)
	}
	codexHome := CodexHomeDir(d.Home, spec.AgentID)
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(codexHome, old, old); err != nil {
		t.Fatal(err)
	}
	resumeSpec := spec
	resumeSpec.SessionID = "ses_02"
	resumeSpec.ProviderSessionID = "01a0af28-1d53-7ed0-a6e1-5ac92d9d3ac9"
	if _, err := newCodex(d).Resume(resumeSpec); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(fi.ModTime()) > 10*time.Second {
		t.Fatalf("codex home mtime = %s, want refreshed to ~now", fi.ModTime())
	}
}

// Review round 1, MINOR 1: setupEnv must refuse to launch rather than hand
// codex a CODEX_HOME whose app-server-control socket would exceed SUN_LEN.
// This can't happen with today's 8-hex-char hash and any realistic home
// path, but it's a cheap, clear guard against a future regression (a longer
// hash, a longer "cx" prefix, ...) instead of a cryptic runtime failure from
// codex itself.
func TestCodexSetupEnvRejectsAHomeThatWouldExceedSunLen(t *testing.T) {
	d := testDeps(t)
	// A real, writable path (so MkdirAll itself would happily succeed) that's
	// deliberately long enough to push the socket path over 103 bytes -- the
	// guard must fire before any filesystem call, not rely on MkdirAll
	// failing on its own for an unrelated reason (e.g. permissions).
	d.Home = filepath.Join(t.TempDir(), strings.Repeat("x", 80))
	spec := codexSpec(t, d)
	if _, err := newCodex(d).Launch(spec); err == nil {
		t.Fatal("expected an error when the codex home socket path would exceed SUN_LEN")
	}
}

// P0 (2026-09-26): Wake ran `codex queue` with no CODEX_HOME, so it always
// targeted ~/.codex instead of the session's isolated short home -- queueing
// a message against a thread that lives in a different CODEX_HOME fails
// with "no rollout found for thread id ..." (confirmed live). Wake must set
// CODEX_HOME to the same directory Launch/Resume used for this agent.
func TestCodexWakeUsesTheAgentsCodexHome(t *testing.T) {
	d := testDeps(t)
	var gotEnv map[string]string
	var gotArgv []string
	d.RunEnv = func(ctx context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
		gotEnv = env
		gotArgv = append([]string{name}, args...)
		return []byte("Queued message ...\n"), nil
	}
	ok, err := newCodex(d).Wake(context.Background(), WakeTarget{
		SessionID:         "ses_01",
		AgentID:           "ag_01",
		ProviderSessionID: "01a0af28-1d53-7ed0-a6e1-5ac92d9d3ac9",
		Notice:            "wake up",
	})
	if err != nil || !ok {
		t.Fatalf("Wake = %v, %v", ok, err)
	}
	want := CodexHomeDir(d.Home, "ag_01")
	if gotEnv["CODEX_HOME"] != want {
		t.Fatalf("CODEX_HOME = %q, want %q", gotEnv["CODEX_HOME"], want)
	}
	joined := strings.Join(gotArgv, " ")
	for _, want := range []string{"codex", "queue", "--thread 01a0af28-1d53-7ed0-a6e1-5ac92d9d3ac9", "--message wake up"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %v", want, gotArgv)
		}
	}
}

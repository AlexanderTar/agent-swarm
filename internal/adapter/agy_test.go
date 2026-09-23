package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// §6.7 + P0-12: the effort is in the slug and --effort is never passed.
func TestAgyLaunchUsesTheSuffixedSlugAndNeverEffort(t *testing.T) {
	l, err := newAgy(testDeps(t)).Launch(Spec{AgentName: "a", SessionID: "ses_1",
		Model: "gemini-3.8-flash-high", Effort: "high", Cwd: t.TempDir(),
		Kickoff: "kick", Bin: "/usr/local/bin/swarm"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	if !strings.Contains(joined, "--model gemini-3.8-flash-high") {
		t.Errorf("argv = %s", joined)
	}
	if strings.Contains(joined, "--effort") {
		t.Fatal("agy never gets --effort: a suffixed slug plus --effort is an error (P0-12)")
	}
	for _, want := range []string{"agy", "-i kick", "--dangerously-skip-permissions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %s", want, joined)
		}
	}
}

// UNVERIFIED: §11.1 states `agy --conversation <id>`; no probe ran it.
func TestAgyResumeUsesTheConversation(t *testing.T) {
	l, err := newAgy(testDeps(t)).Resume(Spec{SessionID: "ses_1", Model: "gemini-3.8-flash-high",
		ProviderSessionID: "c2305bee-1111-2222-3333-444444444444", Kickoff: "again",
		Cwd: t.TempDir(), Bin: "/usr/local/bin/swarm"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Argv, " ")
	if !strings.Contains(joined, "--conversation c2305bee-1111-2222-3333-444444444444") ||
		!strings.Contains(joined, "-i again") {
		t.Fatalf("argv = %s", joined)
	}
}

func TestAgyProcessNamesAndIdle(t *testing.T) {
	a := newAgy(testDeps(t))
	if !anyMatch(a.ProcessNames(), "agy") {
		t.Error("agy must be accepted")
	}
	if anyMatch(a.ProcessNames(), "node") {
		t.Error("node is not agy")
	}
	if !a.Idle(pane(t, "agy", "pane-idle.txt")) {
		t.Error("the idle pane should be idle")
	}
	if a.Idle(pane(t, "agy", "pane-busy.txt")) {
		t.Error("'esc to cancel' means busy, not idle")
	}
	if a.Idle(pane(t, "agy", "pane-input-nonempty.txt")) {
		t.Error("typed input is not idle")
	}
}

// §11.1: agy trust is Enter, and it fires for every new folder including children.
func TestAgyTrustDialogMatchesBothScreens(t *testing.T) {
	ds := newAgy(testDeps(t)).StartupDialogs()
	if len(ds) != 1 || strings.Join(ds[0].Keys, ",") != "Enter" {
		t.Fatalf("dialogs = %+v", ds)
	}
	for _, f := range []string{"pane-dialog-trust.txt", "pane-dialog-trust-child-of-trusted.txt"} {
		if !ds[0].Match.MatchString(pane(t, "agy", f)) {
			t.Errorf("the trust pattern does not match %s (agy trusts the exact path only)", f)
		}
	}
	if ds[0].Match.MatchString(pane(t, "agy", "pane-idle.txt")) {
		t.Error("the trust pattern matched the idle screen")
	}
}

// §11.2 + P0-2: context is an ephemeral message; a userMessage would look like a prompt.
func TestAgyHookOutputShapes(t *testing.T) {
	a := newAgy(testDeps(t))
	ctxOut, _ := a.HookOutput("PreInvocation", HookDecision{Context: "T"})
	var inj struct {
		Steps []struct {
			Ephemeral string `json:"ephemeralMessage"`
			User      string `json:"userMessage"`
		} `json:"injectSteps"`
	}
	if err := json.Unmarshal(ctxOut, &inj); err != nil {
		t.Fatal(err)
	}
	if len(inj.Steps) != 1 || inj.Steps[0].Ephemeral != "T" || inj.Steps[0].User != "" {
		t.Fatalf("context output = %s", ctxOut)
	}
	deny, _ := a.HookOutput("PreToolUse", HookDecision{Block: true, Reason: "R"})
	if string(deny) != `{"decision":"deny","reason":"R"}` {
		t.Errorf("deny output = %s", deny)
	}
	stop, _ := a.HookOutput("Stop", HookDecision{Block: true, Reason: "R"})
	if string(stop) != `{"decision":"continue","reason":"R"}` {
		t.Errorf("stop output = %s", stop)
	}
}

// P0-2: agy names MCP calls call_mcp_tool with args.ServerName, and shells run_command.
func TestAgyParseHookToolNames(t *testing.T) {
	a := newAgy(testDeps(t))
	mcp, err := a.ParseHook("PreToolUse", []byte(`{"conversationId":"c1","workspacePaths":[],
		"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"swarm","ToolName":"swarm_sync"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if mcp.ProviderSessionID != "c1" || !mcp.IsSwarmTool {
		t.Fatalf("parsed = %+v", mcp)
	}
	other, _ := a.ParseHook("PreToolUse", []byte(`{"conversationId":"c1",
		"toolCall":{"name":"call_mcp_tool","args":{"ServerName":"railway","ToolName":"x"}}}`))
	if other.IsSwarmTool {
		t.Error("another MCP server's tool is not a swarm tool")
	}
	sh, _ := a.ParseHook("PreToolUse", []byte(`{"conversationId":"c1",
		"toolCall":{"name":"run_command","args":{"CommandLine":"git commit -m x"}}}`))
	if sh.IsSwarmTool || sh.Command != "git commit -m x" {
		t.Fatalf("shell parse = %+v", sh)
	}
	// an empty workspacePaths must not break the parse (the session is found by token)
	if _, err := a.ParseHook("Stop", []byte(`{"conversationId":"c1","workspacePaths":[],"fullyIdle":true}`)); err != nil {
		t.Fatal(err)
	}
}

// §11.5: the only agent whose trust entries are cleaned up, with a structured
// edit that is skipped when the file changed in the meantime.
func TestAgyForgetFolderRemovesOneEntry(t *testing.T) {
	d := testDeps(t)
	p := filepath.Join(d.UserHome, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"model":"Gemini 3.8 Flash","trustedWorkspaces":["/a","/b"],"other":1}`), 0o644)
	a := newAgy(d)
	if err := a.ForgetFolder(context.Background(), "/a"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ws, _ := m["trustedWorkspaces"].([]any)
	if len(ws) != 1 || ws[0] != "/b" {
		t.Fatalf("trustedWorkspaces = %v (%s)", ws, b)
	}
	if m["model"] != "Gemini 3.8 Flash" || m["other"] == nil {
		t.Errorf("other keys must survive: %s", b)
	}
	// a missing file is not an error
	os.Remove(p)
	if err := a.ForgetFolder(context.Background(), "/a"); err != nil {
		t.Fatalf("a missing settings file is fine: %v", err)
	}
}

// §11.5: agy records the path as given, not the realpath, so the cleanup must not resolve it.
func TestAgyForgetFolderDoesNotResolveSymlinks(t *testing.T) {
	d := testDeps(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	p := filepath.Join(d.UserHome, ".gemini", "antigravity-cli", "settings.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"trustedWorkspaces":["`+link+`"]}`), 0o644)
	if err := newAgy(d).ForgetFolder(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), link) {
		t.Fatalf("the entry for the given path should be gone: %s", b)
	}
}

// UNVERIFIED: §11.1 states the token-file + `agy models` pair; no probe ran the
// signed-out half. Preflight fails safe on anything it cannot parse.
func TestAgyChecks(t *testing.T) {
	d := testDeps(t)
	tok := filepath.Join(d.UserHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"agy --version": {Out: "1.2.5\n"},
		"agy models":    {Out: "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n"},
	}}
	d.Run = fake.Runner()
	a := newAgy(d)
	if v, ok := a.Installed(context.Background()); !ok || v != "1.2.5" {
		t.Fatalf("Installed = %q, %v", v, ok)
	}
	if err := a.AuthOK(context.Background()); err == nil {
		t.Fatal("no token file means not signed in")
	}
	if err := os.MkdirAll(filepath.Dir(tok), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tok, []byte(`{"token":{"access_token":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthOK(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The auth rule is a *pair*: the token file exists AND `agy models` exits 0.
	// Both halves are UNVERIFIED (see the note above), so both must fail closed.
	// With the token in place, a failing `agy models` is still "signed out".
	fake.Responses["agy models"] = execx.Result{Err: errors.New("exit 1"), Out: "Reauthentication required\n"}
	err := a.AuthOK(context.Background())
	if err == nil {
		t.Fatal("a failing `agy models` with a token present must still be signed out")
	}
	if want := "agy isn't signed in. Run `agy login` in a terminal."; err.Error() != want {
		t.Fatalf("AuthOK = %q, want %q", err, want)
	}
}

// capturingWriteCloser is a concurrency-safe in-memory Stdin double: Wake
// does exactly one synchronous Write before returning, but the reaper
// goroutine's later Close races the test's own assertions.
type capturingWriteCloser struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
}

func (c *capturingWriteCloser) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}
func (c *capturingWriteCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
func (c *capturingWriteCloser) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}
func (c *capturingWriteCloser) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// agy now natively wakes (2026-09-22): confirmed live against Google's
// documented `--input-format stream-json` schema and the isolated agy-home
// setupEnv already builds. See docs/specs/2026-09-22-agy-native-wake-stream-json.md.
func TestAgyWakeWritesTheDocumentedPayloadWithoutBlockingOnTheTurn(t *testing.T) {
	d := testDeps(t)
	stdin := &capturingWriteCloser{}
	stdoutR, stdoutW := io.Pipe()
	killed := make(chan struct{}, 1)
	var gotEnv map[string]string
	var gotArgv []string
	d.StartEnv = func(ctx context.Context, env map[string]string, name string, args ...string) (*execx.Proc, error) {
		gotEnv = env
		gotArgv = append([]string{name}, args...)
		return &execx.Proc{Stdin: stdin, Stdout: stdoutR, Kill: func() {
			select {
			case killed <- struct{}{}:
			default:
			}
		}}, nil
	}

	done := make(chan struct{})
	var ok bool
	var werr error
	go func() {
		ok, werr = newAgy(d).Wake(context.Background(), WakeTarget{
			SessionID: "ses_1", ProviderSessionID: "conv_1", Notice: "hello"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Wake blocked on the turn instead of returning once the write succeeded (§ Decision 7)")
	}
	if !ok || werr != nil {
		t.Fatalf("Wake = %v, %v", ok, werr)
	}

	wantArgv := []string{"agy", "--conversation", "conv_1", "--input-format", "stream-json",
		"--output-format", "stream-json", "--dangerously-skip-permissions"}
	if strings.Join(gotArgv, " ") != strings.Join(wantArgv, " ") {
		t.Fatalf("argv = %v, want %v", gotArgv, wantArgv)
	}
	agyHome := filepath.Join(d.launchDir("ses_1"), "agy-home")
	if gotEnv["HOME"] != agyHome {
		t.Fatalf("HOME = %q, want %q (Wake must reuse the session's isolated home, not the daemon's ambient $HOME)", gotEnv["HOME"], agyHome)
	}

	var payload struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(stdin.Bytes(), &payload); err != nil {
		t.Fatalf("stdin = %q: %v", stdin.Bytes(), err)
	}
	if payload.Event != "user" || payload.Message.Content != "hello" {
		t.Fatalf("payload = %+v, want event=user message.content=hello (the schema confirmed in the Task 1 probe)", payload)
	}

	if stdin.Closed() {
		t.Fatal("stdin closed before the turn's result event arrived; Wake must not race the reaper")
	}

	fmt.Fprintln(stdoutW, `{"event":"result","result":{"status":"SUCCESS","response":"hi"}}`)
	stdoutW.Close()

	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("reaper goroutine never cleaned up after the result event")
	}
	if !stdin.Closed() {
		t.Fatal("expected stdin closed once the reaper's cleanup ran")
	}
}

func TestAgyWakeReturnsFalseWhenStartFails(t *testing.T) {
	d := testDeps(t)
	wantErr := errors.New("boom")
	d.StartEnv = func(context.Context, map[string]string, string, ...string) (*execx.Proc, error) {
		return nil, wantErr
	}
	ok, err := newAgy(d).Wake(context.Background(), WakeTarget{SessionID: "ses_1", ProviderSessionID: "conv_1"})
	if ok || !errors.Is(err, wantErr) {
		t.Fatalf("Wake = %v, %v; want false, %v", ok, err, wantErr)
	}
}

func agySpec(t *testing.T) Spec {
	t.Helper()
	return Spec{
		AgentName: "agy-coder",
		SessionID: "ses_agy_01",
		Token:     "tok",
		DaemonURL: "http://127.0.0.1:17778",
		Model:     "gemini-3.8-flash-high",
		Effort:    "high",
		Cwd:       t.TempDir(),
		Kickoff:   "kick",
		Bin:       "/usr/local/bin/swarm",
	}
}

func TestAgyIsolatedMCPAndInstructions(t *testing.T) {
	d := testDeps(t)
	tokenPath := filepath.Join(d.UserHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	settingsPath := filepath.Join(d.UserHome, ".gemini", "antigravity-cli", "settings.json")
	_ = os.MkdirAll(filepath.Dir(tokenPath), 0o755)
	_ = os.WriteFile(tokenPath, []byte("oauth-secret"), 0o600)
	_ = os.WriteFile(settingsPath, []byte(`{"trustedWorkspaces":[]}`), 0o644)

	spec := agySpec(t)
	spec.Instructions = "# Agy Custom Rules\nAlways run tests."
	l, err := newAgy(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	agyHome := l.Env["HOME"]
	if agyHome == "" {
		t.Fatalf("expected HOME in Launch.Env")
	}
	symToken := filepath.Join(agyHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if _, err := os.Stat(symToken); err != nil {
		t.Fatalf("expected symlinked antigravity-oauth-token at %s", symToken)
	}
	symSettings := filepath.Join(agyHome, ".gemini", "antigravity-cli", "settings.json")
	if _, err := os.Stat(symSettings); err != nil {
		t.Fatalf("expected symlinked settings.json at %s", symSettings)
	}

	mcpFile := filepath.Join(agyHome, ".gemini", "config", "mcp_config.json")
	mcpBytes, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("expected mcp_config.json at %s: %v", mcpFile, err)
	}
	var mcpCfg struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpBytes, &mcpCfg); err != nil {
		t.Fatal(err)
	}
	if mcpCfg.MCPServers["swarm"] == nil {
		t.Errorf("expected swarm MCP server in mcp_config.json")
	}
	if len(mcpCfg.MCPServers) != 1 {
		t.Errorf("expected only swarm in mcpServers, got %v", mcpCfg.MCPServers)
	}

	rulesFile := filepath.Join(agyHome, ".gemini", "AGENTS.md")
	content, err := os.ReadFile(rulesFile)
	if err != nil || string(content) != spec.Instructions {
		t.Fatalf("expected rules file with content %q, got %q", spec.Instructions, string(content))
	}
}

// P0 (2026-09-22 live incident): setupEnv used to symlink only two named
// files out of the real ~/.gemini/antigravity-cli/ (oauth token, settings.json).
// The onboarding-completed flag actually lives in a third file in that same
// directory (antigravity_state.pbtxt), which the allowlist silently dropped —
// so every swarm-spawned agy hit the interactive "choose your color scheme"
// wizard and failed to start (100% of spawns). Confirmed live and fixed by
// symlinking the whole antigravity-cli directory instead of an allowlist.
func TestAgyIsolatedHomeCarriesOnboardingState(t *testing.T) {
	d := testDeps(t)
	real := filepath.Join(d.UserHome, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "antigravity-oauth-token"), []byte("oauth-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "settings.json"), []byte(`{"trustedWorkspaces":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const onboarded = "post_onboarding: {\n}\nagent_onboarding_completed: AGENT_ONBOARDING_STATE_COMPLETED\n"
	if err := os.WriteFile(filepath.Join(real, "antigravity_state.pbtxt"), []byte(onboarded), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newAgy(d).Launch(agySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	agyHome := l.Env["HOME"]
	got, err := os.ReadFile(filepath.Join(agyHome, ".gemini", "antigravity-cli", "antigravity_state.pbtxt"))
	if err != nil {
		t.Fatalf("expected antigravity_state.pbtxt reachable in isolated home (onboarding state must survive isolation): %v", err)
	}
	if string(got) != onboarded {
		t.Errorf("got %q, want %q", got, onboarded)
	}
}

// P0 (2026-09-22, found while investigating why agy behaves as if it has no
// idea how swarm works): the swarm/swarm-orchestrator skills `swarm install`
// writes to ~/.gemini/skills/ (Config.SkillsDir(KindAgy)) live outside both
// ~/.gemini/antigravity-cli/ and ~/.gemini/config/ -- setupEnv never carried
// that directory into the isolated home, nor ~/.gemini/config/hooks.json
// (swarm's PreToolUse/PostToolUse/Stop wiring). Every swarm-spawned agy agent
// has therefore run with zero knowledge of the swarm protocol and zero hook
// interception, unlike claude (no HOME isolation at all) and codex (isolates
// only CODEX_HOME, not its skills path).
// P0 (2026-09-23), superseding the 2026-09-22 fix of the same name: that fix
// symlinked ~/.gemini/skills/, which turned out to be the wrong path -- a
// live spawn's own reported skill listing never included "swarm" while it
// did include agy's genuinely-discovered built-ins. Per
// antigravity.google/docs/skills/, agy's real global skills directory is
// ~/.gemini/antigravity-cli/skills/ (Config.SkillsDir(KindAgy) now matches),
// which is INSIDE the directory the onboarding-isolation fix already
// symlinks whole -- so correcting the install path needs no separate
// isolation glue for skills at all. Hooks (~/.gemini/config/hooks.json)
// still live outside antigravity-cli and still need their own symlink.
func TestAgyIsolatedHomeCarriesSkillsAndHooks(t *testing.T) {
	d := testDeps(t)
	skillsDir := filepath.Join(d.UserHome, ".gemini", "antigravity-cli", "skills", "swarm")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const skillBody = "# Working as a Swarm agent\n..."
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(d.UserHome, ".gemini", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const hooksBody = `{"swarm":{"PreToolUse":[]}}`
	if err := os.WriteFile(hooksPath, []byte(hooksBody), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newAgy(d).Launch(agySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	agyHome := l.Env["HOME"]

	gotSkill, err := os.ReadFile(filepath.Join(agyHome, ".gemini", "antigravity-cli", "skills", "swarm", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the swarm skill reachable in the isolated home via the antigravity-cli symlink: %v", err)
	}
	if string(gotSkill) != skillBody {
		t.Errorf("skill body = %q, want %q", gotSkill, skillBody)
	}

	gotHooks, err := os.ReadFile(filepath.Join(agyHome, ".gemini", "config", "hooks.json"))
	if err != nil {
		t.Fatalf("expected hooks.json reachable in the isolated home: %v", err)
	}
	if string(gotHooks) != hooksBody {
		t.Errorf("hooks.json = %q, want %q", gotHooks, hooksBody)
	}
}

// P0 (2026-09-23): yesterday's fix carried hooks.json and (via the
// antigravity-cli symlink) the swarm/swarm-orchestrator skills into the
// isolated home, but never ~/.gemini/config/plugins/ -- so the superpowers
// marketplace plugin itself (brainstorming, systematic-debugging,
// writing-plans skill definitions) still wasn't reachable, even though
// Agy.SuperpowersInstalled() checks the real home and reports "installed."
func TestAgyIsolatedHomeCarriesSuperpowersPlugin(t *testing.T) {
	d := testDeps(t)
	pluginPath := filepath.Join(d.UserHome, ".gemini", "config", "plugins",
		"superpowers", "skills", "brainstorming", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const pluginBody = "# Brainstorming\n..."
	if err := os.WriteFile(pluginPath, []byte(pluginBody), 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := newAgy(d).Launch(agySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	agyHome := l.Env["HOME"]

	got, err := os.ReadFile(filepath.Join(agyHome, ".gemini", "config", "plugins",
		"superpowers", "skills", "brainstorming", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the superpowers plugin reachable in the isolated home: %v", err)
	}
	if string(got) != pluginBody {
		t.Errorf("plugin body = %q, want %q", got, pluginBody)
	}
}

func TestAgyInstructionsOmittedWhenUnset(t *testing.T) {
	d := testDeps(t)
	spec := agySpec(t)
	spec.Instructions = ""
	l, err := newAgy(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	agyHome := l.Env["HOME"]
	rulesFile := filepath.Join(agyHome, ".gemini", "AGENTS.md")
	if _, err := os.Stat(rulesFile); err == nil {
		t.Errorf("expected no AGENTS.md when instructions are empty")
	}
}

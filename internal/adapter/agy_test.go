package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestAgyHasNoNativeWake(t *testing.T) {
	ok, err := newAgy(testDeps(t)).Wake(context.Background(), WakeTarget{SessionID: "ses_1"})
	if ok || err != nil {
		t.Fatalf("Wake = %v, %v; agy has no native push (§11.3)", ok, err)
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

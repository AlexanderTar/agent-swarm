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

func cursorSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{AgentName: "ui-reviewer", SessionID: "ses_1", Model: "auto", Cwd: "/tmp/w",
		Kickoff: "kick", Bin: "/usr/local/bin/swarm",
		PluginDirs: []string{"/Users/u/.swarm/vendor/plugins/superpowers",
			"/Users/u/.swarm/vendor/plugins/elements-of-style"}}
}

// §11.1: create-chat is a pre-run step; its id becomes --resume.
func TestCursorLaunchHasACreateChatPreRunAndPluginDirs(t *testing.T) {
	l, err := newCursor(testDeps(t)).Launch(cursorSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.PreRun) != 1 || strings.Join(l.PreRun[0], " ") != "cursor-agent create-chat" {
		t.Fatalf("PreRun = %v", l.PreRun)
	}
	joined := strings.Join(l.Argv, " ")
	for _, want := range []string{"cursor-agent", "--resume", "--yolo", "--trust",
		"--approve-mcps", "--model auto", "--workspace /tmp/w"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %s", want, joined)
		}
	}
	// P0-8: --plugin-dir is repeated per plugin, so a spawn never depends on the copy
	n := strings.Count(joined, "--plugin-dir")
	if n != 2 {
		t.Fatalf("want 2 --plugin-dir flags, got %d: %s", n, joined)
	}
	if l.Argv[len(l.Argv)-1] != "kick" {
		t.Fatalf("the kickoff must be last: %v", l.Argv)
	}
}

// The chat id is substituted into --resume where the pre-run output lands.
func TestCursorLaunchUsesThePreRunPlaceholder(t *testing.T) {
	l, _ := newCursor(testDeps(t)).Launch(cursorSpec(t))
	for i, v := range l.Argv {
		if v == "--resume" && l.Argv[i+1] != PreRunOutput {
			t.Fatalf("--resume %q, want the %s placeholder", l.Argv[i+1], PreRunOutput)
		}
	}
}

func TestCursorResumeUsesTheKnownChatID(t *testing.T) {
	s := cursorSpec(t)
	s.ProviderSessionID = "a542efef-6487-4f70-9a44-4736806a5fe2"
	l, err := newCursor(testDeps(t)).Resume(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.PreRun) != 0 {
		t.Fatal("a resume needs no create-chat")
	}
	if !strings.Contains(strings.Join(l.Argv, " "), "--resume "+s.ProviderSessionID) {
		t.Fatalf("argv = %v", l.Argv)
	}
}

// P0-4: the pane pid is the cursor-agent wrapper, which shows as node.
func TestCursorProcessNamesAndIdle(t *testing.T) {
	a := newCursor(testDeps(t))
	if !anyMatch(a.ProcessNames(), "node") {
		t.Error("node is cursor's pane command")
	}
	if anyMatch(a.ProcessNames(), "zsh") {
		t.Error("a shell is not cursor (I2)")
	}
	if !a.Idle(pane(t, "cursor", "pane-idle-ansi.txt")) {
		t.Error("the dim 'Add a follow-up' placeholder is an empty prompt")
	}
	if a.Idle(pane(t, "cursor", "pane-input-nonempty-ansi.txt")) {
		t.Error("undimmed typed text is not idle")
	}
	if a.Idle(pane(t, "cursor", "pane-busy.txt")) {
		t.Error("'ctrl+c to stop' on the prompt line means busy")
	}
}

// §11.1: --yolo --trust --approve-mcps skip every dialog.
func TestCursorHasNoStartupDialogs(t *testing.T) {
	if ds := newCursor(testDeps(t)).StartupDialogs(); len(ds) != 0 {
		t.Fatalf("dialogs = %+v", ds)
	}
}

func TestCursorInterruptIsCtrlC(t *testing.T) {
	if k := newCursor(testDeps(t)).InterruptKeys(); strings.Join(k, ",") != "C-c" {
		t.Fatalf("keys = %v", k)
	}
}

func TestCursorHookOutputShapes(t *testing.T) {
	a := newCursor(testDeps(t))
	ctxOut, _ := a.HookOutput("beforeSubmitPrompt", HookDecision{Context: "T"})
	if string(ctxOut) != `{"additional_context":"T"}` {
		t.Errorf("context output = %s", ctxOut)
	}
	deny, _ := a.HookOutput("preToolUse", HookDecision{Block: true, Reason: "R"})
	var m map[string]string
	json.Unmarshal(deny, &m)
	if m["permission"] != "deny" || m["user_message"] != "R" || m["agent_message"] != "R" {
		t.Errorf("deny output = %s", deny)
	}
	stop, _ := a.HookOutput("stop", HookDecision{Block: true, Reason: "R"})
	if string(stop) != `{"followup_message":"R"}` {
		t.Errorf("stop output = %s", stop)
	}
}

// P0-2: every swarm tool name starts with swarm_, because the server name is absent.
func TestCursorParseHookToolNames(t *testing.T) {
	a := newCursor(testDeps(t))
	in, err := a.ParseHook("preToolUse", []byte(`{"conversation_id":"c1","transcript_path":null,
		"tool_name":"MCP:swarm_sync"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.ProviderSessionID != "c1" || !in.IsSwarmTool {
		t.Fatalf("parsed = %+v", in)
	}
	sh, _ := a.ParseHook("preToolUse", []byte(`{"conversation_id":"c1","tool_name":"Shell",
		"tool_input":{"command":"gh pr create"}}`))
	if sh.IsSwarmTool || sh.Command != "gh pr create" {
		t.Fatalf("shell parse = %+v", sh)
	}
	other, _ := a.ParseHook("preToolUse", []byte(`{"conversation_id":"c1","tool_name":"MCP:railway_status"}`))
	if other.IsSwarmTool {
		t.Error("another server's tool is not a swarm tool")
	}
}

func TestCursorChecks(t *testing.T) {
	d := testDeps(t)
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"cursor-agent --version": {Out: "2026.09.15-d2fe57e\n"},
		"cursor-agent status":    {Out: "Logged in as someone@example.com\n"},
	}}
	d.Run = fake.Runner()
	a := newCursor(d)
	if v, ok := a.Installed(context.Background()); !ok || v != "2026.09.15-d2fe57e" {
		t.Fatalf("Installed = %q, %v", v, ok)
	}
	if err := a.AuthOK(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Responses["cursor-agent status"] = execx.Result{Out: "Not logged in\n"}
	if err := a.AuthOK(context.Background()); err == nil ||
		err.Error() != "Cursor isn't signed in. Run `cursor-agent login` in a terminal." {
		t.Fatalf("err = %v", err)
	}
	fake.Responses["cursor-agent status"] = execx.Result{Err: errors.New("exit 1")}
	if err := a.AuthOK(context.Background()); err == nil {
		t.Fatal("a non-zero exit is not signed in")
	}
}

func TestCursorHasNoNativeWake(t *testing.T) {
	ok, err := newCursor(testDeps(t)).Wake(context.Background(), WakeTarget{SessionID: "ses_1"})
	if ok || err != nil {
		t.Fatalf("Wake = %v, %v", ok, err)
	}
}

func TestCursorIsolatedMCPAndInstructions(t *testing.T) {
	d := testDeps(t)
	spec := cursorSpec(t)
	spec.Cwd = t.TempDir()
	spec.Instructions = "# Cursor Rules\nStrict Go standard library."
	l, err := newCursor(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	cursorDir := l.Env["CURSOR_DATA_DIR"]
	if cursorDir == "" {
		t.Fatalf("expected CURSOR_DATA_DIR in Launch.Env")
	}
	mcpFile := filepath.Join(cursorDir, "mcp.json")
	mcpBytes, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("expected mcp.json at %s: %v", mcpFile, err)
	}
	var mcpCfg struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpBytes, &mcpCfg); err != nil {
		t.Fatal(err)
	}
	if mcpCfg.MCPServers["swarm"] == nil {
		t.Errorf("expected swarm MCP server in cursor mcp.json")
	}
	if len(mcpCfg.MCPServers) != 1 {
		t.Errorf("expected only swarm in cursor mcp.json, got %v", mcpCfg.MCPServers)
	}

	wsRules := filepath.Join(spec.Cwd, "AGENTS.md")
	content, err := os.ReadFile(wsRules)
	if err != nil || string(content) != spec.Instructions {
		t.Fatalf("expected workspace AGENTS.md with content %q, got %q", spec.Instructions, string(content))
	}
}

func TestCursorInstructionsOmittedWhenUnset(t *testing.T) {
	d := testDeps(t)
	spec := cursorSpec(t)
	spec.Cwd = t.TempDir()
	spec.Instructions = ""
	_, err := newCursor(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	wsRules := filepath.Join(spec.Cwd, "AGENTS.md")
	if _, err := os.Stat(wsRules); err == nil {
		t.Errorf("expected no AGENTS.md in workspace when instructions are empty")
	}
}

// L16: a value Cursor passed through literally counts as unset in the shim.
func TestCursorMCPEnvPlaceholdersAreDocumentedInOneMap(t *testing.T) {
	want := map[string]string{
		"SWARM_URL":        "${env:SWARM_URL}",
		"SWARM_SESSION":    "${env:SWARM_SESSION}",
		"SWARM_TOKEN_FILE": "${env:SWARM_TOKEN_FILE}",
		"SWARM_AGENT_KIND": "cursor",
	}
	got := CursorMCPEnv()
	if len(got) != len(want) {
		t.Fatalf("CursorMCPEnv = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

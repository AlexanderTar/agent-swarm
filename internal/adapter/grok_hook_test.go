package adapter

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func grokHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/grok-hook-" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestGrokParseHook replays the live-probe payloads (SPIKE-52 TASK-1125 and
// TASK-1129) for every event grok fires: the snake_case keys grok sends beside
// its camelCase ones are what the Claude parser reads.
func TestGrokParseHook(t *testing.T) {
	const sid = "11111111-1111-4111-8111-111111111111"
	const sidB = "22222222-2222-4222-8222-222222222222"
	g := newGrok(Deps{})
	cases := []struct {
		fixture, event string
		check          func(t *testing.T, in HookInput)
	}{
		{"session-start", "SessionStart", func(t *testing.T, in HookInput) {
			if in.ProviderSessionID != sid || in.Source != "new" || in.Model != "" || in.Cwd == "" {
				t.Errorf("%+v", in)
			}
		}},
		{"user-prompt-submit", "UserPromptSubmit", func(t *testing.T, in HookInput) {
			if in.ProviderSessionID != sid || !strings.HasPrefix(in.Prompt, "Use the swarm MCP server tool") || in.TranscriptPath == "" {
				t.Errorf("%+v", in)
			}
		}},
		{"pre-tool-use-shell", "PreToolUse", func(t *testing.T, in HookInput) {
			if in.ToolName != "run_terminal_command" || in.Command != "echo hi" || in.IsSwarmTool {
				t.Errorf("%+v", in)
			}
		}},
		{"pre-tool-use-search-tool", "PreToolUse", func(t *testing.T, in HookInput) {
			if in.ToolName != "search_tool" || in.IsSwarmTool {
				t.Errorf("search_tool must not count as a Swarm tool: %+v", in)
			}
		}},
		{"pre-tool-use-swarm", "PreToolUse", func(t *testing.T, in HookInput) {
			if in.ToolName != "swarm__swarm_sync" || !in.IsSwarmTool {
				t.Errorf("%+v", in)
			}
			if string(in.RawToolInput) != "{}" {
				t.Errorf("swarm args must be unwrapped from tool_input.tool_input, got %s", in.RawToolInput)
			}
		}},
		{"post-tool-use-swarm", "PostToolUse", func(t *testing.T, in HookInput) {
			if !in.IsSwarmTool || len(in.ToolResponse) == 0 {
				t.Errorf("%+v", in)
			}
		}},
		{"pre-tool-use-ask-user-question", "PreToolUse", func(t *testing.T, in HookInput) {
			if in.ToolName != "ask_user_question" || in.IsSwarmTool || !strings.Contains(string(in.RawToolInput), "Tea or coffee?") {
				t.Errorf("%+v", in)
			}
		}},
		{"post-tool-use-ask-user-question", "PostToolUse", func(t *testing.T, in HookInput) {
			if in.ToolName != "ask_user_question" || !strings.Contains(string(in.ToolResponse), "User has answered your questions") {
				t.Errorf("%+v", in)
			}
		}},
		{"stop-end-turn", "Stop", func(t *testing.T, in HookInput) {
			if in.ProviderSessionID != sidB || in.Reason != "end_turn" || in.StopHookActive || in.LastAssistantMessage == "" || in.TranscriptPath == "" {
				t.Errorf("%+v", in)
			}
		}},
		{"stop-end-turn-active", "Stop", func(t *testing.T, in HookInput) {
			if in.Reason != "end_turn" || !in.StopHookActive {
				t.Errorf("%+v", in)
			}
		}},
		{"stop-shutdown", "Stop", func(t *testing.T, in HookInput) {
			if in.Reason != "shutdown" || in.LastAssistantMessage != "" {
				t.Errorf("%+v", in)
			}
		}},
		{"stop-cancelled", "StopCancelled", func(t *testing.T, in HookInput) {
			if in.Reason != "user_interrupt" || in.LastAssistantMessage == "" {
				t.Errorf("%+v", in)
			}
		}},
		{"stop-failure", "StopFailure", func(t *testing.T, in HookInput) {
			if !strings.Contains(in.LastAssistantMessage, "429") || in.ProviderSessionID == "" {
				t.Errorf("%+v", in)
			}
		}},
		{"notification-elicitation", "Notification", func(t *testing.T, in HookInput) {
			if in.NotificationType != "elicitation_dialog" {
				t.Errorf("%+v", in)
			}
		}},
		{"notification-idle", "Notification", func(t *testing.T, in HookInput) {
			if in.NotificationType != "idle_prompt" {
				t.Errorf("%+v", in)
			}
		}},
		{"pre-compact", "PreCompact", func(t *testing.T, in HookInput) {
			if in.Source != "manual" || in.ProviderSessionID != sid {
				t.Errorf("%+v", in)
			}
		}},
		{"post-compact", "PostCompact", func(t *testing.T, in HookInput) {
			if in.Source != "manual" || in.ProviderSessionID != sid {
				t.Errorf("%+v", in)
			}
		}},
		{"session-end", "SessionEnd", func(t *testing.T, in HookInput) {
			if in.Reason != "shutdown" || in.ProviderSessionID == "" {
				t.Errorf("%+v", in)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			in, err := g.ParseHook(c.event, grokHookFixture(t, c.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if in.Event != c.event {
				t.Errorf("Event = %q, want %q", in.Event, c.event)
			}
			c.check(t, in)
		})
	}
}

func TestGrokParseHookSwarmToolPrefixes(t *testing.T) {
	g := newGrok(Deps{})
	for name, want := range map[string]bool{
		"swarm__swarm_sync":     true,
		"mcp__swarm__swarm_ask": true,
		"search_tool":           false,
		"swarm_sync":            false,
		"other__swarm__x":       false,
	} {
		in, err := g.ParseHook("PreToolUse", []byte(`{"session_id":"s","tool_name":"`+name+`","tool_input":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		if in.IsSwarmTool != want {
			t.Errorf("IsSwarmTool(%q) = %v, want %v", name, in.IsSwarmTool, want)
		}
	}
	// A wrapped tool_input without an inner object keeps the raw input.
	in, err := g.ParseHook("PreToolUse", []byte(`{"session_id":"s","tool_name":"swarm__swarm_send","tool_input":{"tool_name":"swarm__swarm_send","tool_input":{"to":"parent"}}}`))
	if err != nil || string(in.RawToolInput) != `{"to":"parent"}` {
		t.Fatalf("RawToolInput = %s, err %v", in.RawToolInput, err)
	}
	if _, err := g.ParseHook("Stop", []byte(`{not json`)); err == nil {
		t.Error("malformed stdin must error")
	}
	if in, err := g.ParseHook("Stop", nil); err != nil || in.Event != "Stop" {
		t.Errorf("empty stdin: %+v, %v", in, err)
	}
}

// TestGrokHookOutput pins the shapes grok honoured in the live probe: deny
// (PreToolUse), Stop block, and additionalContext on the two tool hooks. The
// other events' context never reaches the model, so they print nothing.
func TestGrokHookOutput(t *testing.T) {
	g := newGrok(Deps{})
	same := func(t *testing.T, got []byte, want string) {
		t.Helper()
		var a, b any
		if err := json.Unmarshal(got, &a); err != nil {
			t.Fatalf("output %q: %v", got, err)
		}
		_ = json.Unmarshal([]byte(want), &b)
		ga, _ := json.Marshal(a)
		gb, _ := json.Marshal(b)
		if string(ga) != string(gb) {
			t.Errorf("got %s, want %s", got, want)
		}
	}

	out, err := g.HookOutput("PreToolUse", HookDecision{Block: true, Reason: "no"})
	if err != nil {
		t.Fatal(err)
	}
	same(t, out, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no"}}`)

	out, _ = g.HookOutput("Stop", HookDecision{Block: true, Reason: "mail"})
	same(t, out, `{"decision":"block","reason":"mail"}`)

	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		out, _ = g.HookOutput(ev, HookDecision{Context: "hello"})
		same(t, out, `{"hookSpecificOutput":{"hookEventName":"`+ev+`","additionalContext":"hello"}}`)
	}

	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreCompact", "PostCompact", "Stop", "Notification"} {
		out, err := g.HookOutput(ev, HookDecision{Context: "dropped", SessionTitle: "t"})
		if err != nil || out != nil {
			t.Errorf("%s context must print nothing (grok drops it), got %q, %v", ev, out, err)
		}
	}
	if out, _ := g.HookOutput("PreToolUse", HookDecision{UpdatedInput: json.RawMessage(`{"a":1}`)}); out != nil {
		t.Errorf("grok never rewrites tool input, got %q", out)
	}
	if out, _ := g.HookOutput("PostToolUse", HookDecision{}); out != nil {
		t.Errorf("empty decision prints nothing, got %q", out)
	}

	// grok caps hook context at 10,000 characters.
	out, _ = g.HookOutput("PostToolUse", HookDecision{Context: strings.Repeat("é", 20000)})
	var got struct {
		H struct {
			C string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.H.C)); n == 0 || n >= 10000 {
		t.Errorf("context runes = %d, want 1..9999", n)
	}
}

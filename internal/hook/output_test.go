package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func adapters(t *testing.T) map[runtime.AgentKind]adapter.Adapter {
	t.Helper()
	return adapter.All(adapter.Deps{Home: t.TempDir(), UserHome: t.TempDir(),
		Bin: "/usr/local/bin/swarm", Log: func(string, ...any) {}})
}

func fixture(t *testing.T, agent, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", agent, dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameJSON compares by value, so key order and whitespace do not matter.
func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got is not JSON: %s", got)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatalf("want is not JSON: %s", want)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// §11.2: each agent's output shape is pinned to the shape the CLI accepted in P0-2.
func TestHookOutputMatchesThePhase0Goldens(t *testing.T) {
	as := adapters(t)
	cases := []struct {
		agent, file, event string
		d                  adapter.HookDecision
	}{
		{"claude", "SessionStart-context.json", "SessionStart", adapter.HookDecision{Context: goldenContext(t, "claude", "SessionStart-context.json")}},
		{"claude", "UserPromptSubmit-context.json", "UserPromptSubmit", adapter.HookDecision{Context: goldenContext(t, "claude", "UserPromptSubmit-context.json")}},
		{"claude", "PreToolUse-deny.json", "PreToolUse", adapter.HookDecision{Block: true, Reason: goldenReason(t, "claude", "PreToolUse-deny.json")}},
		{"claude", "Stop-block.json", "Stop", adapter.HookDecision{Block: true, Reason: goldenReason(t, "claude", "Stop-block.json")}},
		{"codex", "SessionStart-context.json", "SessionStart", adapter.HookDecision{Context: goldenContext(t, "codex", "SessionStart-context.json")}},
		{"codex", "UserPromptSubmit-context.json", "UserPromptSubmit", adapter.HookDecision{Context: goldenContext(t, "codex", "UserPromptSubmit-context.json")}},
		{"codex", "PreToolUse-block.json", "PreToolUse", adapter.HookDecision{Block: true, Reason: goldenReason(t, "codex", "PreToolUse-block.json")}},
		{"codex", "Stop-block.json", "Stop", adapter.HookDecision{Block: true, Reason: goldenReason(t, "codex", "Stop-block.json")}},
		{"agy", "PreInvocation-ephemeral.json", "PreInvocation", adapter.HookDecision{Context: goldenEphemeral(t, "agy", "PreInvocation-ephemeral.json")}},
		{"agy", "PreToolUse-deny.json", "PreToolUse", adapter.HookDecision{Block: true, Reason: goldenReason(t, "agy", "PreToolUse-deny.json")}},
		{"agy", "Stop-continue.json", "Stop", adapter.HookDecision{Block: true, Reason: goldenReason(t, "agy", "Stop-continue.json")}},
		// cursor's three captures (P0-2). Its stop shape is the odd one across the
		// four agents — a followup_message rather than a decision — so pinning it
		// against a real capture is worth more here than anywhere else.
		{"cursor", "beforeSubmitPrompt-context.json", "beforeSubmitPrompt", adapter.HookDecision{Context: goldenContext(t, "cursor", "beforeSubmitPrompt-context.json")}},
		{"cursor", "stop-followup.json", "stop", adapter.HookDecision{Block: true, Reason: goldenReason(t, "cursor", "stop-followup.json")}},
	}
	for _, c := range cases {
		a := as[runtime.AgentKind(c.agent)]
		got, err := a.HookOutput(c.event, c.d)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.agent, c.file, err)
		}
		if want := fixture(t, c.agent, "hook-output", c.file); !sameJSON(t, got, want) {
			t.Errorf("%s/%s:\n got %s\nwant %s", c.agent, c.file, got, want)
		}
	}
}

// P0-2: the shapes the CLIs rejected must never be produced.
func TestCodexPostCompactNeverCarriesAdditionalContext(t *testing.T) {
	a := adapters(t)[runtime.Codex]
	got, err := a.HookOutput("PostCompact", adapter.HookDecision{Context: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	rejected := fixture(t, "codex", "hook-output", "PostCompact-additionalContext-REJECTED.json")
	if len(got) > 0 && sameJSON(t, got, rejected) {
		t.Fatalf("produced the rejected shape: %s", got)
	}
	uiOnly := fixture(t, "codex", "hook-output", "PostCompact-systemMessage-UI-only.json")
	if len(got) > 0 && sameJSON(t, got, uiOnly) {
		t.Fatalf("produced the UI-only shape, which never reaches the model: %s", got)
	}
}

// P0-2: userMessage works but shows as a user prompt, so it is not what we send.
func TestAgyContextIsNotAUserMessage(t *testing.T) {
	a := adapters(t)[runtime.Agy]
	got, _ := a.HookOutput("PreInvocation", adapter.HookDecision{Context: "T"})
	if sameJSON(t, got, fixture(t, "agy", "hook-output", "PreInvocation-userMessage.json")) {
		t.Fatalf("agy context must be ephemeralMessage, not userMessage: %s", got)
	}
}

func goldenContext(t *testing.T, agent, file string) string {
	t.Helper()
	var m struct {
		H struct {
			Ctx string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		Additional string `json:"additional_context"`
	}
	json.Unmarshal(fixture(t, agent, "hook-output", file), &m)
	if m.H.Ctx != "" {
		return m.H.Ctx
	}
	return m.Additional
}

func goldenReason(t *testing.T, agent, file string) string {
	t.Helper()
	var m struct {
		Reason string `json:"reason"`
		Follow string `json:"followup_message"`
		User   string `json:"user_message"`
		H      struct {
			Reason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	json.Unmarshal(fixture(t, agent, "hook-output", file), &m)
	for _, s := range []string{m.Reason, m.H.Reason, m.Follow, m.User} {
		if s != "" {
			return s
		}
	}
	t.Fatalf("%s/%s has no reason field", agent, file)
	return ""
}

func goldenEphemeral(t *testing.T, agent, file string) string {
	t.Helper()
	var m struct {
		Steps []map[string]string `json:"injectSteps"`
	}
	json.Unmarshal(fixture(t, agent, "hook-output", file), &m)
	if len(m.Steps) == 0 {
		t.Fatalf("%s/%s has no injectSteps", agent, file)
	}
	return m.Steps[0]["ephemeralMessage"]
}

// P0-2 captured cursor's deny with two different strings; Swarm sends its single
// reason in both fields. So this pins the key set and the decision, not the text.
func TestCursorDenyMatchesTheCapturedShape(t *testing.T) {
	want := fixture(t, "cursor", "hook-output", "preToolUse-deny.json")
	got, err := adapters(t)[runtime.Cursor].HookOutput("preToolUse",
		adapter.HookDecision{Block: true, Reason: "R"})
	if err != nil {
		t.Fatal(err)
	}
	var wantMap, gotMap map[string]any
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("output is not JSON: %s", got)
	}
	if len(gotMap) != len(wantMap) {
		t.Fatalf("key count %d, capture has %d: %s", len(gotMap), len(wantMap), got)
	}
	for k := range wantMap {
		if _, ok := gotMap[k]; !ok {
			t.Errorf("missing key %q the CLI was given: %s", k, got)
		}
	}
	if gotMap["permission"] != "deny" {
		t.Errorf("permission = %v", gotMap["permission"])
	}
	if gotMap["user_message"] != "R" || gotMap["agent_message"] != "R" {
		t.Errorf("both message fields carry the reason: %s", got)
	}
}

// §23.3: ParseHook reads every saved hook input and finds the provider session id.
func TestParseHookReadsEverySavedInput(t *testing.T) {
	as := adapters(t)
	for _, agent := range []string{"claude", "codex", "agy", "cursor"} {
		dir := filepath.Join("testdata", agent, "hook-stdin")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Fatalf("no saved hook inputs for %s", agent)
		}
		for _, e := range entries {
			event := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".json"), "-2")
			in, err := as[runtime.AgentKind(agent)].ParseHook(event, fixture(t, agent, "hook-stdin", e.Name()))
			if err != nil {
				t.Errorf("%s/%s: %v", agent, e.Name(), err)
				continue
			}
			if in.ProviderSessionID == "" {
				t.Errorf("%s/%s: no provider session id in %+v", agent, e.Name(), in)
			}
		}
	}
}

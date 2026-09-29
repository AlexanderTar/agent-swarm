package hook

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

var (
	finishQ    = `Finish TASK-101 "Build the login form"? Not pushed.`
	finishOpts = []string{"Create PR, auto-merge when checks pass", "Create PR, I'll merge it myself", "Request changes"}
	finishDesc = []string{"Push and open a PR that merges itself.", "Push and open a PR for you to merge.", "Send it back with notes."}
)

// seedFinish opens an accept_fix request whose native prompt is already frozen.
func seedFinish(t *testing.T, h *Handler) {
	t.Helper()
	binding, _ := json.Marshal(map[string]any{"question": finishQ, "header": "Finish fix",
		"options": finishOpts, "descriptions": finishDesc})
	if _, err := h.DB.ExecContext(context.Background(), `INSERT INTO requests
		(id, kind, is_hitl, agent_id, session_id, item_id, prompt, options_json, binding_json, state, created_at)
		VALUES ('req_fin1', 'accept_fix', 0, 'agt_1', 'ses_1', 'itm_1', '', '[]', ?, 'open', 1)`, string(binding)); err != nil {
		t.Fatal(err)
	}
}

func claudeAsk(t *testing.T, h *Handler, ses, question string, options []map[string]any) []byte {
	t.Helper()
	in, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"metadata": map[string]any{"source": "x"}, "questions": []map[string]any{
			{"question": question, "header": "Finish", "multiSelect": false, "options": options}}}})
	out, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses, in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func storedClaudeOptions() []map[string]any {
	var o []map[string]any
	for i, l := range finishOpts {
		o = append(o, map[string]any{"label": l, "description": finishDesc[i]})
	}
	return o
}

func TestClaudeRewritesInventedOptionsToTheStoredOnes(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	seedFinish(t, h)
	invented := []map[string]any{
		{"label": "auto_merge", "description": "made up"}, {"label": "manual_merge", "description": "made up"},
		{"label": "merge_locally", "description": "made up"}, {"label": "request_changes", "description": "made up"}}
	out := claudeAsk(t, h, ses, finishQ, invented)
	var got struct {
		HSO struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Updated  struct {
				Metadata  map[string]any `json:"metadata"`
				Questions []struct {
					Question    string           `json:"question"`
					Header      string           `json:"header"`
					MultiSelect bool             `json:"multiSelect"`
					Options     []map[string]any `json:"options"`
				} `json:"questions"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	q := got.HSO.Updated.Questions
	if got.HSO.Event != "PreToolUse" || got.HSO.Decision != "ask" || len(q) != 1 {
		t.Fatalf("output = %s", out)
	}
	if q[0].Question != finishQ || q[0].Header != "Finish fix" || q[0].MultiSelect || !reflect.DeepEqual(q[0].Options, storedClaudeOptions()) {
		t.Fatalf("questions[0] = %+v", q[0])
	}
	if got.HSO.Updated.Metadata["source"] != "x" {
		t.Fatalf("updatedInput must be the full tool_input, got %s", out)
	}
	if strings.Contains(string(out), `"allow"`) {
		t.Fatalf("must never allow: %s", out)
	}
}

func TestClaudeDoesNotRewriteWhenOptionsAlreadyMatch(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	seedFinish(t, h)
	if out := claudeAsk(t, h, ses, finishQ, storedClaudeOptions()); len(out) != 0 {
		t.Fatalf("equal options must pass untouched, got %s", out)
	}
}

func TestClaudeUnboundQuestionPassesUntouched(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	seedFinish(t, h)
	out := claudeAsk(t, h, ses, "Which database?", []map[string]any{{"label": "pg"}, {"label": "sqlite"}})
	if len(out) != 0 {
		t.Fatalf("an unbound question must pass untouched, got %s", out)
	}
}

func codexAsk(t *testing.T, h *Handler, ses string, options []map[string]any) []byte {
	t.Helper()
	in, _ := json.Marshal(map[string]any{"session_id": codexProvider, "hook_event_name": "PreToolUse",
		"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []map[string]any{
			{"header": "Finish fix", "question": finishQ, "options": options}}}})
	return codexHook(t, h, ses, "PreToolUse", in)
}

func TestCodexDeniesDifferingLabelsAndAllowsEqual(t *testing.T) {
	h, ses := codexSeed(t)
	seedFinish(t, h)
	out := codexAsk(t, h, ses, []map[string]any{{"label": "auto_merge"}, {"label": "request_changes"}})
	want := "[swarm] Use native_prompt's options word for word: " + strings.Join(finishOpts, " / ") +
		". Ask again with exactly those labels."
	if !strings.Contains(string(out), `"decision":"block"`) || !strings.Contains(string(out), want) {
		t.Fatalf("want deny with %q, got %s", want, out)
	}
	var opts []map[string]any
	for i, l := range finishOpts {
		opts = append(opts, map[string]any{"label": l, "description": finishDesc[i]})
	}
	if out := codexAsk(t, h, ses, opts); len(out) != 0 {
		t.Fatalf("equal labels must be allowed, got %s", out)
	}
}

// A native_prompt whose options are plain strings has no descriptions; Claude's
// AskUserQuestion schema still requires a description on every option, so the
// rewrite must emit one (the agent's own when it gave one for that label).
func TestClaudeRewriteKeepsDescriptionFieldForPlainStringOptions(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	binding, _ := json.Marshal(map[string]any{"question": finishQ, "header": "Approve",
		"options": []string{"Approve", "Request changes"}})
	if _, err := h.DB.ExecContext(context.Background(), `INSERT INTO requests
		(id, kind, is_hitl, agent_id, session_id, item_id, prompt, options_json, binding_json, state, created_at)
		VALUES ('req_pl1', 'accept_fix', 0, 'agt_1', 'ses_1', 'itm_1', '', '[]', ?, 'open', 1)`, string(binding)); err != nil {
		t.Fatal(err)
	}
	out := claudeAsk(t, h, ses, finishQ, []map[string]any{
		{"label": "approve", "description": "invented"}, {"label": "Request changes", "description": "Send it back"}})
	var got struct {
		HSO struct {
			Updated struct {
				Questions []struct {
					Options []map[string]any `json:"options"`
				} `json:"questions"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	opts := got.HSO.Updated.Questions[0].Options
	want := []map[string]any{{"label": "Approve", "description": ""}, {"label": "Request changes", "description": "Send it back"}}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %v, want %v", opts, want)
	}
}

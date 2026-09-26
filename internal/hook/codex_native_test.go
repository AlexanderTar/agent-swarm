package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Fixtures: testdata/codex/native-question, captured from codex 0.157 by an
// isolated probe on 2026-09-26 (see docs/specs/2026-09-26-codex-native-approval.md).
// UserPromptSubmit-question-reply.synthesized.json is NOT a capture: it is the
// typed fixture with its prompt replaced by the live-reported wrapper shape.
const (
	probeQuestion = "Approve probe? ⟦swarm:req_01PROBE0000000000000000000⟧"
	probeRef      = "req_01PROBE0000000000000000000"
	codexProvider = "01a0df19-71ec-7321-b37d-ada707746474"
)

func codexSeed(t *testing.T) (*Handler, string) {
	t.Helper()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(context.Background(), `UPDATE agents SET kind = 'codex' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	return h, ses
}

func codexHook(t *testing.T, h *Handler, ses, event string, stdin []byte) []byte {
	t.Helper()
	out, err := h.Handle(context.Background(), runtime.Codex, event, ses, stdin)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func nativeFixture(t *testing.T, name string) []byte {
	t.Helper()
	return fixture(t, "codex", "native-question", name)
}

type qrow struct {
	ID, State, Prompt, Options, Ref string
	Response, Via                   sql.NullString
}

// questionRow reads the one question row with this exact prompt; it fails
// the test when there is none.
func questionRow(t *testing.T, h *Handler, prompt string) qrow {
	t.Helper()
	var r qrow
	if err := h.DB.QueryRowContext(context.Background(), `SELECT id, state, prompt, COALESCE(options_json, '[]'),
		COALESCE(json_extract(binding_json, '$.ref'), ''), response_text, responded_via
		FROM requests WHERE kind = 'question' AND prompt = ?`, prompt).
		Scan(&r.ID, &r.State, &r.Prompt, &r.Options, &r.Ref, &r.Response, &r.Via); err != nil {
		t.Fatalf("question row %q: %v", prompt, err)
	}
	return r
}

// codexPreToolUse is a request_user_input_async PreToolUse payload in the
// captured shape (questions[].title, string options).
func codexPreToolUse(questions ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"session_id": codexProvider, "hook_event_name": "PreToolUse",
		"tool_name": "request_user_input_async", "tool_input": map[string]any{"questions": questions},
		"tool_use_id": "call_test"})
	return b
}

func codexPrompt(prompt string) []byte {
	b, _ := json.Marshal(map[string]any{"session_id": codexProvider, "hook_event_name": "UserPromptSubmit",
		"prompt": prompt})
	return b
}

func questionReplyPrompt(entries ...map[string]string) string {
	b, _ := json.Marshal(entries)
	return "<send_user_message_question_reply>" + string(b) + "</send_user_message_question_reply>"
}

// S1
func TestCodexPreToolUseFixtureOpensARefBoundRow(t *testing.T) {
	h, ses := codexSeed(t)
	if out := codexHook(t, h, ses, "PreToolUse", nativeFixture(t, "PreToolUse.json")); len(out) != 0 {
		t.Fatalf("the question tool must not be blocked, got %s", out)
	}
	r := questionRow(t, h, probeQuestion)
	var opts []string
	if err := json.Unmarshal([]byte(r.Options), &opts); err != nil {
		t.Fatal(err)
	}
	if r.State != "open" || r.Ref != probeRef || strings.Join(opts, "|") != "Approve|Request changes" {
		t.Fatalf("row = %+v options %v, want open, ref %s, Approve|Request changes", r, opts, probeRef)
	}
}

// S2
func TestCodexBatchedTitleQuestionsWithSwarmRefAreDenied(t *testing.T) {
	h, ses := codexSeed(t)
	out := codexHook(t, h, ses, "PreToolUse", codexPreToolUse(
		map[string]any{"title": "Pick a color", "options": []string{"Red", "Blue"}},
		map[string]any{"title": probeQuestion, "options": []string{"Approve", "Request changes"}}))
	if !strings.Contains(string(out), "[swarm] Ask one swarm approval per question call.") {
		t.Fatalf("a batched call carrying a swarm ref must be denied, got %s", out)
	}
	var n int
	if err := h.DB.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}

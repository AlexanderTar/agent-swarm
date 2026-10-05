package adapter

import "testing"

// Hook-field kinds report the live model on stdin; ParseHook must carry it
// through so the runtime can record in-session model changes.
func TestParseHookCarriesObservedModel(t *testing.T) {
	cases := []struct {
		name  string
		parse func(string, []byte) (HookInput, error)
		event string
		stdin string
		want  string
	}{
		{"codex", (&Codex{}).ParseHook, "UserPromptSubmit", `{"session_id":"s","model":"gpt-5.5"}`, "gpt-5.5"},
		{"agy", (&Agy{}).ParseHook, "Stop", `{"conversationId":"c","modelName":"gemini-3.8-flash-low"}`, "gemini-3.8-flash-low"},
		{"cursor", (&Cursor{}).ParseHook, "stop", `{"conversation_id":"c","model":"default"}`, "default"},
		{"claude", (&Claude{}).ParseHook, "SessionStart", `{"session_id":"s","model":"claude-haiku-4-5-20251001"}`, "claude-haiku-4-5-20251001"},
	}
	for _, c := range cases {
		in, err := c.parse(c.event, []byte(c.stdin))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if in.Model != c.want {
			t.Errorf("%s: Model = %q, want %q", c.name, in.Model, c.want)
		}
		if in.Effort != "" {
			t.Errorf("%s: Effort = %q, want empty (slug effort is split by the runtime)", c.name, in.Effort)
		}
	}
}

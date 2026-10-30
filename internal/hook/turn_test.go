package hook

import (
	"context"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/runtime"
)

// Hook events open and close the turn. A blocked Stop keeps it open, a blocked
// PreToolUse does not open a closed turn, cursor's beforeSubmitPrompt is not a
// turn start, and grok's shutdown Stop changes nothing. The verdict is on the
// session row, so a new Store sees it.
func TestHookEventsOpenAndCloseTheTurn(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	if got := h.RT.HookTurn(ses); got.Known {
		t.Fatalf("before any hook: %+v, want no turn state", got)
	}

	step := func(kind runtime.AgentKind, ev, body string, wantOpen bool, wantClose string) {
		t.Helper()
		if _, err := h.Handle(ctx, kind, ev, ses, []byte(body)); err != nil {
			t.Fatalf("%s %s: %v", kind, ev, err)
		}
		got := h.RT.HookTurn(ses)
		if !got.Known || got.Open != wantOpen || got.CloseEvent != wantClose {
			t.Fatalf("after %s %s: %+v, want known open=%v close=%q", kind, ev, got, wantOpen, wantClose)
		}
	}

	step(runtime.Claude, "SessionStart", `{"session_id":"p1","source":"startup"}`, true, "")
	step(runtime.Claude, "Stop", `{}`, false, "Stop")
	step(runtime.Claude, "UserPromptSubmit", `{"session_id":"p1","prompt":"hi"}`, true, "")
	// Already open: a blocked tool call must not close it.
	step(runtime.Claude, "PreToolUse", `{"session_id":"p1","tool_name":"Workflow","tool_input":{}}`, true, "")
	step(runtime.Claude, "Stop", `{}`, false, "Stop")
	// A blocked PreToolUse never ran, so it does not open a closed turn.
	step(runtime.Claude, "PreToolUse", `{"session_id":"p1","tool_name":"Workflow","tool_input":{}}`, false, "Stop")
	step(runtime.Claude, "PreToolUse", `{"session_id":"p1","tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}`, true, "")

	if _, err := h.DB.ExecContext(ctx, `INSERT INTO messages
		(id,seq,kind,wake_class,priority,origin,to_agent_id,root_item_id,payload_json,state,created_at)
		VALUES ('msg_block',1,'finding','immediate',1,'daemon','agt_1','itm_1','{"body":"x"}','pending',1)`); err != nil {
		t.Fatal(err)
	}
	// Pending mail blocks Stop; the turn continues.
	step(runtime.Claude, "Stop", `{}`, true, "")

	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'grok' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	// grok's shutdown Stop is a process exit, not the end of the turn.
	step(runtime.Grok, "Stop", `{"session_id":"p1","reason":"shutdown"}`, true, "")

	if _, err := h.DB.ExecContext(ctx, `DELETE FROM messages WHERE to_agent_id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET stop_blocks = 0 WHERE id = ?`, ses); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'cursor' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	step(runtime.Cursor, "stop", `{}`, false, "Stop")
	// beforeSubmitPrompt fires while a tool still runs; it is not a new turn.
	step(runtime.Cursor, "beforeSubmitPrompt", `{"prompt":"queued follow-up"}`, false, "Stop")

	fresh := &runtime.Store{DB: h.DB, Now: now, Log: func(string, ...any) {}}
	if got := fresh.HookTurn(ses); got.Open || !got.Known || got.CloseEvent != "Stop" {
		t.Fatalf("after a new Store: %+v, want the closed Stop that was written", got)
	}
}

package hook

import (
	"context"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func agentChangedCount(t *testing.T, h *Handler) int {
	t.Helper()
	evs, _ := h.RT.Events.After(context.Background(), 0, 10000)
	n := 0
	for _, e := range evs {
		if e.Type == events.AgentChanged {
			n++
		}
	}
	return n
}

func agentModelEffort(t *testing.T, h *Handler) (model, effort string) {
	t.Helper()
	if err := h.DB.QueryRowContext(context.Background(),
		`SELECT model, COALESCE(effort,'') FROM agents WHERE id = 'agt_1'`).Scan(&model, &effort); err != nil {
		t.Fatal(err)
	}
	return
}

func TestHookRecordsObservedModelOncePerChange(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()
	h.RT.Catalog = &catalog.Service{DB: h.DB, Events: h.RT.Events, Now: now, Log: func(string, ...any) {}}
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('agy','agy-1','[{"id":"gemini-3.8-flash","label":"Flash","efforts":["medium","high"],"default_effort":"high","effort_encoding":"slug","launch_ids":{"medium":"gemini-3.8-flash-medium","high":"gemini-3.8-flash-high"},"advisor_capable":false}]','gemini-3.8-flash','test',1,1)`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		kind          runtime.AgentKind
		event, stdin  string
		model, effort string
	}{
		{runtime.Codex, "UserPromptSubmit", `{"session_id":"p1","model":"gpt-5.5"}`, "gpt-5.5", ""},
		{runtime.Agy, "PreInvocation", `{"conversationId":"c1","modelName":"gemini-3.8-flash-medium"}`, "gemini-3.8-flash", "medium"},
		{runtime.Cursor, "beforeSubmitPrompt", `{"conversation_id":"c1","model":"default"}`, "default", ""},
	}
	for _, c := range cases {
		if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = ?, model = 'old', effort = NULL WHERE id = 'agt_1'`, string(c.kind)); err != nil {
			t.Fatal(err)
		}
		before := agentChangedCount(t, h)
		if _, err := h.Handle(ctx, c.kind, c.event, ses, []byte(c.stdin)); err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if m, e := agentModelEffort(t, h); m != c.model || e != c.effort {
			t.Errorf("%s: row = %q/%q, want %q/%q", c.kind, m, e, c.model, c.effort)
		}
		if n := agentChangedCount(t, h); n != before+1 {
			t.Errorf("%s: agent.changed %d -> %d, want exactly one", c.kind, before, n)
		}
		// repeated identical events publish nothing
		for i := 0; i < 2; i++ {
			if _, err := h.Handle(ctx, c.kind, c.event, ses, []byte(c.stdin)); err != nil {
				t.Fatal(err)
			}
		}
		if n := agentChangedCount(t, h); n != before+1 {
			t.Errorf("%s: repeat published: %d, want %d", c.kind, n, before+1)
		}
	}
}

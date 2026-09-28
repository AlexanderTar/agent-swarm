package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func TestSyncToolCarriesTodosOnlyWhenChanged(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	todos, ok := out.(map[string]any)["todos"].([]runtime.Todo)
	if !ok || len(todos) != 4 { // two tasks + integrate + accept
		t.Fatalf("todos = %s", mustJSON(out))
	}
	out, err = s.call(ctx, seed.Caller, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := out.(map[string]any)["todos"]; present {
		t.Fatalf("unchanged todos resent: %s", mustJSON(out))
	}
}

func TestCheckpointToolPassesSpikeTodosThrough(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	_, a, _, err := s.RT.StartSpike(ctx, runtime.SpikeInput{Name: "Plan it", Intent: "feature", Kind: runtime.Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := Caller{SessionID: ses.ID, AgentID: a.ID, AgentName: a.Name, Role: runtime.RoleOrchestrator}
	if _, err := s.call(ctx, c, "swarm_checkpoint",
		`{"kind":"progress","summary":"framing","todos":[{"id":"frame","status":"in_progress"}]}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.call(ctx, c, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	todos := out.(map[string]any)["todos"].([]runtime.Todo)
	if todos[0].ID != "frame" || todos[0].Status != runtime.TodoInProgress {
		t.Fatalf("todos = %+v", todos)
	}
	_, err = s.call(ctx, c, "swarm_checkpoint", `{"kind":"progress","summary":"x","todos":[{"id":"nope","status":"pending"}]}`)
	if err == nil || !strings.Contains(err.Error(), `todos: unknown step "nope"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckpointToolReportsTodosIgnoredForAWorker(t *testing.T) {
	s, seed := newServerWithSession(t) // a coder
	out, err := s.call(context.Background(), seed.Caller, "swarm_checkpoint",
		`{"kind":"progress","summary":"x","todos":[{"id":"frame","status":"completed"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["todos_ignored"]; got != "only the orchestrator keeps a task list" {
		t.Fatalf("todos_ignored = %v", got)
	}
}

func TestCheckpointSchemaDeclaresTodos(t *testing.T) {
	s := newTestServer(t)
	for _, d := range s.Tools() {
		if d.Name == "swarm_checkpoint" && !strings.Contains(string(d.Schema), `"todos":{"type":"array"`) {
			t.Fatalf("schema = %s", d.Schema)
		}
	}
}

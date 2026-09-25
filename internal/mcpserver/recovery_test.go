package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// TestSpawnBriefNestedSchema pins the swarm_spawn brief contract: brief is
// a nested object schema (objective, acceptance, scope_in, scope_out,
// context, verify, stop_when), not an opaque object, so callers and
// validators see the real shape.
func TestSpawnBriefNestedSchema(t *testing.T) {
	s := newTestServer(t)
	var schema json.RawMessage
	for _, d := range s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleOrchestrator}) {
		if d.Name == "swarm_spawn" {
			schema = d.Schema
		}
	}
	if len(schema) == 0 {
		t.Fatal("swarm_spawn not in orchestrator tools")
	}
	var parsed struct {
		Properties map[string]struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("spawn schema is not JSON: %v", err)
	}
	brief, ok := parsed.Properties["brief"]
	if !ok {
		t.Fatal("spawn schema has no brief property")
	}
	if brief.Type != "object" {
		t.Fatalf("brief type = %q, want object", brief.Type)
	}
	for _, want := range []string{"objective", "acceptance", "scope_in", "scope_out", "context", "verify", "stop_when"} {
		prop, ok := brief.Properties[want]
		if !ok {
			t.Fatalf("brief schema missing nested property %q", want)
		}
		if prop.Type == "" {
			t.Fatalf("brief.%s has no type", want)
		}
	}
	if !strings.Contains(string(schema), "scope_in") {
		t.Fatal("spawn schema text omits scope_in")
	}
}

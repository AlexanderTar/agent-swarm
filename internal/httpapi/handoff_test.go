package httpapi

import (
	"encoding/json"
	"testing"
)

// Batch 3: POST /api/agents/{name}/handoff with {request_id} returns 202
// plus a ReplacementResult; GET /api/agents/{name}/replacement reports the
// in-flight operation and 404s when none exists.
func TestHandoffReturns202WithReplacementResult(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/task-worker/handoff", `{"request_id":"h1"}`)
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		OperationID string `json:"operation_id"`
		Agent       string `json:"agent"`
		Mode        string `json:"mode"`
		Phase       string `json:"phase"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OperationID == "" || body.Agent != "task-worker" || body.Mode != "handoff" || body.Phase == "" {
		t.Fatalf("replacement = %+v, want operation_id/agent/mode/phase", body)
	}
	if body.Phase == "completed" {
		t.Fatalf("phase = completed on a 202 accept; the successor walk is asynchronous")
	}
}

func TestHandoffUnknownAgentIs404(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/no-such-agent/handoff", `{"request_id":"h1"}`)
	if rec.Code != 404 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestHandoffReplayReturnsSameOperation(t *testing.T) {
	s, _ := newRuntimeServer(t)
	first := s.post(t, "/api/agents/task-worker/handoff", `{"request_id":"same"}`)
	second := s.post(t, "/api/agents/task-worker/handoff", `{"request_id":"same"}`)
	if first.Code != 202 || second.Code != 202 {
		t.Fatalf("status = %d/%d: %s / %s", first.Code, second.Code, first.Body, second.Body)
	}
	var a, b struct {
		OperationID string `json:"operation_id"`
	}
	json.Unmarshal(first.Body.Bytes(), &a)
	json.Unmarshal(second.Body.Bytes(), &b)
	if a.OperationID == "" || a.OperationID != b.OperationID {
		t.Fatalf("replay operation = %q vs %q, want the same id", a.OperationID, b.OperationID)
	}
}

func TestReplacementAbsentIs404ThenPresent(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.get(t, "/api/agents/task-worker/replacement")
	if rec.Code != 404 {
		t.Fatalf("absent status = %d: %s", rec.Code, rec.Body)
	}
	s.post(t, "/api/agents/task-worker/handoff", `{"request_id":"h1"}`)
	// The handoff walks its phases synchronously and may already be
	// terminal; pin a fresh in-flight operation directly so the read path
	// is deterministic.
	if _, err := s.DB.ExecContext(bg, `DELETE FROM agent_operations WHERE agent_id =
		(SELECT id FROM agents WHERE name = 'task-worker')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(bg, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, created_at, updated_at)
		SELECT 'op_pinned', id, 'handoff', 'stopping', 'k1', 1, 1 FROM agents WHERE name = 'task-worker'`); err != nil {
		t.Fatal(err)
	}
	present := s.get(t, "/api/agents/task-worker/replacement")
	if present.Code != 200 {
		t.Fatalf("present status = %d: %s", present.Code, present.Body)
	}
	var body struct {
		OperationID string `json:"operation_id"`
		Phase       string `json:"phase"`
	}
	json.Unmarshal(present.Body.Bytes(), &body)
	if body.OperationID != "op_pinned" || body.Phase != "stopping" {
		t.Fatalf("replacement = %+v, want op_pinned/stopping", body)
	}
}

// Batch 3: the state wire carries the optional in-flight replacement on the
// node, reusing the existing effort field (no new effort surface).
func TestStateNodeCarriesReplacementWhileInFlight(t *testing.T) {
	s, _ := newRuntimeServer(t)
	if _, err := s.DB.ExecContext(bg, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		SELECT 'op_state1', id, 'handoff', 'stopping', 'k1', '', 1, 1, 1 FROM agents WHERE name = 'task-worker'`); err != nil {
		t.Fatal(err)
	}
	rec := s.get(t, "/api/state")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Agents []map[string]any `json:"agents"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	var walk func(nodes []map[string]any) map[string]any
	walk = func(nodes []map[string]any) map[string]any {
		for _, n := range nodes {
			if n["name"] == "task-worker" {
				return n
			}
			for _, key := range []string{"children", "finished"} {
				if kids, ok := n[key].([]any); ok {
					sub := make([]map[string]any, 0, len(kids))
					for _, k := range kids {
						if m, ok := k.(map[string]any); ok {
							sub = append(sub, m)
						}
					}
					if found := walk(sub); found != nil {
						return found
					}
				}
			}
		}
		return nil
	}
	found := walk(body.Agents)
	if found == nil {
		t.Fatal("task-worker missing from state")
	}
	rep, ok := found["replacement"].(map[string]any)
	if !ok || rep["operation_id"] != "op_state1" {
		t.Fatalf("task-worker replacement = %v, want op_state1", found["replacement"])
	}
}

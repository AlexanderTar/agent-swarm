package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
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

// Spec 2026-09-27-single-agent-limit-live: a capacity-keyed operation
// carries reason "capacity" on the state node; any other key omits it.
func TestStateNodeReplacementCarriesCapacityReason(t *testing.T) {
	s, _ := newRuntimeServer(t)
	if _, err := s.DB.ExecContext(bg, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		SELECT 'op_cap', id, 'handoff', 'queued', 'capacity:ses_x', '', 1, 1, 1 FROM agents WHERE name = 'task-worker'`); err != nil {
		t.Fatal(err)
	}
	rec := s.get(t, "/api/agents/task-worker/replacement")
	var body struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || body.Reason != "capacity" {
		t.Fatalf("replacement = %d %s, want reason capacity", rec.Code, rec.Body)
	}
	if _, err := s.DB.ExecContext(bg, `UPDATE agent_operations SET request_key = 'h1' WHERE id = 'op_cap'`); err != nil {
		t.Fatal(err)
	}
	rec = s.get(t, "/api/agents/task-worker/replacement")
	if strings.Contains(rec.Body.String(), `"reason"`) {
		t.Fatalf("plain handoff must omit reason: %s", rec.Body)
	}
}

func TestHandoffWithSwitchIs202(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"s1","agent":"fake","model":"fake-1","advisor":"none"}`)
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var sw string
	if err := s.DB.QueryRowContext(bg, `SELECT switch_json FROM agent_operations WHERE request_key = 's1'`).Scan(&sw); err != nil || sw == "" {
		t.Fatalf("switch_json = %q (%v), want the stored switch", sw, err)
	}
}

func TestHandoffSwitchFieldsWithoutAgentIs400(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"s2","model":"x"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 400, "bad_request", "Choose an agent.")
}

func TestHandoffSwitchOnWorkerIs400(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/task-worker/handoff", `{"request_id":"s3","agent":"fake","model":"fake-1"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 400, "bad_request", "Only an orchestrator can switch agents on handoff.")
}

func TestHandoffSwitchBadModelIs422(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"s4","agent":"fake","model":"nope"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 422, "preflight_failed", "")
}

func handoffOverrides(t *testing.T, s *runtimeEnv, name string) map[string]map[string]string {
	t.Helper()
	var raw string
	if err := s.DB.QueryRowContext(bg, `SELECT COALESCE(role_overrides, '') FROM agents WHERE name = ?`, name).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestHandoffRolesPersistWorkerOverrides(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"r1","roles":{"coder":{"agent":"fake","model":"fake-1","effort":""}}}`)
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	got := handoffOverrides(t, s, "root-orchestrator")
	if got["coder"]["agent"] != "fake" || got["coder"]["model"] != "fake-1" {
		t.Fatalf("role_overrides = %v, want coder=fake/fake-1", got)
	}
}

func TestHandoffAbsentRolesLeavesOverridesUnchanged(t *testing.T) {
	s, _ := newRuntimeServer(t)
	if rec := s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"r2","roles":{"coder":{"agent":"fake","model":"fake-1","effort":""}}}`); rec.Code != 202 {
		t.Fatalf("seed status = %d: %s", rec.Code, rec.Body)
	}
	if rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"r3"}`); rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := handoffOverrides(t, s, "root-orchestrator"); got["coder"]["model"] != "fake-1" {
		t.Fatalf("role_overrides = %v, want coder kept", got)
	}
}

func TestHandoffRolesRejectInvalid(t *testing.T) {
	for name, roles := range map[string]string{
		"orchestrator role": `{"orchestrator":{"agent":"fake","model":"fake-1","effort":""}}`,
		"advisor role":      `{"advisor":{"agent":"fake","model":"fake-1","effort":""}}`,
		"unknown role":      `{"nope":{"agent":"fake","model":"fake-1","effort":""}}`,
		"bad agent":         `{"coder":{"agent":"zzz","model":"fake-1","effort":""}}`,
		"bad model":         `{"coder":{"agent":"fake","model":"nope","effort":""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newRuntimeServer(t)
			rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"bad","roles":`+roles+`}`)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status = %d: %s, want 4xx", rec.Code, rec.Body)
			}
			if got := handoffOverrides(t, s, "root-orchestrator"); len(got) != 0 {
				t.Fatalf("role_overrides = %v, want none persisted", got)
			}
			var n int
			_ = s.DB.QueryRowContext(bg, `SELECT COUNT(*) FROM agent_operations WHERE request_key = 'bad'`).Scan(&n)
			if n != 0 {
				t.Fatalf("handoff operation recorded despite invalid roles")
			}
		})
	}
}

func TestHandoffRolesNotPersistedWhenHandoffRejected(t *testing.T) {
	s, _ := newRuntimeServer(t)
	// Valid roles, but a model without an agent is a 400 before any op runs.
	rec := s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"atom1","model":"fake-1","roles":{"coder":{"agent":"fake","model":"fake-1","effort":""}}}`)
	if rec.Code != 400 {
		t.Fatalf("status = %d: %s, want 400", rec.Code, rec.Body)
	}
	if got := handoffOverrides(t, s, "root-orchestrator"); len(got) != 0 {
		t.Fatalf("role_overrides = %v, want none persisted after a rejected hand-off", got)
	}
	// Preflight failure (422) is atomic too.
	rec = s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"atom2","agent":"fake","model":"nope","roles":{"coder":{"agent":"fake","model":"fake-1","effort":""}}}`)
	if rec.Code != 422 {
		t.Fatalf("status = %d: %s, want 422", rec.Code, rec.Body)
	}
	if got := handoffOverrides(t, s, "root-orchestrator"); len(got) != 0 {
		t.Fatalf("role_overrides = %v, want none persisted after preflight failure", got)
	}
}

func TestSetWorkerRoleOverridesRejectsNonOrchestrator(t *testing.T) {
	s, _ := newRuntimeServer(t)
	a, err := s.RT.Agent(bg, "task-worker") // the seeded coder
	if err != nil {
		t.Fatal(err)
	}
	id := a.ID
	err = s.RT.SetWorkerRoleOverrides(bg, id, map[runtime.Role]settings.RoleDefault{
		runtime.RoleCoder: {Agent: "fake", Model: "fake-1"}})
	if err == nil {
		t.Fatal("want error for a non-orchestrator agent")
	}
	if got := handoffOverrides(t, s, "task-worker"); len(got) != 0 {
		t.Fatalf("role_overrides = %v, want none", got)
	}
}

// The Orchestrate task window prefills its Worker overrides from the
// orchestrator's stored role_overrides, so the state node must carry them.
func TestStateNodeCarriesRoleOverrides(t *testing.T) {
	s, _ := newRuntimeServer(t)
	if rec := s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"so1","roles":{"coder":{"agent":"fake","model":"fake-1","effort":""}}}`); rec.Code != 202 {
		t.Fatalf("seed status = %d: %s", rec.Code, rec.Body)
	}
	rec := s.get(t, "/api/state")
	var body struct {
		Agents []struct {
			Name          string                       `json:"name"`
			RoleOverrides map[string]map[string]string `json:"role_overrides"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, a := range body.Agents {
		if a.Name == "root-orchestrator" {
			if a.RoleOverrides["coder"]["model"] != "fake-1" {
				t.Fatalf("role_overrides = %v, want coder=fake-1", a.RoleOverrides)
			}
			return
		}
	}
	t.Fatal("root-orchestrator missing from state")
}

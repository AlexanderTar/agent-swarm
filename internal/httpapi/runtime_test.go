package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// `net/http` and `net/http/httptest` are not needed here: every request goes
// through the harness in helpers_test.go, which owns the transport.

// contracts §3.2: AgentNode's full key set, with the right null and [] shapes.
func TestAgentNodeWireShape(t *testing.T) {
	s, seed := newRuntimeServer(t)
	rec := s.get(t, "/api/agents?state=all")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatal("no agents returned")
	}
	n := nodes[0]
	for _, k := range []string{"id", "name", "kind", "model", "effort", "role", "item_key",
		"item_title", "root_key", "parent_name", "advisor", "state", "session",
		"preflight_error", "created_at", "finished_at", "children", "finished"} {
		if _, ok := n[k]; !ok {
			t.Errorf("AgentNode is missing %q: %s", k, rec.Body)
		}
	}
	if n["children"] == nil || n["finished"] == nil {
		t.Error("arrays are never null (W4)")
	}
	ses, ok := n["session"].(map[string]any)
	if !ok {
		t.Fatalf("session = %v", n["session"])
	}
	for _, k := range []string{"id", "state", "attempt", "generation", "waiting", "stale",
		"tmux_alive", "started_at", "ended_at"} {
		if _, ok := ses[k]; !ok {
			t.Errorf("SessionInfo is missing %q", k)
		}
	}
	if _, isNumber := ses["started_at"].(float64); !isNumber {
		t.Errorf("started_at must be integer ms (W2), got %T", ses["started_at"])
	}
	_ = seed
}

// contracts §3.2: preflight_error is non-null exactly when there is no session.
func TestPreflightFailureShape(t *testing.T) {
	s, _ := newRuntimeServerWithPreflightFailure(t)
	rec := s.get(t, "/api/agents?state=all")
	var nodes []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &nodes)
	var found bool
	for _, n := range nodes {
		if n["preflight_error"] != nil {
			found = true
			if n["session"] != nil {
				t.Error(`"failed at preflight" means session === null && preflight_error !== null`)
			}
		}
	}
	if !found {
		t.Fatal("no agent with a preflight error")
	}
}

// contracts §4: /api/state is the menubar snapshot.
func TestStateSnapshot(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.get(t, "/api/state")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"agents", "requests", "notifications", "usage", "active_count", "settings"} {
		if _, ok := body[k]; !ok {
			t.Errorf("/api/state is missing %q: %s", k, rec.Body)
		}
	}
	ntf := body["notifications"].(map[string]any)
	if _, ok := ntf["unread"]; !ok {
		t.Error("notifications.unread is required")
	}
	if _, ok := ntf["items"]; !ok {
		t.Error("notifications.items is required")
	}
	if items := ntf["items"].([]any); len(items) > 20 {
		t.Fatalf("notifications.items holds at most 20, got %d", len(items))
	}
}

func TestStateIsEmptyButWellShapedOnAFreshDaemon(t *testing.T) {
	s := newEmptyServer(t)
	rec := s.get(t, "/api/state")
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["agents"] == nil || body["requests"] == nil || body["usage"] == nil {
		t.Fatalf("empty lists must be [], not null: %s", rec.Body)
	}
	if body["active_count"].(float64) != 0 {
		t.Errorf("active_count = %v", body["active_count"])
	}
}

// D13: P2 fills item detail's agents, requests and artifacts.
func TestItemDetailIsFilledByP2(t *testing.T) {
	s, seed := newRuntimeServer(t)
	rec := s.get(t, "/api/items/"+seed.TaskKey)
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body["agents"].([]any)) == 0 {
		t.Error("agents must be filled now")
	}
	if body["requests"] == nil || body["artifacts"] == nil {
		t.Error("requests and artifacts are always arrays")
	}
}

// contracts §4: checkpoints, newest first, with limit and before.
func TestCheckpointsRoute(t *testing.T) {
	s, seed := newRuntimeServerWithCheckpoints(t, 4)
	rec := s.get(t, "/api/items/"+seed.TaskKey+"/checkpoints?limit=2")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("got %d checkpoints", len(list))
	}
	if list[0]["created_at"].(float64) < list[1]["created_at"].(float64) {
		t.Error("newest first")
	}
	for _, k := range []string{"id", "item_key", "agent_name", "kind", "attempt", "resolution",
		"summary", "next", "blockers", "git", "verification", "artifacts", "daemon_written", "created_at"} {
		if _, ok := list[0][k]; !ok {
			t.Errorf("Checkpoint is missing %q: %s", k, rec.Body)
		}
	}
	before := int64(list[1]["created_at"].(float64))
	rec = s.get(t, fmt.Sprintf("/api/items/%s/checkpoints?before=%d", seed.TaskKey, before))
	var older []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &older)
	for _, c := range older {
		if int64(c["created_at"].(float64)) >= before {
			t.Fatalf("before= must exclude newer rows")
		}
	}
}

func TestUnknownItemIs404(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.get(t, "/api/items/TASK-999/checkpoints")
	if rec.Code != 404 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message string }
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "not_found" {
		t.Fatalf("error = %+v", body.Error)
	}
}

func TestAgentsRoutesRequireTheDaemonToken(t *testing.T) {
	s, _ := newRuntimeServer(t)
	for _, p := range []string{"/api/state", "/api/agents"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 401 {
			t.Errorf("%s without a token = %d", p, rec.Code)
		}
	}
}

// paneTestTmux lets one test control Capture's return/error and Panes's
// liveness, without changing what every other test in this package gets from
// the shared testTmux (agent-hover-preview spec, decision 1).
type paneTestTmux struct {
	testTmux
	captureText string
	captureErr  error
}

func (f *paneTestTmux) Capture(_ context.Context, _ string, _ int) (string, error) {
	if f.captureErr != nil {
		return "", f.captureErr
	}
	return f.captureText, nil
}

// contracts: GET /api/agents/{name}/pane (agent-hover-preview spec, decision 1).
func TestAgentPaneReturnsANSIStrippedTextAndTmuxAlive(t *testing.T) {
	s, running := newServerWithSessionState(t, "running")
	tm := &paneTestTmux{captureText: "\x1b[32mgreen\x1b[0m plain\n"}
	tm.panes = []runtime.Pane{{Session: running}}
	s.RT.Tmux = tm
	rec := s.get(t, "/api/agents/"+running+"/pane?lines=10")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	body := decode[paneWire](t, rec.Body.Bytes())
	if body.Text != "green plain\n" {
		t.Fatalf("text = %q, want ANSI stripped", body.Text)
	}
	if !body.TmuxAlive {
		t.Fatal("tmux_alive = false, want true (a live pane is seeded)")
	}
	if body.Lines != 10 {
		t.Fatalf("lines = %d, want 10", body.Lines)
	}
}

func TestAgentPaneUnknownAgentIs404(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.get(t, "/api/agents/totally-unknown-agent/pane")
	wantErr(t, rec.Code, rec.Body.Bytes(), 404, "not_found", "")
}

func TestAgentPaneNoSessionIs409(t *testing.T) {
	s, queued := newServerWithSessionState(t, "queued")
	rec := s.get(t, "/api/agents/"+queued+"/pane")
	wantErr(t, rec.Code, rec.Body.Bytes(), 409, "conflict", "That agent has no session.")
}

func TestAgentPaneCaptureFailureIs502(t *testing.T) {
	s, running := newServerWithSessionState(t, "running")
	s.RT.Tmux = &paneTestTmux{captureErr: errors.New("tmux: no such session")}
	rec := s.get(t, "/api/agents/"+running+"/pane")
	wantErr(t, rec.Code, rec.Body.Bytes(), 502, "tmux_unreachable", "Can't reach tmux.")
}

func TestAgentPaneLinesClamping(t *testing.T) {
	s, running := newServerWithSessionState(t, "running")
	s.RT.Tmux = &paneTestTmux{captureText: "x"}
	rec := s.get(t, "/api/agents/"+running+"/pane?lines=9999")
	if got := decode[paneWire](t, rec.Body.Bytes()).Lines; got != 200 {
		t.Fatalf("lines=9999 clamped to %d, want 200 (maxPaneLines)", got)
	}
	rec = s.get(t, "/api/agents/"+running+"/pane?lines=abc")
	if got := decode[paneWire](t, rec.Body.Bytes()).Lines; got != 40 {
		t.Fatalf("lines=abc fell back to %d, want 40 (defaultPaneLines)", got)
	}
	rec = s.get(t, "/api/agents/"+running+"/pane?lines=0")
	if got := decode[paneWire](t, rec.Body.Bytes()).Lines; got != 1 {
		t.Fatalf("lines=0 clamped to %d, want 1", got)
	}
}

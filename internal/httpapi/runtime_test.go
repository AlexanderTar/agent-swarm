package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
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

// 2026-09-22 incident: a child cancelled while still queued gets agents.state =
// 'finished' with no session ever created (runtime.Cancel's queued-agent path
// touches no session row at all). isFinishedChild only checked
// AgentAcknowledged and a completed/cancelled *session* state, missing this
// exact shape, so the cancelled child stayed in the parent's live `children`
// list forever, mislabeled "queued" in both clients' display-state logic
// (whose `session == nil` branch assumes "never spawned yet", not "spawned
// then cancelled before a session existed").
func TestCancelledWhileQueuedChildGoesToFinishedNotChildren(t *testing.T) {
	s, seed := newRuntimeServer(t)
	var orchID, taskID, epicID string
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM agents WHERE name = ?`, seed.AgentName).Scan(&orchID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.TaskKey).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.RootKey).Scan(&epicID); err != nil {
		t.Fatal(err)
	}
	cancelledID := seedAgent(t, s, runtime.RoleCoder, taskID, epicID, orchID, "cancelled-while-queued")
	if _, err := s.s.DB.ExecContext(bg, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`,
		db.Millis(time.Now()), cancelledID); err != nil {
		t.Fatal(err)
	}

	rec := s.get(t, "/api/state")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var orchNode map[string]any
	for _, a := range body["agents"].([]any) {
		n := a.(map[string]any)
		if n["name"] == seed.AgentName {
			orchNode = n
		}
	}
	if orchNode == nil {
		t.Fatalf("orchestrator %q not found in /api/state", seed.AgentName)
	}
	for _, c := range orchNode["children"].([]any) {
		if c.(map[string]any)["name"] == "cancelled-while-queued" {
			t.Fatal("a cancelled-while-queued child must not appear in `children` (live)")
		}
	}
	var foundInFinished bool
	for _, f := range orchNode["finished"].([]any) {
		if f.(map[string]any)["name"] == "cancelled-while-queued" {
			foundInFinished = true
		}
	}
	if !foundInFinished {
		t.Fatal("a cancelled-while-queued child must appear in `finished`")
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

// 2026-09-22: active_count is "actually running right now", not "queued or
// agent-level active" -- the old definition included paused and zombie
// agents, so it never dropped when the user paused a bunch of agents. The
// base tree already has two running agents (root-orchestrator, task-worker);
// adding a paused one, a queued-with-no-session one and a finished one must
// not move the count.
func TestActiveCountReflectsOnlyRunningSessions(t *testing.T) {
	s, seed := newRuntimeServer(t)
	var orchID, taskID, epicID string
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM agents WHERE name = ?`, seed.AgentName).Scan(&orchID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.TaskKey).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.RootKey).Scan(&epicID); err != nil {
		t.Fatal(err)
	}
	pausedID := seedAgent(t, s, runtime.RoleCoder, taskID, epicID, orchID, "paused-worker")
	seedSession(t, s, pausedID, "paused-worker", 1, "paused")
	queuedID := seedAgent(t, s, runtime.RoleCoder, taskID, epicID, orchID, "queued-worker")
	if _, err := s.s.DB.ExecContext(bg, `UPDATE agents SET state = 'queued' WHERE id = ?`, queuedID); err != nil {
		t.Fatal(err)
	}
	finishedID := seedAgent(t, s, runtime.RoleCoder, taskID, epicID, orchID, "cancelled-while-queued-2")
	if _, err := s.s.DB.ExecContext(bg, `UPDATE agents SET state = 'finished' WHERE id = ?`, finishedID); err != nil {
		t.Fatal(err)
	}

	rec := s.get(t, "/api/state")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := body["active_count"].(float64); got != 2 {
		t.Fatalf("active_count = %v, want 2 (root-orchestrator + task-worker running; paused/queued/finished must not count)", got)
	}
}

// A cancelled/finished mid-tree orchestrator does not cascade to its own
// already-spawned children (runtime.Cancel only touches the named agent's
// row) -- a still-running grandchild must keep counting even though its
// immediate parent lands in the grandparent's `finished` list, not `children`.
func TestActiveCountCountsRunningDescendantsOfAFinishedParent(t *testing.T) {
	s, seed := newRuntimeServer(t)
	var rootOrchID, taskID, epicID string
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM agents WHERE name = ?`, seed.AgentName).Scan(&rootOrchID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.TaskKey).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := s.s.DB.QueryRowContext(bg, `SELECT id FROM items WHERE key = ?`, seed.RootKey).Scan(&epicID); err != nil {
		t.Fatal(err)
	}
	// Mirrors what runtime.Cancel actually leaves behind: the named agent's own
	// session moves to 'cancelled' and its agents.state to 'finished' -- only
	// its already-spawned child (never touched by Cancel) stays running.
	subOrchID := seedAgent(t, s, runtime.RoleOrchestrator, taskID, epicID, rootOrchID, "sub-orchestrator")
	seedSession(t, s, subOrchID, "sub-orchestrator", 1, "cancelled")
	if _, err := s.s.DB.ExecContext(bg, `UPDATE agents SET state = 'finished' WHERE id = ?`, subOrchID); err != nil {
		t.Fatal(err)
	}
	grandchildID := seedAgent(t, s, runtime.RoleCoder, taskID, epicID, subOrchID, "still-running-grandchild")
	seedSession(t, s, grandchildID, "still-running-grandchild", 1, "running")

	rec := s.get(t, "/api/state")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// root-orchestrator + task-worker (base tree) + still-running-grandchild = 3;
	// sub-orchestrator's own session is cancelled, so it must not count itself,
	// but must not block its still-running child from counting either.
	if got := body["active_count"].(float64); got != 3 {
		t.Fatalf("active_count = %v, want 3 (a finished mid-tree parent must not hide a still-running grandchild)", got)
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

// The raw capture rides beside the stripped text so the menubar can render SGR colours;
// old clients keep reading text (pane-preview-ux spec, locked decisions).
func TestAgentPaneAlsoReturnsRawANSICapture(t *testing.T) {
	s, running := newServerWithSessionState(t, "running")
	raw := "\x1b[31mred\x1b[0m plain"
	tm := &paneTestTmux{captureText: raw}
	tm.panes = []runtime.Pane{{Session: running}}
	s.RT.Tmux = tm
	rec := s.get(t, "/api/agents/"+running+"/pane")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	body := decode[paneWire](t, rec.Body.Bytes())
	if body.Text != "red plain" {
		t.Fatalf("text = %q, want ANSI stripped", body.Text)
	}
	if body.ANSI != raw {
		t.Fatalf("ansi = %q, want raw capture %q", body.ANSI, raw)
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

// The pane is confirmed gone (absent from Panes(), not just erroring): same dead-session
// shape as TestAgentPaneNoSessionIs409, not a generic tmux_unreachable (scenario 8).
func TestAgentPaneCaptureFailureWithAConfirmedDeadPaneIs409(t *testing.T) {
	s, running := newServerWithSessionState(t, "running")
	s.RT.Tmux = &paneTestTmux{captureErr: errors.New("tmux: no such session")} // panes left empty: gone
	rec := s.get(t, "/api/agents/"+running+"/pane")
	wantErr(t, rec.Code, rec.Body.Bytes(), 409, "conflict", "That agent has no session.")
}

// The pane still shows up as live (a transient Capture error, e.g. tmux itself is down) —
// this is the genuine tmux_unreachable case, distinct from the confirmed-dead-pane 409 above.
func TestAgentPaneCaptureFailureWithALivePaneIs502(t *testing.T) {
	s, running := newServerWithSessionState(t, "running")
	tm := &paneTestTmux{captureErr: errors.New("tmux: no such session")}
	tm.panes = []runtime.Pane{{Session: running}}
	s.RT.Tmux = tm
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

func findAgentInTree(nodes []map[string]any, name string) map[string]any {
	for _, n := range nodes {
		if n["name"] == name {
			return n
		}
		if children, ok := n["children"].([]any); ok {
			var childMaps []map[string]any
			for _, c := range children {
				if cm, ok := c.(map[string]any); ok {
					childMaps = append(childMaps, cm)
				}
			}
			if found := findAgentInTree(childMaps, name); found != nil {
				return found
			}
		}
		if finished, ok := n["finished"].([]any); ok {
			var finMaps []map[string]any
			for _, f := range finished {
				if fm, ok := f.(map[string]any); ok {
					finMaps = append(finMaps, fm)
				}
			}
			if found := findAgentInTree(finMaps, name); found != nil {
				return found
			}
		}
	}
	return nil
}

func TestAgentsPayloadIncludesStep(t *testing.T) {
	s, seed := newRuntimeServer(t)

	var workerID, workerItemID, orchID string
	if err := s.DB.QueryRowContext(bg, `SELECT id, item_id FROM agents WHERE name = 'task-worker'`).Scan(&workerID, &workerItemID); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(bg, `SELECT id FROM agents WHERE name = 'root-orchestrator'`).Scan(&orchID); err != nil {
		t.Fatal(err)
	}
	epic, err := s.items.Get(bg, seed.RootKey)
	if err != nil {
		t.Fatal(err)
	}

	now := db.Millis(time.Now())
	wfID := ids.New("wf")
	if _, err := s.DB.ExecContext(bg, `INSERT INTO workflows
		(id, item_id, root_item_id, owner_agent_id, state, round, escalation, worktrees_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'running', 1, '', '[]', ?, ?)`,
		wfID, workerItemID, epic.ID, orchID, now, now); err != nil {
		t.Fatal(err)
	}

	// Seed an active run in workflow_runs with step_id = 'build', round = 1
	runID1 := ids.New("wfr")
	if _, err := s.DB.ExecContext(bg, `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES (?, ?, 'build', 1, 'coder', ?, 'active', ?)`,
		runID1, wfID, workerID, now); err != nil {
		t.Fatal(err)
	}

	rec := s.get(t, "/api/agents")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}

	workerNode := findAgentInTree(nodes, "task-worker")
	if workerNode == nil {
		t.Fatal("task-worker not found in tree")
	}
	if workerNode["step"] != "build" {
		t.Errorf("task-worker step = %v, want build", workerNode["step"])
	}

	orchNode := findAgentInTree(nodes, "root-orchestrator")
	if orchNode == nil {
		t.Fatal("root-orchestrator not found in tree")
	}
	if v, ok := orchNode["step"]; ok && v != nil {
		t.Errorf("orchestrator step = %v, want omitted or null", v)
	}

	// round = 2: insert another run with step_id = 'review', round = 2, created_at slightly later
	runID2 := ids.New("wfr")
	if _, err := s.DB.ExecContext(bg, `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES (?, ?, 'review', 2, 'reviewer', ?, 'active', ?)`,
		runID2, wfID, workerID, now+1000); err != nil {
		t.Fatal(err)
	}

	rec = s.get(t, "/api/agents")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var nodesRound2 []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &nodesRound2); err != nil {
		t.Fatal(err)
	}

	workerNode2 := findAgentInTree(nodesRound2, "task-worker")
	if workerNode2 == nil {
		t.Fatal("task-worker not found in tree (round 2)")
	}
	if workerNode2["step"] != "review r2" {
		t.Errorf("task-worker step = %v, want review r2", workerNode2["step"])
	}
}


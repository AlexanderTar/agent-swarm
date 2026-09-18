package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func names(defs []ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	sort.Strings(out)
	return out
}

func TestToolListsByRole(t *testing.T) {
	s := newTestServer(t)
	shared := []string{"swarm_ask", "swarm_checkpoint", "swarm_kb", "swarm_read", "swarm_send", "swarm_sync"}
	for _, role := range []runtime.Role{runtime.RoleCoder, runtime.RoleReviewer, runtime.RoleUIReviewer,
		runtime.RoleResearcher, runtime.RoleDebugger, runtime.RoleMechanical} {
		got := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: role}))
		if strings.Join(got, ",") != strings.Join(shared, ",") {
			t.Errorf("%s sees %v, want the 6 shared tools", role, got)
		}
	}
	// §8.1: 6 shared + 5 orchestrator tools = 11. swarm_materialize is the twelfth
	// and only appears for a *spike* orchestrator — TestMaterializeIsSpikeOnly below
	// pins both halves. An earlier draft asserted 12 here and "not present" there,
	// which is two tests in one file contradicting each other (R7).
	orch := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleOrchestrator}))
	if len(orch) != 11 {
		t.Fatalf("an orchestrator sees %d tools, want 11: %v", len(orch), orch)
	}
	for _, want := range []string{"swarm_items", "swarm_artifact", "swarm_worktree", "swarm_spawn",
		"swarm_control"} {
		if !slices.Contains(orch, want) {
			t.Errorf("an orchestrator is missing %s", want)
		}
	}
	if slices.Contains(orch, "swarm_materialize") {
		t.Error("swarm_materialize belongs to a spike orchestrator only")
	}
	spikeOrch := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleOrchestrator, SpikeOrchestrator: true}))
	if len(spikeOrch) != 12 {
		t.Fatalf("a spike orchestrator sees %d tools, want 12: %v", len(spikeOrch), spikeOrch)
	}
	unbound := names(s.ToolsFor(Caller{Unbound: true}))
	if strings.Join(unbound, ",") != "swarm_kb,swarm_read" {
		t.Fatalf("an unbound caller sees %v", unbound)
	}
}

// L28: swarm_advise only in simulated mode.
func TestAdviseToolFollowsTheAdvisorMode(t *testing.T) {
	s := newTestServer(t)
	sim := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleCoder, AdvisorMode: "simulated"}))
	if !slices.Contains(sim, "swarm_advise") {
		t.Error("a simulated advisor exposes swarm_advise")
	}
	native := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleCoder, AdvisorMode: "native"}))
	if slices.Contains(native, "swarm_advise") {
		t.Error("a native Claude advisor uses the built-in tool, not swarm_advise")
	}
	none := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleCoder}))
	if slices.Contains(none, "swarm_advise") {
		t.Error("no advisor means no tool")
	}
}

// §8.1: swarm_materialize is refused for an orchestrator that isn't running a spike.
func TestMaterializeIsSpikeOnly(t *testing.T) {
	s := newTestServer(t)
	notSpike := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleOrchestrator}))
	if slices.Contains(notSpike, "swarm_materialize") {
		t.Error("an epic orchestrator has nothing to materialize")
	}
	spike := names(s.ToolsFor(Caller{SessionID: "ses_1", Role: runtime.RoleOrchestrator, SpikeOrchestrator: true}))
	if !slices.Contains(spike, "swarm_materialize") {
		t.Error("a spike orchestrator has swarm_materialize")
	}
}

// §8: every description is at most 60 words.
func TestDescriptionsAreShortAndSchemasParse(t *testing.T) {
	s := newTestServer(t)
	for _, d := range s.Tools() {
		if d.Description == "" {
			t.Errorf("%s has no description", d.Name)
		}
		if n := len(strings.Fields(d.Description)); n > 60 {
			t.Errorf("%s description is %d words, cap is 60", d.Name, n)
		}
		var schema map[string]any
		if err := json.Unmarshal(d.Schema, &schema); err != nil {
			t.Errorf("%s schema does not parse: %v", d.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s schema is not an object", d.Name)
		}
	}
}

// L16: identity comes from the token; an argument naming another agent is ignored.
func TestToolArgumentsCannotChangeIdentity(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	other := seedOtherAgent(t, s)
	out, err := s.call(ctx, seed.Caller, "swarm_checkpoint",
		`{"kind":"progress","summary":"mine","agent":"`+other+`","session":"ses_other"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		CheckpointID string `json:"checkpoint_id"`
	}
	json.Unmarshal(mustJSON(out), &res)
	var agentID string
	s.RT.DB.QueryRowContext(ctx, `SELECT agent_id FROM checkpoints WHERE id = ?`, res.CheckpointID).Scan(&agentID)
	if agentID != seed.Caller.AgentID {
		t.Fatalf("the checkpoint was attributed to %q", agentID)
	}
}

// I6: an unbound caller cannot write to the knowledge base, even calling directly.
func TestUnboundKbWriteIsRefused(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	_, err := s.call(ctx, Caller{Unbound: true}, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"x.md","title":"x","body":"y"}`)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("err = %v", err)
	}
	// and its schema does not offer write
	for _, d := range s.ToolsFor(Caller{Unbound: true}) {
		if d.Name != "swarm_kb" {
			continue
		}
		if strings.Contains(string(d.Schema), `"write"`) {
			t.Fatalf("the unbound schema offers write: %s", d.Schema)
		}
	}
}

// C3: the pause allow-list is enforced on the server, whatever the hooks do.
func TestPausedSessionsAreGatedAtTheServer(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	s.RT.DB.ExecContext(ctx, `UPDATE sessions SET state = 'pause_requested' WHERE id = ?`, seed.Caller.SessionID)
	for _, tool := range []string{"swarm_send", "swarm_kb"} {
		if _, err := s.call(ctx, seed.Caller, tool, `{}`); err == nil ||
			!strings.Contains(err.Error(), "paused: finish your handoff and stop.") {
			t.Errorf("%s during a pause: err = %v", tool, err)
		}
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_sync", `{}`); err != nil {
		t.Errorf("swarm_sync stays allowed: %v", err)
	}
}

// Auth: a bad token is 401, and an older generation's token is 401 too.
func TestHandlerAuth(t *testing.T) {
	s, seed := newServerWithSession(t)
	h := s.Handler(seed.Resolve)
	for _, tok := range []string{"", "nonsense", seed.RevokedToken} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Errorf("token %q gave %d, want 401", tok, rec.Code)
		}
	}
}

// A real round trip through Handler with a valid token: tools/list, then a
// tools/call for swarm_sync. This is what actually exercises MCPServer's
// dispatch wiring (the SDK's Arguments re-marshal path and the stateless
// POST-without-initialize path); TestHandlerAuth only ever gets 401.
func TestHandlerRoundTripWithAValidToken(t *testing.T) {
	s, seed := newServerWithSession(t)
	srv := httptest.NewServer(s.Handler(seed.Resolve))
	defer srv.Close()
	post := func(body string) string {
		req, err := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+seed.Caller.SessionID)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(respBody)
	}
	list := post(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if !strings.Contains(list, "swarm_sync") {
		t.Fatalf("tools/list = %s", list)
	}
	call := post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"swarm_sync","arguments":{}}}`)
	if !strings.Contains(call, "session_state") {
		t.Fatalf("tools/call swarm_sync = %s", call)
	}
	// a handler error becomes an MCP tool error (isError:true), not an HTTP failure.
	bad := post(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"swarm_checkpoint","arguments":{"kind":"not-a-kind","summary":"x"}}}`)
	if !strings.Contains(bad, `"isError":true`) {
		t.Fatalf("tools/call with a bad kind = %s", bad)
	}
}

// dispatch refuses a session id that resolves to no live session.
func TestDispatchRefusesAnUnknownSession(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.call(context.Background(), Caller{SessionID: "ses_does_not_exist"}, "swarm_sync", `{}`); err == nil {
		t.Fatal("an unknown session must not reach the handler")
	}
}

// L7: no MCP code path ever writes a message with origin = 'user_action'.
func TestNoToolEverWritesAUserActionOrigin(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	s.call(ctx, seed.Caller, "swarm_sync", `{}`)
	s.call(ctx, seed.Caller, "swarm_checkpoint", `{"kind":"progress","summary":"x"}`)
	s.call(ctx, seed.Caller, "swarm_send", `{"to":"parent","body":"hi"}`)
	var n int
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE origin = 'user_action'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d messages carry origin = user_action; L7 forbids that from any MCP path", n)
	}
}

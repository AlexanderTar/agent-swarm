package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func newTestServerWithSettings(t *testing.T) (*Server, func()) {
	t.Helper()
	s := newTestServer(t)
	s.Settings = s.RT.Settings
	now := db.Millis(s.RT.Now())
	ctx := context.Background()
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE settings SET value_json = '["claude"]' WHERE key = 'enabled_agents'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO items
		(id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_instr', 'TASK-INSTR', 'task', 'itm_instr', 'Instructions Test', 'in_progress', ?, ?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_instr', 'coder-1', 'fake', 'fake-1', 'coder', 'itm_instr', 'itm_instr', 'brief', 'active', ?)`,
		now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('s1', 'agt_instr', 1, 1, 'tok_instr', 'coder-1', '/tmp', 'running', 'neutral', ?)`,
		now); err != nil {
		t.Fatal(err)
	}
	return s, func() {}
}

func TestInstructionsToolGetAndSet(t *testing.T) {
	srv, cleanup := newTestServerWithSettings(t)
	defer cleanup()
	ctx := context.Background()
	c := Caller{SessionID: "s1", AgentName: "coder-1"}

	// 1. Get initial empty instructions
	res, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"get"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["instructions"] != "" {
		t.Fatalf("expected empty instructions, got %v", m["instructions"])
	}

	// 2. Set new instructions
	newInstr := "# Team Guidelines\nAlways write tests."
	setRes, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"set","instructions":"# Team Guidelines\nAlways write tests."}`))
	if err != nil {
		t.Fatal(err)
	}
	sm := setRes.(map[string]any)
	if sm["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", sm)
	}

	// 3. Get updated instructions
	res2, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"get"}`))
	if err != nil {
		t.Fatal(err)
	}
	m2 := res2.(map[string]any)
	if m2["instructions"] != newInstr {
		t.Fatalf("expected updated instructions %q, got %q", newInstr, m2["instructions"])
	}
}

func TestInstructionsToolInvalidOp(t *testing.T) {
	srv, cleanup := newTestServerWithSettings(t)
	defer cleanup()
	ctx := context.Background()
	c := Caller{SessionID: "s1", AgentName: "coder-1"}

	_, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"delete"}`))
	if err == nil || !strings.Contains(err.Error(), `op must be get or set, got "delete"`) {
		t.Fatalf("expected error containing op must be get or set, got %v", err)
	}
}

func TestInstructionsToolUnboundCaller(t *testing.T) {
	srv, cleanup := newTestServerWithSettings(t)
	defer cleanup()
	ctx := context.Background()
	c := Caller{Unbound: true}

	// 1. Get instructions as unbound caller succeeds
	res, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"get"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["instructions"] != "" {
		t.Fatalf("expected empty instructions, got %v", m["instructions"])
	}

	// 2. Set instructions as unbound caller fails with read-only error
	_, err = srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"set","instructions":"# Global Instructions"}`))
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only error for unbound caller set, got %v", err)
	}
}

func TestSyncToolReturnsSessionState(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_sync", `{"ack":[],"limit":5}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), "session_state") {
		t.Fatalf("out = %s", mustJSON(out))
	}
}

func TestSyncRefusesAnUnknownAckID(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_sync", `{"ack":["msg_does_not_exist"]}`); err == nil {
		t.Fatal("acking an unknown message id must be refused")
	}
}

func TestCheckpointRejectsABadKind(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_checkpoint", `{"kind":"not-a-real-kind","summary":"x"}`); err == nil {
		t.Fatal("a bad checkpoint kind must be refused")
	}
}

func countCheckpoints(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.RT.DB.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Task 41: a repeated request_id must not write a second checkpoint, and must
// return the exact same result as the first call (I11).
func TestCheckpointRequestIDReplaysInsteadOfWritingTwice(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	out1, err := s.call(ctx, seed.Caller, "swarm_checkpoint",
		`{"kind":"accepted","summary":"starting","request_id":"req-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := countCheckpoints(t, s); n != 1 {
		t.Fatalf("checkpoints after first call = %d, want 1", n)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_checkpoint",
		`{"kind":"accepted","summary":"starting","request_id":"req-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := countCheckpoints(t, s); n != 1 {
		t.Fatalf("checkpoints after replayed call = %d, want still 1", n)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

// Distinct request_ids (or no request_id at all) must each genuinely mutate.
func TestCheckpointWithoutOrDistinctRequestIDsEachMutate(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_checkpoint",
		`{"kind":"accepted","summary":"starting","request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_checkpoint",
		`{"kind":"progress","summary":"still going","request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_checkpoint",
		`{"kind":"progress","summary":"more still going"}`); err != nil {
		t.Fatal(err)
	}
	if n := countCheckpoints(t, s); n != 3 {
		t.Fatalf("checkpoints = %d, want 3", n)
	}
}

// swarm_ask requires an artifact for an approval.
func TestAskRequiresArtifactForApproval(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	_, err := s.call(ctx, seed.Caller, "swarm_ask", `{"kind":"approval","prompt":"Approve this?"}`)
	if err == nil || !strings.Contains(err.Error(), "artifact_id") {
		t.Fatalf("err = %v", err)
	}
}

// swarm_ask's success path: a plain question round-trips through requestOut.
// §8.1: the result is exactly {"request_id","state"} - no kind/prompt/
// artifact_id/section_id echoed back (fix round 2, item 1).
func TestAskQuestionSucceeds(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_ask", `{"kind":"question","prompt":"Keep the email field?"}`)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mustJSON(out), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("result must be exactly {request_id,state}, got keys %v", raw)
	}
	var res struct {
		RequestID string `json:"request_id"`
		State     string `json:"state"`
	}
	json.Unmarshal(mustJSON(out), &res)
	if res.RequestID == "" || res.State != "open" {
		t.Fatalf("result = %+v", res)
	}
}

func countRequests(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.RT.DB.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Task 41: a repeated request_id on swarm_ask must not open a second request.
func TestAskQuestionRequestIDReplaysInsteadOfAskingTwice(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	body := `{"kind":"question","prompt":"Keep the email field?","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_ask", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRequests(t, s); n != 1 {
		t.Fatalf("requests after first call = %d, want 1", n)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_ask", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRequests(t, s); n != 1 {
		t.Fatalf("requests after replayed call = %d, want still 1", n)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

func TestAskWithoutOrDistinctRequestIDsEachAsk(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_ask",
		`{"kind":"question","prompt":"One?","request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_ask",
		`{"kind":"question","prompt":"Two?","request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_ask", `{"kind":"question","prompt":"Three?"}`); err != nil {
		t.Fatal(err)
	}
	if n := countRequests(t, s); n != 3 {
		t.Fatalf("requests = %d, want 3", n)
	}
}

// withdraw is Ask's fourth branch (kind ignored, Withdraw set) and goes
// through its own idemTx call; a repeated request_id must not withdraw twice
// (the second withdraw would otherwise fail with "Already resolved.").
func TestAskWithdrawRequestIDReplays(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	asked, err := s.call(ctx, seed.Caller, "swarm_ask", `{"kind":"question","prompt":"Keep it?"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		RequestID string `json:"request_id"`
	}
	json.Unmarshal(mustJSON(asked), &res)
	body := `{"withdraw":"` + res.RequestID + `","request_id":"req-w1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_ask", body)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_ask", body)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

// swarm_send refuses a target outside the caller's own top-level item.
func TestSendRefusesACrossRootTarget(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	otherID := seedOtherAgent(t, s)
	var otherName string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, otherID).Scan(&otherName); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_send", `{"to":"`+otherName+`","body":"hi"}`); err == nil {
		t.Fatal("a message to an agent outside the caller's top-level item must be refused")
	}
}

func TestSendSucceedsToAnAgentInTheSameRoot(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_send", `{"to":"`+seed.Caller.AgentName+`","body":"note to self"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), "msg_id") {
		t.Fatalf("out = %s", mustJSON(out))
	}
}

func countMessagesFor(t *testing.T, s *Server, toAgentID string) int {
	t.Helper()
	var n int
	if err := s.RT.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE to_agent_id = ?`, toAgentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Task 41: a repeated request_id must not enqueue a second message.
func TestSendRequestIDReplaysInsteadOfSendingTwice(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	body := `{"to":"` + seed.Caller.AgentName + `","body":"note to self","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_send", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countMessagesFor(t, s, seed.Caller.AgentID); n != 1 {
		t.Fatalf("messages after first call = %d, want 1", n)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_send", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countMessagesFor(t, s, seed.Caller.AgentID); n != 1 {
		t.Fatalf("messages after replayed call = %d, want still 1", n)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

func TestSendWithoutOrDistinctRequestIDsEachSend(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_send",
		`{"to":"`+seed.Caller.AgentName+`","body":"one","request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_send",
		`{"to":"`+seed.Caller.AgentName+`","body":"two","request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_send",
		`{"to":"`+seed.Caller.AgentName+`","body":"three"}`); err != nil {
		t.Fatal(err)
	}
	if n := countMessagesFor(t, s, seed.Caller.AgentID); n != 3 {
		t.Fatalf("messages = %d, want 3", n)
	}
}

// Omitted kind stores finding, the generic agent message kind. Explicit
// relay is rejected by TestSendRejectsUnknownKind — relay is daemon-only.
func TestSendDefaultsOmittedKindToFinding(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_send",
		`{"to":"`+seed.Caller.AgentName+`","body":"no kind"}`); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	rows, err := s.RT.DB.QueryContext(ctx, `SELECT kind FROM messages WHERE to_agent_id = ? ORDER BY seq`, seed.Caller.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 1 {
		t.Fatalf("stored kinds = %v, want exactly 1 message", kinds)
	}
	for _, k := range kinds {
		if k != "finding" {
			t.Errorf("stored kind = %q, want finding", k)
		}
	}
}

// Only the strict agent kind set is accepted: invented kinds fail fast
// instead of being stored verbatim, and relay stays daemon-only — an agent
// sending it explicitly gets the same rejection.
func TestSendRejectsUnknownKind(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	for _, kind := range []string{"banana", "relay"} {
		_, err := s.call(ctx, seed.Caller, "swarm_send",
			`{"to":"`+seed.Caller.AgentName+`","kind":"`+kind+`","body":"x"}`)
		if err == nil {
			t.Errorf("kind %q: want rejection, got success", kind)
			continue
		}
		if !strings.Contains(err.Error(), "kind") {
			t.Errorf("kind %q: error %q should name kind", kind, err)
		}
	}
}

func TestReadToolRefusesAnUnknownRef(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_read", `{"refs":["TASK-does-not-exist"]}`); err == nil {
		t.Fatal("an unknown ref must be refused")
	}
}

// §8.1: the bound tool's op enum must allow write, not just search/get -
// otherwise a real MCP client that validates arguments against the declared
// schema before sending could never reach the write case below (fix round 2
// full-pass finding, same class as item 3's swarm_artifact enum).
func TestKbBoundSchemaAllowsWrite(t *testing.T) {
	s, seed := newServerWithSession(t)
	var schema struct {
		Properties struct {
			Op struct {
				Enum []string `json:"enum"`
			} `json:"op"`
		} `json:"properties"`
	}
	for _, d := range s.ToolsFor(seed.Caller) {
		if d.Name == "swarm_kb" {
			if err := json.Unmarshal(d.Schema, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Contains(schema.Properties.Op.Enum, "write") {
		t.Fatalf("a bound caller's swarm_kb op enum must allow \"write\": %v", schema.Properties.Op.Enum)
	}
}

// I6/§8.1: swarm_kb search, get and write for a bound caller.
func TestKbSearchGetAndWrite(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"decision-one","title":"Decision one","body":"Use the zephyr protocol for retries."}`)
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Slug string `json:"slug"`
	}
	json.Unmarshal(mustJSON(out), &w)
	if w.Slug != "notes/decision-one" {
		t.Fatalf("slug = %q", w.Slug)
	}

	out, err = s.call(ctx, seed.Caller, "swarm_kb", `{"op":"get","slug":"`+w.Slug+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	// §8.1: get's result is exactly {"markdown"} - no slug/title/frontmatter
	// (fix round 2, item 2).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mustJSON(out), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("get result must be exactly {markdown}, got keys %v", raw)
	}
	if !strings.Contains(string(mustJSON(out)), "zephyr") {
		t.Fatalf("get result = %s", mustJSON(out))
	}

	out, err = s.call(ctx, seed.Caller, "swarm_kb", `{"op":"search","q":"zephyr"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), "decision-one") {
		t.Fatalf("search result = %s", mustJSON(out))
	}
}

// Task 41: a repeated request_id must not overwrite the file a second time --
// proven here by changing the body on the replay and checking it did NOT
// take, which a merely-idempotent-by-construction write (same path, same
// content) wouldn't distinguish from a real guard.
func TestKbWriteRequestIDReplaysInsteadOfWritingTwice(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	out1, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"decision-two","title":"D2","body":"first body","request_id":"req-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"decision-two","title":"D2","body":"second body","request_id":"req-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
	path := filepath.Join(s.KB.Dir, "notes", "decision-two.md")
	content := readFile(t, path)
	if !strings.Contains(content, "first body") || strings.Contains(content, "second body") {
		t.Fatalf("replayed write must not have re-run: content = %q", content)
	}
}

func TestKbWriteWithoutOrDistinctRequestIDsEachWrite(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"a","title":"A","body":"a","request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"a","title":"A","body":"a again","request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.KB.Dir, "notes", "a.md")
	if content := readFile(t, path); !strings.Contains(content, "a again") {
		t.Fatalf("a distinct request_id must genuinely re-write: content = %q", content)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"a","title":"A","body":"a once more"}`); err != nil {
		t.Fatal(err)
	}
	if content := readFile(t, path); !strings.Contains(content, "a once more") {
		t.Fatalf("no request_id at all must genuinely re-write: content = %q", content)
	}
}

func TestKbUnknownOpIsRefused(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_kb", `{"op":"delete"}`); err == nil {
		t.Fatal("an unknown swarm_kb op must be refused")
	}
}

func TestKbWriteRejectsDotDotAndEmptyFilename(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"../etc","filename":"x.md","title":"x","body":"y"}`); err == nil {
		t.Fatal("a subdir containing .. must be refused")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"notes","filename":"","title":"x","body":"y"}`); err == nil {
		t.Fatal("an empty filename must be refused")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_kb",
		`{"op":"write","subdir":"bogus","filename":"x.md","title":"x","body":"y"}`); err == nil {
		t.Fatal("a subdir outside the enum must be refused, not just one containing ..")
	}
}

// §8.1: the result is {"advice_id","state","answer"?,"error"?} - "error" is
// present only when adv.Error is non-empty (a failed run), added 2026-09-18
// so a diagnostic isn't silently discarded. This test covers the success path,
// where "error" must be absent.
//
// seedAgentAndSession leaves advisor_kind unset, which AdvisorCommand (no
// case for "") turns into an immediate "can't run as a read-only advisor"
// failure before the fixture's stubbed Run ({"result":"advice"}, wired for
// runtime.Claude's parseClaudeAnswer) ever runs. The agent needs a handled
// kind to actually exercise the success path this test is named for.
func TestAdvisorToolRunsWhenVisible(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.RT.DB.ExecContext(ctx,
		`UPDATE agents SET advisor_kind = 'claude', advisor_model = 'claude-sonnet' WHERE id = ?`,
		seed.AgentID); err != nil {
		t.Fatal(err)
	}
	c := seed.Caller
	c.AdvisorMode = "simulated"
	out, err := s.call(ctx, c, "swarm_advise", `{"question":"Client or server?","wait_seconds":5}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), "advice_id") {
		t.Fatalf("out = %s", mustJSON(out))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mustJSON(out), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["error"]; ok {
		t.Fatalf("a successful run must not have an \"error\" key: %v", raw)
	}
	for _, k := range []string{"advice_id", "state", "answer"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("result missing %q: %v", k, raw)
		}
	}
	var state, answer string
	json.Unmarshal(raw["state"], &state)
	json.Unmarshal(raw["answer"], &answer)
	if state != "answered" || answer != "advice" {
		t.Fatalf("want a genuine success (state=answered, answer=advice), got state=%q answer=%q: %v", state, answer, raw)
	}
	// a wait outside 0-50 is clamped, not refused
	if _, err := s.call(ctx, c, "swarm_advise", `{"question":"q","wait_seconds":500}`); err != nil {
		t.Fatal(err)
	}
}

func TestAdvisorToolRefusesWithNoAdvisorConfigured(t *testing.T) {
	s, seed := newServerWithSession(t)
	noAdvisor := &Server{RT: s.RT, KB: s.KB}
	ctx := context.Background()
	c := seed.Caller
	c.AdvisorMode = "simulated"
	if _, err := noAdvisor.call(ctx, c, "swarm_advise", `{"question":"q","wait_seconds":-5}`); err == nil {
		t.Fatal("swarm_advise with no advisor configured must be refused")
	}
}

func TestSwarmBlockerOpensHITLRequest(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()

	out, err := s.call(ctx, seed.Caller, "swarm_blocker", `{"reason":"Need AWS credentials to deploy","options":["Provide credentials","Deploy locally"]}`)
	if err != nil {
		t.Fatal(err)
	}

	var res map[string]any
	if err := json.Unmarshal(mustJSON(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "blocked" {
		t.Fatalf("status = %v, want blocked", res["status"])
	}
	reqID, ok := res["request_id"].(string)
	if !ok || reqID == "" {
		t.Fatalf("request_id missing in %v", res)
	}

	var isHITL int
	var kind, prompt string
	err = s.RT.DB.QueryRowContext(ctx, `SELECT is_hitl, kind, prompt FROM requests WHERE id = ?`, reqID).
		Scan(&isHITL, &kind, &prompt)
	if err != nil {
		t.Fatal(err)
	}
	if isHITL != 1 || kind != "blocker" || prompt != "Need AWS credentials to deploy" {
		t.Fatalf("request not recorded properly: isHITL=%d kind=%s prompt=%s", isHITL, kind, prompt)
	}
}

func TestSwarmReadIncludesParentAgent(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()

	worker := spawnWorker(t, s, seed)
	out, err := s.call(ctx, seed.Caller, "swarm_read", `{"refs":["`+worker.Name+`"]}`)
	if err != nil {
		t.Fatal(err)
	}

	var res struct {
		Agents []struct {
			Name   string  `json:"name"`
			Parent *string `json:"parent"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(mustJSON(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(res.Agents))
	}
	if res.Agents[0].Parent == nil || *res.Agents[0].Parent != seed.Caller.AgentName {
		t.Fatalf("expected agent parent to be %q, got %v", seed.Caller.AgentName, res.Agents[0].Parent)
	}

	// Also verify orchestrator itself has nil parent in swarm_read
	outOrch, err := s.call(ctx, seed.Caller, "swarm_read", `{"refs":["`+seed.Caller.AgentName+`"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var resOrch struct {
		Agents []struct {
			Name   string  `json:"name"`
			Parent *string `json:"parent"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(mustJSON(outOrch), &resOrch); err != nil {
		t.Fatal(err)
	}
	if len(resOrch.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(resOrch.Agents))
	}
	if resOrch.Agents[0].Parent != nil {
		t.Fatalf("expected orchestrator parent to be nil, got %v", *resOrch.Agents[0].Parent)
	}
}

func TestSyncToolReturnsAnUnackedListEvenWhenEmpty(t *testing.T) {
	s, seed := newServerWithSession(t)
	out, err := s.call(context.Background(), seed.Caller, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), `"unacked":[]`) {
		t.Fatalf("out = %s", mustJSON(out))
	}
}

func TestSwarmCheckpointVerdictSchema(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()

	// 1. Check schema of swarm_checkpoint
	var ckpDef ToolDef
	for _, d := range s.ToolsFor(seed.Caller) {
		if d.Name == "swarm_checkpoint" {
			ckpDef = d
			break
		}
	}
	if ckpDef.Name == "" {
		t.Fatal("swarm_checkpoint tool not found")
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Verdict struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			} `json:"verdict"`
			Findings struct {
				Type  string `json:"type"`
				Items struct {
					Required   []string       `json:"required"`
					Properties map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"findings"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(ckpDef.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	for _, req := range []string{"kind", "summary"} {
		if !slices.Contains(schema.Required, req) {
			t.Fatalf("checkpoint schema.Required = %v, want %q", schema.Required, req)
		}
	}
	for _, v := range []string{"pass", "changes_requested", "blocked"} {
		if !slices.Contains(schema.Properties.Verdict.Enum, v) {
			t.Fatalf("schema verdict enum = %v, want %q", schema.Properties.Verdict.Enum, v)
		}
	}
	for _, req := range []string{"severity", "summary"} {
		if !slices.Contains(schema.Properties.Findings.Items.Required, req) {
			t.Fatalf("schema findings items required = %v, want %q", schema.Properties.Findings.Items.Required, req)
		}
	}
	for _, prop := range []string{"severity", "file", "line", "unit", "summary"} {
		if _, ok := schema.Properties.Findings.Items.Properties[prop]; !ok {
			t.Fatalf("schema findings items properties missing %q: %v", prop, schema.Properties.Findings.Items.Properties)
		}
	}

	// 2. Non-reviewer sets verdict -> refused
	_, err := s.call(ctx, seed.Caller, "swarm_checkpoint",
		`{"kind":"progress","summary":"test","verdict":"pass"}`)
	if err == nil || err.Error() != "Only reviewers set a verdict." {
		t.Fatalf("err = %v, want 'Only reviewers set a verdict.'", err)
	}

	// 3. Reviewer caller
	revAgentID, revSessionID, _ := seedAgentAndSession(t, s, runtime.RoleReviewer, "", "")
	revAgent, err := s.RT.AgentByID(ctx, revAgentID)
	if err != nil {
		t.Fatal(err)
	}
	revCaller := Caller{SessionID: revSessionID, AgentID: revAgentID, AgentName: revAgent.Name, Role: runtime.RoleReviewer}

	// Invalid verdict
	_, err = s.call(ctx, revCaller, "swarm_checkpoint",
		`{"kind":"completed","summary":"test","verdict":"invalid"}`)
	if err == nil || err.Error() != "Reviewers must complete with verdict: pass, changes_requested or blocked." {
		t.Fatalf("err = %v, want 'Reviewers must complete with verdict: pass, changes_requested or blocked.'", err)
	}

	// Invalid finding severity
	_, err = s.call(ctx, revCaller, "swarm_checkpoint",
		`{"kind":"completed","summary":"test","verdict":"changes_requested","findings":[{"severity":"invalid","summary":"bad"}]}`)
	wantSev := `finding severity "invalid" must be critical, major, minor or nit.`
	if err == nil || err.Error() != wantSev {
		t.Fatalf("err = %v, want %q", err, wantSev)
	}
}

// swarm_ask approvals without a named kind leave the daemon to infer intent;
// pin kind as required and document the approval fields.
func TestAskToolSchemaRequiresKind(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Artifact struct {
				Description string `json:"description"`
			} `json:"artifact"`
			Section struct {
				Description string `json:"description"`
			} `json:"section"`
		} `json:"properties"`
	}
	for _, d := range s.ToolsFor(seed.Caller) {
		if d.Name == "swarm_ask" {
			if err := json.Unmarshal(d.Schema, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Contains(schema.Required, "kind") {
		t.Errorf("swarm_ask required = %v, want kind", schema.Required)
	}
	if schema.Properties.Artifact.Description == "" || schema.Properties.Section.Description == "" {
		t.Errorf("swarm_ask artifact/section need descriptions")
	}
}

// Every shared tool must declare what it actually needs: clients that
// validate arguments before sending can only see the schema, so an omitted
// `required` turns every missing field into a runtime round-trip. sync/read
// stay all-optional by design (any filter combo is valid) — pinned here too.
func TestSharedToolSchemasDeclareRequired(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	caller := seed.Caller
	caller.AdvisorMode = "simulated"
	required := map[string][]string{}
	for _, d := range s.ToolsFor(caller) {
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(d.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		required[d.Name] = schema.Required
	}
	for tool, want := range map[string][]string{
		"swarm_blocker":      {"reason"},
		"swarm_send":         {"to", "body"},
		"swarm_advise":       {"question"},
		"swarm_instructions": {"op"},
		"swarm_kb":           {"op"},
	} {
		got, ok := required[tool]
		if !ok {
			t.Errorf("%s not visible to orchestrator caller", tool)
			continue
		}
		for _, w := range want {
			if !slices.Contains(got, w) {
				t.Errorf("%s required = %v, want %q", tool, got, w)
			}
		}
	}
	for _, tool := range []string{"swarm_sync", "swarm_read"} {
		if got := required[tool]; len(got) != 0 {
			t.Errorf("%s required = %v, want empty (all-optional by design)", tool, got)
		}
	}
}

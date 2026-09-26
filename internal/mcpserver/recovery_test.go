package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
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

// TestSyncCarriesAssignmentAndRecovery pins swarm_sync's additive
// recovery: every generation's first sync carries assignment + recovery
// (operation, manifest path/hash, predecessor session, cursor, workflow
// binding) independent of acked inbox rows; later syncs carry assignment
// only.
func TestSyncCarriesAssignmentAndRecovery(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	agentID, ses1, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	var agentName, itemID string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT name, item_id FROM agents WHERE id = ?`,
		agentID).Scan(&agentName, &itemID); err != nil {
		t.Fatal(err)
	}
	now := db.Millis(s.RT.Now())
	ses2 := ids.New("ses")
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 2, ?, ?, ?, 'running', 'neutral', ?)`,
		ses2, agentID, ses2, agentName, "/tmp/work", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, manifest_path, manifest_hash, created_at, updated_at)
		VALUES ('op_sync1', ?, 'handoff', 'succeeded', 'k1', ?, 1, '/h/manifest.json', 'abc', ?, ?)`,
		agentID, ses1, now, now); err != nil {
		t.Fatal(err)
	}

	first, err := s.call(ctx, Caller{SessionID: ses2, Role: runtime.RoleCoder}, "swarm_sync", `{}`)
	if err != nil {
		t.Fatalf("sync err = %v", err)
	}
	fm := first.(map[string]any)
	assignment, ok := fm["assignment"].(map[string]any)
	if !ok || assignment["item"] == nil || assignment["item"] == "" {
		t.Fatalf("first sync assignment = %v, want the durable brief", fm["assignment"])
	}
	rec, ok := fm["recovery"].(map[string]any)
	if !ok || rec["operation_id"] != "op_sync1" || rec["predecessor_session"] != ses1 {
		t.Fatalf("first sync recovery = %v, want op_sync1 naming %s", fm["recovery"], ses1)
	}
	if fm["first_sync"] != true {
		t.Fatalf("first_sync = %v, want true", fm["first_sync"])
	}

	second, err := s.call(ctx, Caller{SessionID: ses2, Role: runtime.RoleCoder}, "swarm_sync", `{}`)
	if err != nil {
		t.Fatalf("second sync err = %v", err)
	}
	sm := second.(map[string]any)
	if sm["recovery"] != nil {
		t.Fatalf("second sync recovery = %v, want nil", sm["recovery"])
	}
	sa, ok := sm["assignment"].(map[string]any)
	if !ok || sa["item"] == nil || sa["item"] == "" {
		t.Fatalf("second sync assignment = %v, want it to survive", sm["assignment"])
	}
}

// TestReadRecoveryHistory pins swarm_read's agent-scoped recovery read:
// full checkpoint fields with provenance across generations, plus readable
// worktrees and artifact revisions/hashes.
func TestReadRecoveryHistory(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	agentID, ses1, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	var agentName string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`,
		agentID).Scan(&agentName); err != nil {
		t.Fatal(err)
	}
	c := Caller{SessionID: ses1, Role: runtime.RoleCoder, AgentID: agentID, AgentName: agentName}
	if _, err := s.call(ctx, c, "swarm_checkpoint", `{"kind":"accepted","summary":"plan"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, c, "swarm_checkpoint",
		`{"kind":"progress","summary":"half","next":["finish"],"blockers":[],"verification":[{"cmd":"go test ./...","ok":true}]}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.call(ctx, c, "swarm_read", `{"recovery":{"agent":"`+agentName+`","limit":10}}`)
	if err != nil {
		t.Fatalf("read err = %v", err)
	}
	rec, ok := out.(map[string]any)["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("read recovery = %v, want the recovery bundle", out)
	}
	cps, ok := rec["checkpoints"].([]any)
	if !ok || len(cps) != 2 {
		t.Fatalf("checkpoints = %v, want 2 with full fields", rec["checkpoints"])
	}
	newest := cps[0].(map[string]any)
	for _, field := range []string{"summary", "next", "blockers", "verification", "session_id", "generation", "kind"} {
		if _, ok := newest[field]; !ok {
			t.Fatalf("checkpoint missing %q: %v", field, newest)
		}
	}
	if newest["summary"] != "half" {
		t.Fatalf("newest summary = %v, want half", newest["summary"])
	}
}

// IMPORTANT 4 (plan: another agent's history denied): the recovery read is
// scoped to the caller itself and its own subtree. A sibling session cannot
// read an unrelated agent's checkpoints, worktrees or requests.
func TestReadRecoveryDeniesAnotherAgentsHistory(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	aID, aSes, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	bID, _, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	var aName, bName string
	s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, aID).Scan(&aName)
	s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, bID).Scan(&bName)
	c := Caller{SessionID: aSes, Role: runtime.RoleCoder, AgentID: aID, AgentName: aName}
	if _, err := s.call(ctx, c, "swarm_read", `{"recovery":{"agent":"`+bName+`"}}`); err == nil {
		t.Fatal("reading another agent's recovery history must be denied")
	}
	if _, err := s.call(ctx, c, "swarm_read", `{"recovery":{"agent":"`+bID+`"}}`); err == nil {
		t.Fatal("reading another agent's recovery history by id must be denied")
	}
	if _, err := s.call(ctx, c, "swarm_read", `{"recovery":{"agent":"`+aName+`"}}`); err != nil {
		t.Fatalf("reading your own recovery history: %v", err)
	}
}

// IMPORTANT 4: checkpoints sharing one created_at millisecond page exactly
// once through the (created_at, id) cursor returned as next_cursor.
func TestReadRecoveryPagesTiedTimestampsExactlyOnce(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	aID, aSes, itemID := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	var aName string
	s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, aID).Scan(&aName)
	const total = 205
	for i := 0; i < total; i++ {
		ts := int64(1000 + i/10) // ten checkpoints per millisecond
		if _, err := s.RT.DB.ExecContext(ctx, `INSERT INTO checkpoints
			(id, session_id, agent_id, item_id, kind, attempt, summary, created_at)
			VALUES (?, ?, ?, ?, 'progress', 1, 's', ?)`, ids.New("ckp"), aSes, aID, itemID, ts); err != nil {
			t.Fatal(err)
		}
	}
	c := Caller{SessionID: aSes, Role: runtime.RoleCoder, AgentID: aID, AgentName: aName}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		args := `{"recovery":{"agent":"` + aName + `","limit":50`
		if cursor != "" {
			args += `,"cursor":"` + cursor + `"`
		}
		out, err := s.call(ctx, c, "swarm_read", args+`}}`)
		if err != nil {
			t.Fatal(err)
		}
		rec := out.(map[string]any)["recovery"].(map[string]any)
		for _, cp := range rec["checkpoints"].([]any) {
			id := cp.(map[string]any)["checkpoint_id"].(string)
			if seen[id] {
				t.Fatalf("checkpoint %s returned twice", id)
			}
			seen[id] = true
		}
		next, _ := rec["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != total {
		t.Fatalf("paged %d checkpoints, want all %d exactly once", len(seen), total)
	}
}

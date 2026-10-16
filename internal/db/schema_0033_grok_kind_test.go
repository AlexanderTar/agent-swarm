package db

import (
	"reflect"
	"strings"
	"testing"
)

// TestGrokMigrationKeepsColumns pins 0033's agents rebuild: a row written
// before the migration keeps every column (compared by name, so a reordered
// rebuild is caught), the indexes and foreign keys survive, and the widened
// kind CHECK accepts 'grok' while still rejecting bogus kinds.
func TestGrokMigrationKeepsColumns(t *testing.T) {
	raw := openFixtureAtVersion(t, 32)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(query, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_root', 'EPIC-1', 'epic', 'itm_root', 'root epic', 'ready', 1, 1)`)
	exec(`INSERT INTO items (id, key, type, parent_id, root_id, title, status, created_at, updated_at)
		VALUES ('itm_task', 'TASK-1', 'task', 'itm_root', 'itm_root', 'test task', 'ready', 2, 2)`)

	// Every column non-NULL and distinct, plus a child row whose
	// parent_agent_id points at the first.
	const cols = `id, name, kind, model, effort, role, item_id, root_item_id,
			parent_agent_id, advisor_kind, advisor_model, advisor_effort, advisor_mode,
			brief, state, preflight_error, created_at, finished_at, role_overrides,
			auto_restart, kind_reason, advisor_requested_effort, low_token`
	exec(`INSERT INTO agents (` + cols + `)
		VALUES ('agt_1', 'agent-one', 'claude', 'claude-3-opus', 'high', 'coder', 'itm_task', 'itm_root',
			NULL, 'codex', 'gpt-5', 'low', 'native',
			'brief one', 'active', 'preflight one', 100, 200, '{"a":1}',
			0, 'reason one', 'xhigh', 1)`)
	exec(`INSERT INTO agents (` + cols + `)
		VALUES ('agt_2', 'agent-two', 'agy', 'gemini', 'medium', 'reviewer', 'itm_task', 'itm_root',
			'agt_1', 'cursor', 'gpt-4o', 'high', 'simulated',
			'brief two', 'finished', 'preflight two', 150, 250, '{"b":2}',
			1, 'reason two', 'low', 0)`)

	beforeAgents := namedRowSnapshot(t, raw, `SELECT * FROM agents ORDER BY id`)
	beforeIdx := indexList(t, raw, "agents")
	beforeFK := foreignKeyList(t, raw, "agents")
	if len(beforeFK) == 0 {
		t.Fatal("agents has no foreign keys before migrating; fixture is wrong")
	}

	continueMigratingTo(t, raw, 32, 33)

	if got := namedRowSnapshot(t, raw, `SELECT * FROM agents ORDER BY id`); !reflect.DeepEqual(beforeAgents, got) {
		t.Errorf("agents rows changed across the rebuild:\nbefore: %v\nafter:  %v", beforeAgents, got)
	}
	if got := indexList(t, raw, "agents"); !reflect.DeepEqual(beforeIdx, got) {
		t.Errorf("agents indexes changed:\nbefore: %+v\nafter:  %+v", beforeIdx, got)
	}
	if got := foreignKeyList(t, raw, "agents"); !reflect.DeepEqual(beforeFK, got) {
		t.Errorf("agents foreign keys changed:\nbefore: %+v\nafter:  %+v", beforeFK, got)
	}
	if n := foreignKeyCheckViolations(t, raw); n != 0 {
		t.Errorf("PRAGMA foreign_key_check reported %d violation(s), want 0", n)
	}

	insert := func(id, kind string) error {
		_, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
			VALUES (?, ?, ?, 'm', 'coder', 'itm_task', 'itm_root', 'b', 'active', 1)`, id, "n-"+id, kind)
		return err
	}
	for _, kind := range []string{"claude", "codex", "agy", "cursor", "muse", "grok", "fake"} {
		if err := insert("agt_"+kind, kind); err != nil {
			t.Errorf("kind %q should be allowed: %v", kind, err)
		}
	}
	if err := insert("agt_bogus", "bogus"); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("kind 'bogus' should still violate the CHECK constraint, err = %v", err)
	}
}

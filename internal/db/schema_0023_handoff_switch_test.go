package db

import "testing"

func TestHandoffSwitchMigrationAddsColumnDefaultEmpty(t *testing.T) {
	raw := openFixtureAtVersion(t, 22)
	if _, err := raw.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_progress', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'o', 'fake', 'fake-1', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, generation, created_at, updated_at)
		VALUES ('op_1', 'agt_1', 'handoff', 'succeeded', 'k', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	continueMigratingTo(t, raw, 22, 23)
	var sw string
	if err := raw.QueryRow(`SELECT switch_json FROM agent_operations WHERE id = 'op_1'`).Scan(&sw); err != nil {
		t.Fatal(err)
	}
	if sw != "" {
		t.Fatalf("existing row switch_json = %q, want ''", sw)
	}
}

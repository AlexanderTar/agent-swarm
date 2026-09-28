package db

import "testing"

func TestOrchestratorTodosMigrationAddsBothColumnsAndKeepsRows(t *testing.T) {
	raw := openFixtureAtVersion(t, 20)
	if _, err := raw.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_progress', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	before := tableRowCount(t, raw, "items")
	continueMigratingTo(t, raw, 20, 21)
	for table, col := range map[string]string{"checkpoints": "todos_json", "sessions": "todos_sent_hash"} {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s.%s missing after 0021", table, col)
		}
	}
	if after := tableRowCount(t, raw, "items"); after != before {
		t.Errorf("items rows = %d, want %d", after, before)
	}
}

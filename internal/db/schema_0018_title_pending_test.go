package db

import "testing"

func TestTitlePendingMigrationDefaultsExistingItemsToNotPending(t *testing.T) {
	raw := openFixtureAtVersion(t, 17)
	if _, err := raw.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_progress', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	continueMigratingTo(t, raw, 17, 18)
	var pending int
	if err := raw.QueryRow(`SELECT title_pending FROM items WHERE id = 'itm_1'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("title_pending = %d, want 0", pending)
	}
}

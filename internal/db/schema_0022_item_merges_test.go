package db

import "testing"

func TestItemMergesMigrationCreatesTableAndUnfreezesOpenAccepts(t *testing.T) {
	raw := openFixtureAtVersion(t, 21)
	for _, q := range []string{
		`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
			VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_review', 1, 1)`,
		`INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at) VALUES
			('req_open', 'accept_epic', 'itm_1', 'p', 'open', '{"item_revision":1,"question":"Q","header":"H"}', 1),
			('req_done', 'accept_epic', 'itm_1', 'p', 'approved', '{"item_revision":1,"question":"Q","header":"H"}', 1)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	before := tableRowCount(t, raw, "requests")
	continueMigratingTo(t, raw, 21, 22)
	for _, col := range []string{"id", "item_id", "integrated_checkpoint", "repo", "repo_id", "kind", "url", "number",
		"base", "head", "auto_merge", "state", "checks", "merged_sha", "checked_at", "created_at"} {
		var n int
		raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('item_merges') WHERE name = ?`, col).Scan(&n)
		if n != 1 {
			t.Errorf("item_merges.%s missing", col)
		}
	}
	var q, h any
	raw.QueryRow(`SELECT json_extract(binding_json,'$.question'), json_extract(binding_json,'$.header') FROM requests WHERE id='req_open'`).Scan(&q, &h)
	if q != nil || h != nil {
		t.Errorf("open accept still frozen: %v %v", q, h)
	}
	raw.QueryRow(`SELECT json_extract(binding_json,'$.question') FROM requests WHERE id='req_done'`).Scan(&q)
	if q != "Q" {
		t.Errorf("approved row touched: %v", q)
	}
	if after := tableRowCount(t, raw, "requests"); after != before {
		t.Errorf("requests rows = %d, want %d", after, before)
	}
}

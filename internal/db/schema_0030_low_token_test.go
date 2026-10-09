package db

import "testing"

func TestMigrationAddsLowTokenColumns(t *testing.T) {
	raw := openFixtureAtVersion(t, 28)
	continueMigratingTo(t, raw, 28, 29)
	for _, c := range []struct{ table, col string }{
		{"agents", "low_token"}, {"sessions", "context_tokens"},
		{"sessions", "context_window"}, {"sessions", "context_strikes"},
	} {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s.%s missing", c.table, c.col)
		}
	}
}

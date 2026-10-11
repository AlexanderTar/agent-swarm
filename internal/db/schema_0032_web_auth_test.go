package db

import "testing"

func TestMigrationAddsWebAuthTables(t *testing.T) {
	raw := openFixtureAtVersion(t, 31)
	continueMigratingTo(t, raw, 31, 32)
	for _, c := range []struct{ table, col string }{
		{"web_auth", "password_hash"}, {"web_auth", "email"},
		{"web_sessions", "token_hash"}, {"web_sessions", "expires_at"}, {"web_sessions", "last_seen"},
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

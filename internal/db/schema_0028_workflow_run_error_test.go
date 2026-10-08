package db

import "testing"

func TestWorkflowRunErrorMigrationAddsNullableColumn(t *testing.T) {
	raw := openFixtureAtVersion(t, 27)
	continueMigratingTo(t, raw, 27, 28)
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('workflow_runs') WHERE name IN ('error', 'error_fatal')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("workflow_runs.error columns = %d, want 2", n)
	}
}

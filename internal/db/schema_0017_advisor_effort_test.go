package db

import "testing"

func TestAdvisorRequestedEffortMigrationBackfillsExistingAgents(t *testing.T) {
	raw := openFixtureAtVersion(t, 16)
	if _, err := raw.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_progress', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, mode, effort string }{
		{"agt_sim", "simulated", "high"},
		{"agt_native", "native", "high"},
	} {
		if _, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id,
			advisor_kind, advisor_model, advisor_effort, advisor_mode, brief, state, created_at)
			VALUES (?, ?, 'claude', 'sonnet', 'coder', 'itm_1', 'itm_1', 'claude', 'fable', NULLIF(?, ''), ?, 'brief', 'active', 1)`,
			row.id, row.id, row.effort, row.mode); err != nil {
			t.Fatal(err)
		}
	}
	continueMigratingTo(t, raw, 16, 17)
	for _, row := range []struct{ id, requested, effective string }{
		{"agt_sim", "high", "high"}, {"agt_native", "high", ""},
	} {
		var requested, effective string
		if err := raw.QueryRow(`SELECT COALESCE(advisor_requested_effort, ''), COALESCE(advisor_effort, '') FROM agents WHERE id = ?`, row.id).Scan(&requested, &effective); err != nil {
			t.Fatal(err)
		}
		if requested != row.requested || effective != row.effective {
			t.Errorf("%s requested/effective effort = %q/%q, want %q/%q", row.id, requested, effective, row.requested, row.effective)
		}
	}
}

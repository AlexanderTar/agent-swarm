package db

import (
	"strings"
	"testing"
)

// Live incident (2026-09-25): untagged runtime.GitRef/Verify marshaled
// uppercase keys (Repo/SHA/Cmd/OK) into checkpoints.git_json/verify_json and
// accept-request binding_json. The web approval page reads lowercase
// (g.sha), so opening an accept request crashed on sha.slice. Tags fix new
// writes; this proves a database parked at version 9 gets its existing rows
// rewritten to lowercase on its next open.
//
// Moved here from db_test.go (same name, same assertions) when the
// main-branch merge put migrations after 0010: the old downgrade
// user_version trick replays every later migration too, so the fixture is
// built at version 9 with the migration helpers instead.
func TestMigration0010LowercasesGitVerifyKeys(t *testing.T) {
	raw := openFixtureAtVersion(t, 9) // pre-0010 shape
	exec := func(stmt string) {
		t.Helper()
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, stmt)
		}
	}
	exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'test epic', 'in_review', 0, 0)`)
	exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'agt_1', 'claude', 'm', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 0)`)
	exec(`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_1', 'agt_1', 1, 1, 'tok', 'tm', '/tmp', 'running', 'neutral', 0)`)
	exec(`INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, git_json, verify_json, created_at)
		VALUES ('ckp_1', 'ses_1', 'agt_1', 'itm_1', 'integrated', 1, 's',
			'[{"Repo":"r","Branch":"b","SHA":"abc","Dirty":false}]',
			'[{"Cmd":"c","Phase":"green","OK":true,"Note":"n"}]', 0)`)
	exec(`INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at)
		VALUES ('req_1', 'accept_epic', 'itm_1', 'p', 'open',
			'{"item_revision":1,"integrated_checkpoint":"ckp_1","git":[{"Repo":"r","Branch":"b","SHA":"abc","Dirty":false}]}', 0)`)

	continueMigrating(t, raw, 9) // applies 0010 onward, like a real reopen

	for _, q := range []struct {
		name, query string
	}{
		{"checkpoints.git_json", `SELECT git_json FROM checkpoints WHERE id = 'ckp_1'`},
		{"checkpoints.verify_json", `SELECT verify_json FROM checkpoints WHERE id = 'ckp_1'`},
		{"requests.binding_json", `SELECT binding_json FROM requests WHERE id = 'req_1'`},
	} {
		var body string
		if err := raw.QueryRow(q.query).Scan(&body); err != nil {
			t.Fatal(err)
		}
		for _, upper := range []string{`"Repo":`, `"Branch":`, `"SHA":`, `"Dirty":`, `"Cmd":`, `"Phase":`, `"OK":`, `"Note":`} {
			if strings.Contains(body, upper) {
				t.Errorf("%s still contains %s: %s", q.name, upper, body)
			}
		}
	}
	var body string
	if err := raw.QueryRow(`SELECT binding_json FROM requests WHERE id = 'req_1'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, lower := range []string{`"repo":`, `"branch":`, `"sha":`} {
		if !strings.Contains(body, lower) {
			t.Errorf("binding_json missing %s: %s", lower, body)
		}
	}
	var v int
	raw.QueryRow("PRAGMA user_version").Scan(&v)
	if v != SchemaVersion {
		t.Fatalf("user_version = %d, want %d after catching up", v, SchemaVersion)
	}
}

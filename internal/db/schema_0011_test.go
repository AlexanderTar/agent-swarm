package db

import (
	"strings"
	"testing"
)

// TestMigration0011Schema pins down every column, table, index and CHECK
// spec B1 (plus B3's items.units_json/solo and B7's workflows.extra_rounds)
// calls for.
func TestMigration0011Schema(t *testing.T) {
	raw := openFixtureAtVersion(t, 10) // post-0010, pre-0011
	continueMigrating(t, raw, 10)      // applies 0011

	exec := func(query string, args ...any) error {
		t.Helper()
		_, err := raw.Exec(query, args...)
		return err
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if err := exec(query, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, query)
		}
	}

	mustExec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'TASK-1', 'task', 'itm_1', 'test item', 'ready', 1, 1)`)
	mustExec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'agent-1', 'claude', 'claude-3', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 1)`)

	// items gains workflow_json, steps_json, units_json, solo, verify_json.
	if err := exec(`UPDATE items SET workflow_json = '{}', steps_json = '[]', units_json = '[]', solo = 'reason', verify_json = '[]' WHERE id = 'itm_1'`); err != nil {
		t.Fatalf("items new columns: %v", err)
	}

	// checkpoints gains verdict (CHECK'd) and findings_json.
	mustExec(`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_1', 'agt_1', 1, 1, 'tok_1', 'tmux_1', '/tmp', 'running', 'neutral', 1)`)
	if err := exec(`INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, verdict, findings_json, created_at)
		VALUES ('ckp_1', 'ses_1', 'agt_1', 'itm_1', 'completed', 1, 's', 'pass', '[]', 1)`); err != nil {
		t.Fatalf("checkpoints.verdict/findings_json: %v", err)
	}
	if err := exec(`INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, verdict, created_at)
		VALUES ('ckp_bad', 'ses_1', 'agt_1', 'itm_1', 'completed', 1, 's', 'bogus', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("checkpoints.verdict should reject 'bogus', err = %v", err)
	}
	if err := exec(`INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, created_at)
		VALUES ('ckp_null', 'ses_1', 'agt_1', 'itm_1', 'completed', 2, 's', 1)`); err != nil {
		t.Fatalf("checkpoints.verdict should allow NULL: %v", err)
	}

	// workflows table, including extra_rounds (B7).
	if err := exec(`INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds, worktrees_json, created_at, updated_at)
		VALUES ('wf_1', 'itm_1', 'itm_1', 'agt_1', 'running', 1, 0, '[]', 1, 1)`); err != nil {
		t.Fatalf("workflows insert: %v", err)
	}
	if err := exec(`INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, worktrees_json, created_at, updated_at)
		VALUES ('wf_bad', 'itm_1', 'itm_1', 'agt_1', 'bogus', '[]', 1, 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("workflows.state should reject 'bogus', err = %v", err)
	}
	var extraRounds int
	if err := raw.QueryRow(`SELECT extra_rounds FROM workflows WHERE id = 'wf_1'`).Scan(&extraRounds); err != nil {
		t.Fatalf("workflows.extra_rounds: %v", err)
	}

	// round and extra_rounds default to 1 and 0 when omitted.
	if err := exec(`INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, worktrees_json, created_at, updated_at)
		VALUES ('wf_defaults', 'itm_1', 'itm_1', 'agt_1', 'failed', '[]', 1, 1)`); err != nil {
		t.Fatalf("workflows insert omitting round/extra_rounds: %v", err)
	}
	var round, defaultExtraRounds int
	if err := raw.QueryRow(`SELECT round, extra_rounds FROM workflows WHERE id = 'wf_defaults'`).Scan(&round, &defaultExtraRounds); err != nil {
		t.Fatalf("workflows round/extra_rounds defaults: %v", err)
	}
	if round != 1 || defaultExtraRounds != 0 {
		t.Fatalf("workflows round/extra_rounds defaults = %d/%d, want 1/0", round, defaultExtraRounds)
	}

	// workflow_runs table, its UNIQUE key and its agent index.
	if err := exec(`INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES ('run_1', 'wf_1', 'build', 1, 'coder', 'agt_1', 'active', 1)`); err != nil {
		t.Fatalf("workflow_runs insert: %v", err)
	}
	if err := exec(`INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES ('run_dup', 'wf_1', 'build', 1, 'coder', 'agt_1', 'active', 1)`); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("workflow_runs (workflow_id, step_id, round, role) should be unique, err = %v", err)
	}
	if err := exec(`INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES ('run_bad', 'wf_1', 'build', 2, 'coder', 'agt_1', 'bogus', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("workflow_runs.state should reject 'bogus', err = %v", err)
	}
	var n int
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'workflow_runs_agent'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("workflow_runs_agent index missing: n=%d err=%v", n, err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'workflows_one_live'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("workflows_one_live index missing: n=%d err=%v", n, err)
	}
}

// TestOneLiveWorkflowPerItem pins down the partial unique index
// workflows_one_live: at most one 'running' or 'escalated' workflow row per
// item, but any number of terminal ones.
func TestOneLiveWorkflowPerItem(t *testing.T) {
	raw := openFixtureAtVersion(t, 10)
	continueMigrating(t, raw, 10)

	exec := func(query string, args ...any) error {
		t.Helper()
		_, err := raw.Exec(query, args...)
		return err
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if err := exec(query, args...); err != nil {
			t.Fatalf("fixture insert failed: %v\n%s", err, query)
		}
	}
	mustExec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'TASK-1', 'task', 'itm_1', 'test item', 'ready', 1, 1)`)
	mustExec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'agent-1', 'claude', 'claude-3', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 1)`)

	insertWorkflow := func(id, state string) error {
		return exec(`INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, worktrees_json, created_at, updated_at)
			VALUES (?, 'itm_1', 'itm_1', 'agt_1', ?, '[]', 1, 1)`, id, state)
	}

	if err := insertWorkflow("wf_failed", "failed"); err != nil {
		t.Fatalf("a terminal workflow should always be allowed: %v", err)
	}
	if err := insertWorkflow("wf_running_1", "running"); err != nil {
		t.Fatalf("the first live (running) workflow should be allowed: %v", err)
	}
	if err := insertWorkflow("wf_running_2", "running"); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("a second running workflow for the same item should be rejected, err = %v", err)
	}
	if err := insertWorkflow("wf_escalated", "escalated"); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("escalated also counts as live and should collide with the running one, err = %v", err)
	}

	// Once the live workflow ends, a new one may start.
	mustExec(`UPDATE workflows SET state = 'succeeded' WHERE id = 'wf_running_1'`)
	if err := insertWorkflow("wf_running_3", "running"); err != nil {
		t.Fatalf("a new running workflow should be allowed once the prior one is terminal: %v", err)
	}
	if err := insertWorkflow("wf_failed_2", "failed"); err != nil {
		t.Fatalf("a second failed workflow alongside a running one should be allowed: %v", err)
	}
}

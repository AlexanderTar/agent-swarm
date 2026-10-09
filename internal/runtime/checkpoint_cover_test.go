package runtime

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

func TestShaMatches(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"", "abcdef0", false},
		{"abcdef0", "", false},
		{"abcdef0", "abcdef0", true},
		{"abcdef0", "abcdef0123456", true}, // an abbreviated sha of 7+ matches its full form
		{"abcdef0123456", "abcdef0", true},
		{"abcdef0", "abcdef1", false},
		{"abc", "abcdef0123", false}, // too short to be a trustworthy abbreviation
		{"abcdef0123", "abc", false},
	}
	for _, c := range cases {
		if got := shaMatches(c.a, c.b); got != c.want {
			t.Errorf("shaMatches(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// seedCheckpointRow writes a raw checkpoint for agent a on its item.
func seedCheckpointRow(t *testing.T, s *Store, a Agent, ses Session, verdict, gitJSON, verifyJSON string) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(), `INSERT INTO checkpoints
		(id, session_id, agent_id, item_id, kind, attempt, summary, git_json, verify_json, verdict, created_at)
		VALUES (?, ?, ?, ?, 'completed', 1, 'seed', ?, ?, NULLIF(?, ''), 1)`,
		ids.New("ckp"), ses.ID, a.ID, a.ItemID, gitJSON, verifyJSON, verdict); err != nil {
		t.Fatal(err)
	}
}

func TestHasIntegrationVerifyPassedReadsCurrentAndPastEvidence(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	check := func(current []Verify, cmd string) bool {
		t.Helper()
		var ok bool
		err := s.tx(ctx, func(tx *sql.Tx) error {
			var err error
			ok, err = s.hasIntegrationVerifyPassed(ctx, tx, w.ItemID, current, cmd)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if check(nil, "make test") {
		t.Fatal("passed with no evidence at all")
	}
	if !check([]Verify{{Cmd: "make test", OK: true}}, "make test") {
		t.Fatal("the checkpoint being written was ignored")
	}
	if check([]Verify{{Cmd: "make test", OK: false}}, "make test") {
		t.Fatal("a failing run counted as a pass")
	}
	// A malformed past row is skipped; a failing one doesn't count; a passing one does.
	seedCheckpointRow(t, s, w, wSes, "", `[]`, `{not json`)
	seedCheckpointRow(t, s, w, wSes, "", `[]`, `[{"cmd":"make test","ok":false}]`)
	if check(nil, "make test") {
		t.Fatal("past failing evidence counted")
	}
	seedCheckpointRow(t, s, w, wSes, "", `[]`, `[{"cmd":"make lint","ok":true},{"cmd":"make test","ok":true}]`)
	if !check(nil, "make test") || check(nil, "make bench") {
		t.Fatal("past evidence must match the exact command")
	}
}

func TestHasFinalReviewPassedMatchesReviewerShas(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	rev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleReviewer, Kind: Fake, Model: "fake-1",
		ParentAgentID: orch.ID, Brief: BriefInput{Objective: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	revSes, err := s.LatestSession(ctx, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	check := func(sha string) bool {
		t.Helper()
		var ok bool
		err := s.tx(ctx, func(tx *sql.Tx) error {
			var err error
			ok, err = s.hasFinalReviewPassed(ctx, tx, w.ItemID, sha)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	const full = "0123456789abcdef0123456789abcdef01234567"

	if check(full) {
		t.Fatal("passed with no reviews")
	}
	// A coder's own passing verdict is not a review; a reviewer's failing one isn't a pass.
	seedCheckpointRow(t, s, w, revSes, "pass", `[{"repo":"proj","sha":"`+full+`"}]`, `[]`)
	seedCheckpointRow(t, s, rev, revSes, "changes_requested", `[{"repo":"proj","sha":"`+full+`"}]`, `[]`)
	if check(full) {
		t.Fatal("a non-reviewer or non-pass checkpoint counted")
	}
	seedCheckpointRow(t, s, rev, revSes, "pass", `not json`, `[]`) // unreadable git refs are skipped
	seedCheckpointRow(t, s, rev, revSes, "pass", `[{"repo":"proj","sha":"deadbee"}]`, `[]`)
	if check(full) {
		t.Fatal("a review of another sha counted")
	}
	seedCheckpointRow(t, s, rev, revSes, "pass", `[{"repo":"proj","sha":"0123456789ab"}]`, `[]`)
	if !check(full) {
		t.Fatal("an abbreviated sha of the integrated commit must count")
	}

	// A reviewer's passing workflow run counts the same way, by its recorded sha.
	const other = "fedcba9876543210fedcba9876543210fedcba98"
	if check(other) {
		t.Fatal("unreviewed sha passed")
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds,
		context_json, worktrees_json, created_at, updated_at) VALUES ('wf_r', ?, ?, ?, 'running', 1, 0, '[]', '[]', 1, 1)`,
		w.ItemID, w.RootItemID, orch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, agent_id, state, verdict, sha, created_at)
		VALUES ('wfr_r', 'wf_r', 'review', 1, 'reviewer', ?, 'completed', 'pass', ?, 1)`, rev.ID, other[:10]); err != nil {
		t.Fatal(err)
	}
	if !check(other) {
		t.Fatal("a passing reviewer workflow run was ignored")
	}
}

func TestCommitGateRefusesDirtyUnknownAndStaleGitEntries(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, _, st, head := startedWorkflow(t, s, gatedBuildReviewSpec(t, 2))
	ses := agentSessionForStep(t, s, st.ID, "build")
	complete := func(git []GitRef) error {
		_, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done", Git: git})
		return err
	}

	for _, c := range []struct {
		name string
		git  []GitRef
		want string
	}{
		{"no git", nil, "Completed needs git"},
		{"claims dirty", []GitRef{{Repo: "proj", Branch: "main", SHA: head, Dirty: true}}, "proj is dirty"},
		{"other repo only", []GitRef{{Repo: "elsewhere", Branch: "main", SHA: head}}, "no git entry for proj"},
		{"stale sha", []GitRef{{Repo: "proj", Branch: "main", SHA: "0000000000000000000000000000000000000000"}}, "checkpoint says 0000000"},
	} {
		err := complete(c.git)
		if !isBadRequest(err, c.want) {
			t.Errorf("%s: err = %v, want bad request containing %q", c.name, err, c.want)
		}
	}
	// None of the refusals recorded a sha or advanced the workflow.
	var sha sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT sha FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&sha); err != nil || sha.Valid {
		t.Fatalf("run sha = %v err=%v, want unset after refusals", sha, err)
	}
	if err := complete([]GitRef{{Repo: "proj", Branch: "main", SHA: head[:12]}}); err != nil {
		t.Fatalf("an abbreviated but unambiguous sha must pass: %v", err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT sha FROM workflow_runs WHERE workflow_id = ? AND step_id = 'build'`, st.ID).Scan(&sha); err != nil || sha.String != head {
		t.Fatalf("run sha = %v err=%v, want the full HEAD %s", sha, err, head)
	}
}

func TestResearchNotesGateRequiresAndRegistersNotes(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	spec, err := workflow.Resolve(workflow.Spec{Steps: []workflow.Step{
		{ID: "research", Run: "researcher", Gates: []workflow.Gate{workflow.GateArtifactNotes}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, st, _ := startedWorkflow(t, s, spec)
	ses := agentSessionForStep(t, s, st.ID, "research")
	dir := filepath.Join(s.Home, "research", "EPIC-1")
	notes := filepath.Join(dir, "findings.md")
	complete := func(artifacts ...string) error {
		_, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done", Artifacts: artifacts})
		return err
	}

	// No artifacts, a path outside the root's research dir, and a missing file are all refused.
	for _, arts := range [][]string{nil, {"/etc/hosts"}, {notes}} {
		if err := complete(arts...); !isBadRequest(err, "research notes in artifacts (under "+dir) {
			t.Fatalf("artifacts %v: err = %v, want the research-notes refusal naming %s", arts, err, dir)
		}
	}
	if err := wantCount(s, `SELECT COUNT(*) FROM artifacts WHERE kind = 'research'`, 0); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, []byte("# Findings\nIt works.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := complete(notes); err != nil {
		t.Fatal(err)
	}
	if err := wantCount(s, `SELECT COUNT(*) FROM artifacts WHERE kind = 'research' AND path = ?`, 1, notes); err != nil {
		t.Fatal(err)
	}
}

package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// worker spawns a coder on TASK-1 under an orchestrator and returns both sessions.
func worker(t *testing.T, s *Store) (orch Agent, w Agent, wSes Session) {
	t.Helper()
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	w, _, err = s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "build it"}})
	if err != nil {
		t.Fatal(err)
	}
	wSes, err = s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	return orch, w, wSes
}

func TestAcceptedStartsTheItemAndRelaysToTheParent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemStatus != items.InProgress {
		t.Fatalf("item status = %s", res.ItemStatus)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'`, orch.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("relay count = %d, want exactly one", n)
	}
	if got := lastNotified(t, s).Kind; got != "agent.accepted" {
		t.Fatalf("notification = %q", got)
	}
}

func TestATopLevelOrchestratorGetsNoRelay(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Solo", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "off we go"}); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'`).Scan(&n)
	if n != 0 {
		t.Fatalf("relay count = %d, want 0", n)
	}
}

func TestBlockedThenProgressBlocksAndUnblocksTheItem(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: BlockedCkp,
		Summary: "the signing key needs a passphrase", Blockers: []string{"gpg-agent has no cached passphrase"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemStatus != items.Blocked {
		t.Fatalf("status = %s", res.ItemStatus)
	}
	next, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "unblocked"})
	if err != nil {
		t.Fatal(err)
	}
	if next.ItemStatus != items.InProgress {
		t.Fatalf("status = %s, want the saved status back", next.ItemStatus)
	}
}

// Ported from writeHandoff: a handoff never changes the item status.
func TestHandoffLeavesTheItemStatusAlone(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	before, _ := s.Items.Get(ctx, "TASK-1")
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Handoff,
		Summary: "stopped after the failing test", Next: []string{"make the test pass"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Items.Get(ctx, "TASK-1")
	if before.Status != after.Status {
		t.Fatalf("status went %s → %s", before.Status, after.Status)
	}
}

// L24: the gate, in all four shapes.
func TestTDDGate(t *testing.T) {
	red := Verify{Cmd: "go test ./internal/x -run TestLogin", Phase: "red", OK: false}
	green := Verify{Cmd: "go test ./internal/x -run TestLogin", Phase: "green", OK: true}
	cases := []struct {
		name string
		v    []Verify
		ok   bool
	}{
		{"red then green", []Verify{red, green}, true},
		{"green only", []Verify{green}, false},
		{"green then red", []Verify{green, red}, false},
		{"red then failing green", []Verify{red, {Cmd: green.Cmd, Phase: "green", OK: false}}, false},
		{"nothing at all", nil, false},
	}
	for _, c := range cases {
		s, _, _ := newStore(t)
		ctx := context.Background()
		_, _, wSes := worker(t, s)
		s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
		_, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp,
			Summary: "done", Verification: c.v,
			Git: []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}})
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("%s: should be refused", c.name)
			} else if err.Error() != "TDD evidence missing: record the failing test run (phase: red) before completing." {
				t.Errorf("%s: err = %q", c.name, err)
			}
		}
	}
}

func TestTDDGateSkippedForExemptTasksAndReviewRoles(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	// a reviewer is never gated
	rev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleReviewer, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	rSes, _ := s.LatestSession(ctx, rev.ID)
	if _, err := s.WriteCheckpoint(ctx, rSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "reviewed, two findings sent"}); err != nil {
		t.Fatalf("a reviewer needs no TDD evidence: %v", err)
	}
	// An exempt task is not gated either. Do NOT call worker(t, s) a second time:
	// §5's "at most one orchestrator per top-level item among agents in state
	// queued or active" would refuse the second StartOrchestrator("EPIC-1") and the
	// helper's t.Fatal would fire (D36). Spawn a second coder under the same
	// orchestrator, on the same now-exempt TASK-1, instead. ids.Unique suffixes the
	// generated name to write-the-failing-test-coder-2, which is expected.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET tdd_exempt = 'docs' WHERE key = 'TASK-1'`); err != nil {
		t.Fatal(err)
	}
	w2, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	w2Ses, err := s.LatestSession(ctx, w2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, w2Ses.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "docs updated"}); err != nil {
		t.Fatalf("an exempt task needs no TDD evidence: %v", err)
	}
}

// The evidence carries across a pause and resume inside one attempt.
func TestTDDEvidenceCarriesAcrossGenerations(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "red",
		Verification: []Verify{{Cmd: "go test ./x", Phase: "red", OK: false}}}); err != nil {
		t.Fatal(err)
	}
	// generation 2, same attempt
	next, err := s.startSessionForTest(ctx, w, wSes.Attempt, wSes.Generation+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, next.ID, CheckpointInput{Kind: CompletedCkp, Summary: "green",
		Verification: []Verify{{Cmd: "go test ./x", Phase: "green", OK: true}},
		Git:          []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}}); err != nil {
		t.Fatalf("evidence from an earlier generation of the same attempt counts: %v", err)
	}
}

// I1: item may be any descendant of the assignment, and nothing else.
func TestCheckpointItemMustBeADescendantOfTheAssignment(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	oSes, _ := s.LatestSession(ctx, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, CheckpointInput{Kind: Progress,
		ItemKey: "TASK-1", Summary: "did it myself"}); err != nil {
		t.Fatalf("an orchestrator may checkpoint a descendant: %v", err)
	}
	_, other, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Elsewhere", Intent: "feature", Kind: Fake, Model: "fake-1"})
	_ = other
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, CheckpointInput{Kind: Progress,
		ItemKey: "SPIKE-1", Summary: "not mine"}); err == nil {
		t.Fatal("a checkpoint outside the assignment's subtree must be refused")
	}
}

// C1: integrated is orchestrator-only and needs git plus verification.
func TestIntegratedRequiresGitAndVerificationAndIsOrchestratorOnly(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	oSes, _ := s.LatestSession(ctx, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Integrated,
		Summary: "merged"}); err == nil {
		t.Fatal("a coder cannot write an integrated checkpoint")
	}
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, CheckpointInput{Kind: Integrated,
		Summary: "merged"}); err == nil {
		t.Fatal("integrated needs git and verification")
	}
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:          []GitRef{{Repo: "proj", Branch: "main", SHA: "deadbee"}},
		Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
}

// C3: during a pause only handoff, blocked and failed are accepted.
func TestCheckpointKindsDuringAPause(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'pause_requested' WHERE id = ?`, wSes.ID)
	for _, k := range []CheckpointKind{Accepted, Progress, CompletedCkp, Integrated} {
		if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: k, Summary: "x"}); err == nil ||
			err.Error() != "paused: finish your handoff and stop." {
			t.Errorf("%s during a pause: err = %v", k, err)
		}
	}
	for _, k := range []CheckpointKind{Handoff, BlockedCkp, FailedCkp} {
		if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: k, Summary: "x"}); err != nil {
			t.Errorf("%s must be accepted during a pause: %v", k, err)
		}
		s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'pause_requested' WHERE id = ?`, wSes.ID)
	}
}

func TestProcessedAcksMessagesInTheSameTransaction(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	res, _ := s.Sync(ctx, wSes.ID, nil, 20)
	id := res.Messages[0].MsgID
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted,
		Summary: "starting", Processed: []string{id}}); err != nil {
		t.Fatal(err)
	}
	var state string
	s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE id = ?`, id).Scan(&state)
	if state != "acked" {
		t.Fatalf("state = %q", state)
	}
	// a rejected checkpoint leaves them un-acked
	res2, _ := s.Sync(ctx, wSes.ID, nil, 20)
	if len(res2.Messages) == 0 {
		enq(t, s, w.ID, w.RootItemID, "finding", `{"body":"x"}`, 1)
		res2, _ = s.Sync(ctx, wSes.ID, nil, 20)
	}
	id2 := res2.Messages[0].MsgID
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "no evidence", Processed: []string{id2}})
	s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE id = ?`, id2).Scan(&state)
	if state == "acked" {
		t.Fatal("a refused checkpoint must not ack its processed messages")
	}
}

func TestSummaryLengthIsEnforced(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress,
		Summary: strings.Repeat("x", 501)}); err == nil {
		t.Fatal("a summary over 500 characters must be refused")
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: ""}); err == nil {
		t.Fatal("an empty summary must be refused")
	}
}

// I4: a spike's completed with a resolution opens a close_spike request.
func TestCompletedWithAResolutionOpensCloseSpike(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Nothing to build", Intent: "feature",
		Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "already handled by TASK-98", Resolution: "duplicate_of:TASK-98"}); err != nil {
		t.Fatal(err)
	}
	var kind, state string
	if err := s.DB.QueryRowContext(ctx, `SELECT kind, state FROM requests
		WHERE item_id = (SELECT id FROM items WHERE key = 'SPIKE-1')`).Scan(&kind, &state); err != nil {
		t.Fatal(err)
	}
	if kind != "close_spike" || state != "open" {
		t.Fatalf("request = %s/%s", kind, state)
	}
	it, _ := s.Items.Get(ctx, "SPIKE-1")
	if it.Status != items.AwaitingApproval {
		t.Fatalf("spike status = %s", it.Status)
	}
}

func TestResolutionValidation(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress,
		Summary: "x", Resolution: "no_change"}); err == nil ||
		err.Error() != "resolution is only valid on a completed checkpoint." {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "x", Resolution: "no_change"}); err == nil ||
		err.Error() != "resolution is only valid on a spike." {
		t.Fatalf("err = %v", err)
	}
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "BadRes", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "x", Resolution: "nonsense"}); err == nil ||
		err.Error() != "resolution must be no_change or duplicate_of:<KEY>." {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteCheckpointRefusesAnUnknownSession(t *testing.T) {
	s, _, _ := newStore(t)
	if _, err := s.WriteCheckpoint(context.Background(), "ses_nope", CheckpointInput{Kind: Progress, Summary: "x"}); err == nil {
		t.Fatal("an unknown session must be refused")
	}
}

func TestCheckpointsListsMostRecentFirstAndRespectsLimit(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "still going"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.Checkpoints(ctx, "TASK-1", 0, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Kind != Progress || list[1].Kind != Accepted {
		t.Fatalf("checkpoints = %+v", list)
	}
	limited, err := s.Checkpoints(ctx, "TASK-1", 1, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Kind != Progress {
		t.Fatalf("limited = %+v", limited)
	}
}

// changedFiles is the daemon-computed diff for L24's lenient orchestrator path.
func TestChangedFilesCountsShortstatOutputAndIsLenientOnErrors(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, _ := worker(t, s)
	repoID := seedRepo(t, s, "proj")
	wtPath := t.TempDir()
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktrees
		(id, repo_id, path, branch, base_ref, base_sha, owner_agent_id, root_item_id, state, created_at)
		VALUES (?, ?, ?, 'task/x', 'main', 'abc1234', ?, ?, 'active', ?)`,
		ids.New("wt"), repoID, wtPath, w.ID, w.RootItemID, now); err != nil {
		t.Fatal(err)
	}
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"git -C " + wtPath + " diff --shortstat abc1234..deadbee": {Out: " 2 files changed, 10 insertions(+), 2 deletions(-)\n"},
	}}
	s.Exec = fake.Runner()
	if n := s.changedFiles(ctx, []GitRef{{Repo: "proj", SHA: "deadbee"}}); n != 2 {
		t.Fatalf("changedFiles = %d, want 2", n)
	}
	// a repo with no worktree row counts as zero, the lenient direction
	if n := s.changedFiles(ctx, []GitRef{{Repo: "nope", SHA: "x"}}); n != 0 {
		t.Fatalf("unknown repo = %d, want 0", n)
	}
}

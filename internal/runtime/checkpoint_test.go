package runtime

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
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

// Live incident (2026-09-20): s1-lane-a wrote a `blocked` checkpoint waiting
// on s1-lane-b's item; lane-b's item finished hours later and nothing ever
// told lane-a -- it sat blocked until a human typed into the orchestrator's
// pane. TASK-2 depends on TASK-1 (item_deps); lane A blocks on TASK-2 while
// TASK-1 is still open, then lane B finishes TASK-1. The daemon must relay
// "dependency_added" to their shared orchestrator (mirroring the no_ack/
// crashed relay shape exactly) the moment TASK-1 actually reaches Done, not
// before.
func TestDependencyDoneRelaysToTheOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	s.Items.DepUnblocked = s.OnDepUnblocked // cmd/swarm/daemon.go wires this in production
	ctx := context.Background()
	seedEpicWithTwoTasks(t, s)
	if err := s.Items.AddDep(ctx, "TASK-2", "TASK-1", items.User("board")); err != nil {
		t.Fatal(err)
	}
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	laneB, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "unblock TASK-2"}})
	if err != nil {
		t.Fatal(err)
	}
	laneBSes, err := s.LatestSession(ctx, laneB.ID)
	if err != nil {
		t.Fatal(err)
	}
	laneA, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-2", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "wait on TASK-1"}})
	if err != nil {
		t.Fatal(err)
	}
	laneASes, err := s.LatestSession(ctx, laneA.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.WriteCheckpoint(ctx, laneASes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, laneASes.ID, CheckpointInput{Kind: BlockedCkp,
		Summary: "waiting on TASK-1", Blockers: []string{"TASK-1 isn't done yet"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.WriteCheckpoint(ctx, laneBSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, laneBSes.ID, CheckpointInput{Kind: Progress, Summary: "red",
		Verification: []Verify{{Cmd: "go test ./x", Phase: "red", OK: false}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, laneBSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Verification: []Verify{{Cmd: "go test ./x", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}

	relayCount := func() int {
		var n int
		s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'
			AND payload_json LIKE '%"event":"dependency_added"%'`).Scan(&n)
		return n
	}
	if n := relayCount(); n != 0 {
		t.Fatalf("dependency_added must not fire before TASK-1 actually reaches done: count = %d", n)
	}

	// TASK-1 is only InReview so far (a completed checkpoint on a task moves it
	// to in_review, not done); the daemon/orchestrator still has to accept it.
	if _, err := s.Items.Transition(ctx, "TASK-1", items.Done, items.Daemon()); err != nil {
		t.Fatal(err)
	}

	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"dependency_added"%' AND payload_json LIKE '%"item":"TASK-2"%'
		AND wake_class = 'immediate'`, orch.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("dependency_added relay to the orchestrator = %d, want exactly 1", n)
	}
}

// TASK-2 has no dependency on TASK-1 here, so finishing TASK-1 must not relay
// anything: the fix must key off item_deps, not "some sibling item finished."
func TestDependencyDoneDoesNothingWithoutADependencyEdge(t *testing.T) {
	s, _, _ := newStore(t)
	s.Items.DepUnblocked = s.OnDepUnblocked // cmd/swarm/daemon.go wires this in production
	ctx := context.Background()
	seedEpicWithTwoTasks(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	laneB, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "finish TASK-1"}})
	if err != nil {
		t.Fatal(err)
	}
	laneBSes, err := s.LatestSession(ctx, laneB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, laneBSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, laneBSes.ID, CheckpointInput{Kind: Progress, Summary: "red",
		Verification: []Verify{{Cmd: "go test ./x", Phase: "red", OK: false}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, laneBSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Verification: []Verify{{Cmd: "go test ./x", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Items.Transition(ctx, "TASK-1", items.Done, items.Daemon()); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'
		AND payload_json LIKE '%"event":"dependency_added"%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("dependency_added count = %d, want 0 with no item_deps edge", n)
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

// The deterministic half of "an orchestrator closes children marked
// completed, regardless of the report" (the s11-tool-stubs incident: a coder
// reported done via a finding, the orchestrator completed the item itself,
// and the coder's own session sat 'running' forever because nothing ever
// told it to stop). The orchestrator completing TASK-1 on the coder's behalf
// must close the coder's still-live session out from under it.
func TestCompletedChecksClosesTheOtherLiveSessionOnTheSameItem(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	oSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, CheckpointInput{Kind: CompletedCkp,
		ItemKey: "TASK-1", Summary: "verified and fixed it myself"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != Completed {
		t.Fatalf("coder session state = %s, want completed", after.State)
	}
	wAfter, err := s.Agent(ctx, w.Name)
	if err != nil {
		t.Fatal(err)
	}
	if wAfter.State != AgentFinished {
		t.Fatalf("coder agent state = %s, want finished", wAfter.State)
	}
	found := false
	for _, k := range tm.killed {
		if k == wSes.TmuxName {
			found = true
		}
	}
	if !found {
		t.Fatalf("coder's tmux pane %s was never killed: %v", wSes.TmuxName, tm.killed)
	}
}

// Reproduces the TASK-123 incident (swarm_items update kept rejecting a
// freshly-read revision): a coder's own `completed` checkpoint really does
// bump the item's revision (Ready/InProgress -> InReview), but a SECOND
// `completed` checkpoint on the same item -- e.g. an orchestrator completing
// it too, having missed that the coder already had -- lands on an item
// already InReview, so its own tryTransition is a same-state no-op and the
// revision does NOT move again. WriteCheckpoint's own result must report
// which is which: the caller needs the real number to write its own
// swarm_items update next, not a guess that assumes every completed
// checkpoint bumps revision.
func TestCompletedChecksReportsTheRealRevisionEvenWhenItsOwnTransitionIsANoOp(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})

	res1, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "done", Verification: []Verify{{Cmd: "go test ./..."}},
		Git: []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}})
	if err != nil {
		t.Fatal(err)
	}
	afterCoder, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if res1.ItemRevision != afterCoder.Revision {
		t.Fatalf("coder's own completed: ItemRevision = %d, want %d (the real, just-bumped value)",
			res1.ItemRevision, afterCoder.Revision)
	}

	oSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := s.WriteCheckpoint(ctx, oSes.ID, CheckpointInput{Kind: CompletedCkp,
		ItemKey: "TASK-1", Summary: "verified it myself too"})
	if err != nil {
		t.Fatal(err)
	}
	if res2.ItemRevision != afterCoder.Revision {
		t.Fatalf("orchestrator's redundant completed (a same-state no-op): ItemRevision = %d, want %d unchanged",
			res2.ItemRevision, afterCoder.Revision)
	}
}

// A completed checkpoint an agent writes on its own item must not tear its
// own live session down out from under it: WriteCheckpoint hasn't returned
// yet, so it is still mid-turn.
func TestCompletedChecksNeverClosesTheCallersOwnSession(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	// Spawn's own defensive pre-start Kill (agents.go:1062, "no duplicate
	// session name") already put both fixture agents in tm.killed by this
	// point -- a plain non-empty check would pass for the wrong reason, so
	// this asserts no *new* kill happens instead.
	before := len(tm.killed)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "done", Verification: []Verify{{Cmd: "go test ./..."}},
		Git: []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}}); err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != before {
		t.Fatalf("the caller's own session was torn down: %v", tm.killed[before:])
	}
	after, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != Running {
		t.Fatalf("caller's own session state = %s, want unchanged (running)", after.State)
	}
}

// L24: the generalist verification gate.
func TestVerificationGate(t *testing.T) {
	red := Verify{Cmd: "go test ./internal/x -run TestLogin", Phase: "red", OK: false}
	green := Verify{Cmd: "go test ./internal/x -run TestLogin", Phase: "green", OK: true}
	cases := []struct {
		name    string
		v       []Verify
		ok      bool
		wantErr string // exact message when !ok
	}{
		{"red only", []Verify{red}, true, ""},
		{"green only", []Verify{green}, true, ""},
		{"red then green", []Verify{red, green}, true, ""},
		{"nothing at all", nil, false, verifyMissing},
		{"empty command", []Verify{{Cmd: "  "}}, false,
			verifyMissing + ` 1 verification entry given, but none has a non-empty "cmd" -- each entry needs {"cmd": "...", "ok": true}.`},
		{"wrong field names", []Verify{{}, {}}, false,
			verifyMissing + ` 2 verification entries given, but none has a non-empty "cmd" -- each entry needs {"cmd": "...", "ok": true}.`},
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
			} else if err.Error() != c.wantErr {
				t.Errorf("%s: err = %q, want %q", c.name, err, c.wantErr)
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

// tryTransition's swallow of a denied transition must still leave a trace: the
// checkpoint record and the item's displayed status can now disagree, and an
// operator needs a log line to find out why (matching changedFiles' own
// lenient-but-logged pattern in this file).
func TestTryTransitionLogsADeniedTransitionInsteadOfSilence(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	// TASK-1 is blocked, so completing it as a reviewer asks for an
	// InReview transition that the state machine denies (Blocked only ever
	// unblocks back to StatusBeforeBlock, never straight to InReview) --
	// unlike a merely-still-Ready task, which self-heals to InReview on a
	// real completed checkpoint since 2026-09-22 (items/transition.go) and so
	// no longer denies here.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'blocked', status_before_block = 'ready'
		WHERE key = 'TASK-1'`); err != nil {
		t.Fatal(err)
	}
	rev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleReviewer, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	rSes, _ := s.LatestSession(ctx, rev.ID)
	// worker(t, s) already left a coder assigned to TASK-1 live and unclosed,
	// so this same completed checkpoint also triggers closeCompletedSiblings
	// (checkpoint.go) -- a second, expected log line, not a replacement for
	// the denied-transition one. Capture every line rather than the last.
	var logged []string
	s.Log = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	if _, err := s.WriteCheckpoint(ctx, rSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "reviewed, still ready"}); err != nil {
		t.Fatal(err)
	}
	if len(logged) == 0 {
		t.Fatal("a denied transition must be logged, not silently swallowed")
	}
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "TASK-1") }) {
		t.Fatalf("the log line should name the item, got %q", logged)
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

// seedTopLevelItem creates a single parentless item, readied for a direct
// spawn -- the multi-task-worker case (spec 2026-09-22): the worker's
// assignment is the parent item itself, not one of its tasks.
func seedTopLevelItem(t *testing.T, s *Store, typ items.Type) items.Item {
	t.Helper()
	ctx := context.Background()
	it, err := s.Items.Create(ctx, items.CreateInput{Type: typ, Title: "Ship it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id = ?`, it.ID); err != nil {
		t.Fatal(err)
	}
	return it
}

// assertRefusesCompleted simulates a gated-role (coder) agent already
// assigned to a non-task item from before Spawn's item-type gate existed --
// e.g. still running across a deploy of that fix. It spawns legitimately on
// a real task, then rewrites the agent's own assignment directly (bypassing
// Spawn, the only thing that would otherwise refuse this), and confirms
// WriteCheckpoint still refuses a completed checkpoint on the wrong item.
// This is what's left of the guard after runtime.Spawn's item-type gate
// became the primary fix: defense-in-depth for that transitional window,
// not something a normal, freshly-spawned coder can ever hit.
func assertRefusesCompleted(t *testing.T, s *Store, target items.Item) {
	t.Helper()
	ctx := context.Background()
	seedEpicWithTask(t, s) // EPIC-1 > STORY-1 > TASK-1, all ready
	w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "cover several tasks"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET item_id = ? WHERE id = ?`, target.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	wSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "all done"})
	if err == nil {
		t.Fatalf("a completed checkpoint on %s must be refused", target.Key)
	}
	ie, ok := err.(*items.Error)
	if !ok || ie.Code != items.CodeBadRequest {
		t.Fatalf("err = %v, want a CodeBadRequest items.Error", err)
	}
	if !strings.Contains(ie.Message, "Completed checkpoints attach to tasks") {
		t.Fatalf("message = %q", ie.Message)
	}
}

// I-completed-gate (spec 2026-09-22): STORY-27 got stuck because a
// multi-task worker's completed checkpoint silently defaulted onto its Story
// assignment -- accepted (200 OK), but the transition switch has no case for
// Story/Epic/Bug, so nothing moved and the mistake was invisible. A completed
// checkpoint from a gated role must be refused outright on any item type
// with no consumer for it, forcing the caller to pass item: "<TASK-KEY>".
func TestWriteCheckpointRefusesCompletedOnAStory(t *testing.T) {
	s, _, _ := newStore(t)
	seedEpicWithTask(t, s) // EPIC-1 > STORY-1 > TASK-1, all ready
	story, err := s.Items.Get(context.Background(), "STORY-1")
	if err != nil {
		t.Fatal(err)
	}
	assertRefusesCompleted(t, s, story)
}

func TestWriteCheckpointRefusesCompletedOnAnEpic(t *testing.T) {
	s, _, _ := newStore(t)
	it := seedTopLevelItem(t, s, items.Epic)
	assertRefusesCompleted(t, s, it)
}

func TestWriteCheckpointRefusesCompletedOnABug(t *testing.T) {
	s, _, _ := newStore(t)
	it := seedTopLevelItem(t, s, items.Bug)
	assertRefusesCompleted(t, s, it)
}

// P0 (2026-09-23): nothing required an orchestrator to have produced a plan
// before completing its own epic -- POST /api/items lets a human create an
// epic directly (no spike), so brainstorming/writing-plans could be skipped
// entirely. This closes that gap at the one place every root item's
// completion already passes through.
func TestCompletedOnEpicRequiresARegisteredPlan(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"})
	if err == nil {
		t.Fatal("expected completed to be refused with no registered plan")
	}
	const want = `completed requires a registered plan for this epic. Register one with swarm_artifact register, or set tdd_exempt if this genuinely needs neither.`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestCompletedOnEpicSucceedsOnceAPlanIsRegistered(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", it.Key, "plan", writeFile(t, planBody), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"}); err != nil {
		t.Fatalf("completed must succeed once a plan is registered: %v", err)
	}
}

func TestCompletedOnBugRequiresARegisteredDebugReport(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Bug)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "bug closed"})
	if err == nil {
		t.Fatal("expected completed to be refused with no registered debug_report")
	}
	const want = `completed requires a registered debug_report for this bug. Register one with swarm_artifact register, or set tdd_exempt if this genuinely needs neither.`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestCompletedOnBugSucceedsOnceADebugReportIsRegistered(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Bug)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", it.Key, "debug_report", writeFile(t, reportBody), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "bug closed"}); err != nil {
		t.Fatalf("completed must succeed once a debug_report is registered: %v", err)
	}
}

func TestCompletedOnChoreNeedsNoArtifact(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Chore)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "chore done"}); err != nil {
		t.Fatalf("a chore root must never require an artifact: %v", err)
	}
}

func TestCompletedOnTddExemptEpicNeedsNoArtifact(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	// items.Store.Update refuses tdd_exempt on anything but a Task ("Only
	// tasks can be TDD-exempt.") -- that restriction is unrelated to this
	// gate and out of scope to change, so set it directly the same way
	// TestTDDGateSkippedForExemptTasksAndReviewRoles (line ~418) already does.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET tdd_exempt = 'spike-research' WHERE id = ?`, it.ID); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"}); err != nil {
		t.Fatalf("a tdd_exempt epic must never require an artifact: %v", err)
	}
}

// Regression guard for the bug the first version of this gate introduced:
// completed is the universal session-terminal checkpoint (every role ends
// its assignment with completed or failed; terminalCheckpointKind reads it
// to close the session as "completed" rather than "crashed"). An orchestrator
// ending its own Epic, and a reviewer ending a story-wide review, must not
// be blocked -- only gatedRoles are.
func TestWriteCheckpointAllowsOrchestratorCompletedOnItsOwnEpic(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", it.Key, "plan", writeFile(t, planBody), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"}); err != nil {
		t.Fatalf("an orchestrator must be able to end its own epic with completed: %v", err)
	}
}

func TestWriteCheckpointAllowsReviewerCompletedOnAStory(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s) // EPIC-1 > STORY-1 > TASK-1, all ready
	w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "STORY-1", Role: RoleReviewer, Kind: Fake,
		Model: "fake-1", Brief: BriefInput{Objective: "review the story"}})
	if err != nil {
		t.Fatal(err)
	}
	wSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "review done"}); err != nil {
		t.Fatalf("a reviewer must be able to end a story-wide review with completed: %v", err)
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
// This also doubles as the Spike exemption for the completed-item-type gate
// (spec 2026-09-22): Spike is the one non-Task type a completed checkpoint
// may still land on, because its resolution is how a spike reports
// no_change/duplicate_of:<KEY>.
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

func TestBlockedCheckpointOpensHITLRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "BlockMe", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)

	// Writing Blocked checkpoint with blockers opens a HITL request
	_, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{
		Kind:     BlockedCkp,
		Summary:  "Database migration failed",
		Blockers: []string{"Need manual DB password", "Schema mismatch"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var reqID, kind string
	var isHITL int
	var options string
	err = s.DB.QueryRowContext(ctx, `SELECT id, kind, is_hitl, options_json FROM requests WHERE item_id = ?`, a.ItemID).
		Scan(&reqID, &kind, &isHITL, &options)
	if err != nil {
		t.Fatalf("expected request to be created: %v", err)
	}
	if kind != "blocker" || isHITL != 1 {
		t.Fatalf("expected kind=blocker, is_hitl=1; got kind=%s, is_hitl=%d", kind, isHITL)
	}
	if !strings.Contains(options, "Need manual DB password") {
		t.Fatalf("expected options to contain blocker, got %s", options)
	}
}

// The assignment duplicates the kickoff brief, and SKILL.md rule 1 makes the
// agent write `accepted` right after its first sync, so accepting acks it.
// Only a delivered assignment: one the agent never synced stays pending.
func TestAcceptedAcksADeliveredAssignmentOnly(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	state := func() string {
		var st string
		s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE to_agent_id = ? AND kind = 'assignment'`, w.ID).Scan(&st)
		return st
	}
	if _, err := s.Sync(ctx, wSes.ID, nil, 20); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "delivered" {
		t.Fatalf("assignment after sync = %q, want delivered", got)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "acked" {
		t.Fatalf("assignment after accepted = %q, want acked", got)
	}
}

func TestAcceptedLeavesAnUnreadAssignmentPending(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	var st string
	s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE to_agent_id = ? AND kind = 'assignment'`, w.ID).Scan(&st)
	if st != "pending" {
		t.Fatalf("an assignment the agent never synced must stay pending, got %q", st)
	}
}

func TestParentedBlockedCheckpointRelaysButOpensNoRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: BlockedCkp,
		Summary: "signing key needs a passphrase", Blockers: []string{"gpg-agent has no cached passphrase"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemStatus != items.Blocked {
		t.Fatalf("item status = %s, want blocked", res.ItemStatus)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE is_hitl = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("HITL rows = %d, want 0", n)
	}
	var payload string
	if err := s.DB.QueryRow(`SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%gpg-agent%'`, orch.ID).Scan(&payload); err != nil {
		t.Fatalf("parent got no relay with the blocker: %v", err)
	}
}

// While the parent's kind is confirmed exhausted, a checkpoint relay must be
// held (one suppressed_relays row), not enqueued.
func TestCheckpointRelayHeldWhileExhausted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	s.Usage = fakeUsage{Fake: true}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway"}); err != nil {
		t.Fatal(err)
	}
	var relays, rows int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'`, orch.ID).Scan(&relays)
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays WHERE agent_id = ? AND event = 'progress'`, orch.ID).Scan(&rows)
	if relays != 0 {
		t.Fatalf("relay count = %d, want 0 held while exhausted", relays)
	}
	if rows != 1 {
		t.Fatalf("suppressed rows = %d, want 1", rows)
	}
}

// --- Unit 8.1: verdicts and findings (spec B5) ---

func TestVerdictRequiredForWorkflowReviewer(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	setItemWorkflow(t, s, "TASK-1", workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder", Gates: []workflow.Gate{workflow.GateVerify}},
		{ID: "review", Review: []string{"reviewer"}, Of: "build"},
	}})
	rev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleReviewer, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	rSes, _ := s.LatestSession(ctx, rev.ID)
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	seedWorkflowRun(t, s, it.ID, orch.RootItemID, orch.ID, rev.ID, "review", "reviewer", 1)

	_, err = s.WriteCheckpoint(ctx, rSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "reviewed"})
	if err == nil {
		t.Fatal("expected a verdict-required error")
	}
	want := `Reviewers must complete with verdict: pass, changes_requested or blocked.`
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}

	if _, err := s.WriteCheckpoint(ctx, rSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "reviewed",
		Verdict: "pass"}); err != nil {
		t.Fatalf("a valid verdict should be accepted: %v", err)
	}
}

func TestVerdictRefusedForCoder(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	_, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
		Verification: []Verify{{Cmd: "go test ./..."}},
		Git:          []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}},
		Verdict:      "pass"})
	if err == nil {
		t.Fatal("expected a verdict-refused error")
	}
	if want := "Only reviewers set a verdict."; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}

func TestPassVerdictRefusesMajorFindings(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	setItemWorkflow(t, s, "TASK-1", workflow.Spec{Steps: []workflow.Step{
		{ID: "build", Run: "coder"},
		{ID: "review", Review: []string{"reviewer"}, Of: "build"},
	}})
	rev, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleReviewer, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	rSes, _ := s.LatestSession(ctx, rev.ID)
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	seedWorkflowRun(t, s, it.ID, orch.RootItemID, orch.ID, rev.ID, "review", "reviewer", 1)

	_, err = s.WriteCheckpoint(ctx, rSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "reviewed",
		Verdict:  "pass",
		Findings: []workflow.Finding{{Severity: "major", File: "a.go", Line: 3, Summary: "leaks a file handle"}}})
	if err == nil {
		t.Fatal("expected a pass-with-major-finding error")
	}
	if want := "verdict pass can't carry critical or major findings."; err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}

	// A minor finding is fine with pass.
	if _, err := s.WriteCheckpoint(ctx, rSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "reviewed",
		Verdict:  "pass",
		Findings: []workflow.Finding{{Severity: "minor", File: "a.go", Line: 3, Summary: "nit"}}}); err != nil {
		t.Fatalf("pass with only a minor finding should be accepted: %v", err)
	}
}

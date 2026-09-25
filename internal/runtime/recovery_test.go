package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// TestSyncRecoverySurvivesAckedAssignment pins the successor-recovery
// contract: every generation's first sync carries the durable assignment
// plus the recovery bundle (operation, predecessor session), independent of
// acked inbox rows. A later sync of the same session carries the assignment
// but no recovery bundle.
func TestSyncRecoverySurvivesAckedAssignment(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}

	// Generation 1 never replaced: assignment present, no recovery bundle.
	gen1, err := s.SyncRecovery(ctx, wSes.ID)
	if err != nil {
		t.Fatalf("gen1 SyncRecovery err = %v", err)
	}
	if gen1.Assignment.ItemKey != "TASK-1" {
		t.Fatalf("gen1 assignment item = %q, want TASK-1", gen1.Assignment.ItemKey)
	}
	if gen1.Recovery != nil {
		t.Fatalf("gen1 recovery = %+v, want nil (never replaced)", gen1.Recovery)
	}
	if !gen1.FirstSync {
		t.Fatal("gen1 FirstSync = false, want true")
	}

	// Ack every inbox row, then replace: the successor's first sync must
	// still carry both assignment and recovery.
	syncRes, err := s.Sync(ctx, wSes.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ack []string
	for _, m := range syncRes.Messages {
		ack = append(ack, m.MsgID)
	}
	if _, err := s.Sync(ctx, wSes.ID, ack, 0); err != nil {
		t.Fatal(err)
	}

	panes(tm, Pane{Session: wSes.TmuxName})
	op, err := s.RequestReplacement(ctx, w.ID, ModeRecover, "rec1", "")
	if err != nil {
		t.Fatalf("request err = %v", err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatalf("resume err = %v", err)
	}
	succ, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.Generation != wSes.Generation+1 {
		t.Fatalf("successor generation = %d, want %d", succ.Generation, wSes.Generation+1)
	}

	first, err := s.SyncRecovery(ctx, succ.ID)
	if err != nil {
		t.Fatalf("successor SyncRecovery err = %v", err)
	}
	if !first.FirstSync {
		t.Fatal("successor FirstSync = false, want true")
	}
	if first.Assignment.ItemKey != "TASK-1" {
		t.Fatalf("successor assignment item = %q, want TASK-1", first.Assignment.ItemKey)
	}
	if first.Assignment.Brief == "" {
		t.Fatal("successor assignment brief empty, want the durable brief")
	}
	if first.Recovery == nil {
		t.Fatal("successor recovery nil, want the recovery bundle")
	} else {
		if first.Recovery.OperationID != op.ID {
			t.Fatalf("recovery op = %q, want %q", first.Recovery.OperationID, op.ID)
		}
		if first.Recovery.PredecessorSessionID != wSes.ID {
			t.Fatalf("recovery predecessor = %q, want %q", first.Recovery.PredecessorSessionID, wSes.ID)
		}
	}

	second, err := s.SyncRecovery(ctx, succ.ID)
	if err != nil {
		t.Fatalf("second SyncRecovery err = %v", err)
	}
	if second.FirstSync {
		t.Fatal("second FirstSync = true, want false")
	}
	if second.Assignment.ItemKey != "TASK-1" {
		t.Fatalf("second assignment item = %q, want TASK-1 (survives acks)", second.Assignment.ItemKey)
	}
	if second.Recovery != nil {
		t.Fatalf("second recovery = %+v, want nil", second.Recovery)
	}
}

// TestRecoveryHistoryAcrossGenerations pins the successor's history read:
// agent-scoped checkpoint history spans generations with full fields and
// provenance (session, generation), paginates with a stable cursor, and
// resource discovery names the agent's worktrees and artifact
// revisions/hashes so the successor re-checks disk HEADs before editing.
func TestRecoveryHistoryAcrossGenerations(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Running); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "gen1 plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "gen1 half done"}); err != nil {
		t.Fatal(err)
	}

	panes(tm, Pane{Session: wSes.TmuxName})
	if _, err := s.RequestReplacement(ctx, w.ID, ModeRecover, "rec-hist", ""); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	succ, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, succ.ID, Running); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, succ.ID, CheckpointInput{Kind: Progress, Summary: "gen2 picks up"}); err != nil {
		t.Fatal(err)
	}

	hist, err := s.RecoveryHistory(ctx, w.ID, 10, time.Time{})
	if err != nil {
		t.Fatalf("RecoveryHistory err = %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3 across two generations", len(hist))
	}
	if hist[0].Summary != "gen2 picks up" || hist[0].Generation != succ.Generation {
		t.Fatalf("newest = %+v, want gen2 checkpoint with successor generation", hist[0])
	}
	if hist[0].SessionID != succ.ID {
		t.Fatalf("newest session = %q, want %q", hist[0].SessionID, succ.ID)
	}
	if hist[2].Summary != "gen1 plan" || hist[2].Generation != wSes.Generation {
		t.Fatalf("oldest = %+v, want gen1 accepted with predecessor generation", hist[2])
	}
	if hist[2].SessionID != wSes.ID {
		t.Fatalf("oldest session = %q, want %q", hist[2].SessionID, wSes.ID)
	}
	for _, c := range hist {
		if c.AgentID != w.ID || c.Kind == "" {
			t.Fatalf("checkpoint %+v missing full fields/provenance", c)
		}
	}

	// Stable cursor: everything strictly before the middle checkpoint.
	page, err := s.RecoveryHistory(ctx, w.ID, 10, hist[1].CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Summary != "gen1 plan" {
		t.Fatalf("cursor page = %+v, want just the gen1 plan", page)
	}

	// Resource discovery: a worktree and an artifact revision owned by the
	// agent are visible with revisions/hashes for the HEAD re-check.
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at)
		VALUES ('repo_hist', 'repo1', '/tmp/repo1', 'main', 'manual', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktrees (id, repo_id, path, branch, base_ref, base_sha, state, owner_agent_id, root_item_id, created_at)
		VALUES ('wt_hist', 'repo_hist', '/tmp/repo1-wt', 'branch-1', 'main', 'abc1234', 'active', ?, ?, ?)`,
		w.ID, it.RootID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifacts
		(id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES ('art_hist', ?, 'spec', 'docs/plan.md', 2, ?, ?)`, it.ID, w.ID, now); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []struct {
		n int
		h string
	}{ {1, "aaa"}, {2, "bbb"} } {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifact_revisions
			(artifact_id, revision, sha256, content, sections_json, created_at)
			VALUES ('art_hist', ?, ?, 'body', '[]', ?)`, rev.n, rev.h, now); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.RecoveryResources(ctx, w.ID)
	if err != nil {
		t.Fatalf("RecoveryResources err = %v", err)
	}
	if len(res.Worktrees) != 1 || res.Worktrees[0].Path != "/tmp/repo1-wt" || res.Worktrees[0].BaseSHA != "abc1234" {
		t.Fatalf("worktrees = %+v, want the owned worktree with its recorded HEAD", res.Worktrees)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].HeadRevision != 2 {
		t.Fatalf("artifacts = %+v, want art_hist at revision 2", res.Artifacts)
	}
	got := map[int]string{}
	for _, r := range res.Artifacts[0].Revisions {
		got[r.Revision] = r.SHA256
	}
	if got[1] != "aaa" || got[2] != "bbb" {
		t.Fatalf("revisions = %+v, want both hashes", res.Artifacts[0].Revisions)
	}
}

// Spec §4 prompts are normative templates: these goldens pin pause,
// fresh handoff, resumed history, broken predecessor, live-children
// orchestrator and reviewer wordings. New notices never carry [swarm].
func TestPauseNoticeGolden(t *testing.T) {
	got := PauseHandoffNotice("PAUSE", "login-coder", "TASK-101")
	want := "PAUSE requested for login-coder (TASK-101). Stop taking new work and call swarm_sync. " +
		"Safely finish or interrupt your current operation, collect its status, and preserve your work. " +
		"You may use tools needed to save files, wait for or stop your own commands, inspect git, " +
		"commit task-owned changes, and write the handoff manifest/checkpoint. Do not delegate, start " +
		"another workflow step, push, deploy, or report the assignment completed. If preservation fails, " +
		"record the blocker and surviving paths; do not claim a clean handoff. Pause: wait for Resume. " +
		Preamble
	if got != want {
		t.Fatalf("pause notice mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFreshHandoffKickoffGolden(t *testing.T) {
	got := SuccessorKickoff("login-coder", RoleCoder, "TASK-101", "Build it", "handoff")
	if !startsWith(got, "You are swarm agent login-coder (coder) for TASK-101: Build it, continuing in a fresh session after handoff. ") {
		t.Fatalf("kickoff head mismatch: %q", got)
	}
	for _, want := range []string{"Call swarm_sync first", "Continue the unfinished unit and next action",
		"do not repeat completed work", Preamble} {
		if !contains(got, want) {
			t.Fatalf("kickoff missing %q: %q", want, got)
		}
	}
	if contains(got, "[swarm]") {
		t.Fatalf("kickoff must not add [swarm]: %q", got)
	}
}

func TestResumedHistoryGolden(t *testing.T) {
	got := SuccessorKickoff("login-coder", RoleCoder, "TASK-101", "Build it", "resume") + " " + ResumeAddition
	for _, want := range []string{"resuming", "durable state wins"} {
		if !contains(got, want) {
			t.Fatalf("resume kickoff missing %q: %q", want, got)
		}
	}
}

func TestBrokenPredecessorGolden(t *testing.T) {
	got := BrokenPredecessorWarning([]string{"/tmp/repo1-wt"}, []string{"ckp_9"})
	for _, want := range []string{"incomplete recovery", "/tmp/repo1-wt", "ckp_9",
		"never reset/clean", "invent test results"} {
		if !contains(got, want) {
			t.Fatalf("broken-predecessor warning missing %q: %q", want, got)
		}
	}
}

func TestLiveChildrenOrchestratorGolden(t *testing.T) {
	got := OrchestratorHandoffAddition([]string{"lane-a", "lane-b"})
	for _, want := range []string{"lane-a", "lane-b", "keep running", "without fabricating their checkpoints"} {
		if !contains(got, want) {
			t.Fatalf("orchestrator addition missing %q: %q", want, got)
		}
	}
}

func TestReviewerKickoffGolden(t *testing.T) {
	got := SuccessorKickoff("ui-1", RoleReviewer, "TASK-101", "Review it", "handoff")
	for _, want := range []string{"ui-1", "reviewer", "swarm-reviewer", "Call swarm_sync first"} {
		if !contains(got, want) {
			t.Fatalf("reviewer kickoff missing %q: %q", want, got)
		}
	}
}

func startsWith(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

package runtime

// Batch 4 worker-lifecycle contract tests (F11 relay revision, F3/F4/F5/F14
// lifecycle guards). The Batch 1-2 harnesses (worker, newStore) are reused
// additively; no existing lifecycle behavior is redefined here.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// TestRelayCarriesFinalItemRevision (F11): the parent relay for a
// checkpoint carries the item's final revision after cascading
// transitions — the same revision WriteCheckpoint reports — so the
// observing parent sees current state without a revision:latest shortcut.
func TestRelayCarriesFinalItemRevision(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)

	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "done", Verification: []Verify{{Cmd: "go test ./..."}},
		Git: []GitRef{{Repo: "proj", Branch: "task/task-1", SHA: "abc1234"}}})
	if err != nil {
		t.Fatal(err)
	}
	final, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemRevision != final.Revision {
		t.Fatalf("result revision = %d, current = %d", res.ItemRevision, final.Revision)
	}
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' ORDER BY seq DESC LIMIT 1`, orch.ID).Scan(&payload); err != nil {
		t.Fatalf("parent got no checkpoint relay: %v", err)
	}
	var body struct {
		Event        string `json:"event"`
		ItemRevision *int   `json:"item_revision"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatal(err)
	}
	if body.Event != string(CompletedCkp) {
		t.Fatalf("relay event = %q, want completed", body.Event)
	}
	if body.ItemRevision == nil {
		t.Fatal("relay carries no item_revision")
	}
	if *body.ItemRevision != final.Revision {
		t.Fatalf("relay item_revision = %d, want final %d", *body.ItemRevision, final.Revision)
	}
	if final.Status != items.InReview {
		t.Fatalf("status = %s, want in_review", final.Status)
	}
}

// TestCompletedBlockedByOpenQuestion (F4): a completed checkpoint claims
// the assignment is done, so an open HITL question owned by the same agent
// refuses it naming the request. Answering unblocks; other kinds (progress)
// are never gated.
func TestCompletedBlockedByOpenQuestion(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "BlockedQ", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	req, err := s.AskQuestion(ctx, ses.ID, "Ship it?", []string{"Yes", "No"})
	if err != nil {
		t.Fatal(err)
	}
	completed := CheckpointInput{Kind: CompletedCkp, Summary: "done"}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, completed); err == nil ||
		!strings.Contains(err.Error(), req.ID) {
		t.Fatalf("completed with an open question = %v, want refusal naming %s", err, req.ID)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID,
		CheckpointInput{Kind: Progress, Summary: "still working"}); err != nil {
		t.Fatalf("progress with an open question must stay allowed: %v", err)
	}
	if _, err := s.Answer(ctx, req.ID, "Yes", "board"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, completed); err != nil {
		t.Fatalf("completed after the answer = %v, want nil", err)
	}
}

// TestPreservationRefusesDestructiveCwd (F3): rm -r/rmdir against the
// workspace itself or an ancestor is refused; ordinary subdir cleanup,
// file removal and unrelated trees stay allowed.
func TestPreservationRefusesDestructiveCwd(t *testing.T) {
	cwd := "/tmp/w/login-coder"
	refused := []struct{ cmd, dir string }{
		{"rm -rf /", ""},
		{"rm -rf ~", ""},
		{"rm -rf $HOME", ""},
		{"rm -rf /tmp/w/login-coder", cwd},
		{"rm -rf /tmp/w/login-coder/", cwd},
		{"rm -rf .", cwd},
		{"rm -fr /tmp/w", cwd},
		{"rmdir -p /tmp/w/login-coder", cwd},
		{"rmdir -p /tmp/w", cwd},
		{"go test ./...; rm -rf /tmp/w/login-coder", cwd},
	}
	for _, tc := range refused {
		if err := PreservationCommandInDirAllowed(tc.cmd, tc.dir); err == nil {
			t.Errorf("PreservationCommandInDirAllowed(%q, %q) = nil, want refusal", tc.cmd, tc.dir)
		} else if !strings.Contains(err.Error(), "preservation: ") {
			t.Errorf("PreservationCommandInDirAllowed(%q) error = %q, want preservation-prefixed", tc.cmd, err)
		}
	}
	allowed := []struct{ cmd, dir string }{
		{"rm -rf /tmp/w/login-coder/build", cwd},
		{"rmdir build", cwd},
		{"rm /tmp/w/login-coder/notes.txt", cwd},
		{"rm -rf /tmp/other", cwd},
		{"rm -rf /tmp/x", ""},
		{"git -C /tmp/w/login-coder commit -m fix", cwd},
		{"go test ./...", cwd},
	}
	for _, tc := range allowed {
		if err := PreservationCommandInDirAllowed(tc.cmd, tc.dir); err != nil {
			t.Errorf("PreservationCommandInDirAllowed(%q, %q) = %v, want nil", tc.cmd, tc.dir, err)
		}
	}
	if err := PreservationCommandAllowed("rm -rf /"); err == nil {
		t.Error("PreservationCommandAllowed(rm -rf /) = nil, want refusal without any cwd")
	}
}

// TestCommandHandleRetentionContract (F5): declared command handles must be
// non-blank, unique and bounded; an empty list (nothing to retain) is valid.
func TestCommandHandleRetentionContract(t *testing.T) {
	if err := ValidateCommandHandles(nil); err != nil {
		t.Fatalf("ValidateCommandHandles(nil) = %v, want nil", err)
	}
	if err := ValidateCommandHandles([]string{"cmd_1", "cmd_2"}); err != nil {
		t.Fatalf("ValidateCommandHandles(valid) = %v, want nil", err)
	}
	for name, handles := range map[string][]string{
		"blank":     {"cmd_1", "  "},
		"duplicate": {"cmd_1", "cmd_1"},
		"too long":  {strings.Repeat("x", 257)},
		"control":   {"cmd_\x01"},
	} {
		if err := ValidateCommandHandles(handles); err == nil {
			t.Errorf("ValidateCommandHandles(%s) = nil, want refusal", name)
		}
	}
	if err := ValidateCommandHandles(make([]string, 65)); err == nil {
		t.Error("ValidateCommandHandles(65 blanks) = nil, want refusal")
	}
}

// TestScratchCleanupTargetsExactIDs (F14): completion cleanup targets only
// exact IDs the agent owns in this session. Predecessor-session, shared,
// unlabelled and task-data IDs are never touched.
func TestScratchCleanupTargetsExactIDs(t *testing.T) {
	owned := []ScratchResource{
		{ID: "ctr_a", AgentID: "ag1", SessionID: "ses2"},
		{ID: "ctr_b", AgentID: "ag1", SessionID: "ses2"},
		{ID: "ctr_old", AgentID: "ag1", SessionID: "ses1"},
		{ID: "ctr_shared", AgentID: "ag1", SessionID: "ses2", Shared: true},
		{ID: "ctr_other", AgentID: "ag2", SessionID: "ses2"},
	}
	got := ScratchCleanupTargets("ag1", "ses2", owned,
		[]string{"ctr_a", "ctr_b", "ctr_a", "ctr_old", "ctr_shared", "ctr_other", "ctr_ghost", "task-data-vol"})
	want := []string{"ctr_a", "ctr_b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ScratchCleanupTargets = %q, want %q", got, want)
	}
}

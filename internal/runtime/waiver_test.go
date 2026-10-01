package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

const hintCopy = " An orchestrator can waive this gate with a reason."

// seedWaiver stores waivers on key the way an orchestrator's swarm_items call would.
func seedWaiver(t *testing.T, s *Store, key string, gates ...string) {
	t.Helper()
	ws := make([]items.Waiver, len(gates))
	for i, g := range gates {
		ws[i] = items.Waiver{Gate: g, Reason: "test", Agent: "agt_orch"}
	}
	b, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE items SET waivers_json = ? WHERE key = ?`,
		string(b), key); err != nil {
		t.Fatal(err)
	}
}

func TestWaivedGateRefusalsCarryTheHint(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		gate workflow.Gate
		want string
	}{
		"tdd":    {workflow.GateTDD, tddMissingRound},
		"verify": {workflow.GateVerify, "Declared verify commands not recorded as passing: go test ./..." + "." + hintCopy},
		"commit": {workflow.GateCommit, "Completed needs git: [{repo, branch, sha, dirty:false}]." + hintCopy},
	} {
		s, _, _ := newStore(t)
		_, ses, _ := buildOnly(t, s, c.gate)
		setItemVerify(t, s, "TASK-1", "go test ./...")
		_, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestWaivedTddAndVerifyLetCompletedPass(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	_, ses, _ := buildOnly(t, s, workflow.GateTDD, workflow.GateVerify)
	setItemVerify(t, s, "TASK-1", "go test ./...")
	seedWaiver(t, s, "TASK-1", "tdd")
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"}); err == nil ||
		!strings.Contains(err.Error(), "Declared verify commands") {
		t.Fatalf("tdd waived but verify not: err = %v, want the verify refusal", err)
	}
	seedWaiver(t, s, "TASK-1", "tdd", "verify")
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"}); err != nil {
		t.Fatalf("both waived, completed should pass: %v", err)
	}
}

func TestWaivedCommitGateStillRecordsHead(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	coder, ses, workflowID := buildOnly(t, s, workflow.GateCommit)
	dir, head := seedCommitRepo(t, s, coder)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedWaiver(t, s, "TASK-1", "commit")
	// No git entry and a dirty tree: the waived gate passes, but the run still
	// gets HEAD, or workflow/next.go escalates "completed without a sha".
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"}); err != nil {
		t.Fatalf("waived commit gate should pass: %v", err)
	}
	var stored string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(sha, '') FROM workflow_runs WHERE workflow_id = ?`,
		workflowID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != head {
		t.Fatalf("workflow_runs.sha = %q, want HEAD %q", stored, head)
	}
}

func TestWaivedDesignArtifactGate(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	setItemWorkflow(t, s, "TASK-1", workflow.Spec{Steps: []workflow.Step{
		{ID: "design", Run: "designer", Gates: []workflow.Gate{workflow.GateArtifactDesign}},
		{ID: "review", Review: []string{"ui_reviewer"}, Of: "design"},
	}})
	designer, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleDesigner, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "design it"}})
	if err != nil {
		t.Fatal(err)
	}
	dSes, err := s.LatestSession(ctx, designer.ID)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	seedWorkflowRun(t, s, it.ID, orch.RootItemID, orch.ID, designer.ID, "design", "designer", 1)
	if _, err := s.WriteCheckpoint(ctx, dSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "designed"}); err == nil ||
		!strings.HasSuffix(err.Error(), hintCopy) {
		t.Fatalf("unwaived err = %v, want the waive hint", err)
	}
	seedWaiver(t, s, "TASK-1", "artifact:design")
	if _, err := s.WriteCheckpoint(ctx, dSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "designed"}); err != nil {
		t.Fatalf("waived artifact:design should pass: %v", err)
	}
}

func TestWaivedOpenQuestionsAndRequiredArtifact(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	it := seedTopLevelItem(t, s, items.Epic)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: it.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.AskQuestion(ctx, ses.ID, "Ship it?", []string{"Yes", "No"})
	if err != nil {
		t.Fatal(err)
	}
	done := CheckpointInput{Kind: CompletedCkp, Summary: "epic accepted"}
	if _, err := s.WriteCheckpoint(ctx, ses.ID, done); err == nil || !strings.Contains(err.Error(), req.ID) ||
		!strings.HasSuffix(err.Error(), hintCopy) {
		t.Fatalf("open question err = %v, want it to name %s and carry the hint", err, req.ID)
	}
	seedWaiver(t, s, it.Key, "open_questions")
	_, err = s.WriteCheckpoint(ctx, ses.ID, done)
	want := "completed requires a registered plan for this epic. Register one with swarm_artifact register, " +
		"or set tdd_exempt if this genuinely needs neither." + hintCopy
	if err == nil || err.Error() != want {
		t.Fatalf("after open_questions waiver: err = %v, want %q", err, want)
	}
	seedWaiver(t, s, it.Key, "open_questions", "required_artifact")
	if _, err := s.WriteCheckpoint(ctx, ses.ID, done); err != nil {
		t.Fatalf("both waived, completed should pass: %v", err)
	}
}

func TestIntegratedHonoursRootWaiversAndWaiveInput(t *testing.T) {
	ctx := context.Background()
	setup := func() (*Store, Session) {
		s, _, _ := newStore(t)
		orch, _, _ := worker(t, s)
		oSes, err := s.LatestSession(ctx, orch.ID)
		if err != nil {
			t.Fatal(err)
		}
		setItemWorkflow(t, s, "EPIC-1", workflow.Spec{Integration: &workflow.Integration{
			Verify: []string{"go test ./..."}, FinalReview: []string{"reviewer"}}})
		if _, err := s.DB.ExecContext(ctx, "UPDATE items SET tdd_exempt = 'docs' WHERE key = 'EPIC-1'"); err != nil {
			t.Fatal(err)
		}
		return s, oSes
	}
	integrated := CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:          []GitRef{{Repo: "proj", Branch: "main", SHA: "abcdef123456"}},
		Verification: []Verify{{Cmd: "make lint", Phase: "green", OK: true}}}

	s, oSes := setup()
	_, err := s.WriteCheckpoint(ctx, oSes.ID, integrated)
	if err == nil || err.Error() != "Integration verify not recorded as passing: go test ./..."+"."+hintCopy {
		t.Fatalf("unwaived err = %v", err)
	}
	seedWaiver(t, s, "EPIC-1", "integration_verify")
	_, err = s.WriteCheckpoint(ctx, oSes.ID, integrated)
	if err == nil || err.Error() != "Integration needs a passing final review of abcdef1."+hintCopy {
		t.Fatalf("integration_verify waived only: err = %v", err)
	}
	seedWaiver(t, s, "EPIC-1", "integration_verify", "final_review")
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, integrated); err != nil {
		t.Fatalf("root waivers should let integrated pass: %v", err)
	}

	// waive on the checkpoint itself records root waivers and applies at once.
	s, oSes = setup()
	in := integrated
	in.Waive = []Waiver{{Gate: "integration_verify", Reason: "ci down"}, {Gate: "final_review", Reason: "solo repo"}}
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, in); err != nil {
		t.Fatalf("integrated with waive should pass: %v", err)
	}
	root, err := s.Items.Get(ctx, "EPIC-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Waivers) != 2 || root.Waivers[0].Reason != "ci down" {
		t.Fatalf("root waivers = %+v", root.Waivers)
	}
	// An unknown gate or empty reason on the input is refused with the items copy.
	s, oSes = setup()
	in.Waive = []Waiver{{Gate: "lint", Reason: "x"}}
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, in); err == nil || !strings.Contains(err.Error(), "Unknown gate lint") {
		t.Fatalf("unknown gate err = %v", err)
	}
	in.Waive = []Waiver{{Gate: "final_review", Reason: ""}}
	if _, err := s.WriteCheckpoint(ctx, oSes.ID, in); err == nil || err.Error() != "Give a reason (1–300 characters)." {
		t.Fatalf("empty reason err = %v", err)
	}
}

// A task with no workflow run falls back to verifyOK; a tdd or verify waiver lifts it too.
func TestWaivedTddLetsLegacyCompletedPass(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	_, _, ses := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"}); err == nil {
		t.Fatal("legacy completed with no verification should be refused")
	}
	seedWaiver(t, s, "TASK-1", "tdd")
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done"}); err != nil {
		t.Fatalf("tdd waived, legacy completed should pass: %v", err)
	}
}

func TestWaiveOnlyOnIntegrated(t *testing.T) {
	s, _, _ := newStore(t)
	_, _, ses := worker(t, s)
	_, err := s.WriteCheckpoint(context.Background(), ses.ID, CheckpointInput{Kind: Progress, Summary: "x",
		Waive: []Waiver{{Gate: "tdd", Reason: "x"}}})
	if err == nil || err.Error() != "waive is only valid on an integrated checkpoint." {
		t.Fatalf("err = %v", err)
	}
}

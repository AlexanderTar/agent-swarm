package runtime

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// driveChangesRequested runs the seeded workflow, answering every review with
// changes_requested, until it escalates or maxRounds rounds have been
// reviewed. hook runs after each reviewed round (1-based). It returns the
// escalation text ("" if never escalated) and the rounds reviewed.
func driveChangesRequested(t *testing.T, s *Store, orch Agent, taskKey string, maxRounds int, hook func(round int)) (string, int) {
	t.Helper()
	ctx := context.Background()
	wtID, _, _, head := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= maxRounds; round++ {
		coderSes := agentSessionForStep(t, s, st.ID, "build")
		if _, err := s.WriteCheckpoint(ctx, coderSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
			Git: []GitRef{{Repo: "proj", Branch: "main", SHA: head}}}); err != nil {
			t.Fatal(err)
		}
		reviewerSes := agentSessionForStep(t, s, st.ID, "review")
		if _, err := s.WriteCheckpoint(ctx, reviewerSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "nope",
			Verdict:  "changes_requested",
			Findings: []workflow.Finding{{Severity: "major", File: "a.go", Summary: "bug"}}}); err != nil {
			t.Fatal(err)
		}
		if hook != nil {
			hook(round)
		}
		var state, escalation string
		if err := s.DB.QueryRowContext(ctx, `SELECT state, COALESCE(escalation, '') FROM workflows WHERE id = ?`, st.ID).
			Scan(&state, &escalation); err != nil {
			t.Fatal(err)
		}
		if state == "escalated" {
			return escalation, round
		}
	}
	return "", maxRounds
}

func TestEngineClampsWhenOwnerLowToken(t *testing.T) {
	s, _, _ := newStore(t)
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	esc, rounds := driveChangesRequested(t, s, orch, taskKey, 3, nil)
	if rounds != 2 || esc != "review rounds exhausted (2/2)" {
		t.Fatalf("rounds=%d escalation=%q, want escalation after round 2 (2/2)", rounds, esc)
	}
}

func TestEngineClampsOnMidFlightToggle(t *testing.T) {
	s, _, _ := newStore(t)
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	esc, rounds := driveChangesRequested(t, s, orch, taskKey, 3, func(round int) {
		if round == 1 {
			mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
		}
	})
	_ = esc
	if rounds != 2 {
		t.Fatalf("rounds=%d, want escalation after round 2 once toggled on", rounds)
	}
}

func TestEngineUnclampedWhenOff(t *testing.T) {
	s, _, _ := newStore(t)
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	esc, rounds := driveChangesRequested(t, s, orch, taskKey, 3, nil)
	if rounds != 3 || esc != "review rounds exhausted (3/3)" {
		t.Fatalf("rounds=%d escalation=%q, want escalation after round 3", rounds, esc)
	}
}

func TestLowTokenRootIntegrationListsOneFinalReviewer(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, workflow.Spec{
		Integration: &workflow.Integration{FinalReview: []string{"reviewer", "ui_reviewer"}}})
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.finalReviewRoles(ctx, it, orch.ID)
	if err != nil || !reflect.DeepEqual(got, []string{"reviewer", "ui_reviewer"}) {
		t.Fatalf("off: roles=%v err=%v", got, err)
	}
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	got, err = s.finalReviewRoles(ctx, it, orch.ID)
	if err != nil || !reflect.DeepEqual(got, []string{"ui_reviewer"}) {
		t.Fatalf("on: roles=%v err=%v", got, err)
	}
}

func TestRunSpecIsClampedForOwner(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, taskKey := seedWorkflowTask(t, s, gatedBuildReviewSpec(t, 3))
	wtID, _, _, _ := seedOwnedRepoWorktree(t, s, orch)
	st, err := s.StartWorkflow(ctx, orch, StartWorkflowInput{ItemKey: taskKey,
		Worktrees: []WorkflowWorktree{{WorktreeID: wtID, Mode: "rw"}}})
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, taskKey)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowRun{WorkflowID: st.ID}
	spec, err := s.runSpec(ctx, it, run)
	if err != nil || spec.Retries != it.Workflow.Retries && spec.Retries != nil {
		t.Fatalf("off: spec=%+v err=%v", spec, err)
	}
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	spec, err = s.runSpec(ctx, it, run)
	if err != nil || spec.Retries == nil || *spec.Retries != 0 {
		t.Fatalf("on: spec=%+v err=%v, want clamped retries 0", spec, err)
	}
}

// Engine reads of the workflow spec must go through engineSpec.
func TestNoRawSpecReadsInCheckpoint(t *testing.T) {
	src, err := os.ReadFile("checkpoint.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"stepFor(it.Workflow", "findFixStepsFor(it.Workflow", "it.Workflow.Integration.Verify"} {
		if strings.Contains(string(src), bad) {
			t.Errorf("checkpoint.go reads the raw spec via %q; use engineSpec", bad)
		}
	}
}

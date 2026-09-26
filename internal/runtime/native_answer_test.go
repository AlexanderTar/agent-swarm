package runtime

import (
	"context"
	"strings"
	"testing"
)

// seedApprovalWithNativePrompt is a small fixture for Task 13c: an
// orchestrator with a registered spec artifact and an open approve_section
// request, its NativePrompt already computed via a real Ask call.
func seedApprovalWithNativePrompt(t *testing.T) (s *Store, ses string, req Request) {
	t.Helper()
	s, _, _ = newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Native answer", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses = mustSessionID(t, s, a.ID)
	p := writeFile(t, "# Spec\n\n## Data model\n\nrows\n")
	res, err := s.RegisterArtifact(ctx, ses, "register", "SPIKE-1", "spec", p, "")
	if err != nil {
		t.Fatal(err)
	}
	sec := res.Sections[0]
	req, err = s.Ask(ctx, ses, AskInput{Kind: "approval", Prompt: "Review " + sec.Title,
		ArtifactID: res.ArtifactID, SectionID: sec.ID})
	if err != nil {
		t.Fatal(err)
	}
	return s, ses, req
}

// hookSimulate replays what the claude PreToolUse/PostToolUse hooks do for a
// native prompt shown verbatim: open the bound question row, then resolve it
// with the terminal's answer text (or "" for an adapter with no answer text,
// which ResolveQuestionByPrompt turns into "Resolved in terminal" via the
// same fallback handler.go's PostToolUse path uses).
func hookSimulate(t *testing.T, s *Store, ses string, np NativePrompt, answer string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.AskQuestion(ctx, ses, np.Question, np.Options); err != nil {
		t.Fatal(err)
	}
	if answer == "" {
		answer = "Resolved in terminal"
	}
	if err := s.ResolveQuestionByPrompt(ctx, ses, np.Question, answer); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAnswerApprovesOnlyWithMatchingEvidence(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()

	// 1. No evidence yet.
	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"}); err == nil ||
		!strings.Contains(err.Error(), "No answered native prompt") {
		t.Fatalf("err = %v, want errNoNativeEvidence", err)
	}
	still, _ := s.RequestByID(ctx, req.ID)
	if still.State != "open" {
		t.Fatalf("state = %v, want open", still.State)
	}

	// 2. Hook simulation: the user picked "Request changes: tighten scope".
	hookSimulate(t, s, ses, *req.NativePrompt, "Request changes: tighten scope")
	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"}); err == nil ||
		!strings.Contains(err.Error(), `"Request changes: tighten scope"`) {
		t.Fatalf("err = %v, want errDecisionMismatch", err)
	}
	still, _ = s.RequestByID(ctx, req.ID)
	if still.State != "open" {
		t.Fatalf("state after mismatch = %v, want open", still.State)
	}

	// 3. Forward the matching decision.
	out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "request_changes", Comment: "tighten scope"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "changes_requested" || out.RespondedVia != "terminal" {
		t.Fatalf("out = %+v", out)
	}
	wire, err := s.RequestWireByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != EvidenceObserved {
		t.Fatalf("approval_evidence = %v, want observed", wire.ApprovalEvidence)
	}

	// 4. Replay is refused.
	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "request_changes", Comment: "x"}); err == nil ||
		!strings.Contains(err.Error(), "Already resolved") {
		t.Fatalf("replay err = %v", err)
	}
}

func TestNativeAnswerAgentReportedIsAcceptedAndFlagged(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	hookSimulate(t, s, ses, *req.NativePrompt, "") // -> "Resolved in terminal"

	out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "approved" || out.RespondedVia != "terminal" {
		t.Fatalf("out = %+v", out)
	}
	wire, err := s.RequestWireByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != EvidenceAgentReported {
		t.Fatalf("approval_evidence = %v, want agent_reported", wire.ApprovalEvidence)
	}
}

func TestNativeAnswerAgentReportedRequestChanges(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	hookSimulate(t, s, ses, *req.NativePrompt, "")

	out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "request_changes", Comment: "narrow it"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "changes_requested" {
		t.Fatalf("out = %+v", out)
	}
	wire, _ := s.RequestWireByID(ctx, req.ID)
	if wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != EvidenceAgentReported {
		t.Fatalf("approval_evidence = %v, want agent_reported", wire.ApprovalEvidence)
	}
	if out.ResponseText != "narrow it" {
		t.Fatalf("response_text = %q, want %q", out.ResponseText, "narrow it")
	}
}

func TestBoardApprovalHasNullEvidence(t *testing.T) {
	s, _, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	out, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: req.SectionSHA256,
		ArtifactRevision: req.ArtifactRevision, Via: "menubar"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "approved" {
		t.Fatalf("out = %+v", out)
	}
	wire, err := s.RequestWireByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wire.ApprovalEvidence != nil {
		t.Fatalf("approval_evidence = %v, want nil", *wire.ApprovalEvidence)
	}
}

func TestNativeAnswerMismatchIsRefusedEvenWhenTextPresent(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	hookSimulate(t, s, ses, *req.NativePrompt, "Approve")

	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "request_changes", Comment: "x"}); err == nil ||
		!strings.Contains(err.Error(), `"Approve"`) {
		t.Fatalf("err = %v, want errDecisionMismatch", err)
	}
}

func TestNativeAnswerRefusesAnotherAgentsEvidence(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	hookSimulate(t, s, ses, *req.NativePrompt, "Approve")

	// A different top-level agent, with no bound row of its own, forwards
	// the same ref: refused, because the evidence row belongs to another agent.
	_, other, _, err := s.StartSpike(ctx, SpikeInput{Name: "Someone else", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	otherSes := mustSessionID(t, s, other.ID)
	if _, err := s.Ask(ctx, otherSes, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"}); err == nil ||
		!strings.Contains(err.Error(), "No answered native prompt") {
		t.Fatalf("err = %v, want errNoNativeEvidence", err)
	}
}

func TestNativeAnswerConfirmRepos(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoID := seedRepo(t, s, "endurio-chat")
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Confirm native", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repoID}})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, a.ID)
	req, err := s.Ask(ctx, ses, AskInput{Kind: "confirm_repos", Prompt: "Confirm repos",
		Repos: []ReposProposal{{Repo: repoID, Reason: "needed"}}})
	if err != nil {
		t.Fatal(err)
	}
	hookSimulate(t, s, ses, *req.NativePrompt, "Approve")

	out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "approved" || len(out.Confirmed) != 1 || out.Confirmed[0] != repoID {
		t.Fatalf("out = %+v", out)
	}
}

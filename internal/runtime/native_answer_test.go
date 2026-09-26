package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
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

// TestNativeAnswerChildApprovalObservedAndAgentReported is Task 13d: a
// child's approval question resolves through native_answer into an
// approval_result the child receives directly, with no separate request
// row, for both an observed and an agent-reported hook answer.
func TestNativeAnswerChildApprovalObservedAndAgentReported(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		answer   string
		evidence string
	}{
		{"observed", "Approve", EvidenceObserved},
		{"agent_reported", "", EvidenceAgentReported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			orch, w, wSes := worker(t, s)
			orchSes := mustSessionID(t, s, orch.ID)

			q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
			if err != nil {
				t.Fatal(err)
			}
			promptReq, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q})
			if err != nil {
				t.Fatal(err)
			}
			hookSimulate(t, s, orchSes, *promptReq.NativePrompt, tc.answer)

			out, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: q, Decision: "approve"})
			if err != nil {
				t.Fatal(err)
			}
			if out.State != "approved" {
				t.Fatalf("out.State = %v, want approved", out.State)
			}
			wire, err := s.RequestWireByID(ctx, out.ID)
			if err != nil {
				t.Fatal(err)
			}
			if wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != tc.evidence {
				t.Fatalf("approval_evidence = %v, want %s", wire.ApprovalEvidence, tc.evidence)
			}

			// the child's inbox has approval_result with reply_to == q
			var kind, replyTo, toAgent, payload string
			if err := s.DB.QueryRowContext(ctx, `SELECT kind, reply_to, to_agent_id, payload_json
				FROM messages WHERE kind = 'approval_result' ORDER BY seq DESC LIMIT 1`).
				Scan(&kind, &replyTo, &toAgent, &payload); err != nil {
				t.Fatal(err)
			}
			if kind != "approval_result" || replyTo != q || toAgent != w.ID {
				t.Fatalf("kind=%s reply_to=%s to=%s", kind, replyTo, toAgent)
			}
			if !strings.Contains(payload, `"decision":"approved"`) {
				t.Fatalf("payload = %s", payload)
			}

			// a second native_answer for the same ref is refused: the bound
			// row already moved on from "answered", so there is no fresh
			// evidence for it any more.
			if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: q, Decision: "approve"}); err == nil {
				t.Fatal("replay must be refused")
			}
		})
	}
}

// TestNativeAnswerChildApprovalSuppressesOwedAnswerRelay is Task 13d +
// Task 12: an approval_result counts as the owed answer, so the 10-minute
// question_unanswered scan never fires for it.
func TestNativeAnswerChildApprovalSuppressesOwedAnswerRelay(t *testing.T) {
	s, tm, at := clockStore(t)
	_ = tm
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)

	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE id = ?`, q)
	promptReq, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q})
	if err != nil {
		t.Fatal(err)
	}
	hookSimulate(t, s, orchSes, *promptReq.NativePrompt, "Approve")
	if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: q, Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	at.Advance(11 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE kind = 'relay' AND reply_to = ?`, q).Scan(&n)
	if n != 0 {
		t.Fatalf("relays = %d, want 0", n)
	}
}

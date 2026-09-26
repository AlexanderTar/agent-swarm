package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// seedApprovalWithNativePrompt is a small fixture for Task 13c: an
// orchestrator with a registered spec artifact and an open approve_section
// request, its NativePrompt already computed via a real Ask call.
func seedApprovalWithNativePrompt(t *testing.T) (s *Store, ses string, req Request) {
	s, ses, req, _ = seedApprovalWithNativePromptAndPath(t)
	return s, ses, req
}

// seedApprovalWithNativePromptAndPath also returns the artifact's file path,
// which a caller that wants to revise the SAME artifact (RegisterArtifact
// looks it up by item + path, artifacts.go:381) needs to reuse.
func seedApprovalWithNativePromptAndPath(t *testing.T) (s *Store, ses string, req Request, path string) {
	t.Helper()
	s, _, _ = newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Native answer", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses = mustSessionID(t, s, a.ID)
	path = writeFile(t, "# Spec\n\n## Data model\n\nrows\n")
	res, err := s.RegisterArtifact(ctx, ses, "register", "SPIKE-1", "spec", path, "")
	if err != nil {
		t.Fatal(err)
	}
	sec := res.Sections[0]
	req, err = s.Ask(ctx, ses, AskInput{Kind: "approval", Prompt: "Review " + sec.Title,
		ArtifactID: res.ArtifactID, SectionID: sec.ID})
	if err != nil {
		t.Fatal(err)
	}
	return s, ses, req, path
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
	if payload := latestMessagePayload(t, s, "approval_result"); !strings.Contains(payload, `"evidence":"observed"`) {
		t.Fatalf("approval_result payload = %s, want evidence:observed", payload)
	}
	if payload := latestEventPayload(t, s, "request.resolved"); !strings.Contains(payload, `"approval_evidence":"observed"`) {
		t.Fatalf("request.resolved payload = %s, want approval_evidence:observed", payload)
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
	if payload := latestMessagePayload(t, s, "approval_result"); !strings.Contains(payload, `"evidence":"agent_reported"`) {
		t.Fatalf("approval_result payload = %s, want evidence:agent_reported", payload)
	}
	if payload := latestEventPayload(t, s, "request.resolved"); !strings.Contains(payload, `"approval_evidence":"agent_reported"`) {
		t.Fatalf("request.resolved payload = %s, want approval_evidence:agent_reported", payload)
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

// TestNativeAnswerStaleSectionConflicts is Task 13c's stale-artifact
// scenario. nativeAnswer fills ApproveInput from the request's own current
// row (spec 2.3 step 6), so it can never itself disagree with that row --
// but RegisterArtifact's revise flips a request whose section changed
// straight to state:"stale" (artifacts.go's RegisterArtifact), so resolve's
// own "already resolved" guard fires first here, not approveCheck's "This
// request changed" text. Either way nothing gets approved.
func TestNativeAnswerStaleSectionConflicts(t *testing.T) {
	s, ses, req, path := seedApprovalWithNativePromptAndPath(t)
	ctx := context.Background()
	hookSimulate(t, s, ses, *req.NativePrompt, "Approve")

	os.WriteFile(path, []byte("# Spec\n\n## Data model\n\nrows and columns now\n"), 0o644)
	if _, err := s.RegisterArtifact(ctx, ses, "revise", "SPIKE-1", "spec", path, ""); err != nil {
		t.Fatal(err)
	}
	still, err := s.RequestByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.State != "stale" {
		t.Fatalf("state after revise = %v, want stale", still.State)
	}

	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"}); err == nil {
		t.Fatal("native_answer on a stale request must be refused")
	}
	still, _ = s.RequestByID(ctx, req.ID)
	if still.State == "approved" {
		t.Fatal("a stale request must never end up approved")
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
	if payload := latestMessagePayload(t, s, "repos_confirmed"); !strings.Contains(payload, `"evidence":"observed"`) {
		t.Fatalf("repos_confirmed payload = %s, want evidence:observed", payload)
	}
}

// latestMessagePayload returns the payload_json of the most recent message
// of the given kind, for asserting native_answer's evidence key lands on
// the wire (spec 2.3.6(b)).
func latestMessagePayload(t *testing.T, s *Store, kind string) string {
	t.Helper()
	var payload string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT payload_json FROM messages
		WHERE kind = ? ORDER BY seq DESC LIMIT 1`, kind).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// latestEventPayload returns the payload of the most recent event of the
// given type, for asserting the request.resolved wire carries
// approval_evidence (spec 2.3.6(c)).
func latestEventPayload(t *testing.T, s *Store, eventType string) string {
	t.Helper()
	ctx := context.Background()
	evs, err := s.Events.After(ctx, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == eventType {
			return string(evs[i].Payload)
		}
	}
	t.Fatalf("no %s event found", eventType)
	return ""
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
			if want := `"evidence":"` + tc.evidence + `"`; !strings.Contains(payload, want) {
				t.Fatalf("payload = %s, want %s", payload, want)
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

// TestNativeAnswerRefusesASecondRowForTheSameRef is Task B4 finding 2: spec
// 2.3.8 says a ref can be forwarded once, but the old replay guard only
// checked the one bound question row (state = 'answered' -> its own new
// state), not the ref as a whole. When the same child-approval prompt is
// shown (and answered) twice, there are two 'answered' question rows bound
// to the same ref; consuming row B with "approve" left row A still
// 'answered', so a second native_answer with "request_changes" found row A
// and succeeded, producing two conflicting approval_result messages for one
// question.
func TestNativeAnswerRefusesASecondRowForTheSameRef(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, _, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)

	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	promptReq, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q})
	if err != nil {
		t.Fatal(err)
	}
	// The same prompt is shown (and answered) twice in the terminal, e.g. a
	// re-render: two question rows now bind to the same ref.
	hookSimulate(t, s, orchSes, *promptReq.NativePrompt, "Request changes: no")
	hookSimulate(t, s, orchSes, *promptReq.NativePrompt, "Approve")

	if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: q, Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: q, Decision: "request_changes", Comment: "no"}); err == nil ||
		!strings.Contains(err.Error(), "Already resolved") {
		t.Fatalf("err = %v, want Already resolved -- a ref can be forwarded once, not once per row", err)
	}

	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE kind = 'approval_result' AND reply_to = ?`, q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("approval_result messages = %d, want 1", n)
	}
}

// TestNativeAnswerRefusesANonApprovalRequestKind is Task B4 finding 3:
// native_answer's request branch never checked req.Kind, so a ref that
// happens to name any other request the caller owns -- a plain
// kind:"question" HITL row, accept_epic, accept_fix -- was accepted the same
// as approve_section/plan/report, confirm_repos, close_spike (spec 2.3 only
// names those). Reproduced here as the orchestrator mistakenly using its own
// plain question's request id as the ⟦swarm:...⟧ ref.
func TestNativeAnswerRefusesANonApprovalRequestKind(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Wrong kind", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, a.ID)

	// A plain HITL question -- not one of approvalTerminalKinds.
	target, err := s.AskQuestion(ctx, ses, "Is now a good time?", nil)
	if err != nil {
		t.Fatal(err)
	}

	np := NativePrompt{Header: "x", Question: fmt.Sprintf("Approve? %s", refToken(target.ID)), Options: approveOptions}
	hookSimulate(t, s, ses, np, "Approve")

	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: target.ID, Decision: "approve"}); err == nil {
		t.Fatal("native_answer over a non-approval request kind must be refused")
	}
	still, err := s.RequestByID(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.State == "approved" {
		t.Fatal("a plain question must never end up approved via native_answer")
	}
}

// TestNativeAnswerRefusesARequestOwnedByAnotherAgent is Task B4 finding 3:
// native_answer's request branch never checked req.AgentID, so a caller that
// forged its own local evidence row bound to another agent's approve_section
// ref could move that other agent's request to approved.
func TestNativeAnswerRefusesARequestOwnedByAnotherAgent(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	_ = ses

	_, other, _, err := s.StartSpike(ctx, SpikeInput{Name: "Someone else", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	otherSes := mustSessionID(t, s, other.ID)

	// The other agent forges its own evidence row bound to the first
	// orchestrator's request id.
	np := NativePrompt{Header: "x", Question: fmt.Sprintf("Approve? %s", refToken(req.ID)), Options: approveOptions}
	hookSimulate(t, s, otherSes, np, "Approve")

	if _, err := s.Ask(ctx, otherSes, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"}); err == nil {
		t.Fatal("native_answer over another agent's request must be refused")
	}
	still, err := s.RequestByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.State != "open" {
		t.Fatalf("state = %v, want open", still.State)
	}
}

// TestNativeAnswerRefusesAMessageRefNotAddressedToTheCaller is Task B4
// finding 3: native_answer's message-ref branch read `SELECT ... FROM
// messages WHERE id = ?` with no check that the ref names an approval
// question addressed to the caller, so any agent that forged its own
// evidence row bound to another agent's approval-question message id could
// resolve someone else's child approval.
func TestNativeAnswerRefusesAMessageRefNotAddressedToTheCaller(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)

	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}

	_, intruder, _, err := s.StartSpike(ctx, SpikeInput{Name: "Intruder", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	intruderSes := mustSessionID(t, s, intruder.ID)

	np := NativePrompt{Header: "x", Question: fmt.Sprintf("Approve? %s", refToken(q)), Options: approveOptions}
	hookSimulate(t, s, intruderSes, np, "Approve")

	if _, err := s.Ask(ctx, intruderSes, AskInput{Kind: "native_answer", Ref: q, Decision: "approve"}); err == nil ||
		!strings.Contains(err.Error(), "not a question addressed to you") {
		t.Fatalf("err = %v, want errForMsgNotApproval", err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE kind = 'approval_result' AND reply_to = ?`, q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("approval_result messages = %d, want 0", n)
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

package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
)

func reportKind(t *testing.T, s *Store, ses string, kind AgentKind) {
	t.Helper()
	_, err := s.DB.ExecContext(context.Background(), `UPDATE agents SET kind = ? WHERE id = (SELECT agent_id FROM sessions WHERE id = ?)`, kind, ses)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnhookedNativeRequestReport(t *testing.T) {
	for _, kind := range []AgentKind{Cursor, Muse} {
		t.Run(string(kind), func(t *testing.T) {
			for _, tc := range []struct{ name, answer, decision, wantState, wantComment string }{
				{"approve", "Approve", "approve", "approved", ""},
				{"changes", "Request changes: tighten scope", "request_changes", "changes_requested", "tighten scope"},
				{"typed comment", "Please tighten scope", "request_changes", "changes_requested", "Please tighten scope"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s, ses, req := seedApprovalWithNativePrompt(t)
					reportKind(t, s, ses, kind)
					out, err := s.Ask(context.Background(), ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: tc.decision, AnswerText: tc.answer})
					if err != nil {
						t.Fatal(err)
					}
					if out.State != RequestState(tc.wantState) || out.ResponseText != tc.wantComment {
						t.Fatalf("out=%+v", out)
					}
					wire, err := s.RequestWireByID(context.Background(), req.ID)
					if err != nil {
						t.Fatal(err)
					}
					if wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != EvidenceAgentReported {
						t.Fatalf("evidence=%v", wire.ApprovalEvidence)
					}
					if payload := latestMessagePayload(t, s, "approval_result"); !strings.Contains(payload, `"evidence":"agent_reported"`) {
						t.Fatal(payload)
					}
					if payload := latestEventPayload(t, s, "request.resolved"); !strings.Contains(payload, `"approval_evidence":"agent_reported"`) {
						t.Fatal(payload)
					}
					var recorded string
					if err := s.DB.QueryRowContext(context.Background(), `SELECT json_extract(binding_json,'$.answer_text') FROM requests WHERE id = ?`, req.ID).Scan(&recorded); err != nil {
						t.Fatal(err)
					}
					if recorded != tc.answer {
						t.Fatalf("recorded=%q", recorded)
					}
					if _, err := s.Ask(context.Background(), ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: tc.decision, AnswerText: tc.answer}); err == nil {
						t.Fatal("replay accepted")
					}
				})
			}
		})
	}
}

func TestUnhookedNativeReportRefusals(t *testing.T) {
	for _, kind := range []AgentKind{Cursor, Muse} {
		t.Run(string(kind), func(t *testing.T) {
			for _, tc := range []struct{ name, answer, decision string }{
				{"cancelled", "", "approve"}, {"empty", "  ", "approve"}, {"contradictory", "Request changes", "approve"}, {"no decision", "Approve", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s, ses, req := seedApprovalWithNativePrompt(t)
					reportKind(t, s, ses, kind)
					if _, err := s.Ask(context.Background(), ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: tc.decision, AnswerText: tc.answer}); err == nil {
						t.Fatal("accepted invalid report")
					}
					still, _ := s.RequestByID(context.Background(), req.ID)
					if still.State != "open" {
						t.Fatalf("state=%s", still.State)
					}
				})
			}
			s, ses, req, path := seedApprovalWithNativePromptAndPath(t)
			reportKind(t, s, ses, kind)
			_, other, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Other", Intent: "feature", Kind: Fake, Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.startSession(context.Background(), other, 1, 1, false, "", ""); err != nil {
				t.Fatal(err)
			}
			otherSes := mustSessionID(t, s, other.ID)
			reportKind(t, s, otherSes, kind)
			if _, err := s.Ask(context.Background(), otherSes, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve", AnswerText: "Approve"}); err == nil {
				t.Fatal("other agent accepted")
			}
			if err := os.WriteFile(path, []byte("# Spec\n\n## Data model\n\nchanged\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RegisterArtifact(context.Background(), ses, "revise", "SPIKE-1", "spec", path, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Ask(context.Background(), ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve", AnswerText: "Approve"}); err == nil {
				t.Fatal("stale accepted")
			}
		})
	}
}

func TestUnhookedNativeChildApprovalReport(t *testing.T) {
	for _, kind := range []AgentKind{Cursor, Muse} {
		t.Run(string(kind), func(t *testing.T) {
			for _, tc := range []struct{ name, decision, answer, state, comment string }{
				{"approve", "approve", "Approve", "approved", ""},
				{"changes", "request_changes", "Request changes: keep the API", "changes_requested", "keep the API"},
				{"typed comment", "request_changes", "Please keep the API", "changes_requested", "Please keep the API"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s, _, _ := newStore(t)
					orch, _, workerSes := worker(t, s)
					orchSes := mustSessionID(t, s, orch.ID)
					reportKind(t, s, orchSes, kind)
					msg, err := s.SendApproval(context.Background(), workerSes.ID, "may I change it?", "")
					if err != nil {
						t.Fatal(err)
					}
					prompt, err := s.Ask(context.Background(), orchSes, AskInput{Kind: "native_prompt", ForMsg: msg})
					if err != nil || prompt.NativePrompt == nil {
						t.Fatalf("prompt=%+v err=%v", prompt, err)
					}
					if _, err := s.Ask(context.Background(), orchSes, AskInput{Kind: "native_answer", Ref: msg, Decision: "approve", AnswerText: ""}); err == nil {
						t.Fatal("cancelled accepted")
					}
					out, err := s.Ask(context.Background(), orchSes, AskInput{Kind: "native_answer", Ref: msg, Decision: tc.decision, AnswerText: tc.answer})
					if err != nil {
						t.Fatal(err)
					}
					if out.State != RequestState(tc.state) || out.ResponseText != tc.answer {
						t.Fatalf("out=%+v", out)
					}
					wire, err := s.RequestWireByID(context.Background(), out.ID)
					if err != nil {
						t.Fatal(err)
					}
					if wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != EvidenceAgentReported {
						t.Fatalf("evidence=%v", wire.ApprovalEvidence)
					}
					if payload := latestMessagePayload(t, s, "approval_result"); !strings.Contains(payload, `"evidence":"agent_reported"`) || !strings.Contains(payload, `"decision":"`+tc.state+`"`) || (tc.comment != "" && !strings.Contains(payload, tc.comment)) {
						t.Fatal(payload)
					}
					if _, err := s.Ask(context.Background(), orchSes, AskInput{Kind: "native_answer", Ref: msg, Decision: "approve", AnswerText: "Approve"}); err == nil {
						t.Fatal("replay accepted")
					}
				})
			}
		})
	}
}

func TestNativeReportCannotReplaceHookEvidence(t *testing.T) {
	for _, kind := range []AgentKind{Claude, Agy, Codex} {
		t.Run(string(kind), func(t *testing.T) {
			s, ses, req := seedApprovalWithNativePrompt(t)
			reportKind(t, s, ses, kind)
			if _, err := s.Ask(context.Background(), ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve", AnswerText: "Approve"}); err == nil || !strings.Contains(err.Error(), "only for Cursor and Muse") {
				t.Fatalf("err=%v", err)
			}
			hookSimulate(t, s, ses, *req.NativePrompt, "Approve")
			if _, err := s.Ask(context.Background(), ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"}); err != nil {
				t.Fatal(err)
			}
			wire, err := s.RequestWireByID(context.Background(), req.ID)
			if err != nil {
				t.Fatal(err)
			}
			if wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != EvidenceObserved {
				t.Fatalf("evidence=%v", wire.ApprovalEvidence)
			}
		})
	}
}

func TestUnhookedChildReportRejectsMismatchAndOtherOwner(t *testing.T) {
	for _, kind := range []AgentKind{Cursor, Muse} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, _ := newStore(t)
			orch, _, workerSes := worker(t, s)
			orchSes := mustSessionID(t, s, orch.ID)
			reportKind(t, s, orchSes, kind)
			msg, err := s.SendApproval(context.Background(), workerSes.ID, "may I change it?", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Ask(context.Background(), orchSes, AskInput{Kind: "native_answer", Ref: msg, Decision: "approve", AnswerText: "Request changes"}); err == nil {
				t.Fatal("mismatch accepted")
			}
			_, intruder, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Intruder", Intent: "feature", Kind: Fake, Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.startSession(context.Background(), intruder, 1, 1, false, "", ""); err != nil {
				t.Fatal(err)
			}
			intruderSes := mustSessionID(t, s, intruder.ID)
			reportKind(t, s, intruderSes, kind)
			if _, err := s.Ask(context.Background(), intruderSes, AskInput{Kind: "native_answer", Ref: msg, Decision: "approve", AnswerText: "Approve"}); err == nil {
				t.Fatal("other owner accepted")
			}
			var n int
			if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE kind='approval_result' AND reply_to=?`, msg).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("approval_result messages=%d", n)
			}
		})
	}
}

func TestUnhookedNativeReportRejectsContradictoryComment(t *testing.T) {
	for _, kind := range []AgentKind{Cursor, Muse} {
		t.Run(string(kind), func(t *testing.T) {
			s, ses, req := seedApprovalWithNativePrompt(t)
			reportKind(t, s, ses, kind)
			_, err := s.Ask(context.Background(), ses, AskInput{Kind: "native_answer", Ref: req.ID,
				Decision: "request_changes", AnswerText: "Request changes: keep API", Comment: "drop API"})
			if err == nil || !strings.Contains(err.Error(), "comment") {
				t.Fatalf("contradictory comment err = %v", err)
			}
			still, err := s.RequestByID(context.Background(), req.ID)
			if err != nil || still.State != "open" {
				t.Fatalf("request = %+v, err = %v", still, err)
			}
		})
	}
}

func TestUnhookedChildReportRefusesPriorPlainAnswer(t *testing.T) {
	for _, kind := range []AgentKind{Cursor, Muse} {
		t.Run(string(kind), func(t *testing.T) {
			s, _, _ := newStore(t)
			orch, child, childSes := worker(t, s)
			orchSes := mustSessionID(t, s, orch.ID)
			reportKind(t, s, orchSes, kind)
			msg, err := s.SendApproval(context.Background(), childSes.ID, "may I change it?", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Send(context.Background(), orchSes, child.Name, "answer", "Request changes", msg, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Ask(context.Background(), orchSes, AskInput{Kind: "native_answer", Ref: msg,
				Decision: "approve", AnswerText: "Approve"}); err == nil || !strings.Contains(err.Error(), "Already resolved") {
				t.Fatalf("native report after plain answer err = %v", err)
			}
			var n int
			if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE kind = 'approval_result' AND reply_to = ?`, msg).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("approval_result messages = %d", n)
			}
		})
	}
}

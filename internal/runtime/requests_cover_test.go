package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func isBadRequest(err error, wantSubstr string) bool {
	var ie *items.Error
	return errors.As(err, &ie) && ie.Code == items.CodeBadRequest && strings.Contains(ie.Message, wantSubstr)
}

func spikeSession(t *testing.T, s *Store, name string) (Agent, Session) {
	t.Helper()
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: name, Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, ses
}

func TestAskRefusesUnknownAndRetiredKinds(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, ses := spikeSession(t, s, "Ask kinds")

	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "wish", Prompt: "x"}); !isBadRequest(err, "kind must be question") {
		t.Fatalf("unknown kind err = %v", err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "x"}); !isBadRequest(err, "no longer used") {
		t.Fatalf("confirm_repos err = %v", err)
	}
	if err := wantCount(s, `SELECT COUNT(*) FROM requests`, 0); err != nil {
		t.Fatal(err)
	}
}

func TestAskBlockerAndPromptValidatePromptLength(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, ses := spikeSession(t, s, "Prompt length")
	tooLong := strings.Repeat("é", 1001)

	for _, p := range []string{"", tooLong} {
		if _, err := s.AskBlocker(ctx, ses.ID, p, nil); !isBadRequest(err, "1–1000") {
			t.Fatalf("AskBlocker(%d runes) err = %v", len([]rune(p)), err)
		}
		if _, err := s.AskPrompt(ctx, ses.ID, p, nil); !isBadRequest(err, "1–1000") {
			t.Fatalf("AskPrompt(%d runes) err = %v", len([]rune(p)), err)
		}
	}
	// Exactly 1000 multi-byte characters is within bounds.
	if _, err := s.AskPrompt(ctx, ses.ID, strings.Repeat("é", 1000), nil); err != nil {
		t.Fatalf("1000-rune prompt refused: %v", err)
	}
}

func TestAskApprovalValidatesShapeBeforeTouchingTheArtifact(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, ses := spikeSession(t, s, "Approval shape")

	cases := []struct {
		name string
		in   AskInput
		want string
	}{
		{"no artifact", AskInput{Kind: "approval", Prompt: "ok"}, "artifact_id is required"},
		{"empty prompt", AskInput{Kind: "approval", ArtifactID: "art_x"}, "Prompt must be 1–2000"},
		{"empty section summary", AskInput{Kind: "approval", ArtifactID: "art_x", SectionID: "s", Prompt: "  "}, "Spec section summary must be 1–2000"},
		{"long single-line summary", AskInput{Kind: "approval", ArtifactID: "art_x", Prompt: strings.Repeat("a", 301)},
			"lead sentence plus bullets"},
	}
	for _, c := range cases {
		if _, err := s.Ask(ctx, ses.ID, c.in); !isBadRequest(err, c.want) {
			t.Errorf("%s: err = %v, want bad request containing %q", c.name, err, c.want)
		}
	}
}

func TestApproveRefusesOverlongComment(t *testing.T) {
	s, _, _ := newStore(t)
	_, err := s.Approve(context.Background(), "req_x", ApproveInput{Comment: strings.Repeat("c", 2001)})
	if !isBadRequest(err, "at most 2000") {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveCheckRefusesStaleOrMismatchedBindings(t *testing.T) {
	conflict := func(err error) bool {
		var ie *items.Error
		return errors.As(err, &ie) && ie.Code == items.CodeConflict
	}
	section := Request{Kind: "approve_section", SectionSHA256: "aaa", ArtifactRevision: 3}
	if err := approveCheck(ApproveInput{SectionSHA256: "aaa", ArtifactRevision: 3})(section); err != nil {
		t.Fatalf("matching section = %v", err)
	}
	if err := approveCheck(ApproveInput{SectionSHA256: "bbb", ArtifactRevision: 3})(section); !conflict(err) {
		t.Fatalf("stale section hash = %v, want conflict", err)
	}
	if err := approveCheck(ApproveInput{SectionSHA256: "aaa", ArtifactRevision: 2})(section); !conflict(err) {
		t.Fatalf("stale revision = %v, want conflict", err)
	}
	// A caller that omits the revision is not refused on that ground.
	if err := approveCheck(ApproveInput{SectionSHA256: "aaa"})(section); err != nil {
		t.Fatalf("omitted revision = %v", err)
	}

	accept := Request{Kind: "accept_epic", Binding: json.RawMessage(`{"item_revision":4,"integrated_checkpoint":"ck"}`)}
	// Key order differs between the stored struct bytes and a decoded client map; only content matters.
	same := json.RawMessage(`{"integrated_checkpoint":"ck","item_revision":4}`)
	if err := approveCheck(ApproveInput{Binding: same})(accept); err != nil {
		t.Fatalf("equal binding in another key order = %v", err)
	}
	moved := json.RawMessage(`{"integrated_checkpoint":"ck2","item_revision":4}`)
	if err := approveCheck(ApproveInput{Binding: moved})(accept); !conflict(err) {
		t.Fatalf("moved binding = %v, want conflict", err)
	}
	if err := approveCheck(ApproveInput{})(accept); !conflict(err) {
		t.Fatalf("omitted binding on an accept row = %v, want conflict", err)
	}
}

func TestResolveSessionPromptsFallsBackToRetiredSessionsAgent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, ses := spikeSession(t, s, "Retired prompts")
	p1, err := s.AskPrompt(ctx, ses.ID, "Run rm -rf build?", nil)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.AskPrompt(ctx, ses.ID, "Run make?", nil)
	if err != nil {
		t.Fatal(err)
	}
	state := func(id string) string {
		t.Helper()
		r, err := s.RequestByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return string(r.State)
	}

	// A live session never falls back to the agent-wide sweep.
	if err := s.ResolveSessionPrompts(ctx, "ses_unknown", ""); err != nil {
		t.Fatal(err)
	}
	if state(p1.ID) != "open" || state(p2.ID) != "open" {
		t.Fatalf("unknown session closed prompts: %s/%s", state(p1.ID), state(p2.ID))
	}

	// The session is retired: a named command closes just its own prompt on the agent...
	if err := s.SetSessionState(ctx, ses.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveSessionPrompts(ctx, ses.ID, "Run make?"); err != nil {
		t.Fatal(err)
	}
	if state(p2.ID) == "open" || state(p1.ID) != "open" {
		t.Fatalf("after named resolve: p1=%s p2=%s, want only p2 closed", state(p1.ID), state(p2.ID))
	}
	// ...and a blank command closes whatever is left for the agent.
	if err := s.ResolveSessionPrompts(ctx, ses.ID, ""); err != nil {
		t.Fatal(err)
	}
	if state(p1.ID) == "open" {
		t.Fatal("blank-command resolve left a prompt open")
	}
	_ = a
}

func TestResolveAnsweredInTerminalClosesQuestionsAndBlockers(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, ses := spikeSession(t, s, "Terminal answers")
	q, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Keep the email?"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AskBlocker(ctx, ses.ID, "Need a token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveAnsweredInTerminal(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{q.ID, b.ID} {
		r, err := s.RequestByID(ctx, id)
		if err != nil || r.State != "answered" || r.RespondedVia != "terminal" {
			t.Fatalf("request %s = %+v err=%v, want answered via terminal", id, r, err)
		}
	}
	if err := s.ResolveAnsweredInTerminal(canceledCtx(), a.ID); err == nil {
		t.Fatal("ResolveAnsweredInTerminal swallowed a DB error")
	}
}

func TestResolveQuestionReplyWithUnknownSessionResolvesNothing(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, ses := spikeSession(t, s, "Reply fallback")
	open, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Keep the email?"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveQuestionReply(ctx, "ses_unknown", "Keep the email?", "yes")
	if err != nil || got.ID != "" {
		t.Fatalf("reply = %+v err=%v, want the zero Request", got, err)
	}
	r, _ := s.RequestByID(ctx, open.ID)
	if r.State != "open" {
		t.Fatalf("question state = %s, want still open", r.State)
	}
}

func TestNativeAskKindsValidateTheirOwnInputs(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, ses := spikeSession(t, s, "Native input")

	cases := []struct {
		name string
		in   AskInput
		want string
	}{
		{"unknown decision", AskInput{Kind: "native_answer", Decision: "maybe", Ref: "r"}, "decision must be approve or request_changes"},
		{"missing ref", AskInput{Kind: "native_answer", Decision: "approve"}, "ref is required"},
		{"missing for_msg", AskInput{Kind: "native_prompt"}, "for_msg is required"},
	}
	for _, c := range cases {
		if _, err := s.Ask(ctx, ses.ID, c.in); !isBadRequest(err, c.want) {
			t.Errorf("%s: err = %v, want bad request containing %q", c.name, err, c.want)
		}
	}
	if !HasRefToken("see ⟦swarm:req_01ABC⟧ for details") || HasRefToken("see ⟦swarm:ref=abc⟧ or plain swarm:req_01ABC") {
		t.Error("HasRefToken must match only the bracketed req_/msg_ form")
	}
	if err := wantCount(s, `SELECT COUNT(*) FROM requests`, 0); err != nil {
		t.Fatal(err)
	}
}

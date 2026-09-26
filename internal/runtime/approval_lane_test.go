package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Spec E1 copy: the two accept kinds' native prompts.
func TestNativePromptForAcceptKinds(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	bug, err := s.Items.Create(ctx, items.CreateInput{Type: items.Bug, Title: "Login loop"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		got, err := s.nativePromptFor(ctx, tx, Request{ID: "req_e1", Kind: KindAcceptEpic, ItemID: ep.ID}, "", nil)
		if err != nil {
			return err
		}
		want := NativePrompt{Header: "Accept epic", Question: `Accept EPIC-1 "Build it" as done? ⟦swarm:req_e1⟧`, Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_epic prompt = %+v, want %+v", got, want)
		}
		got, err = s.nativePromptFor(ctx, tx, Request{ID: "req_f1", Kind: KindAcceptFix, ItemID: bug.ID}, "", nil)
		if err != nil {
			return err
		}
		want = NativePrompt{Header: "Accept fix",
			Question: fmt.Sprintf(`Accept the fix for %s "Login loop" as done? ⟦swarm:req_f1⟧`, bug.Key), Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_fix prompt = %+v, want %+v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// openAcceptRow inserts an accept row exactly as reconcileRoot does (no agent,
// no session) and fires the RequestOpened hook in the same tx.
func openAcceptRow(t *testing.T, s *Store, id, kind, itemKey string) {
	t.Helper()
	ctx := context.Background()
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := fmt.Sprintf(`{"item_revision":%d,"integrated_checkpoint":"ckp_x","git":[]}`, it.Revision)
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at)
			VALUES (?, ?, ?, 'Review completed work and accept the epic.', 'open', ?, 1)`, id, kind, it.ID, binding); err != nil {
			return err
		}
		return s.OnRequestOpened(ctx, tx, id)
	}); err != nil {
		t.Fatal(err)
	}
}

// relayFor returns the newest request_open relay payload for reqID and how many exist.
func relayFor(t *testing.T, s *Store, toAgentID, reqID string) (map[string]any, int) {
	t.Helper()
	rows, err := s.DB.QueryContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND request_id = ? ORDER BY seq`, toAgentID, reqID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var last map[string]any
	n := 0
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		last = map[string]any{}
		if err := json.Unmarshal([]byte(p), &last); err != nil {
			t.Fatal(err)
		}
		n++
	}
	return last, n
}

func decodeNP(t *testing.T, p map[string]any) NativePrompt {
	t.Helper()
	raw, _ := json.Marshal(p["native_prompt"])
	var np NativePrompt
	if err := json.Unmarshal(raw, &np); err != nil {
		t.Fatal(err)
	}
	return np
}

// Spec E1.
func TestAcceptRowRoutesToLiveRootOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")

	req, err := s.RequestByID(ctx, "req_accept")
	if err != nil {
		t.Fatal(err)
	}
	if req.AgentID != orch.ID || req.SessionID != orchSes {
		t.Fatalf("bound to (%q, %q), want (%q, %q)", req.AgentID, req.SessionID, orch.ID, orchSes)
	}
	p, n := relayFor(t, s, orch.ID, "req_accept")
	if n != 1 {
		t.Fatalf("%d request_open relays, want 1", n)
	}
	if p["event"] != "request_open" || p["kind"] != "accept_epic" || p["item"] != "EPIC-1" || p["request_id"] != "req_accept" {
		t.Fatalf("relay payload = %v", p)
	}
	np := decodeNP(t, p)
	if np.Header != "Accept epic" || np.Question != `Accept EPIC-1 "Build it" as done? ⟦swarm:req_accept⟧` {
		t.Fatalf("native_prompt = %+v", np)
	}
	if p["question"] != np.Question || p["next"] != NativePromptNextStep("req_accept") {
		t.Fatalf("question/next = %v / %v", p["question"], p["next"])
	}
}

// Spec E4.
func TestAcceptRowStaysAgentlessWithoutLiveOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE agent_id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	req, err := s.RequestByID(ctx, "req_accept")
	if err != nil {
		t.Fatal(err)
	}
	if req.AgentID != "" || req.SessionID != "" {
		t.Fatalf("bound to (%q, %q) with no live orchestrator", req.AgentID, req.SessionID)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE request_id = 'req_accept'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d messages for an unrouted accept row", n)
	}
}

// Spec E11 (exhaust): the relay keeps the native prompt even while the kind is out of usage.
func TestAcceptRelayIsNotHeldWhileExhausted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	s.Usage = fakeUsage{Fake: true}
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	if _, n := relayFor(t, s, orch.ID, "req_accept"); n != 1 {
		t.Fatalf("%d relays while exhausted, want 1", n)
	}
	var held int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays`).Scan(&held)
	if held != 0 {
		t.Fatalf("%d suppressed_relays rows, want 0", held)
	}
}

// routedAccept is E1's setup plus the native question shown and answered in
// the orchestrator's terminal.
func routedAccept(t *testing.T, answer string) (s *Store, orch Agent, orchSes string) {
	t.Helper()
	s, _, _ = newStore(t)
	orch, _, _ = worker(t, s)
	orchSes = mustSessionID(t, s, orch.ID)
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	p, _ := relayFor(t, s, orch.ID, "req_accept")
	hookSimulate(t, s, orchSes, decodeNP(t, p), answer)
	return s, orch, orchSes
}

func approvalResultFor(t *testing.T, s *Store, toAgentID, reqID string) map[string]any {
	t.Helper()
	var raw string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'approval_result' AND request_id = ?`, toAgentID, reqID).Scan(&raw); err != nil {
		t.Fatalf("no approval_result for %s: %v", reqID, err)
	}
	var p map[string]any
	json.Unmarshal([]byte(raw), &p)
	return p
}

// Spec E2.
func TestNativeAnswerApprovesRoutedAcceptRow(t *testing.T) {
	s, orch, orchSes := routedAccept(t, "Approve")
	ctx := context.Background()
	out, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: "req_accept", Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "approved" || out.RespondedVia != "terminal" {
		t.Fatalf("state/via = %s/%s", out.State, out.RespondedVia)
	}
	p := approvalResultFor(t, s, orch.ID, "req_accept")
	if p["decision"] != "approved" || p["evidence"] != EvidenceObserved {
		t.Fatalf("approval_result = %v", p)
	}
}

// Spec E3.
func TestNativeAnswerRequestChangesOnAcceptRow(t *testing.T) {
	s, orch, orchSes := routedAccept(t, "Request changes: rename the flag")
	ctx := context.Background()
	out, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: "req_accept", Decision: "request_changes"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "changes_requested" || out.ResponseText != "rename the flag" {
		t.Fatalf("state/text = %s/%q", out.State, out.ResponseText)
	}
	if p := approvalResultFor(t, s, orch.ID, "req_accept"); p["decision"] != "changes_requested" {
		t.Fatalf("approval_result = %v", p)
	}
}

// Spec E6.
func TestNativeAnswerStaleAcceptIsRefused(t *testing.T) {
	s, _, orchSes := routedAccept(t, "Approve")
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET state = 'stale' WHERE id = 'req_accept'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: "req_accept", Decision: "approve"})
	want := "req_accept is stale: EPIC-1 changed after the question was asked. Don't forward it; Swarm sends a new request when the work is ready again."
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict || ie.Message != want {
		t.Fatalf("err = %v, want conflict %q", err, want)
	}
}

// Spec E8.
func TestCloseSpikeRelaysNativePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Nothing", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, a.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: CompletedCkp,
		Summary: "nothing to build", Resolution: "no_change"}); err != nil {
		t.Fatal(err)
	}
	var reqID string
	s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE kind = 'close_spike'`).Scan(&reqID)
	p, n := relayFor(t, s, a.ID, reqID)
	if n != 1 {
		t.Fatalf("%d relays for close_spike, want 1", n)
	}
	np := decodeNP(t, p)
	if np.Header != "Close spike" || np.Question != "Close SPIKE-1? ⟦swarm:"+reqID+"⟧" {
		t.Fatalf("native_prompt = %+v", np)
	}
	hookSimulate(t, s, ses, np, "Approve")
	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Items.Get(ctx, "SPIKE-1"); it.Status != items.Done {
		t.Fatalf("spike status = %s, want done", it.Status)
	}
}

// Spec E12.
func TestAskQuestionReusesOpenRowWithSamePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Dedupe", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses := mustSessionID(t, s, a.ID)
	first, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	// As if the row was asked by an earlier generation of this agent.
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET session_id = NULL WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.SessionID != ses {
		t.Fatalf("re-ask = (%s, %q), want (%s, %s)", again.ID, again.SessionID, first.ID, ses)
	}
	var open int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question' AND state = 'open'`).Scan(&open)
	if open != 1 {
		t.Fatalf("%d open question rows, want 1", open)
	}
	// Once answered, the same prompt is a new question.
	if _, err := s.ResolveQuestionByPrompt(ctx, ses, "Which sync strategy?", "Pull"); err != nil {
		t.Fatal(err)
	}
	third, err := s.AskQuestion(ctx, ses, "Which sync strategy?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Fatalf("an answered row was reused")
	}
}

// Spec copy: question and blocker relays.
func TestRelayRequestForQuestionAndBlocker(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Relay", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses := mustSessionID(t, s, a.ID)
	q, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AskBlocker(ctx, ses, "Need a staging token.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.relayRequestTx(ctx, tx, q.ID); err != nil {
			return err
		}
		return s.relayRequestTx(ctx, tx, b.ID)
	}); err != nil {
		t.Fatal(err)
	}
	pq, _ := relayFor(t, s, a.ID, q.ID)
	if pq["question"] != "Which sync strategy?" || pq["next"] != reaskQuestionNext || pq["native_prompt"] != nil {
		t.Fatalf("question relay = %v", pq)
	}
	if opts, _ := pq["options"].([]any); len(opts) != 2 {
		t.Fatalf("question relay options = %v", pq["options"])
	}
	pb, _ := relayFor(t, s, a.ID, b.ID)
	if pb["question"] != "Need a staging token." || pb["next"] != blockerOpenNext {
		t.Fatalf("blocker relay = %v", pb)
	}
}

// Spec E11 (dedupe): an unacked relay is counted but not sent again.
func TestResurfaceSkipsRequestsWithAPendingRelay(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	a, err := s.AgentByID(ctx, req.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		n, err := s.resurfaceOpenRequests(ctx, a, ses, true)
		if err != nil || n != 1 {
			t.Fatalf("round %d: n = %d, err = %v", i, n, err)
		}
	}
	if _, n := relayFor(t, s, a.ID, req.ID); n != 1 {
		t.Fatalf("%d relays after two unacked rounds, want 1", n)
	}
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE request_id = ?`, req.ID)
	if _, err := s.resurfaceOpenRequests(ctx, a, ses, true); err != nil {
		t.Fatal(err)
	}
	if _, n := relayFor(t, s, a.ID, req.ID); n != 2 {
		t.Fatalf("%d relays after the ack, want 2", n)
	}
	if got := OpenRequestsReminder(2); got != "2 request(s) still wait on your user. swarm_sync delivers each as a "+
		"request_open relay with its native prompt and next step; ask again as it says." {
		t.Fatalf("reminder = %q", got)
	}
}

// Spec E5.
func TestStartOrchestratorBindsAndRelaysAgentlessAcceptRows(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1") // no orchestrator yet
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := s.RequestByID(ctx, "req_accept")
	if req.AgentID != orch.ID || req.SessionID != mustSessionID(t, s, orch.ID) {
		t.Fatalf("bound to (%q, %q)", req.AgentID, req.SessionID)
	}
	if _, n := relayFor(t, s, orch.ID, "req_accept"); n != 1 {
		t.Fatalf("%d relays, want 1", n)
	}
	if !strings.HasSuffix(fa.LastSpec.Kickoff, " "+OpenRequestsReminder(1)) {
		t.Fatalf("kickoff lacks the reminder:\n%s", fa.LastSpec.Kickoff)
	}
}

// Spec E9.
func TestResumeResurfacesOpenRequests(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	fa := s.Adapters[Fake].(*adapter.Fake)
	q, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.AgentByID(ctx, req.AgentID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = NULL WHERE id = ?`, ses)
	if _, err := s.Resume(ctx, a.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	pa, na := relayFor(t, s, a.ID, req.ID)
	if na != 1 || !reflect.DeepEqual(decodeNP(t, pa), *req.NativePrompt) {
		t.Fatalf("approval relay = %d × %v, want the original %+v", na, pa, *req.NativePrompt)
	}
	pq, nq := relayFor(t, s, a.ID, q.ID)
	if nq != 1 || pq["next"] != reaskQuestionNext {
		t.Fatalf("question relay = %d × %v", nq, pq)
	}
	if !strings.HasSuffix(fa.LastSpec.Kickoff, " "+OpenRequestsReminder(2)) {
		t.Fatalf("resume kickoff lacks the reminder:\n%s", fa.LastSpec.Kickoff)
	}
}

// Spec E10.
func TestQuotaResetWakeResurfacesOnlyWhatIsNotVisible(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Quota", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses := mustSessionID(t, s, a.ID)
	path := writeFile(t, "# Spec\n\n## Data model\n\nrows\n\n## API\n\ncalls\n")
	res, err := s.RegisterArtifact(ctx, ses, "register", "SPIKE-1", "spec", path, "")
	if err != nil {
		t.Fatal(err)
	}
	shown, err := s.Ask(ctx, ses, AskInput{Kind: "approval", Prompt: "Review", ArtifactID: res.ArtifactID, SectionID: res.Sections[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := s.Ask(ctx, ses, AskInput{Kind: "approval", Prompt: "Review", ArtifactID: res.ArtifactID, SectionID: res.Sections[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	// shown's native question is open in this very session; q was asked here;
	// a permission prompt never comes back.
	if _, err := s.AskQuestion(ctx, ses, shown.NativePrompt.Question, shown.NativePrompt.Options); err != nil {
		t.Fatal(err)
	}
	q, _ := s.AskQuestion(ctx, ses, "Which sync strategy?", nil)
	p, _ := s.AskPrompt(ctx, ses, "rm -rf build", nil)

	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"─────\n❯ \n─────\n"}
	if _, err := s.WakeOnQuotaReset(ctx, Fake, tm.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if want := QuotaResetNotice() + " " + OpenRequestsReminder(1); fa.LastWakeTarget.Notice != want {
		t.Fatalf("notice = %q, want %q", fa.LastWakeTarget.Notice, want)
	}
	if _, n := relayFor(t, s, a.ID, hidden.ID); n != 1 {
		t.Fatalf("hidden approval: %d relays, want 1", n)
	}
	for _, id := range []string{shown.ID, q.ID, p.ID} {
		if _, n := relayFor(t, s, a.ID, id); n != 0 {
			t.Fatalf("%s is visible or a prompt but got %d relays", id, n)
		}
	}
}

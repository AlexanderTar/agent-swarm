package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Tests for the finish question (docs/specs/2026-09-29-finish-with-pr.md).

var approvePair = []string{"approve", "request_changes"}

const githubRemote = "git@github.com:o/proj.git"

var prOptions = []string{"Create PR, auto-merge when checks pass", "Create PR, I'll merge it myself", "Request changes"}

// finishNoGit is the finish prompt for a binding with no git refs ("git":[] fixtures).
func finishNoGit(header, question string) NativePrompt {
	return NativePrompt{Header: header, Question: question, Options: prOptions, Descriptions: []string{
		"Push, open a PR into the default branch, merge automatically when checks pass.",
		"Push and open a PR into the default branch; Done when you merge it.",
		"Say what to change; I'll re-integrate and ask again.",
	}}
}

func seedFinishRepo(t *testing.T, s *Store, name, remote, base string) {
	t.Helper()
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, remote_url, remote_owner, default_branch, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'o', ?, 'manual', 1, 1)`, "repo_"+name, name, "/tmp/"+name, nullIf(remote), base)
}

// openAcceptRowGit is openAcceptRow with the binding's git refs spelled out.
func openAcceptRowGit(t *testing.T, s *Store, id, kind, itemKey, gitJSON string) {
	t.Helper()
	ctx := context.Background()
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := `{"item_revision":` + strconv.Itoa(it.Revision) + `,"integrated_checkpoint":"ckp_x","git":` + gitJSON + `}`
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

func promptFor(t *testing.T, s *Store, kind RequestKind, itemID, gitJSON string) NativePrompt {
	t.Helper()
	ctx := context.Background()
	var np NativePrompt
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		np, err = s.nativePromptFor(ctx, tx, Request{Kind: kind, ItemID: itemID,
			Binding: []byte(`{"item_revision":1,"integrated_checkpoint":"ckp_x","git":` + gitJSON + `}`)}, "", nil, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return np
}

const (
	gitOne   = `[{"repo":"agent-swarm","branch":"swarm/epic-1","sha":"3f9c2ab0123456"}]`
	gitMixed = `[{"repo":"agent-swarm","branch":"swarm/epic-1","sha":"3f9c2ab0123456"},{"repo":"docs","branch":"swarm/epic-1","sha":"81d0e44abcdef"}]`
)

func TestFinishPromptCopy(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	seedFinishRepo(t, s, "agent-swarm", githubRemote, "main")
	seedFinishRepo(t, s, "docs", "", "main")
	seedFinishRepo(t, s, "legacy", githubRemote, "master")

	want := NativePrompt{Header: "Finish epic",
		Question: `Finish EPIC-1 "Build it"? Branch swarm/epic-1 at 3f9c2ab, not pushed.`,
		Options:  prOptions,
		Descriptions: []string{
			"Push, open a PR into main, merge automatically when checks pass.",
			"Push and open a PR into main; Done when you merge it.",
			"Say what to change; I'll re-integrate and ask again.",
		}}
	if got := promptFor(t, s, KindAcceptEpic, ep.ID, gitOne); !reflect.DeepEqual(got, want) {
		t.Fatalf("single repo = %+v", got)
	}

	mixed := promptFor(t, s, KindAcceptEpic, ep.ID, gitMixed)
	if mixed.Question != `Finish EPIC-1 "Build it"? Not pushed: agent-swarm swarm/epic-1 at 3f9c2ab, docs swarm/epic-1 at 81d0e44.` ||
		!reflect.DeepEqual(mixed.Options, prOptions) ||
		mixed.Descriptions[0] != "Push, open a PR into main, merge automatically when checks pass. docs has no GitHub remote: merged into main locally." ||
		mixed.Descriptions[1] != "Push and open a PR into main; Done when you merge it. docs has no GitHub remote: merged into main locally." {
		t.Fatalf("mixed = %+v", mixed)
	}

	local := promptFor(t, s, KindAcceptEpic, ep.ID, `[{"repo":"docs","branch":"swarm/epic-1","sha":"81d0e44abcdef"}]`)
	if !reflect.DeepEqual(local.Options, []string{"Merge into main locally", "Request changes"}) ||
		!reflect.DeepEqual(local.Descriptions, []string{"Merge the branch into main in your local checkout; Done once it's merged.",
			"Say what to change; I'll re-integrate and ask again."}) {
		t.Fatalf("all-local = %+v", local)
	}

	two := promptFor(t, s, KindAcceptEpic, ep.ID,
		`[{"repo":"agent-swarm","branch":"b","sha":"1"},{"repo":"legacy","branch":"b","sha":"2"}]`)
	if two.Descriptions[1] != "Push and open a PR into each repo's default branch; Done when you merge it." {
		t.Fatalf("two bases = %+v", two)
	}

	bug, err := s.Items.Create(ctx, items.CreateInput{Type: items.Bug, Title: "Login loop"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	chore, err := s.Items.Create(ctx, items.CreateInput{Type: items.Chore, Title: "Bump deps"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if h := promptFor(t, s, KindAcceptFix, bug.ID, gitOne).Header; h != "Finish fix" {
		t.Fatalf("bug header = %q", h)
	}
	if h := promptFor(t, s, KindAcceptFix, chore.ID, gitOne).Header; h != "Finish chore" {
		t.Fatalf("chore header = %q", h)
	}
}

func TestFinishPromptFreezesOptionsAndReplays(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	seedFinishRepo(t, s, "agent-swarm", githubRemote, "main")
	seedFinishRepo(t, s, "docs", "", "main")
	openAcceptRowGit(t, s, "req_accept", "accept_epic", "EPIC-1", gitMixed)
	passPrint(t, s, mustSessionID(t, s, orch.ID))
	p, _ := relayFor(t, s, orch.ID, "req_accept")
	first := decodeNP(t, p)
	// The catalog changing after the ask must not change the replay.
	mustExec(t, s.DB, `UPDATE repos SET remote_url = ? WHERE name = 'docs'`, githubRemote)
	req, err := s.RequestByID(ctx, "req_accept")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var np NativePrompt
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			var err error
			np, err = s.storedNativePromptTx(ctx, tx, req)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(np, first) {
			t.Fatalf("replay = %+v, want %+v", np, first)
		}
	}
	var opts, descs string
	s.DB.QueryRowContext(ctx, `SELECT json_extract(binding_json,'$.options'), json_extract(binding_json,'$.descriptions')
		FROM requests WHERE id = 'req_accept'`).Scan(&opts, &descs)
	if !strings.Contains(opts, "Create PR, I'll merge it myself") || !strings.Contains(descs, "docs has no GitHub remote") {
		t.Fatalf("frozen options/descriptions = %s / %s", opts, descs)
	}
}

func TestFinishChatBlock(t *testing.T) {
	got := ApprovalChatBlock(ChatBlockInput{Kind: "accept_epic", ItemKey: "EPIC-14", Summary: "S", Git: []GitRef{
		{Repo: "agent-swarm", Branch: "swarm/epic-14", SHA: "3f9c2ab0123"},
		{Repo: "endurio", Branch: "swarm/epic-14", SHA: "81d0e44abc"}}})
	want := "### Approval · Finish EPIC-14\n\nS\n\nagent-swarm: swarm/epic-14 at 3f9c2ab\nendurio: swarm/epic-14 at 81d0e44"
	if got != want {
		t.Fatalf("chat block = %q", got)
	}

	s, orch, _, key, reqID := finishFixture(t, githubRemote, "")
	ctx := context.Background()
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.OnRequestOpened(ctx, tx, reqID) }); err != nil {
		t.Fatal(err)
	}
	p, n := relayFor(t, s, orch.ID, reqID)
	if n != 1 {
		t.Fatalf("%d relays", n)
	}
	if p["chat_block"] != "### Approval · Finish "+key+"\n\nmerged\n\nproj: swarm/chore-1 at 3f9c2ab" {
		t.Fatalf("relay chat_block = %q", p["chat_block"])
	}
	if p["next"] != PrintNext || p["native_prompt"] != nil {
		t.Fatalf("next = %v, native_prompt = %v; want the print step only", p["next"], p["native_prompt"])
	}
	passPrint(t, s, mustSessionID(t, s, orch.ID))
	p, _ = relayFor(t, s, orch.ID, reqID)
	np := decodeNP(t, p)
	if p["event"] != "request_ask" || p["next"] != NativePromptNextStep(reqID, np.Options, PromptDecisions(KindAcceptFix, np)) {
		t.Fatalf("relay = %v", p)
	}
}

func TestFinishNextStepDecisions(t *testing.T) {
	finishOpts := []string{"Create PR, auto-merge when checks pass", "Create PR, I'll merge it myself", "Request changes"}
	got := NativePromptNextStep("req_A", finishOpts, []string{"auto_merge", "manual_merge", "request_changes"})
	if !strings.HasPrefix(got, "Ask this now with your native question tool:") {
		t.Fatalf("next = %s", got)
	}
	want := `decision set from their pick: "Create PR, auto-merge when checks pass" → "auto_merge", "Create PR, I'll merge it myself" → "manual_merge", "Request changes" → "request_changes"`
	if !strings.Contains(got, want) {
		t.Fatalf("next = %s", got)
	}
	// Decision codes must not sit beside the ask instruction.
	if ask := got[:strings.Index(got, "After the user answers")]; strings.Contains(ask, "auto_merge") || strings.Contains(ask, "manual_merge") {
		t.Fatalf("codes listed beside the ask instruction: %s", ask)
	}
	if d := PromptDecisions(KindAcceptEpic, NativePrompt{Options: []string{"Merge into main locally", "Request changes"}}); !reflect.DeepEqual(d, []string{"merge_locally", "request_changes"}) {
		t.Fatalf("local decisions = %v", d)
	}
	if d := PromptDecisions(KindApprovePlan, NativePrompt{Options: approveOptions}); !reflect.DeepEqual(d, approvePair) {
		t.Fatalf("plan decisions = %v", d)
	}
	for text, want := range map[string]string{
		"Create PR, auto-merge when checks pass":            "auto_merge",
		"Create PR, I'll merge it myself: go":               "manual_merge",
		"Merge into main locally":                           "merge_locally",
		"merge into each repo's default branch locally: ok": "merge_locally",
		"Approve":               "",
		"Merge into main later": "",
	} {
		if got := finishDecisionFor(text); got != want {
			t.Errorf("finishDecisionFor(%q) = %q, want %q", text, got, want)
		}
	}
	r := Request{Binding: []byte(`{"ref":"req_A"}`), ResponseText: "Create PR, I'll merge it myself"}
	if got := NativeAnswerNextStep(r); got != `[swarm] Recorded "Create PR, I'll merge it myself" for req_A. Forward it now: swarm_ask kind:"native_answer", ref:"req_A", decision:"manual_merge"` {
		t.Fatalf("answer next = %s", got)
	}
}

// routedFinish is finishFixture's open accept_fix routed to its orchestrator, with answer given.
func routedFinish(t *testing.T, remote, answer string) (s *Store, orch Agent, ses, key, reqID string) {
	t.Helper()
	s, orch, ses, key, reqID = finishFixture(t, remote, "")
	ctx := context.Background()
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.OnRequestOpened(ctx, tx, reqID) }); err != nil {
		t.Fatal(err)
	}
	passPrint(t, s, ses)
	p, _ := relayFor(t, s, orch.ID, reqID)
	hookSimulate(t, s, ses, decodeNP(t, p), answer)
	return
}

func wantBadRequest(t *testing.T, err error, msg string) {
	t.Helper()
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Message != msg {
		t.Fatalf("err = %v, want %q", err, msg)
	}
}

func TestFinishNativeAnswer(t *testing.T) {
	ctx := context.Background()
	t.Run("auto_merge", func(t *testing.T) {
		s, orch, ses, key, reqID := routedFinish(t, githubRemote, "Create PR, auto-merge when checks pass")
		out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "auto_merge"})
		if err != nil || out.State != "approved" {
			t.Fatalf("out = %+v, %v", out, err)
		}
		var merge string
		s.DB.QueryRowContext(ctx, `SELECT json_extract(binding_json,'$.merge') FROM requests WHERE id = ?`, reqID).Scan(&merge)
		if merge != "auto" {
			t.Fatalf("$.merge = %q", merge)
		}
		if p := approvalResultFor(t, s, orch.ID, reqID); p["merge"] != "auto" || p["decision"] != "approved" || p["evidence"] != EvidenceObserved {
			t.Fatalf("payload = %v", p)
		}
		if st := itemStatus(t, s, key); st != items.InReview {
			t.Fatalf("status = %s", st)
		}
	})
	t.Run("approve refused", func(t *testing.T) {
		s, _, ses, _, reqID := routedFinish(t, githubRemote, "Create PR, auto-merge when checks pass")
		_, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "approve"})
		wantBadRequest(t, err, "decision for a finish request must be one of: auto_merge, manual_merge, request_changes.")
	})
	t.Run("mismatch", func(t *testing.T) {
		s, _, ses, _, reqID := routedFinish(t, githubRemote, "Request changes")
		_, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "auto_merge"})
		wantBadRequest(t, err, `The user's native answer was "Request changes", not "Create PR, auto-merge when checks pass".`)
	})
	t.Run("merge_locally on a PR prompt", func(t *testing.T) {
		s, _, ses, _, reqID := routedFinish(t, githubRemote, "Create PR, auto-merge when checks pass")
		_, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "merge_locally"})
		wantBadRequest(t, err, "decision for a finish request must be one of: auto_merge, manual_merge, request_changes.")
	})
	t.Run("merge_locally on a local prompt", func(t *testing.T) {
		s, _, ses, _, reqID := routedFinish(t, "", "Merge into main locally")
		out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "merge_locally"})
		if err != nil || out.State != "approved" {
			t.Fatalf("out = %+v, %v", out, err)
		}
	})
	t.Run("old frozen approve prompt", func(t *testing.T) {
		s, orch, ses, _, reqID := finishFixture(t, githubRemote, "")
		mustExec(t, s.DB, `UPDATE requests SET binding_json = json_set(binding_json, '$.question', 'Accept it?', '$.header', 'Accept chore') WHERE id = ?`, reqID)
		if err := s.tx(ctx, func(tx *sql.Tx) error { return s.OnRequestOpened(ctx, tx, reqID) }); err != nil {
			t.Fatal(err)
		}
		passPrint(t, s, ses)
		p, _ := relayFor(t, s, orch.ID, reqID)
		hookSimulate(t, s, ses, decodeNP(t, p), "Approve")
		for _, d := range []string{"approve", "merge_locally"} {
			_, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: d})
			wantBadRequest(t, err, "decision for a finish request must be one of: request_changes.")
		}
	})
	t.Run("request_changes", func(t *testing.T) {
		s, _, ses, key, reqID := routedFinish(t, githubRemote, "Request changes: rename it")
		out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "request_changes"})
		if err != nil || out.State != "changes_requested" {
			t.Fatalf("out = %+v, %v", out, err)
		}
		if st := itemStatus(t, s, key); st != items.InProgress {
			t.Fatalf("status = %s", st)
		}
	})
	t.Run("merge decision on another kind", func(t *testing.T) {
		s, ses, req := seedApprovalWithNativePrompt(t)
		hookSimulate(t, s, ses, *req.NativePrompt, "Approve")
		_, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "auto_merge"})
		wantBadRequest(t, err, "decision must be approve or request_changes.")
		_, err = s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: "msg_nope", Decision: "auto_merge"})
		if err == nil {
			t.Fatal("auto_merge on a msg_ ref must be refused")
		}
	})
}

func TestFinishBoardApprove(t *testing.T) {
	ctx := context.Background()
	s, _, _, key, reqID := finishFixture(t, githubRemote, "")
	req, _ := s.RequestByID(ctx, reqID)
	_, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Via: "board"})
	wantBadRequest(t, err, `Choose how to finish: merge must be "auto" or "manual".`)
	_, err = s.Approve(ctx, reqID, ApproveInput{Binding: []byte(`{"item_revision":99}`), Merge: "manual", Via: "board"})
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict {
		t.Fatalf("stale binding: %v", err)
	}
	out, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: "manual", Via: "board"})
	if err != nil || out.State != "approved" {
		t.Fatalf("out = %+v, %v", out, err)
	}
	if st := itemStatus(t, s, key); st != items.InReview {
		t.Fatalf("status = %s", st)
	}
	if w, _ := s.RequestWireByID(ctx, reqID); w.FinishLocal {
		t.Fatal("a GitHub item is not finish_local")
	}

	s, _, _, _, reqID = finishFixture(t, "", "")
	req, _ = s.RequestByID(ctx, reqID)
	if w, _ := s.RequestWireByID(ctx, reqID); !w.FinishLocal {
		t.Fatal("an all-local item is finish_local")
	}
	_, err = s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: "auto", Via: "board"})
	wantBadRequest(t, err, `Choose how to finish: merge must be "local" (no repository has a GitHub remote).`)
	if _, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: "local", Via: "board"}); err != nil {
		t.Fatal(err)
	}
}

func approvalResults(t *testing.T, s *Store, reqID string) (n int, merge string) {
	t.Helper()
	s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*), COALESCE(MAX(json_extract(payload_json,'$.merge')),'')
		FROM messages WHERE kind = 'approval_result' AND request_id = ?`, reqID).Scan(&n, &merge)
	return
}

func TestResurfaceDeliversAgentlessFinishApproval(t *testing.T) {
	ctx := context.Background()
	s, orch, ses, _, reqID := finishFixture(t, githubRemote, "")
	req, _ := s.RequestByID(ctx, reqID)
	if _, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: "auto", Via: "board"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := approvalResults(t, s, reqID); n != 0 {
		t.Fatalf("%d approval_results before any orchestrator bind", n)
	}
	for i := range 2 {
		if _, err := s.resurfaceOpenRequests(ctx, orch, ses, true, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if n, merge := approvalResults(t, s, reqID); n != 1 || merge != "auto" {
			t.Fatalf("call %d: %d approval_results, merge %q", i, n, merge)
		}
	}
	if got, _ := s.RequestByID(ctx, reqID); got.AgentID != orch.ID {
		t.Fatalf("agent = %q", got.AgentID)
	}

	s, orch, ses, key, reqID2 := finishFixture(t, githubRemote, "")
	reqID = reqID2
	req, _ = s.RequestByID(ctx, reqID)
	if _, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: "auto", Via: "board"}); err != nil {
		t.Fatal(err)
	}
	var ckp string
	s.DB.QueryRowContext(ctx, `SELECT json_extract(binding_json,'$.integrated_checkpoint') FROM requests WHERE id = ?`, reqID).Scan(&ckp)
	mustExec(t, s.DB, `INSERT INTO item_merges (id, item_id, integrated_checkpoint, repo, repo_id, kind, url, number, base, head, state, created_at)
		VALUES ('mrg_1', ?, ?, 'proj', 'repo_proj', 'pr', ?, 412, 'main', 'swarm/chore-1', 'open', 1)`, mustItemID(t, s, key), ckp, prURL)
	if _, err := s.resurfaceOpenRequests(ctx, orch, ses, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := approvalResults(t, s, reqID); n != 0 {
		t.Fatalf("%d approval_results once finishing rows exist", n)
	}
}

// CHORE-18: the finish question tells the user when the tree was finished with
// waived gates or forced statuses, so "accept" is never blind to them.
func TestFinishPromptDisclosesWaiversAndOverrides(t *testing.T) {
	s, _, _ := newStore(t)
	ep := seedEpicWithTask(t, s)
	plain := promptFor(t, s, KindAcceptEpic, ep.ID, gitOne)
	if strings.Contains(plain.Question, "Waived/overridden") {
		t.Fatalf("a clean tree must not mention waivers: %q", plain.Question)
	}
	seedWaiver(t, s, "TASK-1", "tdd", "verify")
	mustExec(t, s.DB, `UPDATE items SET override_json = '{"status":"done","reason":"by hand","agent":"a","at":"2026-10-01T09:00:00Z"}'
		WHERE key = 'STORY-1'`)
	var rk string
	if err := s.DB.QueryRow(`SELECT key FROM items WHERE id = ?`, ep.ID).Scan(&rk); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"tdd", "verify"} {
		mustExec(t, s.DB, `INSERT INTO events (type, payload_json, created_at) VALUES ('item.waived', ?, 1)`,
			`{"key":"TASK-1","root_key":"`+rk+`","gate":"`+g+`","reason":"test","agent":"a"}`)
	}
	mustExec(t, s.DB, `INSERT INTO events (type, payload_json, created_at) VALUES ('item.overridden', ?, 1)`,
		`{"key":"STORY-1","root_key":"`+rk+`","status":"done","reason":"by hand","agent":"a"}`)
	got := promptFor(t, s, KindAcceptEpic, ep.ID, gitOne)
	want := plain.Question + " Waived/overridden: 3 (see board)."
	if got.Question != want {
		t.Fatalf("question = %q, want %q", got.Question, want)
	}
	if !reflect.DeepEqual(got.Options, plain.Options) || !reflect.DeepEqual(got.Descriptions, plain.Descriptions) {
		t.Fatalf("options changed: %+v", got)
	}
}

func finishOptionsFixture(t *testing.T, opts []FinishOption) (*Store, string, error) {
	t.Helper()
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "bumping"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:           []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "3f9c2ab0000"}},
		Verification:  []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}},
		FinishOptions: opts})
	return s, key, err
}

func TestIntegratedStoresFinishOptionsInAcceptBinding(t *testing.T) {
	want := []FinishOption{{Label: "Squash-merge PR", Description: "Push, open PR"}, {Label: "Keep branch"}}
	s, key, err := finishOptionsFixture(t, want)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	it, _ := s.Items.Get(ctx, key)
	var raw string
	if err := s.DB.QueryRowContext(ctx, `SELECT json_extract(binding_json, '$.finish_options') FROM requests
		WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`, it.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got []FinishOption
	if err := json.Unmarshal([]byte(raw), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("binding finish_options = %q (%v), want %+v", raw, err, want)
	}
}

func TestIntegratedWithoutFinishOptionsLeavesBindingUnchanged(t *testing.T) {
	s, key, err := finishOptionsFixture(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	it, _ := s.Items.Get(ctx, key)
	var has int
	s.DB.QueryRowContext(ctx, `SELECT json_extract(binding_json, '$.finish_options') IS NOT NULL FROM requests
		WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`, it.ID).Scan(&has)
	if has != 0 {
		t.Fatal("binding carries finish_options without any being sent")
	}
}

func TestIntegratedRefusesInvalidFinishOptions(t *testing.T) {
	const shape = "finish_options needs 1–4 options with unique labels of 1–80 characters."
	five := []FinishOption{{Label: "a"}, {Label: "b"}, {Label: "c"}, {Label: "d"}, {Label: "e"}}
	for name, c := range map[string]struct {
		opts []FinishOption
		msg  string
	}{
		"five":      {five, shape},
		"empty":     {[]FinishOption{{Label: ""}}, shape},
		"long":      {[]FinishOption{{Label: strings.Repeat("x", 81)}}, shape},
		"duplicate": {[]FinishOption{{Label: "a"}, {Label: "a"}}, shape},
		"reserved":  {[]FinishOption{{Label: "Request changes"}}, "\"Request changes\" is added by Swarm; don't send it as an option."},
		"longdesc":  {[]FinishOption{{Label: "a", Description: strings.Repeat("x", 301)}}, shape},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := finishOptionsFixture(t, c.opts)
			if err == nil || err.Error() != c.msg {
				t.Fatalf("err = %v, want %q", err, c.msg)
			}
		})
	}
}

// A custom approval made on the board with no orchestrator routed still tells the orchestrator which
// option the user picked once it binds.
func TestResurfaceDeliversCustomChoiceToOrchestrator(t *testing.T) {
	ctx := context.Background()
	s, orch, ses, _, reqID := finishFixture(t, githubRemote, "")
	mustExec(t, s.DB, `UPDATE requests SET binding_json = json_set(binding_json, '$.finish_options', json(?)) WHERE id = ?`,
		`[{"label":"Keep branch","description":""}]`, reqID)
	req, _ := s.RequestByID(ctx, reqID)
	if _, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: "custom", Choice: "Keep branch", Via: "board"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resurfaceOpenRequests(ctx, orch, ses, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	var choice string
	s.DB.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload_json,'$.choice'),'') FROM messages
		WHERE kind = 'approval_result' AND request_id = ?`, reqID).Scan(&choice)
	if n, merge := approvalResults(t, s, reqID); n != 1 || merge != "custom" || choice != "Keep branch" {
		t.Fatalf("%d approval_results, merge %q, choice %q", n, merge, choice)
	}
}

// A waiver removed or an override cleared before finishing is still disclosed:
// the count comes from the audit events, not the live item columns.
func TestFinishPromptDisclosesRemovedWaiversAndClearedOverrides(t *testing.T) {
	s, _, _ := newStore(t)
	ep := seedEpicWithTask(t, s)
	plain := promptFor(t, s, KindAcceptEpic, ep.ID, gitOne)
	var rootKey string
	if err := s.DB.QueryRow(`SELECT key FROM items WHERE id = ?`, ep.ID).Scan(&rootKey); err != nil {
		t.Fatal(err)
	}
	ev := func(typ, payload string) {
		mustExec(t, s.DB, `INSERT INTO events (type, payload_json, created_at) VALUES (?, ?, 1)`, typ, payload)
	}
	// waived tdd, then removed (empty reason); one override later cleared by a transition
	ev("item.waived", `{"key":"TASK-1","root_key":"`+rootKey+`","gate":"tdd","reason":"slow","agent":"a"}`)
	ev("item.waived", `{"key":"TASK-1","root_key":"`+rootKey+`","gate":"tdd","reason":"","agent":"a"}`)
	ev("item.overridden", `{"key":"STORY-1","root_key":"`+rootKey+`","from":"ready","status":"done","reason":"by hand","agent":"a"}`)
	ev("item.waived", `{"key":"OTHER-1","root_key":"EPIC-999","gate":"tdd","reason":"x","agent":"a"}`)
	got := promptFor(t, s, KindAcceptEpic, ep.ID, gitOne)
	want := plain.Question + " Waived/overridden: 2 (see board)."
	if got.Question != want {
		t.Fatalf("question = %q, want %q", got.Question, want)
	}
}

// A finish request that carries agent options only accepts one of them: auto/manual/local with no
// choice would bypass the options the user was shown.
func TestApproveRefusesMergeWithoutChoiceWhenOptionsExist(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, reqID := finishFixture(t, githubRemote, "")
	mustExec(t, s.DB, `UPDATE requests SET binding_json = json_set(binding_json, '$.finish_options', json(?)) WHERE id = ?`,
		`[{"label":"Keep branch","description":""}]`, reqID)
	req, _ := s.RequestByID(ctx, reqID)
	for _, m := range []string{"auto", "manual", "local"} {
		_, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: m, Via: "board"})
		if err == nil || !strings.Contains(err.Error(), "Choose one of this request's options: Keep branch.") {
			t.Fatalf("merge %q without a choice: err = %v", m, err)
		}
	}
	if _, err := s.Approve(ctx, reqID, ApproveInput{Binding: req.Binding, Merge: "custom", Choice: "Keep branch", Via: "board"}); err != nil {
		t.Fatalf("a choice must still approve: %v", err)
	}
}

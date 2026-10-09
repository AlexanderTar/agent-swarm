package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func TestAskQuestionOpensARequestAndNotifies(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Ask me", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question",
		Prompt: "Should the form keep the email after a failed login?", Options: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.Kind != "question" || req.State != "open" {
		t.Fatalf("request = %+v", req)
	}
	// The "Answer needed" title is notify.Rules' (Task 23 pins it); what runtime
	// owns is the kind, the item key and the {prompt} argument.
	n := notified(t, s, "request.question")
	if n.ItemKey != "SPIKE-1" || n.Args["prompt"] == "" {
		t.Fatalf("notification = %+v", n)
	}
	// request.opened carries a full Request (R5)
	evs, _ := s.Events.After(ctx, 0, 100)
	var payload json.RawMessage
	for _, e := range evs {
		if e.Type == "request.opened" {
			payload = e.Payload
		}
	}
	var wire map[string]any
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "kind", "agent_name", "item_key", "item_title", "root_key",
		"prompt", "options", "state", "created_at"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("request.opened is missing %q: %s", k, payload)
		}
	}
}

// TestSwarmAskQuestionIsRefusedForHookedKindsOnly is Task 9 (spec section
// 1.7), updated 2026-09-26 (docs/specs/2026-09-26-codex-native-approval.md):
// claude, agy and codex have a live-confirmed hook for their native question
// tool, so swarm_ask kind:"question" is refused for them. cursor's
// AskQuestion never fires a hook at all (confirmed, forum bug 161836), and
// muse's request_user_input the same (confirmed live, Task 4), so both keep
// swarm_ask as their only path to Needs you.
func TestSwarmAskQuestionIsRefusedForHookedKindsOnly(t *testing.T) {
	for _, tc := range []struct {
		kind    AgentKind
		refused bool
	}{{Claude, true}, {Codex, true}, {Agy, true}, {Muse, false}, {Cursor, false}} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Ask me", Intent: "feature", Kind: Fake, Model: "fake-1"})
			if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`, string(tc.kind), a.ID); err != nil {
				t.Fatal(err)
			}
			ses, _ := s.LatestSession(ctx, a.ID)
			_, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Anything?"})
			refused := err != nil && strings.Contains(err.Error(), errQuestionUseNativeTool)
			if refused != tc.refused {
				t.Fatalf("err = %v, refused = %v, want %v", err, refused, tc.refused)
			}
		})
	}
}

func TestHITLRequestWire(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Ask me", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)

	// 1. Question has is_hitl = true
	q, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "A question?", Options: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	if !q.IsHITL {
		t.Fatalf("expected question to have IsHITL=true, got %+v", q)
	}
	qw, err := s.RequestWireByID(ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !qw.IsHITL {
		t.Fatalf("expected wire question to have IsHITL=true, got %+v", qw)
	}

	// 2. Blocker has is_hitl = true
	b, err := s.AskBlocker(ctx, ses.ID, "Missing API key", []string{"Provide key", "Skip"})
	if err != nil {
		t.Fatal(err)
	}
	if !b.IsHITL || b.Kind != KindBlocker {
		t.Fatalf("expected blocker to have IsHITL=true and Kind=blocker, got %+v", b)
	}

	// 3. Prompt has is_hitl = true
	p, err := s.AskPrompt(ctx, ses.ID, "Do you trust this folder?", []string{"Yes", "No"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsHITL || p.Kind != KindPrompt {
		t.Fatalf("expected prompt to have IsHITL=true and Kind=prompt, got %+v", p)
	}
}

func TestAnswerSendsAUserAnswerAndClosesTheRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Answer me", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Keep the email?"})
	out, err := s.Answer(ctx, req.ID, "Yes, keep it.", "menubar")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "answered" || out.RespondedVia != "menubar" || out.ResponseText != "Yes, keep it." {
		t.Fatalf("request = %+v", out)
	}
	var kind, origin, payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT kind, origin, payload_json FROM messages
		WHERE request_id = ?`, req.ID).Scan(&kind, &origin, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "user_answer" || origin != "user_action" {
		t.Fatalf("message = %s / %s", kind, origin)
	}
	if !strings.Contains(payload, "Yes, keep it.") || !strings.Contains(payload, req.ID) {
		t.Fatalf("payload = %s", payload)
	}
	if _, err := s.Answer(ctx, req.ID, "again", "board"); err == nil ||
		!strings.Contains(err.Error(), "Already resolved.") {
		t.Fatalf("a second answer must be refused: %v", err)
	}
}

// §10.2 step 4: an answer typed in the terminal is valid, and the agent withdraws.
func TestWithdrawClosesTheRequestWithNoMessage(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Withdraw", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Keep the email?"})
	if _, err := s.Ask(ctx, ses.ID, AskInput{Withdraw: req.ID}); err != nil {
		t.Fatal(err)
	}
	out, _ := s.RequestByID(ctx, req.ID)
	if out.State != "withdrawn" {
		t.Fatalf("state = %s", out.State)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE request_id = ?`, req.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("a withdrawal sends no message, got %d", n)
	}
}

// L7: approving with the current hash sends approval_result; an old hash conflicts.
func TestApproveBindsToTheSectionHash(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	req, ses, art := seedSectionApproval(t, s)
	_ = ses
	if _, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: "not-the-hash", Via: "board"}); err == nil ||
		!strings.Contains(err.Error(), "This request changed. Review the latest version.") {
		t.Fatalf("a stale hash must conflict: %v", err)
	}
	out, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: art.Sections[0].SHA256, Via: "board"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "approved" {
		t.Fatalf("state = %s", out.State)
	}
	var kind, origin, payload string
	s.DB.QueryRowContext(ctx, `SELECT kind, origin, payload_json FROM messages WHERE request_id = ?`, req.ID).
		Scan(&kind, &origin, &payload)
	if kind != "approval_result" || origin != "user_action" {
		t.Fatalf("message = %s / %s", kind, origin)
	}
	var p struct {
		Decision  string `json:"decision"`
		SectionID string `json:"section_id"`
		Hash      string `json:"section_sha256"`
	}
	json.Unmarshal([]byte(payload), &p)
	if p.Decision != "approved" || p.SectionID == "" || p.Hash != art.Sections[0].SHA256 {
		t.Fatalf("payload = %s", payload)
	}
}

// The brief requires comparing "the whole binding against the stored one" for
// accept_epic/accept_fix unconditionally, unlike ArtifactRevision's explicit
// "when supplied" qualifier: a caller that omits Binding must not bypass the
// staleness check and silently approve a request whose underlying integration
// or item revision has already moved on.
func TestApproveRefusesAnAcceptRequestWhoseBindingIsOmittedOrStale(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	reqID := ids.New("req")
	binding := `{"item_revision":1,"integrated_checkpoint":"ckp_x","git":null}`
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at)
		VALUES (?, 'accept_epic', ?, 'Review completed work and accept the epic.', 'open', ?, ?)`,
		reqID, ep.ID, binding, db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, reqID, ApproveInput{Via: "board"}); err == nil ||
		!strings.Contains(err.Error(), "This request changed. Review the latest version.") {
		t.Fatalf("omitting the binding must be refused, not silently approved: %v", err)
	}
	if _, err := s.Approve(ctx, reqID, ApproveInput{
		Binding: json.RawMessage(`{"item_revision":2,"integrated_checkpoint":"ckp_x","git":null}`),
		Via:     "board"}); err == nil || !strings.Contains(err.Error(), "This request changed. Review the latest version.") {
		t.Fatalf("a mismatched binding must be refused: %v", err)
	}
	// echoing the exact stored binding back, with a finish choice, succeeds
	if _, err := s.Approve(ctx, reqID, ApproveInput{Binding: json.RawMessage(binding), Merge: "auto", Via: "board"}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestChangesNeedsAComment(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	req, _, _ := seedSectionApproval(t, s)
	if _, err := s.RequestChanges(ctx, req.ID, "", "board"); err == nil ||
		err.Error() != "Add a comment describing what to change." {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.RequestChanges(ctx, req.ID, strings.Repeat("x", 2001), "board"); err == nil {
		t.Fatal("a comment over 2000 characters must be refused")
	}
	out, err := s.RequestChanges(ctx, req.ID, "Split the data model into its own section.", "board")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "changes_requested" {
		t.Fatalf("state = %s", out.State)
	}
	var payload string
	s.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages WHERE request_id = ?`, req.ID).Scan(&payload)
	if !strings.Contains(payload, `"decision":"changes_requested"`) ||
		!strings.Contains(payload, "Split the data model") {
		t.Fatalf("payload = %s", payload)
	}
}

// §10.1: an open approval on a spike drives awaiting_approval and back.
func TestAnOpenApprovalMovesTheSpikeToAwaitingApproval(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	req, _, art := seedSectionApproval(t, s)
	it, _ := s.Items.Get(ctx, "SPIKE-1")
	if it.Status != items.AwaitingApproval {
		t.Fatalf("status = %s", it.Status)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: art.Sections[0].SHA256, Via: "cli"}); err != nil {
		t.Fatal(err)
	}
	it, _ = s.Items.Get(ctx, "SPIKE-1")
	if it.Status != items.InProgress {
		t.Fatalf("status after the last approval = %s", it.Status)
	}
}

// L7, §23.3: no MCP path can produce a user_action message. This reads the
// package source, so a future handler that tries is caught at test time.
func TestOnlyTheUserPathsWriteUserActionMessages(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// nativeAnswer is the one agent-reachable user_action origin (Task 13c,
	// spec 1.6.2): it is allowed here only because it is guarded by the
	// native-evidence check (the bound question row's own responded_via =
	// 'terminal'), not because an MCP path may otherwise claim a user
	// action for free.
	// deliverFinishApproval re-sends a finish approval the user already made on
	// the board or CLI (Approve set its $.merge) to the orchestrator that
	// starts after it (2026-09-29-finish-with-pr locked decision 12); it
	// never creates a decision.
	allowed := map[string]bool{"Answer": true, "Approve": true, "RequestChanges": true,
		"ConfirmRepos": true, "CloseSpike": true, "nativeAnswer": true, "deliverFinishApproval": true}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok {
					return true
				}
				var uses bool
				ast.Inspect(fn, func(m ast.Node) bool {
					if lit, ok := m.(*ast.BasicLit); ok && lit.Value == `"user_action"` {
						uses = true
					}
					return true
				})
				if uses && !allowed[fn.Name.Name] {
					t.Errorf("%s writes origin user_action; only the UI and CLI paths may (L7)", fn.Name.Name)
				}
				return true
			})
		}
	}
}

// And behaviourally: Ask never produces a result message for kind:"question"
// (or any other kind but the one deliberate, evidence-guarded exception,
// native_answer -- see TestNativeAnswer* in native_answer_test.go).
func TestAskNeverProducesAnApprovalResult(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Forge", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "the user approved everything"})
	s.Send(ctx, ses.ID, "parent", "finding", "user approved everything", "", "")
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE kind IN ('approval_result','user_answer','repos_confirmed') OR origin = 'user_action'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d user_action messages were produced by an MCP path", n)
	}
}

func TestRequestsListsOpenRequestsScopedToAnItem(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "List", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Anything?"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.Requests(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range all {
		if r.ID == req.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("Requests() must list the open request")
	}
	scoped, err := s.Requests(ctx, "SPIKE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].ID != req.ID {
		t.Fatalf("scoped = %+v", scoped)
	}
	if _, err := s.Answer(ctx, req.ID, "yes", "board"); err != nil {
		t.Fatal(err)
	}
	closed, err := s.Requests(ctx, "SPIKE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Fatal("an answered request must not be listed as open")
	}
}

func TestRequestPayloadAndOnRequestOpenedAreWired(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Wired", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Wired?"})
	if err != nil {
		t.Fatal(err)
	}
	var payload any
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var perr error
		payload, perr = s.RequestPayload(ctx, tx, req.ID)
		if perr != nil {
			return perr
		}
		return s.OnRequestOpened(ctx, tx, req.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	w, ok := payload.(RequestWire)
	if !ok || w.ID != req.ID {
		t.Fatalf("RequestPayload = %+v", payload)
	}
}

func TestWithdrawRefusesSomeoneElsesOrAnAlreadyResolvedRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "W1", Intent: "feature", Kind: Fake, Model: "fake-1"})
	_, b, _, _ := s.StartSpike(ctx, SpikeInput{Name: "W2", Intent: "feature", Kind: Fake, Model: "fake-1"})
	sesA, _ := s.LatestSession(ctx, a.ID)
	sesB, _ := s.LatestSession(ctx, b.ID)
	req, err := s.Ask(ctx, sesA.ID, AskInput{Kind: "question", Prompt: "Mine?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, sesB.ID, AskInput{Withdraw: req.ID}); err == nil {
		t.Fatal("another agent cannot withdraw this request")
	}
	if _, err := s.Ask(ctx, sesA.ID, AskInput{Withdraw: req.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, sesA.ID, AskInput{Withdraw: req.ID}); err == nil {
		t.Fatal("an already-resolved request cannot be withdrawn again")
	}
}

func TestApproveAndAnswerRefuseAnUnknownRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.Approve(ctx, "req_nope", ApproveInput{Via: "board"}); err == nil {
		t.Fatal("an unknown request must be refused")
	}
	if _, err := s.Answer(ctx, "req_nope", "x", "board"); err == nil {
		t.Fatal("an unknown request must be refused")
	}
}

func TestAnswerRefusesAnEmptyText(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Empty", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "?"})
	if _, err := s.Answer(ctx, req.ID, "", "board"); err == nil {
		t.Fatal("an empty answer must be refused")
	}
}

func TestWithdrawRefusesAnUnknownRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "W3", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.Ask(ctx, ses.ID, AskInput{Withdraw: "req_nope"}); err == nil {
		t.Fatal("an unknown request must be refused")
	}
}

func TestAskApprovalValidatesTheArtifact(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Val", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "x"}); err == nil {
		t.Fatal("approval needs an artifact_id")
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: "art_nope", Prompt: "x"}); err == nil {
		t.Fatal("an unknown artifact must be refused")
	}
	note, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "note", writeFile(t, "# n\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: note.ArtifactID, Prompt: "x"}); err == nil {
		t.Fatal("a note cannot be approved")
	}
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", writeFile(t, "# s\n\n## One\n\na\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID, Prompt: "x"}); err == nil {
		t.Fatal("a spec approval needs a section_id")
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
		SectionID: "nope", Prompt: "x"}); err == nil {
		t.Fatal("an unknown section must be refused")
	}
}

func TestRequestsRefusesAnUnknownItem(t *testing.T) {
	s, _, _ := newStore(t)
	if _, err := s.Requests(context.Background(), "TASK-999"); err == nil {
		t.Fatal("an unknown item must be refused")
	}
}

func TestOnRequestOpenedRefusesAnUnknownRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	err := s.tx(ctx, func(tx *sql.Tx) error { return s.OnRequestOpened(ctx, tx, "req_nope") })
	if err == nil {
		t.Fatal("an unknown request must be refused")
	}
}

func TestNotifyIsANoOpWithoutANotifier(t *testing.T) {
	s, _, _ := newStore(t)
	s.Notify = nil
	if err := s.notify(context.Background(), nil, NotifyInput{Kind: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestPromptLengthIsEnforced(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Long", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: strings.Repeat("x", 1001)}); err == nil {
		t.Fatal("a prompt over 1000 characters must be refused")
	}
}

func TestResolvePromptResolvesAndSendsNoKeys(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Prompt spike", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatalf("LatestSession failed: %v", err)
	}

	req, err := s.AskPrompt(ctx, ses.ID, "Trust folder?", []string{"Enter"})
	if err != nil {
		t.Fatalf("AskPrompt failed: %v", err)
	}

	resolved, err := s.ResolvePrompt(ctx, req.ID, "menubar")
	if err != nil {
		t.Fatalf("ResolvePrompt failed: %v", err)
	}
	if resolved.State != "answered" {
		t.Fatalf("expected state answered, got %s", resolved.State)
	}
	if resolved.RespondedVia != "menubar" {
		t.Fatalf("expected responded_via menubar, got %s", resolved.RespondedVia)
	}
	// Resolving is bookkeeping only: nothing is typed into the pane.
	for _, k := range tm.keys {
		if strings.HasPrefix(k, ses.TmuxName+"|") {
			t.Fatalf("expected no keys sent to tmux, got %v", tm.keys)
		}
	}

	// Verify resolving again fails with conflict
	if _, err := s.ResolvePrompt(ctx, req.ID, "menubar"); err == nil {
		t.Fatal("expected conflict on already resolved prompt, got nil")
	}
}

func TestResolveQuestionFromTerminal(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Question spike", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatalf("LatestSession failed: %v", err)
	}

	req, err := s.AskQuestion(ctx, ses.ID, "Choose option", []string{"A", "B"})
	if err != nil {
		t.Fatalf("AskQuestion failed: %v", err)
	}

	resolved, err := s.ResolveQuestion(ctx, req.ID, "A", "terminal")
	if err != nil {
		t.Fatalf("ResolveQuestion failed: %v", err)
	}
	if resolved.State != "answered" {
		t.Fatalf("expected state answered, got %s", resolved.State)
	}
	if resolved.ResponseText != "A" {
		t.Fatalf("expected response_text A, got %s", resolved.ResponseText)
	}
	if resolved.RespondedVia != "terminal" {
		t.Fatalf("expected responded_via terminal, got %s", resolved.RespondedVia)
	}

	// Verify resolving again fails with conflict
	if _, err := s.ResolveQuestion(ctx, req.ID, "A", "terminal"); err == nil {
		t.Fatal("expected conflict on already resolved question, got nil")
	}
}

func TestResolvePromptEmptyActionDoesNotSendKeys(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Prompt spike empty", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatalf("LatestSession failed: %v", err)
	}

	req, err := s.AskPrompt(ctx, ses.ID, "Auto approved prompt?", []string{"Enter"})
	if err != nil {
		t.Fatalf("AskPrompt failed: %v", err)
	}

	resolved, err := s.ResolvePrompt(ctx, req.ID, "terminal")
	if err != nil {
		t.Fatalf("ResolvePrompt failed: %v", err)
	}
	if resolved.State != "answered" {
		t.Fatalf("expected state answered, got %s", resolved.State)
	}
	if resolved.ResponseText != "" {
		t.Fatalf("expected empty response_text, got %q", resolved.ResponseText)
	}
	if resolved.RespondedVia != "terminal" {
		t.Fatalf("expected responded_via terminal, got %s", resolved.RespondedVia)
	}
	// Verify NO keys were sent to tmux
	for _, k := range tm.keys {
		if strings.HasPrefix(k, ses.TmuxName+"|") {
			t.Fatalf("expected no keys sent to tmux, got %v", tm.keys)
		}
	}
}

func TestParentedAgentCannotOpenQuestionOrBlocker(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)

	if _, err := s.Ask(ctx, wSes.ID, AskInput{Kind: "question", Prompt: "which db?"}); err == nil ||
		!strings.Contains(err.Error(), errRelayToParent) {
		t.Fatalf("Ask err = %v, want %q", err, errRelayToParent)
	}
	if _, err := s.AskBlocker(ctx, wSes.ID, "need a key", nil); err == nil ||
		!strings.Contains(err.Error(), errRelayToParent) {
		t.Fatalf("AskBlocker err = %v, want %q", err, errRelayToParent)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE is_hitl = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("HITL rows = %d, want 0", n)
	}
}

func TestResolveAnsweredInTerminalClosesQuestionAndBlockerButNotPrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Human", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	q, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "which?"})
	b, err := s.AskBlocker(ctx, ses.ID, "need a key", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.AskPrompt(ctx, ses.ID, "terraform apply", nil)
	if err := s.ResolveAnsweredInTerminal(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{q.ID: "answered", b.ID: "answered", p.ID: "open"} {
		if got := stateOfRequest(t, s, id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
	var text, via string
	if err := s.DB.QueryRow(`SELECT response_text, responded_via FROM requests WHERE id = ?`, q.ID).Scan(&text, &via); err != nil {
		t.Fatal(err)
	}
	if text != "Answered in terminal" || via != "terminal" {
		t.Fatalf("response = %q via %q", text, via)
	}
}

func TestRequestWireTerminalAgent(t *testing.T) {
	ctx := context.Background()
	want := func(t *testing.T, s *Store, id string, name *string) {
		t.Helper()
		w, err := s.RequestWireByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if (w.TerminalAgent == nil) != (name == nil) || (name != nil && *w.TerminalAgent != *name) {
			t.Fatalf("terminal_agent = %v, want %v", w.TerminalAgent, name)
		}
	}
	t.Run("top-level question is its own terminal", func(t *testing.T) {
		s, _, _ := newStore(t)
		_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Top", Intent: "feature", Kind: Fake, Model: "fake-1"})
		ses, _ := s.LatestSession(ctx, a.ID)
		req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "which?"})
		want(t, s, req.ID, &a.Name)
	})
	t.Run("legacy parented question points at the root orchestrator", func(t *testing.T) {
		s, _, _ := newStore(t)
		orch, w, wSes := worker(t, s)
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at) VALUES ('req_legacy','question',1,?,?,?,'which?','[]','open',1)`,
			w.ID, wSes.ID, w.ItemID); err != nil {
			t.Fatal(err)
		}
		want(t, s, "req_legacy", &orch.Name)
	})
	t.Run("permission prompt points at the asking agent", func(t *testing.T) {
		s, _, _ := newStore(t)
		_, w, wSes := worker(t, s)
		req, err := s.AskPrompt(ctx, wSes.ID, "terraform apply", nil)
		if err != nil {
			t.Fatal(err)
		}
		want(t, s, req.ID, &w.Name)
	})
	// Task 13e (spec 2.1's 21-D5 amendment) revises the 09-21 decision this
	// subtest used to assert: an approval kind with an asking agent now
	// targets that tree's root orchestrator, same as a question or blocker,
	// instead of having no terminal at all.
	t.Run("approval kinds with an asking agent target the root orchestrator", func(t *testing.T) {
		s, _, _ := newStore(t)
		orch, w, wSes := worker(t, s)
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at) VALUES ('req_close','close_spike',0,?,?,?,'x','[]','open',1)`,
			w.ID, wSes.ID, w.ItemID); err != nil {
			t.Fatal(err)
		}
		want(t, s, "req_close", &orch.Name)
	})
	t.Run("accept_epic targets the root item's live orchestrator, or none", func(t *testing.T) {
		s, _, _ := newStore(t)
		orch, _, _ := worker(t, s)
		var rootItemID string
		s.DB.QueryRowContext(ctx, `SELECT root_item_id FROM agents WHERE id = ?`, orch.ID).Scan(&rootItemID)
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, item_id,
			prompt, options_json, state, created_at) VALUES ('req_accept','accept_epic',0,?,'x','[]','open',1)`,
			rootItemID); err != nil {
			t.Fatal(err)
		}
		want(t, s, "req_accept", &orch.Name)

		if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, orch.ID); err != nil {
			t.Fatal(err)
		}
		want(t, s, "req_accept", nil)
	})
}

// TestRequestWireNativePending is Task 13e: an approval hides its
// native-pending state once the bound question row closes.
func TestRequestWireNativePending(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	if _, err := s.AskQuestion(ctx, ses, req.NativePrompt.Question, req.NativePrompt.Options); err != nil {
		t.Fatal(err)
	}
	wire, err := s.RequestWireByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wire.NativePending {
		t.Fatalf("native_pending = false while the bound question is open")
	}
	if _, err := s.ResolveQuestionByPrompt(ctx, ses, req.NativePrompt.Question, "Approve"); err != nil {
		t.Fatal(err)
	}
	wire, err = s.RequestWireByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wire.NativePending {
		t.Fatalf("native_pending = true after the bound question closed")
	}
}

func TestOpenDialogPromptDedupesPerSessionAndTitle(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Dlg", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	r1, created1, err := s.OpenDialogPrompt(ctx, ses.ID, "Trust this project")
	if err != nil || !created1 {
		t.Fatalf("first open: created=%v err=%v", created1, err)
	}
	r2, created2, err := s.OpenDialogPrompt(ctx, ses.ID, "Trust this project")
	if err != nil || created2 || r2.ID != r1.ID {
		t.Fatalf("second open: id=%s created=%v err=%v, want %s reused", r2.ID, created2, err, r1.ID)
	}
	if r1.Kind != KindPrompt || !r1.IsHITL || r1.Prompt != "Trust this project" {
		t.Fatalf("row = %+v", r1)
	}
	n := 0
	for _, k := range s.Notify.(*fakeNotifier).kinds() {
		if k == "request.prompt" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("request.prompt raised %d times, want 1", n)
	}
}

func TestResolveDialogPromptClosesOnlyThatTitle(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Dlg2", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	dlg, _, _ := s.OpenDialogPrompt(ctx, ses.ID, "Trust this project")
	perm, _ := s.AskPrompt(ctx, ses.ID, "rm -rf build", nil)
	if err := s.ResolveDialogPrompt(ctx, ses.ID, "Trust this project"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RequestByID(ctx, dlg.ID)
	if got.State != "answered" || got.RespondedVia != "terminal" {
		t.Fatalf("dialog row = %+v, want answered via terminal", got)
	}
	other, _ := s.RequestByID(ctx, perm.ID)
	if other.State != "open" {
		t.Fatalf("permission row state = %s, want open", other.State)
	}
	if err := s.ResolveDialogPrompt(ctx, ses.ID, "Trust this project"); err != nil {
		t.Fatalf("second resolve should be a no-op, got %v", err)
	}
}

// TestRequestsSurviveReplacement pins the continuity half of HITL: open
// requests are keyed by canonical agent, so they ride out a full
// replacement (predecessor stopped, successor started) still open, repointed
// at the live session, and answerable. The orphan sweep must not withdraw
// them while an operation owns the agent -- only after it clears.
func TestRequestsSurviveReplacement(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Ask me", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Running); err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question",
		Prompt: "Should the form keep the email after a failed login?", Options: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	// A second agent with an in-flight operation and a dead session: the
	// orphan sweep must leave its open request alone while owned.
	_, w, wSes := worker(t, s)
	now := db.Millis(s.Now())
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
		prompt, options_json, state, created_at) VALUES ('req_owned', 'question', 1, ?, ?, ?, 'held?', '[]', 'open', ?)`,
		w.ID, wSes.ID, w.ItemID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_owned', ?, 'recover', 'stopping', 'k', ?, ?, ?, ?)`,
		w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, wSes.ID, Failed); err != nil {
		t.Fatal(err)
	}
	if err := s.withdrawOrphanedRequests(ctx); err != nil {
		t.Fatal(err)
	}
	held, err := s.RequestByID(ctx, "req_owned")
	if err != nil {
		t.Fatal(err)
	}
	if held.State != "open" {
		t.Fatalf("owned request state = %q, want open (sweep must skip agents under operation)", held.State)
	}

	// Full round trip on the asker: predecessor stops, successor starts.
	panes(tm, Pane{Session: ses.TmuxName})
	op, err := s.RequestReplacement(ctx, a.ID, ModeRecover, "rq", "")
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase != PhaseStopping {
		t.Fatalf("phase = %q, want stopping", op.Phase)
	}
	mid, err := s.RequestByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mid.State != "open" {
		t.Fatalf("request state mid-replacement = %q, want open", mid.State)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	succ, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succ.ID == ses.ID {
		t.Fatal("no successor session started")
	}
	after, err := s.RequestByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "open" {
		t.Fatalf("request state after replacement = %q, want open", after.State)
	}
	if after.SessionID != succ.ID {
		t.Fatalf("request session = %s, want live successor %s", after.SessionID, succ.ID)
	}
	resolved, err := s.ResolveQuestion(ctx, req.ID, "Yes", "cli")
	if err != nil {
		t.Fatalf("answer after replacement err = %v", err)
	}
	if resolved.State != "answered" {
		t.Fatalf("request state = %q, want answered", resolved.State)
	}
}

// TestRetiredSessionResolversFollowRepoint locks the session-scoped
// resolvers' continuity fallback: after open rows move to a successor
// generation, resolving by the retired predecessor session still closes
// them. A live session with no match still resolves nothing.
func TestRetiredSessionResolversFollowRepoint(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Prompts", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AskPrompt(ctx, ses.ID, "terraform apply", nil); err != nil {
		t.Fatal(err)
	}
	q, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "ship it?", Options: []string{"yes"}})
	if err != nil {
		t.Fatal(err)
	}
	// A live session with no match resolves nothing and errors nothing.
	if _, err := s.ResolveQuestionByPrompt(ctx, ses.ID, "no such prompt", "x"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequestByID(ctx, q.ID); err != nil || got.State != "open" {
		t.Fatalf("live no-match resolved something: %+v, %v", got, err)
	}
	// New generation takes over; the old session retires.
	succ, err := s.startSessionForTest(ctx, a, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Interrupted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET session_id = ? WHERE agent_id = ? AND state = 'open'`,
		succ.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveQuestionByPrompt(ctx, ses.ID, "ship it?", "yes"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequestByID(ctx, q.ID); err != nil || got.State != "answered" {
		t.Fatalf("retired-session resolve missed the repointed row: %+v, %v", got, err)
	}
	if err := s.ResolveSessionPrompts(ctx, ses.ID, "terraform apply"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests
		WHERE agent_id = ? AND kind = 'prompt' AND state = 'open'`, a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("open prompts after retired-session resolve = %d, want 0", n)
	}
}

// TestResolveQuestionReplyBindsByRefThenByPrompt is S12 of
// docs/specs/2026-09-26-codex-native-approval.md: a codex question-reply
// entry binds by its ref (surrounding text may differ), else by exact
// prompt; an unknown ref is a silent no-op; a ref'd row repointed at a
// successor session still resolves from the retired one.
func TestResolveQuestionReplyBindsByRefThenByPrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Replies", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.AskQuestion(ctx, ses.ID, "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧", []string{"Approve", "Request changes"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.AskQuestion(ctx, ses.ID, "Pick a color", []string{"Red", "Blue"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ResolveQuestionReply(ctx, ses.ID, "Anything? ⟦swarm:req_UNKNOWN⟧", "Approve")
	if err != nil || got.ID != "" {
		t.Fatalf("unknown ref: got %+v, %v; want the zero Request and nil", got, err)
	}

	got, err = s.ResolveQuestionReply(ctx, ses.ID, "Reworded ⟦swarm:req_PLAN1⟧", "Approve")
	if err != nil || got.ID != ref.ID || got.State != "answered" || got.ResponseText != "Approve" || got.RespondedVia != "terminal" {
		t.Fatalf("by ref: got %+v, %v", got, err)
	}

	got, err = s.ResolveQuestionReply(ctx, ses.ID, "Pick a color", "Blue")
	if err != nil || got.ID != plain.ID || got.ResponseText != "Blue" {
		t.Fatalf("by prompt: got %+v, %v", got, err)
	}

	moved, err := s.AskQuestion(ctx, ses.ID, "Close SPIKE-1? ⟦swarm:req_CLOSE1⟧", nil)
	if err != nil {
		t.Fatal(err)
	}
	succ, err := s.startSessionForTest(ctx, a, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Interrupted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET session_id = ? WHERE agent_id = ? AND state = 'open'`,
		succ.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.ResolveQuestionReply(ctx, ses.ID, "Close SPIKE-1? ⟦swarm:req_CLOSE1⟧", "Approve")
	if err != nil || got.ID != moved.ID || got.State != "answered" {
		t.Fatalf("repointed: got %+v, %v", got, err)
	}
}

// TestResolveQuestionReplyUsesAlreadyBoundRowNotFreshRebind is
// docs/specs/2026-09-28-empty-section-auto-approve.md locked decision 6
// (Opus review of 576a53d, minor 1): two children of the same orchestrator
// send byte-identical approval bodies. The orchestrator's PreToolUse hook
// observed childA's native question first, with the correct header, so
// askQuestion froze childA's msg_ ref onto that row (bindNativeQuestionTx,
// header-disambiguated, mirroring TestBindNativeQuestionHeaderDisambiguates
// IdenticalChildBodies). Codex's async reply for that same question text
// carries no header. The old ResolveQuestionReply re-ran BindNativeQuestion
// fresh with header="", which -- with two open identical-body children --
// picks the newest (msgB) and finds no open row bound to it, leaving the
// right row open. The fix resolves through the already-bound row instead.
func TestResolveQuestionReplyUsesAlreadyBoundRowNotFreshRebind(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, childA, childASes := worker(t, s)
	childB, childBSes := spawnSecondChild(t, s, "childB", orch)

	_, err := s.SendApproval(ctx, childASes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendApproval(ctx, childBSes, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = childB

	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	rowA, err := s.askQuestion(ctx, orchSes.ID, AskInput{Prompt: "may I drop table x?",
		Options: []string{"Approve", "Request changes"}, Header: childA.Name + " asks"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ResolveQuestionReply(ctx, orchSes.ID, "may I drop table x?", "Approve")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != rowA.ID {
		t.Fatalf("resolved %+v, want the already-bound row %q (childA)", got, rowA.ID)
	}
	if got.State != "answered" || got.RespondedVia != "terminal" {
		t.Fatalf("resolved request state = %+v", got)
	}
}

// TestResolveQuestionReplyFallsThroughOnAmbiguousBoundMatch is post-review
// minor 3: when more than one open, already-bound question row's normalized
// prompt matches the observed text, ResolveQuestionReply must never guess
// among them -- it falls through to the plain-prompt fallback (which has
// its own separate, pre-existing tie-break) and logs the ambiguity.
func TestResolveQuestionReplyFallsThroughOnAmbiguousBoundMatch(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, childA, childASes := worker(t, s)
	_, childBSes := spawnSecondChild(t, s, "childB", orch)

	_, err := s.SendApproval(ctx, childASes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	msgB, err := s.SendApproval(ctx, childBSes, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}

	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	rowA, err := s.askQuestion(ctx, orchSes.ID, AskInput{Prompt: "may I drop table x?",
		Options: []string{"Approve", "Request changes"}, Header: childA.Name + " asks"})
	if err != nil {
		t.Fatal(err)
	}
	// A second, independently-bound open question row for the same agent
	// and the same normalized text -- an ambiguous state that can only be
	// reached out-of-band (askQuestion itself dedups on (agent, prompt)),
	// but that ResolveQuestionReply must still never guess through.
	rowBID := ids.New("req")
	binding, err := json.Marshal(map[string]string{"ref": msgB})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, is_hitl, agent_id, session_id, item_id, prompt, options_json, state, binding_json, created_at)
		VALUES (?, 'question', 1, ?, ?, ?, ?, '["Approve","Request changes"]', 'open', ?, ?)`,
		rowBID, orch.ID, orchSes.ID, orch.ItemID, "may I drop table x?", string(binding), db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}

	var logs []string
	s.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	got, err := s.ResolveQuestionReply(ctx, orchSes.ID, "may I drop table x?", "Approve")
	if err != nil {
		t.Fatal(err)
	}
	// The new bound-row matcher refused to guess between rowA and rowB;
	// resolution (if any) came from the pre-existing plain-prompt fallback,
	// which is free to pick either by its own rule -- the point under test
	// is that the ambiguity was logged, not silently resolved by the new code.
	found := false
	for _, l := range logs {
		if strings.Contains(l, "refusing to guess") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ambiguous match was not logged: %v", logs)
	}
	if got.ID != "" && got.ID != rowA.ID && got.ID != rowBID {
		t.Fatalf("resolved an unrelated row: %+v", got)
	}
}

// registerSpecSection registers a one-section spec ("## "+heading) and
// returns the artifact id, the section id and the head revision.
func registerSpecSection(t *testing.T, s *Store, ses Session, key, heading, body string) (artifactID, sectionID string, revision int) {
	t.Helper()
	ctx := context.Background()
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec",
		writeFile(t, "# s\n\n## "+heading+"\n\n"+body+"\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	return spec.ArtifactID, spec.Sections[0].ID, spec.Revision
}

// TestAskApprovalNothingToReviewRefusedOnPlan is locked decision 2: the
// field is refused on any approval kind other than approve_section.
func TestAskApprovalNothingToReviewRefusedOnPlan(t *testing.T) {
	s, _, _ := newStore(t)
	seedRepo(t, s, "chat") // the plan's swarm-tree names repo "chat"
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR plan", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, planBody), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID,
		Prompt: "Ship the plan.", NothingToReview: "No new endpoints."})
	if err == nil || !strings.Contains(err.Error(), "nothing_to_review is only for spec sections.") {
		t.Fatalf("err = %v, want the spec-sections-only refusal", err)
	}
}

// TestAskApprovalNothingToReviewLengthValidated is locked decision 1: the
// reason is 3-200 runes.
func TestAskApprovalNothingToReviewLengthValidated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		ok     bool
	}{
		{"too short", "No", false},
		{"too long", strings.Repeat("x", 201), false},
		{"minimum", "Yes", true},
		{"maximum", strings.Repeat("x", 200), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR len", Intent: "feature", Kind: Fake, Model: "fake-1"})
			if err != nil {
				t.Fatal(err)
			}
			ses, err := s.LatestSession(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, "DB models", "None.")
			_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
				Prompt: "No DB changes.", NothingToReview: tc.reason})
			if (err == nil) != tc.ok {
				t.Fatalf("reason %q: err = %v, want ok=%v", tc.reason, err, tc.ok)
			}
		})
	}
}

// TestAskApprovalNothingToReviewRefusedWhenSectionHasContent is locked
// decision 2 (post-review tightened bound): a section body over 120 runes
// (heading stripped, trimmed) is refused with the exact character count,
// and no request row is created.
func TestAskApprovalNothingToReviewRefusedWhenSectionHasContent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR content", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 301)
	artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, "DB models", long)
	var before int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE artifact_id = ?`, artifactID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
		Prompt: "No DB changes.", NothingToReview: "No new tables or columns."})
	wantMsg := "This section has content to review (301 characters); ask for approval normally."
	if err == nil || !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("err = %v, want %q", err, wantMsg)
	}
	var after int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE artifact_id = ?`, artifactID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("a request row was created on refusal: before=%d after=%d", before, after)
	}
}

// TestAskApprovalNothingToReviewRefusesMultiLineBody is the coordinator's
// tightened guard (post-review): a short-but-substantive multi-line body
// (each line well under 120 runes) is still refused -- only a single
// non-empty line auto-approves.
func TestAskApprovalNothingToReviewRefusesMultiLineBody(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR multiline", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := "Adds table foo.\nAdds table bar."
	artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, "DB models", body)
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
		Prompt: "Two tables.", NothingToReview: "Nothing to review."})
	if err == nil || !strings.Contains(err.Error(), "This section has content to review") {
		t.Fatalf("err = %v, want the content refusal", err)
	}
}

// TestAskApprovalNothingToReviewRefusesTableListOrFence is the coordinator's
// tightened guard: a table row, a list item or a code fence refuses the
// auto-approve even when the body is short.
func TestAskApprovalNothingToReviewRefusesTableListOrFence(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"table", "| a | b |"},
		{"dash list", "- one thing"},
		{"star list", "* one thing"},
		{"plus list", "+ one thing"},
		{"numbered list", "1. one thing"},
		{"code fence", "```\nx\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR marker", Intent: "feature", Kind: Fake, Model: "fake-1"})
			if err != nil {
				t.Fatal(err)
			}
			ses, err := s.LatestSession(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, "DB models", tc.body)
			_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
				Prompt: "Review.", NothingToReview: "Nothing to review."})
			if err == nil || !strings.Contains(err.Error(), "This section has content to review") {
				t.Fatalf("err = %v, want the content refusal", err)
			}
		})
	}
}

// TestAskApprovalNothingToReviewAllowsShortSingleLineReason is the
// coordinator's canonical allowed body: a single short line naming what's
// absent, no tables, lists or code.
func TestAskApprovalNothingToReviewAllowsShortSingleLineReason(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR allowed", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, "DB models",
		"None — no tables, columns or migrations.")
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
		Prompt: "No DB changes.", NothingToReview: "No new tables, columns or migrations."})
	if err != nil {
		t.Fatal(err)
	}
	if req.State != "approved" {
		t.Fatalf("request = %+v", req)
	}
}

// TestAskApprovalNothingToReviewRefusedForLongHeading is the coordinator's
// tightened guard: a section heading over 80 runes is refused.
func TestAskApprovalNothingToReviewRefusedForLongHeading(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR heading", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	longHeading := strings.Repeat("h", 81)
	artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, longHeading, "None.")
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
		Prompt: "Nothing here.", NothingToReview: "Nothing to review."})
	if err == nil || !strings.Contains(err.Error(), "heading is too long") {
		t.Fatalf("err = %v, want the long-heading refusal", err)
	}
}

// TestAskApprovalNothingToReviewRefusedForHeadinglessPreambleContent is a
// review fix: a headingless preamble (the "document" section) has no "## "
// heading line to strip, so its first line is real content, not a heading --
// stripping it unconditionally would let a long single-line preamble slip
// under the 300-rune guard.
func TestAskApprovalNothingToReviewRefusedForHeadinglessPreambleContent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR preamble", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 400)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec",
		writeFile(t, long+"\n\n## Context\n\nWhy it matters.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Sections[0].ID != "document" {
		t.Fatalf("registered sections = %+v, want a headingless document section first", spec.Sections)
	}
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID,
		Prompt: "Ship it.", NothingToReview: "Nothing new here."})
	if err == nil || !strings.Contains(err.Error(), "This section has content to review (400 characters)") {
		t.Fatalf("err = %v, want the content refusal with 400 characters", err)
	}
}

// TestAskApprovalNothingToReviewAutoApproves is locked decisions 2-4: a
// short, flagged section is approved immediately, exactly like a user
// approval (same message, same event, same reconcile), with no native
// question ever issued.
func TestAskApprovalNothingToReviewAutoApproves(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR auto", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, "DB models", "None.")
	reason := "No new tables, columns or migrations."
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
		Prompt: "No DB changes.", NothingToReview: reason})
	if err != nil {
		t.Fatal(err)
	}
	if req.State != "approved" || req.RespondedVia != "auto" {
		t.Fatalf("request = %+v", req)
	}
	var binding string
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(binding_json,'') FROM requests WHERE id = ?`, req.ID).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	var b struct {
		Evidence        string `json:"evidence"`
		NothingToReview string `json:"nothing_to_review"`
	}
	if err := json.Unmarshal([]byte(binding), &b); err != nil {
		t.Fatalf("binding_json = %q: %v", binding, err)
	}
	if b.Evidence != "auto_empty" || b.NothingToReview != reason {
		t.Fatalf("binding = %+v", b)
	}
	wantNext := `Print this line in chat: Section "DB models": nothing to review (` + reason + `) — auto-approved. Then continue with the next section.`
	if req.Next != wantNext {
		t.Fatalf("next = %q, want %q", req.Next, wantNext)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE to_agent_id = ? AND kind = 'approval_result' AND request_id = ?`, a.ID, req.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("approval_result messages to %s = %d, want 1", a.ID, n)
	}
	var qn int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question'`).Scan(&qn); err != nil {
		t.Fatal(err)
	}
	if qn != 0 {
		t.Fatalf("no native question should have been issued, found %d", qn)
	}
	if got := notifiedCount(s, "request.approve_section"); got != 0 {
		t.Fatalf("Needs-you notification raised for an auto-approved section: %d", got)
	}
}

// TestSpecApprovedWhenAllSectionsIncludingAutoApproved is locked decision 3:
// the spec's materialization gate treats an auto-approved section exactly
// like a user-approved one.
func TestSpecApprovedWhenAllSectionsIncludingAutoApproved(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR rollup", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec",
		writeFile(t, "# s\n\n## Screens\n\nA screen.\n\n## DB models\n\nNone.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	var screens, dbModels ArtifactSection
	for _, sec := range spec.Sections {
		switch sec.Title {
		case "Screens":
			screens = sec
		case "DB models":
			dbModels = sec
		}
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
		SectionID: screens.ID, Prompt: "One screen."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: screens.SHA256,
		ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
		SectionID: dbModels.ID, Prompt: "No DB changes.", NothingToReview: "No new tables or columns."}); err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		return s.checkEverySectionApproved(ctx, tx, spec.ArtifactID)
	}); err != nil {
		t.Fatalf("spec not approved with one normal and one auto-approved section: %v", err)
	}
}

// TestAutoApprovedSectionRevisedRequiresNormalApproval is locked decision 5:
// revising an auto-approved section's content requires a fresh approval at
// the new hash, exactly like any other stale-revision rule -- no new
// production code, the existing section_sha256 match already covers it.
func TestAutoApprovedSectionRevisedRequiresNormalApproval(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "NTR stale", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifactID, sectionID, _ := registerSpecSection(t, s, ses, key, "DB models", "None.")
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: artifactID, SectionID: sectionID,
		Prompt: "No DB changes.", NothingToReview: "No new tables or columns."}); err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		return s.checkEverySectionApproved(ctx, tx, artifactID)
	}); err != nil {
		t.Fatalf("auto-approved section blocks materialization: %v", err)
	}
	revised, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec",
		writeFile(t, "# s\n\n## DB models\n\nAdds a `users` table.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		return s.checkEverySectionApproved(ctx, tx, revised.ArtifactID)
	}); err == nil || !strings.Contains(err.Error(), "approval_missing") {
		t.Fatalf("revised section should require a fresh approval: %v", err)
	}
}

package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
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
// 1.7): claude, codex and agy have a live- or source-confirmed hook for
// their native question tool, so swarm_ask kind:"question" is refused for
// them. cursor and muse do not -- cursor's AskQuestion never fires a hook at
// all (confirmed, forum bug 161836), and muse's request_user_input the same
// (confirmed live, Task 4) -- so both keep swarm_ask as their only path to
// Needs you.
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
	// echoing the exact stored binding back succeeds
	if _, err := s.Approve(ctx, reqID, ApproveInput{Binding: json.RawMessage(binding), Via: "board"}); err != nil {
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
	allowed := map[string]bool{"Answer": true, "Approve": true, "RequestChanges": true,
		"ConfirmRepos": true, "CloseSpike": true}
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

// And behaviourally: Ask never produces a result message.
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
	t.Run("approval kinds have none", func(t *testing.T) {
		s, _, _ := newStore(t)
		_, w, wSes := worker(t, s)
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at) VALUES ('req_close','close_spike',0,?,?,?,'x','[]','open',1)`,
			w.ID, wSes.ID, w.ItemID); err != nil {
			t.Fatal(err)
		}
		want(t, s, "req_close", nil)
	})
}

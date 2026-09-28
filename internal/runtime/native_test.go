package runtime

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNativePromptNextStepDescribesVisibleReviewAndAgentReportedAnswers(t *testing.T) {
	got := NativePromptNextStep("req_A")
	for _, want := range []string{"verbatim and complete", "shows only its first line", "review_paths.spec", "review_paths.plan", "Cursor AskQuestion", "Muse request_user_input", "answer_text", "agent_reported", "cancellation", `ref:"req_A"`,
		"Codex: use request_user_input, not request_user_input_async", "a review question is a design decision the user chooses, not a permission request"} {
		if !strings.Contains(got, want) {
			t.Errorf("next step missing %q: %s", want, got)
		}
	}
}

func TestRefTokenAndRefFromPrompt(t *testing.T) {
	q := "Approve the plan (rev 1)?" + refToken("req_ABC123")
	if !strings.HasSuffix(q, " ⟦swarm:req_ABC123⟧") {
		t.Fatalf("refToken suffix = %q", q)
	}
	if got := refFromPrompt(q); got != "req_ABC123" {
		t.Fatalf("refFromPrompt(%q) = %q", q, got)
	}
	if got := refFromPrompt("plain text"); got != "" {
		t.Fatalf("refFromPrompt(plain) = %q, want empty", got)
	}
}

// TestTruncateWithTokenNeverCutsTheRefToken is the 2026-09-26 fix
// (native-railway-tracing finding): a body over 1000 runes must be trimmed
// from its own end, never the trailing ref token the hook needs to bind the
// answer back to the request.
func TestTruncateWithTokenNeverCutsTheRefToken(t *testing.T) {
	body := strings.Repeat("a", 2000)
	ref := "req_ABC123"
	got := truncateWithToken(body, ref)
	if n := utf8.RuneCountInString(got); n > 1000 {
		t.Fatalf("length = %d runes, want <= 1000", n)
	}
	if !strings.HasSuffix(got, refToken(ref)) {
		t.Fatalf("ref token missing or cut: tail = %q", got[len(got)-40:])
	}
}

// TestApprovalSummaryHeadForHookedKindsIsFirstLineCappedAt200Runes is
// 2026-09-28-summary-in-native-question: claude/agy/codex have Swarm's
// native-question hook, so a later hook step can confirm the asking agent
// printed the full summary in chat; the native question itself only needs
// the summary's first line, capped to 200 runes.
func TestApprovalSummaryHeadForHookedKindsIsFirstLineCappedAt200Runes(t *testing.T) {
	multiline := "First line is the important part.\nSecond line should never appear."
	for _, kind := range []AgentKind{Claude, Agy, Codex} {
		got := approvalSummaryHead(multiline, kind)
		if got != "First line is the important part." {
			t.Fatalf("%s head = %q, want only the first line", kind, got)
		}
	}
	long := strings.Repeat("x", 300)
	got := approvalSummaryHead(long, Claude)
	if n := utf8.RuneCountInString(got); n > 200 {
		t.Fatalf("head = %d runes, want <= 200", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("head = %q, want a truncated head ending in an ellipsis", got)
	}
}

// TestApprovalSummaryHeadForUnhookedKindsAllowsUpTo600Runes: cursor/muse have
// no native-question hook, so nothing can later confirm the full summary was
// printed in chat -- their native question head runs longer (600 runes) and
// is not restricted to the first line.
func TestApprovalSummaryHeadForUnhookedKindsAllowsUpTo600Runes(t *testing.T) {
	multiline := "First line.\nSecond line stays too, within the 600-rune budget."
	for _, kind := range []AgentKind{Cursor, Muse} {
		if got := approvalSummaryHead(multiline, kind); got != multiline {
			t.Fatalf("%s head = %q, want the summary unchanged", kind, got)
		}
	}
	long := strings.Repeat("y", 700)
	got := approvalSummaryHead(long, Cursor)
	if n := utf8.RuneCountInString(got); n > 600 {
		t.Fatalf("head = %d runes, want <= 600", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("head = %q, want a truncated head ending in an ellipsis", got)
	}
}

// TestBuildApprovalQuestionCapsAt1000RunesWithoutCuttingTail is the
// 2026-09-28-summary-in-native-question truncation contract directly on the
// helper: only the summary head is shortened (ending in "…") to make room;
// the paths, approve line and ref token survive completely, and the total
// never exceeds 1000 runes.
func TestBuildApprovalQuestionCapsAt1000RunesWithoutCuttingTail(t *testing.T) {
	summary := strings.Repeat("word ", 200) // 1000 runes on its own
	paths := "Spec: /repo/docs/specs/plan.md\nPlan: /repo/docs/plans/plan.md\n"
	approveLine := "Approve the plan (rev 4)?\nWarnings:\n- Task t2 has no verify command."
	ref := "req_CAP1"

	got := buildApprovalQuestion(summary, paths, approveLine, ref)
	if n := utf8.RuneCountInString(got); n > 1000 {
		t.Fatalf("question = %d runes, want <= 1000", n)
	}
	if !strings.HasSuffix(got, paths+approveLine+refToken(ref)) {
		t.Fatalf("tail (paths+approve line+token) was cut: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("question = %q, want the shortened summary to end with an ellipsis", got)
	}
}

// TestNativeAnswerNextStep covers every shape ResolveQuestionByPrompt can
// hand PostToolUse: an observed Approve/Request changes pick, genuine typed
// free text, and agy's placeholder "Resolved in terminal" (spec 1.7) --
// which is the daemon's own fallback, never something the user typed, so it
// must not be quoted back as "the user's text".
func TestNativeAnswerNextStep(t *testing.T) {
	for _, tc := range []struct {
		name         string
		responseText string
		want         []string
		notWant      []string
	}{
		{"approve", "Approve", []string{`Recorded "Approve" for req_X`, `decision:"approve"`}, nil},
		{"request_changes", "Request changes", []string{`Recorded "Request changes" for req_X`, `decision:"request_changes"`}, nil},
		{"typed free text", "Approve, but drop endurio-docs", []string{
			`decide approve or request_changes from the user's text "Approve, but drop endurio-docs"`,
			"if the text is neither an approval nor a change request, ask the user again instead of forwarding"}, nil},
		{"agy placeholder", "Resolved in terminal", []string{"the option the user picked"},
			[]string{`the user's text "Resolved in terminal"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Binding: []byte(`{"ref":"req_X"}`), ResponseText: tc.responseText}
			got := NativeAnswerNextStep(req)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("got %q, want it to contain %q", got, w)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Fatalf("got %q, want it NOT to contain %q", got, nw)
				}
			}
		})
	}

	if got := NativeAnswerNextStep(Request{}); got != "" {
		t.Fatalf("no-ref request = %q, want empty", got)
	}
}

// TestNativePromptForBuildsExactCopy is Task 13a: the daemon-issued native
// prompt for each approval kind matches spec section 6, verbatim, and ends
// with the ref token.
func TestNativePromptForBuildsExactCopy(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Copy test", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	repoA := seedRepo(t, s, "endurio-chat")
	repoB := seedRepo(t, s, "endurio-web")
	repoC := seedRepo(t, s, "endurio-docs")
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// a is a Fake-kind agent (not in questionHookKinds), so its summary head
	// takes the unhooked, un-firstlined, up-to-600-rune path -- these short
	// single-line summaries pass through unchanged either way.
	sectionSummary := "Users table gets id, email, and hashed_password columns; sessions reference it by user_id."
	planSummary := "Ship auth end to end: login, session cookies, and logout across web and API."
	reportSummary := "Root cause: the token cache read stale entries after rotation; fix invalidates on rotate."

	tests := []struct {
		name        string
		req         Request
		section     string
		warnings    []string
		reviewPaths *ReviewPaths
		wantHeader  string
		wantQ       string
		wantOpts    []string
	}{
		{
			name:       "approve_section",
			req:        Request{ID: "req_SEC1", Kind: "approve_section", ArtifactRevision: 2, ItemID: it.ID, AgentID: a.ID, Prompt: sectionSummary},
			section:    "Data model",
			wantHeader: "Spike approval",
			wantQ:      sectionSummary + "\n\n" + `Approve Spec section "Data model" (rev 2)?` + refToken("req_SEC1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_plan_no_warnings",
			req:        Request{ID: "req_PLAN1", Kind: "approve_plan", ArtifactRevision: 1, ItemID: it.ID, AgentID: a.ID, Prompt: planSummary},
			wantHeader: "Spike approval",
			wantQ:      planSummary + "\n\n" + `Approve the plan (rev 1)?` + refToken("req_PLAN1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:        "approve_plan_with_paths",
			req:         Request{ID: "req_PLAN3", Kind: "approve_plan", ArtifactRevision: 1, ItemID: it.ID, AgentID: a.ID, Prompt: planSummary},
			reviewPaths: &ReviewPaths{Spec: "/abs/repo/specs/spec.md", Plan: "/abs/repo/plans/plan.md"},
			wantHeader:  "Spike approval",
			wantQ: planSummary + "\n\n" + "Spec: /abs/repo/specs/spec.md\nPlan: /abs/repo/plans/plan.md\n" +
				`Approve the plan (rev 1)?` + refToken("req_PLAN3"),
			wantOpts: []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_plan_with_warnings",
			req:        Request{ID: "req_PLAN2", Kind: "approve_plan", ArtifactRevision: 3, ItemID: it.ID, AgentID: a.ID, Prompt: planSummary},
			warnings:   []string{"Task t2 has no verify command."},
			wantHeader: "Spike approval",
			wantQ:      planSummary + "\n\n" + "Approve the plan (rev 3)?\nWarnings:\n- Task t2 has no verify command." + refToken("req_PLAN2"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_report",
			req:        Request{ID: "req_REP1", Kind: "approve_report", ArtifactRevision: 1, ItemID: it.ID, AgentID: a.ID, Prompt: reportSummary},
			wantHeader: "Spike approval",
			wantQ:      reportSummary + "\n\n" + `Approve the debug report (rev 1)?` + refToken("req_REP1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name: "confirm_repos",
			req: Request{ID: "req_REPO1", Kind: "confirm_repos", ItemID: it.ID,
				Options: []byte(`{"proposed":[{"repo":"` + repoA + `","reason":"r"}]}`)},
			wantHeader: "Repositories",
			wantQ:      "Confirm 1 repositories for " + key + ": endurio-chat?" + refToken("req_REPO1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name: "confirm_repos_with_expansion_and_dropped",
			req: Request{ID: "req_REPO2", Kind: "confirm_repos", ItemID: it.ID,
				Options: []byte(`{"proposed":[{"repo":"` + repoA + `","reason":"r"},` +
					`{"repo":"` + repoC + `","reason":"stale","source":"dropped"}],` +
					`"expansion":[{"repo":"` + repoB + `","reason":"client/server pair"}]}`)},
			wantHeader: "Repositories",
			wantQ: "Confirm 2 repositories for " + key + ": endurio-chat, endurio-web?" +
				"\nDropped: endurio-docs." + refToken("req_REPO2"),
			wantOpts: []string{"Approve", "Request changes"},
		},
		{
			name:       "close_spike",
			req:        Request{ID: "req_CLOSE1", Kind: "close_spike", ItemID: it.ID},
			wantHeader: "Close spike",
			wantQ:      "Close " + key + "?" + refToken("req_CLOSE1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			np, err := s.nativePromptFor(ctx, tx, tc.req, tc.section, tc.warnings, tc.reviewPaths)
			if err != nil {
				t.Fatal(err)
			}
			if np.Header != tc.wantHeader {
				t.Errorf("header = %q, want %q", np.Header, tc.wantHeader)
			}
			if np.Question != tc.wantQ {
				t.Errorf("question = %q, want %q", np.Question, tc.wantQ)
			}
			if strings.Join(np.Options, ",") != strings.Join(tc.wantOpts, ",") {
				t.Errorf("options = %v, want %v", np.Options, tc.wantOpts)
			}
			if refFromPrompt(np.Question) != tc.req.ID {
				t.Errorf("refFromPrompt(%q) = %q, want %q", np.Question, refFromPrompt(np.Question), tc.req.ID)
			}
		})
	}
}

func TestNativePromptForMsg(t *testing.T) {
	np := nativePromptForMsg("go-migration-agent", "may I drop table x?", "msg_ABC")
	if np.Header != "go-migration-agent asks" {
		t.Fatalf("header = %q", np.Header)
	}
	want := "may I drop table x?" + refToken("msg_ABC")
	if np.Question != want {
		t.Fatalf("question = %q, want %q", np.Question, want)
	}
	if len(np.Options) != 2 || np.Options[0] != "Approve" || np.Options[1] != "Request changes" {
		t.Fatalf("options = %v", np.Options)
	}
	if refFromPrompt(np.Question) != "msg_ABC" {
		t.Fatalf("refFromPrompt = %q", refFromPrompt(np.Question))
	}
}

func TestNativePromptQuestionTruncatesButKeepsTheToken(t *testing.T) {
	long := strings.Repeat("x", 1200)
	np := nativePromptForMsg("child", long, "msg_XYZ")
	if got := len([]rune(np.Question)); got > 1000 {
		t.Fatalf("question is %d runes, want <= 1000", got)
	}
	if !strings.HasSuffix(np.Question, refToken("msg_XYZ")) {
		t.Fatalf("token did not survive truncation: %q", np.Question[len(np.Question)-40:])
	}
	if refFromPrompt(np.Question) != "msg_XYZ" {
		t.Fatalf("refFromPrompt = %q", refFromPrompt(np.Question))
	}
}

// TestAskApprovalReturnsNativePrompt exercises the real Ask() path for
// approve_section: the returned Request carries the NativePrompt.
func TestAskApprovalReturnsNativePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Spec", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	p := writeFile(t, "# Spec\n\n## Data model\n\nrows\n")
	res, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", p, "")
	if err != nil {
		t.Fatal(err)
	}
	sec := res.Sections[0]
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Review " + sec.Title,
		ArtifactID: res.ArtifactID, SectionID: sec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if req.NativePrompt == nil {
		t.Fatalf("NativePrompt is nil on %+v", req)
	}
	want := "Review " + sec.Title + "\n\n" + `Approve Spec section "Data model" (rev 1)?` + refToken(req.ID)
	if req.NativePrompt.Question != want {
		t.Fatalf("question = %q, want %q", req.NativePrompt.Question, want)
	}
	if req.NativePrompt.Header != "Spike approval" {
		t.Fatalf("header = %q", req.NativePrompt.Header)
	}
}

func TestPlanApprovalCarriesFullReviewPaths(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	longSpec := "/" + strings.Repeat("spec-path/", 150) + "spec.md"
	longPlan := "/" + strings.Repeat("plan-path/", 150) + "plan.md"
	if _, err := s.DB.ExecContext(ctx, `UPDATE artifacts SET path = ? WHERE id = ?`, longSpec, specID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE artifacts SET path = ? WHERE id = ?`, longPlan, planID); err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: "Build the API and verify it."})
	if err != nil {
		t.Fatal(err)
	}
	if req.ReviewPaths == nil || req.ReviewPaths.Spec != longSpec || req.ReviewPaths.Plan != longPlan {
		t.Fatalf("review paths = %+v", req.ReviewPaths)
	}
	if req.NativePrompt == nil || !strings.HasSuffix(req.NativePrompt.Question, refToken(req.ID)) {
		t.Fatalf("native prompt = %+v", req.NativePrompt)
	}
	if !strings.Contains(NativePromptNextStep(req.ID), "for a plan also print the full absolute review_paths.spec and review_paths.plan") {
		t.Fatalf("initial next step lacks path display instruction: %q", NativePromptNextStep(req.ID))
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.relayRequestTx(ctx, tx, req.ID) }); err != nil {
		t.Fatal(err)
	}
	payload, n := relayFor(t, s, req.AgentID, req.ID)
	if n != 1 {
		t.Fatalf("relays = %d", n)
	}
	paths, ok := payload["review_paths"].(map[string]any)
	if !ok || paths["spec"] != longSpec || paths["plan"] != longPlan {
		t.Fatalf("relay review_paths = %v", payload["review_paths"])
	}
	if payload["summary"] != req.Prompt || payload["next"] != NativePromptNextStep(req.ID) {
		t.Fatalf("replay summary or next step changed: summary=%v next=%v", payload["summary"], payload["next"])
	}
	native, ok := payload["native_prompt"].(map[string]any)
	if !ok || native["question"] != req.NativePrompt.Question {
		t.Fatalf("replay native prompt changed: %v", payload["native_prompt"])
	}
}

// TestNativePromptForPlanApprovalShowsOnlySummaryHeadForHookedAgent is
// 2026-09-28-summary-in-native-question: for a hooked-kind agent (claude,
// agy, codex) the native question shows only the summary's first line,
// capped to 200 runes -- the full summary is printed in chat by the asking
// agent instead (a later hook task enforces that print, not this one).
func TestNativePromptForPlanApprovalShowsOnlySummaryHeadForHookedAgent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, _, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'claude' WHERE id = ?`, ses.AgentID); err != nil {
		t.Fatal(err)
	}
	longFirstLine := strings.Repeat("word ", 60) // > 200 runes
	summary := longFirstLine + "\nsecond line must not appear in the native question."
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: summary})
	if err != nil {
		t.Fatal(err)
	}
	q := req.NativePrompt.Question
	if strings.Contains(q, "second line must not appear") {
		t.Fatalf("native question leaked the summary's second line: %q", q)
	}
	head := strings.SplitN(q, "\n\n", 2)[0]
	if n := utf8.RuneCountInString(head); n > 200 {
		t.Fatalf("summary head = %d runes, want <= 200", n)
	}
	if !strings.HasSuffix(head, "…") {
		t.Fatalf("summary head = %q, want it truncated with an ellipsis", head)
	}
}

// TestStoredNativePromptRebuildIsByteIdenticalForPlanApproval is Task 13a's
// replay contract, re-verified after 2026-09-28-summary-in-native-question:
// storedNativePromptTx (used by relayRequestTx for a re-shown or resurfaced
// question) must reconstruct the exact same Question swarm_ask's own
// askApproval call returned -- summary head, review paths and warnings all
// included -- so the hook's question row still binds to the same ref.
func TestStoredNativePromptRebuildIsByteIdenticalForPlanApproval(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, _, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'claude' WHERE id = ?`, ses.AgentID); err != nil {
		t.Fatal(err)
	}
	summary := strings.Repeat("word ", 60) + "\nsecond line, never shown inline."
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: summary})
	if err != nil {
		t.Fatal(err)
	}
	original := req.NativePrompt.Question

	var rebuilt NativePrompt
	err = s.tx(ctx, func(tx *sql.Tx) error {
		reqRow, err := s.requestTx(ctx, tx, req.ID)
		if err != nil {
			return err
		}
		rebuilt, err = s.storedNativePromptTx(ctx, tx, reqRow)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Question != original {
		t.Fatalf("rebuilt question = %q, want byte-identical to original %q", rebuilt.Question, original)
	}
}

func TestLegacyPlanApprovalReviewPathsAreAbsolute(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, planPath := approvedFeatureSpike(t, s)
	var specPath string
	if err := s.DB.QueryRowContext(ctx, `SELECT path FROM artifacts WHERE id = ?`, specID).Scan(&specPath); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	specRel, _ := filepath.Rel(cwd, specPath)
	planRel, _ := filepath.Rel(cwd, planPath)
	if _, err := s.DB.ExecContext(ctx, `UPDATE artifacts SET path = ? WHERE id = ?`, specRel, specID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE artifacts SET path = ? WHERE id = ?`, planRel, planID); err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: "Build it."})
	if err != nil {
		t.Fatal(err)
	}
	if req.ReviewPaths == nil || req.ReviewPaths.Spec != specPath || req.ReviewPaths.Plan != planPath {
		t.Fatalf("legacy review paths = %+v", req.ReviewPaths)
	}
}

func TestPlanApprovalRequiresCurrentSpecApproval(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Gate", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec", writeFile(t, "## Architecture\n\nUse SQLite.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, planBody), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID, Prompt: "Ship it"}); err == nil || !strings.Contains(err.Error(), "approval_missing") {
		t.Fatalf("plan approval before spec approval: %v", err)
	}
	r, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID, Prompt: "Use SQLite."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, r.ID, ApproveInput{SectionSHA256: spec.Sections[0].SHA256, ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID, Prompt: "Ship it"}); err != nil {
		t.Fatal(err)
	}
}

func TestSpecApprovalReplayRetainsExactSummary(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Replay summary", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec", writeFile(t, "## Design\n\nThe API uses SQLite.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	summary := "| Part | Choice |\n|---|---|\n| Data | SQLite |"
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID, Prompt: summary})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.relayRequestTx(ctx, tx, req.ID) }); err != nil {
		t.Fatal(err)
	}
	payload, n := relayFor(t, s, req.AgentID, req.ID)
	if n != 1 || payload["summary"] != summary {
		t.Fatalf("replayed summary = %v, relays = %d", payload["summary"], n)
	}
}

// TestAskConfirmReposReturnsNativePrompt exercises the real Ask() path for
// confirm_repos.
func TestAskConfirmReposReturnsNativePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoID := seedRepo(t, s, "endurio-chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Confirm test", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repoID}})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.askConfirmRepos(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "Confirm repos",
		Repos: []ReposProposal{{Repo: repoID, Reason: "needed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if req.NativePrompt == nil {
		t.Fatalf("NativePrompt is nil on %+v", req)
	}
	if req.NativePrompt.Header != "Repositories" {
		t.Fatalf("header = %q", req.NativePrompt.Header)
	}
	want := "Confirm 1 repositories for " + key + ": endurio-chat?" + refToken(req.ID)
	if req.NativePrompt.Question != want {
		t.Fatalf("question = %q, want %q", req.NativePrompt.Question, want)
	}
}

// TestAskQuestionBindsARefFromTheirPrompt is Task 13b: a native question row
// binds to the ref token its prompt carries, and an unbound prompt stores no
// binding_json at all.
func TestAskQuestionBindsARefFromTheirPrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Bind", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)

	bound, err := s.AskQuestion(ctx, ses.ID, "Approve the plan (rev 1)?"+refToken("req_PLAN1"),
		[]string{"Approve", "Request changes"})
	if err != nil {
		t.Fatal(err)
	}
	var bindingJSON sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT binding_json FROM requests WHERE id = ?`, bound.ID).
		Scan(&bindingJSON); err != nil {
		t.Fatal(err)
	}
	if !bindingJSON.Valid || bindingJSON.String != `{"ref":"req_PLAN1"}` {
		t.Fatalf("bound binding_json = %v", bindingJSON)
	}

	plain, err := s.AskQuestion(ctx, ses.ID, "Which db?", nil)
	if err != nil {
		t.Fatal(err)
	}
	var plainBinding sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT binding_json FROM requests WHERE id = ?`, plain.ID).
		Scan(&plainBinding); err != nil {
		t.Fatal(err)
	}
	if plainBinding.Valid {
		t.Fatalf("unbound prompt got binding_json = %v", plainBinding)
	}
}

// TestAskNativePromptForMsg is Task 13b: swarm_ask kind:"native_prompt"
// for_msg builds the child-approval prompt for an approval question
// addressed to the caller, and refuses one that is not.
func TestAskNativePromptForMsg(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)

	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q})
	if err != nil {
		t.Fatal(err)
	}
	if req.NativePrompt == nil {
		t.Fatalf("NativePrompt is nil on %+v", req)
	}
	if req.NativePrompt.Header != w.Name+" asks" {
		t.Fatalf("header = %q", req.NativePrompt.Header)
	}
	want := "may I drop table x?" + refToken(q)
	if req.NativePrompt.Question != want {
		t.Fatalf("question = %q, want %q", req.NativePrompt.Question, want)
	}

	// A plain (non-approval) question is refused.
	plainQ, err := s.Send(ctx, wSes.ID, "parent", "question", "which db?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: plainQ}); err == nil {
		t.Fatal("native_prompt for a non-approval question must be refused")
	}

	// A ref naming nothing addressed to the caller is refused too.
	if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: "msg_bogus"}); err == nil {
		t.Fatal("native_prompt for an unknown msg_id must be refused")
	}
}

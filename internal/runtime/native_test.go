package runtime

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

func TestNativePromptNextStepDescribesVisibleReviewAndAgentReportedAnswers(t *testing.T) {
	got := NativePromptNextStep("req_A", approvePair)
	if !strings.HasPrefix(got, "Ask this now with your native question tool:") || strings.Contains(got, "print it exactly") {
		t.Errorf("next step must be the ask instruction only: %s", got)
	}
	for _, want := range []string{"Cursor AskQuestion", "Muse request_user_input", "answer_text", "agent_reported", "cancellation", `ref:"req_A"`,
		"Codex: use request_user_input, not request_user_input_async", "a review question is a design decision the user chooses, not a permission request",
		`decision:"approve"|"request_changes"`} {
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

// TestCapRunesToOneThousand is the 2026-09-28-approval-summary-enforced
// replacement for the old truncateWithToken cap: with no ref token to
// preserve, a body over 1000 runes is simply capped at 1000, ending in "…".
func TestCapRunesToOneThousand(t *testing.T) {
	body := strings.Repeat("a", 2000)
	got := capRunes(body, 1000)
	if n := utf8.RuneCountInString(got); n > 1000 {
		t.Fatalf("length = %d runes, want <= 1000", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("capped body = %q, want it to end with an ellipsis", got)
	}
}

// TestNormalizeQuestion is locked decision 2: trimmed, whitespace runs
// collapsed to one space, so reflowed native-tool text still binds.
func TestNormalizeQuestion(t *testing.T) {
	got := NormalizeQuestion("  Approve  the\nplan   (rev 1)?  ")
	if want := "Approve the plan (rev 1)?"; got != want {
		t.Fatalf("NormalizeQuestion = %q, want %q", got, want)
	}
}

// TestNormForMatch is the summary gate's substring-match normalizer: case
// folded, letters and digits only, so markdown reformatting and wrapping
// never break the match.
func TestNormForMatch(t *testing.T) {
	a := NormForMatch("# Locked Decisions\n\n1. Foo-Bar!")
	b := NormForMatch("locked decisions 1 foobar")
	if a != b {
		t.Fatalf("NormForMatch mismatch: %q vs %q", a, b)
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

// TestApprovalSummaryHeadSkipsLeadingBlankLinesAndTrims covers a summary
// that starts with a blank line (bare \n or \r\n) or leading/trailing
// whitespace on its first real line: the hooked-kind head must be the
// first NON-blank line, trimmed -- not an empty string, and not a line
// carrying a stray \r or spaces.
func TestApprovalSummaryHeadSkipsLeadingBlankLinesAndTrims(t *testing.T) {
	for _, tc := range []struct {
		name, summary, want string
	}{
		{"leading blank line", "\nFirst real line.\nSecond line.", "First real line."},
		{"leading CRLF blank line", "\r\nFirst real line.\r\nSecond line.", "First real line."},
		{"whitespace-only first lines", "   \n\t\nFirst real line.", "First real line."},
		{"surrounding whitespace on first line", "  First real line.  \nSecond.", "First real line."},
		{"trailing CR on first line", "First real line.\r\nSecond.", "First real line."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := approvalSummaryHead(tc.summary, Claude); got != tc.want {
				t.Fatalf("head = %q, want %q", got, tc.want)
			}
		})
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
	got := buildApprovalQuestion(summary, paths, approveLine)
	if n := utf8.RuneCountInString(got); n > 1000 {
		t.Fatalf("question = %d runes, want <= 1000", n)
	}
	if !strings.HasSuffix(got, paths+approveLine) {
		t.Fatalf("tail (paths+approve line) was cut: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("question = %q, want the shortened summary to end with an ellipsis", got)
	}
}

// TestBuildApprovalQuestionFallsBackWhenPathsAloneOverflow is the advisor
// review fix: when paths + approve line + token alone already exceed 1000
// runes (huge absolute paths), buildApprovalQuestion must still return a
// question <= 1000 runes with the token intact -- dropping the summary
// entirely is not enough if paths themselves overflow, so the paths block
// is dropped too (still shown losslessly via review_paths/chat).
func TestBuildApprovalQuestionFallsBackWhenPathsAloneOverflow(t *testing.T) {
	summary := "Ship the API."
	paths := "Spec: /" + strings.Repeat("spec-path/", 150) + "spec.md\n" +
		"Plan: /" + strings.Repeat("plan-path/", 150) + "plan.md\n"
	approveLine := "Approve the plan (rev 1)?"

	got := buildApprovalQuestion(summary, paths, approveLine)
	if n := utf8.RuneCountInString(got); n > 1000 {
		t.Fatalf("question = %d runes, want <= 1000", n)
	}
	if !strings.HasSuffix(got, approveLine) {
		t.Fatalf("approve line was cut: %q", got)
	}
}

// TestBuildApprovalQuestionFallsBackWhenApproveLineAloneOverflows covers the
// no-paths case: a huge warnings block makes the approve line itself exceed
// 1000 runes. The question must still be <= 1000 runes with the token
// intact, even though that now means cutting from the approve line's own
// tail (the old truncateWithToken behaviour) rather than the summary.
func TestBuildApprovalQuestionFallsBackWhenApproveLineAloneOverflows(t *testing.T) {
	var b strings.Builder
	b.WriteString("Approve the plan (rev 1)?\nWarnings:")
	for i := 0; i < 50; i++ {
		b.WriteString("\n- Task t" + strings.Repeat("x", 20) + " has no verify command.")
	}
	approveLine := b.String()

	got := buildApprovalQuestion("Ship the API.", "", approveLine)
	if n := utf8.RuneCountInString(got); n > 1000 {
		t.Fatalf("question = %d runes, want <= 1000", n)
	}
	if got != capRunes(approveLine, 1000) {
		t.Fatalf("approve-line-only fallback = %q, want capRunes result", got)
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
// prompt for each approval kind matches spec section 6, verbatim, with no
// ref token or id (2026-09-28-approval-summary-enforced).
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
			wantQ:      sectionSummary + "\n\n" + `Approve Spec section "Data model" (rev 2)?`,
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_plan_no_warnings",
			req:        Request{ID: "req_PLAN1", Kind: "approve_plan", ArtifactRevision: 1, ItemID: it.ID, AgentID: a.ID, Prompt: planSummary},
			wantHeader: "Spike approval",
			wantQ:      planSummary + "\n\n" + `Approve the plan (rev 1)?`,
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:        "approve_plan_with_paths",
			req:         Request{ID: "req_PLAN3", Kind: "approve_plan", ArtifactRevision: 1, ItemID: it.ID, AgentID: a.ID, Prompt: planSummary},
			reviewPaths: &ReviewPaths{Spec: "/abs/repo/specs/spec.md", Plan: "/abs/repo/plans/plan.md"},
			wantHeader:  "Spike approval",
			wantQ: planSummary + "\n\n" + "Spec: /abs/repo/specs/spec.md\nPlan: /abs/repo/plans/plan.md\n" +
				`Approve the plan (rev 1)?`,
			wantOpts: []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_plan_with_warnings",
			req:        Request{ID: "req_PLAN2", Kind: "approve_plan", ArtifactRevision: 3, ItemID: it.ID, AgentID: a.ID, Prompt: planSummary},
			warnings:   []string{"Task t2 has no verify command."},
			wantHeader: "Spike approval",
			wantQ:      planSummary + "\n\n" + "Approve the plan (rev 3)?\nWarnings:\n- Task t2 has no verify command.",
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_report",
			req:        Request{ID: "req_REP1", Kind: "approve_report", ArtifactRevision: 1, ItemID: it.ID, AgentID: a.ID, Prompt: reportSummary},
			wantHeader: "Spike approval",
			wantQ:      reportSummary + "\n\n" + `Approve the debug report (rev 1)?`,
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name: "confirm_repos",
			req: Request{ID: "req_REPO1", Kind: "confirm_repos", ItemID: it.ID,
				Options: []byte(`{"proposed":[{"repo":"` + repoA + `","reason":"r"}]}`)},
			wantHeader: "Repositories",
			wantQ:      "Confirm 1 repositories for " + key + ": endurio-chat?",
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
				"\nDropped: endurio-docs.",
			wantOpts: []string{"Approve", "Request changes"},
		},
		{
			name:       "close_spike",
			req:        Request{ID: "req_CLOSE1", Kind: "close_spike", ItemID: it.ID},
			wantHeader: "Close spike",
			wantQ:      "Close " + key + "?",
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
		})
	}
}

func TestNativePromptForMsg(t *testing.T) {
	np := nativePromptForMsg("go-migration-agent", "may I drop table x?", "msg_ABC")
	if np.Header != "go-migration-agent asks" {
		t.Fatalf("header = %q", np.Header)
	}
	want := "may I drop table x?"
	if np.Question != want {
		t.Fatalf("question = %q, want %q", np.Question, want)
	}
	if len(np.Options) != 2 || np.Options[0] != "Approve" || np.Options[1] != "Request changes" {
		t.Fatalf("options = %v", np.Options)
	}
	if refFromPrompt(np.Question) != "" {
		t.Fatalf("refFromPrompt = %q, want empty (no token)", refFromPrompt(np.Question))
	}
}

func TestNativePromptQuestionTruncatesToOneThousandRunes(t *testing.T) {
	long := strings.Repeat("x", 1200)
	np := nativePromptForMsg("child", long, "msg_XYZ")
	if got := len([]rune(np.Question)); got > 1000 {
		t.Fatalf("question is %d runes, want <= 1000", got)
	}
	if !strings.HasSuffix(np.Question, "…") {
		t.Fatalf("capped question = %q, want it to end with an ellipsis", np.Question)
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
	want := "Review " + sec.Title + "\n\n" + `Approve Spec section "Data model" (rev 1)?`
	if req.NativePrompt.Question != want {
		t.Fatalf("question = %q, want %q", req.NativePrompt.Question, want)
	}
	if req.NativePrompt.Header != "Spike approval" {
		t.Fatalf("header = %q", req.NativePrompt.Header)
	}
	if refFromPrompt(req.NativePrompt.Question) != "" {
		t.Fatalf("question carries a ref token: %q", req.NativePrompt.Question)
	}
}

// TestAskApprovalAcceptsA1500CharacterSummary is the Opus-review fix: the
// application-level cap moved from 1000 to 2000 characters (askApproval),
// but requests.prompt's own CHECK constraint had to move with it (schema
// migration 0019) or a summary between 1001 and 2000 characters would pass
// Go validation only to fail at INSERT. Unlike TestNativePromptForBuildsExactCopy
// (which builds a Request{} directly, bypassing the DB), this goes through
// the real s.Ask -> askApproval -> INSERT path against the actual migrated
// schema.
func TestAskApprovalAcceptsA1500CharacterSummary(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Long summary", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	p := writeFile(t, "# Spec\n\n## Data model\n\nrows\n")
	res, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", p, "")
	if err != nil {
		t.Fatal(err)
	}
	sec := res.Sections[0]
	// 1500 runes, multi-line: a single line over 300 runes is refused
	// (2026-09-28-approval-chat-block), which is not what this test covers.
	summary := strings.Repeat("word\n", 300)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: summary,
		ArtifactID: res.ArtifactID, SectionID: sec.ID})
	if err != nil {
		t.Fatalf("1500-character summary must be accepted (cap is 2000): %v", err)
	}
	if req.Prompt != summary {
		t.Fatalf("stored prompt length = %d, want %d", len(req.Prompt), len(summary))
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
	if req.NativePrompt == nil {
		t.Fatalf("native prompt = %+v", req.NativePrompt)
	}
	// Paths this long overflow the 1000-rune cap on their own: the question
	// must still fit by dropping them from the inline question (they still
	// reach the user losslessly via review_paths and the chat print above).
	if n := utf8.RuneCountInString(req.NativePrompt.Question); n > 1000 {
		t.Fatalf("native prompt question = %d runes, want <= 1000", n)
	}
	// The full paths now reach the user through chat_block, which the print
	// step tells the agent to reply with.
	if !strings.Contains(PrintNext, "chat_block") {
		t.Fatalf("print step lacks the chat_block instruction: %q", PrintNext)
	}
	if !strings.Contains(req.ChatBlock, "Spec: "+longSpec+"\nPlan: "+longPlan) {
		t.Fatalf("chat_block lacks the full review paths: %q", req.ChatBlock)
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
	if payload["summary"] != req.Prompt || payload["next"] != PrintNext || payload["native_prompt"] != nil {
		t.Fatalf("replay summary or print step changed: summary=%v next=%v", payload["summary"], payload["next"])
	}
	passPrint(t, s, req.SessionID)
	payload, _ = relayFor(t, s, req.AgentID, req.ID)
	if payload["next"] != NativePromptNextStep(req.ID, approvePair) {
		t.Fatalf("ask next step = %v", payload["next"])
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

// TestFrozenNativeQuestionSurvivesStateChanges is 2026-09-28-approval-
// summary-enforced locked decision 2 (post-review): the exact question
// shown at ask time is frozen into binding_json.question/header and
// replayed verbatim by storedNativePromptTx, with no DB lookup at all on
// the frozen path -- a kind change, a plan revision gaining warnings, or any
// other state drift after the question was first issued must never change
// the replayed text or break BindNativeQuestion's match against it. (Before
// this fix, storedNativePromptTx always rebuilt from current state, so a
// kind change between ask and answer broke text-based binding -- see the
// old TestStoredNativePromptRebuildAgentKindChangeBreaksTextBinding this
// test replaces.)
func TestFrozenNativeQuestionSurvivesStateChanges(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, _, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'claude' WHERE id = ?`, ses.AgentID); err != nil {
		t.Fatal(err)
	}
	summary := strings.Repeat("word ", 60) + "\nsecond line."
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: summary})
	if err != nil {
		t.Fatal(err)
	}
	original := req.NativePrompt.Question

	// Change the asking agent's kind (would change the summary head budget
	// if rebuilt) and add a warning to the plan's own revision (would
	// change the approve line if rebuilt).
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'cursor' WHERE id = ?`, req.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE artifact_revisions SET warnings_json = '["new warning"]'
		WHERE artifact_id = ? AND revision = ?`, planID, req.ArtifactRevision); err != nil {
		t.Fatal(err)
	}

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
		t.Fatalf("frozen question drifted after state changes: got %q, want the original %q", rebuilt.Question, original)
	}
	ref, ok := s.BindNativeQuestion(ctx, req.AgentID, "", original)
	if !ok || ref != req.ID {
		t.Fatalf("BindNativeQuestion(original) = %q, %v, want %q, true (frozen text still binds after state changes)", ref, ok, req.ID)
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
	want := "Confirm 1 repositories for " + key + ": endurio-chat?"
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
	want := "may I drop table x?"
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

// TestQuestionTextForRef is 2026-09-28-approval-summary-enforced Task 9:
// nativeAnswer's Muse branch rebuilds the exact native-question text a ref
// currently shows, so ObservedAnswer's session-log scan can match Muse's
// own logged question by text instead of a ref token.
func TestQuestionTextForRef(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, _, planID, _ := approvedFeatureSpike(t, s)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: "Ship it."})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.questionTextForRef(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != req.NativePrompt.Question {
		t.Fatalf("questionTextForRef = %q, want %q", got, req.NativePrompt.Question)
	}

	orch, w, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)
	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	msgPrompt, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q})
	if err != nil {
		t.Fatal(err)
	}
	gotMsg, err := s.questionTextForRef(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if gotMsg != msgPrompt.NativePrompt.Question {
		t.Fatalf("questionTextForRef(msg) = %q, want %q", gotMsg, msgPrompt.NativePrompt.Question)
	}
	_ = w
}

// TestBindNativeQuestion is 2026-09-28-approval-summary-enforced Task 2:
// BindNativeQuestion's text-match binding, the old-token fallback, the
// newest-wins tie-break, and a child-approval message match.
func TestBindNativeQuestion(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Bind", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)

	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", writeFile(t, "## Design\n\nUse SQLite.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Use SQLite for storage.",
		ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	want := req.NativePrompt.Question

	t.Run("exact match", func(t *testing.T) {
		ref, ok := s.BindNativeQuestion(ctx, a.ID, "", want)
		if !ok || ref != req.ID {
			t.Fatalf("BindNativeQuestion = %q, %v, want %q, true", ref, ok, req.ID)
		}
	})
	t.Run("reflowed whitespace still binds", func(t *testing.T) {
		reflowed := strings.ReplaceAll(want, " ", "  \n")
		ref, ok := s.BindNativeQuestion(ctx, a.ID, "", reflowed)
		if !ok || ref != req.ID {
			t.Fatalf("BindNativeQuestion(reflowed) = %q, %v, want %q, true", ref, ok, req.ID)
		}
	})
	t.Run("altered text does not bind", func(t *testing.T) {
		if _, ok := s.BindNativeQuestion(ctx, a.ID, "", want+" extra words"); ok {
			t.Fatal("altered text must not bind")
		}
	})
	t.Run("old token question still binds via fallback", func(t *testing.T) {
		ref, ok := s.BindNativeQuestion(ctx, a.ID, "", "Some other question entirely"+refToken(req.ID))
		if !ok || ref != req.ID {
			t.Fatalf("BindNativeQuestion(token) = %q, %v, want %q, true", ref, ok, req.ID)
		}
	})
	t.Run("no match for an unrelated agent", func(t *testing.T) {
		_, other, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Other", Intent: "feature", Kind: Fake, Model: "fake-1"})
		if _, ok := s.BindNativeQuestion(ctx, other.ID, "", want); ok {
			t.Fatal("a question routed to a different agent must not bind")
		}
	})

	// Child-approval message.
	orch, w, wSes := worker(t, s)
	msgID, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = w
	t.Run("child-approval message binds by text", func(t *testing.T) {
		ref, ok := s.BindNativeQuestion(ctx, orch.ID, "", "may I drop table x?")
		if !ok || ref != msgID {
			t.Fatalf("BindNativeQuestion(child msg) = %q, %v, want %q, true", ref, ok, msgID)
		}
	})

	t.Run("newest of two candidates wins", func(t *testing.T) {
		// A second approval over the exact same artifact revision/section
		// rebuilds byte-identical question text, so a fresh AskQuestion of
		// `want` is genuinely ambiguous between req and req2: newest wins.
		req2, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Use SQLite for storage.",
			ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID})
		if err != nil {
			t.Fatal(err)
		}
		if req2.NativePrompt.Question != want {
			t.Fatalf("expected the second approval's rebuilt question to collide with the first: %q vs %q", req2.NativePrompt.Question, want)
		}
		ref, ok := s.BindNativeQuestion(ctx, a.ID, "", want)
		if !ok || ref != req2.ID {
			t.Fatalf("BindNativeQuestion = %q, %v, want the newer %q", ref, ok, req2.ID)
		}
	})
}

// spawnSecondChild inserts a second coder agent (and a live session) as
// another child of parent, directly via SQL -- worker(t, s) already
// provides the first child; this is the minimal second one, for tests that
// only need two children's names and sessions to exist, not a full spawn
// lifecycle.
func spawnSecondChild(t *testing.T, s *Store, name string, parent Agent) (Agent, string) {
	t.Helper()
	ctx := context.Background()
	id := ids.New("agt")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id, parent_agent_id, brief, state, created_at)
		VALUES (?, ?, 'fake', 'fake-1', 'coder', ?, ?, ?, '', 'active', ?)`,
		id, name, parent.ItemID, parent.RootItemID, parent.ID, db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}
	sesID := ids.New("ses")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 1, ?, ?, '/tmp', 'running', 'neutral', ?)`,
		sesID, id, "hash-"+id, name, db.Millis(s.Now())); err != nil {
		t.Fatal(err)
	}
	return Agent{ID: id, Name: name, ItemID: parent.ItemID, RootItemID: parent.RootItemID}, sesID
}

// TestBindNativeQuestionHeaderDisambiguatesIdenticalChildBodies is
// 2026-09-28-approval-summary-enforced Task 3 (post-review): two children
// of the same orchestrator sending byte-identical approval bodies must not
// cross-bind -- the observed native question's header ("<child> asks")
// picks out the right one.
func TestBindNativeQuestionHeaderDisambiguatesIdenticalChildBodies(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, childA, childASes := worker(t, s)
	childB, childBSes := spawnSecondChild(t, s, "childB", orch)

	msgA, err := s.SendApproval(ctx, childASes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	msgB, err := s.SendApproval(ctx, childBSes, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}

	// Without a header, the match is ambiguous by design (question-only
	// fallback): newest wins, which is msgB here.
	ref, ok := s.BindNativeQuestion(ctx, orch.ID, "", "may I drop table x?")
	if !ok || ref != msgB {
		t.Fatalf("no-header bind = %q, %v, want the newest %q", ref, ok, msgB)
	}

	// With the correct header, each child's own message binds regardless of
	// which is newer.
	ref, ok = s.BindNativeQuestion(ctx, orch.ID, childA.Name+" asks", "may I drop table x?")
	if !ok || ref != msgA {
		t.Fatalf("childA-headered bind = %q, %v, want %q", ref, ok, msgA)
	}
	ref, ok = s.BindNativeQuestion(ctx, orch.ID, childB.Name+" asks", "may I drop table x?")
	if !ok || ref != msgB {
		t.Fatalf("childB-headered bind = %q, %v, want %q", ref, ok, msgB)
	}
}

// TestBindNativeQuestionHeaderPreventsPlainQuestionCrossBindingToChildApproval
// is the other collision the review flagged: an orchestrator's own native
// question tool call, with a header that is NOT "<child> asks" (its own
// plain question's header, whatever that happens to be), must never bind to
// a child's open approval merely because the question text coincides.
func TestBindNativeQuestionHeaderPreventsPlainQuestionCrossBindingToChildApproval(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, child, childSes := worker(t, s)

	msgID, err := s.SendApproval(ctx, childSes.ID, "restart the service?", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = child

	// The orchestrator's own plain question tool call happens to have the
	// exact same text, but a different (its own) header.
	if _, ok := s.BindNativeQuestion(ctx, orch.ID, "Confirm", "restart the service?"); ok {
		t.Fatalf("a plain question with an unrelated header must not bind to the child's approval %q", msgID)
	}
	// Sanity: with no header at all, it still falls back to matching (the
	// documented, pre-existing limitation for callers that can't supply one).
	if ref, ok := s.BindNativeQuestion(ctx, orch.ID, "", "restart the service?"); !ok || ref != msgID {
		t.Fatalf("no-header bind = %q, %v, want %q (fallback)", ref, ok, msgID)
	}
}

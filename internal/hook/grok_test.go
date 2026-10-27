package hook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	usagesvc "github.com/AlexanderTar/agent-swarm/internal/usage"
	"github.com/AlexanderTar/agent-swarm/internal/usagegate"
)

// seedGrok is seed with the agent's kind switched to grok.
func seedGrok(t *testing.T, pending int, state runtime.SessionState) (*Handler, string) {
	t.Helper()
	h, ses := seed(t, pending, state)
	mustExec(t, h.DB, `UPDATE agents SET kind = 'grok' WHERE id = 'agt_1'`)
	return h, ses
}

// grokFixture is a live-probe payload (internal/adapter/testdata/grok-hook-*).
func grokFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../adapter/testdata/grok-hook-" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func grokHandle(t *testing.T, h *Handler, event, ses, fixture string) []byte {
	t.Helper()
	out, err := h.Handle(context.Background(), runtime.Grok, event, ses, grokFixture(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sessionFlag(t *testing.T, h *Handler, col, ses string) int {
	t.Helper()
	var n int
	if err := h.DB.QueryRowContext(context.Background(), `SELECT `+col+` FROM sessions WHERE id = ?`, ses).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNormalizeGrokEvents(t *testing.T) {
	for in, want := range map[string]string{
		"StopFailure":   "StopFailure",
		"StopCancelled": "StopCancelled",
		"Notification":  "Notification",
		"notification":  "Notification",
		"SessionEnd":    "SessionEnd",
	} {
		if got := normalize(runtime.Grok, in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// SessionStart context never reaches a grok model, so the hook must neither
// print it nor spend it: the compaction flag and the inbox notice stay for the
// tool hooks.
func TestGrokSessionStartPrintsNothingAndKeepsNotices(t *testing.T) {
	h, ses := seedGrok(t, 1, runtime.Running)
	mustExec(t, h.DB, `UPDATE sessions SET needs_compaction_notice = 1 WHERE id = 'ses_1'`)
	if out := grokHandle(t, h, "SessionStart", ses, "session-start"); len(out) != 0 {
		t.Fatalf("SessionStart output = %s, want nothing", out)
	}
	if sessionFlag(t, h, "needs_compaction_notice", ses) != 1 {
		t.Fatal("SessionStart must not consume the compaction notice")
	}
	got := contextOf(t, grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-shell"))
	if !strings.Contains(got, runtime.CompactionNotice()) || !strings.Contains(got, "1 new message(s)") {
		t.Fatalf("PreToolUse context = %q, want the compaction and inbox notices", got)
	}
	if sessionFlag(t, h, "needs_compaction_notice", ses) != 0 {
		t.Fatal("the notice is delivered once")
	}
}

func TestGrokSessionStartStillBlocksFork(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Grok, "SessionStart", ses, []byte(`{"session_id":"g","source":"fork"}`))
	if err != nil || !strings.Contains(string(out), "Forked sessions are disabled") {
		t.Fatalf("out = %s, err %v", out, err)
	}
}

func TestGrokToolHooksCarryTheInboxNoticeRateLimited(t *testing.T) {
	h, ses := seedGrok(t, 1, runtime.Running)
	clock := now()
	h.Now = func() time.Time { return clock }
	pre := grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-shell")
	if !strings.Contains(contextOf(t, pre), "Call swarm_sync") || !strings.Contains(string(pre), `"hookEventName":"PreToolUse"`) {
		t.Fatalf("PreToolUse = %s", pre)
	}
	if out := grokHandle(t, h, "PostToolUse", ses, "post-tool-use-swarm"); len(out) != 0 {
		t.Fatalf("a second notice within 60 s must be suppressed: %s", out)
	}
	clock = clock.Add(61 * time.Second)
	if got := contextOf(t, grokHandle(t, h, "PostToolUse", ses, "post-tool-use-swarm")); !strings.Contains(got, "Call swarm_sync") {
		t.Fatalf("after 60 s the notice goes out again: %q", got)
	}
}

func TestGrokPreToolUseWithNothingPendingAllows(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	if out := grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-shell"); len(out) != 0 {
		t.Fatalf("out = %s", out)
	}
}

func TestGrokPreCompactArmsTheCompactionNotice(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	if out := grokHandle(t, h, "PreCompact", ses, "pre-compact"); len(out) != 0 {
		t.Fatalf("PreCompact context never reaches grok, got %s", out)
	}
	if sessionFlag(t, h, "needs_compaction_notice", ses) != 1 {
		t.Fatal("PreCompact must arm needs_compaction_notice for grok")
	}
	if got := contextOf(t, grokHandle(t, h, "PostToolUse", ses, "post-tool-use-swarm")); got != runtime.CompactionNotice() {
		t.Fatalf("context = %q", got)
	}
	if out := grokHandle(t, h, "PostCompact", ses, "post-compact"); len(out) != 0 {
		t.Fatalf("PostCompact out = %s", out)
	}
}

// A Stop with unread mail blocks once; the re-fire carries stopHookActive and
// is allowed.
func TestGrokStopBlocksOnceForUnreadMail(t *testing.T) {
	h, ses := seedGrok(t, 1, runtime.Running)
	var blocked struct {
		Decision, Reason string
	}
	out := grokHandle(t, h, "Stop", ses, "stop-end-turn")
	if err := json.Unmarshal(out, &blocked); err != nil || blocked.Decision != "block" || !strings.Contains(blocked.Reason, "swarm_sync") {
		t.Fatalf("first stop = %s", out)
	}
	if out := grokHandle(t, h, "Stop", ses, "stop-end-turn-active"); len(out) != 0 {
		t.Fatalf("a Stop after our own block must be allowed: %s", out)
	}
	if sessionFlag(t, h, "stop_blocks", ses) != 0 {
		t.Fatal("stop_blocks must reset once the stop is allowed")
	}
}

func TestGrokShutdownStopIsIgnored(t *testing.T) {
	h, ses := seedGrok(t, 1, runtime.Running)
	if out := grokHandle(t, h, "Stop", ses, "stop-shutdown"); len(out) != 0 {
		t.Fatalf("shutdown Stop = %s, want ignored", out)
	}
	if sessionFlag(t, h, "stop_blocks", ses) != 0 {
		t.Fatal("a shutdown Stop must not count as a block")
	}
	// Even a pausing session is not held at shutdown.
	h2, ses2 := seedGrok(t, 0, runtime.PauseRequested)
	if out := grokHandle(t, h2, "Stop", ses2, "stop-shutdown"); len(out) != 0 {
		t.Fatalf("shutdown Stop while pausing = %s", out)
	}
}

func TestGrokStopCancelledAndFailureEndTheTurnWithoutBlocking(t *testing.T) {
	for _, c := range []struct{ event, fixture string }{
		{"StopCancelled", "stop-cancelled"},
		{"StopFailure", "stop-failure"},
	} {
		t.Run(c.event, func(t *testing.T) {
			h, ses := seedGrok(t, 1, runtime.Running)
			mustExec(t, h.DB, `UPDATE sessions SET stop_blocks = 2 WHERE id = 'ses_1'`)
			if out := grokHandle(t, h, c.event, ses, c.fixture); len(out) != 0 {
				t.Fatalf("%s output = %s, want none (a failed or cancelled turn cannot be blocked)", c.event, out)
			}
			if sessionFlag(t, h, "stop_blocks", ses) != 0 {
				t.Fatalf("%s must reset stop_blocks like an allowed Stop", c.event)
			}
		})
	}
}

func TestGrokNotificationIsAccepted(t *testing.T) {
	h, ses := seedGrok(t, 1, runtime.Running)
	for _, f := range []string{"notification-elicitation", "notification-idle"} {
		if out := grokHandle(t, h, "Notification", ses, f); len(out) != 0 {
			t.Errorf("%s output = %s", f, out)
		}
	}
}

// UserPromptSubmit context never reaches grok: its side effects stay (a human
// prompt closes open question rows) but nothing is printed or spent.
func TestGrokUserPromptSubmitKeepsSideEffectsAndSpendsNoNotice(t *testing.T) {
	ctx := context.Background()
	h, ses := seedGrok(t, 1, runtime.Running)
	mustExec(t, h.DB, `UPDATE sessions SET needs_compaction_notice = 1 WHERE id = 'ses_1'`)
	q, err := h.RT.AskQuestion(ctx, ses, "which?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := grokHandle(t, h, "UserPromptSubmit", ses, "user-prompt-submit"); len(out) != 0 {
		t.Fatalf("UserPromptSubmit output = %s, want nothing", out)
	}
	var state string
	if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, q.ID).Scan(&state); err != nil || state != "answered" {
		t.Fatalf("a human prompt must close the open row, state = %q, err %v", state, err)
	}
	if sessionFlag(t, h, "needs_compaction_notice", ses) != 1 {
		t.Fatal("UserPromptSubmit must not consume the compaction notice")
	}
}

func TestIsQuestionToolCoversGroksAskUserQuestion(t *testing.T) {
	for name, want := range map[string]bool{
		"ask_user_question": true, "AskUserQuestion": true, "ask_question": true,
		"request_user_input": true, "search_tool": false, "run_terminal_command": false, "swarm__swarm_ask": false,
	} {
		if isQuestionTool(name) != want {
			t.Errorf("isQuestionTool(%q) = %v, want %v", name, !want, want)
		}
	}
}

func openQuestion(t *testing.T, h *Handler, ses string) (id, prompt, options, state string) {
	t.Helper()
	if err := h.DB.QueryRowContext(context.Background(),
		`SELECT id, prompt, COALESCE(options_json,''), state FROM requests WHERE session_id = ? AND kind = 'question'`, ses).
		Scan(&id, &prompt, &options, &state); err != nil {
		t.Fatal(err)
	}
	return
}

// A top-level grok agent's ask_user_question opens a Needs-you row and is
// denied before its card renders (a card swallows the wake paste), with the
// end-your-turn reason; the answer comes back as a Swarm message.
func TestGrokAskUserQuestionOpensARowAndIsDenied(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	out := grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-ask-user-question")
	var deny struct {
		H map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &deny); err != nil {
		t.Fatalf("out = %s: %v", out, err)
	}
	id, prompt, options, state := openQuestion(t, h, ses)
	reason := deny.H["permissionDecisionReason"]
	if deny.H["permissionDecision"] != "deny" || !strings.Contains(reason, id) ||
		!strings.Contains(strings.ToLower(reason), "end your turn") || !strings.Contains(reason, "swarm_sync") {
		t.Fatalf("deny = %s", out)
	}
	if prompt != "Tea or coffee?" || state != "open" || options != `["Tea","Coffee"]` {
		t.Fatalf("row = %q %q %q", prompt, options, state)
	}
}

func TestGrokParentedAskUserQuestionIsRelayedNotAsked(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	mustExec(t, h.DB, `UPDATE agents SET parent_agent_id = 'agt_1' WHERE id = 'agt_1'`)
	out := grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-ask-user-question")
	if !strings.Contains(string(out), "swarm_send") {
		t.Fatalf("out = %s", out)
	}
	var n int
	h.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM requests`).Scan(&n)
	if n != 0 {
		t.Fatalf("requests = %d", n)
	}
}

// A late PostToolUse (the card answered in the terminal, or an older row)
// still closes the row with grok's UserAnswered text.
func TestGrokPostToolUseBindsTheTerminalAnswer(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-ask-user-question")
	grokHandle(t, h, "PostToolUse", ses, "post-tool-use-ask-user-question")
	var state, text, via string
	if err := h.DB.QueryRowContext(context.Background(),
		`SELECT state, COALESCE(response_text,''), COALESCE(responded_via,'') FROM requests WHERE session_id = ? AND kind = 'question'`, ses).
		Scan(&state, &text, &via); err != nil {
		t.Fatal(err)
	}
	if state != "answered" || text != "Coffee" || via != "terminal" {
		t.Fatalf("row = %s %q %s", state, text, via)
	}
}

func TestExtractToolResponseTextReadsGrokUserAnswered(t *testing.T) {
	msg := func(m string) []byte {
		b, _ := json.Marshal(map[string]any{"type": "AskUserQuestion", "UserAnswered": map[string]string{"message": m}})
		return b
	}
	two := `User has answered your questions: "Tea or coffee?"="Coffee", "Size?"="Large". You can now continue with the user's answers in mind.`
	for name, c := range map[string]struct {
		raw          []byte
		prompt, want string
	}{
		"single":              {msg(`User has answered your questions: "Tea or coffee?"="Coffee". You can now continue.`), "Tea or coffee?", "Coffee"},
		"picks prompt":        {msg(two), "Size?", "Large"},
		"falls back to first": {msg(two), "unrelated", "Coffee"},
		"escaped quote":       {msg(`User has answered your questions: "Q?"="say \"hi\"". You can now continue.`), "Q?", `say "hi"`},
		"headless":            {msg("No user is available to answer questions in this non-interactive session."), "Q?", ""},
	} {
		if got := extractToolResponseText(c.raw, c.prompt); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// A daemon approval question (BindNativeQuestion finds its ref) is not denied
// for grok: the card must show, because only a terminal answer is evidence
// for swarm_ask native_answer. The answer then arrives in PostToolUse, binds
// as terminal, and the forward-it-now next step rides its additionalContext.
func TestGrokBoundApprovalShowsTheCardAndTheAnswerEmitsTheNextStep(t *testing.T) {
	ctx := context.Background()
	h, ses := seedGrok(t, 0, runtime.Running)
	reqID, question := seedApproval(t, h, "Users table gets id and email.")
	input := map[string]any{"questions": []map[string]any{{"question": question,
		"options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}}}}
	pre, _ := json.Marshal(map[string]any{"session_id": "g1", "tool_name": "ask_user_question", "tool_input": input})
	out, err := h.Handle(ctx, runtime.Grok, "PreToolUse", ses, pre)
	if err != nil || len(out) != 0 {
		t.Fatalf("PreToolUse out=%s err=%v, want the card allowed", out, err)
	}
	var n int
	h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question' AND state = 'open'
		AND json_extract(binding_json, '$.ref') = ?`, reqID).Scan(&n)
	if n != 1 {
		t.Fatalf("bound question rows = %d, want 1", n)
	}
	post, _ := json.Marshal(map[string]any{"session_id": "g1", "tool_name": "ask_user_question", "tool_input": input,
		"tool_response": map[string]any{"type": "AskUserQuestion", "UserAnswered": map[string]string{
			"message": `User has answered your questions: "` + question + `"="Approve". You can now continue with the user's answers in mind.`}}})
	out, err = h.Handle(ctx, runtime.Grok, "PostToolUse", ses, post)
	if err != nil {
		t.Fatal(err)
	}
	next := contextOf(t, out)
	if !strings.Contains(next, `Recorded "Approve" for `+reqID) || !strings.Contains(next, "native_answer") {
		t.Fatalf("additionalContext = %q", next)
	}
	var via string
	h.DB.QueryRowContext(ctx, `SELECT responded_via FROM requests WHERE kind = 'question' AND session_id = ?`, ses).Scan(&via)
	if via != "terminal" {
		t.Fatalf("responded_via = %q, want terminal", via)
	}
}

// A bound grok card is held to native_prompt's options like Codex's and
// agy's: invented labels are denied with the stored options in the reason.
func TestGrokBoundApprovalWithInventedLabelsIsDenied(t *testing.T) {
	ctx := context.Background()
	h, ses := seedGrok(t, 0, runtime.Running)
	reqID, question := seedApproval(t, h, "Users table gets id and email.")
	input := map[string]any{"questions": []map[string]any{{"question": question,
		"options": []map[string]any{{"label": "D6-A"}, {"label": "D6-B"}}}}}
	pre, _ := json.Marshal(map[string]any{"session_id": "g1", "tool_name": "ask_user_question", "tool_input": input})
	out, err := h.Handle(ctx, runtime.Grok, "PreToolUse", ses, pre)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"permissionDecision":"deny"`) || !strings.Contains(string(out), "word for word") {
		t.Fatalf("out = %s, want a deny naming native_prompt's options", out)
	}
	var n int
	h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question' AND json_extract(binding_json, '$.ref') = ?`, reqID).Scan(&n)
	if n != 0 {
		t.Fatalf("denied call recorded %d bound question rows, want 0", n)
	}
}

// Grok's Stop carries lastAssistantMessage, so the print-then-ask check judges
// it like Claude's: a printed approval block sends the ask and blocks.
func TestGrokStopAfterPrintedBlockSendsAskAndBlocks(t *testing.T) {
	ctx := context.Background()
	h, ses := seedGrok(t, 0, runtime.Running)
	reqID, block := printingApproval(t, h, "Users table gets id and email.")
	in, _ := json.Marshal(map[string]any{"session_id": "g1", "reason": "end_turn", "stop_hook_active": false, "last_assistant_message": "**" + block + "**"})
	out, err := h.Handle(ctx, runtime.Grok, "Stop", ses, in)
	if err != nil || !strings.Contains(string(out), `"decision":"block"`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if ev := relayEvent(t, h, reqID); ev != "request_ask" {
		t.Fatalf("event = %q, want request_ask", ev)
	}
	// A paraphrase is sent back for a reprint.
	h2, ses2 := seedGrok(t, 0, runtime.Running)
	reqID2, _ := printingApproval(t, h2, "Users table gets id and email.")
	in2, _ := json.Marshal(map[string]any{"session_id": "g1", "reason": "end_turn", "last_assistant_message": "Approving the users table."})
	if _, err := h2.Handle(ctx, runtime.Grok, "Stop", ses2, in2); err != nil {
		t.Fatal(err)
	}
	if ev := relayEvent(t, h2, reqID2); ev != "request_print" {
		t.Fatalf("event = %q, want request_print", ev)
	}
}

// A grok session records its context size at a real end of turn from the
// session dir's signals.json (the Stop hook hands over updates.jsonl).
func TestGrokStopRecordsTheContextSample(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/signals.json", []byte(`{"contextTokensUsed":1234,"contextWindowTokens":256000,"primaryModelId":"grok-4.7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"session_id": "g1", "reason": "end_turn", "transcript_path": dir + "/updates.jsonl"})
	if _, err := h.Handle(context.Background(), runtime.Grok, "Stop", ses, in); err != nil {
		t.Fatal(err)
	}
	if got := sessionFlag(t, h, "context_tokens", ses); got != 1234 {
		t.Fatalf("context_tokens = %d, want 1234", got)
	}
	if got := sessionFlag(t, h, "context_window", ses); got != 256000 {
		t.Fatalf("context_window = %d", got)
	}
}

// exhaustedByHook runs the usage source the way the daemon does, with only the
// hook's exhaustion record behind it, and asks the usage gate's own predicate.
func exhaustedByHook(t *testing.T, ex *usagesvc.GrokExhaustion, now time.Time) bool {
	t.Helper()
	g := &usagesvc.Grok{Exhaustion: ex, Now: func() time.Time { return now }}
	meters, headline, err := g.Fetch(context.Background())
	if errors.Is(err, usagesvc.ErrNoData) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return usagegate.Exhausted(usagesvc.Snapshot{Agent: runtime.Grok, Meters: meters, HeadlineID: headline}, now)
}

func TestGrokExhaustionFromStopFailure(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	for _, c := range []struct{ event, fixture string }{
		{"StopFailure", "stop-failure"},
		{"Notification", "notification-agent-error"},
	} {
		t.Run(c.event, func(t *testing.T) {
			h, ses := seedGrok(t, 0, runtime.Running)
			ex := &usagesvc.GrokExhaustion{}
			h.GrokExhaustion = ex
			h.Now = func() time.Time { return now }
			if exhaustedByHook(t, ex, now) {
				t.Fatal("grok must not read exhausted before any failure")
			}
			grokHandle(t, h, c.event, ses, c.fixture)
			if !exhaustedByHook(t, ex, now) {
				t.Fatalf("a %s carrying the 429 quota error must mark grok exhausted", c.event)
			}
			if exhaustedByHook(t, ex, now.Add(24*time.Hour+time.Minute)) {
				t.Fatal("grok must read available again after the reset")
			}
		})
	}
}

func TestGrokOtherKindsAndPlainStopFailuresDoNotMarkExhausted(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	h, ses := seedGrok(t, 0, runtime.Running)
	ex := &usagesvc.GrokExhaustion{}
	h.GrokExhaustion = ex
	h.Now = func() time.Time { return now }
	grokHandle(t, h, "StopCancelled", ses, "stop-cancelled")
	grokHandle(t, h, "Notification", ses, "notification-idle")
	if exhaustedByHook(t, ex, now) {
		t.Fatal("a cancelled turn and an idle notification are not exhaustion")
	}
}

// An open tool call is what tells the daemon a worker is busy, not stuck: the
// tool hooks open and close it and a turn end clears a lost PostToolUse.
func TestToolHooksTrackTheBusyState(t *testing.T) {
	h, ses := seedGrok(t, 0, runtime.Running)
	if _, ok := h.RT.BusySince(ses); ok {
		t.Fatal("busy before any tool call")
	}
	grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-shell")
	if _, ok := h.RT.BusySince(ses); !ok {
		t.Fatal("PreToolUse must open a tool call")
	}
	grokHandle(t, h, "PostToolUse", ses, "post-tool-use-swarm")
	if _, ok := h.RT.BusySince(ses); ok {
		t.Fatal("PostToolUse must close it")
	}
	grokHandle(t, h, "PreToolUse", ses, "pre-tool-use-shell")
	if _, err := h.Handle(context.Background(), runtime.Grok, "Stop", ses, []byte(`{"session_id":"g"}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.RT.BusySince(ses); ok {
		t.Fatal("a turn end must clear an unclosed tool call")
	}
}

// G3 (TASK-1439): a worker making a tool call every few seconds with mail
// pending must hear about it once per noticeGap, at both PreToolUse and
// PostToolUse, for as long as the mail stays unread.
func TestGrokNoticeCadenceHoldsAcrossManyToolCalls(t *testing.T) {
	h, ses := seedGrok(t, 1, runtime.Running)
	clock := now()
	h.Now = func() time.Time { return clock }
	start := clock
	var at []time.Duration
	for i := 0; clock.Sub(start) < 5*time.Minute; i++ {
		event, fixture := "PreToolUse", "pre-tool-use-shell"
		if i%2 == 1 {
			event, fixture = "PostToolUse", "post-tool-use-swarm"
		}
		if strings.Contains(contextOf(t, grokHandle(t, h, event, ses, fixture)), "Call swarm_sync") {
			at = append(at, clock.Sub(start))
		}
		clock = clock.Add(7 * time.Second)
	}
	if len(at) < 5 {
		t.Fatalf("notices at %v in 5 min of tool calls, want one every ~60 s (5 or more)", at)
	}
	for i := 1; i < len(at); i++ {
		if gap := at[i] - at[i-1]; gap < noticeGap || gap > noticeGap+14*time.Second {
			t.Fatalf("notice gap %v at %v, want %v to %v", gap, at, noticeGap, noticeGap+14*time.Second)
		}
	}
}

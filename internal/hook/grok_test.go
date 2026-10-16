package hook

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
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

package hook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func now() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }

// seed makes EPIC-1, one coder agent, one running session and n pending messages.
func seed(t *testing.T, pending int, state runtime.SessionState) (*Handler, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := context.Background()
	ev := events.New(d, now)
	st := &runtime.Store{DB: d, Events: ev, Items: &items.Store{DB: d, Events: ev, Now: now},
		Now: now, Log: func(string, ...any) {}}
	_, err := d.ExecContext(ctx, `
		INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1','TASK-101','task','itm_1','Build the login form','in_progress',1,1);
		INSERT INTO agents (id,name,kind,model,role,item_id,root_item_id,brief,state,created_at)
		VALUES ('agt_1','login-form-coder','claude','claude-sonnet-5','coder','itm_1','itm_1','','active',1);
		INSERT INTO sessions (id,agent_id,attempt,generation,token_hash,tmux_name,cwd,state,cwd_kind,started_at)
		VALUES ('ses_1','agt_1',1,1,'hash','login-form-coder','/tmp/w',?,'neutral',1);`, string(state))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pending; i++ {
		if _, err := d.ExecContext(ctx, `INSERT INTO messages
			(id,seq,kind,wake_class,priority,origin,to_agent_id,root_item_id,payload_json,state,created_at)
			VALUES (?,?,'finding','immediate',1,'daemon','agt_1','itm_1','{"body":"x"}','pending',1)`,
			"msg_"+string(rune('a'+i)), i+1); err != nil {
			t.Fatal(err)
		}
	}
	return &Handler{DB: d, RT: st, Adapters: adapter.All(adapter.Deps{Home: t.TempDir(),
		UserHome: t.TempDir(), Bin: "/usr/local/bin/swarm", Log: func(string, ...any) {}}),
		Now: now, Log: func(string, ...any) {}}, "ses_1"
}

func contextOf(t *testing.T, out []byte) string {
	t.Helper()
	if len(out) == 0 {
		return ""
	}
	var m struct {
		H struct {
			Ctx string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m.H.Ctx
}

func TestSessionStartInjectsThePendingNoticeOnlyWhenTheInboxHasMessages(t *testing.T) {
	h, ses := seed(t, 2, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := runtime.PendingNotice(2, "login-form-coder", "TASK-101")
	if contextOf(t, out) != want {
		t.Fatalf("context = %q, want %q", contextOf(t, out), want)
	}
	h2, ses2 := seed(t, 0, runtime.Running)
	out2, _ := h2.Handle(context.Background(), runtime.Claude, "SessionStart", ses2,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if len(out2) != 0 {
		t.Fatalf("an empty inbox prints nothing, got %s", out2)
	}
}

// I7: a compact-source SessionStart always carries the compaction notice.
func TestCompactSourceSessionStartAddsTheCompactionNotice(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"compact"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(contextOf(t, out), runtime.CompactionNotice()) {
		t.Fatalf("context = %q", contextOf(t, out))
	}
}

// I7: PreCompact asks for a progress checkpoint and never blocks.
func TestPreCompactAsksForACheckpoint(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "PreCompact", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if contextOf(t, out) != "Write a `progress` checkpoint with your current state before context is compacted." {
		t.Fatalf("context = %q", contextOf(t, out))
	}
	if strings.Contains(string(out), "block") {
		t.Fatalf("PreCompact never blocks: %s", out)
	}
}

// P0-2: codex and cursor get no SessionStart after compaction, so PreCompact sets
// the flag and the next prompt or PostToolUse carries the notice once.
func TestCodexCompactionNoticeArrivesOnTheNextPromptAndClears(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.Handle(context.Background(), runtime.Codex, "PreCompact", ses, []byte(`{"session_id":"p1"}`)); err != nil {
		t.Fatal(err)
	}
	var flag int
	if err := h.DB.QueryRowContext(context.Background(),
		`SELECT needs_compaction_notice FROM sessions WHERE id = ?`, ses).Scan(&flag); err != nil {
		t.Fatal(err)
	}
	if flag != 1 {
		t.Fatal("PreCompact must set needs_compaction_notice for codex")
	}
	out, err := h.Handle(context.Background(), runtime.Codex, "UserPromptSubmit", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(contextOf(t, out), runtime.CompactionNotice()) {
		t.Fatalf("context = %q, want the compaction notice first", contextOf(t, out))
	}
	again, _ := h.Handle(context.Background(), runtime.Codex, "UserPromptSubmit", ses, []byte(`{"session_id":"p1"}`))
	if strings.Contains(contextOf(t, again), "compacted") {
		t.Fatalf("the notice must be delivered once: %q", contextOf(t, again))
	}
}

// C3: while pausing, every non-swarm tool is denied with the control notice.
func TestPreToolUseDeniesNonSwarmToolsWhilePausing(t *testing.T) {
	for _, state := range []runtime.SessionState{runtime.PauseRequested, runtime.Quiescing, runtime.Stopping} {
		h, ses := seed(t, 0, state)
		out, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
			[]byte(`{"session_id":"p1","tool_name":"Edit","tool_input":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]map[string]string
		json.Unmarshal(out, &m)
		if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
			t.Fatalf("%s: output = %s", state, out)
		}
		if m["hookSpecificOutput"]["permissionDecisionReason"] != runtime.ControlNotice("login-form-coder", "TASK-101") {
			t.Fatalf("%s: reason = %q", state, m["hookSpecificOutput"]["permissionDecisionReason"])
		}
		// a swarm tool is still allowed
		ok, _ := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
			[]byte(`{"session_id":"p1","tool_name":"mcp__swarm__swarm_checkpoint"}`))
		if len(ok) != 0 {
			t.Fatalf("%s: a swarm tool must be allowed, got %s", state, ok)
		}
	}
}

func TestPreToolUseAllowsEverythingWhileRunning(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
		[]byte(`{"session_id":"p1","tool_name":"Edit","tool_input":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("output = %s", out)
	}
}

// §11.2: a PostToolUse notice goes out at most once every 60 s.
func TestPostToolUseNoticeIsRateLimited(t *testing.T) {
	h, ses := seed(t, 1, runtime.Running)
	clock := now()
	h.Now = func() time.Time { return clock }
	first, _ := h.Handle(context.Background(), runtime.Claude, "PostToolUse", ses, []byte(`{"session_id":"p1"}`))
	if contextOf(t, first) == "" {
		t.Fatal("the first notice should go out")
	}
	clock = clock.Add(30 * time.Second)
	second, _ := h.Handle(context.Background(), runtime.Claude, "PostToolUse", ses, []byte(`{"session_id":"p1"}`))
	if len(second) != 0 {
		t.Fatalf("a second notice within 60 s must be suppressed: %s", second)
	}
	clock = clock.Add(31 * time.Second)
	third, _ := h.Handle(context.Background(), runtime.Claude, "PostToolUse", ses, []byte(`{"session_id":"p1"}`))
	if contextOf(t, third) == "" {
		t.Fatal("after 60 s the notice goes out again")
	}
}

// §11.2 Stop: the pause check wins over the pending check, and the counter caps at 3.
func TestStopBlocksForAPendingHandoffThenForMessagesUpToThreeTimes(t *testing.T) {
	h, ses := seed(t, 0, runtime.PauseRequested)
	out, err := h.Handle(context.Background(), runtime.Claude, "Stop", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	json.Unmarshal(out, &m)
	if m["decision"] != "block" || m["reason"] != runtime.ControlNotice("login-form-coder", "TASK-101") {
		t.Fatalf("pause stop = %s", out)
	}

	h2, ses2 := seed(t, 1, runtime.Running)
	for i := 1; i <= 3; i++ {
		out, _ := h2.Handle(context.Background(), runtime.Claude, "Stop", ses2, []byte(`{"session_id":"p1"}`))
		var mm map[string]string
		json.Unmarshal(out, &mm)
		if mm["decision"] != "block" {
			t.Fatalf("attempt %d should block: %s", i, out)
		}
	}
	last, _ := h2.Handle(context.Background(), runtime.Claude, "Stop", ses2, []byte(`{"session_id":"p1"}`))
	if len(last) != 0 {
		t.Fatalf("the fourth stop is allowed: %s", last)
	}
	var blocks int
	h2.DB.QueryRowContext(context.Background(), `SELECT stop_blocks FROM sessions WHERE id = ?`, ses2).Scan(&blocks)
	if blocks != 0 {
		t.Fatalf("stop_blocks = %d, want it reset to 0 after the allow", blocks)
	}
}

// Once the handoff exists, Stop is allowed even while pausing.
func TestStopIsAllowedOnceTheHandoffIsWritten(t *testing.T) {
	h, ses := seed(t, 0, runtime.PauseRequested)
	if _, err := h.DB.ExecContext(context.Background(), `INSERT INTO checkpoints
		(id,session_id,agent_id,item_id,kind,attempt,summary,created_at)
		VALUES ('ckp_1',?, 'agt_1','itm_1','handoff',1,'paused at step 2',1)`, ses); err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(context.Background(), runtime.Claude, "Stop", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("output = %s", out)
	}
}

// §11.2: the provider id is stored on the first event that carries one, then never overwritten.
func TestProviderSessionIDIsStoredOnceAndLastSeenIsUpdated(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()
	if _, err := h.Handle(ctx, runtime.Claude, "SessionStart", ses, []byte(`{"session_id":"first"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, []byte(`{"session_id":"second"}`)); err != nil {
		t.Fatal(err)
	}
	var got string
	var lastSeen *int64
	h.DB.QueryRowContext(ctx, `SELECT provider_session_id, last_seen_at FROM sessions WHERE id = ?`, ses).Scan(&got, &lastSeen)
	if got != "first" {
		t.Fatalf("provider_session_id = %q, want the first one", got)
	}
	if lastSeen == nil {
		t.Fatal("last_seen_at must be updated on every hook call")
	}
}

// A ported P1 invariant: no hook event ever creates an item.
func TestNoHookEventCreatesAnItem(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()
	var before int
	h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&before)
	for _, e := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact", "Stop"} {
		h.Handle(ctx, runtime.Claude, e, ses, []byte(`{"session_id":"p1"}`))
	}
	var after int
	h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&after)
	if before != after {
		t.Fatalf("items went from %d to %d", before, after)
	}
}

// An unknown session id is not an error; it prints nothing.
func TestUnknownSessionIsSilent(t *testing.T) {
	h, _ := seed(t, 0, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "Stop", "ses_missing", []byte(`{}`))
	if err != nil || len(out) != 0 {
		t.Fatalf("out = %s, err = %v", out, err)
	}
}

type fakeAdvisor struct {
	scanned []string
}

func (f *fakeAdvisor) ScanTranscript(ctx context.Context, sessionID, transcriptPath string) error {
	f.scanned = append(f.scanned, sessionID+":"+transcriptPath)
	return nil
}

func TestPostToolUseScansAdvisorTranscript(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	adv := &fakeAdvisor{}
	h.Advisor = adv
	ctx := context.Background()
	_, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses,
		[]byte(`{"session_id":"p1","transcript_path":"/tmp/t.json"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(adv.scanned) != 1 || adv.scanned[0] != ses+":/tmp/t.json" {
		t.Fatalf("scanned = %v", adv.scanned)
	}
}

func TestHandlerEdgeCases(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	// Unparseable JSON input
	out, err := h.Handle(ctx, runtime.Claude, "Stop", ses, []byte(`not-json`))
	if err != nil || len(out) != 0 {
		t.Fatalf("unparseable: out=%s, err=%v", out, err)
	}

	// Missing adapter
	hNoAdapters := &Handler{
		DB:  h.DB,
		RT:  h.RT,
		Now: h.Now,
		Log: h.Log,
	}
	out, err = hNoAdapters.Handle(ctx, runtime.Claude, "Stop", ses, []byte(`{}`))
	if err != nil || len(out) != 0 {
		t.Fatalf("no adapter: out=%s, err=%v", out, err)
	}

	// PostCompact
	out, err = h.Handle(ctx, runtime.Claude, "PostCompact", ses, []byte(`{}`))
	if err != nil || len(out) != 0 {
		t.Fatalf("postcompact: out=%s, err=%v", out, err)
	}

	// Default Now() clock
	hDefaultNow := &Handler{
		DB:       h.DB,
		RT:       h.RT,
		Adapters: h.Adapters,
		Log:      h.Log,
	}
	out, err = hDefaultNow.Handle(ctx, runtime.Claude, "Stop", ses, []byte(`{}`))
	if err != nil || len(out) != 0 {
		t.Fatalf("default now: out=%s, err=%v", out, err)
	}
}

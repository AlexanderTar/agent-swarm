package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

func now() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }

// seed makes EPIC-1, one coder agent, one running session and n pending messages.
func seed(t *testing.T, pending int, state runtime.SessionState) (*Handler, string) {
	t.Helper()
	d := dbtest.Open(t)
	ctx := context.Background()
	ev := events.New(d, now)
	st := &runtime.Store{DB: d, Events: ev, Items: &items.Store{DB: d, Events: ev, Now: now},
		Settings: &settings.Store{DB: d, Events: ev, Now: now},
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

func TestPreToolUseBlocksSubagentsWhenBudgetExceeded(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	wantReason := "[swarm] Subagent budget exceeded (max 3 active). Run sequentially or wait for active subagents to finish."

	cases := []struct {
		kind  runtime.AgentKind
		tools []struct {
			name  string
			stdin []byte
		}
		verifyDeny func(t *testing.T, tool string, out []byte)
	}{
		{
			kind: runtime.Claude,
			tools: []struct {
				name  string
				stdin []byte
			}{
				{"mcp__swarm__swarm_spawn", []byte(`{"session_id":"p1","tool_name":"mcp__swarm__swarm_spawn","tool_input":{}}`)},
			},
			verifyDeny: func(t *testing.T, tool string, out []byte) {
				var m map[string]map[string]string
				if err := json.Unmarshal(out, &m); err != nil {
					t.Fatalf("claude %s: %v", tool, err)
				}
				if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
					t.Fatalf("claude %s: want deny, got %s", tool, out)
				}
				if m["hookSpecificOutput"]["permissionDecisionReason"] != wantReason {
					t.Fatalf("claude %s: reason = %q, want %q", tool, m["hookSpecificOutput"]["permissionDecisionReason"], wantReason)
				}
			},
		},
		{
			kind: runtime.Agy,
			tools: []struct {
				name  string
				stdin []byte
			}{
				{"call_mcp_tool:swarm_spawn", []byte(`{"conversationId":"p1","toolCall":{"name":"call_mcp_tool","args":{"ServerName":"swarm","ToolName":"swarm_spawn"}}}`)},
			},
			verifyDeny: func(t *testing.T, tool string, out []byte) {
				var m map[string]string
				if err := json.Unmarshal(out, &m); err != nil {
					t.Fatalf("agy %s: %v", tool, err)
				}
				if m["decision"] != "deny" {
					t.Fatalf("agy %s: want deny, got %s", tool, out)
				}
				if m["reason"] != wantReason {
					t.Fatalf("agy %s: reason = %q, want %q", tool, m["reason"], wantReason)
				}
			},
		},
		{
			kind: runtime.Cursor,
			tools: []struct {
				name  string
				stdin []byte
			}{
				{"MCP:swarm_spawn", []byte(`{"conversation_id":"p1","tool_name":"MCP:swarm_spawn","tool_input":{}}`)},
			},
			verifyDeny: func(t *testing.T, tool string, out []byte) {
				var m map[string]string
				if err := json.Unmarshal(out, &m); err != nil {
					t.Fatalf("cursor %s: %v", tool, err)
				}
				if m["permission"] != "deny" {
					t.Fatalf("cursor %s: want deny, got %s", tool, out)
				}
				if m["user_message"] != wantReason {
					t.Fatalf("cursor %s: reason = %q, want %q", tool, m["user_message"], wantReason)
				}
			},
		},
		{
			kind: runtime.Codex,
			tools: []struct {
				name  string
				stdin []byte
			}{
				{"mcp__swarm__swarm_spawn", []byte(`{"session_id":"p1","tool_name":"mcp__swarm__swarm_spawn","tool_input":{}}`)},
			},
			verifyDeny: func(t *testing.T, tool string, out []byte) {
				var m map[string]string
				if err := json.Unmarshal(out, &m); err != nil {
					t.Fatalf("codex %s: %v", tool, err)
				}
				if m["decision"] != "block" {
					t.Fatalf("codex %s: want block, got %s", tool, out)
				}
				if m["reason"] != wantReason {
					t.Fatalf("codex %s: reason = %q, want %q", tool, m["reason"], wantReason)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = 'agt_1'`, string(tc.kind)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.DB.ExecContext(ctx, `DELETE FROM agents WHERE parent_agent_id = 'agt_1'`); err != nil {
				t.Fatal(err)
			}

			// Under budget: all tools allowed
			for _, tool := range tc.tools {
				out, err := h.Handle(ctx, tc.kind, "PreToolUse", ses, tool.stdin)
				if err != nil {
					t.Fatalf("%s: %v", tool.name, err)
				}
				if len(out) != 0 {
					t.Fatalf("%s: under budget must be allowed, got %s", tool.name, out)
				}
			}

			// Insert 3 active children
			for i := 1; i <= 3; i++ {
				_, err := h.DB.ExecContext(ctx, `INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, parent_agent_id, brief, state, created_at)
					VALUES (?, ?, ?, 'model', 'coder', 'itm_1', 'itm_1', 'agt_1', '', 'active', 1)`,
					fmt.Sprintf("%s_child_%d", tc.kind, i), fmt.Sprintf("child-%s-%d", tc.kind, i), string(tc.kind))
				if err != nil {
					t.Fatal(err)
				}
			}

			// At budget: all tools denied
			for _, tool := range tc.tools {
				out, err := h.Handle(ctx, tc.kind, "PreToolUse", ses, tool.stdin)
				if err != nil {
					t.Fatalf("%s: %v", tool.name, err)
				}
				tc.verifyDeny(t, tool.name, out)
			}

			// One child finishes: active becomes 2 -> allowed again
			if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, fmt.Sprintf("%s_child_1", tc.kind)); err != nil {
				t.Fatal(err)
			}
			out, err := h.Handle(ctx, tc.kind, "PreToolUse", ses, tc.tools[0].stdin)
			if err != nil {
				t.Fatalf("%s: %v", tc.tools[0].name, err)
			}
			if len(out) != 0 {
				t.Fatalf("%s: after child finishes must be allowed, got %s", tc.tools[0].name, out)
			}
		})
	}
}

func TestPreToolUseBlocksNativeForksAndSubagents(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	wantReason := "[swarm] Native forks and subagents are disabled. Use swarm_spawn to delegate work to Swarm-managed agents, or execute tasks sequentially in this session."

	tools := []string{
		"Agent",
		"Task",
		"Fork",
		"fork",
		"invoke_subagent",
		"subagent",
		"dispatch_agent",
		"spawn_agent",
	}

	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			stdin := []byte(fmt.Sprintf(`{"session_id":"p1","tool_name":"%s","tool_input":{}}`, tool))
			out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, stdin)
			if err != nil {
				t.Fatalf("claude %s: %v", tool, err)
			}
			var m map[string]map[string]string
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("claude %s unmarshal: %v", tool, err)
			}
			if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
				t.Fatalf("claude %s: want deny, got %s", tool, out)
			}
			if m["hookSpecificOutput"]["permissionDecisionReason"] != wantReason {
				t.Fatalf("claude %s: reason = %q, want %q", tool, m["hookSpecificOutput"]["permissionDecisionReason"], wantReason)
			}
		})
	}
}

func TestPreToolUseBlocksNestedClaudeShellCommand(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	wantReason := "[swarm] Nested agent invocations via shell are disabled. Use swarm_spawn to delegate work."

	blockedCmds := []string{
		`claude -p "do something"`,
		`claude`,
		"claude\n",
		`claude --fork`,
		`echo hello && claude -p "nested"`,
		`foo; claude`,
		`cat file | claude`,
		`$(claude -p "subshell")`,
	}

	for _, cmd := range blockedCmds {
		t.Run("block_"+cmd, func(t *testing.T) {
			input, _ := json.Marshal(map[string]any{
				"session_id": "p1",
				"tool_name":  "Bash",
				"tool_input": map[string]string{"command": cmd},
			})
			out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, input)
			if err != nil {
				t.Fatalf("%q: %v", cmd, err)
			}
			var m map[string]map[string]string
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("%q unmarshal: %v", cmd, err)
			}
			if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
				t.Fatalf("%q: want deny, got %s", cmd, out)
			}
			if m["hookSpecificOutput"]["permissionDecisionReason"] != wantReason {
				t.Fatalf("%q: reason = %q, want %q", cmd, m["hookSpecificOutput"]["permissionDecisionReason"], wantReason)
			}
		})
	}

	allowedCmds := []string{
		`echo claude`,
		`echo "claude"`,
		`cat claude.txt`,
		`git commit -m "update claude docs"`,
		`echo claude_something`,
	}

	for _, cmd := range allowedCmds {
		t.Run("allow_"+cmd, func(t *testing.T) {
			input, _ := json.Marshal(map[string]any{
				"session_id": "p1",
				"tool_name":  "Bash",
				"tool_input": map[string]string{"command": cmd},
			})
			out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, input)
			if err != nil {
				t.Fatalf("%q: %v", cmd, err)
			}
			if len(out) != 0 {
				t.Fatalf("%q: want allowed (empty output), got %s", cmd, out)
			}
		})
	}
}

func TestSessionStartBlocksForkSource(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	wantReason := "[swarm] Forked sessions are disabled. Work must run within the assigned Swarm session."

	out, err := h.Handle(ctx, runtime.Claude, "SessionStart", ses, []byte(`{"session_id":"p1","source":"fork"}`))
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
		t.Fatalf("want deny, got %s", out)
	}
	if m["hookSpecificOutput"]["permissionDecisionReason"] != wantReason {
		t.Fatalf("reason = %q, want %q", m["hookSpecificOutput"]["permissionDecisionReason"], wantReason)
	}
}

func TestIsClaudeCommand(t *testing.T) {
	positives := []string{
		"claude",
		"claude ",
		"claude\n",
		"claude -p 'test'",
		"claude --fork",
		"  claude -p test",
		"VAR=1 claude",
		"foo && claude",
		"foo; claude",
		"foo | claude",
		"$(claude)",
		"`claude`",
		"/usr/local/bin/claude -p foo",
		"./claude",
		"sudo claude -p foo",
		"env claude -p foo",
	}
	for _, s := range positives {
		if !isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = false, want true", s)
		}
	}

	negatives := []string{
		"",
		"echo claude",
		`echo "claude"`,
		"echo claude -p",
		"cat claude.txt",
		"grep claude file",
		`git commit -m "update claude"`,
		"claude_tools",
		"myclaude",
	}
	for _, s := range negatives {
		if isClaudeCommand(s) {
			t.Errorf("isClaudeCommand(%q) = true, want false", s)
		}
	}
}

func TestQuestionToolInterceptionCreatesHITLRequest(t *testing.T) {
	ctx := context.Background()

	t.Run("claude AskUserQuestion", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		input, _ := json.Marshal(map[string]any{
			"session_id": "p1",
			"tool_name":  "AskUserQuestion",
			"tool_input": map[string]any{
				"question": "Deploy to staging?",
				"options":  []string{"yes", "no"},
			},
		})
		out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Fatalf("question tool must not be blocked, got %s", out)
		}

		var count, isHITL int
		var prompt, kind string
		err = h.DB.QueryRowContext(ctx, `SELECT COUNT(*), is_hitl, prompt, kind FROM requests WHERE session_id = ?`, ses).
			Scan(&count, &isHITL, &prompt, &kind)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 || isHITL != 1 || prompt != "Deploy to staging?" || kind != "question" {
			t.Fatalf("expected 1 hitl question request, got count=%d isHITL=%d prompt=%q kind=%q", count, isHITL, prompt, kind)
		}
	})

	t.Run("agy ask_question", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		_, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'agy' WHERE id = 'agt_1'`)
		if err != nil {
			t.Fatal(err)
		}

		input, _ := json.Marshal(map[string]any{
			"conversationId": "p1",
			"toolCall": map[string]any{
				"name": "ask_question",
				"args": map[string]any{
					"questions": []map[string]any{
						{
							"question": "Which database engine?",
							"options":  []string{"postgres", "sqlite"},
						},
					},
				},
			},
		})
		out, err := h.Handle(ctx, runtime.Agy, "PreToolUse", ses, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Fatalf("question tool must not be blocked, got %s", out)
		}

		var count, isHITL int
		var prompt, kind string
		err = h.DB.QueryRowContext(ctx, `SELECT COUNT(*), is_hitl, prompt, kind FROM requests WHERE session_id = ?`, ses).
			Scan(&count, &isHITL, &prompt, &kind)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 || isHITL != 1 || prompt != "Which database engine?" || kind != "question" {
			t.Fatalf("expected 1 hitl question request, got count=%d isHITL=%d prompt=%q kind=%q", count, isHITL, prompt, kind)
		}
	})
}

func TestPermissionRequestCreatesHITLRequest(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	_, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'codex' WHERE id = 'agt_1'`)
	if err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(map[string]any{
		"command": "terraform apply",
	})
	out, err := h.Handle(ctx, runtime.Codex, "PermissionRequest", ses, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("permission request hook should return empty, got %s", out)
	}

	var count, isHITL int
	var prompt, kind string
	err = h.DB.QueryRowContext(ctx, `SELECT COUNT(*), is_hitl, prompt, kind FROM requests WHERE session_id = ?`, ses).
		Scan(&count, &isHITL, &prompt, &kind)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || isHITL != 1 || prompt != "terraform apply" || kind != "prompt" {
		t.Fatalf("expected 1 hitl prompt request, got count=%d isHITL=%d prompt=%q kind=%q", count, isHITL, prompt, kind)
	}
}

type testSession struct {
	ID string
}

func newTestHandler(t *testing.T) (*Handler, *runtime.Store, testSession) {
	t.Helper()
	h, ses := seed(t, 0, runtime.Running)
	_, err := h.DB.ExecContext(context.Background(), `UPDATE agents SET kind = 'agy' WHERE id = 'agt_1'`)
	if err != nil {
		t.Fatal(err)
	}
	return h, h.RT, testSession{ID: ses}
}

func TestPostToolUseResolvesOpenQuestionRequest(t *testing.T) {
	h, rt, ses := newTestHandler(t)
	ctx := context.Background()

	// Intercept ask_question in PreToolUse opens request
	input := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PreToolUse", ses.ID, input)

	var reqID, state string
	err := rt.DB.QueryRowContext(ctx, `SELECT id, state FROM requests WHERE session_id = ? AND kind = 'question'`, ses.ID).Scan(&reqID, &state)
	if err != nil || state != "open" {
		t.Fatalf("expected open question request, got err=%v, state=%s", err, state)
	}

	// Tool finishes; PostToolUse fires
	postInput := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]},"tool_response":{"answer":"PostgreSQL"}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PostToolUse", ses.ID, postInput)

	var responseText, respondedVia sql.NullString
	err = rt.DB.QueryRowContext(ctx, `SELECT state, response_text, responded_via FROM requests WHERE id = ?`, reqID).Scan(&state, &responseText, &respondedVia)
	if err != nil || state != "answered" {
		t.Fatalf("expected request answered, got err=%v, state=%s", err, state)
	}
	if responseText.String != "PostgreSQL" {
		t.Fatalf("expected response_text 'PostgreSQL', got %q", responseText.String)
	}
	if respondedVia.String != "terminal" {
		t.Fatalf("expected responded_via 'terminal', got %q", respondedVia.String)
	}
}

func TestPostToolUseResolvesOpenQuestionRequestFallbackWhenEmptyResponse(t *testing.T) {
	h, rt, ses := newTestHandler(t)
	ctx := context.Background()

	input := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PreToolUse", ses.ID, input)

	var reqID, state string
	err := rt.DB.QueryRowContext(ctx, `SELECT id, state FROM requests WHERE session_id = ? AND kind = 'question'`, ses.ID).Scan(&reqID, &state)
	if err != nil || state != "open" {
		t.Fatalf("expected open question request, got err=%v, state=%s", err, state)
	}

	// Tool finishes without explicit answer
	postInput := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PostToolUse", ses.ID, postInput)

	var responseText, respondedVia sql.NullString
	err = rt.DB.QueryRowContext(ctx, `SELECT state, response_text, responded_via FROM requests WHERE id = ?`, reqID).Scan(&state, &responseText, &respondedVia)
	if err != nil || state != "answered" {
		t.Fatalf("expected request answered, got err=%v, state=%s", err, state)
	}
	if responseText.String != "Resolved in terminal" {
		t.Fatalf("expected fallback 'Resolved in terminal', got %q", responseText.String)
	}
	if respondedVia.String != "terminal" {
		t.Fatalf("expected responded_via 'terminal', got %q", respondedVia.String)
	}
}

func TestPostToolUseClaudeResolvesOpenQuestionRequest(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	input, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"question": "Deploy to staging?",
			"options":  []string{"yes", "no"},
		},
	})
	_, _ = h.Handle(ctx, runtime.Claude, "PreToolUse", ses, input)

	var reqID, state string
	err := h.DB.QueryRowContext(ctx, `SELECT id, state FROM requests WHERE session_id = ? AND kind = 'question'`, ses).Scan(&reqID, &state)
	if err != nil || state != "open" {
		t.Fatalf("expected open question request, got err=%v, state=%s", err, state)
	}

	postInput, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"question": "Deploy to staging?",
			"options":  []string{"yes", "no"},
		},
		"tool_response": map[string]any{
			"answer": "yes",
		},
	})
	_, _ = h.Handle(ctx, runtime.Claude, "PostToolUse", ses, postInput)

	var responseText, respondedVia sql.NullString
	err = h.DB.QueryRowContext(ctx, `SELECT state, response_text, responded_via FROM requests WHERE id = ?`, reqID).Scan(&state, &responseText, &respondedVia)
	if err != nil || state != "answered" {
		t.Fatalf("expected request answered, got err=%v, state=%s", err, state)
	}
	if responseText.String != "yes" {
		t.Fatalf("expected response_text 'yes', got %q", responseText.String)
	}
	if respondedVia.String != "terminal" {
		t.Fatalf("expected responded_via 'terminal', got %q", respondedVia.String)
	}
}

func TestPostToolUseNonQuestionToolDoesNotResolveOpenQuestionRequest(t *testing.T) {
	h, rt, ses := newTestHandler(t)
	ctx := context.Background()

	input := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PreToolUse", ses.ID, input)

	var reqID, state string
	err := rt.DB.QueryRowContext(ctx, `SELECT id, state FROM requests WHERE session_id = ? AND kind = 'question'`, ses.ID).Scan(&reqID, &state)
	if err != nil || state != "open" {
		t.Fatalf("expected open question request, got err=%v, state=%s", err, state)
	}

	// Non-question tool finishes
	postInput := []byte(`{"session_id":"` + ses.ID + `","tool_name":"run_command","tool_response":{"output":"done"}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PostToolUse", ses.ID, postInput)

	err = rt.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, reqID).Scan(&state)
	if err != nil || state != "open" {
		t.Fatalf("expected request to remain open, got err=%v, state=%s", err, state)
	}
}




// A message the agent has already synced (state 'delivered') is not "new": no
// nudge on PostToolUse and no Stop block. Only 'pending' counts.
func TestReadButUnackedMessagesNeitherNudgeNorBlockStop(t *testing.T) {
	h, ses := seed(t, 2, runtime.Running)
	ctx := context.Background()
	if _, err := h.DB.ExecContext(ctx, `UPDATE messages SET state = 'delivered', delivery_count = 1`); err != nil {
		t.Fatal(err)
	}
	post, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, post); got != "" {
		t.Fatalf("a delivered message must not nudge: %q", got)
	}
	stop, err := h.Handle(ctx, runtime.Claude, "Stop", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(stop) != 0 {
		t.Fatalf("a delivered message must not block Stop: %s", stop)
	}
}

func TestParentedAgentQuestionToolIsBlockedAndOpensNoRequest(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		kind runtime.AgentKind
		tool string
	}{{runtime.Claude, "AskUserQuestion"}, {runtime.Codex, "request_user_input"}, {runtime.Cursor, "ask_question"}, {runtime.Agy, "ask_question"}} {
		t.Run(string(c.kind)+" "+c.tool, func(t *testing.T) {
			h, ses := seed(t, 0, runtime.Running)
			// The seeded agent "has a parent" via its own id: the FK is satisfied and the
			// hook only tests for a non-empty parent_agent_id.
			if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET parent_agent_id = 'agt_1', kind = ? WHERE id = 'agt_1'`, string(c.kind)); err != nil {
				t.Fatal(err)
			}
			in, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": c.tool,
				"tool_input": map[string]any{"question": "Deploy?"}})
			out, err := h.Handle(ctx, c.kind, "PreToolUse", ses, in)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), "swarm_send") {
				t.Fatalf("a parented agent's question tool must be blocked with the relay text, got %s", out)
			}
			var n int
			if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("requests = %d, want 0", n)
			}
		})
	}
}

func TestQuestionToolPostToolUseClosesOnlyTheMatchingRow(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	// B? is opened by the tool call first; A? (a swarm_ask) is opened later, so A? is
	// the newest open question and the old "newest open question" logic would close it.
	pre, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "AskUserQuestion", "tool_input": map[string]any{"question": "B?"}})
	if _, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, pre); err != nil {
		t.Fatal(err)
	}
	h.RT.Now = func() time.Time { return now().Add(time.Minute) }
	a, err := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "A?"})
	if err != nil {
		t.Fatal(err)
	}
	post, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"question": "B?"}, "tool_response": map[string]any{"answer": "yes"}})
	if _, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, post); err != nil {
		t.Fatal(err)
	}
	state := func(prompt string) (s string) {
		if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE prompt = ?`, prompt).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := state("B?"); got != "answered" {
		t.Fatalf("B? = %s, want answered", got)
	}
	if got := state("A?"); got != "open" {
		t.Fatalf("A? = %s, want open (%s)", got, a.ID)
	}
}

func TestQuestionToolPostToolUseWithoutToolInputClosesNothing(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "A?"}); err != nil {
		t.Fatal(err)
	}
	post, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "AskUserQuestion", "tool_response": map[string]any{"answer": "yes"}})
	if _, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, post); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "open" {
		t.Fatalf("state = %s, want open (no tool_input, no match)", st)
	}
}

func TestPostToolUseResolvesOnlyTheMatchingPermissionPrompt(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'codex' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	perm, _ := json.Marshal(map[string]any{"command": "terraform apply"})
	if _, err := h.Handle(ctx, runtime.Codex, "PermissionRequest", ses, perm); err != nil {
		t.Fatal(err)
	}
	stateOf := func() (state, via string) {
		if err := h.DB.QueryRowContext(ctx, `SELECT state, COALESCE(responded_via, '') FROM requests
			WHERE session_id = ? AND kind = 'prompt'`, ses).Scan(&state, &via); err != nil {
			t.Fatal(err)
		}
		return
	}
	other, _ := json.Marshal(map[string]any{"command": "ls"})
	if _, err := h.Handle(ctx, runtime.Codex, "PostToolUse", ses, other); err != nil {
		t.Fatal(err)
	}
	if st, _ := stateOf(); st != "open" {
		t.Fatalf("state after unrelated tool = %s, want open", st)
	}
	if _, err := h.Handle(ctx, runtime.Codex, "PostToolUse", ses, perm); err != nil {
		t.Fatal(err)
	}
	if st, via := stateOf(); st != "answered" || via != "terminal" {
		t.Fatalf("state after matching tool = %s via %s, want answered via terminal", st, via)
	}
}

func TestHumanPromptClosesOpenRowsButDaemonPromptsDoNot(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	q, _ := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "which?"})
	submit := func(prompt string) {
		t.Helper()
		in, _ := json.Marshal(map[string]any{"session_id": "p1", "prompt": prompt})
		if _, err := h.Handle(ctx, runtime.Claude, "UserPromptSubmit", ses, in); err != nil {
			t.Fatal(err)
		}
	}
	state := func() (s string) {
		if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, q.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, daemon := range []string{runtime.IdleToken, runtime.Kickoff("login-form-coder", runtime.RoleCoder, "TASK-101", "T"),
		runtime.PendingNotice(1, "login-form-coder", "TASK-101"), ""} {
		submit(daemon)
		if got := state(); got != "open" {
			t.Fatalf("after daemon prompt %q the row is %s, want open", daemon, got)
		}
	}
	submit("Use zod")
	if got := state(); got != "answered" {
		t.Fatalf("after a human prompt the row is %s, want answered", got)
	}
}

func TestAgyPromptlessSubmitNeverClosesRows(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	q, _ := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "which?"})
	in, _ := json.Marshal(map[string]any{"conversationId": "c1"})
	if _, err := h.Handle(ctx, runtime.Agy, "PreInvocation", ses, in); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, q.ID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "open" {
		t.Fatalf("row = %s, want open (agy sends no prompt text)", st)
	}
}

func TestHumanPromptInANewSessionClosesTheRowOfTheOldOne(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	q, _ := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "which?"})
	if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = 'ses_1';
		INSERT INTO sessions (id,agent_id,attempt,generation,token_hash,tmux_name,cwd,state,cwd_kind,started_at)
		VALUES ('ses_2','agt_1',1,2,'hash2','login-form-coder-2','/tmp/w','running','neutral',2)`); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"session_id": "p2", "prompt": "Use zod"})
	if _, err := h.Handle(ctx, runtime.Claude, "UserPromptSubmit", "ses_2", in); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, q.ID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "answered" {
		t.Fatalf("row = %s, want answered (rows are keyed by agent, not session)", st)
	}
}

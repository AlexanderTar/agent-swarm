package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
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
		Now:      now, Log: func(string, ...any) {}}
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

func TestSessionStartInjectsTheRichInboxNoticeOnlyWhenTheInboxHasMessages(t *testing.T) {
	h, ses := seed(t, 2, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := contextOf(t, out)
	if !strings.Contains(got, `"x"`) || !strings.Contains(got, "swarm_sync") {
		t.Fatalf("context = %q, want the rich inbox notice with message content", got)
	}
	h2, ses2 := seed(t, 0, runtime.Running)
	out2, _ := h2.Handle(context.Background(), runtime.Claude, "SessionStart", ses2,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if got := contextOf(t, out2); got != "" {
		t.Fatalf("an empty inbox adds no context, got %q", got)
	}
}

func titleOf(t *testing.T, out []byte) string {
	t.Helper()
	var m struct {
		H struct {
			Title string `json:"sessionTitle"`
		} `json:"hookSpecificOutput"`
	}
	if len(out) == 0 {
		return ""
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m.H.Title
}

// TASK-769: claude's session title follows the Swarm agent name via the hook,
// even when the decision carries nothing else.
func TestClaudeSessionStartAndPromptCarryTheAgentNameAsSessionTitle(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		out, err := h.Handle(context.Background(), runtime.Claude, ev, ses,
			[]byte(`{"session_id":"p1","source":"startup","prompt":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := titleOf(t, out); got != "login-form-coder" {
			t.Errorf("%s sessionTitle = %q, want login-form-coder (out=%s)", ev, got, out)
		}
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
	// BUG-29: Claude Code rejects hookSpecificOutput for PreCompact; the reminder
	// travels in the top-level systemMessage.
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if _, bad := m["hookSpecificOutput"]; bad {
		t.Fatalf("PreCompact must not emit hookSpecificOutput: %s", out)
	}
	if m["systemMessage"] != "Write a `progress` checkpoint with your current state before context is compacted." {
		t.Fatalf("systemMessage = %v", m["systemMessage"])
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

// Preservation mode (spec §3, supersedes C3 deny-all): while pausing, the
// save path (read/edit/shell/commit) stays allowed, while delegation and
// push/deploy are denied with the preservation reason.
func TestPreToolUsePreservationPolicyWhilePausing(t *testing.T) {
	for _, state := range []runtime.SessionState{runtime.PauseRequested, runtime.Quiescing, runtime.Stopping} {
		h, ses := seed(t, 0, state)
		// a save-path native tool is allowed
		allowed, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
			[]byte(`{"session_id":"p1","tool_name":"Edit","tool_input":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(allowed) != 0 {
			t.Fatalf("%s: Edit must be allowed while preserving, got %s", state, allowed)
		}
		// delegation is denied
		out, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
			[]byte(`{"session_id":"p1","tool_name":"Agent","tool_input":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]map[string]string
		json.Unmarshal(out, &m)
		if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
			t.Fatalf("%s: output = %s", state, out)
		}
		if !strings.Contains(m["hookSpecificOutput"]["permissionDecisionReason"], "delegates") {
			t.Fatalf("%s: reason = %q, want the preservation denial", state, m["hookSpecificOutput"]["permissionDecisionReason"])
		}
		// push is denied even on the save path
		pushed, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
			[]byte(`{"session_id":"p1","tool_name":"Bash","tool_input":{"command":"git push origin main"}}`))
		if err != nil {
			t.Fatal(err)
		}
		var pm map[string]map[string]string
		json.Unmarshal(pushed, &pm)
		if pm["hookSpecificOutput"]["permissionDecision"] != "deny" {
			t.Fatalf("%s: git push must be denied while preserving: %s", state, pushed)
		}
		// staging a secret is denied on the save path too
		secrets, err := h.Handle(context.Background(), runtime.Claude, "PreToolUse", ses,
			[]byte(`{"session_id":"p1","tool_name":"Bash","tool_input":{"command":"git add .env"}}`))
		if err != nil {
			t.Fatal(err)
		}
		var sm map[string]map[string]string
		json.Unmarshal(secrets, &sm)
		if sm["hookSpecificOutput"]["permissionDecision"] != "deny" {
			t.Fatalf("%s: git add .env must be denied while preserving: %s", state, secrets)
		}
		if !strings.Contains(sm["hookSpecificOutput"]["permissionDecisionReason"], "never commit") {
			t.Fatalf("%s: reason = %q, want the secrets denial", state, sm["hookSpecificOutput"]["permissionDecisionReason"])
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
	if m["decision"] != "block" || m["reason"] != runtime.PausePreservationNotice("login-form-coder", "TASK-101") {
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

func TestPreToolUseNeverBudgetBlocksSwarmSpawn(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	cases := []struct {
		kind  runtime.AgentKind
		tools []struct {
			name  string
			stdin []byte
		}
	}{
		{
			kind: runtime.Claude,
			tools: []struct {
				name  string
				stdin []byte
			}{
				{"mcp__swarm__swarm_spawn", []byte(`{"session_id":"p1","tool_name":"mcp__swarm__swarm_spawn","tool_input":{}}`)},
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
		},
		{
			kind: runtime.Cursor,
			tools: []struct {
				name  string
				stdin []byte
			}{
				{"MCP:swarm_spawn", []byte(`{"conversation_id":"p1","tool_name":"MCP:swarm_spawn","tool_input":{}}`)},
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

			// Five active children: far past the removed default budget of 3.
			for i := 1; i <= 5; i++ {
				_, err := h.DB.ExecContext(ctx, `INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, parent_agent_id, brief, state, created_at)
					VALUES (?, ?, ?, 'model', 'coder', 'itm_1', 'itm_1', 'agt_1', '', 'active', 1)`,
					fmt.Sprintf("%s_child_%d", tc.kind, i), fmt.Sprintf("child-%s-%d", tc.kind, i), string(tc.kind))
				if err != nil {
					t.Fatal(err)
				}
			}
			// There is one limit (max_concurrent_agents), enforced by Admit and
			// the reconciler, never by the hook: every spawn tool stays allowed.
			for _, tool := range tc.tools {
				out, err := h.Handle(ctx, tc.kind, "PreToolUse", ses, tool.stdin)
				if err != nil {
					t.Fatalf("%s: %v", tool.name, err)
				}
				if len(out) != 0 {
					t.Fatalf("%s: swarm_spawn must never be budget-blocked, got %s", tool.name, out)
				}
			}
		})
	}
}

func mustExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
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

// TestPreToolUseBlocksWorkflowTool is spec A6: the native Workflow tool is
// disabled in Swarm sessions, both spellings ("Workflow" the tool name,
// "workflow" as some adapters lowercase it).
func TestPreToolUseBlocksWorkflowTool(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	wantReason := "[swarm] The Workflow tool is disabled in Swarm sessions. Use swarm_spawn or swarm_workflow."

	for _, tool := range []string{"Workflow", "workflow"} {
		t.Run(tool, func(t *testing.T) {
			stdin := []byte(fmt.Sprintf(`{"session_id":"p1","tool_name":"%s","tool_input":{}}`, tool))
			out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, stdin)
			if err != nil {
				t.Fatalf("%s: %v", tool, err)
			}
			var m map[string]map[string]string
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("%s unmarshal: %v", tool, err)
			}
			if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
				t.Fatalf("%s: want deny, got %s", tool, out)
			}
			if m["hookSpecificOutput"]["permissionDecisionReason"] != wantReason {
				t.Fatalf("%s: reason = %q, want %q", tool, m["hookSpecificOutput"]["permissionDecisionReason"], wantReason)
			}
		})
	}
}

// TestPreToolUseBlocksNativeAgentMessaging: native SendMessage/ListAgents see
// unrelated local sessions, so Swarm sessions use swarm_send instead.
func TestPreToolUseBlocksNativeAgentMessaging(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()

	wantReason := "[swarm] Native agent messaging is disabled in Swarm sessions. Use swarm_send (to: an agent name, \"parent\", or another top-level item's key) and swarm_read to find peers."

	for _, tool := range []string{"SendMessage", "ListAgents"} {
		t.Run(tool, func(t *testing.T) {
			stdin := []byte(fmt.Sprintf(`{"session_id":"p1","tool_name":"%s","tool_input":{}}`, tool))
			out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, stdin)
			if err != nil {
				t.Fatalf("%s: %v", tool, err)
			}
			var m map[string]map[string]string
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("%s unmarshal: %v (%s)", tool, err, out)
			}
			if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
				t.Fatalf("%s: want deny, got %s", tool, out)
			}
			if m["hookSpecificOutput"]["permissionDecisionReason"] != wantReason {
				t.Fatalf("%s: reason = %q, want %q", tool, m["hookSpecificOutput"]["permissionDecisionReason"], wantReason)
			}
		})
	}
}

// TestWorkflowToolAllowedOutsideSwarm: a PreToolUse call for a session Swarm
// doesn't manage (no row in sessions) is a no-op, same as any other tool --
// the block only applies inside a Swarm session.
func TestWorkflowToolAllowedOutsideSwarm(t *testing.T) {
	h, _ := seed(t, 0, runtime.Running)
	ctx := context.Background()
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", "not-a-swarm-session",
		[]byte(`{"session_id":"p1","tool_name":"Workflow","tool_input":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("output = %s, want no-op outside a swarm session", out)
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

	t.Run("claude AskUserQuestion with a native prompt ref binds the row", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		input, _ := json.Marshal(map[string]any{
			"session_id": "p1",
			"tool_name":  "AskUserQuestion",
			"tool_input": map[string]any{
				"question": "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧",
				"options":  []string{"Approve", "Request changes"},
			},
		})
		out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Fatalf("question tool must not be blocked, got %s", out)
		}
		var bindingJSON sql.NullString
		if err := h.DB.QueryRowContext(ctx, `SELECT binding_json FROM requests WHERE session_id = ?`, ses).
			Scan(&bindingJSON); err != nil {
			t.Fatal(err)
		}
		if !bindingJSON.Valid || bindingJSON.String != `{"ref":"req_PLAN1"}` {
			t.Fatalf("binding_json = %v, want {\"ref\":\"req_PLAN1\"}", bindingJSON)
		}
	})

	t.Run("cursor AskQuestion", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		_, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'cursor' WHERE id = 'agt_1'`)
		if err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(map[string]any{
			"session_id": "p1",
			"tool_name":  "AskQuestion",
			"tool_input": map[string]any{
				"question": "Deploy to staging?",
				"options":  []string{"yes", "no"},
			},
		})
		out, err := h.Handle(ctx, runtime.Cursor, "PreToolUse", ses, input)
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

// TestQuestionToolBindsALongMultiLineNativeQuestion is the
// 2026-09-28-summary-in-native-question regression: nativePromptFor's
// question now carries a summary head, optional review paths, and the
// approve line (with warnings) ahead of the ⟦swarm:ref⟧ token -- several
// lines, ~990 runes, close to the 1000-rune cap. PreToolUse must still bind
// the request row to that ref regardless, for every hooked kind (claude,
// codex, agy).
func TestQuestionToolBindsALongMultiLineNativeQuestion(t *testing.T) {
	ctx := context.Background()
	question := "Ship auth end to end: login, session cookies, and logout across every surface -- " +
		"web, mobile, and the public API -- including refresh-token rotation and the new " +
		"device-approval step, with backward compatibility for existing sessions during the " +
		"full rollout window and staged deploy.\n\n" +
		"Spec: /Users/dev/repo/docs/specs/2026-09-28-auth.md\n" +
		"Plan: /Users/dev/repo/docs/plans/2026-09-28-auth.md\n" +
		"Approve the plan (rev 3)?\nWarnings:" +
		"\n- Task t2 has no verify command." +
		"\n- Task t5 depends on t9, which is not yet materialized." +
		"\n- Task t7's workflow role does not match its role_hint." +
		"\n- Task t11 has no acceptance criteria listed." +
		"\n- Task t14's verify command references a script that does not exist yet." +
		"\n- Task t16's dependency graph has a cycle through t3 and t8." +
		"\n- Task t18 was split from t4 but never re-batched with its siblings." +
		"\n- Task t20's role_hint disagrees with the workflow template's own role assignment." +
		"\n- Task t22's package review has not resolved every critical finding yet." +
		" ⟦swarm:req_PLAN9⟧"
	if n := utf8.RuneCountInString(question); n < 950 || n > 1000 {
		t.Fatalf("fixture setup: question = %d runes, want ~990 (close to the 1000-rune cap)", n)
	}

	assertBoundToPlan9 := func(t *testing.T, h *Handler, ses string) {
		t.Helper()
		var bindingJSON sql.NullString
		if err := h.DB.QueryRowContext(ctx, `SELECT binding_json FROM requests WHERE session_id = ?`, ses).
			Scan(&bindingJSON); err != nil {
			t.Fatal(err)
		}
		if !bindingJSON.Valid || bindingJSON.String != `{"ref":"req_PLAN9"}` {
			t.Fatalf("binding_json = %v, want {\"ref\":\"req_PLAN9\"}", bindingJSON)
		}
	}

	t.Run("claude AskUserQuestion", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		input, _ := json.Marshal(map[string]any{
			"session_id": "p1",
			"tool_name":  "AskUserQuestion",
			"tool_input": map[string]any{"question": question, "options": []string{"Approve", "Request changes"}},
		})
		if _, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, input); err != nil {
			t.Fatal(err)
		}
		assertBoundToPlan9(t, h, ses)
	})

	t.Run("codex request_user_input", func(t *testing.T) {
		h, ses := codexSeed(t)
		codexHook(t, h, ses, "PreToolUse", codexPreToolUse(map[string]any{"title": question,
			"options": []string{"Approve", "Request changes"}}))
		assertBoundToPlan9(t, h, ses)
	})

	t.Run("agy ask_question", func(t *testing.T) {
		h, ses := seed(t, 0, runtime.Running)
		if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'agy' WHERE id = 'agt_1'`); err != nil {
			t.Fatal(err)
		}
		input, _ := json.Marshal(map[string]any{
			"conversationId": "p1",
			"toolCall": map[string]any{
				"name": "ask_question",
				"args": map[string]any{"questions": []map[string]any{{"question": question,
					"options": []string{"Approve", "Request changes"}}}},
			},
		})
		if _, err := h.Handle(ctx, runtime.Agy, "PreToolUse", ses, input); err != nil {
			t.Fatal(err)
		}
		assertBoundToPlan9(t, h, ses)
	})
}

// TestAgyLiveHookFixturesOpenAndCloseAQuestionRow replays the byte-for-byte
// PreToolUse/PostToolUse payloads captured from a live, non-Swarm agy session
// asking `ask_question` (docs/plans/2026-09-25-needs-you-and-child-approval-routing.md
// Task 4b). It is the regression guard behind spec section 1.7's agy row: a
// top-level agy agent's ask_question opens a HITL row, and the matching
// PostToolUse (which carries no result field, confirmed live) closes it.
func TestAgyLiveHookFixturesOpenAndCloseAQuestionRow(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'agy' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}

	pre, err := os.ReadFile("../adapter/testdata/agy-hook-pretooluse-ask_question.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(ctx, runtime.Agy, "PreToolUse", ses, pre)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("question tool must not be blocked, got %s", out)
	}

	var reqID, state, prompt, kind string
	err = h.DB.QueryRowContext(ctx, `SELECT id, state, prompt, kind FROM requests WHERE session_id = ?`, ses).
		Scan(&reqID, &state, &prompt, &kind)
	if err != nil {
		t.Fatal(err)
	}
	if state != "open" || kind != "question" || prompt != "Choose red or blue." {
		t.Fatalf("got state=%q kind=%q prompt=%q, want open/question/%q", state, kind, prompt, "Choose red or blue.")
	}

	post, err := os.ReadFile("../adapter/testdata/agy-hook-posttooluse-ask_question.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(ctx, runtime.Agy, "PostToolUse", ses, post); err != nil {
		t.Fatal(err)
	}
	if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, reqID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "answered" {
		t.Fatalf("state = %q, want answered", state)
	}
}

func TestParentedAgyLiveHookFixtureIsBlocked(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'agy', parent_agent_id = 'agt_1' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	pre, err := os.ReadFile("../adapter/testdata/agy-hook-pretooluse-ask_question.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(ctx, runtime.Agy, "PreToolUse", ses, pre)
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

// TestMuseSiblingToolHookFixturesOpenNoRowAndDoNotBlock replays the Task 4
// live-probe PreToolUse/PostToolUse fixtures. They capture a sibling tool
// call (submit_reminder_decision), not request_user_input -- muse never
// dispatches a hook for its own request_user_input (spec section 1.7), so
// there is no request_user_input payload to replay and no row to open or
// block from this path. This locks in the true behavior instead of
// fabricating a request_user_input payload that was never observed live.
func TestMuseSiblingToolHookFixturesOpenNoRowAndDoNotBlock(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'muse' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}

	pre, err := os.ReadFile("../adapter/testdata/muse-hook-pretooluse.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(ctx, runtime.Muse, "PreToolUse", ses, pre)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("a sibling tool call must not be blocked, got %s", out)
	}

	post, err := os.ReadFile("../adapter/testdata/muse-hook-posttooluse.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(ctx, runtime.Muse, "PostToolUse", ses, post); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE session_id = ?`, ses).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("requests = %d, want 0 (no hook ever fires for muse's own request_user_input)", n)
	}
}

// TestMuseUserPromptSubmitClosesOpenQuestionRows replays the Task 4
// live-probe UserPromptSubmit fixture: a top-level muse agent has no way to
// have Swarm open its native question's row (see the sibling-tool test
// above), so it uses swarm_ask kind:"question" directly (spec section 1.7,
// muse joins cursor's exception). This is the fallback that still works for
// muse: UserPromptSubmit closes whatever question/blocker rows are open when
// the human types a reply in the terminal, same as every other kind
// (handler.go's UserPromptSubmit case, ResolveAnsweredInTerminal).
func TestMuseUserPromptSubmitClosesOpenQuestionRows(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'muse' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	req, err := h.RT.AskQuestion(ctx, ses, "Red or blue?", []string{"Red", "Blue"})
	if err != nil {
		t.Fatal(err)
	}

	prompt, err := os.ReadFile("../adapter/testdata/muse-hook-userpromptsubmit.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(ctx, runtime.Muse, "UserPromptSubmit", ses, prompt); err != nil {
		t.Fatal(err)
	}

	var state, via string
	if err := h.DB.QueryRowContext(ctx, `SELECT state, responded_via FROM requests WHERE id = ?`, req.ID).
		Scan(&state, &via); err != nil {
		t.Fatal(err)
	}
	if state != "answered" || via != "terminal" {
		t.Fatalf("state = %q via %q, want answered/terminal", state, via)
	}
}

func TestParentedAgentQuestionToolIsBlockedAndOpensNoRequest(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		kind runtime.AgentKind
		tool string
	}{{runtime.Claude, "AskUserQuestion"}, {runtime.Codex, "request_user_input"}, {runtime.Codex, "request_user_input_async"}, {runtime.Cursor, "ask_question"}, {runtime.Agy, "ask_question"}} {
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

// TestPreToolUseDeniesMultiQuestionBatchWithSwarmRef is the 2026-09-26 fix
// (native-railway-tracing finding): the hook only ever binds Questions[0],
// so a batched AskUserQuestion call that carries a swarm ref anywhere in it
// must be refused up front rather than silently losing every ref past the
// first.
func TestPreToolUseDeniesMultiQuestionBatchWithSwarmRef(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)

	in, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{
				{"question": "Approve section 1? ⟦swarm:req_S1⟧"},
				{"question": "Approve section 2? ⟦swarm:req_S2⟧"},
			},
		},
	})
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Ask one swarm approval per question call.") {
		t.Fatalf("a batched swarm-ref question call must be denied with that reason, got %s", out)
	}

	var n int
	if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("requests = %d, want 0 (denied before AskQuestion ran)", n)
	}
}

// TestPreToolUseDeniesMultiQuestionBatchBoundByText is
// 2026-09-28-approval-summary-enforced: a daemon-issued native question no
// longer carries a ⟦swarm:ref⟧ token, so the batched guard above (which
// only ever catches an old-style token) must also catch a batch whose
// Questions[1] matches an open approval's rebuilt text.
func TestPreToolUseDeniesMultiQuestionBatchBoundByText(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, is_hitl, agent_id, session_id, item_id, prompt, options_json, state, created_at)
		VALUES ('req_close1', 'close_spike', 0, 'agt_1', 'ses_1', 'itm_1', '', '[]', 'open', 1)`); err != nil {
		t.Fatal(err)
	}

	in, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{
				{"question": "Which db?"},
				{"question": "Close TASK-101?"},
			},
		},
	})
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Ask one swarm approval per question call.") {
		t.Fatalf("a batched call whose second question binds by text must be denied, got %s", out)
	}

	var n int
	if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("question rows = %d, want 0 (denied before AskQuestion ran)", n)
	}
}

// TestPreToolUseAllowsSingleQuestionWithSwarmRef confirms the new batch
// check does not catch the normal, single-question native_prompt flow.
func TestPreToolUseAllowsSingleQuestionWithSwarmRef(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)

	in, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{
				{"question": "Approve section 1? ⟦swarm:req_S1⟧"},
			},
		},
	})
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "Ask one swarm approval per question call.") {
		t.Fatalf("a single-question call must not be denied, got %s", out)
	}
}

// TestPreToolUseAllowsMultiQuestionBatchWithoutSwarmRef confirms an
// ordinary, non-swarm multi-question call (agy asking the user several
// unrelated things at once) is untouched.
func TestPreToolUseAllowsMultiQuestionBatchWithoutSwarmRef(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)

	in, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{
				{"question": "Which database?"},
				{"question": "Which region?"},
			},
		},
	})
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "Ask one swarm approval per question call.") {
		t.Fatalf("a plain multi-question call must not be denied, got %s", out)
	}
}

// TestPreToolUseAllowsMultiQuestionBatchWithMalformedRefLikeText confirms the
// batch check matches the real ⟦swarm:ref⟧ token shape (native.go's refRe),
// not a bare "⟦swarm:" substring -- text that merely mentions the token
// syntax without a well-formed, closed ref must not trip the guard.
func TestPreToolUseAllowsMultiQuestionBatchWithMalformedRefLikeText(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)

	in, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{
				{"question": "Does ⟦swarm: look right to you?"},
				{"question": "Which region?"},
			},
		},
	})
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "Ask one swarm approval per question call.") {
		t.Fatalf("an unterminated, non-ref-shaped mention of the token syntax must not be denied, got %s", out)
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
	a, err := h.RT.AskQuestion(ctx, ses, "A?", nil)
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
	if _, err := h.RT.AskQuestion(ctx, ses, "A?", nil); err != nil {
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
	q, _ := h.RT.AskQuestion(ctx, ses, "which?", nil)
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
	for _, daemon := range []string{runtime.IdleToken, runtime.Kickoff("login-form-coder", runtime.RoleCoder, items.Task, "TASK-101", "T"),
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
	q, _ := h.RT.AskQuestion(ctx, ses, "which?", nil)
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
	q, _ := h.RT.AskQuestion(ctx, ses, "which?", nil)
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

func TestSessionStartUsesRichInboxNotice(t *testing.T) {
	h, ses := seed(t, 2, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := contextOf(t, out)
	// v2: real newlines between items are the template's own structure, not
	// message content (spec Locked decision 2) -- one item per line.
	if !strings.Contains(got, "\n- ") {
		t.Fatalf("SessionStart context should render one item per line: %q", got)
	}
	// Rich content, not just a count: the finding bodies ("x") and their ids.
	if !strings.Contains(got, `"x"`) {
		t.Errorf("SessionStart context missing message content: %q", got)
	}
	if !strings.Contains(got, "swarm_sync") {
		t.Errorf("SessionStart context missing swarm_sync pointer: %q", got)
	}
}

func TestUserPromptSubmitSkipsDoubleDeliveryForDaemonPrompt(t *testing.T) {
	h, ses := seed(t, 2, runtime.Running)
	// The pasted/native-delivered notice becomes the next prompt; the hook
	// must not stack another notice on top of the daemon's own text.
	daemonPrompt, err := json.Marshal(map[string]string{
		"session_id": "p1",
		"prompt":     runtime.PendingNotice(2, "login-form-coder", "TASK-101"),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(context.Background(), runtime.Claude, "UserPromptSubmit", ses, daemonPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if contextOf(t, out) != "" {
		t.Fatalf("daemon prompt got a stacked context: %q", contextOf(t, out))
	}
}

func TestPostToolUseStaysTerseUnderRepeatedCalls(t *testing.T) {
	h, ses := seed(t, 2, runtime.Running)
	out, err := h.Handle(context.Background(), runtime.Claude, "PostToolUse", ses,
		[]byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := runtime.PendingNotice(2, "login-form-coder", "TASK-101")
	if contextOf(t, out) != want {
		t.Fatalf("PostToolUse context = %q, want terse %q", contextOf(t, out), want)
	}
}

// TestExtractToolResponseTextReadsAnswers is Task B4 finding 1: a real claude
// AskUserQuestion PostToolUse tool_response has the shape
// {questions, answers:{<question text>: <chosen label(s)>}, annotations}
// (confirmed from toolUseResult in a local ~/.claude transcript,
// 83e3eeed-213a-4f4f-98e8-03dc059ee72a.jsonl). extractToolResponseText must
// read the prompt's own key out of "answers", not just answer/response/
// text/output/result, or every claude decision looks agent_reported.
func TestExtractToolResponseTextReadsAnswers(t *testing.T) {
	raw := []byte(`{"questions":[{"question":"Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧","header":"Plan","options":[{"label":"Approve"},{"label":"Request changes"}]}],"answers":{"Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧":"Request changes: tighten scope"},"annotations":{}}`)
	got := extractToolResponseText(raw, "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧")
	if got != "Request changes: tighten scope" {
		t.Fatalf("got %q, want the answers[prompt] value", got)
	}
	// A prompt that doesn't match any key falls back to the first value
	// rather than losing the answer entirely.
	if got := extractToolResponseText(raw, "some other prompt"); got != "Request changes: tighten scope" {
		t.Fatalf("fallback got %q", got)
	}
	// The old generic keys still work for other adapters/tools.
	if got := extractToolResponseText([]byte(`{"answer":"yes"}`), "x"); got != "yes" {
		t.Fatalf("generic key got %q", got)
	}
}

// TestClaudeAskUserQuestionAnswerBecomesObservedEvidence is Task B4 finding 1's
// end-to-end regression: PreToolUse binds the row to the daemon's ref token,
// then a PostToolUse tool_response shaped like a real claude AskUserQuestion
// result (questions/answers/annotations) must resolve the row with the
// user's actual chosen label as response_text, not the generic
// "Resolved in terminal" fallback -- so native_answer's evidence
// classification (matchDecisionEvidence) can tell an observed decision from
// an agent's word, and a decision that disagrees with what the user picked
// is refused.
func TestClaudeAskUserQuestionAnswerBecomesObservedEvidence(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	question := "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧"

	pre, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{{"question": question,
				"options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}}},
		},
	})
	if _, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, pre); err != nil {
		t.Fatal(err)
	}

	post, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{{"question": question,
				"options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}}},
		},
		"tool_response": map[string]any{
			"questions":   []map[string]any{{"question": question}},
			"answers":     map[string]string{question: "Approve"},
			"annotations": map[string]any{},
		},
	})
	if _, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, post); err != nil {
		t.Fatal(err)
	}

	var responseText string
	if err := h.DB.QueryRowContext(ctx, `SELECT COALESCE(response_text,'') FROM requests
		WHERE session_id = ?`, ses).Scan(&responseText); err != nil {
		t.Fatal(err)
	}
	if responseText != "Approve" {
		t.Fatalf("response_text = %q, want the observed %q, not the agent_reported fallback", responseText, "Approve")
	}

	// A decision that disagrees with what the user actually picked ("Approve")
	// is refused as a mismatch, not silently accepted as agent_reported.
	if _, err := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "native_answer", Ref: "req_PLAN1",
		Decision: "request_changes", Comment: "x"}); err == nil || !strings.Contains(err.Error(), `"Approve"`) {
		t.Fatalf("err = %v, want a decision mismatch against the observed \"Approve\"", err)
	}
}

// TestPostToolUseAnsweredSwarmRefEmitsForwardingNextStep is the 2026-09-26
// fix (native-railway-tracing finding): the spike bound 10 approvals via the
// hook and never called native_answer, because nothing told it to. Once a
// ref-bearing native question row is recorded as answered, PostToolUse must
// say so and name the exact next call.
func TestPostToolUseAnsweredSwarmRefEmitsForwardingNextStep(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	question := "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧"

	pre, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{{"question": question,
				"options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}}},
		},
	})
	if _, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, pre); err != nil {
		t.Fatal(err)
	}

	post, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{{"question": question,
				"options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}}},
		},
		"tool_response": map[string]any{
			"questions":   []map[string]any{{"question": question}},
			"answers":     map[string]string{question: "Approve"},
			"annotations": map[string]any{},
		},
	})
	out, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, post)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("PostToolUse output = %s, not decodable: %v", out, err)
	}
	next := decoded.HookSpecificOutput.AdditionalContext
	if !strings.Contains(next, `Recorded "Approve" for req_PLAN1`) ||
		!strings.Contains(next, `native_answer`) ||
		!strings.Contains(next, `ref:"req_PLAN1"`) ||
		!strings.Contains(next, `decision:"approve"`) {
		t.Fatalf("additionalContext = %q, want the forward-it-now next step", next)
	}
}

// TestPostToolUseNextStepIsNotRateLimitedAndDoesNotStampNoticeAt is the
// Opus-review fix's own regression test: the native-answer next step must
// survive the pending-notice rate limit (a recent noticeAt must still
// suppress the pending notice itself), and appending it must not stamp
// noticeAt -- doing so would silently suppress a later, real pending-inbox
// notice for the full noticeGap.
func TestPostToolUseNextStepIsNotRateLimitedAndDoesNotStampNoticeAt(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 1, runtime.Running) // Pending > 0
	sentinelNow := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
	h.Now = func() time.Time { return sentinelNow }
	stamped := sentinelNow.Add(-10 * time.Second) // within noticeGap (60s): canNotice = false
	h.noticeAt = map[string]time.Time{ses: stamped}
	question := "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧"

	pre, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{{"question": question,
				"options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}}},
		},
	})
	if _, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, pre); err != nil {
		t.Fatal(err)
	}

	post, _ := json.Marshal(map[string]any{
		"session_id": "p1",
		"tool_name":  "AskUserQuestion",
		"tool_input": map[string]any{
			"questions": []map[string]any{{"question": question,
				"options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}}},
		},
		"tool_response": map[string]any{
			"questions":   []map[string]any{{"question": question}},
			"answers":     map[string]string{question: "Approve"},
			"annotations": map[string]any{},
		},
	})
	out, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, post)
	if err != nil {
		t.Fatal(err)
	}
	next := contextOf(t, out)
	if !strings.Contains(next, "native_answer") {
		t.Fatalf("additionalContext = %q, want the native_answer next step", next)
	}
	if strings.Contains(next, "Call swarm_sync") {
		t.Fatalf("additionalContext = %q, want the rate-limited pending notice (PendingNotice) suppressed", next)
	}

	h.mu.Lock()
	got := h.noticeAt[ses]
	h.mu.Unlock()
	if !got.Equal(stamped) {
		t.Fatalf("noticeAt[%s] = %v, want unchanged %v (next step must not stamp it)", ses, got, stamped)
	}
}

// TestAgyPostToolUseWithNoResponseTextGetsPickedOptionNextStep is the Opus
// review's minor item 4: agy's PostToolUse carries no response text (spec
// 1.7), so a ref-bearing question resolves via the ResolvedInTerminal
// placeholder. AGY rejects injectSteps on PostToolUse, so the output is {}.
func TestAgyPostToolUseWithNoResponseTextGetsPickedOptionNextStep(t *testing.T) {
	ctx := context.Background()
	h, _, ses := newTestHandler(t)
	question := "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧"

	pre := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"` + question + `"}]}}`)
	if _, err := h.Handle(ctx, runtime.Agy, "PreToolUse", ses.ID, pre); err != nil {
		t.Fatal(err)
	}

	post := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"` + question + `"}]}}`)
	out, err := h.Handle(ctx, runtime.Agy, "PostToolUse", ses.ID, post)
	if err != nil {
		t.Fatal(err)
	}
	// AGY rejects injectSteps on PostToolUse (BUG-13): the contract is an empty
	// object, so the next step cannot ride this event.
	if strings.TrimSpace(string(out)) != "{}" {
		t.Fatalf("PostToolUse output = %s, want {}", out)
	}
	// The next step is deferred, not dropped: the next PreInvocation (where
	// injectSteps is valid) delivers it.
	next, err := h.Handle(ctx, runtime.Agy, "PreInvocation", ses.ID, []byte(`{"session_id":"`+ses.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(next), "injectSteps") || !strings.Contains(string(next), "req_PLAN1") {
		t.Fatalf("PreInvocation output = %s, want the deferred picked-option next step in injectSteps", next)
	}
	again, _ := h.Handle(ctx, runtime.Agy, "PreInvocation", ses.ID, []byte(`{"session_id":"`+ses.ID+`"}`))
	if strings.Contains(string(again), "req_PLAN1") {
		t.Fatalf("deferred context delivered twice: %s", again)
	}
}

// PostInvocation accepts injectSteps, so its context is delivered at once and
// must not also be deferred to the next PreInvocation (CHORE-27 final review).
func TestAgyPostInvocationContextIsNotDeferredAgain(t *testing.T) {
	ctx := context.Background()
	h, _, ses := newTestHandler(t)
	question := "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧"
	body := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"` + question + `"}]}}`)
	if _, err := h.Handle(ctx, runtime.Agy, "PreToolUse", ses.ID, body); err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(ctx, runtime.Agy, "PostInvocation", ses.ID, body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "injectSteps") || !strings.Contains(string(out), "req_PLAN1") {
		t.Fatalf("PostInvocation output = %s, want the next step in injectSteps", out)
	}
	next, _ := h.Handle(ctx, runtime.Agy, "PreInvocation", ses.ID, []byte(`{"session_id":"`+ses.ID+`"}`))
	if strings.Contains(string(next), "req_PLAN1") {
		t.Fatalf("PostInvocation context delivered again on PreInvocation: %s", next)
	}
}

// TestPostToolUseAnsweredQuestionWithoutRefEmitsNoNextStep confirms a plain
// question's PostToolUse (no ⟦swarm:ref⟧) is untouched.
func TestPostToolUseAnsweredQuestionWithoutRefEmitsNoNextStep(t *testing.T) {
	ctx := context.Background()
	h, _, ses := newTestHandler(t)

	pre := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]}}`)
	if _, err := h.Handle(ctx, runtime.Agy, "PreToolUse", ses.ID, pre); err != nil {
		t.Fatal(err)
	}
	post := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]},"tool_response":{"answer":"PostgreSQL"}}`)
	out, err := h.Handle(ctx, runtime.Agy, "PostToolUse", ses.ID, post)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "native_answer") {
		t.Fatalf("a ref-less question must not get a native_answer next step, got %s", out)
	}
}

// A handoff rides the pause delivery path, so its Stop block must carry the
// HANDOFF notice (a fresh session follows), not the PAUSE one.
func TestStopBlocksWithHandoffNoticeDuringHandoff(t *testing.T) {
	h, ses := seed(t, 0, runtime.PauseRequested)
	if _, err := h.DB.ExecContext(context.Background(), `INSERT INTO agent_operations
		(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
		VALUES ('op_h', 'agt_1', 'handoff', 'preserving', 'k', ?, 1, 1, 1)`, ses); err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(context.Background(), runtime.Claude, "Stop", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	json.Unmarshal(out, &m)
	if m["decision"] != "block" || m["reason"] != runtime.HandoffPreservationNotice("login-form-coder", "TASK-101") {
		t.Fatalf("handoff stop = %s", out)
	}
}

// seedApproval seeds an open approve_section request for agt_1 (an
// empty artifact/section id, so sectionTitle resolves to "" without a DB
// lookup) with the given summary as its prompt, and returns the exact
// native question text a real hook-bound question tool call must carry to
// bind to it (buildApprovalQuestion's approve_section shape, capped
// summary head first line included -- agt_1 is kind claude, so the head is
// the summary's own first non-blank line, capped to 200 runes).
func seedApproval(t *testing.T, h *Handler, summary string) (reqID, question string) {
	t.Helper()
	ctx := context.Background()
	reqID = "req_sumgate1"
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, is_hitl, agent_id, session_id, item_id, artifact_revision, prompt, options_json, state, created_at)
		VALUES (?, 'approve_section', 0, 'agt_1', 'ses_1', 'itm_1', 1, ?, '[]', 'open', 1)`, reqID, summary); err != nil {
		t.Fatal(err)
	}
	head := summary
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	question = head + "\n\n" + `Approve Spec section "" (rev 1)?`
	return reqID, question
}

func askInput(t *testing.T, question, transcriptPath string) []byte {
	t.Helper()
	in, err := json.Marshal(map[string]any{
		"session_id":      "p1",
		"tool_name":       "AskUserQuestion",
		"transcript_path": transcriptPath,
		"tool_input": map[string]any{
			"questions": []map[string]any{
				{"question": question, "options": []map[string]any{{"label": "Approve"}, {"label": "Request changes"}}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func claudeUserLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

func claudeAssistantTextLine(id, text string) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"id": id, "role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}}})
	return string(b)
}

// seedPlanApproval seeds a real spec+plan artifact pair and an open
// approve_plan request over them, so the print check can resolve real review
// paths -- unlike a bare request row with no artifacts.
func seedPlanApproval(t *testing.T, h *Handler, summary string) (reqID, question, specPath, planPath string) {
	t.Helper()
	ctx := context.Background()
	specPath, planPath = filepath.Join(t.TempDir(), "spec.md"), filepath.Join(t.TempDir(), "plan.md")
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_at)
		VALUES ('art_spec1', 'itm_1', 'spec', ?, 1, 1)`, specPath); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO artifacts (id, item_id, kind, path, head_revision, created_at)
		VALUES ('art_plan1', 'itm_1', 'plan', ?, 1, 2)`, planPath); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO artifact_revisions
		(artifact_id, revision, sha256, content, sections_json, warnings_json, created_at)
		VALUES ('art_plan1', 1, 'x', 'x', '[]', '[]', 2)`); err != nil {
		t.Fatal(err)
	}
	reqID = "req_sumplan1"
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, is_hitl, agent_id, session_id, item_id, artifact_id, artifact_revision, prompt, options_json, state, created_at)
		VALUES (?, 'approve_plan', 0, 'agt_1', 'ses_1', 'itm_1', 'art_plan1', 1, ?, '[]', 'open', 3)`, reqID, summary); err != nil {
		t.Fatal(err)
	}
	question = summary + "\n\n" + "Spec: " + specPath + "\nPlan: " + planPath + "\n" + `Approve the plan (rev 1)?`
	return reqID, question, specPath, planPath
}

func TestPreToolUseAllowsAnApprovalQuestionWithoutAGate(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	_, question := seedApproval(t, h, "Users table gets id, email, and hashed_password columns.")
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, askInput(t, question, writeTranscript(t, claudeUserLine("go"))))
	if err != nil || len(out) != 0 {
		t.Fatalf("out=%s err=%v, want allowed", out, err)
	}
	var n int
	h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question'
		AND json_extract(binding_json, '$.ref') = 'req_sumgate1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("bound question rows = %d, want 1", n)
	}
}

func printingApproval(t *testing.T, h *Handler, summary string) (reqID, block string) {
	t.Helper()
	reqID, _ = seedApproval(t, h, summary)
	h.DB.Exec(`UPDATE requests SET binding_json = json_object('print_phase', 'print', 'print_attempts', 0, 'print_at', 1) WHERE id = ?`, reqID)
	return reqID, runtime.ApprovalChatBlock(runtime.ChatBlockInput{Kind: string(runtime.KindApproveSection), Revision: 1, Summary: summary})
}

func stopInput(t *testing.T, transcript string) []byte {
	b, _ := json.Marshal(map[string]any{"session_id": "p1", "transcript_path": transcript})
	return b
}

func relayEvent(t *testing.T, h *Handler, reqID string) string {
	t.Helper()
	var ev string
	h.DB.QueryRowContext(context.Background(), `SELECT COALESCE(json_extract(payload_json, '$.event'), '') FROM messages
		WHERE request_id = ? ORDER BY seq DESC LIMIT 1`, reqID).Scan(&ev)
	return ev
}

func TestStopAfterPrintedBlockSendsAskAndBlocks(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	reqID, block := printingApproval(t, h, "Users table gets id and email.")
	tr := writeTranscript(t, claudeUserLine("go"), claudeAssistantTextLine("m1", "**"+block+"**"))
	out, err := h.Handle(ctx, runtime.Claude, "Stop", ses, stopInput(t, tr))
	if err != nil || !strings.Contains(string(out), `"decision":"block"`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	var stopBlocks int
	h.DB.QueryRowContext(ctx, `SELECT stop_blocks FROM sessions WHERE id = ?`, ses).Scan(&stopBlocks)
	if ev := relayEvent(t, h, reqID); ev != "request_ask" || stopBlocks != 0 {
		t.Fatalf("event=%q stop_blocks=%d", ev, stopBlocks)
	}
}

// Claude Code fires Stop before the final assistant text reaches the transcript, so the reply is
// judged from the Stop input's last_assistant_message when it is there.
func TestStopJudgesLastAssistantMessageOverStaleTranscript(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	reqID, block := printingApproval(t, h, "Users table gets id and email.")
	tr := writeTranscript(t, claudeUserLine("go"))
	in, _ := json.Marshal(map[string]any{"session_id": "p1", "transcript_path": tr, "last_assistant_message": block})
	if _, err := h.Handle(ctx, runtime.Claude, "Stop", ses, in); err != nil {
		t.Fatal(err)
	}
	if ev := relayEvent(t, h, reqID); ev != "request_ask" {
		t.Fatalf("event = %q, want request_ask", ev)
	}
}

func TestStopAfterParaphraseSendsReprint(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	reqID, _ := printingApproval(t, h, "Users table gets id and email.")
	tr := writeTranscript(t, claudeUserLine("go"), claudeAssistantTextLine("m1", "Approving the users table."))
	if _, err := h.Handle(ctx, runtime.Claude, "Stop", ses, stopInput(t, tr)); err != nil {
		t.Fatal(err)
	}
	if ev := relayEvent(t, h, reqID); ev != "request_print" {
		t.Fatalf("event = %q", ev)
	}
}

func TestStopWithUnreadableTranscriptSendsAsk(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	reqID, _ := printingApproval(t, h, "Users table gets id and email.")
	if _, err := h.Handle(ctx, runtime.Claude, "Stop", ses, stopInput(t, "/does/not/exist.jsonl")); err != nil {
		t.Fatal(err)
	}
	if ev := relayEvent(t, h, reqID); ev != "request_ask" {
		t.Fatalf("event = %q, want request_ask (fail open)", ev)
	}
}

func TestStopPlanBlockNeedsReviewPaths(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	summary := "Ship auth end to end.\n\n- Login\n- Logout"
	reqID, _, specPath, planPath := seedPlanApproval(t, h, summary)
	h.DB.Exec(`UPDATE requests SET binding_json = json_object('print_phase', 'print', 'print_attempts', 0, 'print_at', 1) WHERE id = ?`, reqID)
	block := runtime.ApprovalChatBlock(runtime.ChatBlockInput{Kind: string(runtime.KindApprovePlan), Revision: 1, Summary: summary,
		Paths: &runtime.ReviewPaths{Spec: specPath, Plan: planPath}})

	noPaths := writeTranscript(t, claudeUserLine("go"), claudeAssistantTextLine("m1", summary))
	if _, err := h.Handle(ctx, runtime.Claude, "Stop", ses, stopInput(t, noPaths)); err != nil {
		t.Fatal(err)
	}
	if ev := relayEvent(t, h, reqID); ev != "request_print" {
		t.Fatalf("summary without the review paths: event = %q, want request_print", ev)
	}
	h.DB.Exec(`UPDATE messages SET state = 'delivered' WHERE state = 'pending'`)
	wrapped := writeTranscript(t, claudeUserLine("go"), claudeAssistantTextLine("m2",
		strings.ReplaceAll(block, "Spec: "+specPath, "**Spec:** `"+specPath+"`")))
	if _, err := h.Handle(ctx, runtime.Claude, "Stop", ses, stopInput(t, wrapped)); err != nil {
		t.Fatal(err)
	}
	if ev := relayEvent(t, h, reqID); ev != "request_ask" {
		t.Fatalf("markdown-wrapped block: event = %q, want request_ask", ev)
	}
}

func TestStopCursorIsTrusted(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	h.DB.Exec(`UPDATE agents SET kind = 'cursor' WHERE id = 'agt_1'`)
	printingApproval(t, h, "Users table gets id and email.")
	out, err := h.Handle(ctx, runtime.Cursor, "stop", ses, []byte(`{"conversation_id":"c1"}`))
	if err != nil || !strings.Contains(string(out), "followup_message") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

// TestExtractQuestionHeader covers the shapes confirmed live: a header per
// question (Claude/Codex/agy/Muse) and no header at all.
func TestExtractQuestionHeader(t *testing.T) {
	got := extractQuestionHeader("AskUserQuestion", []byte(`{"questions":[{"header":"Section 1","question":"Approve?"}]}`))
	if got != "Section 1" {
		t.Fatalf("header = %q, want %q", got, "Section 1")
	}
	if got := extractQuestionHeader("AskUserQuestion", []byte(`{"question":"Approve?"}`)); got != "" {
		t.Fatalf("header = %q, want empty", got)
	}
	if got := extractQuestionHeader("AskUserQuestion", nil); got != "" {
		t.Fatalf("header = %q, want empty for nil input", got)
	}
}

// seedGraphifyWorktree wires a worktree service into the handler and inserts
// one active worktree row owned by agt_1 at path.
func seedGraphifyWorktree(t *testing.T, h *Handler, id, path string) {
	t.Helper()
	h.RT.Worktree = &worktree.Service{DB: h.DB, Log: func(string, ...any) {}}
	if _, err := h.DB.ExecContext(context.Background(), `
		INSERT INTO repos (id, path, name, source, created_at, updated_at)
		VALUES ('repo_1', '/tmp/proj', 'proj', 'scan', 1, 1)
		ON CONFLICT(id) DO NOTHING;`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(context.Background(), `
		INSERT INTO worktrees (id, repo_id, path, branch, base_ref, base_sha,
			owner_agent_id, root_item_id, state, created_at)
		VALUES (?, 'repo_1', ?, 'task/x', 'main', 'abc', 'agt_1', 'itm_1', 'active', 1);`,
		id, path); err != nil {
		t.Fatal(err)
	}
}

func writeGraphJSON(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "graphify-out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graphify-out", "graph.json"),
		[]byte(`{"built_at_commit":"abc"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A worktree with a built graph appends the ready hint after the inbox notice.
func TestSessionStartAppendsTheGraphifyHintAfterTheInboxNotice(t *testing.T) {
	h, ses := seed(t, 1, runtime.Running)
	dir := t.TempDir()
	seedGraphifyWorktree(t, h, "wt_1", dir)
	writeGraphJSON(t, dir)
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := contextOf(t, out)
	inbox := strings.Index(got, "swarm_sync")
	hint := strings.Index(got, "has a code graph")
	if inbox < 0 {
		t.Fatalf("context = %q, want the inbox notice", got)
	}
	if hint < 0 || hint < inbox {
		t.Fatalf("context = %q, want the graphify hint after the inbox notice", got)
	}
}

// No worktrees means no graphify text.
func TestSessionStartOmitsTheGraphifyHintWithoutWorktrees(t *testing.T) {
	h, ses := seed(t, 1, runtime.Running)
	h.RT.Worktree = &worktree.Service{DB: h.DB, Log: func(string, ...any) {}}
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, out); strings.Contains(got, "graphify") {
		t.Fatalf("context = %q, want no graphify text without worktrees", got)
	}
}

// An active worktree without a graph gets the build hint.
func TestSessionStartHintsAWorktreeWithoutAGraph(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	dir := t.TempDir()
	seedGraphifyWorktree(t, h, "wt_1", dir)
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := contextOf(t, out)
	if !strings.Contains(got, "has no code graph yet") ||
		!strings.Contains(got, "cd "+dir+" && graphify extract . --code-only") {
		t.Fatalf("context = %q, want the no-graph build hint for %s", got, dir)
	}
}

// A ForAgent failure logs and leaves the rest of the context unchanged.
func TestSessionStartSkipsTheGraphifyHintWhenForAgentFails(t *testing.T) {
	h, ses := seed(t, 1, runtime.Running)
	var logs []string
	h.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	h.RT.Worktree = &worktree.Service{DB: h.DB, Log: func(string, ...any) {}}
	// Drop the worktrees table, not the DB: the earlier session lookup still
	// works, but ForAgent fails.
	if _, err := h.DB.ExecContext(context.Background(), `DROP TABLE worktrees`); err != nil {
		t.Fatal(err)
	}
	out, err := h.Handle(context.Background(), runtime.Claude, "SessionStart", ses,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := contextOf(t, out)
	h2, ses2 := seed(t, 1, runtime.Running)
	out2, err := h2.Handle(context.Background(), runtime.Claude, "SessionStart", ses2,
		[]byte(`{"session_id":"p1","source":"startup"}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := contextOf(t, out2); got != want {
		t.Fatalf("context = %q, want the no-hint context %q", got, want)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, "hook: graphify hint for") {
			found = true
		}
	}
	if !found {
		t.Fatalf("logs = %q, want a hook: graphify hint for line", logs)
	}
}

// The hint is SessionStart-only: UserPromptSubmit never carries it.
func TestUserPromptSubmitOmitsTheGraphifyHint(t *testing.T) {
	h, ses := seed(t, 1, runtime.Running)
	dir := t.TempDir()
	seedGraphifyWorktree(t, h, "wt_1", dir)
	writeGraphJSON(t, dir)
	out, err := h.Handle(context.Background(), runtime.Claude, "UserPromptSubmit", ses,
		[]byte(`{"session_id":"p1","prompt":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := contextOf(t, out)
	if got == "" {
		t.Fatal("want the inbox notice on UserPromptSubmit, got nothing")
	}
	if strings.Contains(got, "graphify") {
		t.Fatalf("context = %q, want no graphify text on UserPromptSubmit", got)
	}
}

func sessionSample(t *testing.T, h *Handler) (tokens, window sql.NullInt64) {
	t.Helper()
	if err := h.DB.QueryRowContext(context.Background(),
		`SELECT context_tokens, context_window FROM sessions WHERE id = 'ses_1'`).Scan(&tokens, &window); err != nil {
		t.Fatal(err)
	}
	return
}

func TestStopHookRecordsSample(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	path, err := filepath.Abs("../adapter/testdata/context/claude.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"session_id": "p1", "transcript_path": path})
	if _, err := h.Handle(context.Background(), runtime.Claude, "Stop", ses, in); err != nil {
		t.Fatal(err)
	}
	// claude-sonnet-5 is a 1M-window model family
	if tok, win := sessionSample(t, h); tok.Int64 != 93123 || win.Int64 != 1000000 {
		t.Fatalf("row = %v/%v, want 93123/1000000", tok, win)
	}
}

func TestPreCompactRecordsSample(t *testing.T) {
	h, ses := seed(t, 0, runtime.Running)
	ctx := context.Background()
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'cursor' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../adapter/testdata/context/cursor-precompact.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(ctx, runtime.Cursor, "preCompact", ses, raw); err != nil {
		t.Fatal(err)
	}
	if tok, win := sessionSample(t, h); tok.Int64 != 180000 || win.Int64 != 200000 {
		t.Fatalf("preCompact row = %v/%v, want 180000/200000", tok, win)
	}
	// a later byte-proxy Stop sample replaces the tokens and keeps the window
	path, _ := filepath.Abs("../adapter/testdata/context/cursor-transcript.jsonl")
	in, _ := json.Marshal(map[string]string{"conversation_id": "c1", "transcript_path": path})
	if _, err := h.Handle(ctx, runtime.Cursor, "stop", ses, in); err != nil {
		t.Fatal(err)
	}
	if tok, win := sessionSample(t, h); tok.Int64 != 100000 || win.Int64 != 200000 {
		t.Fatalf("stop row = %v/%v, want 100000/200000", tok, win)
	}
}

// A blocked Stop is not the end of the turn: the agent keeps going and Stop
// fires again, so only the allowed Stop samples (one strike per real turn).
func TestBlockedStopRecordsNoSample(t *testing.T) {
	h, ses := seed(t, 1, runtime.Running) // pending message -> Stop is blocked
	path, err := filepath.Abs("../adapter/testdata/context/claude.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"session_id": "p1", "transcript_path": path})
	out, err := h.Handle(context.Background(), runtime.Claude, "Stop", ses, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "block") {
		t.Fatalf("setup: Stop should be blocked, got %s", out)
	}
	if tok, _ := sessionSample(t, h); tok.Valid {
		t.Fatalf("blocked Stop recorded a sample: %v", tok)
	}
}

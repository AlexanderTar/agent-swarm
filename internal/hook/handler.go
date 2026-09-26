package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

var claudeCmdRe = regexp.MustCompile(`(?m)(?:^|[;&|()` + "`" + `]|\$\()\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*(?:(?:exec|nohup|sudo|env)\s+)*(?:[\w./]*\/)?claude(\s|[;&|)` + "`" + `]|$)`)

func isClaudeCommand(cmd string) bool {
	return claudeCmdRe.MatchString(cmd)
}

// capPrompt is the 1000-rune cap every hook-recorded question prompt gets,
// shared by extractQuestion and the codex question-reply binder so both
// compute the same prompt text.
func capPrompt(p string) string {
	if utf8.RuneCountInString(p) > 1000 {
		return string([]rune(p)[:997]) + "..."
	}
	return p
}

func extractQuestion(toolName string, raw []byte) (string, []string) {
	if len(raw) == 0 {
		return fmt.Sprintf("%s called", toolName), nil
	}

	var payload struct {
		Question  string `json:"question"`
		Prompt    string `json:"prompt"`
		Message   string `json:"message"`
		Options   []any  `json:"options"`
		Choices   []any  `json:"choices"`
		Questions []struct {
			Question string `json:"question"`
			Title    string `json:"title"` // codex 0.157 request_user_input_async
			Options  []any  `json:"options"`
		} `json:"questions"`
	}

	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Sprintf("%s called", toolName), nil
	}

	var prompt string
	var rawOptions []any

	if len(payload.Questions) > 0 {
		prompt = payload.Questions[0].Question
		if prompt == "" {
			prompt = payload.Questions[0].Title
		}
		rawOptions = payload.Questions[0].Options
	} else {
		if payload.Question != "" {
			prompt = payload.Question
		} else if payload.Prompt != "" {
			prompt = payload.Prompt
		} else if payload.Message != "" {
			prompt = payload.Message
		}
		if len(payload.Options) > 0 {
			rawOptions = payload.Options
		} else if len(payload.Choices) > 0 {
			rawOptions = payload.Choices
		}
	}

	if prompt == "" {
		prompt = fmt.Sprintf("%s called", toolName)
	}

	prompt = capPrompt(prompt)

	var options []string
	for _, opt := range rawOptions {
		switch v := opt.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				options = append(options, v)
			}
		case map[string]any:
			if label, ok := v["label"].(string); ok && label != "" {
				options = append(options, label)
			} else if text, ok := v["text"].(string); ok && text != "" {
				options = append(options, text)
			}
		}
	}

	return prompt, options
}

// questionsHaveBatchedSwarmRef reports whether a native question tool's raw
// input is a multi-question batch where at least one question carries a
// daemon-issued ⟦swarm:ref⟧ token (native.go's refToken). extractQuestion
// only ever reads Questions[0], so any ref past the first would bind to
// nothing -- see the PreToolUse guard that calls this.
func questionsHaveBatchedSwarmRef(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var payload struct {
		Questions []struct {
			Question string `json:"question"`
			Title    string `json:"title"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Questions) < 2 {
		return false
	}
	for _, q := range payload.Questions {
		if runtime.HasRefToken(q.Question) || runtime.HasRefToken(q.Title) {
			return true
		}
	}
	return false
}

// extractToolResponseText reads the answer text out of a question tool's
// PostToolUse tool_response. A real claude AskUserQuestion result has the
// shape {questions, answers:{<question text>: <chosen label(s)>}, annotations}
// (confirmed from toolUseResult in a local ~/.claude transcript, never
// answer/response/text/output/result), so prompt -- the same question text
// extractQuestion computed for this call -- is used to pick answers' own
// entry; a prompt that matches no key (e.g. a multi-question tool call, or a
// truncated prompt) falls back to answers' first value rather than losing
// the answer to the generic-key branch below.
func extractToolResponseText(raw []byte, prompt string) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return strings.TrimSpace(str)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if answers, ok := obj["answers"].(map[string]any); ok {
			if v, ok := answers[prompt]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
			for _, v := range answers {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
		for _, key := range []string{"answer", "response", "text", "output", "result"} {
			if v, ok := obj[key]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
		if content, ok := obj["content"]; ok {
			if s, ok := content.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
			if list, ok := content.([]any); ok {
				var parts []string
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						if t, ok := m["text"].(string); ok && strings.TrimSpace(t) != "" {
							parts = append(parts, strings.TrimSpace(t))
						}
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, "\n")
				}
			}
		}
	}
	return ""
}

const noticeGap = 60 * time.Second
const maxStopBlocks = 3

// AdvisorScanner is the transcript scanner interface for native advisors.
type AdvisorScanner interface {
	ScanTranscript(ctx context.Context, sessionID, transcriptPath string) error
}

// normalize maps each agent's event name to the shared name used below.
func normalize(kind runtime.AgentKind, event string) string {
	switch strings.ToLower(event) {
	case "sessionstart":
		return "SessionStart"
	case "userpromptsubmit", "beforesubmitprompt", "preinvocation":
		return "UserPromptSubmit"
	case "pretooluse":
		return "PreToolUse"
	case "posttooluse", "postinvocation":
		return "PostToolUse"
	case "permissionrequest":
		return "PermissionRequest"
	case "precompact":
		return "PreCompact"
	case "postcompact":
		return "PostCompact"
	case "stop":
		return "Stop"
	}
	return event
}

// isQuestionTool is the one list of native "ask the human" tools that a
// hook can intercept. Per-adapter status, live-probed 2026-09-25/26
// (spec section 1.7, docs/plans/2026-09-25-needs-you-and-child-approval-routing.md
// Tasks 4 and 4b):
//
//	claude AskUserQuestion: PreToolUse deny live-tested (handler_test.go:868 and around it).
//	agy ask_question: confirmed live. PreToolUse fires before the dialog renders (a deny
//	  suppresses it entirely); PostToolUse carries no result field, so agy stays
//	  agent_reported (spec 2.3.5). Fixtures: testdata/agy-hook-{pre,post}tooluse-ask_question.json.
//	codex request_user_input_async: confirmed live 2026-09-26 (codex 0.157; fixtures
//	  testdata/codex/native-question). Async: PostToolUse carries only {"accepted":true};
//	  the answer arrives on the next UserPromptSubmit as a <send_user_message_question_reply>
//	  message (parseQuestionReply). request_user_input / experimental_request_user_input
//	  are kept for older builds.
//	muse request_user_input: confirmed live to dispatch NO hook at all, ever -- not
//	  "fires but can't deny" but no event to intercept in the first place, while the same
//	  plugin's hooks fired correctly for muse's other tool calls in the same turn
//	  (testdata/muse-hook-{pre,post}tooluse.json capture that sibling firing, not
//	  request_user_input). muse is therefore NOT in this list: it joins cursor's
//	  exception (swarm_ask kind:"question" stays available) rather than being refused.
//	cursor AskQuestion: confirmed (Cursor staff, forum bug 161836) never to fire
//	  PreToolUse/PostToolUse at all. Also not in this list, same exception as muse.
//
// Where a block is honored, a parented agent's question is relayed instead
// (nativeQuestionRelay). Where the hook is absent or unconfirmed (cursor,
// muse), a parented agent still has no other way to reach the user;
// swarm_ask stays available and Task 9 does not refuse it for these kinds.
func isQuestionTool(name string) bool {
	switch name {
	case "ask_question", "AskUserQuestion", "request_user_input", "experimental_request_user_input", asyncQuestionTool, "AskQuestion":
		return true
	}
	return false
}

// asyncQuestionTool is codex 0.157's native question tool. It returns
// {"accepted":true} at once; the user's answer arrives later as a
// <send_user_message_question_reply> UserPromptSubmit (parseQuestionReply).
const asyncQuestionTool = "request_user_input_async"

// nativeQuestionRelay is the PreToolUse block reason for a parented agent.
const nativeQuestionRelay = "[swarm] You report to an orchestrator, not the user. Send this question to it with swarm_send (to: \"parent\", kind: \"question\") instead of a question tool."

type sessionRow struct {
	ID, AgentID, AgentName, ItemKey, ProviderID string
	State                                       runtime.SessionState
	StopBlocks                                  int
	NeedsCompaction                             bool
	LastNoticeAt                                *time.Time
	Pending                                     int
	HasHandoff                                  bool
	Handoff                                     bool // a handoff operation is in flight (HANDOFF notice, not PAUSE)
	Kind                                        runtime.AgentKind
	ParentAgentID                               string
}

type Handler struct {
	DB       *db.DB
	RT       *runtime.Store
	Adapters map[runtime.AgentKind]adapter.Adapter
	Advisor  AdvisorScanner
	Now      func() time.Time
	Log      func(string, ...any)
	// WorktreesDir is ~/.swarm/worktrees (install.Config.Worktrees()); empty
	// disables the worktree-mutation guard below, so every existing Handler
	// literal in tests keeps compiling and keeps its current behaviour.
	WorktreesDir string
	mu           sync.Mutex
	noticeAt     map[string]time.Time
	readFile     func(string) ([]byte, error) // nil means os.ReadFile
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) logf(format string, args ...any) {
	if h.Log != nil {
		h.Log(format, args...)
	}
}

func (h *Handler) load(ctx context.Context, sessionID string) (*sessionRow, error) {
	var s sessionRow
	var needsCompaction int
	var hasHandoff, handoffOp int
	err := h.DB.QueryRowContext(ctx, `
		SELECT
			s.id,
			s.agent_id,
			a.name,
			i.key,
			COALESCE(s.provider_session_id, ''),
			s.state,
			s.stop_blocks,
			s.needs_compaction_notice,
			a.kind,
			COALESCE(a.parent_agent_id, ''),
			(SELECT COUNT(*) FROM messages WHERE to_agent_id = s.agent_id AND state = 'pending'),
			EXISTS (SELECT 1 FROM checkpoints WHERE session_id = s.id AND kind = 'handoff'),
			EXISTS (SELECT 1 FROM agent_operations o WHERE o.agent_id = s.agent_id AND o.mode = 'handoff'
				AND o.phase IN ('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting'))
		FROM sessions s
		JOIN agents a ON s.agent_id = a.id
		JOIN items i ON a.item_id = i.id
		WHERE s.id = ?`, sessionID).Scan(
		&s.ID,
		&s.AgentID,
		&s.AgentName,
		&s.ItemKey,
		&s.ProviderID,
		&s.State,
		&s.StopBlocks,
		&needsCompaction,
		&s.Kind,
		&s.ParentAgentID,
		&s.Pending,
		&hasHandoff,
		&handoffOp,
	)
	if err != nil {
		return nil, err
	}
	s.NeedsCompaction = (needsCompaction != 0)
	s.HasHandoff = (hasHandoff != 0)
	s.Handoff = (handoffOp != 0)

	h.mu.Lock()
	if t, ok := h.noticeAt[s.ID]; ok {
		s.LastNoticeAt = &t
	}
	h.mu.Unlock()

	return &s, nil
}

func (h *Handler) touch(ctx context.Context, s *sessionRow, in adapter.HookInput) error {
	nowMs := h.now().UnixMilli()
	if in.ProviderSessionID != "" {
		if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET provider_session_id = ? WHERE id = ? AND (provider_session_id IS NULL OR provider_session_id = '')`, in.ProviderSessionID, s.ID); err != nil {
			return err
		}
	}
	_, err := h.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, nowMs, s.ID)
	return err
}

// Handle runs the §11.2 decision table and returns the agent-specific output
// bytes (empty means "print nothing"). It never errors on an unknown session.
// Handle logs one line per call, unconditionally, timing the whole round trip:
// before this, a hook that never arrived and a hook that silently failed left
// the exact same trace in daemon.err.log (none), so a sustained delivery gap
// -- an agent going many tool calls with pending mail nobody saw fire --
// couldn't be told apart from the agent simply not calling anything hook-
// worthy. This line is the only place that distinction gets recorded.
func (h *Handler) Handle(ctx context.Context, kind runtime.AgentKind, event, sessionID string, stdin []byte) (out []byte, err error) {
	start := h.now()
	agentName := "?"
	defer func() {
		outcome := "no-op"
		switch {
		case err != nil:
			outcome = "error: " + err.Error()
		case len(out) > 0:
			outcome = fmt.Sprintf("decision (%d bytes)", len(out))
		}
		h.logf("hook: %s %s for %s took %s -> %s", kind, event, agentName, h.now().Sub(start).Round(time.Millisecond), outcome)
	}()
	// L16: identity comes from the token. The {agent} path segment is a hint only;
	// the adapter is the one the session's agent row names, and a mismatch is
	// logged and ignored rather than trusted.
	s0, err := h.load(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	agentName = s0.AgentName
	if s0.Kind != kind {
		h.logf("hook: %s posted to /hook/%s; using the session's kind", s0.Kind, kind)
	}
	a, ok := h.Adapters[s0.Kind]
	if !ok {
		return nil, nil
	}
	ev := normalize(s0.Kind, event)
	in, err := a.ParseHook(event, stdin)
	if err != nil {
		h.logf("hook: %s %s does not parse: %v", s0.Kind, event, err)
		in = adapter.HookInput{}
	}
	if err := h.touch(ctx, s0, in); err != nil {
		return nil, err
	}
	s, err := h.load(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// native advisor accounting reads the transcript on PostToolUse and Stop (§11.6)
	if (ev == "PostToolUse" || ev == "Stop") && in.TranscriptPath != "" && h.Advisor != nil {
		if err := h.Advisor.ScanTranscript(ctx, s.ID, in.TranscriptPath); err != nil {
			h.logf("advisor: transcript scan for %s: %v", s.ID, err)
		}
	}
	d, err := h.decide(ctx, kind, a, s, ev, in)
	if err != nil {
		return nil, err
	}
	if d.Context == "" && !d.Block {
		return nil, nil
	}
	return a.HookOutput(event, d)
}

// inboxNoticeOrFallback renders the rich inbox notice, falling back to the
// old terse PendingNotice if h.RT is nil (some handler unit tests construct
// a Handler without a Store) or the render errs — a notice render failure
// must never block a hook response.
func (h *Handler) inboxNoticeOrFallback(ctx context.Context, s *sessionRow) string {
	if h.RT == nil {
		return runtime.PendingNotice(s.Pending, s.AgentName, s.ItemKey)
	}
	notice, err := h.RT.InboxNotice(ctx, s.AgentID, s.AgentName, s.ItemKey)
	if err != nil {
		h.logf("hook: inbox notice for %s: %v", s.ID, err)
		return runtime.PendingNotice(s.Pending, s.AgentName, s.ItemKey)
	}
	return notice
}

func (h *Handler) decide(ctx context.Context, kind runtime.AgentKind, a adapter.Adapter, s *sessionRow, ev string, in adapter.HookInput) (adapter.HookDecision, error) {
	switch ev {
	case "SessionStart":
		if in.Source == "fork" {
			return adapter.HookDecision{
				Block:  true,
				Reason: "[swarm] Forked sessions are disabled. Work must run within the assigned Swarm session.",
			}, nil
		}

		var parts []string
		if in.Source == "compact" || s.NeedsCompaction {
			parts = append(parts, runtime.CompactionNotice())
			if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET needs_compaction_notice = 0 WHERE id = ?`, s.ID); err != nil {
				return adapter.HookDecision{}, err
			}
		}
		if s.Pending > 0 {
			parts = append(parts, h.inboxNoticeOrFallback(ctx, s))
		}
		return adapter.HookDecision{Context: strings.Join(parts, " ")}, nil

	case "PreCompact":
		if kind == runtime.Codex || kind == runtime.Cursor || s.Kind == runtime.Codex || s.Kind == runtime.Cursor {
			if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET needs_compaction_notice = 1 WHERE id = ?`, s.ID); err != nil {
				return adapter.HookDecision{}, err
			}
		}
		return adapter.HookDecision{
			Context: "Write a `progress` checkpoint with your current state before context is compacted.",
		}, nil

	case "PostCompact":
		return adapter.HookDecision{}, nil

	case "UserPromptSubmit":
		if in.Prompt != "" && !runtime.IsDaemonPrompt(in.Prompt) && h.RT != nil && s.AgentID != "" {
			if err := h.RT.ResolveAnsweredInTerminal(ctx, s.AgentID); err != nil {
				h.logf("hook: close rows after human prompt for %s: %v", s.ID, err)
			}
		}
		var parts []string
		if s.NeedsCompaction {
			parts = append(parts, runtime.CompactionNotice())
			if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET needs_compaction_notice = 0 WHERE id = ?`, s.ID); err != nil {
				return adapter.HookDecision{}, err
			}
		}
		if s.Pending > 0 && !runtime.IsDaemonPrompt(in.Prompt) {
			parts = append(parts, h.inboxNoticeOrFallback(ctx, s))
		}
		return adapter.HookDecision{Context: strings.Join(parts, " ")}, nil

	case "PreToolUse":
		// Preservation mode (spec §3): a pausing predecessor may still use
		// the save path -- read/edit/shell/wait/commit under its existing
		// permissions -- while delegation, new workflow steps and
		// push/deploy are denied. Swarm MCP tools keep their own daemon
		// gate (PauseAllowed); completed checkpoints are refused by
		// WriteCheckpoint's kind gate.
		if s.State.Pausing() && !in.IsSwarmTool {
			if err := runtime.PreservationNativeAllowed(in.ToolName); err != nil {
				return adapter.HookDecision{Block: true, Reason: err.Error()}, nil
			}
			if in.Command != "" {
				if err := runtime.PreservationCommandAllowed(in.Command); err != nil {
					return adapter.HookDecision{Block: true, Reason: err.Error()}, nil
				}
			}
		}

		// A6: the native Workflow tool is disabled in Swarm sessions -- use
		// swarm_spawn (single step) or swarm_workflow (the multi-step engine).
		if in.ToolName == "Workflow" || in.ToolName == "workflow" {
			return adapter.HookDecision{
				Block:  true,
				Reason: "[swarm] The Workflow tool is disabled in Swarm sessions. Use swarm_spawn or swarm_workflow.",
			}, nil
		}

		isNativeFork := in.ToolName == "Agent" ||
			in.ToolName == "Task" ||
			in.ToolName == "Fork" ||
			in.ToolName == "fork" ||
			in.ToolName == "invoke_subagent" ||
			in.ToolName == "subagent" ||
			in.ToolName == "dispatch_agent" ||
			in.ToolName == "spawn_agent"

		if isNativeFork {
			return adapter.HookDecision{
				Block:  true,
				Reason: "[swarm] Native forks and subagents are disabled. Use swarm_spawn to delegate work to Swarm-managed agents, or execute tasks sequentially in this session.",
			}, nil
		}

		if in.Command != "" {
			if isClaudeCommand(in.Command) {
				return adapter.HookDecision{
					Block:  true,
					Reason: "[swarm] Nested agent invocations via shell are disabled. Use swarm_spawn to delegate work.",
				}, nil
			}
			if blocksWorktreeMutation(in.Command, h.WorktreesDir) {
				reason := worktreeGuardOrchestrator
				if s.ParentAgentID != "" {
					reason = worktreeGuardWorker
				}
				return adapter.HookDecision{Block: true, Reason: reason}, nil
			}
			if blocked, reason := (AttrCheck{ReadFile: h.readFile}).Block(in.Command, in.Cwd); blocked {
				return adapter.HookDecision{
					Block:  true,
					Reason: reason,
				}, nil
			}
		}

		// A parented agent reports to its orchestrator, never to the user: block the
		// native question tool. Top-level agents fall through to the intercept below.
		if isQuestionTool(in.ToolName) && s.ParentAgentID != "" {
			return adapter.HookDecision{Block: true, Reason: nativeQuestionRelay}, nil
		}

		// A batched AskUserQuestion call binds only Questions[0] (the intercept
		// below), so a swarm-issued ref anywhere past the first question would
		// be recorded and answered but never bindable -- native_answer could
		// never find it (2026-09-26 fix, native-railway-tracing finding).
		if isQuestionTool(in.ToolName) && questionsHaveBatchedSwarmRef(in.RawToolInput) {
			return adapter.HookDecision{Block: true, Reason: "[swarm] Ask one swarm approval per question call."}, nil
		}

		// For Swarm's own swarm_spawn tool: enforce max_concurrent_subagents
		isSpawn := (in.IsSwarmTool && strings.Contains(in.ToolName, "swarm_spawn")) || strings.Contains(in.ToolName, "swarm_spawn")

		if isSpawn && s.AgentID != "" && h.RT != nil && h.RT.Settings != nil {
			// A queued child holds its slot (matches Admit's own comment in
			// limits.go), but an 'active' child whose latest session died to
			// interrupted/crashed/failed does not (runtime.NotAZombieSlot,
			// 2026-09-22 zombie-slot fix) -- otherwise a forgotten dead child
			// pins the parent's subagent budget at capacity forever.
			// SubagentSlots (P9) owns this exact query, shared with the
			// engine's own budget check, so the two can never disagree.
			active, maxSubagents, err := h.RT.SubagentSlots(ctx, s.AgentID)
			if err != nil {
				return adapter.HookDecision{}, err
			}

			if active >= maxSubagents {
				reason := fmt.Sprintf("[swarm] Subagent budget exceeded (max %d active). Run sequentially or wait for active subagents to finish.", maxSubagents)
				// Surface (never auto-cancel, see runtime.NoAckChildren) any
				// slot-holding child that has run past the ack timeout with
				// zero checkpoints -- the daemon's only other signal for this
				// is agent.no_ack, which only ever mails the parent's inbox
				// asynchronously and was easy to miss in the live incident
				// this addresses.
				noAck, err := h.RT.NoAckChildren(ctx, s.AgentID)
				if err != nil {
					// informational only -- never let this suppress the budget block
					h.logf("hook: no-ack children lookup failed for %s: %v", s.AgentID, err)
				} else if len(noAck) > 0 {
					reason += fmt.Sprintf(" %d slot(s) among those show no checkpoint since its session started (past the ack timeout): %s. swarm_read them; swarm_control cancel if genuinely stuck.",
						len(noAck), strings.Join(noAck, ", "))
				}
				return adapter.HookDecision{
					Block:  true,
					Reason: reason,
				}, nil
			}
		}

		// Intercept native question tools to record HITL request in Swarm without blocking
		if isQuestionTool(in.ToolName) && h.RT != nil && s.ID != "" {
			prompt, options := extractQuestion(in.ToolName, in.RawToolInput)
			_, _ = h.RT.AskQuestion(ctx, s.ID, prompt, options)
		}

		return adapter.HookDecision{}, nil

	case "PermissionRequest":
		if h.RT != nil && s.ID != "" {
			prompt := in.Command
			if prompt == "" {
				prompt = "Permission requested"
			}
			_, _ = h.RT.AskPrompt(ctx, s.ID, prompt, nil)
		}
		return adapter.HookDecision{}, nil

	case "PostToolUse":
		var parts []string
		if isQuestionTool(in.ToolName) && h.RT != nil && s.ID != "" {
			prompt, _ := extractQuestion(in.ToolName, in.RawToolInput)
			answer := extractToolResponseText(in.ToolResponse, prompt)
			if answer == "" {
				answer = runtime.ResolvedInTerminal
			}
			req, err := h.RT.ResolveQuestionByPrompt(ctx, s.ID, prompt, answer)
			if err != nil {
				h.logf("hook: resolve question for %s: %v", s.ID, err)
			} else if next := runtime.NativeAnswerNextStep(req); next != "" {
				// Never rate-limited by canNotice below: a missed forward
				// leaves the user's approval stranded (native-railway-tracing
				// finding), unlike the informational notices canNotice guards.
				parts = append(parts, next)
			}
		}
		if h.RT != nil && s.ID != "" {
			if err := h.RT.ResolveSessionPrompts(ctx, s.ID, in.Command); err != nil {
				h.logf("hook: resolve prompts for %s: %v", s.ID, err)
			}
		}

		gaveNotice := false
		if s.NeedsCompaction {
			parts = append(parts, runtime.CompactionNotice())
			gaveNotice = true
			if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET needs_compaction_notice = 0 WHERE id = ?`, s.ID); err != nil {
				return adapter.HookDecision{}, err
			}
		}
		canNotice := s.LastNoticeAt == nil || h.now().Sub(*s.LastNoticeAt) >= noticeGap
		if s.Pending > 0 && canNotice {
			parts = append(parts, runtime.PendingNotice(s.Pending, s.AgentName, s.ItemKey))
			gaveNotice = true
		}
		if len(parts) == 0 {
			return adapter.HookDecision{}, nil
		}
		// Only a rate-limited notice (compaction/pending) stamps noticeAt --
		// the native-answer next step above is neither rate-limited nor a
		// reason to suppress a later pending-inbox notice.
		if gaveNotice {
			h.mu.Lock()
			if h.noticeAt == nil {
				h.noticeAt = make(map[string]time.Time)
			}
			h.noticeAt[s.ID] = h.now()
			h.mu.Unlock()
		}
		return adapter.HookDecision{Context: strings.Join(parts, " ")}, nil

	case "Stop":
		if s.State.Pausing() && !s.HasHandoff {
			reason := runtime.PausePreservationNotice(s.AgentName, s.ItemKey)
			if s.Handoff {
				reason = runtime.HandoffPreservationNotice(s.AgentName, s.ItemKey)
			}
			return adapter.HookDecision{Block: true, Reason: reason}, nil
		}
		if s.Pending > 0 && s.StopBlocks < maxStopBlocks {
			if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET stop_blocks = stop_blocks + 1 WHERE id = ?`, s.ID); err != nil {
				return adapter.HookDecision{}, err
			}
			return adapter.HookDecision{
				Block:  true,
				Reason: h.inboxNoticeOrFallback(ctx, s),
			}, nil
		}
		if s.StopBlocks > 0 {
			if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET stop_blocks = 0 WHERE id = ?`, s.ID); err != nil {
				return adapter.HookDecision{}, err
			}
		}
		return adapter.HookDecision{}, nil
	}

	return adapter.HookDecision{}, nil
}

package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
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

// extractQuestionHeader reads a native question tool call's own header
// field, when its shape carries one -- confirmed live for Claude, Codex
// (request_user_input's questions[].header) and Muse (same field); agy's
// ask_question also carries one per question (spec probe). "" when the raw
// input is empty, doesn't parse, or has no header at any level. Used only
// to disambiguate a child-approval (msg_) BindNativeQuestion match
// (2026-09-28-approval-summary-enforced, post-review): every other use of
// the observed question ignores it.
func extractQuestionHeader(toolName string, raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		Header    string `json:"header"`
		Questions []struct {
			Header string `json:"header"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if len(payload.Questions) > 0 && payload.Questions[0].Header != "" {
		return payload.Questions[0].Header
	}
	return payload.Header
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

// batchedQuestionsAfterFirst returns every question text in a multi-question
// batch's Questions[1:] (Questions[0] is the one extractQuestion/AskQuestion
// will actually bind, via the PreToolUse intercept below) -- "" for
// Question falls back to Title, same as extractQuestion. Returns nil when
// raw isn't a >=2-question batch.
func batchedQuestionsAfterFirst(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Questions []struct {
			Question string `json:"question"`
			Title    string `json:"title"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Questions) < 2 {
		return nil
	}
	out := make([]string, 0, len(payload.Questions)-1)
	for _, q := range payload.Questions[1:] {
		text := q.Question
		if text == "" {
			text = q.Title
		}
		out = append(out, text)
	}
	return out
}

// questionReply is one entry of codex's question-reply user message: the
// answer to a request_user_input_async question arrives on the next
// UserPromptSubmit as
// <send_user_message_question_reply>[{"answer":…,"question":…}]</send_user_message_question_reply>.
type questionReply struct {
	Question string // entry "question", or "title" when "question" is empty
	Answer   string // trimmed; "" when absent or not a JSON string
}

var questionReplyRe = regexp.MustCompile(`(?s)<send_user_message_question_reply>(.*?)</send_user_message_question_reply>`)

// parseQuestionReply extracts the entries of a codex question reply. ok is
// false when the prompt has no wrapper, the body is not a JSON array, or the
// array is empty -- the caller then treats the prompt as typed free text.
// ponytail: decodes only question/title/answer from a shape reported live but
// not yet captured in a fixture; recapture into testdata/codex/native-question
// if codex changes it.
func parseQuestionReply(prompt string) ([]questionReply, bool) {
	m := questionReplyRe.FindStringSubmatch(prompt)
	if m == nil {
		return nil, false
	}
	var raw []struct {
		Question string          `json:"question"`
		Title    string          `json:"title"`
		Answer   json.RawMessage `json:"answer"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &raw); err != nil || len(raw) == 0 {
		return nil, false
	}
	out := make([]questionReply, 0, len(raw))
	for _, r := range raw {
		q := r.Question
		if q == "" {
			q = r.Title
		}
		var a string
		_ = json.Unmarshal(r.Answer, &a) // a non-string answer stays ""
		out = append(out, questionReply{Question: q, Answer: strings.TrimSpace(a)})
	}
	return out, true
}

// codexAnswerText reads codex's synchronous request_user_input tool_response
// once it has been unwrapped from its outer JSON string: inner decodes to
// {"answers":{"<id>":{"answers":["<label>", "user_note: <note>"?]}}}
// (docs/specs/2026-09-28-codex-sync-request-user-input.md, live probe).
// Swarm asks one question per call, so only the first entry is read -- by
// sorted id, since Go map iteration order is random and a Codex batch would
// otherwise bind a different question's answer on every run. Label is the
// first answer element that isn't a "user_note: " remark, with any trailing
// " (Recommended)" the model appended stripped (case-insensitive); note is
// the text after "user_note: " in the first element that has it. ok is true
// once the "answers" key itself is present -- confirming the codex shape --
// even when the id's own answers array is empty or note-only (the caller
// then falls back to ResolvedInTerminal or the note alone rather than the
// generic-key branch below). ok is false only for a plain string
// tool_response or JSON with no "answers" key at all (not this shape).
func codexAnswerText(inner string) (string, bool) {
	var payload struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(inner), &payload); err != nil || payload.Answers == nil {
		return "", false
	}
	ids := make([]string, 0, len(payload.Answers))
	for id := range payload.Answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var entryAnswers []string
	if len(ids) > 0 {
		entryAnswers = payload.Answers[ids[0]].Answers
	}
	const notePrefix = "user_note:"
	var label, note string
	for _, a := range entryAnswers {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a)), notePrefix) {
			if note == "" {
				note = strings.TrimSpace(a[strings.Index(a, ":")+1:])
			}
			continue
		}
		if label == "" {
			label = a
		}
	}
	if label == "" {
		return note, true // note alone, or nothing answered yet -- either way, the shape is confirmed
	}
	const recommended = " (recommended)"
	if strings.HasSuffix(strings.ToLower(label), recommended) {
		label = label[:len(label)-len(recommended)]
	}
	if note != "" {
		return label + ": " + note, true
	}
	return label, true
}

// extractToolResponseText reads the answer text out of a question tool's
// PostToolUse tool_response. A real claude AskUserQuestion result has the
// shape {questions, answers:{<question text>: <chosen label(s)>}, annotations}
// (confirmed from toolUseResult in a local ~/.claude transcript, never
// answer/response/text/output/result), so prompt -- the same question text
// extractQuestion computed for this call -- is used to pick answers' own
// entry; a prompt that matches no key (e.g. a multi-question tool call, or a
// truncated prompt) falls back to answers' first value rather than losing
// the answer to the generic-key branch below. codex's synchronous
// request_user_input answers the same way claude does, but wraps its object
// in a JSON string (spec above) rather than sending the object directly, so
// the string branch tries codexAnswerText before returning the raw string.
func extractToolResponseText(raw []byte, prompt string) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if s, ok := codexAnswerText(str); ok {
			return s
		}
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

// transcriptTexter is implemented by the adapters with a Stop hook and a readable transcript
// (Claude, Codex, agy): AssistantTextSinceLastTurn reads the text the agent printed since the last
// user turn, so Stop can check the approval chat_block was actually printed. Declared here, not on
// adapter.Adapter, so every other adapter (Cursor, Muse, Fake) needs no stub -- a type assertion
// picks it up where it exists.
type transcriptTexter interface {
	AssistantTextSinceLastTurn(transcriptPath string) (text string, ok bool)
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
	deferred     map[string]string            // agy: PostToolUse context held for the next PreInvocation (in memory: lost on daemon restart)
	readFile     func(string) ([]byte, error) // nil means os.ReadFile
	obs          map[string]transcriptObs     // transcript path -> last ObserveModel answer
}

// transcriptObs is one ObserveModel answer, valid while the transcript's size
// and mtime are unchanged.
type transcriptObs struct {
	size          int64
	mtime         time.Time
	model, effort string
	ok            bool
}

// observeTranscriptModel is mo.ObserveModel, but a transcript whose size and
// mtime match the last call returns that answer without re-reading the tail.
func (h *Handler) observeTranscriptModel(mo adapter.ModelObserver, path, providerSessionID string) (string, string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return mo.ObserveModel(path, providerSessionID)
	}
	h.mu.Lock()
	memo, hit := h.obs[path]
	h.mu.Unlock()
	if hit && memo.size == info.Size() && memo.mtime.Equal(info.ModTime()) {
		return memo.model, memo.effort, memo.ok
	}
	memo = transcriptObs{size: info.Size(), mtime: info.ModTime()}
	memo.model, memo.effort, memo.ok = mo.ObserveModel(path, providerSessionID)
	h.mu.Lock()
	if h.obs == nil {
		h.obs = map[string]transcriptObs{}
	}
	h.obs[path] = memo
	h.mu.Unlock()
	return memo.model, memo.effort, memo.ok
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
	// a human's in-session /model or /effort shows up in the hook's model field
	if in.Model != "" && h.RT != nil {
		if _, err := h.RT.RecordObservedModel(ctx, s.ID, in.Model, in.Effort, "hook"); err != nil {
			h.logf("hook: record observed model for %s: %v", s.ID, err)
		}
	}
	// native advisor accounting reads the transcript on PostToolUse and Stop (§11.6)
	if (ev == "PostToolUse" || ev == "Stop") && in.TranscriptPath != "" && h.Advisor != nil {
		if err := h.Advisor.ScanTranscript(ctx, s.ID, in.TranscriptPath); err != nil {
			h.logf("advisor: transcript scan for %s: %v", s.ID, err)
		}
	}
	// every session records its context size at end of turn; cursor's preCompact
	// payload carries an exact reading, its Stop falls back to the byte proxy
	if h.RT != nil {
		switch {
		case ev == "Stop" && in.TranscriptPath != "":
			if err := h.RT.SampleTranscript(ctx, s.ID, s.Kind, in.TranscriptPath); err != nil {
				h.logf("hook: record context sample for %s: %v", s.ID, err)
			}
		case ev == "PreCompact" && in.ContextTokens > 0:
			var window *int
			if in.ContextWindow > 0 {
				window = &in.ContextWindow
			}
			if err := h.RT.RecordContextSample(ctx, s.ID, in.ContextTokens, window); err != nil {
				h.logf("hook: record context sample for %s: %v", s.ID, err)
			}
		}
	}
	// a model or effort change in the transcript tail (claude, codex)
	if (ev == "PostToolUse" || ev == "Stop") && in.TranscriptPath != "" && h.RT != nil {
		if mo, ok := a.(adapter.ModelObserver); ok {
			if model, effort, ok := h.observeTranscriptModel(mo, in.TranscriptPath, in.ProviderSessionID); ok {
				if _, err := h.RT.RecordObservedModel(ctx, s.ID, model, effort, "transcript"); err != nil {
					h.logf("hook: record transcript model for %s: %v", s.ID, err)
				}
			}
		}
	}
	d, err := h.decide(ctx, kind, a, s, ev, in)
	if err != nil {
		return nil, err
	}
	// AGY accepts injected context only on PreInvocation, but PostToolUse has
	// already consumed it (answered question, compaction flag). Hold it for
	// the next PreInvocation instead of dropping it.
	if s0.Kind == runtime.Agy && !d.Block {
		h.mu.Lock()
		if h.deferred == nil {
			h.deferred = make(map[string]string)
		}
		switch ev {
		case "PostToolUse":
			// PostInvocation also normalizes here but accepts injectSteps,
			// so its context goes out now; only raw PostToolUse is held.
			if d.Context != "" && strings.EqualFold(event, "PostToolUse") {
				h.deferred[s.ID] = strings.TrimSpace(h.deferred[s.ID] + " " + d.Context)
			}
		case "UserPromptSubmit":
			if held := h.deferred[s.ID]; held != "" {
				delete(h.deferred, s.ID)
				d.Context = strings.TrimSpace(held + " " + d.Context)
			}
		}
		h.mu.Unlock()
	}
	if d.Context == "" && !d.Block && len(d.UpdatedInput) == 0 {
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
	notice, err := h.RT.InboxNotice(ctx, s.ID, s.AgentID, s.AgentName, s.ItemKey)
	if err != nil {
		h.logf("hook: inbox notice for %s: %v", s.ID, err)
		return runtime.PendingNotice(s.Pending, s.AgentName, s.ItemKey)
	}
	h.RT.MarkNoticeSeen(s.ID) // hook context goes out with this response
	return notice
}

// graphifyHint is the SessionStart line for the agent's active worktrees.
// "" when there is nothing to say. A ForAgent failure is logged and the
// hint skipped, never blocking the hook.
func (h *Handler) graphifyHint(ctx context.Context, s *sessionRow) string {
	if h.RT == nil || h.RT.Worktree == nil || s.AgentID == "" {
		return ""
	}
	wts, err := h.RT.Worktree.ForAgent(ctx, s.AgentID)
	if err != nil {
		h.logf("hook: graphify hint for %s: %v", s.AgentID, err)
		return ""
	}
	var paths []string
	var ready []bool
	for _, wt := range wts {
		if wt.State != "active" {
			continue
		}
		paths = append(paths, wt.Path)
		_, statErr := os.Stat(filepath.Join(wt.Path, "graphify-out", "graph.json"))
		ready = append(ready, statErr == nil)
	}
	return runtime.GraphifyHint(paths, ready)
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
		if hint := h.graphifyHint(ctx, s); hint != "" {
			parts = append(parts, hint)
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
		var parts []string
		// A codex question reply names the rows it answers: bind exactly
		// those (by ref, else prompt) and never run the blanket close below.
		// Any other human prompt is typed free text and keeps it (the same
		// rule as claude, spec 2026-09-26-codex-native-approval decision 2).
		// A daemon prompt (an Inbox notice quoting a peer's body) never
		// binds: a forged wrapper there must not answer the user's row.
		if replies, ok := parseQuestionReply(in.Prompt); ok && !runtime.IsDaemonPrompt(in.Prompt) && h.RT != nil && s.ID != "" {
			for _, r := range replies {
				answer := r.Answer
				if answer == "" {
					answer = runtime.ResolvedInTerminal
				}
				req, err := h.RT.ResolveQuestionReply(ctx, s.ID, capPrompt(r.Question), answer)
				if err != nil {
					h.logf("hook: bind question reply for %s: %v", s.ID, err)
					continue
				}
				// Never rate-limited, as in PostToolUse: a missed forward
				// strands the user's approval.
				if next := runtime.NativeAnswerNextStep(req); next != "" {
					parts = append(parts, next)
				}
			}
		} else if in.Prompt != "" && !runtime.IsDaemonPrompt(in.Prompt) && h.RT != nil && s.AgentID != "" {
			if err := h.RT.ResolveAnsweredInTerminal(ctx, s.AgentID); err != nil {
				h.logf("hook: close rows after human prompt for %s: %v", s.ID, err)
			}
		}
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

		// Same guard, by normalized question text (2026-09-28-approval-
		// summary-enforced locked decision 2, now that a daemon-issued
		// question carries no token to check for): a question anywhere past
		// Questions[0] that binds to an open approval routed to this agent
		// would be recorded and answered but never bindable, same failure
		// mode as the token check above. No header available per batched
		// entry (documented scope limit): falls back to question-only
		// matching, same collision risk the single-question path below no
		// longer has.
		if isQuestionTool(in.ToolName) && h.RT != nil && s.ID != "" {
			for _, q := range batchedQuestionsAfterFirst(in.RawToolInput) {
				if _, ok := h.RT.BindNativeQuestion(ctx, s.AgentID, "", q); ok {
					return adapter.HookDecision{Block: true, Reason: "[swarm] Ask one swarm approval per question call."}, nil
				}
			}
		}

		// Bind the question to its swarm ref and record the HITL row. There is no summary gate any
		// more: the chat_block is checked at turn end (Stop), before the question is ever sent.
		if isQuestionTool(in.ToolName) && h.RT != nil && s.ID != "" {
			prompt, options := extractQuestion(in.ToolName, in.RawToolInput)
			header := extractQuestionHeader(in.ToolName, in.RawToolInput)
			if ref, bound := h.RT.BindNativeQuestion(ctx, s.AgentID, header, prompt); bound {
				// Hold the native tool to the stored options word for word: an
				// agent that invents labels from decision codes gets its input
				// rewritten (Claude) or the call denied (Codex, agy).
				np, haveNP := h.RT.NativePromptForRef(ctx, ref)
				haveNP = haveNP && len(np.Options) > 0
				if haveNP && (kind == runtime.Codex || kind == runtime.Agy) && !slices.Equal(options, np.Options) {
					return adapter.HookDecision{Block: true, Reason: "[swarm] Use native_prompt's options word for word: " +
						strings.Join(np.Options, " / ") + ". Ask again with exactly those labels."}, nil
				}
				_, _ = h.RT.AskQuestionBoundTo(ctx, s.ID, prompt, options, ref)
				if haveNP && kind == runtime.Claude && in.ToolName == "AskUserQuestion" {
					if upd, changed := rewriteClaudeQuestion(in.RawToolInput, np); changed {
						return adapter.HookDecision{UpdatedInput: upd}, nil
					}
				}
			} else {
				_, _ = h.RT.AskQuestion(ctx, s.ID, prompt, options)
			}
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
		// codex's async question tool answers {"accepted":true} before the
		// user has picked anything; its answer is bound on UserPromptSubmit.
		if isQuestionTool(in.ToolName) && in.ToolName != asyncQuestionTool && h.RT != nil && s.ID != "" {
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
		// Print-then-ask: judge the reply that just ended against any approval block the agent was
		// told to print, and send the question (or a reprint) in a fresh relay. print_attempts
		// bounds this, so it never touches stop_blocks.
		if h.RT != nil && s.ID != "" {
			r := runtime.TurnReply{}
			if texter, ok := a.(transcriptTexter); !ok {
				r.Trusted = true
			} else if in.LastAssistantMessage != "" {
				// Claude fires Stop before the final text reaches the transcript; its hook input
				// carries that text directly.
				r.Text, r.Readable = in.LastAssistantMessage, true
			} else if in.TranscriptPath != "" {
				r.Text, r.Readable = texter.AssistantTextSinceLastTurn(in.TranscriptPath)
			}
			sent, err := h.RT.PrintTurnEnded(ctx, s.ID, r)
			if err != nil {
				h.logf("hook: print check for %s: %v", s.ID, err)
			} else if sent {
				return adapter.HookDecision{Block: true, Reason: h.inboxNoticeOrFallback(ctx, s)}, nil
			}
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

// rewriteClaudeQuestion returns tool_input with questions[0] (the bound
// question) carrying np's header, question and options (label + description),
// single-select, and every other field untouched. Options np gives no
// description keep the agent's description for the same label, else "". changed is false when the
// options and select mode already equal np's, so nothing needs rewriting.
func rewriteClaudeQuestion(raw []byte, np runtime.NativePrompt) (json.RawMessage, bool) {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return nil, false
	}
	qs, _ := input["questions"].([]any)
	if len(qs) == 0 {
		return nil, false
	}
	q, _ := qs[0].(map[string]any)
	if q == nil {
		return nil, false
	}
	want := make([]any, len(np.Options))
	same := q["multiSelect"] != true
	have, _ := q["options"].([]any)
	same = same && len(have) == len(np.Options)
	for i, label := range np.Options {
		// Claude's schema requires "description" on every option, so it is
		// always emitted: np's, else the agent's own for the same label.
		desc := ""
		if i < len(np.Descriptions) {
			desc = np.Descriptions[i]
		}
		var hd string
		if i < len(have) {
			h, _ := have[i].(map[string]any)
			hd, _ = h["description"].(string)
			if desc == "" && h["label"] == label {
				desc = hd
			}
			same = same && h["label"] == label && hd == desc
		}
		want[i] = map[string]any{"label": label, "description": desc}
	}
	if same {
		return nil, false
	}
	q["header"], q["question"], q["options"], q["multiSelect"] = np.Header, np.Question, want, false
	out, err := json.Marshal(input)
	return out, err == nil
}

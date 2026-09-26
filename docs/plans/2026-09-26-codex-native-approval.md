# Codex Native Approval Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A codex 0.157 orchestrator's native approval binds to its `⟦swarm:<ref>⟧` request and can be forwarded with `swarm_ask kind:"native_answer"`, the same way Claude's does.

**Architecture:** The hook recognises `request_user_input_async` and reads `questions[].title`. PostToolUse ignores the tool's `{"accepted":true}` acknowledgement. UserPromptSubmit parses codex's `<send_user_message_question_reply>` message and binds each entry through a new `Store.ResolveQuestionReply`: by ref when the entry has one, otherwise by exact prompt. Any other human prompt keeps today's blanket close (the free-text fallback, identical to Claude's). `questionHookKinds` gains `Codex`, and the copy and skills follow.

**Tech Stack:** Go 1.x, SQLite (`requests` table, no migration), Markdown skills synced by `make skills-sync`.

**Spec:** `docs/specs/2026-09-26-codex-native-approval.md`. Read it first. Its locked decisions, copy and scenarios S1–S12 are normative.

## Global Constraints

- Worktree: `/Users/alexandertar/GitHub/agent-swarm-codex-native`, branch `fix/codex-native-approval`. Work only there.
- Never touch the primary checkout, `~/.swarm`, `~/.codex`, `~/.claude*`, the live daemon, launchd or `tmux -L swarm`.
- Strict TDD for each task: write the test, run it and watch it fail for the stated reason, write the minimal code, run it and watch it pass, then commit.
- Stage explicit paths only. Never `git add -A`, never `--amend`, never delete a test (port it).
- End every commit message with the line `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- After any `skills/*` edit, run `make skills-sync` and commit `internal/install/skills/` with it.
- No DB migration. No adapter change. No web or menubar change.
- Copy is verbatim from the spec's "All user-facing copy" section.
- The fixtures are already committed in `internal/hook/testdata/codex/native-question/`: `PreToolUse.json`, `PostToolUse-ack.json`, `UserPromptSubmit-typed-approve.json`, `UserPromptSubmit-question-reply.synthesized.json`. Do not move them into `hook-stdin/`.

## Batches

| Batch | Scope | Tasks | Depends on |
|---|---|---|---|
| **1** (only) | hook title/async recognition, ack skip, `ResolveQuestionReply`, question-reply binding, `questionHookKinds` + copy + ported tests, skills, verification | 1–7 | none |

One implementer and one review loop. Before starting, rebase on `origin/main`: `git -C /Users/alexandertar/GitHub/agent-swarm-codex-native fetch origin && git -C /Users/alexandertar/GitHub/agent-swarm-codex-native rebase origin/main`.

## File map

| File | Responsibility |
|---|---|
| `internal/hook/handler.go` | recognition (`isQuestionTool`, `extractQuestion`, `capPrompt`, batched guard), PostToolUse ack skip, `parseQuestionReply` and the UserPromptSubmit binding |
| `internal/hook/codex_native_test.go` (new) | every codex hook scenario (S1–S8) driven by the fixtures |
| `internal/runtime/requests.go` | `ResolveQuestionReply`, `questionHookKinds`, copy |
| `internal/runtime/requests_test.go`, `internal/runtime/native_answer_test.go`, `internal/hook/handler_test.go` | ported and added runtime/kind tests |
| `internal/mcpserver/tools.go` | the `swarm_ask` kind description |
| `skills/swarm/SKILL.md`, `skills/swarm-orchestrator/SKILL.md`, `internal/install/skills/**` | agent-facing copy |

---

### Task 1: PreToolUse recognises codex's async tool and `title` (S1, S2, S9)

**Files:**
- Create: `internal/hook/codex_native_test.go`
- Modify: `internal/hook/handler.go:26-118` (`extractQuestion`, `questionsHaveBatchedSwarmRef`), `internal/hook/handler.go:211-244` (`isQuestionTool` and its doc comment)
- Modify (port): `internal/hook/handler_test.go:1402` (`TestParentedAgentQuestionToolIsBlockedAndOpensNoRequest` case list)

**Interfaces:**
- Produces: `const asyncQuestionTool = "request_user_input_async"`, `func capPrompt(p string) string`, and these test helpers in `codex_native_test.go`: `probeQuestion`, `probeRef`, `codexSeed`, `codexHook`, `nativeFixture`, `questionRow`, `codexPreToolUse`, `codexPrompt`, `questionReplyPrompt`, plus the `qrow` type. Tasks 2 and 4 use them.

- [ ] **Step 1: Write the failing tests.** Create `internal/hook/codex_native_test.go`:

```go
package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Fixtures: testdata/codex/native-question, captured from codex 0.157 by an
// isolated probe on 2026-09-26 (see docs/specs/2026-09-26-codex-native-approval.md).
// UserPromptSubmit-question-reply.synthesized.json is NOT a capture: it is the
// typed fixture with its prompt replaced by the live-reported wrapper shape.
const (
	probeQuestion = "Approve probe? ⟦swarm:req_01PROBE0000000000000000000⟧"
	probeRef      = "req_01PROBE0000000000000000000"
	codexProvider = "01a0df19-71ec-7321-b37d-ada707746474"
)

func codexSeed(t *testing.T) (*Handler, string) {
	t.Helper()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(context.Background(), `UPDATE agents SET kind = 'codex' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	return h, ses
}

func codexHook(t *testing.T, h *Handler, ses, event string, stdin []byte) []byte {
	t.Helper()
	out, err := h.Handle(context.Background(), runtime.Codex, event, ses, stdin)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func nativeFixture(t *testing.T, name string) []byte {
	t.Helper()
	return fixture(t, "codex", "native-question", name)
}

type qrow struct {
	ID, State, Prompt, Options, Ref string
	Response, Via                   sql.NullString
}

// questionRow reads the one question row with this exact prompt; it fails
// the test when there is none.
func questionRow(t *testing.T, h *Handler, prompt string) qrow {
	t.Helper()
	var r qrow
	if err := h.DB.QueryRowContext(context.Background(), `SELECT id, state, prompt, COALESCE(options_json, '[]'),
		COALESCE(json_extract(binding_json, '$.ref'), ''), response_text, responded_via
		FROM requests WHERE kind = 'question' AND prompt = ?`, prompt).
		Scan(&r.ID, &r.State, &r.Prompt, &r.Options, &r.Ref, &r.Response, &r.Via); err != nil {
		t.Fatalf("question row %q: %v", prompt, err)
	}
	return r
}

// codexPreToolUse is a request_user_input_async PreToolUse payload in the
// captured shape (questions[].title, string options).
func codexPreToolUse(questions ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"session_id": codexProvider, "hook_event_name": "PreToolUse",
		"tool_name": "request_user_input_async", "tool_input": map[string]any{"questions": questions},
		"tool_use_id": "call_test"})
	return b
}

func codexPrompt(prompt string) []byte {
	b, _ := json.Marshal(map[string]any{"session_id": codexProvider, "hook_event_name": "UserPromptSubmit",
		"prompt": prompt})
	return b
}

func questionReplyPrompt(entries ...map[string]string) string {
	b, _ := json.Marshal(entries)
	return "<send_user_message_question_reply>" + string(b) + "</send_user_message_question_reply>"
}

// S1
func TestCodexPreToolUseFixtureOpensARefBoundRow(t *testing.T) {
	h, ses := codexSeed(t)
	if out := codexHook(t, h, ses, "PreToolUse", nativeFixture(t, "PreToolUse.json")); len(out) != 0 {
		t.Fatalf("the question tool must not be blocked, got %s", out)
	}
	r := questionRow(t, h, probeQuestion)
	var opts []string
	if err := json.Unmarshal([]byte(r.Options), &opts); err != nil {
		t.Fatal(err)
	}
	if r.State != "open" || r.Ref != probeRef || strings.Join(opts, "|") != "Approve|Request changes" {
		t.Fatalf("row = %+v options %v, want open, ref %s, Approve|Request changes", r, opts, probeRef)
	}
}

// S2
func TestCodexBatchedTitleQuestionsWithSwarmRefAreDenied(t *testing.T) {
	h, ses := codexSeed(t)
	out := codexHook(t, h, ses, "PreToolUse", codexPreToolUse(
		map[string]any{"title": "Pick a color", "options": []string{"Red", "Blue"}},
		map[string]any{"title": probeQuestion, "options": []string{"Approve", "Request changes"}}))
	if !strings.Contains(string(out), "[swarm] Ask one swarm approval per question call.") {
		t.Fatalf("a batched call carrying a swarm ref must be denied, got %s", out)
	}
	var n int
	if err := h.DB.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}
```

Port the parented test in `internal/hook/handler_test.go:1402`. The case list becomes:

```go
	}{{runtime.Claude, "AskUserQuestion"}, {runtime.Codex, "request_user_input"}, {runtime.Codex, "request_user_input_async"}, {runtime.Cursor, "ask_question"}, {runtime.Agy, "ask_question"}} {
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/hook/ -run 'TestCodexPreToolUseFixture|TestCodexBatchedTitle|TestParentedAgentQuestionTool' -count=1`
Expected: FAIL. S1 fails with `question row "Approve probe? …": sql: no rows in result set`. S2 fails with `a batched call carrying a swarm ref must be denied`. The parented subtest `codex request_user_input_async` fails with `must be blocked with the relay text`.

- [ ] **Step 3: Write the minimal implementation** in `internal/hook/handler.go`.

Add above `extractQuestion`:

```go
// capPrompt is the 1000-rune cap every hook-recorded question prompt gets,
// shared by extractQuestion and the codex question-reply binder so both
// compute the same prompt text.
func capPrompt(p string) string {
	if utf8.RuneCountInString(p) > 1000 {
		return string([]rune(p)[:997]) + "..."
	}
	return p
}
```

In `extractQuestion`, the `Questions` element struct becomes:

```go
		Questions []struct {
			Question string `json:"question"`
			Title    string `json:"title"` // codex 0.157 request_user_input_async
			Options  []any  `json:"options"`
		} `json:"questions"`
```

The `len(payload.Questions) > 0` branch becomes:

```go
	if len(payload.Questions) > 0 {
		prompt = payload.Questions[0].Question
		if prompt == "" {
			prompt = payload.Questions[0].Title
		}
		rawOptions = payload.Questions[0].Options
	} else {
```

Replace the inline cap

```go
	if n := utf8.RuneCountInString(prompt); n > 1000 {
		prompt = string([]rune(prompt)[:997]) + "..."
	}
```

with `prompt = capPrompt(prompt)`.

In `questionsHaveBatchedSwarmRef`:

```go
	var payload struct {
		Questions []struct {
			Question string `json:"question"`
			Title    string `json:"title"`
		} `json:"questions"`
	}
	…
	for _, q := range payload.Questions {
		if runtime.HasRefToken(q.Question) || runtime.HasRefToken(q.Title) {
			return true
		}
	}
```

Above `isQuestionTool`:

```go
// asyncQuestionTool is codex 0.157's native question tool. It returns
// {"accepted":true} at once; the user's answer arrives later as a
// <send_user_message_question_reply> UserPromptSubmit (parseQuestionReply).
const asyncQuestionTool = "request_user_input_async"
```

`isQuestionTool`'s case becomes:

```go
	case "ask_question", "AskUserQuestion", "request_user_input", "experimental_request_user_input", asyncQuestionTool, "AskQuestion":
```

In its doc comment, replace the four `codex request_user_input: UNCONFIRMED live …` lines with:

```go
//	codex request_user_input_async: confirmed live 2026-09-26 (codex 0.157; fixtures
//	  testdata/codex/native-question). Async: PostToolUse carries only {"accepted":true};
//	  the answer arrives on the next UserPromptSubmit as a <send_user_message_question_reply>
//	  message (parseQuestionReply). request_user_input / experimental_request_user_input
//	  are kept for older builds.
```

Change the closing paragraph's `(cursor,\n// muse, codex)` to `(cursor, muse)`.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/hook/ -count=1`
Expected: PASS for the whole package, including the existing Claude/agy question tests.

- [ ] **Step 5: Commit.**

```bash
cd /Users/alexandertar/GitHub/agent-swarm-codex-native
git add internal/hook/handler.go internal/hook/handler_test.go internal/hook/codex_native_test.go
git commit -m "fix(hook): recognise codex request_user_input_async and its title field

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: PostToolUse ignores the async acknowledgement (S3)

**Files:**
- Modify: `internal/hook/handler.go` (the `case "PostToolUse":` question block, currently `handler.go:634`)
- Test: `internal/hook/codex_native_test.go`

**Interfaces:**
- Consumes: `asyncQuestionTool`, `codexSeed`, `codexHook`, `nativeFixture`, `questionRow`, `probeQuestion` (Task 1).

- [ ] **Step 1: Write the failing test.** Append to `codex_native_test.go`:

```go
// S3: the tool_response is the JSON string "{\"accepted\":true}", an
// acknowledgement, never the user's answer.
func TestCodexAckOnlyPostToolUseLeavesTheRowOpen(t *testing.T) {
	h, ses := codexSeed(t)
	codexHook(t, h, ses, "PreToolUse", nativeFixture(t, "PreToolUse.json"))
	out := codexHook(t, h, ses, "PostToolUse", nativeFixture(t, "PostToolUse-ack.json"))
	if strings.Contains(string(out), "[swarm] Recorded") {
		t.Fatalf("an ack must not produce a forwarding step, got %s", out)
	}
	if r := questionRow(t, h, probeQuestion); r.State != "open" || r.Response.Valid {
		t.Fatalf("the ack must not answer the row: %+v", r)
	}
}
```

- [ ] **Step 2: Run it and confirm it fails.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/hook/ -run TestCodexAckOnlyPostToolUse -count=1`
Expected: FAIL. Either `an ack must not produce a forwarding step` or `the ack must not answer the row: {… State:answered … Response:{String:{"accepted":true} Valid:true} …}`.

- [ ] **Step 3: Write the minimal implementation.** In `case "PostToolUse":`, change the first guard to:

```go
		// codex's async question tool answers {"accepted":true} before the
		// user has picked anything; its answer is bound on UserPromptSubmit.
		if isQuestionTool(in.ToolName) && in.ToolName != asyncQuestionTool && h.RT != nil && s.ID != "" {
```

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/hook/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
cd /Users/alexandertar/GitHub/agent-swarm-codex-native
git add internal/hook/handler.go internal/hook/codex_native_test.go
git commit -m "fix(hook): never record codex's async question ack as the answer

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `Store.ResolveQuestionReply` (S12)

**Files:**
- Modify: `internal/runtime/requests.go` (add below `ResolveQuestionByPrompt`, around line 1333)
- Test: `internal/runtime/requests_test.go` (append)

**Interfaces:**
- Consumes: `refFromPrompt` (`native.go:44`), `ResolveQuestionByPrompt`, `ResolveQuestion`, `queryIDs`.
- Produces: `func (s *Store) ResolveQuestionReply(ctx context.Context, sessionID, question, answer string) (Request, error)`. Task 4 calls it.

- [ ] **Step 1: Write the failing test.** Append to `internal/runtime/requests_test.go`:

```go
// TestResolveQuestionReplyBindsByRefThenByPrompt is S12 of
// docs/specs/2026-09-26-codex-native-approval.md: a codex question-reply
// entry binds by its ref (surrounding text may differ), else by exact
// prompt; an unknown ref is a silent no-op; a ref'd row repointed at a
// successor session still resolves from the retired one.
func TestResolveQuestionReplyBindsByRefThenByPrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Replies", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.AskQuestion(ctx, ses.ID, "Approve the plan (rev 1)? ⟦swarm:req_PLAN1⟧", []string{"Approve", "Request changes"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.AskQuestion(ctx, ses.ID, "Pick a color", []string{"Red", "Blue"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ResolveQuestionReply(ctx, ses.ID, "Anything? ⟦swarm:req_UNKNOWN⟧", "Approve")
	if err != nil || got.ID != "" {
		t.Fatalf("unknown ref: got %+v, %v; want the zero Request and nil", got, err)
	}

	got, err = s.ResolveQuestionReply(ctx, ses.ID, "Reworded ⟦swarm:req_PLAN1⟧", "Approve")
	if err != nil || got.ID != ref.ID || got.State != "answered" || got.ResponseText != "Approve" || got.RespondedVia != "terminal" {
		t.Fatalf("by ref: got %+v, %v", got, err)
	}

	got, err = s.ResolveQuestionReply(ctx, ses.ID, "Pick a color", "Blue")
	if err != nil || got.ID != plain.ID || got.ResponseText != "Blue" {
		t.Fatalf("by prompt: got %+v, %v", got, err)
	}

	moved, err := s.AskQuestion(ctx, ses.ID, "Close SPIKE-1? ⟦swarm:req_CLOSE1⟧", nil)
	if err != nil {
		t.Fatal(err)
	}
	succ, err := s.startSessionForTest(ctx, a, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Interrupted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET session_id = ? WHERE agent_id = ? AND state = 'open'`,
		succ.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.ResolveQuestionReply(ctx, ses.ID, "Close SPIKE-1? ⟦swarm:req_CLOSE1⟧", "Approve")
	if err != nil || got.ID != moved.ID || got.State != "answered" {
		t.Fatalf("repointed: got %+v, %v", got, err)
	}
}
```

- [ ] **Step 2: Run it and confirm it fails.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/runtime/ -run TestResolveQuestionReplyBindsByRefThenByPrompt -count=1`
Expected: FAIL to compile with `s.ResolveQuestionReply undefined`.

- [ ] **Step 3: Write the minimal implementation.** Add below `ResolveQuestionByPrompt` in `requests.go`:

```go
// ResolveQuestionReply closes the question row that one entry of a codex
// question reply answers, via terminal (docs/specs/2026-09-26-codex-native-
// approval.md). A question carrying a ⟦swarm:ref⟧ resolves the session's
// agent's newest open question row bound to that ref -- agent-keyed, like
// ResolveAnsweredInTerminal, so a row repointed at a successor session still
// matches; a question without one resolves by exact prompt through
// ResolveQuestionByPrompt. No match is not an error: the zero Request comes
// back.
func (s *Store) ResolveQuestionReply(ctx context.Context, sessionID, question, answer string) (Request, error) {
	ref := refFromPrompt(question)
	if ref == "" {
		return s.ResolveQuestionByPrompt(ctx, sessionID, question, answer)
	}
	ids, err := s.queryIDs(ctx, `SELECT r.id FROM requests r JOIN sessions se ON se.agent_id = r.agent_id
		WHERE se.id = ? AND r.kind = 'question' AND r.state = 'open'
		  AND json_extract(r.binding_json, '$.ref') = ?
		ORDER BY r.created_at DESC LIMIT 1`, sessionID, ref)
	if err != nil {
		return Request{}, err
	}
	if len(ids) == 0 {
		return Request{}, nil
	}
	return s.ResolveQuestion(ctx, ids[0], answer, "terminal")
}
```

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/runtime/ -run 'TestResolveQuestionReply|TestRetiredSessionResolvers' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
cd /Users/alexandertar/GitHub/agent-swarm-codex-native
git add internal/runtime/requests.go internal/runtime/requests_test.go
git commit -m "feat(runtime): ResolveQuestionReply binds a question reply by ref, else prompt

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: UserPromptSubmit binds codex question replies (S4–S8)

**Files:**
- Modify: `internal/hook/handler.go` (add `questionReply`, `questionReplyRe`, `parseQuestionReply` after `questionsHaveBatchedSwarmRef`; rewrite the head of `case "UserPromptSubmit":`, currently `handler.go:478-484`)
- Test: `internal/hook/codex_native_test.go`

**Interfaces:**
- Consumes: `capPrompt`, the test helpers (Task 1), `runtime.(*Store).ResolveQuestionReply` (Task 3), `runtime.NativeAnswerNextStep`, `runtime.ResolvedInTerminal`, `contextOf` (`handler_test.go:55`).
- Produces: `type questionReply struct{ Question, Answer string }` and `func parseQuestionReply(prompt string) ([]questionReply, bool)`.

- [ ] **Step 1: Write the failing tests.** Append to `codex_native_test.go`:

```go
// S4
func TestCodexQuestionReplyBindsByRefAndEmitsTheNextStep(t *testing.T) {
	h, ses := codexSeed(t)
	codexHook(t, h, ses, "PreToolUse", nativeFixture(t, "PreToolUse.json"))
	codexHook(t, h, ses, "PostToolUse", nativeFixture(t, "PostToolUse-ack.json"))
	out := codexHook(t, h, ses, "UserPromptSubmit", nativeFixture(t, "UserPromptSubmit-question-reply.synthesized.json"))

	r := questionRow(t, h, probeQuestion)
	if r.State != "answered" || r.Via.String != "terminal" || r.Response.String != "Approve" {
		t.Fatalf("row = %+v, want answered via terminal with the picked %q", r, "Approve")
	}
	want := `swarm_ask kind:"native_answer", ref:"` + probeRef + `", decision:"approve"`
	if got := contextOf(t, out); !strings.Contains(got, want) {
		t.Fatalf("context = %q, want it to contain %q", got, want)
	}
	// Observed evidence: forwarding the opposite of the picked option is refused.
	if _, err := h.RT.Ask(context.Background(), ses, runtime.AskInput{Kind: "native_answer", Ref: probeRef,
		Decision: "request_changes"}); err == nil || !strings.Contains(err.Error(), `"Approve"`) {
		t.Fatalf("err = %v, want a decision mismatch against the observed \"Approve\"", err)
	}
}

// S5: typed free text keeps Claude's blanket close (locked decision 2).
func TestCodexTypedReplyFallsBackToAnsweredInTerminal(t *testing.T) {
	h, ses := codexSeed(t)
	codexHook(t, h, ses, "PreToolUse", nativeFixture(t, "PreToolUse.json"))
	codexHook(t, h, ses, "PostToolUse", nativeFixture(t, "PostToolUse-ack.json"))
	out := codexHook(t, h, ses, "UserPromptSubmit", nativeFixture(t, "UserPromptSubmit-typed-approve.json"))

	r := questionRow(t, h, probeQuestion)
	if r.State != "answered" || r.Via.String != "terminal" || r.Response.String != "Answered in terminal" {
		t.Fatalf("row = %+v, want answered via terminal as %q", r, "Answered in terminal")
	}
	if got := contextOf(t, out); strings.Contains(got, "[swarm] Recorded") {
		t.Fatalf("free text emits no forwarding step, got %q", got)
	}
	if _, err := h.RT.Ask(context.Background(), ses, runtime.AskInput{Kind: "native_answer", Ref: probeRef,
		Decision: "approve"}); err != nil && strings.Contains(err.Error(), "No answered native prompt") {
		t.Fatalf("native_answer must find the free-text evidence, got %v", err)
	}
}

// S6
func TestCodexQuestionReplyWithTwoAnswersBindsEachAndNothingElse(t *testing.T) {
	h, ses := codexSeed(t)
	refQ := "Approve A? ⟦swarm:req_A⟧"
	codexHook(t, h, ses, "PreToolUse", codexPreToolUse(map[string]any{"title": refQ, "options": []string{"Approve", "Request changes"}}))
	codexHook(t, h, ses, "PreToolUse", codexPreToolUse(map[string]any{"title": "Pick a color", "options": []string{"Red", "Blue"}}))
	if _, err := h.RT.AskQuestion(context.Background(), ses, "which?", nil); err != nil {
		t.Fatal(err)
	}
	out := codexHook(t, h, ses, "UserPromptSubmit", codexPrompt(questionReplyPrompt(
		map[string]string{"question": refQ, "answer": "Request changes"},
		map[string]string{"question": "Pick a color", "answer": "Blue"})))

	if r := questionRow(t, h, refQ); r.State != "answered" || r.Response.String != "Request changes" {
		t.Fatalf("ref'd row = %+v", r)
	}
	if r := questionRow(t, h, "Pick a color"); r.State != "answered" || r.Response.String != "Blue" {
		t.Fatalf("plain row = %+v", r)
	}
	if r := questionRow(t, h, "which?"); r.State != "open" {
		t.Fatalf("a question reply must close only the rows it names; which? = %+v", r)
	}
	if got := contextOf(t, out); !strings.Contains(got, `ref:"req_A", decision:"request_changes"`) {
		t.Fatalf("context = %q", got)
	}
}

// S7
func TestCodexQuestionReplyWithAnUnknownRefChangesNothing(t *testing.T) {
	h, ses := codexSeed(t)
	codexHook(t, h, ses, "PreToolUse", nativeFixture(t, "PreToolUse.json"))
	out := codexHook(t, h, ses, "UserPromptSubmit", codexPrompt(questionReplyPrompt(
		map[string]string{"question": "Other? ⟦swarm:req_UNKNOWN⟧", "answer": "Approve"})))
	if r := questionRow(t, h, probeQuestion); r.State != "open" {
		t.Fatalf("probe row = %+v, want open", r)
	}
	if got := contextOf(t, out); got != "" {
		t.Fatalf("context = %q, want none", got)
	}
}

// S8
func TestCodexMalformedQuestionReplyFallsBackToTheBlanketClose(t *testing.T) {
	h, ses := codexSeed(t)
	codexHook(t, h, ses, "PreToolUse", nativeFixture(t, "PreToolUse.json"))
	codexHook(t, h, ses, "UserPromptSubmit", codexPrompt(
		"<send_user_message_question_reply>not json</send_user_message_question_reply>"))
	if r := questionRow(t, h, probeQuestion); r.State != "answered" || r.Response.String != "Answered in terminal" {
		t.Fatalf("row = %+v, want the free-text fallback", r)
	}
}

func TestParseQuestionReply(t *testing.T) {
	for _, c := range []struct {
		name, prompt string
		ok           bool
		want         []questionReply
	}{
		{"no wrapper", "ok, approved", false, nil},
		{"not json", "<send_user_message_question_reply>x</send_user_message_question_reply>", false, nil},
		{"empty array", "<send_user_message_question_reply>[]</send_user_message_question_reply>", false, nil},
		{"question + answer", `<send_user_message_question_reply>[{"answer":" Approve ","question":"Q?","extra":1}]</send_user_message_question_reply>`,
			true, []questionReply{{Question: "Q?", Answer: "Approve"}}},
		{"title fallback, non-string answer", `<send_user_message_question_reply>[{"answer":["a","b"],"title":"T?"}]</send_user_message_question_reply>`,
			true, []questionReply{{Question: "T?", Answer: ""}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseQuestionReply(c.prompt)
			if ok != c.ok || len(got) != len(c.want) {
				t.Fatalf("got %+v, %v; want %+v, %v", got, ok, c.want, c.ok)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("entry %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/hook/ -run 'TestCodex|TestParseQuestionReply' -count=1`
Expected: FAIL to compile with `undefined: questionReply` / `undefined: parseQuestionReply`. Temporarily comment out `TestParseQuestionReply` and run again to see the behavioural failures:
- S4 fails with `want answered via terminal with the picked "Approve"` (it got `Answered in terminal`).
- S6 fails with `a question reply must close only the rows it names`, or on the ref'd row's response.
- S7 fails with `probe row = {… State:answered …}, want open`.
- S5 and S8 already pass. They lock today's fallback, so it cannot regress.

Restore `TestParseQuestionReply`.

- [ ] **Step 3: Write the minimal implementation** in `internal/hook/handler.go`.

After `questionsHaveBatchedSwarmRef`:

```go
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
```

Replace the head of `case "UserPromptSubmit":`

```go
	case "UserPromptSubmit":
		if in.Prompt != "" && !runtime.IsDaemonPrompt(in.Prompt) && h.RT != nil && s.AgentID != "" {
			if err := h.RT.ResolveAnsweredInTerminal(ctx, s.AgentID); err != nil {
				h.logf("hook: close rows after human prompt for %s: %v", s.ID, err)
			}
		}
		var parts []string
```

with:

```go
	case "UserPromptSubmit":
		var parts []string
		// A codex question reply names the rows it answers: bind exactly
		// those (by ref, else prompt) and never run the blanket close below.
		// Any other human prompt is typed free text and keeps it (the same
		// rule as claude, spec 2026-09-26-codex-native-approval decision 2).
		if replies, ok := parseQuestionReply(in.Prompt); ok && h.RT != nil && s.ID != "" {
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
```

The rest of the case (compaction notice, pending inbox notice, `return adapter.HookDecision{Context: strings.Join(parts, " ")}, nil`) is unchanged.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/hook/ -count=1`
Expected: PASS for the whole package, including `TestHumanPromptClosesOpenRowsButDaemonPromptsDoNot`, `TestMuseUserPromptSubmitClosesOpenQuestionRows` and `TestUserPromptSubmitSkipsDoubleDeliveryForDaemonPrompt`.

- [ ] **Step 5: Commit.**

```bash
cd /Users/alexandertar/GitHub/agent-swarm-codex-native
git add internal/hook/handler.go internal/hook/codex_native_test.go
git commit -m "fix(hook): bind codex question replies on UserPromptSubmit and emit the forward step

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: codex joins `questionHookKinds`; copy and ported tests (S10, S11)

**Files:**
- Modify: `internal/runtime/requests.go:454-457` (`reaskQuestionNext`), `requests.go:674-696` (`errQuestionUseNativeTool`, `questionHookKinds` and their comments)
- Modify: `internal/mcpserver/tools.go:163`
- Modify (port): `internal/runtime/requests_test.go:58-72`, `internal/runtime/native_answer_test.go:505-515`
- Test (add): `internal/runtime/native_answer_test.go`

**Interfaces:**
- Consumes: `newStore`, `worker`, `mustSessionID`, `(*Store).SendApproval`, `AskInput{Kind:"native_prompt", ForMsg}` (existing test helpers).

- [ ] **Step 1: Port and add the tests.**

In `internal/runtime/requests_test.go`, replace the comment above `TestSwarmAskQuestionIsRefusedForHookedKindsOnly` with:

```go
// TestSwarmAskQuestionIsRefusedForHookedKindsOnly is Task 9 (spec section
// 1.7), updated 2026-09-26 (docs/specs/2026-09-26-codex-native-approval.md):
// claude, agy and codex have a live-confirmed hook for their native question
// tool, so swarm_ask kind:"question" is refused for them. cursor's
// AskQuestion never fires a hook at all (confirmed, forum bug 161836), and
// muse's request_user_input the same (confirmed live, Task 4), so both keep
// swarm_ask as their only path to Needs you.
```

Change the case list to:

```go
	}{{Claude, true}, {Codex, true}, {Agy, true}, {Muse, false}, {Cursor, false}} {
```

In `internal/runtime/native_answer_test.go`, change the comment's `cursor,\n// muse and codex have no native question hook (spec 1.7)` to `cursor\n// and muse have no native question hook (spec 1.7; codex gained one 2026-09-26,\n// see TestNativePromptForMsgAllowedForCodex)`. Change the loop to:

```go
	for _, kind := range []AgentKind{Cursor, Muse} {
```

Add right after that test:

```go
// TestNativePromptForMsgAllowedForCodex is the codex case of the test above,
// inverted (docs/specs/2026-09-26-codex-native-approval.md S11): codex's
// request_user_input_async is hooked, so a child's approval question gets a
// native prompt instead of errChildApprovalNoNativePath.
func TestNativePromptForMsgAllowedForCodex(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, _, wSes := worker(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'codex' WHERE id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	orchSes := mustSessionID(t, s, orch.ID)
	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q})
	if err != nil {
		t.Fatalf("codex has a native approval hook now, got %v", err)
	}
	if out.NativePrompt == nil || !strings.HasSuffix(out.NativePrompt.Question, "⟦swarm:"+q+"⟧") {
		t.Fatalf("native prompt = %+v, want a question ending in the %s ref token", out.NativePrompt, q)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/runtime/ -run 'TestSwarmAskQuestionIsRefusedForHookedKindsOnly|TestNativePromptForMsg' -count=1`
Expected: FAIL. The `codex` subtest fails with `refused = false, want true`, and `TestNativePromptForMsgAllowedForCodex` fails with `codex has a native approval hook now, got Your agent kind has no native approval hook…`.

- [ ] **Step 3: Write the minimal implementation.**

`requests.go`: set `questionHookKinds` to:

```go
var questionHookKinds = map[AgentKind]bool{Claude: true, Agy: true, Codex: true}
```

and replace its doc comment with:

```go
// questionHookKinds are the kinds whose native question tool Swarm
// intercepts via a hook (spec section 1.7; codex added 2026-09-26,
// docs/specs/2026-09-26-codex-native-approval.md). cursor and muse are
// absent on purpose: neither dispatches a hook for its native question
// tool at all, so both keep swarm_ask kind:"question" as their only path
// to Needs you.
```

Replace `errQuestionUseNativeTool` with:

```go
const errQuestionUseNativeTool = "Ask the user with your own native question tool " +
	"(claude AskUserQuestion, agy ask_question, codex request_user_input). " +
	"Swarm shows it in Needs you and closes it when the user answers."
```

Replace the whole comment block above it (`requests.go:674-685`) with:

```go
// errQuestionUseNativeTool is swarm_ask's refusal for kind:"question" when
// the caller's kind has a hooked native question tool (spec section 1.7):
// the hook opens the Needs-you row itself, so swarm_ask would only ever
// duplicate it. muse is deliberately absent from the copy below: Task 4's
// live probe (2026-09-25/26, docs/plans/2026-09-25-needs-you-and-child-
// approval-routing.md) found its request_user_input never dispatches a hook
// at all, so it joins cursor's exception. codex's request_user_input_async
// was confirmed live on 2026-09-26 (docs/specs/2026-09-26-codex-native-
// approval.md), superseding Task 4b's unfinished check.
```

Replace `reaskQuestionNext` with:

```go
const reaskQuestionNext = "Ask the user again with the same text and options: claude, agy and codex with your " +
	"native question tool, cursor and muse with swarm_ask kind:\"question\". Swarm keeps one Needs-you row for it."
```

`internal/mcpserver/tools.go:163`: in the `kind` description, replace

```
question is refused for claude and agy (they have a native question tool Swarm hooks instead); cursor, muse and codex keep it, since their native question tool is either not hookable or not yet confirmed.
```

with

```
question is refused for claude, agy and codex (they have a native question tool Swarm hooks instead); cursor and muse keep it, since their native question tool is not hookable.
```

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/runtime/ ./internal/mcpserver/ ./internal/hook/ -count=1`
Expected: PASS. `approval_lane_test.go` compares against the `reaskQuestionNext` constant, so it follows the new copy. If any test hard-codes the old copy strings (`grep -rn 'cursor, muse and codex' internal --include='*_test.go'`), update that assertion to the new copy. Do not delete it.

- [ ] **Step 5: Commit.**

```bash
cd /Users/alexandertar/GitHub/agent-swarm-codex-native
git add internal/runtime/requests.go internal/runtime/requests_test.go internal/runtime/native_answer_test.go internal/mcpserver/tools.go
git commit -m "feat(runtime): codex is a native-question-hook kind; update refusal, relay and tool copy

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Skills copy and `make skills-sync`

**Files:**
- Modify: `skills/swarm/SKILL.md:17`, `skills/swarm-orchestrator/SKILL.md:44,53,68`
- Regenerate: `internal/install/skills/**`

- [ ] **Step 1: Write the failing check.** This task is copy-only, so the check is a grep:

Run: `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && grep -n 'muse and codex\|Cursor, muse and codex' skills/swarm/SKILL.md skills/swarm-orchestrator/SKILL.md`
Expected: 4 matches (lines 17, 44, 53, 68). The task is done when this prints nothing.

- [ ] **Step 2: Edit `skills/swarm/SKILL.md` line 17.** Replace

> Top-level agents ask the user only with their native question tool (claude `AskUserQuestion`, agy `ask_question`); `swarm_ask kind: "question"` is refused. Cursor, muse and codex are the exception: their question tool is invisible to Swarm or unconfirmed, so agents of these kinds use `swarm_ask kind: "question"`.

with

> Top-level agents ask the user only with their native question tool (claude `AskUserQuestion`, agy `ask_question`, codex `request_user_input`); `swarm_ask kind: "question"` is refused. Cursor and muse are the exception: their question tool is invisible to Swarm, so agents of these kinds use `swarm_ask kind: "question"`.

- [ ] **Step 3: Edit `skills/swarm-orchestrator/SKILL.md`.**
  - Line 44: replace `claude and agy with their native question tool (header `<child> asks`, the child's text and options verbatim); cursor, muse and codex with `swarm_ask kind: "question"` instead, since their native question tool has no Swarm hook` with `claude, agy and codex with their native question tool (header `<child> asks`, the child's text and options verbatim); cursor and muse with `swarm_ask kind: "question"` instead, since their native question tool has no Swarm hook`.
  - Line 53: replace `cursor, muse and codex leave it to the board or `swarm approve`` with `cursor and muse leave it to the board or `swarm approve``.
  - Line 68: right after `… do not treat the bound question row as the approval by itself — only `native_answer`'s `approval_result` is.`, insert ` Codex only: `request_user_input` returns before the user answers, so end your turn right after showing the prompt. The user's answer arrives as your next user message, and Swarm adds a `[swarm] Recorded …` line naming the exact `native_answer` call; make that call first thing in that turn. If the user typed a reply instead of picking an option, interpret it and forward what they meant, the same as below.` Replace the line's final sentence `Cursor, muse and codex have no native path: the user approves in the board or with `swarm approve`.` with `Cursor and muse have no native path: the user approves in the board or with `swarm approve`.`

- [ ] **Step 4: Sync and verify.**

Run:
```bash
cd /Users/alexandertar/GitHub/agent-swarm-codex-native
make skills-sync
grep -rn 'muse and codex\|Cursor, muse and codex' skills internal/install/skills
diff -r skills internal/install/skills && echo IN-SYNC
go test ./internal/install/ ./internal/adapter/ -count=1
```
Expected: no grep output, `IN-SYNC`, and PASS.

- [ ] **Step 5: Commit.**

```bash
cd /Users/alexandertar/GitHub/agent-swarm-codex-native
git add skills/swarm/SKILL.md skills/swarm-orchestrator/SKILL.md internal/install/skills
git commit -m "docs(skills): codex asks and approves through its native question tool

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

(`internal/install/skills` is an explicit directory path, not `-A`. Run `git status --short internal/install/skills` first and confirm that only the two SKILL.md files changed.)

---

### Task 7: Full verification

- [ ] **Step 1: Run the targeted suite.**
  `cd /Users/alexandertar/GitHub/agent-swarm-codex-native && go test ./internal/hook/ ./internal/runtime/ ./internal/mcpserver/ -run 'Codex|QuestionReply|SwarmAskQuestion|NativePromptForMsg|ParentedAgentQuestionTool' -count=1 -v 2>&1 | tail -40`. Expected: every listed test passes, one per scenario S1–S12.
- [ ] **Step 2: Run vet and format.** `go vet ./... && make fmt`. Expected: no output from either and exit 0.
- [ ] **Step 3: Run the whole Go suite with race detection.** `go test -race ./... -count=1 2>&1 | tail -30`. Expected: `ok` for every package, no `FAIL`.
- [ ] **Step 4: Run the e2e suite.** `make e2e` (port 17778, socket `swarm-e2e`; safe). Expected: PASS.
- [ ] **Step 5: Check the test count.** `git diff origin/main --stat` must show no deleted `_test.go` file, and `git diff origin/main -- '*_test.go' | grep '^-func Test'` must print nothing.
- [ ] **Step 6: Report.** List the commit SHAs and the scenario-to-test mapping. Note the manual post-deploy check from the spec: on a real codex TUI orchestrator, confirm the reply arrives wrapped, and recapture the fixture if the shape differs.

## Self-review (done while writing)

- Spec coverage: S1–S2 and S9 → Task 1; S3 → Task 2; S12 → Task 3; S4–S8 → Task 4; S10–S11 → Task 5; skill copy → Task 6; command order → Task 7. MCP description → Task 5. Code-comment corrections → Tasks 1 and 5.
- Names are consistent across tasks: `asyncQuestionTool`, `capPrompt`, `questionReply`, `parseQuestionReply`, `ResolveQuestionReply(ctx, sessionID, question, answer)`.
- No test is deleted. Two tests are ported (a case list is extended or inverted), and the dropped codex refusal case becomes `TestNativePromptForMsgAllowedForCodex`.

# Codex native approval

Date: 2026-09-26. Status: design locked, ready to build (one batch).
Plan: `docs/plans/2026-09-26-codex-native-approval.md`.
Supersedes, for codex only: `docs/specs/2026-09-25-needs-you-and-child-approval-routing.md` §1.7 (line 107, codex listed as an exception) and `docs/specs/2026-09-26-epic-approval-lane.md` lines 61 and 257 (codex on the board/`swarm_ask question` fallback).

## Context

### Problem

A codex orchestrator cannot forward a native approval. Live case: agent `db-tests-are-taking-too-long` (SPIKE-20) called `swarm_ask kind:"native_answer"` four times and got the same refusal each time:

```
No answered native prompt for req_… in your terminal. Show the native_prompt from swarm_ask verbatim with your native question tool, then forward the user's answer.
```

One of the user's replies was free text ("ok, approved") typed as a normal prompt.

`swarm_ask` gives every agent kind a `native_prompt`: `askApproval` (`internal/runtime/requests.go:1081`), `confirm.go:185` and the `request_open` relay (`requests.go:495`). For codex, the hook chain that should record the answer breaks at step 3 (hook binding). An isolated probe of codex 0.157 captured these hook payloads (now in `internal/hook/testdata/codex/native-question/`):

1. **Tool name.** Codex 0.157 emits `request_user_input_async`. `isQuestionTool` (`internal/hook/handler.go:238`) lists only `request_user_input` and `experimental_request_user_input`, so PreToolUse never opens a question row.
2. **Question field.** Codex sends `tool_input.questions[].title`, with options as plain strings. `extractQuestion` (`handler.go:26`) reads only `questions[].question`, so the prompt falls back to `"request_user_input_async called"` and the `⟦swarm:<ref>⟧` token is lost. `questionsHaveBatchedSwarmRef` (`handler.go:100`) has the same blind spot.
3. **Async answer.** The tool returns at once. PostToolUse's `tool_response` is the JSON string `"{\"accepted\":true}"`, which is an acknowledgement and not the answer. The answer arrives later, in a new turn, as a `UserPromptSubmit` whose prompt is a user message:
   ```
   <send_user_message_question_reply>[{"answer":"Approve","question":"…⟦swarm:req_…⟧",…}]</send_user_message_question_reply>
   ```
   UserPromptSubmit binds nothing today. Adding the tool name alone would make PostToolUse record the literal `{"accepted":true}` as the answer, because `extractToolResponseText` (`handler.go:134`) returns a JSON string's contents as-is.
4. **Kind gate and copy.** `questionHookKinds` (`requests.go:696`) leaves out codex. As a result, `native_prompt`/`native_answer` for a child approval (`msg_` ref) refuse codex (`requireNativeApprovalHook`, `native.go:279`), and `swarm_ask kind:"question"` stays open to codex. `skills/swarm-orchestrator/SKILL.md:68` says codex has no native path, while the daemon hands codex a `native_prompt` anyway.

### How Claude binds today (the behaviour to mirror)

- **Picked option.** PreToolUse opens a `question` row whose prompt is the capped question text. `askQuestion` binds `binding_json.ref` from the token. PostToolUse resolves the row by exact prompt (`ResolveQuestionByPrompt`) with the picked label as `response_text`, via `terminal`, and emits `NativeAnswerNextStep`. Then `native_answer` classifies the answer with `matchDecisionEvidence`: the exact label (or `label:` remark) counts as `observed`, the other label is refused as a mismatch, and anything else is `agent_reported`.
- **Typed free text (user dismisses the dialog and types).** UserPromptSubmit sees a non-daemon prompt, and `ResolveAnsweredInTerminal` closes every open HITL question/blocker row of the agent with `response_text = "Answered in terminal"`, via `terminal`. The bound row is now answered via terminal, so `native_answer` finds it. `"Answered in terminal"` is neither label, so the decision is `agent_reported` and the orchestrator's forwarded decision is trusted (spec 2026-09-25 D1: "typed text can mean approve, keep it simple"). No next-step line is emitted on this path. The user's typed text is **not** stored on the row. `matchDecisionEvidence` surfaces `"Answered in terminal"` as the comment when the caller sends none.

### The change

1. The hook recognises `request_user_input_async` and reads `title`.
2. PostToolUse for `request_user_input_async` binds nothing, because its response is only an acknowledgement.
3. UserPromptSubmit parses a `<send_user_message_question_reply>` prompt. Each entry binds by its ref (or by exact prompt when it has none), with the entry's answer as `response_text`. A `NativeAnswerNextStep` line is emitted per bound ref'd row, and the blanket `ResolveAnsweredInTerminal` close is skipped for that prompt.
4. Any other human prompt keeps today's blanket close. That is the free-text fallback, unchanged and identical to Claude.
5. `questionHookKinds` gains `Codex`. The copy in the refusal, the relay, the MCP tool description and both skills is updated to match.

### Affected repos and worktrees

- Repo: `agent-swarm` only. Worktree: `/Users/alexandertar/GitHub/agent-swarm-codex-native`, branch `fix/codex-native-approval` off `origin/main`.
- No web, menubar or DB changes.

### Collision warnings

- `internal/hook/handler.go` and `internal/runtime/requests.go` are hot files. Rebase on `origin/main` before the build batch and again before merging.
- `skills/*` edits must be followed by `make skills-sync`, which rewrites `internal/install/skills/`. Commit both trees together.

### Caveats (scope calls made)

- **The question-reply wrapper was not captured in a fixture.** In the probe, the reply was typed as a plain prompt (`"Approve"`, captured as `UserPromptSubmit-typed-approve.json`). The `<send_user_message_question_reply>` shape comes from the live SPIKE-20 session as reported to this task. `UserPromptSubmit-question-reply.synthesized.json` is built from that shape, and its file name says so. The parser is deliberately tolerant: it regex-extracts the wrapper body, decodes only `question` (or `title`) and `answer`, and treats a non-string answer as empty. If the wrapper does not parse, the prompt falls through to today's free-text path, which never loses the answer.
- Parented codex workers now have `request_user_input_async` blocked with the relay text, as every other hooked kind already does. This is a deliberate behaviour change.
- Codex's batched `title` questions carrying a ref are now refused like Claude's.

## Locked decisions

1. **Make codex native approval work.** Board-only is rejected (user decision).
2. **Typed free text can still mean approve.** A free-typed reply without a ref binds exactly as Claude's does today, through the `ResolveAnsweredInTerminal` blanket close and then `agent_reported` in `native_answer`. No new, looser rule is invented.
3. **Add `Codex` to `questionHookKinds`.** Consequences: `swarm_ask kind:"question"` is refused for codex, and `native_prompt`/`native_answer` for `msg_` refs is allowed for codex.
4. **Fix the skill text** (`skills/swarm/SKILL.md`, `skills/swarm-orchestrator/SKILL.md`) and run `make skills-sync`.
5. **The acknowledgement is not an answer.** PostToolUse for `request_user_input_async` never resolves a row.
6. **A question reply binds by ref first.** An entry whose question carries a ref resolves the agent's newest open `question` row bound to that ref (agent-keyed, so a row repointed to a successor session still matches). An entry without a ref resolves by exact capped prompt through the existing `ResolveQuestionByPrompt`.
7. **A parsed question reply closes only the rows it names.** It never runs the blanket close. An unknown ref changes nothing and is not an error.
8. **`NativeAnswerNextStep` is reused unchanged** as UserPromptSubmit context for each bound ref'd row. It is never rate-limited.

### Explicit assumptions

- The wrapper's `question` text equals the tool call's `title` (so the no-ref prompt match works), and ref'd questions carry the token verbatim. Ref binding does not depend on the rest of the text.
- Codex's `HookOutput` emits `additionalContext` for `UserPromptSubmit` (`internal/adapter/codex.go:225`, fixture `testdata/codex/hook-output/UserPromptSubmit-context.json`). No adapter change is needed.
- `request_user_input` and `experimental_request_user_input` stay in the list for older codex builds, and their sync PostToolUse path is unchanged.

## DB models

None. The change reuses `requests` (`kind='question'`, `binding_json.ref`, `state`, `response_text`, `responded_via`). There is no migration.

## Model / API types

### `internal/hook/handler.go`

```go
// asyncQuestionTool is codex 0.157's native question tool. It returns
// {"accepted":true} at once; the user's answer arrives later as a
// <send_user_message_question_reply> UserPromptSubmit (parseQuestionReply).
const asyncQuestionTool = "request_user_input_async"

// capPrompt is the 1000-rune cap every hook-recorded question prompt gets
// (hoisted out of extractQuestion so the reply binder matches the same text).
func capPrompt(p string) string

// isQuestionTool: case list gains asyncQuestionTool.
func isQuestionTool(name string) bool

// extractQuestion: Questions[] gains `Title string json:"title"`; Title is
// used when Question is empty. The prompt goes through capPrompt.
func extractQuestion(toolName string, raw []byte) (string, []string)

// questionsHaveBatchedSwarmRef: checks Question and Title.
func questionsHaveBatchedSwarmRef(raw []byte) bool

// questionReply is one entry of codex's question-reply user message.
type questionReply struct {
	Question string // entry "question", or "title" when "question" is empty
	Answer   string // trimmed; "" when absent or not a JSON string
}

var questionReplyRe = regexp.MustCompile(`(?s)<send_user_message_question_reply>(.*?)</send_user_message_question_reply>`)

// parseQuestionReply extracts the entries of a codex question reply. ok is
// false when the prompt has no wrapper, the body is not a JSON array, or the
// array is empty -- the caller then treats the prompt as typed free text.
func parseQuestionReply(prompt string) (replies []questionReply, ok bool)
```

Behaviour changes inside `(*Handler).decide`:

- `UserPromptSubmit`:
  ```go
  var parts []string
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
  		if next := runtime.NativeAnswerNextStep(req); next != "" {
  			parts = append(parts, next)
  		}
  	}
  } else if in.Prompt != "" && !runtime.IsDaemonPrompt(in.Prompt) && h.RT != nil && s.AgentID != "" {
  	// unchanged blanket close (free-text fallback)
  }
  // compaction and pending-inbox parts follow, unchanged
  ```
- `PostToolUse`: the question-binding block's guard becomes `isQuestionTool(in.ToolName) && in.ToolName != asyncQuestionTool && h.RT != nil && s.ID != ""`. Everything else in PostToolUse is unchanged.

### `internal/runtime/requests.go`

```go
var questionHookKinds = map[AgentKind]bool{Claude: true, Agy: true, Codex: true}

// ResolveQuestionReply closes the question row that one entry of a codex
// question reply answers, via terminal. A question carrying a ⟦swarm:ref⟧
// resolves the session's agent's newest open question row bound to that ref
// (agent-keyed, like ResolveAnsweredInTerminal, so a row repointed at a
// successor session still matches); a question without one resolves by
// exact prompt through ResolveQuestionByPrompt. No match is not an error:
// the zero Request comes back.
func (s *Store) ResolveQuestionReply(ctx context.Context, sessionID, question, answer string) (Request, error)
```

Query for the ref path:

```sql
SELECT r.id FROM requests r JOIN sessions se ON se.agent_id = r.agent_id
WHERE se.id = ? AND r.kind = 'question' AND r.state = 'open'
  AND json_extract(r.binding_json, '$.ref') = ?
ORDER BY r.created_at DESC LIMIT 1
```

The ref path then calls `s.ResolveQuestion(ctx, id, answer, "terminal")`.

### MCP / HTTP / relays

- MCP `swarm_ask` `kind` description changes (copy below). No schema change.
- The `request_open` relay's `next` for a plain question changes (copy below).
- There are no HTTP changes.

## Screens

No screen changes. The board's Needs-you row lifecycle for a codex approval now matches Claude's:

```
codex orchestrator turn N                         Needs you (board)
  swarm_ask approval -> native_prompt              [approve_plan  SPIKE-20  open]
  request_user_input_async(title=…⟦swarm:req_X⟧)   [question      SPIKE-20  open]   <- PreToolUse (new)
  PostToolUse {"accepted":true}                    (no change)                       <- ack ignored (new)
  -- turn ends --
user answers in the codex TUI
turn N+1 UserPromptSubmit <send_user_message_question_reply>…
                                                   [question      answered/terminal "Approve"]
  context: [swarm] Recorded "Approve" for req_X. Forward it now: …
  swarm_ask native_answer ref:req_X approve        [approve_plan  approved]
```

Not on any screen: the acknowledgement payload, the wrapper text, and the typed-text contents (the free-text row reads "Answered in terminal", as for Claude).

## All user-facing copy (verbatim)

### Changed daemon copy

- `errQuestionUseNativeTool` (`requests.go`):
  ```
  Ask the user with your own native question tool (claude AskUserQuestion, agy ask_question, codex request_user_input). Swarm shows it in Needs you and closes it when the user answers.
  ```
- `reaskQuestionNext` (`requests.go`):
  ```
  Ask the user again with the same text and options: claude, agy and codex with your native question tool, cursor and muse with swarm_ask kind:"question". Swarm keeps one Needs-you row for it.
  ```
- MCP `swarm_ask` `kind` description (`internal/mcpserver/tools.go:163`). Replace the sentence `question is refused for claude and agy (they have a native question tool Swarm hooks instead); cursor, muse and codex keep it, since their native question tool is either not hookable or not yet confirmed.` with:
  ```
  question is refused for claude, agy and codex (they have a native question tool Swarm hooks instead); cursor and muse keep it, since their native question tool is not hookable.
  ```

### Reused unchanged

- `NativeAnswerNextStep` (`native.go:198`), for example `[swarm] Recorded "Approve" for req_X. Forward it now: swarm_ask kind:"native_answer", ref:"req_X", decision:"approve"`.
- PreToolUse batched refusal: `[swarm] Ask one swarm approval per question call.`
- Parented relay: `nativeQuestionRelay`.
- `errNoNativeEvidence`, `errDecisionMismatch`, `errChildApprovalNoNativePath`. The last one is no longer reachable for codex.

### Skills (agent-facing copy)

`skills/swarm/SKILL.md` line 17. Replace:

> Top-level agents ask the user only with their native question tool (claude `AskUserQuestion`, agy `ask_question`); `swarm_ask kind: "question"` is refused. Cursor, muse and codex are the exception: their question tool is invisible to Swarm or unconfirmed, so agents of these kinds use `swarm_ask kind: "question"`.

with:

> Top-level agents ask the user only with their native question tool (claude `AskUserQuestion`, agy `ask_question`, codex `request_user_input`); `swarm_ask kind: "question"` is refused. Cursor and muse are the exception: their question tool is invisible to Swarm, so agents of these kinds use `swarm_ask kind: "question"`.

`skills/swarm-orchestrator/SKILL.md`:

- Line 44. Replace `claude and agy with their native question tool (header `<child> asks`, the child's text and options verbatim); cursor, muse and codex with `swarm_ask kind: "question"` instead, since their native question tool has no Swarm hook` with:
  > claude, agy and codex with their native question tool (header `<child> asks`, the child's text and options verbatim); cursor and muse with `swarm_ask kind: "question"` instead, since their native question tool has no Swarm hook
- Line 53. Replace `cursor, muse and codex leave it to the board or `swarm approve`` with:
  > cursor and muse leave it to the board or `swarm approve`
- Line 68. After the sentence ending `… only `native_answer`'s `approval_result` is.`, insert:
  > Codex only: `request_user_input` returns before the user answers, so end your turn right after showing the prompt. The user's answer arrives as your next user message, and Swarm adds a `[swarm] Recorded …` line naming the exact `native_answer` call; make that call first thing in that turn. If the user typed a reply instead of picking an option, interpret it and forward what they meant, the same as below.

  Replace the final sentence `Cursor, muse and codex have no native path: the user approves in the board or with `swarm approve`.` with:
  > Cursor and muse have no native path: the user approves in the board or with `swarm approve`.

### Code comments to correct (not user-facing, but they must not lie)

- `handler.go:211-237` (`isQuestionTool` doc). The codex entry becomes: `codex request_user_input_async: confirmed live 2026-09-26 (codex 0.157; fixtures testdata/codex/native-question). Async: PostToolUse carries only {"accepted":true}; the answer arrives on the next UserPromptSubmit as a <send_user_message_question_reply> message (parseQuestionReply). request_user_input / experimental_request_user_input are kept for older builds.` Drop codex from the "hook absent or unconfirmed" list at the end.
- `requests.go:674-696` (`errQuestionUseNativeTool` / `questionHookKinds` docs): codex is hooked. Cursor and muse remain absent.
- `requests_test.go:58-66` and `native_answer_test.go:505-512` test comments, as ported below.

## File list

### Changed

| File | Change |
|---|---|
| `internal/hook/handler.go` | `asyncQuestionTool`, `capPrompt`, `questionReply`, `questionReplyRe`, `parseQuestionReply`. `isQuestionTool` gains the async name. `extractQuestion` and `questionsHaveBatchedSwarmRef` read `title`. PostToolUse skips the async tool. UserPromptSubmit binds question replies. Doc comment fixed. |
| `internal/runtime/requests.go` | `questionHookKinds` gains `Codex`, `ResolveQuestionReply` is added, `errQuestionUseNativeTool` and `reaskQuestionNext` copy changes, doc comments fixed. |
| `internal/mcpserver/tools.go` | `swarm_ask` kind description. |
| `skills/swarm/SKILL.md`, `skills/swarm-orchestrator/SKILL.md` | Copy above. |
| `internal/install/skills/**` | Regenerated by `make skills-sync`. |

### Tests (added or ported; none deleted)

| File | Test | Kind |
|---|---|---|
| `internal/hook/codex_native_test.go` (new) | `TestCodexPreToolUseFixtureOpensARefBoundRow` | added |
| same | `TestCodexBatchedTitleQuestionsWithSwarmRefAreDenied` | added |
| same | `TestCodexAckOnlyPostToolUseLeavesTheRowOpen` | added |
| same | `TestCodexQuestionReplyBindsByRefAndEmitsTheNextStep` | added |
| same | `TestCodexTypedReplyFallsBackToAnsweredInTerminal` | added |
| same | `TestCodexQuestionReplyWithTwoAnswersBindsEachAndNothingElse` | added |
| same | `TestCodexQuestionReplyWithAnUnknownRefChangesNothing` | added |
| same | `TestCodexMalformedQuestionReplyFallsBackToTheBlanketClose` | added |
| `internal/runtime/requests_test.go` | `TestResolveQuestionReplyBindsByRefThenByPrompt` | added |
| `internal/runtime/requests_test.go` | `TestSwarmAskQuestionIsRefusedForHookedKindsOnly`: `{Codex, false}` becomes `{Codex, true}`, comment updated | ported |
| `internal/runtime/native_answer_test.go` | `TestNativePromptForMsgRefusedForUnhookedOrchestratorKinds`: kinds `{Cursor, Muse}` (codex is now hooked, so its refusal case becomes the positive test below), comment updated | ported |
| `internal/runtime/native_answer_test.go` | `TestNativePromptForMsgAllowedForCodex` (the codex case, inverted) | added |
| `internal/hook/handler_test.go` | `TestParentedAgentQuestionToolIsBlockedAndOpensNoRequest`: add `{runtime.Codex, "request_user_input_async"}` | ported |

### Fixtures (committed with this spec)

`internal/hook/testdata/codex/native-question/`. They live outside `hook-stdin/` because `TestParseHookReadsEverySavedInput` derives the event name from the file name.

| File | Source |
|---|---|
| `PreToolUse.json` | probe capture `PreToolUse-1790449398380655000-63460.json`, verbatim |
| `PostToolUse-ack.json` | probe capture `PostToolUse-1790449398407734000-63463.json`, verbatim |
| `UserPromptSubmit-typed-approve.json` | probe capture `UserPromptSubmit-1790449429409666000-66434.json`, verbatim (`"prompt":"Approve"`) |
| `UserPromptSubmit-question-reply.synthesized.json` | the typed fixture with `prompt` replaced by the live-reported wrapper shape: `<send_user_message_question_reply>[{"answer":"Approve","question":"Approve probe? ⟦swarm:req_01PROBE0000000000000000000⟧"}]</send_user_message_question_reply>` |

### Reused unchanged

`ResolveQuestionByPrompt`, `ResolveQuestion`, `ResolveAnsweredInTerminal`, `NativeAnswerNextStep`, `matchDecisionEvidence`, `nativeAnswer`, `askQuestion` (ref binding), `requireNativeApprovalHook`, and the codex adapter (`ParseHook`, `HookOutput`).

### Deleted

Nothing.

## Verification

### Command order

1. `go test ./internal/hook/ ./internal/runtime/ ./internal/mcpserver/ -run 'Codex|QuestionReply|SwarmAskQuestion|NativePromptForMsg|ParentedAgentQuestionTool' -count=1`
2. `make skills-sync && git diff --stat internal/install/skills`
3. `go vet ./... && make fmt`
4. `go test -race ./... -count=1`
5. `make e2e` (own port 17778 and socket `swarm-e2e`; safe)

### Scenarios (each one is a test)

| # | Scenario | Expected |
|---|---|---|
| S1 | Codex PreToolUse fixture (`title`, string options, ref) | one `question` row, `open`, prompt `Approve probe? ⟦swarm:req_01PROBE0000000000000000000⟧`, options `["Approve","Request changes"]`, `binding_json.ref = req_01PROBE0000000000000000000`; hook output empty |
| S2 | Codex batched call with two `title` questions, one carrying a ref | blocked with `[swarm] Ask one swarm approval per question call.`; no row |
| S3 | PreToolUse, then the ack-only PostToolUse fixture | row stays `open`, `response_text` NULL; output has no `[swarm] Recorded` |
| S4 | S3, then the question-reply fixture | row `answered`, via `terminal`, `response_text = "Approve"`; context contains `swarm_ask kind:"native_answer", ref:"req_01PROBE0000000000000000000", decision:"approve"`; `native_answer` with `request_changes` is refused with a message containing `"Approve"` (observed mismatch) |
| S5 | S3, then the typed `"Approve"` fixture (free text) | row `answered`, via `terminal`, `response_text = "Answered in terminal"`; no `[swarm] Recorded` context; `native_answer approve` is not refused with `No answered native prompt` |
| S6 | Three open rows (ref'd `req_A` via PreToolUse, plain `Pick a color` via PreToolUse, unrelated `which?` via `AskQuestion`), then one reply with two entries (`req_A` gets `Request changes`, `Pick a color` gets `Blue`) | `req_A` row answered `Request changes` and the context names `decision:"request_changes"`; `Pick a color` row answered `Blue`; `which?` stays `open` |
| S7 | Probe row open, then a reply whose only entry carries `⟦swarm:req_UNKNOWN⟧` | no error; probe row stays `open`; empty context |
| S8 | Probe row open, then `<send_user_message_question_reply>not json</send_user_message_question_reply>` | falls back to the blanket close: row answered `Answered in terminal` |
| S9 | Parented codex agent calls `request_user_input_async` | blocked with the relay text; no row |
| S10 | `swarm_ask kind:"question"` from a top-level codex agent | refused with `errQuestionUseNativeTool` |
| S11 | Codex orchestrator, child approval question: `native_prompt for_msg` | returns a native prompt whose question ends with `⟦swarm:<msg id>⟧` (no `errChildApprovalNoNativePath`) |
| S12 | `ResolveQuestionReply`: a ref'd question with different surrounding text, a plain exact prompt, an unknown ref, and a ref whose row sits on a retired session repointed at the successor | the ref'd row resolves by ref; the plain row resolves by prompt; the unknown ref returns the zero Request and nil; the repointed row resolves |

Out-of-band check after deploy (manual, not part of this batch): on a real codex 0.157 TUI orchestrator, ask one approval and confirm that the reply arrives wrapped and that `native_answer` succeeds. If the live wrapper differs from the synthesized fixture, recapture it into `native-question/` and adjust `parseQuestionReply`.

## Explicitly out of scope

- Recording the user's typed free text on the row (Claude does not either; "Answered in terminal" stays).
- Emitting a next-step line on the free-text path.
- Binding more than `Questions[0]` at PreToolUse for non-ref batches.
- Changes to the codex adapter, cursor or muse, the board UI, the menubar, or the DB.
- `ResolveSessionPrompts`' blank-command close on codex PostToolUse (the existing ponytail note).
- Handling a multi-select (array) answer beyond treating it as `Resolved in terminal`.

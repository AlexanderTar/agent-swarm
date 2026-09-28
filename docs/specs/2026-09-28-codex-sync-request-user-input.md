# Spec: Codex asks with synchronous `request_user_input`; launch overrides documented

## Context

The user pointed at openai/codex#24750: Codex's synchronous `request_user_input` is
hidden in Default mode unless the under-development feature
`default_mode_request_user_input` is on. Swarm currently steers Codex to
`request_user_input_async`, whose PostToolUse carries only `{"accepted":true}` and
whose answer arrives on a later `UserPromptSubmit` (`parseQuestionReply`).

Live probe, 2026-09-28, Codex 0.157.0, isolated `CODEX_HOME` and tmux socket,
YOLO mode, `-c features.default_mode_request_user_input=true`:

1. Both `functions.request_user_input` and `functions.request_user_input_async` are exposed.
2. `request_user_input` renders the dialog and blocks until answered. PreToolUse
   `tool_input` is `{"questions":[{"header","id","question","options":[{"label","description"}]}]}`.
   The model may append ` (Recommended)` to an option label.
3. PostToolUse `tool_response` is a JSON **string** holding
   `{"answers":{"<id>":{"answers":["<label>", "user_note: <typed note>"?]}}}`.
   So the answer is hook-observed in the same turn, as with Claude.
4. The model refuses the sync tool for approvals ("restricted from permission
   requests") and for questions with no task behind them. It calls it when told a
   section review is a design decision the user chooses, not a permission request.

Fixtures: `internal/hook/testdata/codex/native-question-sync/`.

Muse 1.4.0-R4302.1 probe, same day (documented only; no behaviour change here):
- `request_user_input` still dispatches no PreToolUse/PostToolUse hook, even with
  hooks declared in an isolated `settings.json`. Sibling tools did fire.
- Muse's own `~/.local/share/muse/sessions/YYYY/MM/DD/<session>/session.jsonl` records
  `user_input_prompt_requested` (questions with the ⟦swarm:ref⟧ token) and
  `user_input_prompt_settled` (`answers[{id, selected_label}]`). Fixture:
  `internal/adapter/testdata/muse/session-user-input-1.4.0.jsonl`. Follow-up candidate.
- `muse session-message` needs `MUSE_EXPERIMENTAL_EXTERNAL_AGENT_INGRESS=on` in the
  **caller's** env to list sessions. `send` from a non-Muse process then fails
  `sender_unverified` ("sender session context is missing"); `MUSE_SESSION_ID` does
  not satisfy it. Native wake from the daemon stays unavailable.

## Locked decisions

- Codex launches and resumes with `-c features.default_mode_request_user_input=true`.
  Never written to the user's `~/.codex/config.toml`.
- Codex agents are told to ask with `request_user_input`, not the async variant.
  The hook keeps handling `request_user_input_async` and the reply wrapper for
  sessions already running on the old instructions.
- Copy tells Codex a review question is a design decision, not a permission request.
- The user asked to document every launch override and env variable in code: each
  adapter's argv/env builder gets a doc comment listing every flag, `-c` override
  and env var with its reason.

## Types / parsing

`extractToolResponseText(raw, prompt)` in `internal/hook/handler.go` gains a Codex
branch, checked when `raw` decodes to a string that itself decodes to an object
whose `answers` values are objects with an `answers` string array:

```go
// codex request_user_input: {"answers":{"<id>":{"answers":["Label","user_note: note"]}}}
func codexAnswerText(inner string) (string, bool)
```

- Take the first question entry (Swarm asks one question per call).
- Label = first array element not starting with `user_note:`, with a trailing
  ` (Recommended)` removed (case-insensitive).
- Note = text after `user_note:` in the first element that has it, trimmed.
- Return `Label` or `Label: note`, so `labelShape` maps the note to the comment.
- Anything else falls through to today's behaviour.

## User-facing copy

`internal/runtime/requests.go` `errQuestionUseNativeTool`:
"Ask the user with your own native question tool (claude AskUserQuestion, agy ask_question, codex request_user_input). Swarm shows it in Needs you and closes it when the user answers."
(already names `request_user_input`; unchanged).

`NativePromptNextStep` (internal/runtime/native.go) appends:
" Codex: use request_user_input, not request_user_input_async; a review question is a design decision the user chooses, not a permission request."

Skills (`skills/swarm*/SKILL.md` and `internal/install/skills/swarm*/SKILL.md`, kept identical):
- swarm SKILL line 17: codex `request_user_input` stays, adding "(the synchronous tool; Swarm launches Codex with it enabled)".
- swarm-orchestrator SKILL line 75: `Codex request_user_input_async` → `Codex request_user_input`, and drop "Codex answers arrive in the next turn; forward the recorded answer first in that turn." Add: "For Codex, a review question is a design decision the user chooses, not a permission request."

## File list

- `internal/adapter/codex.go`: add the `-c` override; doc comment listing all argv/env.
- `internal/adapter/{claude,agy,cursor,muse}.go`: doc comments listing their argv flags and env vars. Muse comment records the ingress-gate and session.jsonl findings above.
- `internal/adapter/muse_wake_probe_test.go`: update the `TestMuseWakeProbe` comment with the 1.4.0 result.
- `internal/hook/handler.go`: `codexAnswerText`, comment block on question tools.
- `internal/runtime/native.go`: copy above.
- Skills: the four SKILL.md files above.
- Tests: `internal/adapter/codex_test.go` (argv contains the override), `internal/hook/codex_native_test.go` (fixtures), `internal/install/skills_test.go` / runtime copy tests updated where they pin old copy.

## Verification

1. `go test ./internal/hook/ ./internal/adapter/ ./internal/runtime/ ./internal/install/`
2. `go test ./...`
3. Scenarios: sync fixture "Request changes" + note → row answered, `response_text = "Request changes: use German instead"`, next step names `decision:"request_changes"`; French fixture → "French"; ` (Recommended)` stripped; async fixtures still pass unchanged.

## Out of scope

- Reading Muse `session.jsonl` for observed answers (proposed follow-up).
- Muse native wake.
- Upgrading Codex to 0.158.

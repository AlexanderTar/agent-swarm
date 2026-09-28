# Plan: approval summaries enforced in chat; no request id in the question

Companion to `docs/specs/2026-09-28-approval-summary-enforced.md`. Every task is
a TDD unit: failing test, `go test ./...` shows it fail, minimal implementation,
`go test ./...` passes, commit (explicit paths, never `git add -A`/`--amend`).

## Task 1 — `NormalizeQuestion` and drop the token from question builders

Files: `internal/runtime/native.go`, `internal/runtime/native_test.go`.

- Add `func NormalizeQuestion(s string) string`: `strings.Join(strings.Fields(s), " ")`.
- Replace `truncateWithToken(body, ref string) string` with
  `capRunes(body, 1000)` at every call site (`nativePromptFor`'s
  confirm_repos/close_spike/accept_epic/accept_fix branches, `nativePromptForMsg`).
  Delete `truncateWithToken`.
- `buildApprovalQuestion(summary, paths, approveLine string) string` drops its
  `ref` parameter and the trailing `token`; the 1000-rune budget no longer
  reserves space for it.
- `nativePromptFor` and `nativePromptForMsg` stop appending `refToken(...)`.
- Keep `refToken`, `refFromPrompt`, `HasRefToken`, `refRe` as-is (needed by the
  fallback in Task 2 and by `questionsHaveBatchedSwarmRef`).
- Update every pinned `refToken(...)`-suffixed expectation in
  `native_test.go` / `native_answer_test.go` to the new, token-less text
  (keep the tests; change only the expected string).

Test first: `TestNativePromptForApproveSectionHasNoRefToken` — build a
section-approval request, assert `req.NativePrompt.Question` does NOT contain
`⟦swarm:`. Extend to plan/report/confirm_repos/close_spike/accept_epic/accept_fix
and to `nativePromptForMsg`.

## Task 2 — `BindNativeQuestion`

Files: `internal/runtime/native.go`, `internal/runtime/native_test.go`.

```go
func (s *Store) BindNativeQuestion(ctx context.Context, agentID, question string) (ref string, ok bool)
```

- First check `refFromPrompt(question)`: non-empty means an old in-flight
  session still forwarded a token — bind that ref directly (locked decision 2
  fallback). This also covers `questionsHaveBatchedSwarmRef`'s existing usage
  indirectly (Task 5).
- Otherwise normalize the incoming question with `NormalizeQuestion`. Open a
  read tx (`s.tx`) and collect candidates:
  - every open request for `agentID` whose kind satisfies `nativeAnswerKind`
    (approve_section/plan/report, confirm_repos, close_spike, accept_epic,
    accept_fix): rebuild via `s.requestTx` + `s.storedNativePromptTx`,
    compare `NormalizeQuestion(np.Question)`.
  - every open child-approval message addressed to `agentID`
    (`kind='question' AND json_extract(payload_json,'$.approval')=1`, not yet
    answered — mirror the two `NOT EXISTS` guards `nativeAnswerForMsg` already
    checks: no `approval_result`/`answer` reply, no bound question row in
    `approved`/`changes_requested`): rebuild via `nativePromptForMsg`, compare.
- No candidates: return `"", false`.
- One candidate: return its ref.
- More than one: pick the newest by `created_at`, log
  `s.Log("bind_native_question: %d candidates for agent %s, chose newest %s", ...)`.

Tests (`native_test.go`): exact match binds; reflowed whitespace binds
(`"Approve  the\nplan"` vs `"Approve the plan"`); an altered question doesn't
bind (`ok == false`); an old `⟦swarm:req_X⟧`-suffixed question still binds via
the fallback; two open approvals with the same rebuilt text bind the newer
one; a child-approval message binds by text.

## Task 3 — wire `BindNativeQuestion` into the two binding sites

Files: `internal/runtime/requests.go`.

- `:863` (`askQuestion`): replace `refFromPrompt(in.Prompt)` with
  `s.BindNativeQuestion(ctx, a.ID, in.Prompt)` (already inside the agent's tx
  scope — call it before opening the tx, since `BindNativeQuestion` starts
  its own; or inline the same read logic — prefer calling it outside the
  outer tx and passing the bound ref in, since `IdemTx`'s fn already has `a`
  resolved only inside; simplest: look up the agent id first via a small
  read, or just call `s.BindNativeQuestion` using `a.ID` right after
  `sessionAndAgent` resolves it, before the INSERT).
- `:1377` (`ResolveQuestionReply`): resolve `sessionID`'s `agent_id` first
  (`SELECT agent_id FROM sessions WHERE id = ?`), then
  `ref, ok := s.BindNativeQuestion(ctx, agentID, question)`; `ok==false`
  falls back to `ResolveQuestionByPrompt` exactly as the old `ref == ""`
  branch did.

Tests (`requests_test.go` or `native_test.go`): `TestAskQuestionBindsByTextNoToken`
— forward a native_prompt's `Question` verbatim (no token) through
`AskQuestion`, assert the resulting request's `binding_json.ref` equals the
original approval ref. `TestResolveQuestionReplyBindsByText` similarly for the
codex reply path.

## Task 4 — batched-question guard uses text binding

Files: `internal/hook/handler.go`, `internal/hook/handler_test.go`.

- `questionsHaveBatchedSwarmRef` (pure/no DB) still catches the *old-token*
  fallback case (a stale prompt with a literal token in a batch) — keep it
  as-is, it already does only that.
- Add a second check in the `PreToolUse` batched-guard block: for a batch of
  >=2 questions, if `s.RT.BindNativeQuestion(ctx, agentID, q)` returns
  `ok==true` for any question after the first, deny with the same
  `"[swarm] Ask one swarm approval per question call."` reason (mirrors the
  old token check, now by text). Question `[0]`'s own bind is fine — that's
  the one `AskQuestion` will see.

Test: a 2-question batch where question[1]'s text matches an open approval's
rebuilt prompt is denied.

## Task 5 — `SummaryGate` / `RecordSummaryBlock`

Files: `internal/runtime/native.go`, `internal/runtime/native_test.go`.

```go
func (s *Store) SummaryGate(ctx context.Context, ref string) (summary string, paths []string, blocks int, err error)
func (s *Store) RecordSummaryBlock(ctx context.Context, ref string) error
```

- `SummaryGate`: `req, err := s.RequestByID(ctx, ref)` (or the msg-ref
  equivalent — msg refs never have a stored summary, so `SummaryGate` only
  ever applies to `req_` refs of kind approve_section/plan/report per spec
  decision 3; a `msg_` ref or any other kind returns `summary == ""`, which
  the caller treats as "nothing to enforce"). `summary = req.Prompt`. For
  `KindApprovePlan`, call `s.planReviewPathsTx` (inside a read tx) to get
  `paths = []string{"Spec: "+abs, "Plan: "+abs}` (spec omits the Spec line
  when there is no spec — mirror `planPathsBlock`). `blocks` reads
  `json_extract(binding_json,'$.summary_blocks')` (0 when absent).
- `RecordSummaryBlock`: `UPDATE requests SET binding_json =
  json_set(COALESCE(binding_json,'{}'), '$.summary_blocks',
  COALESCE(json_extract(binding_json,'$.summary_blocks'), 0) + 1) WHERE id = ?`.

Tests: gate on a fresh approve_section request returns its `Prompt` as
summary, 0 blocks; on approve_plan returns paths; `RecordSummaryBlock` twice
then `SummaryGate` reports `blocks == 2`; a `close_spike` ref (no summary)
returns `summary == ""`.

## Task 6 — normalize-for-substring-match helper

Files: `internal/runtime/native.go` (or a new `internal/hook/summarygate.go` —
decide by where `norm` is reused; put it in `runtime` since `SummaryGate`'s
caller and the hook both need it, exported as `runtime.NormForMatch`).

```go
func NormForMatch(s string) string // lowercase, keep only letters+digits (unicode-aware via unicode.IsLetter/IsDigit)
```

Test: `"# Locked Decisions\n\n1. Foo-Bar!"` normalizes the same as
`"locked decisions 1 foobar"` (spaces/punctuation/markdown stripped, case
folded) so a plain quote and a markdown-reformatted quote both match.

## Task 7 — adapter transcript readers

Files: `internal/adapter/claude.go`, `internal/adapter/codex.go`,
`internal/adapter/agy.go`, matching `_test.go` files, plus fixtures under each
package's `testdata/`.

Interface (defined once, in `internal/adapter/adapter.go` or inline per type —
put it in `hook` as a local interface like `observedAnswerer`, since only the
hook needs it and adapters just need to implement the method):

```go
// in internal/hook, next to the Handler
type transcriptTexter interface {
    AssistantTextSinceLastTurn(transcriptPath string) (text string, ok bool)
}
```

Each adapter's method:
- **Claude** (`claude.go`): read the file line by line (JSONL). Track the
  index of the last line where `type == "user"` (a real user turn or a
  tool_result envelope — both reset the boundary, matching
  `advisor.readClaudeLines`'s role handling). Collect `content[].text` for
  every `type == "text"` block in every `type == "assistant"` line after that
  boundary, in order, joined by `"\n"`. `ok == false` only when the file
  can't be opened or every line fails to parse as JSON.
  Fixture: `internal/adapter/testdata/claude/transcript-summary-then-ask.jsonl`
  (assistant text line, then an assistant tool_use line sharing the same
  `message.id`, both after a `type:"user"` line) — copy the real shape from
  `internal/advisor/testdata/claude/transcript-plain-assistant.jsonl` (already
  in the repo) and add a second, synthetic entry with the summary text and a
  tool_use block. `internal/adapter/testdata/claude/transcript-no-summary.jsonl`
  for the deny case.
- **Codex** (`codex.go`): read the rollout JSONL. `type == "response_item"`,
  `payload.type == "message"`. Track the last `role == "user"` message index
  (a `function_call_output` does not reset the boundary — only a real user
  message does, since codex's rollout has no separate tool-result envelope
  type). Collect `payload.content[].text` where `content[].type ==
  "output_text"` (confirmed against `internal/advisor/testdata/codex/rollout-sample.jsonl`,
  which already has exactly this shape — reuse it, do not re-derive) for
  every assistant message after the boundary.
  Fixture: copy `internal/advisor/testdata/codex/rollout-sample.jsonl` into
  `internal/adapter/testdata/codex/rollout-summary.jsonl` and add a
  `request_user_input`/`request_user_input_async` `function_call` after the
  assistant `output_text` line, to exercise the "assistant text immediately
  before the tool call" shape end to end.
- **agy** (`agy.go`): read `transcriptPath` (`transcript_full.jsonl`,
  confirmed live format 2026-09-28: `{"type":"USER_INPUT"|"PLANNER_RESPONSE"|
  "GENERIC"|"SYSTEM_MESSAGE"|"ERROR_MESSAGE"|"CHECKPOINT", "source":"MODEL"|
  "USER_EXPLICIT"|"SYSTEM", "content": "<text>"?, "tool_calls": [...]?}`,
  one JSON object per line, no wrapping array — read live from
  `~/.gemini/antigravity-cli/brain/f3c8dffa-ef8b-430f-8ad9-e283deb2621a/.system_generated/logs/transcript_full.jsonl`,
  read-only, never modified). Track the last `type == "USER_INPUT"` line
  index. Collect `content` from every `type == "PLANNER_RESPONSE"` line after
  that boundary (present whether or not the same line also carries
  `tool_calls` — confirmed live: an `ask_question` tool call can share its
  entry with `thinking` only and no `content`, so the summary text is
  necessarily a separate, earlier `PLANNER_RESPONSE` line in the same turn),
  joined by `"\n"`. `ok == false` only on an unreadable/unparseable file —
  this is a real, now-confirmed format, not the fail-open placeholder the
  spec allowed as a fallback.
  Fixture: `internal/adapter/testdata/agy/transcript-summary-then-ask.jsonl`,
  hand-built from the real shapes above (a `USER_INPUT` line, a
  `PLANNER_RESPONSE` line with sanitized summary-like `content`, a
  `PLANNER_RESPONSE` line with an `ask_question` `tool_calls` entry) — no
  real user content copied in, only the structural shape.

Tests per adapter: summary present → text contains it; no-summary fixture →
text does not contain the marker; unreadable path → `ok == false`.

## Task 8 — PreToolUse enforcement gate

Files: `internal/hook/handler.go`, `internal/hook/handler_test.go`,
`internal/hook/testdata/*` (new fixtures reusing Task 7's adapter fixtures via
`TranscriptPath`).

In `decide`'s `"PreToolUse"` case, insert the gate strictly after the
parented-relay check and the batched guard (Task 4), and strictly *before*
the existing `AskQuestion` intercept (a denied call must return before ever
calling `h.RT.AskQuestion`, or the row is stranded — no `PostToolUse` fires
after a deny):

```go
if isQuestionTool(in.ToolName) && h.RT != nil && s.ID != "" {
    prompt, _ := extractQuestion(in.ToolName, in.RawToolInput)
    if ref, ok := h.RT.BindNativeQuestion(ctx, s.AgentID, prompt); ok {
        if summary, paths, blocks, err := h.RT.SummaryGate(ctx, ref); err == nil && summary != "" {
            if blocks < 2 {
                texter, hasTexter := a.(transcriptTexter)
                text, textOK := "", false
                if hasTexter && in.TranscriptPath != "" {
                    text, textOK = texter.AssistantTextSinceLastTurn(in.TranscriptPath)
                }
                if !textOK {
                    h.logf("hook: summary gate for %s: transcript unreadable, allowing", ref)
                } else if !strings.Contains(runtime.NormForMatch(text), runtime.NormForMatch(summary)) ||
                    !allPathsPresent(text, paths) {
                    if err := h.RT.RecordSummaryBlock(ctx, ref); err != nil {
                        h.logf("hook: record summary block for %s: %v", ref, err)
                    }
                    return adapter.HookDecision{Block: true, Reason: summaryGateDenyReason(summary, paths)}, nil
                }
            }
        }
    }
}
```

(`allPathsPresent` checks each `paths` line's raw text, not normalized, is a
substring — the exact-path plan check; empty `paths` always passes.)

`summaryGateDenyReason(summary string, paths []string) string` builds the
exact copy from the spec's "User-facing copy" section, appending each `paths`
line.

Tests (`handler_test.go`, using a fake `transcriptTexter`-implementing
`adapter.Fake` or a small local stub registered in `h.Adapters`): no summary
in transcript → `Block: true`, reason contains the summary verbatim; summary
present reformatted as markdown → not blocked; plan with paths missing →
blocked; third attempt after 2 recorded blocks → allowed regardless of
transcript content; unreadable transcript → allowed. Also a live-shaped
regression test reusing the real Claude/Codex fixtures from Task 7 wired
through `in.TranscriptPath`.

## Task 9 — Muse observed-answer matches by text

Files: `internal/adapter/muse.go`, `internal/runtime/native.go`
(`observedAnswerer`), `internal/runtime/native_answer_test.go`,
`internal/adapter/muse_test.go`.

- `ObservedAnswer(providerSessionID, ref, question string, since time.Time) (label, note string, ok bool)`:
  add the `question` parameter. Muse's session-log scan matches an entry
  whose recorded question, `NormalizeQuestion`'d, equals `NormalizeQuestion(question)`,
  instead of (or in addition to, for old rows) the literal ref token embedded
  in the question text. Keep the ref-token match as a fallback branch so
  sessions already carrying a token in their Muse session log still resolve.
- `nativeAnswer` (native.go) must build `question` before calling
  `ObservedAnswer`: for a `req_` ref, `s.storedNativePromptTx`'s `.Question`
  (needs a tx — open one, read-only, ahead of the existing pre-tx Muse block);
  for a `msg_` ref, `nativePromptForMsg`'s `.Question`. This still runs before
  the state-changing tx (I2 comment preserved).
- Update the `observedAnswerer` interface and its test fake
  (`native_answer_test.go`'s stub) to the new signature.

Tests: a Muse session log entry with the plain rebuilt question text (no
token) is observed; an old token-suffixed entry still matches via fallback;
mismatched text does not match.

## Task 10 — full-suite verification

1. `gofmt -l .` clean.
2. `go vet ./...` clean.
3. `go test ./...` clean.
4. Re-read the spec's Verification section scenarios and confirm each has a
   passing test (cross-reference against Tasks 1-9's test lists above).

No `scripts/e2e.sh`, no push, no merge.

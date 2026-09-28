# Spec: approval summaries are enforced in chat; no request id in the question

## Context

**Problem:** the user reported (2026-09-28) that spike and spec approvals ask before showing
the section summary, so the user must approve blind or dig the summary out through the CLI.
In the live example, orchestrator `typesafe-model-runs-classification` (Claude) asked:
- section 1: "Starting with Section 1: locked decisions";
- section 2: a one-sentence paraphrase instead of the stored summary.

**Cause:** printing the summary is a skill instruction only, and models skip it.

**Previous fix** (branch `fix/summary-in-native-question`, merged before this work):
- the summary goes in chat, up to 2000 characters;
- the native question carries only a short head of the summary;
- the skill copy is updated.

**This spec** adds two things:
1. Enforcement: the question tool is blocked until the full summary is in the agent's chat
   output.
2. The user asked: "don't show request ID in the ask prompt, it's implementation details".
   The `⟦swarm:req_…⟧` token disappears from every native question. Binding moves to an
   exact question-text match.

**Probes** (2026-09-28):
- **Claude 2.1.283:** at PreToolUse(AskUserQuestion), `transcript_path` already contains the
  assistant text block written just before the tool call. The tool_use shares the same message
  id, and the text block is on the preceding line.
- **Codex 0.157:** the rollout (`transcript_path`) records a `response_item`
  `message`/`assistant` before the `function_call` `request_user_input`.
- **agy:** PreToolUse carries `transcriptPath` (`…/.system_generated/logs/transcript_full.jsonl`).
  Its format and timing are unprobed. The check fails open when it can't read or parse the file.
- **Cursor and Muse:** no PreToolUse fires for their question tools, so there is no enforcement.
  They keep the instruction plus a longer head (600 runes) in the question. Muse's answer is
  verified from its `session.jsonl`.

## Locked decisions

1. **No token.** Native questions contain no `⟦swarm:…⟧` token.
   - `nativePromptFor`, `nativePromptForMsg` and the stored-prompt rebuild emit the question
     without it.
   - `truncateWithToken` becomes a plain rune cap (1000).
2. **Binding by question text.** When a native question tool is called (PreToolUse, or the
   Codex async reply, or the Muse session log), the question text is normalized: trimmed, with
   runs of whitespace collapsed to one space. It is compared to the normalized native question
   of each **open** approval request (and open child-approval message) routed to the calling
   agent.
   - Exactly one match: bind that ref, as the token did.
   - No match: a plain question, as today.
   - More than one match: bind the newest and log it.
   - Old in-flight rows that still carry a token: the existing token path stays as a fallback,
     so sessions launched before the deploy keep binding.
3. **Enforcement** (Claude, Codex, agy), on PreToolUse of a question tool whose question binds
   to an approval request that has a summary (`approve_section`, `approve_plan`,
   `approve_report`):
   - Read the transcript and collect the assistant text written since the last user message or
     tool result that preceded this call.
   - Pass if `norm(summary)` is a substring of `norm(assistant text)`. For a plan, both review
     paths must also be present. `norm` lowercases and drops everything except letters and
     digits, so markdown reformatting and wrapping don't matter.
   - Fail: deny the tool with this reason (the exact copy below). Count the denials per request,
     in the request's `binding_json` (`summary_blocks`). After 2 denials for the same request,
     allow and log it, so an approval can never deadlock.
   - The transcript is unreadable, unparseable or missing: allow (fail open) and log it.
4. The summary cap stays 2000 (from the previous fix).

## DB models

None. `requests.binding_json` gains `summary_blocks` (int).

## Model / API types

- `runtime`: `func NormalizeQuestion(s string) string` and
  `func (s *Store) BindNativeQuestion(ctx, agentID, question string) (ref string, ok bool)`. The
  hook calls the latter in place of the `HasRefToken`/`refFromPrompt` paths
  (`internal/runtime/requests.go:863`, `:1377`; `internal/hook/handler.go:127`).
- `runtime`: `func (s *Store) SummaryGate(ctx, ref string) (summary string, paths []string, blocks int, err error)`
  and `RecordSummaryBlock(ctx, ref)`.
- `adapter`: per kind, `AssistantTextSinceLastTurn(transcriptPath string) (string, bool)`:
  - Claude: JSONL with `message.role == "assistant"` and `content[].type == "text"`.
  - Codex: rollout `response_item` with `payload.type == "message"`, `role == "assistant"`
    (`content[].text`).
  - agy: best-effort parse. If the format is unknown, return `false` so the check fails open.
- Muse `ObservedAnswer` matches the prompt by normalized question text instead of the token.
  The token stays as a fallback for old rows.

## User-facing copy

Deny reason (Claude/Codex/agy PreToolUse):

```
[swarm] Print this approval's summary in chat first, verbatim and complete (markdown is fine), then ask again with the same question.

<summary>
```

For a plan, add these lines after the summary:

```
Spec: <abs path>
Plan: <abs path>
```

## Screens

The native question shows no id. Example:

```
Jev becomes the primary gateway classifier…

Approve Spec section "Locked decisions" (rev 1)?
```

## File list

- `internal/runtime/native.go`: prompt builders, the rebuild, binding helpers, the gate.
- `internal/runtime/requests.go`: `:863` and `:1377` binding.
- `internal/hook/handler.go`: the PreToolUse gate, binding, the batched guard.
- `internal/adapter/{claude,codex,agy}.go`: transcript readers.
- `internal/adapter/muse.go`: `ObservedAnswer` matching.
- Tests beside each.
- Fixtures: a Claude transcript with and without the summary; a Codex rollout; an agy
  transcript (if the format can be read from a live file, else a fail-open test).

## Verification

1. `gofmt -l .`, `go vet ./...`, `go test ./...`
2. Scenarios, each a test:
   - The question has no token, for every approval kind.
   - Binding by exact text; reflowed whitespace still binds; an altered text doesn't bind; an old token question still binds.
   - Claude transcript without the summary → deny with the exact copy; with the summary reformatted as markdown → allow; plan without paths → deny.
   - The third attempt after 2 denials is allowed; an unreadable transcript is allowed.
   - The Codex reply wrapper binds by question text.
   - Muse observed answer binds by question text.
   - A child-approval (`msg_`) question binds by text.

## Explicitly out of scope

- Cursor and Muse enforcement (no hook exists).
- Showing summaries on the board.

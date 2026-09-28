# Spec: the daemon builds the approval chat block

## Context

**Problem (live, 2026-09-28):** before a native approval question the agent must print the
approval summary in chat. The PreToolUse summary gate (`internal/hook/handler.go`,
`SummaryGate`/`RecordSummaryBlock`, `summaryGateDenyReason`) denies the question until
`norm(summary)` appears in the assistant text since the last turn, and allows after 2 denials.

- A Claude orchestrator paraphrased instead ("I'll run through the verification plan: …").
  Every question was denied twice, so the user saw two red
  `PreToolUse:AskUserQuestion hook error: [swarm] Print this approval's summary…` blocks per
  approval.
- The summaries themselves were run-on paragraphs (arrow chains `a → b → c`, semicolons).
- A probe showed hook `systemMessage` can't carry the summary: Claude shows it only after the
  dialog closes and agy rejects it. The agent must print it.

**This spec:** the daemon builds the exact chat message (`chat_block`) and hands it to the agent
next to every approval `native_prompt`. The gate's deny reason repeats that block. `swarm_ask`
refuses long single-line summaries, and the skills teach a structured brief.

Worktree: `agent-swarm-gate-handoff`, branch `fix/gate-and-handoff-on-question` (shares the
branch with the pause-dismisses-dialog fix in `pause.go`, untouched here).

## Locked decisions

1. **One builder.** `runtime.ApprovalChatBlock` formats every chat block. All emitters and the
   gate use it, through one store helper that gathers its inputs.
2. **Formats** (exact):
   - Spec section:
     ```
     ### Approval <N> of <M> · Spec section "<Section title>" (rev <R>)

     <summary verbatim>

     Full section: <absolute spec path> → "## <Section title>"
     ```
     N is the section's 1-based position among the revision's required sections
     (`RequiredSpecSection`, document order); M is their count. Unknown position: the header is
     `### Approval · Spec section "<title>" (rev <R>)`.
   - Plan: `### Approval · Plan (rev <R>)`, blank line, summary, blank line, then
     `Spec: <abs path>` (omitted for a legacy chore spike with no spec) and `Plan: <abs path>`.
   - Debug report: `### Approval · Debug report (rev <R>)`, blank line, summary, blank line,
     `Report: <abs path>`.
   - Child approval (`native_prompt` with `for_msg`): `### Approval · <child> asks`, blank line,
     the message body in full. Not gate-enforced (a `msg_` ref has no stored summary).
   - Kinds with no summary (`confirm_repos`, `close_spike`, `accept_*`) get no `chat_block`.
3. **Gate pass condition unchanged:** `norm(summary)` substring plus every plan path, 2-denial
   allow, fail open on an unreadable transcript. Only the deny copy changes.
4. **Summary shape check:** an approval `prompt` longer than 300 runes with no newline is
   refused. The 1–2000 cap is unchanged and checked first.
5. Section N/M and paths are read from the request's own `artifact_revision`, so a replay shows
   the same block as the first ask.

## DB models

None. `chat_block` is computed on every emit, never stored.

## Model / API types

```go
// internal/runtime
type ChatBlockInput struct {
	Kind         string       // KindApproveSection | KindApprovePlan | KindApproveReport, or "" for a child message
	Revision     int
	Summary      string
	SectionTitle string
	N, M         int          // 0 = position unknown
	Path         string       // spec (section) or report absolute path
	Paths        *ReviewPaths // plan
	Child        string       // child agent name (Kind == "")
}
func ApprovalChatBlock(in ChatBlockInput) string
func (s *Store) approvalChatBlockTx(ctx context.Context, tx *sql.Tx, req Request) (string, error)
func (s *Store) SummaryGate(ctx context.Context, ref string) (summary string, paths []ReviewPathLine, chatBlock string, blocks int, err error)

// Request gains an in-process field (never persisted):
ChatBlock string
```

Wire:
- `swarm_ask` approval result: `{"request_id","state","native_prompt","review_paths"?,"chat_block","next"}`.
- `swarm_ask kind:"native_prompt"` with `for_msg`: adds `chat_block`.
- `request_open` relay for `approve_section`/`approve_plan`/`approve_report`: adds `chat_block`
  (keeps `summary`, `review_paths`).

## User-facing copy

- Gate deny reason:
  ```
  [swarm] Your chat message must be the block below, copied exactly — not a summary or paraphrase. Print it, then call the question tool again with the same question.

  <chat_block>
  ```
- `swarm_ask` refusal: `Summary must be a lead sentence plus bullets (see swarm-orchestrator: approval summaries).`
- `next` (every native prompt): print `chat_block`, when present, exactly as the whole chat
  message immediately before the native question; never restate, shorten or paraphrase it.
- Skills (`swarm-orchestrator`, `swarm-spike`): an "Approval summaries" rule (lead sentence,
  6–12 bullets of ≤25 words, sub-bullets, keep tables/sketches, ≤2000 characters, no arrow
  chains, no semicolon lists, no agent jargon), a verbatim example brief, and the presentation
  rule for `chat_block`.

## File list

- `internal/runtime/native.go`: `ApprovalChatBlock`, `approvalChatBlockTx`, `SummaryGate`,
  `NativePromptNextStep`, for_msg result.
- `internal/runtime/requests.go`: `askApproval` validation and `ChatBlock`; `relayRequestTx`.
- `internal/runtime/model.go`: `Request.ChatBlock`.
- `internal/mcpserver/tools.go`: `requestOut` emits `chat_block`; `swarm_ask` description.
- `internal/hook/handler.go`: deny reason.
- `skills/swarm-orchestrator/SKILL.md`, `skills/swarm-spike/SKILL.md` and the
  `internal/install/skills/` mirror.
- Tests next to each.

## Verification

1. `go test ./internal/runtime/ -run ApprovalChatBlock` (section N/M, unknown N/M, plan, plan
   without spec, report, child).
2. Runtime/mcpserver tests: `chat_block` in the approval result, `request_open` payload and the
   for_msg result.
3. Hook test: deny reason text equals the copy above with the built block.
4. `swarm_ask`: a 301-rune single line is refused with the exact copy; the same text with a
   newline is accepted.
5. Skill mirror test passes after `make skills-sync`.
6. `gofmt -l . && go vet ./... && go test ./...`.

## Explicitly out of scope

- Changing the gate's pass condition or the 2-denial allow.
- Showing the block via hook `systemMessage` (probed; unreliable).
- Any web or menubar change.
- Enforcing the chat print for Cursor/Muse (no hookable question tool).

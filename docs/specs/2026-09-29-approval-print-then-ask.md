# Spec: print the approval block first, ask in a separate turn

## Context

**Problem (user report, 2026-09-29, screenshot):** every orchestrator kind is told to print
`chat_block` in chat right before the native question tool. They paraphrase it into one line
instead. On Claude/Codex/agy the PreToolUse summary gate denies twice, then lets the question
through (`internal/hook/handler.go` `SummaryGate`/`RecordSummaryBlock`), so the user still
approves blind. The user needs the full markdown block in chat before the question, on every
kind (claude, codex, agy, cursor, muse). Putting it inside the question dialog is rejected (not
enough space).

**Change:** split an approval into two turns. When a request with a `chat_block` opens, the
agent gets only `chat_block` plus "end your turn with it". The daemon checks the finished reply
against the block, then sends the native question in a new relay. The PreToolUse gate goes.

Emitters of `chat_block` today, all in scope:
- `swarm_ask kind:"approval"` result (`askApproval`, `internal/runtime/requests.go:1313`;
  `requestOut`, `internal/mcpserver/tools.go:246`): section, plan, debug report.
- `request_open` relay (`relayRequestTx`, `requests.go:509`): the same three, plus
  `accept_epic`/`accept_fix` (finish, `docs/specs/2026-09-29-finish-with-pr.md`), on first
  routing and every resurface.
- `swarm_ask kind:"native_prompt", for_msg` (`askNativePromptForMsg`, `native.go:1590`): a child's
  approval question. It has no request row, only the child's `question` message.

Kinds without `chat_block` (`confirm_repos`, `close_spike`, `question`, `blocker`) are unchanged.

Worktree `agent-swarm-print-then-ask`, branch `fix/approval-print-then-ask` off `main` (d8bc237).
Collision warning: the unmerged board-handoff branch uses migration `0023`. This spec needs no
migration (main's head is `0022`).

## Locked decisions

1. **Print step.** Every `chat_block` emitter returns `chat_block` and `next` = `PrintNext`
   (copy below). It returns no `native_prompt` and no `question`. The runtime still builds and
   freezes the native prompt at the same moment as today (`freezeNativeQuestionTx`), so the
   question text and binding don't change. Only the wire output omits it.
2. **Phase state** lives beside the ref:
   - request refs: `requests.binding_json`;
   - `msg_` refs: `messages.payload_json` of the child's question.

   Keys: `print_phase` (`"print"` or `"ask"`), `print_attempts` (int, failed checks so far) and
   `print_at` (ms, when the current print instruction was issued). `startPrintTx` sets
   `print`/0/now on every emit.
3. **Turn-end signal, per kind:**

   | kind | signal | reply source | no text |
   |---|---|---|---|
   | claude | `Stop` hook | `Claude.AssistantTextSinceLastTurn(transcript_path)` | fail open |
   | codex | `Stop` hook | `Codex.AssistantTextSinceLastTurn(transcript_path)` (rollout) | fail open |
   | agy | `Stop` hook | `Agy.AssistantTextSinceLastTurn(transcriptPath)` | fail open |
   | cursor | `stop` hook | none: **trusted** | n/a |
   | muse | daemon tick (no hooks): pane `Idle` and a new `assistant_message_committed` | `Muse.LastReply(providerSessionID, print_at)` | wait |

   "Fail open" means an empty `transcript_path`, or a reader that returns `ok=false`: send the
   ask.
4. **Verification** on turn end, for each of the agent's refs in `print` phase:
   - **Delivered guard.** A ref with any unacked relay (`request_id` = ref, or
     `correlation_id` = ref for `msg_`) is skipped: the agent hasn't seen its instruction.
     `swarm_ask` and `for_msg` results are delivered by construction.
   - **Pass:** `NormForMatch(reply)` contains `NormForMatch(chat_block)`, the same `norm` as the
     old gate. The block holds the review paths, so they are checked too. Also pass: a trusted
     kind, or an unreadable reply. On pass, set `print_phase="ask"` and enqueue `request_ask`.
   - **Fail with `print_attempts == 0`:** set it to 1, reset `print_at`, enqueue `request_print`.
   - **Fail with `print_attempts >= 1`:** log, set `ask`, enqueue `request_ask`. That is 2
     failed prints, then ask anyway, so an approval can never deadlock.
5. **Wake.**
   - Hooked kinds: the `Stop` handler runs the check after the pausing guard and before the
     pending-inbox check. If it enqueued anything, it blocks `Stop` with a freshly rendered
     `InboxNotice`: Claude/Codex `decision:block`, agy `continue`, Cursor `followup_message`.
     This block doesn't touch `stop_blocks`/`maxStopBlocks`, because `print_attempts` already
     bounds it.
   - Muse: the relay is `immediate`, so `WakeDue` pastes as for any relay.
6. **Removal:** `SummaryGate`, `RecordSummaryBlock`, `ReviewPathLine`, `allPathsPresent`,
   `summaryGateDenyReason`, the gate block in PreToolUse and the `summary_blocks` key are
   deleted. `transcriptTexter` stays (Stop uses it). PreToolUse still binds the question
   (`BindNativeQuestion`, `AskQuestionBoundTo`) and still refuses batched swarm refs.
7. **Handoff and resurface restart at print.** `resurfaceOpenRequests` with `fresh=true` first
   retires the unacked `request_ask`/`request_print` relays of the agent's open requests (sets
   `state='acked'`), then relays `request_open` as today, which calls `startPrintTx`. Same-session
   wakes keep today's visibility rules.
8. **UI approval mid-print.** `resolve()` ignores the phase. It also retires unacked
   `request_ask`/`request_print` relays for that id. The turn-end query only selects
   `state='open'` requests. It only selects `msg_` refs that nobody has answered yet (the
   `notifyUnansweredQuestions` NOT EXISTS predicate, `reconcile.go:1775`).
9. **Several refs in print at once** (a resurface after a restart) are judged independently
   against the same reply.
10. **Skills.** The `swarm-orchestrator` rule is replaced by one short rule, and the conflicting
    lines in `swarm-spike` are rewritten. `skills/swarm` is unchanged: child agents never get
    approvals, their parent does.

## DB models

No migration. JSON keys only:

- `requests.binding_json`: `+ print_phase TEXT`, `+ print_attempts INT`, `+ print_at INT (ms)`.
  `summary_blocks` is no longer read or written. Old rows keep it harmlessly.
- `messages.payload_json` (the child's `kind='question'`, `approval=1` row): the same three keys.

Rows issued before the deploy have no `print_phase`. They are never selected, so an in-flight
approval behaves as before. Their next resurface starts them at print.

## Model / API types

```go
// internal/runtime/print.go (new)
const PrintNext = "End your turn now. Your whole reply is chat_block, verbatim: nothing before or after it, not restated, shortened or paraphrased. Swarm sends you the question next."
const ReprintNext = "Your last reply must be chat_block, verbatim. Reply with it and end your turn. Swarm sends you the question next."

type TurnReply struct {
	Text     string
	Readable bool // false: transcript missing/unparseable -> fail open
	Trusted  bool // kind has no reader (cursor) -> pass
}
// PrintTurnEnded judges every print-phase ref of the session's agent (decision 4) and
// enqueues request_ask / request_print relays. sent reports whether anything was enqueued.
func (s *Store) PrintTurnEnded(ctx context.Context, sessionID string, r TurnReply) (sent bool, err error)
// PrintTurnTick is the no-hook path: for each live session whose adapter implements
// turnReplyReader and that has print-phase refs and no pending messages, if the pane is
// Idle and LastReply(providerSessionID, print_at) found a reply, call PrintTurnEnded.
func (s *Store) PrintTurnTick(ctx context.Context) error
type turnReplyReader interface {
	LastReply(providerSessionID string, since time.Time) (text string, found, readable bool)
}
func (s *Store) startPrintTx(ctx context.Context, tx *sql.Tx, ref string) error
func (s *Store) retirePrintRelaysTx(ctx context.Context, tx *sql.Tx, requestID string) error
func (s *Store) sendAskTx(ctx context.Context, tx *sql.Tx, ref string) error    // request_ask
func (s *Store) sendReprintTx(ctx context.Context, tx *sql.Tx, ref string) error // request_print

// NativePromptNextStep: copy changes (below); signature unchanged.

// internal/adapter/muse.go
func (m *Muse) LastReply(providerSessionID string, since time.Time) (text string, found, readable bool)
// Fake gains LastReplyText/LastReplyFound/LastReplyReadable and LastReply.
```

`WakeLoop` calls `PrintTurnTick` after `WakeDue` every tick and logs its errors the same way.

`Muse.LastReply` reads the one dated `session.jsonl`, found with the same glob as
`ObservedAnswer`. It keeps records with `payload.event.kind == "assistant_message_committed"`
and `recorded_at >= since`, and returns `event.text` of the newest `payload.run_id`'s records
joined by `"\n"`. `readable=false` means the glob doesn't match exactly one file, or a read
error. `found=false` means there is no such record yet (the tick waits).
`ponytail:` a Muse agent that goes idle without writing any text stays in print. The board
still resolves it. Add a timeout if this is ever seen.

**Wire.** Go marshals JSON keys sorted; the fields below are listed in logical order.
- `swarm_ask kind:"approval"` result: `{"request_id","state","chat_block","review_paths"?,"next":PrintNext}`.
  `requestOut` omits `native_prompt` whenever `ChatBlock != ""`.
- `swarm_ask kind:"native_prompt", for_msg`: `{"request_id":<msg_id>,"state":"open","chat_block","next":PrintNext}`.
- `request_open` relay, chat_block kinds: `{event,agent,item,request_id,kind,chat_block,summary?,review_paths?,next:PrintNext}`.
  It drops `question` and `native_prompt`. The relay for other kinds is unchanged.
- `request_print` relay (new): `{event:"request_print",agent,item,request_id,kind,chat_block,attempt:2,next:ReprintNext}`.
- `request_ask` relay (new): `{event:"request_ask",agent,item,request_id,kind,question,native_prompt,review_paths?,next:NativePromptNextStep(...)}`.
  `native_prompt` is the frozen one (`storedNativePromptTx`).
- `msg_` refs: `kind:"child_approval"`, `request_id`=msg id in the payload. The message row uses
  `correlation_id`=msg id, because `messages.request_id` is an FK to `requests`.

## Screens

Chat transcript on every kind (Claude shown):

```
● swarm_ask(approval, section "Verification")
  └ {"chat_block": "### Approval 2 of 5 · …", "next": "End your turn now. …"}

● ### Approval 2 of 5 · Spec section "Verification" (rev 1)      <- whole reply, verbatim

  Verification: how we prove it.
  - `go test ./...`
  …
  Full section: /Users/…/spec.md → "## Verification"

  ⎿ Stop hook: [swarm] You have 1 new message … call swarm_sync   <- existing InboxNotice

● swarm_sync → relay request_ask {native_prompt, next: "Ask this now with …"}
● AskUserQuestion  ┌ Approve Spec section "Verification" (rev 1)? ┐
                   │ ○ Approve   ○ Request changes               │
```

A paraphrased reply gets one more turn: `request_print` → the agent replies with the block →
`request_ask`. On Cursor the ask arrives as a `followup_message`; on Muse as a pasted wake.
Not on screen: no deny errors, and no request id in the question.

## Copy

- `PrintNext`, `ReprintNext`: exact strings in the API types above.
- `NativePromptNextStep` (rewritten). It is used by the `request_ask` relay and by the
  `confirm_repos`/`close_spike` results and relays:
  ```
  Ask this now with your native question tool: show native_prompt verbatim, one question per call, no added text, and don't print chat_block again. Once the user answers, call swarm_ask kind:"native_answer", ref:"<ref>", decision:<"a"|"b"> forwarding only what the user picked. <the rest unchanged from today: hooked kinds, Cursor answer_text, Muse answer_text, cancellation, Codex request_user_input sentence>
  ```
  It drops the old sentences "If chat_block is present, print it exactly …" and "The native
  question shows only a short head of the summary. Then show native_prompt with your native
  question tool now (…)".
- Daemon log lines (`s.logf`):
  - `print: %s reply matches chat_block, asking`
  - `print: %s reply is missing chat_block (attempt 1), re-sending the block`
  - `print: %s reply is missing chat_block after 2 attempts, asking anyway`
  - `print: %s reply unreadable, asking`
  - `print: %s %s has no transcript reader, trusting the reply`
- `swarm_ask` tool description (`tools.go:189`), replacing "The result carries chat_block: print it exactly … short head of the summary." and "…print it the same way.":
  `The result carries chat_block and next: end your turn with chat_block as your whole reply; Swarm then sends the native question in a request_ask relay. For plan and debug report approval, chat_block already includes the review paths.`
- Skill rule (`swarm-orchestrator`, Approval summaries → Presentation, replacing the paragraph at line 115):
  `When next tells you to end your turn with a block, reply with exactly that block and nothing else, then stop. Ask the question only when Swarm sends it (a request_ask relay with native_prompt).`
- `swarm-orchestrator` line 45, for_msg: replace "print the returned `chat_block` exactly as your whole chat message, then show the returned `native_prompt` verbatim" with "follow its `next` (reply with `chat_block`, end your turn); when the `request_ask` relay arrives, show its `native_prompt` verbatim".
- `swarm-orchestrator` line 46, add after "Follow `next`.": `A request_print relay repeats a block you didn't reply with verbatim; a request_ask relay carries the native_prompt to ask now.`
- `swarm-orchestrator` line 54: replace "Print `chat_block` exactly, show `native_prompt` …" with "Follow `next`: reply with `chat_block` and end your turn; the `request_ask` relay then carries `native_prompt` (options and descriptions verbatim)."
- `swarm-orchestrator` line 89: replace "Print the result's or relay's `chat_block` first, as described in Approval summaries below." with "The native_prompt arrives in a `request_ask` relay after you reply with `chat_block` (Approval summaries below)."
- `swarm-spike` steps 4 and 7: replace "Immediately before each native question, including a replay, print the returned `chat_block` exactly as your whole chat message, never restated, shortened or paraphrased." (step 7: "…(it already holds both review paths)") with "Reply with the returned `chat_block` and end your turn; ask when Swarm sends `request_ask`."

## File list

Changed:
- `internal/runtime/print.go` (new): the constants, `TurnReply`, `PrintTurnEnded`,
  `PrintTurnTick`, `startPrintTx`, `retirePrintRelaysTx`, `sendAskTx`, `sendReprintTx`.
- `internal/runtime/requests.go`:
  - `askApproval`: `startPrintTx`;
  - `relayRequestTx`: print payload plus `startPrintTx`;
  - `resurfaceOpenRequests`: retire on fresh;
  - `resolve`: retire.
- `internal/runtime/native.go`:
  - `askNativePromptForMsg`: `startPrintTx`;
  - `NativePromptNextStep` copy;
  - delete `SummaryGate`, `RecordSummaryBlock` and `ReviewPathLine`.
- `internal/runtime/wake.go`: `WakeLoop` calls `PrintTurnTick`.
- `internal/hook/handler.go`: Stop check; PreToolUse gate removed; `allPathsPresent` and
  `summaryGateDenyReason` deleted.
- `internal/mcpserver/tools.go`: `requestOut` print output; `swarm_ask` description.
- `internal/adapter/muse.go` (`LastReply`), `internal/adapter/fake.go` (stub).
- `skills/swarm-orchestrator/SKILL.md`, `skills/swarm-spike/SKILL.md`, and the
  `internal/install/skills/` mirror (`make skills-sync`).
- New fixture `internal/adapter/testdata/muse/session-assistant-reply.jsonl`.

Reused unchanged: `ApprovalChatBlock`, `approvalChatBlockTx`, `NormForMatch`, the three
`AssistantTextSinceLastTurn` readers, `storedNativePromptTx`, `BindNativeQuestion`, `InboxNotice`.

Tests updated (the behavior still exists, the wire changed):
- `TestAskApprovalReturnsChatBlock`: drop the `SummaryGate` assertion; assert `print_phase`.
- `TestFinishChatBlock`: drop the `SummaryGate` line; `next == PrintNext`; `native_prompt` absent.
- `TestNativePromptForMsgReturnsChatBlock`: `print_phase` on the message.
- `TestAskApprovalResultHasChatBlock` (mcpserver): `next == runtime.PrintNext`; no `native_prompt`.
- `TestNativePromptNextStepDescribesVisibleReviewAndAgentReportedAnswers`, `TestFinishNextStepDecisions`: the new copy.
- These decode `native_prompt` from a relay, so they call `passPrint` (a trusted
  `PrintTurnEnded`) first and read the `request_ask` relay:
  - `TestAcceptRowRoutesToLiveRootOrchestrator`
  - `TestAcceptRelayIsNotHeldWhileExhausted`
  - `TestFinishPromptFreezesOptionsAndReplays`
  - `TestFinishNativeAnswer`
  - `TestResumeResurfacesOpenRequests`
  - `TestChoreEndToEndAcceptFix`
  - `TestPlanApprovalCarriesFullReviewPaths`
- `TestPreToolUseSummaryGateDeniesWithoutTheSummaryInChat` is ported and renamed
  `TestPreToolUseAllowsAnApprovalQuestionWithoutAGate`. It checks that the same input is now
  allowed and records one bound question row.

Tests deleted with this reason: the PreToolUse gate was removed on purpose (decision 6), so
the behavior they cover no longer exists.
- `TestPreToolUseSummaryGateAllowsAMarkdownReformattedSummary`
- `TestPreToolUseSummaryGateAllowsAfterTwoDenials`
- `TestPreToolUseSummaryGateAllowsWhenTranscriptUnreadable`
- `TestPreToolUseSummaryGatePlanDeniesWithoutPaths`
- `TestPreToolUseSummaryGatePlanAllowsWithSummaryAndPaths`
- `TestPreToolUseSummaryGatePlanAllowsMarkdownWrappedPaths`
- `TestPreToolUseSummaryGateAllowsThePrintedChatBlock`
- `TestAllPathsPresentMatchesBarePathNotLabeledLine`
- `TestSummaryGate` (runtime)

Their norm, unreadable and plan-path cases move to the new print tests below.

## Verification

Commands, in order: `gofmt -l .` (empty), `go vet ./...`, `go test ./internal/runtime/
./internal/hook/ ./internal/mcpserver/ ./internal/adapter/`, `make skills-sync && git diff
--exit-code internal/install/skills`, then `go test ./...`.

Scenarios, each a test:
1. **Print step:**
   - a section approval result has `chat_block` and `PrintNext`, no `native_prompt`;
   - `print_phase="print"`, `print_attempts=0`.
2. **Pass:**
   - a Claude `Stop` whose transcript holds the block reformatted as markdown enqueues one
     `request_ask`;
   - its `native_prompt` equals the frozen one;
   - `Stop` is blocked with the `InboxNotice`, and `stop_blocks` is still 0.
3. **Paraphrase, retry, pass:**
   - a one-line paraphrase enqueues `request_print` (`attempt:2`, `ReprintNext`) and sets
     `attempts=1`;
   - the next `Stop` with the block enqueues `request_ask`.
4. **2 failures, then ask anyway:** a second paraphrase enqueues `request_ask` and logs
   "after 2 attempts".
5. **Unreadable transcript:** a missing `transcript_path` or file enqueues `request_ask` at
   once.
6. **Cursor trusted:** a `stop` with any text enqueues `request_ask`; the output is a
   `followup_message`.
7. **Muse tick (Fake adapter):**
   - pane idle and `found=false`: nothing happens;
   - `found=true` with the block: `request_ask`;
   - `readable=false`: `request_ask`.
   - Plus a `Muse.LastReply` fixture test: the newest run's text, filtered by `since`.
8. **Delivered guard:** a `Stop` while the `request_open` relay is still pending judges nothing
   (`attempts` stays 0).
9. **UI approval mid-print:**
   - `Approve` from the board resolves the request in `print` phase;
   - a later `Stop` enqueues nothing;
   - a pending `request_print` is retired.
10. **Handoff mid-ask:** a request in `ask` phase with a pending `request_ask` is resurfaced
    (`fresh=true`). The `request_ask` is retired, and a new `request_open` with `chat_block`
    resets `print_phase="print"`.
11. **Finish request:**
    - `accept_fix` routed to the orchestrator gets a print-step `request_open`;
    - after the pass, `request_ask` carries the finish options and descriptions;
    - `native_answer auto_merge` resolves it.
12. **Child approval (`for_msg`):**
    - the result has no `native_prompt`;
    - on pass, `request_ask` has `request_id`=msg id and `correlation_id`=msg id;
    - a child already answered directly is skipped.
13. **No gate:** a PreToolUse `AskUserQuestion` binding an open approval is allowed with no
    chat text.

## Explicitly out of scope

- Reading `last_assistant_message` from Stop payloads instead of the transcript. A Stop that
  fires before the transcript is flushed costs at most one needless `request_print`.
- Showing the block inside the question dialog (rejected), or through hook `systemMessage`
  (probed earlier, unreliable).
- A Muse no-text timeout (the `ponytail:` ceiling above).
- Board, web and menubar changes. Changes to the summary shape rules or the 300/2000 limits.
- Enforcing anything for `confirm_repos`, `close_spike`, `question`, `blocker`.

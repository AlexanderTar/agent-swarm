# Epic approvals go through the approval lane

Date: 2026-09-26. Branch: `feat/epic-approval-lane`. Worktree: `/Users/alexandertar/GitHub/agent-swarm-epic-approvals`.
Companion plan: `docs/plans/2026-09-26-epic-approval-lane.md`.

## Context

### Problem

The daemon's reconciler opens `accept_epic` / `accept_fix` itself (`internal/items/transition.go` `reconcileRoot`, lines 546-649). The row has no `agent_id`, no `session_id` and no native prompt. `OnRequestOpened` (`internal/runtime/requests.go` ~line 440, wired at `cmd/swarm/daemon.go:285`) only raises the notification. So:

- `approvalTerminalKinds` (`requests.go:265`) leaves accept kinds out. `nativeAnswer` refuses them (`native.go:372-379`). `nativePromptFor` (`native.go:77`) has no case for them. `resolve` skips `approval_result` because there is no agent (`requests.go` ~line 929).
- The only ways to accept an epic are the board (`POST /api/requests/{id}/approve` or `/request-changes`, `internal/httpapi/requests.go`) and the CLI (`swarm approve`, `swarm request-changes`, `cmd/swarm/runtime_cmds.go:500-560`).
- The orchestrator skill says "ask for user acceptance" and "write `completed` when the daemon reports the item accepted" (`skills/swarm-orchestrator/SKILL.md` lines 20 and 51). No tool or relay gives the orchestrator anything to ask with, and nothing reports the result back.
- `close_spike` is opened in `internal/runtime/checkpoint.go:1422-1445` with `agent_id` set, and `native_answer` accepts it. But `CheckpointResult` (`checkpoint.go:99`) has no native prompt, so the spike orchestrator never receives one.
- The web board shows accept rows under a separate "Reviews" filter instead of Approvals.
- When an agent restarts or wakes, nothing reminds it of questions it asked that are still open. The user never sees them again in the terminal.

### The change

1. Route daemon-opened accept rows to the live top-level orchestrator of their root. Bind `agent_id`/`session_id` and send one `request_open` relay carrying the request id, the native prompt (ending with the `⟦swarm:<req>⟧` ref) and the standard next step. `native_answer` accepts the routed row, and `resolve` sends `approval_result` back.
2. `close_spike` uses the same relay.
3. One helper, `resurfaceOpenRequests`, runs when any agent session starts (fresh start, retry, drain, resume, recovery, handoff successor) and on quota-reset wake. It binds agentless accept rows for a starting top-level orchestrator. Then it re-sends a `request_open` relay for every open request routed to the agent that the agent cannot already see. The kickoff or wake notice gets one reminder line with the count.
4. Asking again never creates duplicate rows. `askQuestion` reuses the agent's open question row that has the same prompt.
5. The web "Reviews" filter merges into Approvals. The menubar row for an accept request shows `Accept EPIC-N`.

### Scoping findings, corrected against the code

| # | Brief said | Code says |
|---|---|---|
| 1 | The web Reviews tab is `Review.tsx:146-154` | `Review.tsx` is the per-kind detail pane and already renders accept rows. The tab is `web/src/panels/NeedsYou.tsx:60` plus `web/src/logic/inbox.ts:11-19` (the `REVIEWS` set), `web/src/state/url.ts:25`, `web/src/types.ts:376` and `web/src/copy.ts:176`. |
| 2 | Decision 5: `approveCheck` refuses when the epic revision moved | `nativeAnswer` passes the stored `req.Binding`, so `approveCheck` compares the stored value with itself and always passes. The real guard is `reconcileRoot` step 1: an open row whose `item_revision` / `integrated_checkpoint` no longer matches is set to `stale` (`resolveStale`). This runs in the same transaction as every revision bump (`items.Update` → `ReconcileTx` at `internal/items/store.go:657`, and `setStatus` bumps revision → reconcile). A forward on a stale row therefore used to get `resolve`'s `"Already resolved."` (409). This spec keeps that guard and adds an explicit stale check in `nativeAnswer` with clearer copy. |
| 3 | Only board and CLI can **approve** | Board and CLI can also **request changes** (`swarm request-changes`, `POST /api/requests/{id}/request-changes`). |
| 4 | The menubar labels the row "Accept epic" | `Copy.acceptEpic` feeds only `RequestLine.text`, which only tests use. The rendered row is the generic `NeedsYouRow.lines` (`AppModel.swift:60`). The notifier already maps every `request.*` kind to the `swarm.approval` category. |
| 5 | The wake prompt is built in `wake.go` | `wake.go` holds message wake (`WakeDue`) and quota-reset wake (`WakeOnQuotaReset`, notice `QuotaResetNotice`, `text.go:197`). Every kickoff (fresh `Kickoff`, `ResumeKickoff`, `SuccessorKickoff`) is chosen in exactly one place, `startSession` (`agents.go:1302-1310`). Every start path goes through it: StartOrchestrator, StartSpike, Spawn, drain (`limits.go:377`), Retry (`agents.go:1975`), Resume (`pause.go:909`) and startSuccessor (`replacement.go:892`). |
| 6 | (not in brief) | `WakeOnQuotaReset`'s paste fallback pastes only `IdleToken`, and muse's provider resume takes no kickoff argv (`pause.go` Resume comment). Text that exists only in a kickoff or notice is lost on those paths. So the durable carrier is a relay message, and the kickoff or notice only points at it. |
| 7 | (not in brief) | `enqueue` holds daemon relays to an exhausted kind in `suppressed_relays` (a 500-char sample, and 120 chars in the flush digest). That would destroy the native prompt, so this relay uses `enqueueRaw`. |
| 8 | (not in brief) | The hook opens a new `question` row on every `AskUserQuestion` (`internal/hook/handler.go:612`), and PostToolUse resolves only the newest (`ResolveQuestionByPrompt`, `LIMIT 1 DESC`). Without a dedupe, a re-asked question would leave its first row open forever. |
| 9 | (not in brief) | e2e `hookPost` hardcodes `tool_name: "Bash"`, so `scripts/e2e` cannot drive the native question path. That path is covered in `internal/runtime` with `hookSimulate`. |

### Collision warnings

- `docs/plans/2026-09-21-claude-idle-wake-and-pause-reaper.md` (untracked in the primary checkout) touches `internal/runtime/wake.go`. Rebase onto it if it lands first. This change adds a few lines inside the `WakeOnQuotaReset` row loop only.
- `docs/plans/2026-09-26-new-orchestrator-dialog.md` touches `apps/menubar/Sources/SwarmBarKit/Copy.swift` / `AppModel.swift`. The edits here are two lines in each; expect only textual conflicts.
- Resume (`pause.go`) never calls `repointRequestsTx`; only `startSuccessor` does. Nothing here depends on repointing.

### Caveats (scope calls made)

- Permission-dialog rows (`kind = 'prompt'`) are never re-surfaced, because the dialog lived in a pane that is gone.
- Question rows bound to a ref (`binding_json.ref`) are never re-surfaced themselves. A `req_` ref row shadows an approval that is relayed on its own. A `msg_` ref row (a child approval) keeps its existing `question_unanswered` relay.
- If the user already answered a native approval but the agent never forwarded it (for example, quota died between the answer and `native_answer`), the agent asks again on wake. The ref makes a second answer idempotent (`"Already resolved."` on the loser).
- While an accept row's native dialog is open, the board hides the row from Needs you (existing `native_pending` rule, spec 2.2.1). This is new for accept rows because they could not have a bound dialog before.

## Locked decisions

1. **Delivery.** When an accept row opens (any path, including re-open after changes or a stale sweep), bind it to the root's live top-level orchestrator (`agent_id`, `session_id`) and relay it `request_id`, the native prompt with the ref, and the standard next step. With no live orchestrator, the row stays agentless. Board and CLI still work, and the row is bound and relayed when an orchestrator for that root starts or wakes.
2. **Wake re-surfacing, all requests.** When any agent session starts (fresh, retry, drain, resume, recovery, handoff successor) or wakes on quota reset, every still-open request routed to it is re-sent with its prompt and next step. One shared helper does this. A request already visible in the same live session is not repeated.
3. **close_spike** gets its native prompt through the same relay helper, not through `CheckpointResult`.
4. **UI.** The web Reviews filter merges into Approvals. The menubar accept row reads `Accept EPIC-N`. Tests are ported, not deleted.
5. **Binding.** `native_answer` for accept kinds carries no revision or sha. The daemon reads the binding from the stored row. A row that went stale because the epic changed refuses with a conflict (see correction 2 for the actual mechanism).
6. **Any agent kind may answer.** cursor, muse and codex get the same relay text and keep the board/CLI fallback.
7. **No migration.**

### Assumptions on open questions

- "Live" means `agents.state = 'active'` and a latest session in `LiveStates` (`spawning, running, pause_requested, quiescing, stopping`). A queued, paused, interrupted or finished orchestrator is not live.
- There is at most one live top-level orchestrator per root (the same assumption `terminalAgent` already makes with `LIMIT 1`). When the root's orchestrator starts, it takes over every open accept row of that root, whatever the row's previous `agent_id`.
- The relay goes to the bound agent even when its kind is exhausted. `WakeDue` already skips exhausted kinds, so the message waits for quota reset.
- The count in the reminder line includes requests whose relay is still unacked and so was not re-sent.

## DB models

None. Everything needed already exists:

- `requests.agent_id` / `requests.session_id` are nullable (`internal/db/schema/0006_hitl_terminal_via.sql:6-7`).
- `requests.responded_via` allows `'terminal'` (`0006:21`).
- `messages.kind` allows `'relay'` and `messages.request_id` references `requests(id)` (`0001_init.sql:213-226`).
- The dedupe queries use existing columns only. At one row per open request, no index is needed.

## Model / API types

### Go: `internal/runtime`

```go
// native.go

// NativePromptNextStep is the show-and-forward instruction that rides with
// every daemon-issued native prompt: swarm_ask's result (mcpserver
// requestOut) and the request_open relay share it verbatim.
func NativePromptNextStep(ref string) string

// nativeAnswerKind reports whether native_answer forwards a request of kind k:
// the asking agent's own approvals plus routed accept rows.
func nativeAnswerKind(k RequestKind) bool

// nativePromptFor gains two cases: KindAcceptEpic, KindAcceptFix (copy below).
func (s *Store) nativePromptFor(ctx context.Context, tx *sql.Tx, req Request, sectionTitle string, warnings []string) (NativePrompt, error)

// storedNativePromptTx rebuilds a stored approval's native prompt exactly as
// swarm_ask first returned it (same section title, same plan warnings).
func (s *Store) storedNativePromptTx(ctx context.Context, tx *sql.Tx, req Request) (NativePrompt, error)

const errRequestStale = "%s is stale: %s changed after the question was asked. Don't forward it; " +
	"Swarm sends a new request when the work is ready again."
```

```go
// requests.go

// liveRootOrchestratorTx: the live top-level orchestrator of itemID's root
// and its newest live session.
func (s *Store) liveRootOrchestratorTx(ctx context.Context, tx *sql.Tx, itemID string) (agentID, sessionID string, ok bool, err error)

// routeAcceptTx binds an accept_epic/accept_fix row to that orchestrator and
// relays it; a no-op for any other kind or when none is live.
func (s *Store) routeAcceptTx(ctx context.Context, tx *sql.Tx, id string) error

// relayRequestTx enqueues (enqueueRaw) one request_open relay to req.AgentID.
func (s *Store) relayRequestTx(ctx context.Context, tx *sql.Tx, id string) error

// resurfaceOpenRequests: the one wake/restart helper (decision 2).
// fresh=true for a new session (nothing visible), false for a same-session wake.
func (s *Store) resurfaceOpenRequests(ctx context.Context, a Agent, sessionID string, fresh bool) (n int, err error)

// OnRequestOpened (existing) now calls routeAcceptTx first, then notifies as before.
func (s *Store) OnRequestOpened(ctx context.Context, tx *sql.Tx, id string) error

const reaskQuestionNext = "..." // copy below
const blockerOpenNext = "..."   // copy below
```

```go
// text.go
func OpenRequestsReminder(n int) string
```

No existing exported signature changes; two exported helpers are added (`NativePromptNextStep`, `OpenRequestsReminder`). `mcpserver.requestOut` calls `runtime.NativePromptNextStep(r.ID)` in place of its inline `fmt.Sprintf`.

### Relay payload (`messages.kind = 'relay'`, `origin = 'daemon'`, `request_id` set)

```jsonc
{
  "event": "request_open",
  "agent": "<routed agent name>",          // what the Inbox line prints after "from"
  "item": "EPIC-12",
  "request_id": "req_…",
  "kind": "accept_epic",                   // any RequestKind except "prompt"
  "question": "<native_prompt.question, or the stored prompt for question/blocker>",
  "native_prompt": {"header": "…", "question": "…⟦swarm:req_…⟧", "options": ["Approve", "Request changes"]}, // approval kinds only
  "options": ["…"],                        // question only (stored options_json)
  "next": "<next step, copy below>"
}
```

The existing relay summarizer (`inbox.go` `summarizeFor`, case `"relay"`) renders this as `request_open from <agent> (<item>): "<question>"`. It needs no change.

### MCP / HTTP

No new tools, routes or fields. `swarm_ask kind:"native_answer", ref:"<accept req id>", decision, comment` now succeeds for a routed accept row.

### TypeScript: web

```ts
// web/src/types.ts
export type InboxFilter = "all" | "questions" | "approvals";
```

### Swift: menubar

```swift
// Copy.swift, replaces `acceptEpic` / `acceptFix`
public static func acceptItem(_ key: String) -> String { "Accept \(key)" }
```

## Screens

### Orchestrator terminal: native question for an accept row (claude AskUserQuestion)

```
 ┌ Accept epic ───────────────────────────────────────────────┐
 │ Accept EPIC-12 "Authentication" as done? ⟦swarm:req_7Hq2⟧  │
 │  ❯ 1. Approve                                              │
 │    2. Request changes                                      │
 │    3. Type something…                                      │
 └────────────────────────────────────────────────────────────┘
```

The orchestrator prints a summary in chat first (integrated SHAs, verification, children), never inside the dialog. There is no preview text and no batching.

### Web board: Needs you (merged filter)

```
┌──────────────────────────────┬──────────────────────────────────────────────┐
│ Needs you                    │ Accept epic · EPIC-12 › Authentication        │
│ [All][Questions][Approvals]  │ Requested by auth-orch · 3m ago              │
│ ┌──────────────────────────┐ │ Item revision 7                              │
│ │EPIC-12 · Authentication ▶│ │ ──────────────────────────────────────────── │
│ │auth-orch                 │ │ chat · main · 1a2b3c4                        │
│ │Waiting for your input    │ │ ✓ go test ./...                              │
│ └──────────────────────────┘ │ STORY-3 Login — Done · merged                │
│ ┌──────────────────────────┐ │ Plan · rev 4 · View                          │
│ │SPIKE-3 · Offline mode   ▶│ │ ──────────────────────────────────────────── │
│ │offline-orch              │ │ [Accept epic] [Request changes]              │
│ │Waiting for your input    │ │                                              │
│ └──────────────────────────┘ │                                              │
└──────────────────────────────┴──────────────────────────────────────────────┘
```

- Segmented control: `All`, `Questions`, `Approvals`. **Reviews is gone.** Approvals = every open, non-`native_pending` request whose kind is not `question` / `prompt` / `blocker`. That includes `accept_epic`, `accept_fix` and `close_spike`, ordered oldest first like every list.
- The row is still the generic three lines (spec 2.2.2). Line 2 now shows the orchestrator name once the row is bound. Before binding it shows `terminal_agent`, or `—` with no orchestrator.
- The ▶ terminal button is unchanged (`requestTarget`): live orchestrator → opens its terminal; paused → disabled plus `Orchestrator is paused. Resume it to continue.`; none → `Orchestrator isn't running.`
- The detail pane (`Review.tsx`) is unchanged.
- Empty state is unchanged: `Nothing needs your attention.` (existing `C.inboxEmpty`).
- An old `#/inbox?filter=reviews` link parses to `all` (the existing `pick` fallback), so the row is still listed.
- Deliberately not here: no per-kind row text on the web, and no "accept" badge.

### Menubar: Needs you row

```
┌─────────────────────────────────────────┐
│ EPIC-12 · Authentication            [>_]│   caption, secondary
│ auth-orch                               │   callout
│ Accept EPIC-12                          │   callout  (was "Waiting for your input")
└─────────────────────────────────────────┘
  accept_fix:  line 3 = "Accept BUG-3"
  other kinds: line 3 = "Waiting for your input" (unchanged)
  paused orchestrator: + caption "Orchestrator is paused. Resume it to continue.", button disabled
```

The same yellow row background, and the same terminal button behaviour as every approval. Deliberately not shown: the agent's prompt text (spec 1.6.3).

## All user-facing copy (verbatim)

### Native prompts (`nativePromptFor`)

| Kind | Header | Question (before the ` ⟦swarm:<req id>⟧` suffix) | Options |
|---|---|---|---|
| `accept_epic` | `Accept epic` | `Accept EPIC-12 "Authentication" as done?` (Go `fmt.Sprintf("Accept %s %q as done?", key, title)`) | `Approve`, `Request changes` |
| `accept_fix` | `Accept fix` | `Accept the fix for BUG-3 "Login loop" as done?` (`fmt.Sprintf("Accept the fix for %s %q as done?", key, title)`) | `Approve`, `Request changes` |
| `close_spike` | `Close spike` (unchanged) | `Close SPIKE-4?` (unchanged) | `Approve`, `Request changes` |

The question is capped at 1000 runes and the ref always survives (`truncateWithToken`).

### Next steps (relay `next`)

- Approval kinds (`approve_section`, `approve_plan`, `approve_report`, `confirm_repos`, `close_spike`, `accept_epic`, `accept_fix`): `NativePromptNextStep(id)`, which is today's swarm_ask `next`, moved unchanged:
  `Print the summary in chat first, not in the question. Then show native_prompt with your native question tool now (one question per call, verbatim, no added text). Once the user answers, call swarm_ask kind:"native_answer", ref:"<req id>", decision:"approve"|"request_changes" forwarding only what the user picked, never a decision they did not make.`
- `question` (no ref): `Ask the user again with the same text and options: claude and agy with your native question tool, cursor, muse and codex with swarm_ask kind:"question". Swarm keeps one Needs-you row for it.`
- `blocker`: `Your blocker is still open in Needs you. The user's answer arrives as a user_answer message; don't ask again.`

### Kickoff / wake reminder (`OpenRequestsReminder`)

`<n> request(s) still wait on your user. swarm_sync delivers each as a request_open relay with its native prompt and next step; ask again as it says.`

It is appended after a single space to the kickoff chosen in `startSession`, and to `QuotaResetNotice()` in `WakeOnQuotaReset`. It is omitted when n = 0.

### Errors

- Stale forward (new): `req_7Hq2 is stale: EPIC-12 changed after the question was asked. Don't forward it; Swarm sends a new request when the work is ready again.` (CodeConflict)
- Wrong target (reworded `errNativeAnswerWrongTarget`): `%s is not an approve_section, approve_plan, approve_report, confirm_repos, close_spike, accept_epic or accept_fix request routed to you.` (CodeBadRequest)
- Board approve on a stale row: unchanged, `Already resolved.` (409, with the board's existing `C.staleApproval` banner).

### UI labels

- Web: `C.reviews` (`"Reviews"`) is deleted. `C.approvals` (`"Approvals"`) is unchanged.
- Menubar: `Copy.acceptItem("EPIC-12")` → `Accept EPIC-12`. `Copy.acceptEpic` / `Copy.acceptFix` are deleted.
- Notifications are unchanged (`request.accept_epic`: `Epic acceptance needed` / `{KEY}: Review completed work and accept the epic.`).

### Skills (agent-facing copy)

- `skills/swarm-orchestrator/SKILL.md`: the lines 20, 51 and 68 rewrites, a new `request_open` relay bullet, and one sentence in the native-approval paragraph. The exact text is in the plan's Batch C.
- `skills/swarm/SKILL.md`: one sentence on `request_open` after a restart or wake (exact text in the plan).

## File list

### Changed

- `internal/runtime/native.go`: accept cases in `nativePromptFor`, `NativePromptNextStep`, `nativeAnswerKind`, `storedNativePromptTx`, stale check and reworded wrong-target copy in `nativeAnswer`.
- `internal/runtime/requests.go`: `liveRootOrchestratorTx`, `routeAcceptTx`, `relayRequestTx`, `resurfaceOpenRequests`, `reaskQuestionNext` / `blockerOpenNext`, `OnRequestOpened` routes first, `askQuestion` dedupe, doc comments on `approvalTerminalKinds` / `resolve` updated.
- `internal/runtime/checkpoint.go`: `relayRequestTx` after the close_spike insert.
- `internal/runtime/agents.go`: `startSession` calls `resurfaceOpenRequests(ctx, a, ses.ID, true)` and appends the reminder.
- `internal/runtime/wake.go`: `WakeOnQuotaReset` calls `resurfaceOpenRequests(ctx, a, sessionID, false)` and appends the reminder to the notice.
- `internal/runtime/text.go`: `OpenRequestsReminder`.
- `internal/mcpserver/tools.go`: `requestOut` uses `runtime.NativePromptNextStep`.
- `web/src/logic/inbox.ts`, `web/src/types.ts`, `web/src/state/url.ts`, `web/src/panels/NeedsYou.tsx`, `web/src/copy.ts`: filter merge.
- `apps/menubar/Sources/SwarmBarKit/Copy.swift`, `apps/menubar/Sources/SwarmBarKit/AppModel.swift`: `acceptItem`, row line 3.
- `skills/swarm-orchestrator/SKILL.md`, `skills/swarm/SKILL.md`, then the `internal/install/skills/**` mirror via `make skills-sync`.

### Tests (added or ported, none deleted)

- New: `internal/runtime/approval_lane_test.go` (Batch A and B runtime tests), `scripts/e2e/epicapproval_test.go` (e2e).
- Ported: `web/src/logic/inbox.test.ts` (the `reviews` assertions become `approvals`), `web/src/panels/NeedsYou.test.tsx` (approvals count 5 → 7), `web/src/state/url.test.ts` (adds `filter=reviews` → `all`), `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift` (`RequestLine` accept lines and `NeedsYouRow` line 3).
- Any existing Go test whose exact assertions change because of the new relay or dedupe is ported in place (see plan A/B "port" steps).

### Reused unchanged

`approveCheck`, `resolve`, `matchDecisionEvidence`, `truncateWithToken`, `enqueueRaw`, `summarizeFor`, `reconcileRoot` (including `resolveStale`), `terminalAgent`, `nativePendingTx`, httpapi approve/request-changes routes, CLI `swarm approve` / `request-changes`, `Review.tsx`, the notification templates, and `cmd/swarm/daemon.go` wiring (it already points `RequestOpened` at `OnRequestOpened`).

### Deleted

The `reviews` filter value, `C.reviews`, `Copy.acceptEpic` and `Copy.acceptFix` (replaced as above). No tests are deleted.

## Verification

### Command order

1. `go test ./internal/runtime/ -run 'TestAccept|TestCloseSpikeRelay|TestNativePrompt|TestNativeAnswer|TestResurface|TestAskQuestionReuses|TestQuotaReset|TestStartOrchestratorBinds|TestResumeResurfaces' -race -count=1`
2. `go test -race ./internal/mcpserver/ ./internal/runtime/ ./internal/items/ ./internal/httpapi/ ./cmd/swarm/ -count=1`
3. `make vet fmt`, then `go test -race ./...`
4. `cd web && pnpm exec vitest run src/logic/inbox.test.ts src/panels/NeedsYou.test.tsx src/state/url.test.ts`, then `pnpm test` and `pnpm exec tsc -b --noEmit` (or `pnpm build`)
5. `cd apps/menubar && swift test --filter AppModelTests`, then `make test-menubar`
6. `make skills-sync && git status --short internal/install/skills` (the mirror diff must equal the `skills/` diff)
7. `make e2e` (fake adapter, own port 17778 and tmux socket `swarm-e2e`; never the live daemon)

### End-to-end scenarios

| # | Scenario | Expected | Covered by |
|---|---|---|---|
| E1 | Epic integrates with its orchestrator live | `accept_epic` opens with `agent_id` = orchestrator. One `request_open` relay with `native_prompt.question` = `Accept EPIC-1 "Build it" as done? ⟦swarm:<id>⟧` and next = `NativePromptNextStep(id)`. | `TestAcceptRowRoutesToLiveRootOrchestrator`, e2e `TestScenarioEpicApprovalLane` |
| E2 | Native approve | hookSimulate (question shown, answer `Approve`), then `native_answer approve`: row `approved`, `responded_via='terminal'`, `approval_result` `{decision: approved, evidence: observed}` to the orchestrator. | `TestNativeAnswerApprovesRoutedAcceptRow` |
| E3 | Native request-changes | Row `changes_requested`, comment stored, `approval_result` `{decision: changes_requested}` to the orchestrator. | `TestNativeAnswerRequestChangesOnAcceptRow` |
| E4 | No live orchestrator | The row stays `agent_id NULL` and no relay is sent. Board approve still works (existing scenario 29). | `TestAcceptRowStaysAgentlessWithoutLiveOrchestrator` |
| E5 | Orchestrator starts later | StartOrchestrator on the root: the row is bound to the new session, one relay is sent, and the kickoff ends with `1 request(s) still wait on your user…`. | `TestStartOrchestratorBindsAndRelaysAgentlessAcceptRows` |
| E6 | Stale revision | The row is `stale`, then `native_answer` gets a conflict with `errRequestStale` copy. Board approve on the stale row returns 409 (scenario 29, unchanged). | `TestNativeAnswerStaleAcceptIsRefused`, e2e 29 |
| E7 | Re-open after request-changes | Board request-changes: `approval_result changes_requested` to the orchestrator and the epic goes back to `in_progress`. A new `integrated` opens a fresh row, and a fresh relay names the new id. Board approve sends `approval_result approved` and the epic is `done`. | e2e `TestScenarioEpicApprovalLane` |
| E8 | close_spike | A spike orchestrator writes `completed` + `resolution: no_change`: relay to itself with header `Close spike`. The native approve moves the spike to `done`. | `TestCloseSpikeRelaysNativePrompt` |
| E9 | Resume re-surface | A paused spike orchestrator with an open approve_section and an open plain question resumes (fresh launch). Two relays: the approve_section `native_prompt` is byte-equal to the original `swarm_ask` one, and the question relay carries `reaskQuestionNext`. The kickoff has `2 request(s)…`. | `TestResumeResurfacesOpenRequests` |
| E10 | Quota-reset wake, same session | These are skipped: a question asked in this session, an approval whose native question is open in this session, and a permission prompt. An approval never shown is relayed. The notice is `QuotaResetNotice() + " 1 request(s)…"`. | `TestQuotaResetWakeResurfacesOnlyWhatIsNotVisible` |
| E11 | Exhaust / dedupe | Calling resurface twice without an ack sends one relay per request, and n counts both. The relay is enqueued even when the kind is exhausted (no `suppressed_relays` row). | `TestResurfaceSkipsRequestsWithAPendingRelay`, `TestAcceptRelayIsNotHeldWhileExhausted` |
| E12 | Re-ask dedupe | AskQuestion twice with the same prompt while the first is open returns the same id and moves `session_id` to the caller, so there is one Needs-you row. | `TestAskQuestionReusesOpenRowWithSamePrompt` |
| E13 | Decline (user types free text) | Existing `matchDecisionEvidence` behaviour applies to accept rows too (agent_reported plus comment). | Covered by existing `TestMatchDecisionEvidence`; no new test |
| E14 | Web | Approvals lists `req_accept`, `req_fix` and `req_close`. `filter=reviews` parses to `all`. | inbox/NeedsYou/url tests |
| E15 | Menubar | The accept row's line 3 is `Accept EPIC-12`, and no row contains the prompt. | `AppModelTests` |

## Explicitly out of scope

- Returning the native prompt in `CheckpointResult` (decision 3 picks the relay).
- Detecting an answered-but-unforwarded native answer on wake and telling the agent to forward instead of re-asking.
- Re-surfacing permission prompts, `msg_` child-approval rows (they keep `question_unanswered`), or messages in general.
- Closing an accept row's open native question when the row goes stale.
- Per-kind row text on the web board, and changes to `Review.tsx`, notifications, httpapi routes or the CLI.
- A migration, index or new message kind.
- `approveCheck` comparing against a live binding. Staleness stays enforced by `reconcileRoot`.

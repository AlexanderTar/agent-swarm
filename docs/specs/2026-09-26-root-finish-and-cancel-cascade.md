# Root-done graceful finish and parent-cancel cascade

Date: 2026-09-26. Branch: `feat/root-finish-cancel-cascade`. Worktree: `/Users/alexandertar/GitHub/agent-swarm-root-finish` (based on `origin/main` at `b472262`, which includes the epic-approval lane, `docs/specs/2026-09-26-epic-approval-lane.md`).
Companion plan: `docs/plans/2026-09-26-root-finish-and-cancel-cascade.md`.

## Context

### Problem 1: an accepted root never finishes its orchestrator

User report: orchestrators should finish on their own once their epic, bug or spike is done and accepted. A manual close records the agent as cancelled.

Case `support-chat-attachments-orchestrator-2` / EPIC-3, 2026-09-26 11:19-11:21:

1. `accept_epic` was approved from the CLI. `reconcileRoot` moved EPIC-3 to Done. The row was agentless (this was before the approval lane), so no message reached the orchestrator.
2. The orchestrator wrote a `handoff` checkpoint. `onPausingCheckpoint` (`internal/runtime/pause.go:356`) moves any handoff to `stopping`, so the session parked as `paused`.
3. It was resumed (generation 3), and the user closed it with `POST /api/agents/{name}/cancel` (`internal/httpapi/spawn.go:243`). `Store.Cancel` (`internal/runtime/agents.go:1730`) set the live session to `cancelled` (`agents.go:1764`).

Nothing in the daemon reacts to a root reaching Done. The only session-ending signal is the agent's own `completed` checkpoint on its own item. `resolveAlive` kills that pane `killCompletedAfter` (60 s, `reconcile.go:19`, check at `reconcile.go:1102`) later, and `resolveDeadInner` (`reconcile.go:739`, completed branch `reconcile.go:842`) ends it `completed` and the agent `finished`.

### Problem 2: cancelling a parent leaves its children running

User (2026-09-26): "parent task cancellation should cancel all its children as well". `TransitionTx` (`internal/items/transition.go:38-70`) cancels only the item itself and stales its accept rows (`staleAccepts`). Its children stay Ready or In progress, their agents keep working, and their workflows keep spawning runs. `Spawn` has no cancelled-item guard (the only status check is `recoverableOrchestrator`, `agents.go:664`), so a `running` workflow row can respawn a run on a cancelled task.

### The change

1. **Root done → daemon finishes the root's agents.** A new `items.Store.RootDone` hook fires from `setStatus` when a top-level item reaches Done. `runtime.OnRootDone` finishes every queued or active agent on that root in the same transaction:
   - A live, non-pausing session gets a daemon-written `completed` checkpoint on its own item and attempt. The reaper ends it `completed` 60 s later, which leaves the orchestrator time to read `approval_result` and post a final message.
   - Every other agent (pausing, paused, interrupted or with no session) is finished at once. Its session is set to `completed` and the agent to `finished`.
2. **Cancel on a done root records Completed.** `Store.Cancel` ends the session `completed` instead of `cancelled` when the agent's root is Done.
3. **A handoff after acceptance is refused.** `WriteCheckpoint` refuses `handoff` once the root is Done, with the copy `Root accepted; write completed.`
4. **Cancel cascades down the item tree.** Cancelling an item also cancels every descendant that is not Done or Cancelled, in the same transaction.
5. **Work on cancelled items stops.** A new Reconcile pass cancels running workflows and then queued or active agents whose own item is Cancelled. It uses the existing `CancelWorkflow` and `Store.Cancel`.

### Brief references, corrected against the code

| # | Brief said | Code says |
|---|---|---|
| 1 | `pause.go:357 onPausingCheckpoint` | The func starts at `pause.go:356`. It moves every `handoff`, pausing or not, to `stopping`. |
| 2 | `Store.Cancel sets Cancelled unconditionally (agents.go:1752)` | `Cancel` is at `agents.go:1730`. `agents.go:1752` is the `auto_restart = 0` update. The session write is `agents.go:1764`, and it runs **only when the latest session is live**. A paused session keeps `paused` and only the agent becomes `finished`. |
| 3 | `transition.go:464` DepUnblocked hook, `store.go:36` field | Correct (`transition.go:464-466`, field doc `store.go:30-36`). |
| 4 | wired `cmd/swarm/daemon.go:287` | `DepUnblocked` is wired at `daemon.go:286`. Line 287 is `StoryReadyForReview`. The new wiring goes after 287. |
| 5 | daemon-written checkpoint precedent `pause.go:854` | `writeDaemonPauseCheckpoint` is at `pause.go:824`, and its `INSERT INTO checkpoints` is at `pause.go:853`. |
| 6 | reaper `reconcile.go:90` | `liveSessionRows` is at `reconcile.go:87`. Its `CompletedCheckpointAt` subquery (own item, same attempt) is at lines 91-93. `killCompletedAfter` is at line 19 and `resolveDeadInner` at line 739. |
| 7 | (not in brief) | `resolveDeadInner` checks `Pausing() && getInterrupted` (→ `interrupted`) and `Stopping` (→ `paused`) **before** `terminalCheckpointKind`. So a daemon-written `completed` only ends `completed` for `spawning`/`running` sessions. Pausing sessions take the direct-finish path (decision 1b). |
| 8 | `materialize.go:345` spike path | Correct. `Materialize` calls `TransitionTx(spike, Done)` inside its own tx, so `RootDone` fires mid-call for the spike. Skip rule: decision 1c. |
| 9 | `checkpoint.go ~1110` handoff refusal | `WriteCheckpoint` is at `checkpoint.go:1095`. The refusal goes between the live check (`1112-1114`) and the pausing check (`1115`). |
| 10 | `SKILL.md:20` "user handoff", `SKILL.md:51` accepted flow | Line 20 is correct. The accepted flow is line **52** (51 is the `swarm_control cancel` bullet). The approval lane already rewrote 52 to "On `approval_result` `approved` … write `completed`". |
| 11 | enqueue a `root_accepted` relay | Reused `approval_result` instead. `resolve` (`requests.go:1145-1153`) enqueues `approval_result` to a routed row's agent **before** `Items.ReconcileTx` (`requests.go:1166`) → `reconcileRoot` → `setStatus(Done)` → `RootDone`, all in one tx. This covers board, CLI and `native_answer` approvals. An agentless row means no live orchestrator, which is the direct-finish case, so there is nobody to message. |
| 12 | cascade `transition.go ~40-68`, `check() :135` | `TransitionTx` is at `38-70`. `check` is at `128`, and its Cancelled case (`135-139`) allows a user on any non-Done item and an orchestrator on a non-root item. |
| 13 | reuse the pause.go subtree walker | `descendantAgents` (`pause.go:616`) walks **agents** by `parent_agent_id`. The cascade walks **items** by `parent_id`, with a recursive CTE shaped like `isDescendant` (`checkpoint.go:905`), and selects agents by `item_id`. |
| 14 | cancel agents "in the same tx" | `Store.Cancel` opens its own tx and calls tmux, so it cannot run inside the items tx. The agent and workflow part runs as a Reconcile pass instead (decision 5). |

### Affected repos and worktrees

Only `agent-swarm`, and only this worktree. Nothing here touches the primary checkout, `~/.swarm`, the live daemon, launchd, `tmux -L swarm` or the user's real `~/.claude*`.

### Collision warnings

- `docs/plans/2026-09-21-claude-idle-wake-and-pause-reaper.md` (untracked in the primary checkout) touches `internal/runtime/reconcile.go` / `pause.go`. This change adds one call in `Reconcile` and no pause.go edits. Rebase if it lands first.
- `skills/swarm-orchestrator/SKILL.md` was just rewritten by the approval lane. The edits here are two sentence swaps and one new bullet.

### Caveats (scope calls made)

- **Pause requested inside the 60 s grace.** If the user pauses an orchestrator after `RootDone` wrote its `completed` and before the reaper kills it, the agent can write neither `handoff` (refused, decision 3) nor `completed` (refused while pausing, `pauseAllowedKinds`). The reaper still kills it at 60 s. It ends `completed` unless the pause deadline already sent interrupt keys, in which case it ends `interrupted`. Recovery: the user closes it, and decision 2 records `completed`. No special code.
- **Operation lock.** `OnRootDone` runs inside the items tx, so it calls `cancelAgentOperationsTx` without `lockAgentOperations`. A replacement driver that already committed its launch could still start a successor. That successor then has a done root: its handoff is refused, and closing it records `completed`. Marked with a `ponytail:` comment.
- **Chore roots** get the same treatment as epics and bugs: when a chore reaches Done, its agents finish.

## Locked decisions

1. **The daemon finishes a done root's agents, authoritatively.**
   a. `setStatus` fires `RootDone(ctx, tx, it.ID)` when `to == Done && it.ID == it.RootID`, next to the `DepUnblocked` hook.
   b. `OnRootDone` acts on every agent with `root_item_id = rootID` and `state IN ('queued','active')`:
      - Latest session `spawning` or `running`: insert a `completed` checkpoint with `daemon_written = 1`, `item_id = agents.item_id` (the agent's own item) and `attempt = sessions.attempt`. The reaper ends the session `completed` 60 s later.
      - Anything else: set the session to `completed` if it is `pause_requested`, `quiescing`, `stopping`, `paused` or `interrupted`. Then set the agent to `finished`, release its worktree reservations and publish `agent.changed`. A leftover live pane is killed by Reconcile's finished-agent pass (`reconcile.go:196-210`) on the next tick.
      - Every agent gets `cancelAgentOperationsTx` first.
   c. On a **spike** root, the live session of the agent whose `item_id` is the spike itself is skipped. Such an agent is either inside `swarm_materialize` or has already written `completed` with a `resolution` (the only way `close_spike` opens). Either way it ends through its own checkpoint. The spike close and materialize paths are unchanged.
   d. The orchestrator learns about acceptance from `approval_result` (reused, correction 11). There is no new relay kind or payload field. The skill text and the handoff refusal copy are the only instructions that say "post a summary and stop".
2. **`Store.Cancel` on an agent whose root is Done records `completed`.** A live session ends `completed` instead of `cancelled`. A paused or interrupted session also becomes `completed`. On a root that is not Done, Cancel behaves exactly as today, and a paused session stays `paused` so the agent stays resumable.
3. **`WriteCheckpoint` refuses `handoff` when the root is Done**, with `CodeConflict` and the copy `Root accepted; write completed.` `completed` stays allowed.
4. **Cancel cascades down the item tree.** Every descendant (recursive on `parent_id`) whose status is not `done` or `cancelled` is set to Cancelled via `setStatus` (so `item.changed` and `DepUnblocked` fire per child) and gets `staleAccepts`, in the same tx. There is no per-child `check()`: the cascade is allowed wherever the parent's cancel was. Archived descendants are included, because archiving hides an item but does not finish it. Done descendants stay Done.
5. **Agents and workflows on cancelled items are cancelled by a Reconcile pass**, `cancelWorkOnCancelledItems`, which runs every tick before `TickPause`:
   - First, each item whose status is `cancelled` and whose **latest** workflow row is `running` or `escalated` gets `CancelWorkflow(ctx, Agent{RootItemID: item.root_id}, item.key, "", "")`. The stand-in `Agent` satisfies `CancelWorkflow`'s root check. Its `tryTransition(Ready)` on a Cancelled item is denied and swallowed (`checkpoint.go:891-902`), so the item stays Cancelled.
   - Then each agent with `state IN ('queued','active')` whose own `item_id` is `cancelled` gets `Store.Cancel(ctx, name, "", "")`.
   - Per-item and per-agent errors are logged, never returned, so one bad row never stops Reconcile.
   - Agents on other items, including Done descendants, are untouched.
6. **Reopening a cancelled parent (Cancelled → Ready) does not reopen its children** (locked assumption from the orchestrator), and it restarts no agents.

### Explicit assumptions

- "Top-level item" means `items.id = items.root_id` (epic, bug, spike, chore).
- A cascade from the user's cancel reaches the agents within one Reconcile tick (5 s). The immediate part is the item state.
- Existing agents on items that were cancelled **before** this ships are cancelled on the first tick after the daemon restarts. This is the intended invariant, not a migration.

## DB models

None. Every column already exists: `checkpoints.daemon_written` (`0001_init.sql:204`), `agents.state` / `finished_at`, `sessions.state` / `ended_at`, `agent_operations.phase`, `workflows.state`, `worktree_reservations.released_at`, `items.parent_id` / `root_id` / `status`. No migration and no index: the Reconcile pass joins `agents` and `workflows` to `items` by primary key and filters on `items.status = 'cancelled'`.

## Model / API types

### Go: `internal/items`

```go
// store.go, new field on Store
// RootDone fires when a top-level item (id == root_id) reaches Done: an
// accepted epic or bug, a closed or materialized spike, a done chore. rootID
// is that item's id; the hook finishes every agent still on the root. nil
// does nothing, same as the other hooks.
RootDone func(ctx context.Context, tx *sql.Tx, rootID string) error

// transition.go, new
// cancelDescendants cancels every descendant of parentID that is not Done or
// Cancelled yet (locked decision 4).
func (s *Store) cancelDescendants(ctx context.Context, tx *sql.Tx, parentID string) error
```

`TransitionTx`, `Transition`, `Update`, `UpdateTx` and `check` keep their signatures.

### Go: `internal/runtime`

```go
// finish.go (new file)

// rootDoneSummary is the daemon-written completed checkpoint's summary; %s is the root key.
const rootDoneSummary = "%s is done; Swarm finished this agent."

// errRootAcceptedHandoff is WriteCheckpoint's handoff refusal on a done root.
const errRootAcceptedHandoff = "Root accepted; write completed."

// OnRootDone is items.Store.RootDone (locked decision 1).
func (s *Store) OnRootDone(ctx context.Context, tx *sql.Tx, rootID string) error

// rootIsDone reports whether the top-level item rootID is Done. q is s.DB or a tx.
func (s *Store) rootIsDone(ctx context.Context, q txQuerier, rootID string) (bool, error)

// cancelWorkOnCancelledItems is Reconcile's cascade pass (locked decision 5).
func (s *Store) cancelWorkOnCancelledItems(ctx context.Context) error
```

Changed bodies, same signatures:

- `func (s *Store) Cancel(ctx context.Context, name, sessionID, requestID string) (Agent, error)` (decision 2).
- `func (s *Store) WriteCheckpoint(ctx context.Context, sessionID string, in CheckpointInput) (CheckpointResult, error)` (decision 3).
- `func (s *Store) Reconcile(ctx context.Context) error` calls `cancelWorkOnCancelledItems` before `TickPause`.

### Wiring: `cmd/swarm/daemon.go`

```go
it.RootDone = rt.OnRootDone // finish every agent on a root that just reached Done
```

This goes after `it.StoryReadyForReview = rt.OnStoryReadyForReview` (`daemon.go:287`). The runtime test `newStore` is **not** wired, so existing runtime tests keep their behaviour. The runtime tests call `OnRootDone` directly, or set `s.Items.RootDone = s.OnRootDone` when they need the real chain (materialize).

### MCP / HTTP / relays

No new tools, routes, fields, message kinds or relay events. `swarm_checkpoint kind:"handoff"` on a done root now returns the conflict above. `approval_result` payloads are unchanged.

## Screens

No UI code changes. Every surface already renders session state and item status. The sketches show what the user now sees.

### Web board: agent row after an accepted epic (finished by the daemon)

```
┌ EPIC-3 · Chat attachments ───────────────────────── Done ┐
│ Agents                                                   │
│ ● support-chat-attachments-orchestrator   Completed      │  grey dot, SESSION_LABEL.completed
│   orchestrator · fake-1                                  │
│   Last checkpoint: EPIC-3 is done; Swarm finished this   │
│   agent.                                                 │
│ ● attachments-coder                       Completed      │
└──────────────────────────────────────────────────────────┘
```

- Status text is `Completed` (`web/src/copy.ts` `SESSION_LABEL.completed`) for both the grace-period path and the direct-finish path. It is never `Cancelled` for an agent on a Done root.
- During the 60 s grace the row still reads `Running` (or `Waiting`), with the daemon checkpoint as its latest checkpoint.
- Deliberately not shown: no badge for "finished by daemon", and no countdown.

### Web board: item tree after cancelling an epic with one Done story

```
EPIC-7 · Offline mode                      Cancelled
├─ STORY-12 · Sync queue                   Done        ← kept
│  └─ TASK-40 · Persist queue              Done        ← kept
└─ STORY-13 · Conflict UI                  Cancelled   ← cascaded
   ├─ TASK-41 · Banner                     Cancelled   ← cascaded (was In progress)
   └─ TASK-42 · Retry button               Cancelled   ← cascaded (was Ready)

Agents
● offline-orchestrator        Cancelled   (within ~5 s)
● banner-coder                Cancelled   (within ~5 s)
```

- Status labels are the existing `StatusLabel` values (`Cancelled`, `Done`).
- After **Reopen** of EPIC-7: EPIC-7 reads `Ready`. STORY-13, TASK-41 and TASK-42 stay `Cancelled`, and no agent restarts.
- Deliberately not shown: no "cancelled by parent" marker and no confirmation dialog listing descendants.

### Menubar: agent rows

```
┌─────────────────────────────────────────┐
│ ○ support-chat-attachments-orch…        │   grey dot (.completed → .grey)
│   EPIC-3 · Completed                    │   AgentActions label "Completed", no actions
├─────────────────────────────────────────┤
│ ○ banner-coder                          │   grey dot (.cancelled → .grey)
│   TASK-41 · Cancelled                   │   label "Cancelled", no actions (agent finished)
└─────────────────────────────────────────┘
```

Both come from `apps/menubar/Sources/SwarmBarKit/AgentActions.swift` (labels lines 43-44, tone line 56, actions lines 179-185). Nothing there changes.

## All user-facing copy (verbatim)

| Where | Copy |
|---|---|
| Daemon-written checkpoint summary (`rootDoneSummary`) | `EPIC-3 is done; Swarm finished this agent.` (`fmt.Sprintf("%s is done; Swarm finished this agent.", rootKey)`) |
| Handoff refusal (`errRootAcceptedHandoff`, CodeConflict) | `Root accepted; write completed.` |
| Web / menubar session label | existing `Completed` / `Cancelled`, unchanged |
| Item status label | existing `Cancelled` / `Done` / `Ready`, unchanged |
| Notifications | none added. The cascade raises no notification per child: the board updates via `item.changed`. |

### Skills (agent-facing copy)

`skills/swarm-orchestrator/SKILL.md`:

- Line 20, replace `when deciding the final branch integration and user handoff.` with `when deciding the final branch integration and what to report to the user.`
- Line 52, replace `On \`approval_result\` \`approved\` the daemon has already moved the item to done: write \`completed\`.` with:
  `On \`approval_result\` \`approved\` the daemon has already moved the item to done and ends your session about a minute later: post a short final summary in chat and stop. Never write \`handoff\` after acceptance; Swarm refuses it (\`Root accepted; write completed.\`).`
- After line 51 (`swarm_control cancel` bullet), add a sub-bullet at the same indent:
  `  - Cancelling a story or task (\`swarm_items update status: "cancelled"\`) also cancels every descendant that isn't Done, and Swarm cancels their agents and workflows within a few seconds. Reopening it later doesn't reopen those children.`

Then run `make skills-sync` so the `internal/install/skills/**` mirror matches.

## File list

### Changed

- `internal/items/store.go`: `RootDone` field.
- `internal/items/transition.go`: `setStatus` fires `RootDone`; `TransitionTx` calls `cancelDescendants`; new `cancelDescendants`.
- `internal/runtime/finish.go` (new): `rootDoneSummary`, `errRootAcceptedHandoff`, `OnRootDone`, `rootIsDone`, `cancelWorkOnCancelledItems`.
- `internal/runtime/agents.go`: `Cancel` end state (decision 2).
- `internal/runtime/checkpoint.go`: handoff refusal in `WriteCheckpoint`.
- `internal/runtime/reconcile.go`: `Reconcile` calls `cancelWorkOnCancelledItems`.
- `cmd/swarm/daemon.go`: `it.RootDone = rt.OnRootDone`.
- `skills/swarm-orchestrator/SKILL.md`, then the `internal/install/skills/swarm-orchestrator/SKILL.md` mirror via `make skills-sync`.

### Tests (added; none deleted)

- `internal/items/hooks_test.go`: `TestRootDoneFiresOnlyWhenARootReachesDone`.
- `internal/items/transition_test.go`: `TestCancelCascadesToUnfinishedDescendants`, `TestOrchestratorStoryCancelCascadesToItsTasks`.
- `internal/runtime/finish_test.go` (new): `TestRootDoneLiveAgentsEndCompletedAfterGrace`, `TestRootDoneFinishesPausedAndPausingAgentsAtOnce`, `TestRootDoneLeavesTheLiveSpikeOrchestratorAlone`, `TestMaterializeDoesNotFinishTheSpikeOrchestrator`, `TestCancelOnADoneRootRecordsCompleted`, `TestHandoffRefusedOnceTheRootIsDone`, `TestReconcileCancelsWorkOnCancelledItems`.
- `scripts/e2e/epicapproval_test.go`: `TestScenarioEpicApprovalLane` gains a root-finish tail. This extends the test in place and removes no assertion.
- `scripts/e2e/cancelcascade_test.go` (new): `TestScenarioCancelCascade`.
- Existing tests: a grep for item cancels in `internal/**/_test.go` and `scripts/e2e` finds none that cancel a parent and expect its children or agents to survive. `TestCancelRules` cancels task, then story, then epic, and still passes unchanged. Any test the full run shows otherwise gets ported in place.

### Reused unchanged

`resolve` / `approval_result`, `reconcileRoot`, `staleAccepts`, `resolveStale`, `resolveAlive`, `resolveDeadInner`, `killCompletedAfter`, the finished-agent pane pass in `Reconcile`, `cancelAgentOperationsTx`, `publishAgentChanged`, `CancelWorkflow`, `tryTransition`, `Materialize`, `checkpoint.go`'s close_spike insert, `SetSessionState`, `LatestSession`, httpapi and mcpserver item update paths, the web and menubar status rendering.

### Deleted

Nothing.

## Verification

### Command order

1. `go test ./internal/items/ -run 'TestRootDone|TestCancelCascades|TestOrchestratorStoryCancel|TestCancelRules|TestDepUnblocked' -race -count=1`
2. `go test ./internal/runtime/ -run 'TestRootDone|TestMaterializeDoesNotFinish|TestCancelOnADoneRoot|TestHandoffRefusedOnce|TestReconcileCancelsWork' -race -count=1`
3. `go test -race ./internal/items/ ./internal/runtime/ ./internal/httpapi/ ./internal/mcpserver/ ./cmd/swarm/ -count=1`
4. `make skills-sync && git status --short internal/install/skills` (the mirror diff must equal the `skills/` diff), then `go test ./internal/install/ ./internal/runtime/ -run 'Skill' -count=1`
5. `make vet fmt`, then `go test -race ./...`
6. `make e2e` (fake adapter, own port 17778 and tmux socket `swarm-e2e`; never the live daemon)

### End-to-end scenarios

| # | Scenario | Expected | Covered by |
|---|---|---|---|
| R1 | Epic accepted with its orchestrator live | `approval_result approved` reaches the orchestrator. Epic Done. One `completed` checkpoint with `daemon_written = 1` on the orchestrator's own item and attempt, summary `EPIC-1 is done; Swarm finished this agent.` The pane is not killed before 60 s and is killed after. Once it is gone the session is `completed` and the agent `finished`. Live workers on the root get the same. | `TestRootDoneLiveAgentsEndCompletedAfterGrace`, e2e `TestScenarioEpicApprovalLane` tail (asserts the checkpoint, then `killPane` stands in for the 60 s reaper, then `completed` / `finished`) |
| R2 | Epic accepted while the orchestrator is paused | The session becomes `completed` at once and the agent `finished`, with no checkpoint written. Its in-flight operation is `cancelled`. A `stopping` worker is also finished at once. | `TestRootDoneFinishesPausedAndPausingAgentsAtOnce` |
| R3 | Late handoff after acceptance | `swarm_checkpoint kind:"handoff"` returns conflict `Root accepted; write completed.`, and `completed` is still accepted. | `TestHandoffRefusedOnceTheRootIsDone`, e2e tail |
| R4 | Manual close on an accepted root (legacy: root Done before this shipped, agent still active) | `POST /api/agents/{name}/cancel` ends a live session `completed` and a paused one `completed`, and the agent `finished`. On a root that is not Done a live session still ends `cancelled` (existing tests). | `TestCancelOnADoneRootRecordsCompleted`, existing Cancel tests |
| R5 | Spike close path unchanged | A spike orchestrator's live session gets no daemon checkpoint when the spike reaches Done. It keeps its own `completed` + resolution path. | `TestRootDoneLeavesTheLiveSpikeOrchestratorAlone`, existing `TestCloseSpikeRelaysNativePrompt`, e2e `closespike_test.go` |
| R6 | Materialize path unchanged | `swarm_materialize` with `RootDone` wired: the spike is Done, the call succeeds, the orchestrator has no daemon checkpoint and is still `active` with a live session. | `TestMaterializeDoesNotFinishTheSpikeOrchestrator`, existing `materialize_test.go`, e2e `spike_test.go` |
| R7 | Hook scope | A story deriving Done does not fire `RootDone`. An epic reaching Done fires it exactly once with the epic's id. | `TestRootDoneFiresOnlyWhenARootReachesDone` |
| C1 | Cancel epic | Stories and tasks that are Ready, In progress, Blocked or Draft (nested story → task) become Cancelled. A Done task stays Done. | `TestCancelCascadesToUnfinishedDescendants`, e2e `TestScenarioCancelCascade` |
| C2 | Orchestrator cancels a story | Its tasks become Cancelled and the epic is untouched. | `TestOrchestratorStoryCancelCascadesToItsTasks` |
| C3 | Agents and workflows on cancelled items | After one Reconcile tick: the workflow on the cancelled task is `cancelled` and its runs `cancelled`. The coder on the cancelled task is `finished` with session `cancelled` and its pane killed. The coder on the Done sibling task and the epic orchestrator stay `active`. | `TestReconcileCancelsWorkOnCancelledItems`, e2e `TestScenarioCancelCascade` (worker and orchestrator `finished` / `cancelled` after an epic cancel) |
| C4 | Reopen parent | Cancelled → Ready on the epic leaves every cascaded child Cancelled and restarts nothing. | `TestCancelCascadesToUnfinishedDescendants`, e2e `TestScenarioCancelCascade` |
| C5 | Error path: denied cancel | An orchestrator cancelling its own root is still refused (`Couldn't update status. The item remains Ready.`) and no child changes. | existing `TestCancelRules` |
| C6 | Exhaust / idempotency | A second Reconcile tick finds nothing left to cancel (the agent is finished and the workflow cancelled), so there are no extra kills. | `TestReconcileCancelsWorkOnCancelledItems` (second tick adds no kill) |

## Explicitly out of scope

- A new relay kind (`root_accepted`) or a `next` field on `approval_result`.
- Refusing `swarm_control pause` on a done root, or special handling of a pause requested inside the 60 s grace (caveat above).
- Finishing agents when a root is **Cancelled**. That goes through the cascade pass (decision 5), not `RootDone`.
- Reopening children on parent reopen, or restarting agents on reopen.
- Notifications per cascaded child, a "cancelled by parent" marker, or a confirmation dialog on the board.
- UI code changes on the web board or menubar.
- Taking `lockAgentOperations` inside `OnRootDone` (caveat above).
- Changing what Cancel does to a paused session on a root that is not Done.

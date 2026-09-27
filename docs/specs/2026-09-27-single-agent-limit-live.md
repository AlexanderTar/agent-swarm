# One agent limit, applied live

Date: 2026-09-27. Branch `feat/cap-change-applies` (worktree
`/Users/alexandertar/GitHub/agent-swarm-cap-apply`, off `origin/main` 4d5ef65).
Plan: `docs/plans/2026-09-27-single-agent-limit-live.md`.

## Context

User requests (verbatim):

1. "when a max agent setting is changing by the user, this should apply to the
   current queue - if the new limit is lower than the previous one, pause some
   agents, otherwise make queued agents active"
2. "there is only one limit, max concurrent agents, any other limits should be
   removed, to avoid confusion"

Today (verified on `origin/main` 4d5ef65):

- Three limits exist. `Settings.MaxConcurrentAgents` (`max_concurrent_agents`,
  default 4, 1..32, `internal/settings/settings.go:52`) is the global pool,
  orchestrators included. `MaxAgentsPerRoot` (`max_agents_per_root`, default
  4, 1..16, `:53`) is checked by `Admit` for non-orchestrators
  (`internal/runtime/limits.go:144-149`). `MaxConcurrentSubagents`
  (`max_concurrent_subagents`, default 3, 1..16, `:54`) is enforced by
  `Store.SubagentSlots` (`limits.go:405`), which the `swarm_spawn` PreToolUse
  hook uses to block (`internal/hook/handler.go:659-697`) and the workflow
  engine uses to hold runs in `waiting` (`internal/runtime/workflow.go:851`).
- Stale rows `max_orchestrators` / `max_agents` may still sit in the
  `settings` table. `Settings.Get` skips unknown keys (`settings.go:136`), so
  they are inert.
- Only the menubar writes settings (`HTTPDaemonClient.swift:138` → `PUT
  /api/settings`, `internal/httpapi/config.go:71` → `Settings.Put`,
  `settings.go:174`). The menubar Limits tab shows only "Max concurrent
  agents" and "Pause deadline" (`SettingsView.swift:176-194`), but
  `SettingsModel.Limit` still has a `.subagents` case and `Wire.Settings`
  still carries both removed keys. The web reads `max_concurrent_agents`
  (`web/src/logic/spawnForm.ts:15`) and types `max_agents_per_root`
  (`web/src/types.ts:280`).
- Raising the limit already drains queued spawns within one reconcile tick
  (`DrainQueue`, `limits.go:161`, called from `Reconcile`,
  `reconcile.go:239`, 5 s loop). Lowering it does nothing to running agents.
  The menubar confirm says so: `Copy.lowerLimit` (`Copy.swift:257`) — "They
  keep running; new agents wait for a free slot."
- Reusable machinery: `RequestReplacement(id, ModeHandoff, key, note)`
  (`replacement.go:88`) asks a live agent to save a handoff checkpoint
  (`beginPreservation`, `:534`), stops it into `interrupted` (which
  `NotAZombieSlot` does not count, `limits.go:66`), parks the operation in
  phase `queued`, where `admitOperation` (`:759`) calls `Admit` each tick via
  `ResumeOperations` (`:328`, called at `reconcile.go:260`), and
  `startSuccessor` (`:886`) restarts the same agent row from the checkpoint.
  The preservation window is bounded by the pause deadline
  (`waitForPreservation`, `:559`). Concurrency: `lockAgentOperations` (`:423`),
  `casPhaseTx` (`:402`), and the one-open-operation unique index.
- Manual `Resume` (`pause.go:874`) never calls `Admit`, so it can exceed the
  cap.

Affected: Go daemon (`internal/settings`, `internal/runtime`,
`internal/hook`, `internal/httpapi`, `internal/notifyrules`), menubar
(`apps/menubar`), web (`web/src`), skills (`skills/swarm-orchestrator`,
`skills/swarm-spike`, synced into `internal/install/skills`), e2e harness.

Collision warnings:

- `fix/quota-reset-wake` edits `internal/runtime/wake.go`, `cmd/swarm/daemon.go`
  `checkQuotaResets` and `internal/adapter/codex.go`. This change touches none
  of them.
- The new-orchestrator-dialog branch rewrites
  `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift` and its files.
  This change does not touch them. `NewOrchestratorView` does not read the
  removed settings (verified by grep).
- `reconcile.go` is a hot file. The edit is confined to the ordering block at
  `reconcile.go:230-265`.

## Locked decisions

1. **One limit.** `MaxAgentsPerRoot` and `MaxConcurrentSubagents` are removed
   everywhere: Go settings fields, defaults, validation, `Admit`'s per-root
   check, `SubagentSlots`, the hook budget block, the workflow budget gate,
   the API wire, menubar wire/model/UI/copy, web types/fixtures, skills text,
   the e2e harness rows. `max_concurrent_agents` keeps its meaning: every
   role, orchestrators included, 1..32, default 4.
2. **No migration.** `db.migrate` returns `ErrTooNew` when `user_version`
   exceeds the embedded file count (`internal/db/db.go:76-78`), so a new
   `0017` would stop a rolled-back daemon from opening the DB, all to delete
   rows `Settings.Get` already ignores. Instead `Settings.Put` deletes the
   retired keys (`max_orchestrators`, `max_agents`, `max_agents_per_root`,
   `max_concurrent_subagents`) inside its existing transaction.
3. **Continuous rule, not a save hook.** `Store.EnforceCapacity` runs every
   reconcile tick. While slot-holders minus in-flight capacity pauses exceed
   the cap, it hands off the newest eligible agents with
   `RequestReplacement(id, ModeHandoff, "capacity:<latest session id>", "")`.
   The note is empty: a note is pasted into the successor's prompt.
4. **Eligibility.** A candidate is `agents.state = 'active'`, passes
   `NotAZombieSlot`, and its latest session (same order as `LatestSession`:
   `generation DESC, attempt DESC`) is in state `running` (not `spawning`,
   not pausing). Skipped:
   - an agent with any nonterminal operation;
   - an agent that already has an operation keyed `capacity:<that session>`,
     in any phase, terminal ones included;
   - an agent with an open request (`requests.agent_id`, `state = 'open'`);
   - an agent that sent a `question` message that has no answer (the same
     answer test as `notifyUnansweredQuestions`, `reconcile.go:1768`, with no
     age cutoff);
   - an orchestrator with live children (`liveChildNames`, `replacement.go:815`).

   Order: workers before orchestrators, then `created_at DESC, id DESC`
   (newest first).
5. **Idempotent ticks and no flapping.**
   - The reduction needed is `holders − pending − cap`. `holders` is `Admit`'s
     count. `pending` is how many of those holders have a nonterminal
     `capacity:` operation (they are still saving, in `requested` or
     `preserving`).
   - The key is per session. A replay returns the same operation.
   - A capacity operation that ended `blocked` or `cancelled` uses up that
     session's key, so the daemon never re-pauses that session. This is
     deliberate. A user who cancels a capacity pause is not overruled 5 s
     later.
   - Pausing only starts when holders exceed the cap. Resuming only happens
     when `Admit` passes (holders below the cap). At exactly the cap, nothing
     moves.
6. **Priority.** The tick order becomes `TickPause → EnforceCapacity →
   ResumeOperations → DrainQueue`. `ResumeOperations` moves up from after
   `recoverWorkflows` and orders by `created_at, id` (not `updated_at`, which
   changes on every phase swap). Agents paused for capacity therefore resume
   before new queued spawns, oldest pause first.
7. **Manual Resume respects the cap.** If `Admit` refuses for the agent,
   `Resume` records `RequestReplacement(id, ModeHandoff,
   "resume:<latest session id>", "")` and returns the agent with no error. The
   operation walks straight to `queued` (the session has already ended) and
   starts when a slot frees. A repeat Resume for the same session returns the
   agent, not a 409, and the idempotency row is recorded. The successor's
   kickoff mode is `"resume"`, the same one `Resume` uses for a fresh launch.
8. **Marker.** The request-key prefix is the only marker. `OperationReason(key)`
   returns `"capacity"`, `"resume"` or `""`. The replacement wire gains
   `reason` (omitted when empty).
9. **Notifications.**
   - `stopPredecessor` raises `agent.capacity_paused` instead of
     `agent.interrupted` for a capacity operation.
   - `startSuccessor` raises no `agent.retried` for capacity or resume
     operations.
   - The parent relay (`event: "handoff"`) is unchanged.
10. **Workflows.** With no subagent budget, `fillWaitingRuns` spawns every
    waiting run at once. The run's agent queues in the global pool when the
    pool is full. `olderWaitingRunElsewhere` is deleted: it only existed for
    budget fairness, and with no budget it would only add stall-scan delay.
    `advanceWaitingForOwner` stays (it retries spawn-failed `waiting` rows).
    An `active` run whose agent is `queued` with no session is safe:
    `healStrandedActiveRuns` skips a missing session (`workflow.go:595`).
11. **Hook.** The `swarm_spawn` budget block is deleted. `NoAckChildren` loses
    its only caller (`handler.go:684`) and is deleted. The async `agent.no_ack`
    relay and notification are unchanged.
12. **Actions while a capacity or resume operation is in flight.** Menubar
    and board offer Cancel only. Resume would 409 ("A replacement is already
    in progress").

Assumptions on open questions:

- The menubar lower-limit count stays `overLimit(.agents, v)`: running agents
  minus the new limit. Agents that the skip rules protect make that count an
  upper bound. The copy says "will pause", and the backlog clears as agents
  finish.
- `swarm_control resume` from an orchestrator gets the same queued behaviour.
  Its MCP result shape is unchanged. The agent's state still reads as not
  running until the slot frees.

## DB models

No schema change and no migration (decision 2).

Rows touched:

| Table | Change |
|---|---|
| `settings` | `Settings.Put` runs `DELETE FROM settings WHERE key IN ('max_orchestrators','max_agents','max_agents_per_root','max_concurrent_subagents')` in its transaction |
| `agent_operations` | New `request_key` conventions: `capacity:<session id>` and `resume:<session id>`. Mode is `handoff` for both. Existing columns and index are unchanged. |

## Model / API types

### Go — `internal/settings/settings.go`

```go
type Settings struct {
	EnabledAgents       []kinds.AgentKind          `json:"enabled_agents"`
	Roles               map[kinds.Role]RoleDefault `json:"roles"`
	FallbackDefault     RoleDefault                `json:"fallback_default"`
	Notifications       map[string]NotifyPref      `json:"notifications"`
	MaxConcurrentAgents int                        `json:"max_concurrent_agents"`
	ScanExcludes        []string                   `json:"scan_excludes"`
	ScanIntervalSec     int                        `json:"scan_interval_sec"`
	MenubarCompact      bool                       `json:"menubar_compact"`
	UsagePollSec        int                        `json:"usage_poll_sec"`
	PauseDeadlineSec    int                        `json:"pause_deadline_sec"`
	Instructions        string                     `json:"instructions"`
}

// retiredKeys are settings rows no field reads any more; Put deletes them.
var retiredKeys = []string{"max_orchestrators", "max_agents", "max_agents_per_root", "max_concurrent_subagents"}
```

A PUT body that still carries a removed key is accepted: `readJSON` does not
reject unknown fields (`internal/httpapi/server.go:309`), so the key is
dropped.

### Go — `internal/runtime/limits.go`

```go
// Admit reports whether a new agent may start now: one global pool,
// every role (spec 2026-09-27-single-agent-limit-live).
func (s *Store) Admit(ctx context.Context, tx *sql.Tx, role Role, rootItemID string) (bool, error)
// (signature unchanged so call sites stay; role/rootItemID are unused)

const slotHoldersSQL = `SELECT COUNT(*) FROM agents WHERE state = 'active' AND ` + NotAZombieSlot

// Deleted: SubagentSlots, NoAckChildren.
```

### Go — new `internal/runtime/capacity.go`

```go
const (
	capacityKeyPrefix = "capacity:"
	resumeKeyPrefix   = "resume:"
)

// OperationReason names why the daemon started an operation: "capacity"
// (paused to fit the agent limit), "resume" (a manual resume waiting for a
// slot) or "" (anything else).
func OperationReason(requestKey string) string

// EnforceCapacity hands off the newest eligible slot-holders while more
// agents hold a slot than max_concurrent_agents allows (decisions 3-5).
func (s *Store) EnforceCapacity(ctx context.Context) error

// admitsNow reports whether Admit would let a start this agent now.
func (s *Store) admitsNow(ctx context.Context, a Agent) (bool, error)
```

### Go — `internal/runtime/replacement.go`

- `ResumeOperations`: `ORDER BY created_at, id`.
- `stopPredecessor`: when `OperationReason(op.RequestKey) == "capacity"`, the
  notification kind is `agent.capacity_paused`, with args `{"name": a.Name,
  "KEY": key}`.
- `startSuccessor`: `succMode` is `"resume"` when the reason is `"resume"`.
  `agent.retried` is raised only when the reason is `""`.

### Go — `internal/runtime/pause.go` `Resume`

The signature is unchanged. New steps between `PeekIdempotent` and the
existing launch:

1. `ses := LatestSession`.
2. If `PendingOperation` returns an operation keyed `resume:<ses.ID>`, return
   the agent.
3. `refuseIfOperationInFlight`, then the existing state guard.
4. If `!admitsNow`: call `RequestReplacement(ctx, a.ID, ModeHandoff,
   resumeKeyPrefix+ses.ID, "")`, record the idempotency row through `IdemTx`
   (`out = a`), and return `a`.

### Go — `internal/httpapi/handoff.go`

```go
type replacementWire struct {
	OperationID string `json:"operation_id"`
	Agent       string `json:"agent"`
	Mode        string `json:"mode"`
	Phase       string `json:"phase"`
	RequestKey  string `json:"request_key,omitempty"`
	Reason      string `json:"reason,omitempty"` // "capacity" | "resume"
	Error       string `json:"error,omitempty"`
}
```

### Go — `internal/notifyrules/notifyrules.go`

```go
"agent.capacity_paused": {"info", "Agent paused", "{name} paused to fit the agent limit. It resumes when a slot frees.", "swarm.info"},
```

The menubar `Notifier.category(forKind:)` falls through to `swarm.info` for
an unknown kind, so it needs no change.

### Swift — `apps/menubar/Sources/SwarmBarKit`

```swift
// Wire.swift
public struct Settings {            // removed: maxConcurrentSubagents, maxAgentsPerRoot (+ CodingKeys, init params, decode/encode)
    public var maxConcurrentAgents: Int
    ...
}
public struct AgentReplacement: Codable, Sendable, Equatable {
    public var operationID: String
    public var mode: String
    public var phase: String
    public var reason: String?     // new, decodeIfPresent via Optional
    public var error: String?
    enum CodingKeys: String, CodingKey { case mode, phase, reason, error; case operationID = "operation_id" }
    public init(operationID: String, mode: String, phase: String, reason: String? = nil, error: String? = nil)
}

// SettingsModel.swift
public enum Limit: CaseIterable, Sendable { case agents, pauseDeadline }   // .subagents removed
```

I checked the decoders. `AgentReplacement.mode` and `.phase` are `String`, so
new values cannot fail to decode. `reason` is `Optional`. `RequestKind` gets
no new value (the new notification is not a request). `AgentState` and
`SessionState` get no new value.

### TypeScript — `web/src/types.ts`

```ts
export interface AgentReplacement {
  operation_id: string;
  agent: string;
  mode: string;
  phase: string;
  request_key?: string;
  reason?: "capacity" | "resume";
  error?: string;
}
export interface AgentNode { /* existing fields */ replacement?: AgentReplacement }
export interface Settings { /* max_agents_per_root removed */ }
```

`web/src/logic/agentActions.ts`:

```ts
export type DisplayState = SessionState | "queued" | "waiting" | "stale" | "preflight_failed" | "capacity_paused" | "resume_queued";
```

## Screens

### Menubar Settings, Limits tab (one limit)

```
┌ Limits ─────────────────────────────────────────────────────┐
│ Max concurrent agents   [ 4 ]▲▼                             │
│ Maximum agents running at once, every role including        │
│ orchestrators.                              (.caption, 2nd) │
│                                                             │
│ ── only after lowering below the running count ──           │
│ 2 agents will pause and resume when a slot frees.  [Apply]  │
│                                                  (.caption) │
│                                                             │
│ Pause deadline          [ 120 ]▲▼ seconds                   │
└─────────────────────────────────────────────────────────────┘
```

- There is no per-item or subagent field, and there never was one on this
  tab. The dead `.subagents` case and `Copy.maxAgentsPerItem` go.
- Raising the limit, or lowering it to a value at or above the running count,
  saves at once with no notice.

### Menubar agent row, line 2 (subtitle)

```
login-form-coder                                   [Cancel]
Coder · TASK-12 · Paused: over the agent limit

billing-review                                     [Cancel]
Reviewer · TASK-9 · Queued: waiting for a free slot
```

The status replaces the handoff phase label for every phase except
`starting`, which keeps "Starting successor…". Actions are Cancel only.

### Board (web) agent row

```
● login-form-coder   Coder   TASK-12   ○ Paused: over the agent limit   [Cancel]
● billing-review     Rev.    TASK-9    ● Queued: waiting for a free slot [Cancel]
```

- `capacity_paused` uses the `hollow` dot, like `paused`. `resume_queued`
  uses `grey`, like `queued`.
- The Kanban card uses the same `stateLabel`.
- Not shown: the operation id, the key, or a countdown.

## User-facing copy

| Where | Key | Text |
|---|---|---|
| Menubar + board status | `Copy.capacityPaused` / `SESSION_LABEL.capacity_paused` | `Paused: over the agent limit` |
| Menubar + board status | `Copy.resumeQueued` / `SESSION_LABEL.resume_queued` | `Queued: waiting for a free slot` |
| Menubar lower-limit confirm | `Copy.lowerLimit(n)` | `{n} agents will pause and resume when a slot frees.` |
| Notification | `agent.capacity_paused` | title `Agent paused`, body `{name} paused to fit the agent limit. It resumes when a slot frees.`, level `info`, category `swarm.info` |
| Removed | `Copy.maxAgentsPerItem` | (deleted) |
| Removed | validation | `Maximum concurrent agents per item must be between 1 and 16.` and `Maximum concurrent subagents per parent must be between 1 and 16.` (deleted) |
| Removed | hook | `[swarm] Subagent budget exceeded (max %d active)…` (deleted) |
| Skill | `skills/swarm-orchestrator/SKILL.md:28` | replace the budget sentences with: `There is one limit: the user's max concurrent agents, every role including you. A child spawned past it is queued and starts on its own when a slot frees (swarm_read shows it queued); don't cancel and respawn it. An agent can also be paused to fit a lowered limit; it resumes on its own.` |
| Skill | `skills/swarm-orchestrator/SKILL.md:40` | `release its budget slot` → `release its agent slot`; delete the sentence `A swarm_spawn refusal ("Subagent budget exceeded") now names any such child directly in its reason;` and start the remainder `Give it a little longer…` |
| Skill | `skills/swarm-spike/SKILL.md:11` | `in parallel within budget` → `in parallel` |

The existing copy stays: `Maximum agents running at once, every role including
orchestrators.`, `Maximum concurrent agents must be between 1 and 32.`

## File list

Changed (Go):

- `internal/settings/settings.go`: remove two fields, defaults and
  validation; add `retiredKeys` and delete them in `Put`.
- `internal/runtime/limits.go`:
  - `Admit` drops the per-root check.
  - Add `slotHoldersSQL`.
  - Delete `SubagentSlots` and `NoAckChildren`.
  - Update the `NotAZombieSlot` comment's list of callers.
- `internal/runtime/capacity.go` (new): `OperationReason`, `EnforceCapacity`,
  `admitsNow`.
- `internal/runtime/replacement.go`: `ResumeOperations` order,
  `stopPredecessor` notification kind, `startSuccessor` mode and notification.
- `internal/runtime/pause.go`: `Resume` queues when full.
- `internal/runtime/reconcile.go`: tick order (`EnforceCapacity`,
  `ResumeOperations` before `DrainQueue`).
- `internal/runtime/workflow.go`: `fillWaitingRuns` without the budget; delete
  `olderWaitingRunElsewhere`; fix budget comments.
- `internal/hook/handler.go`: delete the `swarm_spawn` budget block.
- `internal/httpapi/handoff.go`: `Reason` on `replacementWire`.
- `internal/notifyrules/notifyrules.go`: `agent.capacity_paused`.

Changed (tests):

- `internal/settings/settings_test.go`
- `internal/httpapi/config_test.go`
- `internal/httpapi/handoff_test.go`
- `internal/notify/notify_test.go`
- `internal/runtime/limits_test.go`
- `internal/runtime/workflow_test.go`
- `internal/runtime/reconcile_test.go`
- `internal/runtime/capacity_test.go` (new)
- `internal/hook/handler_test.go`
- `internal/advisor/queue_test.go` (comment)
- `scripts/e2e/harness_test.go`

Changed (menubar):

- Sources: `Sources/SwarmBarKit/Wire.swift`, `SettingsModel.swift`,
  `Copy.swift`, `AgentActions.swift`.
- Tests: `Tests/SwarmBarTests/SettingsModelTests.swift`,
  `SettingsRenderTests.swift`, `FixtureTests.swift`, `HandoffTests.swift`.
- Fixtures: `Tests/Fixtures/settings.json`, `state.json`, `state-empty.json`,
  `events/stream.sse`.

Changed (web):

- `web/src/types.ts`
- `web/src/copy.ts`
- `web/src/logic/agentActions.ts`
- `web/src/logic/agentActions.test.ts`
- `web/src/mock/fixtures.ts`

Changed (skills):

- `skills/swarm-orchestrator/SKILL.md`
- `skills/swarm-spike/SKILL.md`
- `internal/install/skills/**` (via `make skills-sync`)

Reused unchanged:

- `RequestReplacement`, `advanceOperation`, `beginPreservation`,
  `waitForPreservation`, `admitOperation` and `liveChildNames`.
- `TickPause`, `DrainQueue` and `NotAZombieSlot`.
- `menubar Notifier.category`.
- `web spawnForm.orchestratorsBusy`.
- `SettingsView.LimitsTab` (it already shows only the two fields).

Deleted code:

- `SubagentSlots` and `NoAckChildren` (`limits.go`).
- `olderWaitingRunElsewhere` (`workflow.go`).
- The hook budget block (`handler.go:659-697`).
- `Copy.maxAgentsPerItem`.

Deleted tests. Each covers behaviour that is intentionally removed:

| Test | Why |
|---|---|
| `internal/runtime/limits_test.go` `TestSubagentSlotsMatchesHookCount` | `SubagentSlots` and the subagent budget are removed. |
| `internal/runtime/limits_test.go` `TestNoAckChildren` | `NoAckChildren` existed only for the budget-block reason, which is removed. |
| `internal/hook/handler_test.go` `TestPreToolUseSurfacesNoAckChildrenInBudgetBlockReason` | The budget block is removed. |
| `internal/hook/handler_test.go` `TestPreToolUseSurfacesNoAckChildAfterResumeWithZeroNewCheckpoints` | The same no-ack text in the budget block is removed. |

Ported (not deleted):

- `TestAdmitEnforcesThePerRootLimit` → `TestAdmitHasNoPerRootLimit`: the
  second worker in one root starts even with a stale `max_agents_per_root = 1`
  row present.
- `TestPreToolUseBlocksSubagentsWhenBudgetExceeded` →
  `TestPreToolUseNeverBudgetBlocksSwarmSpawn`.
- `TestLoweringALimitQueuesTheNextSpawnAndLeavesRunningAgentsAlone` →
  `TestLoweringALimitQueuesTheNextSpawn`. The "running agent survives Admit"
  assertion stays; the capacity pause is covered in `capacity_test.go`.
- `TestSlotReleaseSpawnsWaitingRun`, `TestFIFOAcrossWorkflows` and
  `TestCancelledWorkflowIgnoresLaterSlotRelease` move to the global pool.
- `TestReplayAdvanceAttachesReviewWorktree` drops its `setMaxSubagents` line.
- In `TestSettingsRoutes`, the `MaxAgentsPerRoot = 0` assertion becomes a
  check that a body carrying `max_agents_per_root` saves fine.
- Settings and menubar tests drop the fields.

## Verification

Command order (batch gates):

1. `go test ./internal/settings/... ./internal/runtime/... ./internal/hook/... ./internal/httpapi/... ./internal/notify/... ./internal/advisor/...`
2. `make skills-sync && git diff --stat internal/install/skills`
3. `go vet ./... && gofmt -l internal cmd scripts` (empty)
4. `go test -race ./...`
5. `cd web && pnpm test && pnpm exec tsc --noEmit`
6. `cd apps/menubar && swift build && swift test`
7. `grep -rn "max_agents_per_root\|max_concurrent_subagents\|MaxAgentsPerRoot\|MaxConcurrentSubagents\|maxAgentsPerRoot\|maxConcurrentSubagents\|SubagentSlots\|NoAckChildren\|Subagent budget" --include='*.go' --include='*.swift' --include='*.ts' --include='*.tsx' --include='*.json' --include='*.sse' --include='*.md' . | grep -v "^./docs/\|node_modules"` returns only the `retiredKeys` literal (`settings.go`) and the tests that seed stale rows on purpose (`TestPutDeletesRetiredLimitKeys`, `TestAdmitHasNoPerRootLimit`, `TestSettingsRoutes`).

End-to-end scenarios (each is a Go test in `capacity_test.go` unless noted):

1. **Lower by N.** Cap 8 → three running workers → cap 1 → `EnforceCapacity`.
   - The two newest workers each get one `capacity:<ses>` handoff operation.
   - Their sessions end `interrupted`, and the operations park in `queued`
     (no panes, so the walk runs synchronously).
   - The oldest worker is still `running`.
   - Two `agent.capacity_paused` notifications are raised.
2. **Idempotent ticks.** Call `EnforceCapacity` twice more: no new operation
   rows.
3. **In-flight counts.** A holder whose capacity operation is still
   `preserving` (a live pane, so it parks) counts toward the reduction. A
   second call pauses no extra agent.
4. **Skip rules.** Each case alone is over the cap by one, and nothing is
   paused. Then a plain worker is added, and only that worker is paused.
   - An orchestrator with a live child.
   - A worker with an open request.
   - A worker with an unanswered question.
   - A worker with a nonterminal non-capacity operation.
   - A worker in `spawning`.
   - A worker whose `capacity:<ses>` operation is `cancelled`.
5. **Order.** A worker and an orchestrator with no children, one over the cap:
   the worker is paused. Two workers: the newer one is paused.
6. **Raise drains paused first** (Reconcile-level). Cap 1, worker A running
   with a live pane, worker B capacity-paused (operation `queued`), worker C
   queued as a spawn. Raise the cap to 2 and run `Reconcile`.
   - B's operation reaches `succeeded`, and B has a new session.
   - C is still `queued`.
7. **Priority among paused.** Two capacity operations and cap room for one:
   the operation with the earlier `created_at` succeeds.
8. **Manual resume when full.** The orchestrator holds the only slot (cap 1)
   and the worker's session is `paused`.
   - `Resume` returns the agent with no error. One operation keyed
     `resume:<ses>` exists in `queued`, and no new session starts.
   - A repeat `Resume` returns the agent with no new row.
   - Raise the cap to 2 and run `ResumeOperations`: the successor starts with
     kickoff mode `resume`, and no `agent.retried` is raised.
9. **Manual resume with room.** It behaves as today: an immediate new
   generation and no operation row.
10. **Removed settings gone.**
    - `Settings.Put` deletes retired rows.
    - A PUT body carrying `max_agents_per_root` is accepted (`config_test.go`).
    - `Admit` ignores the per-root count: two workers in one root with cap 8
      both start.
    - `swarm_spawn` is never blocked by child count (`handler_test.go`).
    - Workflow runs spawn at once and queue globally (`workflow_test.go`).
11. **Wire.**
    - `/api/state` carries `replacement.reason = "capacity"` for a
      `capacity:` operation (`handoff_test.go`).
    - Menubar `handoffStatus` shows the two new labels, and its actions are
      Cancel only (`HandoffTests.swift`).
    - Web `displayState`, `stateLabel`, `stateTone` and `agentActions` handle
      both new states (`agentActions.test.ts`).
12. **Error path.** `RequestReplacement` refuses (for example, the root is
    accepted): `EnforceCapacity` logs, skips that agent and tries the next
    candidate, and the tick does not fail.

Manual check after merge (optional, not a gate): with 3 agents running, set
Max concurrent agents to 1 in the menubar. The confirm reads "2 agents will
pause and resume when a slot frees." Within about 5 s plus the pause deadline,
two rows read "Paused: over the agent limit". Set the limit back to 3, and
both resume within one tick.

## Explicitly out of scope

- **Priority across ticks.** A `swarm_spawn` between ticks calls `Admit` at
  once and can take a slot that a finishing agent just freed before the next
  tick's `ResumeOperations`. Priority holds within a tick only.
- **Deadlock.** N orchestrators can fill the cap while all their children
  queue. This predates the change. Pausing orchestrators whose children are
  only queued is not attempted.
- **Provider reattach.** A resume that had to queue restarts as a fresh
  generation from its handoff checkpoint. Codex and Claude provider-session
  reattach (`Resume`'s `resume=true` path) is lost for that case only.
- **Exact confirm count.** The menubar count is running agents minus the
  limit. It is not reduced by the skip rules.
- **Save hooks.** No settings-change event triggers enforcement early; the
  5 s tick is the only driver.
- **Rate limit.** There is no per-agent cooldown beyond the per-session key.
- **Other limits.** Workflow round and retry budgets (`MaxRounds`,
  `Retries`) are not concurrency limits and stay.
- **History docs.** Older specs and plans that mention the removed settings
  are not rewritten.

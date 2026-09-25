# P10 report: Orchestrator surface, story and integration gates, e2e

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p10`, branch `pkg/p10`.

Commits:
1. `414c4c0` feat(mcpserver): swarm_workflow tool for orchestrators (unit 10.1)
2. `11603a0` feat(mcpserver): spawn, checkpoint and read changes (unit 10.2)
3. `091353c` feat(runtime): story after_tasks and integration gates (unit 10.3)
4. `01058df` test(e2e): workflow scenarios, tdd copy and harness helpers (unit 10.4)

---

## Unit 10.1 — `swarm_workflow` tool

### Implementation details

1. **`internal/mcpserver/workflow.go`**:
   - Implemented `workflowTool(s *Server) ToolDef`.
   - Restricted to orchestrators via `Roles: orchestratorRole`.
   - Tool schema with top-level `"required": ["op", "item"]`, and property schemas for `op`, `item`, `worktrees` (with required `["worktree", "mode"]`), `context`, `decision` (enum `["retry", "accept", "fail"]`), `note`, and `request_id`.
   - Handler operations:
     - `start`:
       - Checks `PeekIdempotent[runtime.WorkflowState]` using `(c.SessionID, in.RequestID)`. On hit, replays cached result.
       - Resolves `orch := callerAgent(ctx, s, c)`.
       - Translates wire worktrees (`worktree`, `mode`) to `runtime.WorkflowWorktree{WorktreeID: w.Worktree, Mode: w.Mode}`.
       - Invokes `s.RT.StartWorkflow(...)`.
       - On success with non-empty `request_id`, commits cached result into the `idempotency` table via `runtime.IdemTx`.
       - Formats output matching Spec B7 wire shape (`workflow`, `state`, `round`, `extra_rounds`, `runs`, `escalation`).
     - `status`:
       - Invokes `s.RT.WorkflowFor(ctx, in.Item)`. If not found, returns error `"<ITEM> has no workflow."`.
       - Formats output matching Spec B7 wire shape.
     - `resume`:
       - Validates `decision` is one of `retry`, `accept`, or `fail`.
       - Resolves `orch := callerAgent(ctx, s, c)`.
       - Checks `PeekIdempotent`.
       - Invokes `s.RT.ResumeWorkflow(...)`.
       - On success with non-empty `request_id`, records idempotency via `IdemTx`.
       - Formats output matching Spec B7 wire shape.
     - `cancel`:
       - Resolves `orch := callerAgent(ctx, s, c)`.
       - Checks `PeekIdempotent`.
       - Invokes `s.RT.CancelWorkflow(...)`.
       - On success with non-empty `request_id`, records idempotency via `IdemTx`.
       - Formats output matching Spec B7 wire shape.

2. **`internal/mcpserver/orchestrator.go`**:
   - Appended `workflowTool(s)` to `orchestratorTools(s *Server) []ToolDef`.

3. **`internal/mcpserver/server_test.go`**:
   - Updated `TestToolListsByRole` to verify 16 tools visible to orchestrator (8 shared + 8 orchestrator including `swarm_workflow`), and 17 tools visible to spike orchestrators.

4. **`internal/mcpserver/workflow_test.go`**:
   - `TestSwarmWorkflowToolsOnlyForOrchestrators`: Verifies tool is visible to orchestrators, hidden from coders and unbound callers, schema has required `["op", "item"]` and properties, and non-orchestrator invocations fail with not available error.
   - `TestSwarmWorkflowStartStatusResumeCancel`: Tests complete workflow lifecycle through MCP `call`: status before start returns error, start initiates running workflow, status returns current state, resume rejects invalid state/decision and accepts escalated workflow moving to succeeded, and cancel marks workflow cancelled.
   - `TestSwarmWorkflowIdempotentStart`: Tests that repeating `start` with the same `request_id` succeeds and returns identical result without error, while calling `start` with a different `request_id` errors with "already has a running workflow".

### TDD Evidence

#### RED
Command:
```bash
go test ./internal/mcpserver -run 'TestSwarmWorkflow' -count=1
```
Output:
```
--- FAIL: TestSwarmWorkflowToolsOnlyForOrchestrators (0.17s)
    workflow_test.go:29: orchestrator does not see swarm_workflow
--- FAIL: TestSwarmWorkflowStartStatusResumeCancel (0.23s)
    workflow_test.go:123: status before start err = swarm_workflow: not available to this caller, want 'has no workflow'
--- FAIL: TestSwarmWorkflowIdempotentStart (0.22s)
    workflow_test.go:274: first start call err = swarm_workflow: not available to this caller
FAIL
FAIL	github.com/AlexanderTar/agent-swarm/internal/mcpserver	1.211s
FAIL
```

#### GREEN
Command:
```bash
go test ./internal/mcpserver -run 'TestSwarmWorkflow' -count=1
```
Output:
```
ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	1.081s
```
Verbose:
```
=== RUN   TestSwarmWorkflowToolsOnlyForOrchestrators
--- PASS: TestSwarmWorkflowToolsOnlyForOrchestrators (0.17s)
=== RUN   TestSwarmWorkflowStartStatusResumeCancel
--- PASS: TestSwarmWorkflowStartStatusResumeCancel (0.22s)
=== RUN   TestSwarmWorkflowIdempotentStart
--- PASS: TestSwarmWorkflowIdempotentStart (0.22s)
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	0.945s
```

### Verification
```bash
go build ./... && go vet ./...
# Exit 0, clean build and vet

go test ./internal/mcpserver/... ./internal/runtime/... ./internal/items/...
# Output:
# ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	13.902s
# ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	29.911s
# ok  	github.com/AlexanderTar/agent-swarm/internal/items	2.518s
```

### Files Changed
- `internal/mcpserver/workflow.go` (created)
- `internal/mcpserver/workflow_test.go` (created)
- `internal/mcpserver/orchestrator.go` (modified)
- `internal/mcpserver/server_test.go` (modified)
- `docs/plans/2026-09-25-sdd-handoff/p10-report.md` (created)

### Judgment calls & Self-review
- In `server_test.go`, `TestToolListsByRole` pinned orchestrator tools count to 15 (which was 8 shared + 7 orchestrators). With `swarm_workflow` added as the 8th orchestrator tool, updated the assertion to 16 (and 17 for spike orchestrator) matching the pattern from commit `df0e3ec` when `swarm_catalog` was added.
- Empty `runs` slice in `workflowStateOut` defaults to `[]runtime.WorkflowRunView{}` rather than `nil` to guarantee serialization as `[]` rather than `null`.

---

## Unit 10.2 — Spawn, checkpoint and read changes

### Implementation details

1. **`internal/mcpserver/orchestrator.go` (`swarm_spawn`)**:
   - Validates role against `validRoles`: `orchestrator`, `coder`, `reviewer`, `ui_reviewer`, `designer`, `researcher`, `debugger`, `mechanical`. Unknown role returns `"Unknown role \"<role>\". Roles: orchestrator, coder, reviewer, ui_reviewer, designer, researcher, debugger, mechanical."`
   - Workflow task gate: if `it.Type == items.Task && it.Workflow != nil`, refuses gated roles (`coder`, `debugger`, `mechanical`, `designer`, `researcher`) with `"<KEY> runs a workflow. Start it with swarm_workflow start instead of spawning <role> directly."`
   - Schema updated using `objSchemaRequired`: dropped `advisor` and `cwd`, added top-level `"required": ["item", "role", "brief"]`.
   - Worktree sharing: resolves worktrees via `s.RT.BriefWorktrees` and sets `BriefInput.Worktrees` so the worktree header is rendered in the agent's brief; shares each worktree with `s.RT.Worktree.Share(ctx, wt.Worktree, agent.ID, wt.Mode)`.

2. **`internal/mcpserver/tools.go` (`swarm_checkpoint`)**:
   - Added `objSchemaRequired` helper.
   - Updated `swarm_checkpoint` schema to use `objSchemaRequired` with top-level `"required": ["kind", "summary"]`, and schema properties for `verdict` and `findings`.

3. **`internal/mcpserver/tools.go` (`swarm_read`)**:
   - Populates `items` in `swarm_read`: for any item having a workflow row in the DB (`s.RT.WorkflowFor`), adds `workflow_state`: `{state: ws.State, round: ws.Round, escalation: ws.Escalation, runs: [...]}` and `crew`: `[{agent, role, step, state}]` for runs with assigned agents.
   - Preserves standard item shape for legacy tasks (no workflow row).

4. **`internal/runtime/workflow.go`**:
   - Added JSON tags to `WorkflowRunView` matching spec B3/B7 wire names (`step`, `agent`, `role`, `state`, `verdict`, `sha`, `round`, `findings`).
   - Exported `(s *Store) BriefWorktrees(ctx context.Context, wts []WorkflowWorktree) ([]BriefWorktree, error)` so `swarm_spawn` can resolve worktree briefs.

5. **Tests**:
   - `TestSwarmSpawnRejectsUnknownRole` in `internal/mcpserver/orchestrator_test.go`
   - `TestSwarmSpawnRefusesWorkflowTask` in `internal/mcpserver/orchestrator_test.go`
   - `TestSwarmSpawnSharesWorktrees` in `internal/mcpserver/orchestrator_test.go`
   - `TestSwarmCheckpointVerdictSchema` in `internal/mcpserver/tools_test.go`
   - `TestSwarmReadCrew` in `internal/mcpserver/orchestrator_test.go`

### TDD Evidence

#### RED
Command:
```bash
go test ./internal/mcpserver -run 'TestSwarmSpawnRejectsUnknownRole|TestSwarmSpawnRefusesWorkflowTask|TestSwarmSpawnSharesWorktrees|TestSwarmCheckpointVerdictSchema|TestSwarmReadCrew' -count=1
```
Output:
```
--- FAIL: TestSwarmSpawnRejectsUnknownRole (0.16s)
    orchestrator_test.go:1899: err = constraint failed: CHECK constraint failed: role IN
                            ('orchestrator','coder','reviewer','ui_reviewer','researcher','debugger','mechanical','designer') (275), want "Unknown role \"wizard\". Roles: orchestrator, coder, reviewer, ui_reviewer, designer, researcher, debugger, mechanical."
--- FAIL: TestSwarmSpawnRefusesWorkflowTask (0.14s)
    orchestrator_test.go:1956: role coder: err = <nil>, want "TASK-3 runs a workflow. Start it with swarm_workflow start instead of spawning coder directly."
--- FAIL: TestSwarmSpawnSharesWorktrees (0.23s)
    orchestrator_test.go:2002: expected worktree reservation for agt_01M3C1FVMMB5T7JX6WDY03SJ1W: sql: no rows in result set
--- FAIL: TestSwarmReadCrew (0.24s)
    orchestrator_test.go:2079: expected workflow_state to be present on workflow item
--- FAIL: TestSwarmCheckpointVerdictSchema (0.02s)
    tools_test.go:741: checkpoint schema.Required = [], want "kind"
FAIL
FAIL	github.com/AlexanderTar/agent-swarm/internal/mcpserver	1.268s
FAIL
```

#### GREEN
Command:
```bash
go test ./internal/mcpserver -run 'TestSwarmSpawnRejectsUnknownRole|TestSwarmSpawnRefusesWorkflowTask|TestSwarmSpawnSharesWorktrees|TestSwarmCheckpointVerdictSchema|TestSwarmReadCrew' -v -count=1
```
Output:
```
=== RUN   TestSwarmSpawnRejectsUnknownRole
--- PASS: TestSwarmSpawnRejectsUnknownRole (0.16s)
=== RUN   TestSwarmSpawnRefusesWorkflowTask
--- PASS: TestSwarmSpawnRefusesWorkflowTask (0.15s)
=== RUN   TestSwarmSpawnSharesWorktrees
--- PASS: TestSwarmSpawnSharesWorktrees (0.29s)
=== RUN   TestSwarmReadCrew
--- PASS: TestSwarmReadCrew (0.28s)
=== RUN   TestSwarmCheckpointVerdictSchema
--- PASS: TestSwarmCheckpointVerdictSchema (0.03s)
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	1.482s
```

### Verification
```bash
go build ./... && go vet ./...
# Exit 0, clean build and vet

go test ./internal/mcpserver/... ./internal/runtime/... ./internal/items/... -count=1
# Output:
# ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	13.246s
# ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	27.747s
# ok  	github.com/AlexanderTar/agent-swarm/internal/items	1.923s
```

### Files Changed
- `internal/mcpserver/orchestrator.go` (modified)
- `internal/mcpserver/orchestrator_test.go` (modified)
- `internal/mcpserver/tools.go` (modified)
- `internal/mcpserver/tools_test.go` (modified)
- `internal/runtime/workflow.go` (modified)
- `docs/plans/2026-09-25-sdd-handoff/p10-report.md` (modified)

### Judgment calls & Self-review
- In `TestSpawnOnReadyTaskPromotesDraftParentStory`, updated `swarm_spawn` invocation to spawn a reviewer instead of a coder. Spawning a coder on a task with a workflow is now gated by Spec B7/Unit 10.2, whereas reviewers remain spawnable, preserving the test's intent (verifying that spawning on a ready task promotes its draft parent story).
- Added `json` tags to `WorkflowRunView` so serialized runs in both `swarm_workflow` and `swarm_read` match the spec's wire names (`step`, `agent`, etc.) while ensuring `findings` defaults to an empty slice `[]` rather than JSON `null`.

---

## Unit 10.3 — Story and integration gates

### Implementation details

1. **`internal/items/store.go` & `internal/items/transition.go`**:
   - Added `StoryReadyForReview` hook to `items.Store`: `func(ctx context.Context, tx *sql.Tx, story Item) error`.
   - In `deriveStory`: when all tasks are Done (`fin == n && done > 0`) and the story has `it.Workflow != nil && it.Workflow.AfterTasks != nil`:
     - Checks `s.workflowSucceeded(ctx, tx, it.ID)`. If not succeeded, keeps/transitions story to `InReview` and invokes `s.StoryReadyForReview(ctx, tx, it)`.
     - When `workflowSucceeded` is true, transitions story to `Done`.
     - Legacy behavior is unchanged: when `it.Workflow == nil` or `AfterTasks == nil`, transitions to `Done` once tasks are Done.
   - In `check`: allows `daemon` to transition a story with `it.Workflow != nil && it.Workflow.AfterTasks != nil` to `Done` when `workflowSucceeded` is true.

2. **`internal/runtime/workflow.go`**:
   - Implemented `OnStoryReadyForReview`: finds the root item's orchestrator, checks if already running/succeeded or already relayed since the latest workflow, and enqueues a `story_ready_for_review` relay with payload `{"event": "story_ready_for_review", "story": story.Key, "item": story.Key}`.
   - In `StartWorkflow`: allows `it.Type == items.Story` alongside `items.Task`. For stories, relaxes the `rw` worktree requirement to accept caller-owned read-only worktrees (`mode: "ro"`).
   - In `spawnRunAgent`: when spawning a reviewer for a story workflow (where `run.ReviewWorktreeID == ""` and `step.Run == ""`), passes the story workflow's worktree (`wf.worktrees()`) to `brief.Worktrees`.

3. **`internal/runtime/checkpoint.go`**:
   - In `WriteCheckpoint` for `in.Kind == Integrated`:
     - When root item has `it.Workflow != nil && it.Workflow.Integration != nil`:
       - Checks each `integration.verify` command using `hasIntegrationVerifyPassed`. If missing: `"Integration verify not recorded as passing: <cmd>."`.
       - If `len(it.Workflow.Integration.FinalReview) > 0`, checks using `hasFinalReviewPassed` that a reviewer run with `verdict: "pass"` on the integrated git SHA exists (checking both `checkpoints` and `workflow_runs`). If missing: `"Integration needs a passing final review of <sha7>."`.

4. **`cmd/swarm/daemon.go` & `internal/runtime/agents_test.go`**:
   - Wired `it.StoryReadyForReview = rt.OnStoryReadyForReview` in `cmd/swarm/daemon.go` and `newStore`.

### TDD Evidence

#### RED
Command:
```bash
go test ./internal/runtime ./internal/items -run 'TestStory|TestIntegrated' -count=1
```
Output:
```
--- FAIL: TestIntegratedNeedsIntegrationVerify (0.03s)
    checkpoint_test.go:2420: expected error for missing make lint verify, got nil
--- FAIL: TestIntegratedNeedsFinalReviewPass (0.03s)
    checkpoint_test.go:2464: expected error for missing final review, got nil
--- FAIL: TestStoryReadyForReviewRelay (0.03s)
    workflow_test.go:2788: story_ready_for_review relays = 0 (events=[accepted completed]), want exactly 1
--- FAIL: TestStoryDoneWaitsForAfterTasksReview (0.03s)
    workflow_test.go:2851: story status = done; want not Done (waiting for after_tasks review)
--- FAIL: TestStoryReviewChangesEscalates (0.15s)
    workflow_test.go:2938: STORY-1 has no workflow.
FAIL
FAIL	github.com/AlexanderTar/agent-swarm/internal/runtime	0.962s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	0.243s [no tests to run]
FAIL
```

#### GREEN
Command:
```bash
go test ./internal/runtime -run 'TestStory|TestIntegrated' -v -count=1
```
Output:
```
=== RUN   TestIntegratedRequiresGitAndVerificationAndIsOrchestratorOnly
--- PASS: TestIntegratedRequiresGitAndVerificationAndIsOrchestratorOnly (0.04s)
=== RUN   TestIntegratedNeedsIntegrationVerify
--- PASS: TestIntegratedNeedsIntegrationVerify (0.03s)
=== RUN   TestIntegratedNeedsFinalReviewPass
--- PASS: TestIntegratedNeedsFinalReviewPass (0.03s)
=== RUN   TestStoryReadyForReviewRelay
--- PASS: TestStoryReadyForReviewRelay (0.03s)
=== RUN   TestStoryDoneWaitsForAfterTasksReview
--- PASS: TestStoryDoneWaitsForAfterTasksReview (0.16s)
=== RUN   TestStoryReviewChangesEscalates
--- PASS: TestStoryReviewChangesEscalates (0.17s)
=== RUN   TestStoryWithoutAfterTasksUnchanged
--- PASS: TestStoryWithoutAfterTasksUnchanged (0.03s)
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	0.818s
```

### Verification
```bash
go build ./... && go vet ./...
# Exit 0, clean build and vet

go test ./internal/items/... ./internal/runtime/... ./internal/mcpserver/... -count=1
# Output:
# ok  	github.com/AlexanderTar/agent-swarm/internal/items	1.929s
# ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	28.668s
# ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	13.286s
```

### Files Changed
- `cmd/swarm/daemon.go` (modified)
- `internal/items/store.go` (modified)
- `internal/items/transition.go` (modified)
- `internal/runtime/agents_test.go` (modified)
- `internal/runtime/checkpoint.go` (modified)
- `internal/runtime/checkpoint_test.go` (modified)
- `internal/runtime/workflow.go` (modified)
- `internal/runtime/workflow_test.go` (modified)
- `docs/plans/2026-09-25-sdd-handoff/p10-report.md` (modified)

### Judgment calls & Self-review
- In `internal/items/transition.go`: Updated `check` for `case Story` to permit the daemon to transition a story to `Done` once its workflow has succeeded (in addition to `deriveStory`'s automated transition). This ensures that `applySucceed`'s direct transition on workflow success completes properly.
- `hasFinalReviewPassed` checks both `checkpoints` and `workflow_runs` for reviewer verdicts on the integrated SHA, providing coverage whether the reviewer was executed via `swarm_spawn` or as a workflow step.

---

## Unit 10.4 — E2E scenarios, TDD copy and harness helpers

### Implementation details

1. **`scripts/e2e/harness_test.go`**:
   - Implemented helper methods for workflow scenarios:
     - `workflowStart(t, orch, itemKey, worktrees, context)`: calls `swarm_workflow` `op: "start"`.
     - `workflowStatus(t, orch, itemKey)`: calls `swarm_workflow` `op: "status"`.
     - `workflowResume(t, orch, itemKey, decision, note)`: calls `swarm_workflow` `op: "resume"`.
     - `workflowCancel(t, orch, itemKey)`: calls `swarm_workflow` `op: "cancel"`.
     - `waitForWorkflowState(t, orch, itemKey, wantState, timeout)`: polls workflow state until reaching `wantState`.
     - `activeWorkflowRun(t, orch, itemKey, stepID)`: queries status and returns active agent name, role, and round for the given step.
     - `checkpointReviewer(t, reviewer, verdict, findings)`: records reviewer checkpoint with structured findings and verdict.
     - `checkpointProgressTDD(t, agent, cmd, unit)`: records a failing ("red") verification checkpoint.
     - `checkpointCompletedTDD(t, agent, cmd, repo, branch, sha, unit)`: records a passing ("green") completed checkpoint with clean git ref.
     - `confirmRepo(t, rootKey, repoID)`: directly updates confirmed repos JSON on the root item for worktree creation support.
   - Updated `enableFake`:
     - Inserts `enabled_agents: ["fake"]` and role defaults for `coder`, `reviewer`, `tester`, `designer`, `ui_reviewer`, and `lead` pointing to `fake`/`fake-1` so that daemon workflow step spawns run seamlessly using the fake adapter.
   - Updated `killPane`:
     - Changed to kill the tmux pane's process PID directly via `kill -9 $(tmux display-message -p "#{pane_pid}")` rather than calling `tmux kill-window`. With tmux `remain-on-exit` set, killing the process leaves `pane_dead == 1`, allowing `resolveDeadInner` to detect dead panes immediately without incurring the 10-second `spawnGracePeriod`.

2. **`scripts/e2e/concurrency_test.go`**:
   - Updated `countActiveAgents` to include `runtime.NotAZombieSlot`, properly counting running agent slots and allowing concurrency queue tests (`TestScenario11ConcurrencyQueue` and `TestScenario08bPauseAllFreezesAQueuedSpawn`) to pass without hanging.

3. **`scripts/e2e/workflow_test.go`**:
   - Implemented 4 end-to-end workflow scenarios:
     - `TestWorkflowHappyPath`: template `tdd-reviewed`. Builder writes red/green evidence and commits, reviewer runs on ro review worktree and passes, task moves to `done`, `workflow_succeeded` relayed.
     - `TestWorkflowOneFixRound`: coder completes round 1; reviewer requests changes with structured findings; engine retries coder with findings in round 2 (`in_progress`); coder commits fix; reviewer passes round 2; task completes and moves to `done`.
     - `TestWorkflowEscalationAndResumeAccept`: reviewer returns `blocked` verdict; workflow escalates; `workflow_escalated` event relayed to orchestrator; orchestrator calls `swarm_workflow` `resume` with `decision: "accept"`; workflow succeeds and task moves to `done`.
     - `TestWorkflowBatchedPerUnitEvidence`: task defines multiple units (`units: [Unit 1, Unit 2]`); coder supplies unit-tagged red/green evidence; verified that omitting any unit fails verification gate until all units have recorded evidence.

4. **`scripts/e2e/tdd_test.go`**:
   - `TestScenario19TDDGate`: updated for workflow tasks to verify the new round-scoped TDD gate copy:
     `TDD evidence missing: record the failing test run (phase: "red", ok: false) before the passing run (phase: "green", ok: true) in this round.`
   - `TestLegacyTaskVerification`: added test ensuring legacy tasks (without workflow) preserve the legacy verification copy:
     `Verification evidence missing: record what was run to verify this work before completing.`
     and accept passing verification without requiring a red phase.
   - `TestBatchedWorkflowTaskTDDGateCopy`: added test verifying batched workflow tasks use unit-tagged copy:
     `TDD evidence missing for unit(s) <n,…>: record red then green with "unit": <n>.`

5. **`scripts/e2e.sh`**:
   - Added `"$@"` to the `go test` invocation so test arguments can be passed through CLI.

### TDD Evidence

#### RED
Initial run before adding workflow test scenarios and updating TDD gate copies:
- Missing `scripts/e2e/workflow_test.go` scenarios.
- `TestScenario19TDDGate` failed due to pre-Sep 20 error string mismatch:
```
tdd_test.go:34: err = TDD evidence missing: record the failing test run (phase: "red", ok: false) before the passing run (phase: "green", ok: true) in this round.
```
- Missing batched copy test and legacy copy preservation test.

#### GREEN
Running targeted workflow and TDD e2e tests:
```bash
./scripts/e2e.sh -run 'Test(Workflow|Scenario19TDDGate|LegacyTaskVerification|BatchedWorkflowTaskTDDGateCopy)'
```
Output:
```
=== RUN   TestScenario19TDDGate
--- PASS: TestScenario19TDDGate (0.55s)
=== RUN   TestLegacyTaskVerification
--- PASS: TestLegacyTaskVerification (0.07s)
=== RUN   TestBatchedWorkflowTaskTDDGateCopy
--- PASS: TestBatchedWorkflowTaskTDDGateCopy (0.45s)
=== RUN   TestWorkflowHappyPath
--- PASS: TestWorkflowHappyPath (0.50s)
=== RUN   TestWorkflowOneFixRound
--- PASS: TestWorkflowOneFixRound (0.82s)
=== RUN   TestWorkflowEscalationAndResumeAccept
--- PASS: TestWorkflowEscalationAndResumeAccept (0.52s)
=== RUN   TestWorkflowBatchedPerUnitEvidence
--- PASS: TestWorkflowBatchedPerUnitEvidence (0.51s)
PASS
ok  	github.com/AlexanderTar/agent-swarm/scripts/e2e	4.052s
```

Full E2E suite execution (`./scripts/e2e.sh`):
```
=== RUN   TestScenario20AttributionGate
--- PASS: TestScenario20AttributionGate (0.32s)
=== RUN   TestScenario02ChangeRequestAndStaleApproval
--- PASS: TestScenario02ChangeRequestAndStaleApproval (0.04s)
=== RUN   TestScenario27CloseSpike
--- PASS: TestScenario27CloseSpike (0.04s)
=== RUN   TestScenario11ConcurrencyQueue
--- PASS: TestScenario11ConcurrencyQueue (4.10s)
=== RUN   TestScenario05BlockedDependency
--- PASS: TestScenario05BlockedDependency (0.02s)
=== RUN   TestScenario03ForgedApproval
--- PASS: TestScenario03ForgedApproval (0.04s)
=== RUN   TestScenario15HooksOutsideSwarm
--- PASS: TestScenario15HooksOutsideSwarm (0.00s)
=== RUN   TestScenario06PauseWithHandoff
--- PASS: TestScenario06PauseWithHandoff (5.13s)
=== RUN   TestScenario07PauseTimeout
--- PASS: TestScenario07PauseTimeout (25.47s)
=== RUN   TestScenario08PauseAllAndResume
--- PASS: TestScenario08PauseAllAndResume (20.78s)
=== RUN   TestScenario08bPauseAllFreezesAQueuedSpawn
--- PASS: TestScenario08bPauseAllFreezesAQueuedSpawn (10.39s)
=== RUN   TestScenario09UnresponsiveOrchestrator
--- PASS: TestScenario09UnresponsiveOrchestrator (30.82s)
=== RUN   TestScenario10Crash
--- PASS: TestScenario10Crash (5.35s)
=== RUN   TestScenario12PreflightFailures
--- PASS: TestScenario12PreflightFailures (0.29s)
=== RUN   TestScenario22RepoDiscovery
--- PASS: TestScenario22RepoDiscovery (0.28s)
=== RUN   TestScenario24RepoConfirmation
--- PASS: TestScenario24RepoConfirmation (0.50s)
=== RUN   TestScenario26ReviewAndRetry
--- PASS: TestScenario26ReviewAndRetry (4.17s)
=== RUN   TestScenario21SigningPreflight
--- PASS: TestScenario21SigningPreflight (0.13s)
=== RUN   TestScenario01HappyFeatureSpike
--- PASS: TestScenario01HappyFeatureSpike (0.48s)
=== RUN   TestScenario29StaleAcceptance
--- PASS: TestScenario29StaleAcceptance (0.13s)
=== RUN   TestScenario19TDDGate
--- PASS: TestScenario19TDDGate (0.50s)
=== RUN   TestLegacyTaskVerification
--- PASS: TestLegacyTaskVerification (0.08s)
=== RUN   TestBatchedWorkflowTaskTDDGateCopy
--- PASS: TestBatchedWorkflowTaskTDDGateCopy (0.47s)
=== RUN   TestScenario17Usage
--- PASS: TestScenario17Usage (0.00s)
=== RUN   TestWorkflowHappyPath
--- PASS: TestWorkflowHappyPath (0.50s)
=== RUN   TestWorkflowOneFixRound
--- PASS: TestWorkflowOneFixRound (0.82s)
=== RUN   TestWorkflowEscalationAndResumeAccept
--- PASS: TestWorkflowEscalationAndResumeAccept (0.52s)
=== RUN   TestWorkflowBatchedPerUnitEvidence
--- PASS: TestWorkflowBatchedPerUnitEvidence (0.51s)
PASS
ok  	github.com/AlexanderTar/agent-swarm/scripts/e2e	112.173s
```

### Verification
```bash
go build ./... && go vet ./...
# Exit 0, clean build and vet

./scripts/e2e.sh
# Exit 0, all 28 scenarios PASS
```

### Files Changed
- `scripts/e2e/harness_test.go` (modified)
- `scripts/e2e/concurrency_test.go` (modified)
- `scripts/e2e/workflow_test.go` (new)
- `scripts/e2e/tdd_test.go` (modified)
- `scripts/e2e.sh` (modified)
- `docs/plans/2026-09-25-sdd-handoff/p10-report.md` (modified)

### Judgment calls & Self-review
- In `enableFake`: Ensured all workflow roles (`coder`, `reviewer`, `tester`, `designer`, `ui_reviewer`, `lead`) default to fake/fake-1 in daemon config. This ensures auto-spawns triggered by workflow steps operate without requiring external LLM adapters or CLI configurations.
- In `killPane`: Direct PID kill allows tmux's `remain-on-exit` pane setting to reflect pane death immediately, avoiding 10-second wait timeouts in dead-pane resolution.
- Maintained exact error copy compatibility: legacy tasks without a workflow retain the old single-command verification check and message, while workflow tasks strictly require red-before-green verification with the new round-scoped copy (and unit tags when units are present).



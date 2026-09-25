# P10 report: Orchestrator surface, story and integration gates, e2e

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p10`, branch `pkg/p10`.

Commits:
1. `414c4c0` feat(mcpserver): swarm_workflow tool for orchestrators (unit 10.1)
2. `11603a0` feat(mcpserver): spawn, checkpoint and read changes (unit 10.2)

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

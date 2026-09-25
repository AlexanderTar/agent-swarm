## Global Constraints

- Everything ships together in one release; no compatibility shims between daemon, web and menubar.
- Legacy behaviour is preserved exactly for tasks whose `workflow_json` is NULL (existing epics, board-created tasks). Every change to checkpoint, transition or spawn code keeps today's legacy-task tests green.
- Never delete or weaken an existing test to make a new one pass. Tests whose expectations change intentionally are named in the unit that changes them.
- After any edit under `skills/`, run `make skills-sync`; `go test ./internal/install/...` enforces the mirror.
- **One commit per unit** (conventional message, e.g. `feat(install): …`, signed, repo style). Review fix rounds add commits; never amend.
- Every Go unit keeps `go build ./... && go vet ./...` green. Web units also run `cd web && pnpm typecheck && pnpm test`. Menubar units also run `cd apps/menubar && swift test`.
- Hot files: `internal/runtime/checkpoint.go`, `internal/runtime/agents.go`, `internal/items/transition.go`. Pull or rebase before starting P7–P10.
- **Package review loop** (every package, once, after all its units are committed):
  1. Run the package Verify set.
  2. Request review with `superpowers:requesting-code-review` against the package Acceptance, walking the diff unit by unit.
  3. Handle findings with `superpowers:receiving-code-review`. Fix, re-run Verify, and commit.
  4. Repeat until the review passes. Stop after the workflow's `max_rounds` and escalate to the human.
  5. If one unit collects all the findings two rounds in a row, split it into a follow-up package instead of re-running the whole package (`swarm-batching` split trigger).

## Review Focus

Spec inputs most likely to be under-tested. Reviewers check these explicitly:

1. Legacy tasks (no workflow) behave byte-for-byte as before across checkpoint, transition and spawn paths.
2. Engine idempotency: a repeated `advance` (daemon restart, duplicate trigger) never double-spawns (`UNIQUE (workflow_id, step_id, round, role)`).
3. `closeCompletedSiblings` no longer tears down a builder when a reviewer completes on the same task, and vice versa.
4. Symlinked skills are actually discovered by each agent CLI. The P1 empirical check decides where the copy fallback is used.
5. Relay suppression: the orchestrator receives exactly one `workflow_succeeded` per successful package. It still receives `blocked`/`failed` checkpoints and questions from step agents.
6. No role is ever inferred. A task without a workflow is rejected at plan registration and on orchestrator `swarm_items create`.


### P7: Roles, guards and item fields
**Workflow:** `tdd-reviewed` · **Units:** 4

**Files:**
- `internal/kinds/kinds.go`
- `internal/runtime/{types.go,titles.go,agents.go}` (`OverridableRoles`)
- `internal/settings/settings.go`
- `internal/hook/handler.go` and its tests
- `internal/items/{model.go,store.go}` and their tests
- `internal/mcpserver/{orchestrator.go,tools.go}` and their tests
- `internal/httpapi/items.go`
- `web/src/types.ts` (Item fields only)

**Interfaces (produces):**
- `kinds.RoleDesigner`
- On `items.Item`, `CreateInput` and `Patch`: `Workflow *workflow.Spec`, `Steps []string`, `Units []Unit`, `Solo string`, `Verify []string`
- The same fields on the wire

**Acceptance:**
- `designer` is a valid role across kinds, settings (default `claude`/`opus`), overrides and the catalog. Its emoji is 🎨.
- `Workflow` and `workflow` tool names are blocked in swarm sessions with the spec copy. They are still allowed outside swarm.
- Items store the resolved workflow and set `role_hint = RunRole(workflow)`.
- A task may not have both `steps` and `units`, and may have at most 8 units.
- Only an orchestrator or the daemon may set these fields.
- An orchestrator `swarm_items create` of a task without a `workflow` is refused with the spec copy. A task the user creates on the board may omit it.
- `swarm_items` accepts the new fields, and `swarm_read` and `GET /api/items/:key` return them.

**Verify:**
- `go test ./internal/kinds/... ./internal/settings/... ./internal/hook/... ./internal/items/... ./internal/mcpserver/... ./internal/httpapi/... ./internal/runtime/...`
- `go build ./... && go vet ./...`
- `cd web && pnpm test`

#### Unit 7.1: Designer role plumbing (Go)
- [ ] Write the failing tests `TestSettingsDefaultsIncludeDesigner`, `TestSpawnDesignerRoleAccepted` and `TestOverridableRolesIncludeDesigner`. Red. Implement. Green. Commit.

#### Unit 7.2: Block the native Workflow tool
- [ ] Write the failing tests `TestPreToolUseBlocksWorkflowTool` and `TestWorkflowToolAllowedOutsideSwarm`. Red. Implement. Green. Commit.

#### Unit 7.3: Item fields in the store
- [ ] Write the failing tests:
  - `TestCreateTaskStoresResolvedWorkflowAndRoleHint`
  - `TestCreateRejectsInvalidWorkflow`
  - `TestCreateRejectsStepsAndUnits`
  - `TestOrchestratorTaskNeedsWorkflow`
  - `TestBoardTaskMayOmitWorkflow`
  - `TestUserCannotSetWorkflow`
- [ ] Red. Implement the model, `scanItem` and the create/update validation. Green. Commit.

#### Unit 7.4: Wire the fields through MCP and HTTP
- [ ] Write the failing tests `TestSwarmItemsAcceptsWorkflowUnitsVerify`, `TestSwarmReadReturnsWorkflowFields` and `TestItemJSONIncludesWorkflowFields`. Red.
- [ ] Implement, and update the web `Item` type. Green. Commit.


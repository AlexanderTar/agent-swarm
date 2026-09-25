## Global Constraints

- Everything ships together in one release; no compatibility shims between daemon, web and menubar.
- Legacy behaviour is preserved exactly for tasks whose `workflow_json` is NULL (existing epics, board-created tasks). Every change to checkpoint, transition or spawn code keeps today's legacy-task tests green.
- Never delete or weaken an existing test to make a new one pass. Tests whose expectations change intentionally are named in the unit that changes them.
- After any edit under `skills/`, run `make skills-sync`; `go test ./internal/install/...` enforces the mirror.
- **One commit per unit** (conventional message, e.g. `feat(install): …`, signed, repo style). Review fix rounds add commits; never amend.
- Every Go unit keeps `go build ./... && go vet ./...` green. Web units also run `cd web && pnpm typecheck && pnpm test`. Menubar units also run `cd apps/menubar && swift test`.
- Hot files: `internal/runtime/checkpoint.go`, `internal/runtime/agents.go`, `internal/items/transition.go`. Pull or rebase before starting P7–P10.
- **Single Coder Per Package & Package-Level Review:** All units within a work package ("P") are addressed sequentially by the same coder agent and reviewed together at the package boundary. Units are structural guidance for the coder's TDD cycles (failing test → red → minimal implementation → green → commit per unit), NOT separate sub-tasks for different workers. The orchestrator must not dispatch separate coder agents for individual units within a package; instead, one coder agent executes all units of the package in sequence, and reviewers review the full package diff once all units are complete.
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


### P11: Board and menubar
**Workflow:** `ui-tdd-reviewed` (coder → reviewer + ui_reviewer) · **Units:** 5

**Files:**
- `web/src/{types.ts,copy.ts,api.ts,components/AgentFields.tsx,components/WorkflowSection.tsx,panels/Details.tsx,mock/fixtures.ts}`
- the kanban card component under `web/src/views/`
- tests for the above
- `internal/httpapi` (item detail `workflow_state`/`crew`; `step` in the agents payload)
- `apps/menubar/Sources/SwarmBarKit/{Wire.swift,Copy.swift,SettingsModel.swift}` and their tests

**Acceptance:**
- Spec Screens and copy, exactly:
  - Designer and chore labels in web and menubar, including the Settings defaults row.
  - A Workflow section in Details (hidden for legacy tasks). It shows findings with a unit tag.
  - A kanban crew row and round badge.
  - "Plan warnings" on the plan review screen.
  - A menubar step suffix.
- Screenshots of the mock board in light and dark are checked with the `run` skill or Playwright.

**Verify:**
- `go test ./internal/httpapi/...`
- `cd web && pnpm typecheck && pnpm test`
- `cd apps/menubar && swift test`

#### Unit 11.1: Designer and chore labels
- [x] Write the failing tests: web `copy.test.ts` (`ROLE_LABEL.designer`, `ITEM_TYPE_LABEL.chore`), Swift `testRoleDecodesDesigner` and `testDefaultsOrderIncludesDesigner`. Red. Implement. Green. Commit. (`11fca8b`)

#### Unit 11.2: HTTP payload fields
- [x] Write the failing tests `TestItemDetailIncludesWorkflowState` and `TestAgentsPayloadIncludesStep`. Red. Implement. Green. Commit. (`47898f1`)

#### Unit 11.3: `WorkflowSection`
- [ ] Write the failing tests: `WorkflowSection.test.tsx` (runs, verdicts, unit-tagged findings, escalation; hidden when there is no workflow) and the `Details.test.tsx` addition. Red. Implement. Green. Commit.

#### Unit 11.4: Kanban crew and plan warnings
- [ ] Write the failing tests for the kanban card (crew row, "+n", "R2" badge) and for the plan review warnings list. Red. Implement. Green. Commit.

#### Unit 11.5: Menubar step suffix
- [ ] Write the failing test `testAgentRowStepSuffix`. Red. Implement. Green. Take the screenshots. Commit.


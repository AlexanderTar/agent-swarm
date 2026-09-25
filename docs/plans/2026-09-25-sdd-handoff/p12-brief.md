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


### P12: Planning emits role-assigned packages
**Workflow:** `tdd-reviewed` · **Units:** 5

**Files:**
- `internal/runtime/{artifacts.go,materialize.go}` and their tests
- `internal/mcpserver/orchestrator.go` (`swarm_artifact` warnings)
- `skills/swarm-{workflows,spike,orchestrator,batching}/SKILL.md`
- `internal/install/skills_test.go`
- `README.md`

**Interfaces (produces):**
- `TreeNode.Workflow`, `.Steps`, `.Units []TreeUnit`, `.Solo`, `.Verify`
- `RegisterArtifactResult.Warnings`
- `func lintTree(Tree) (errs []error, warnings []string)`

**Acceptance:**
- Trees carrying the new fields parse (still `DisallowUnknownFields`) and validate per level.
- Materialize copies the resolved workflow, steps/units, `solo` and verify, and derives `role_hint`.
- Plan registration returns errors and warnings with the exact spec C2 copy. The errors cover:
  - a missing workflow
  - a `role_hint` mismatch
  - review tasks
  - steps and units together
  - more than 8 units
  - reviewer roles on stories or roots
- The warnings cover:
  - split TDD titles
  - no test step
  - single-unit tasks with no `solo`
  - more than 5 units
  - stories made entirely of single-unit tasks
- `swarm-workflows` has the DSL reference, the role assignment table and a worked example that a test parses and validates.
- `swarm-spike` covers the seven-step method, batching via `swarm-batching`, and pre-assigning roles for research and design tasks.
- `swarm-orchestrator`:
  - covers delivery via `swarm_workflow`
  - covers escalation handling, folding follow-ups with no micro-tasks, story reviews, and root integration (including a `ponytail-debt` pass)
  - references `dispatching-parallel-agents`, `subagent-driven-development` and `finishing-a-development-branch`
- `swarm-batching` points to the plan-registration warnings.
- The README reflects the release: roles, skills, vendored licenses, `swarm_workflow`, and how work flows.

**Verify:**
- `make skills-sync`
- `go test ./internal/runtime/... ./internal/install/... ./internal/mcpserver/...`
- `cd web && pnpm test`

#### Unit 12.1: Tree fields and materialize
- [ ] Write the failing tests `TestParseTreeWithWorkflowUnitsSolo`, `TestMaterializeCopiesWorkflowUnitsVerify` and `TestMaterializeDerivesRoleHint`. Red. Implement. Green. Commit.

#### Unit 12.2: Plan lint
- [ ] Write the failing tests:
  - `TestTreeRequiresWorkflow`
  - `TestTreeRejectsRoleHintMismatch`
  - `TestTreeRejectsReviewTasks`
  - `TestTreeRequiresStepsAndVerifyForTDDTasks`
  - `TestTreeWarnsOnSplitTDDTitles` (positives from the spec regex; negatives such as "Add retry to failing uploads")
  - `TestTreeWarnsOnUnbatchedTasks`
  - `TestSwarmArtifactReturnsWarnings`
- [ ] Red. Implement `lintTree`. Green. Commit.
  - Fixtures in `runtime/testdata` that relied on role_hint-only tasks or had review tasks are updated to carry workflows. This is an intentional change.

#### Unit 12.3: `swarm-workflows`
- [ ] Write the failing test `TestWorkflowsSkillExampleValidates`: extract the fenced example, run it through `ParseTree`, `lintTree` and `Validate`, and expect no errors and no warnings. Red.
- [ ] Write the skill. Green. Commit.

#### Unit 12.4: `swarm-spike`
- [ ] Add the test row (references to `superpowers:brainstorming`, `writing-plans`, `systematic-debugging` and `swarm-batching`, plus the phrase "assign every role"). Red.
- [ ] Write the skill, including a bad → good example: split RED/GREEN/review tasks → one package with units. Green. Commit.

#### Unit 12.5: `swarm-orchestrator` and README
- [ ] Write the failing test `TestOrchestratorSkillNoLongerHandRollsReviews`: the old "After a worker's `completed`, spawn a `reviewer`" sentence is gone, and the three superpowers references and `ponytail-debt` are present. Red.
- [ ] Rewrite the skill, add the pointer to `swarm-batching`, and update the README. Green. Commit.

---


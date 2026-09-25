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


### P10: Orchestrator surface, story and integration gates, e2e
**Workflow:** `tdd-reviewed` · **Units:** 4

**Files:**
- `internal/mcpserver/{workflow.go,orchestrator.go,tools.go,server.go}` and their tests
- `internal/runtime/{workflow.go,checkpoint.go}`
- `internal/items/transition.go` (`deriveStory`)
- `scripts/e2e/{workflow_test.go,tdd_test.go,harness_test.go}`

**Acceptance:**
- `swarm_workflow` supports start, status, resume and cancel. It is orchestrator-only, has `required` set in its schema, and is idempotent via `request_id`.
- `swarm_spawn`:
  - validates the role enum with the spec copy
  - refuses build/design/research roles on workflow tasks
  - honours `worktrees`, sharing them and putting them in the brief header
  - drops `advisor`/`cwd` from its schema
- `swarm_read` returns `workflow_state` and `crew`.
- Story `after_tasks` follows spec B8:
  - a `story_ready_for_review` relay
  - start with a read-only worktree
  - `deriveStory` waits for the review
  - changes requested → escalate
- The `integrated` gate checks the integration verify commands and a passing final review.
- The e2e scenarios pass:
  - happy path, one fix round, escalation, `resume accept`
  - a batched package with per-unit evidence
- `tdd_test.go` uses the new copy for workflow tasks and the legacy copy for legacy tasks.

**Verify:**
- `go test ./internal/mcpserver/... ./internal/runtime/... ./internal/items/...`
- `make e2e`

#### Unit 10.1: `swarm_workflow`
- [ ] Write the failing tests `TestSwarmWorkflowToolsOnlyForOrchestrators`, `TestSwarmWorkflowStartStatusResumeCancel` and `TestSwarmWorkflowIdempotentStart`. Red. Implement. Green. Commit.

#### Unit 10.2: Spawn, checkpoint and read changes
- [ ] Write the failing tests:
  - `TestSwarmSpawnRejectsUnknownRole`
  - `TestSwarmSpawnRefusesWorkflowTask`
  - `TestSwarmSpawnSharesWorktrees`
  - `TestSwarmCheckpointVerdictSchema`
  - `TestSwarmReadCrew`
- [ ] Red. Implement. Green. Commit.

#### Unit 10.3: Story and integration gates
- [ ] Write the failing tests:
  - `TestStoryReadyForReviewRelay`
  - `TestStoryDoneWaitsForAfterTasksReview`
  - `TestStoryReviewChangesEscalates`
  - `TestIntegratedNeedsIntegrationVerify`
  - `TestIntegratedNeedsFinalReviewPass`
  - `TestStoryWithoutAfterTasksUnchanged`
- [ ] Red. Implement. Green. Commit.

#### Unit 10.4: e2e
- [ ] Write the scenarios and the harness helpers for `swarm_workflow`, reviewer verdicts and unit-tagged verification. Run `make e2e` and record the red.
- [ ] Fix any real bug in its owning package, test first. Green. Commit.

---


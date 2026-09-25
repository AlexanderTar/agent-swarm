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


### P8: Checkpoint semantics
**Workflow:** `tdd-reviewed` · **Units:** 5 · Every unit touches `internal/runtime/checkpoint.go`, so they share context.

**Files:**
- `internal/runtime/{checkpoint.go,model.go,reconcile.go,artifacts.go}`
- `internal/items/transition.go`
- `internal/mcpserver/tools.go` (`swarm_checkpoint` schema)
- tests: `checkpoint_test.go`, `transition_test.go`, `reconcile_test.go`, `artifacts_test.go`

**Interfaces (produces):**
- `CheckpointInput.Verdict` and `.Findings`
- `Verify.Unit`
- `workflowRunFor(ctx, tx, agentID)`, whose run-row type is defined here
- `tddOK(entries, units)`, `verifyDeclaredOK`, `commitOK`, `artifactGate`, `registerArtifactAsDaemon`

**Acceptance:**
- Verdict rules and copy follow spec B5.
- Siblings are closed only when they have the same role and the same step.
- `completedCurrent` is per agent. This fixes the cross-agent attempt bug, which a test reproduces.
- `OnDepUnblocked` wakes every distinct parent.
- For workflow agents the step's gates apply:
  - tdd: per unit for batched tasks, and skipped when the task is exempt
  - verify: declared commands, matched by containment and normalised whitespace
  - commit: clean, sha equals HEAD, sha stored on the run
  - artifact: registers `design`/`research`
- Legacy agents keep `verifyOK`, and existing legacy tests stay untouched and green.
- Any existing test asserting that a coder's `completed` closes a reviewer is updated to the new rule. This is an intentional change.

**Verify:**
- `go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... -count=1`
- `go build ./... && go vet ./...`

#### Unit 8.1: Verdicts and findings
- [ ] Write the failing tests `TestVerdictRequiredForWorkflowReviewer`, `TestVerdictRefusedForCoder` and `TestPassVerdictRefusesMajorFindings`. Red. Implement storage, validation and the schema. Green. Commit.

#### Unit 8.2: Close siblings by role and step
- [ ] Write the failing tests `TestReviewerCompletedDoesNotCloseBuilder`, `TestBuilderCompletedDoesNotCloseReviewer` and `TestSameRoleSiblingStillClosed`. Red.
- [ ] Implement the query change. It filters on the caller's role and on the step from that agent's latest `workflow_runs` row. Green. Commit.

#### Unit 8.3: Per-agent completion and dependency wake-ups
- [ ] Write the failing tests `TestCompletedCurrentIsPerAgent` and `TestDepUnblockedWakesAllParents`. Red. Implement. Green. Commit.

#### Unit 8.4: tdd and verify gates
- [ ] Write the failing tests:
  - `TestTDDGateNeedsRedBeforeGreen` (green only; red then green across checkpoints; red and green in different attempts)
  - `TestTDDGatePerUnit` (unit 2 missing → error names unit 2)
  - `TestTDDGateSkippedWhenExempt`
  - `TestVerifyGateMatchesDeclaredCommands`
  - `TestLegacyCoderKeepsVerifyOK`
- [ ] Red. Implement. Green. Commit.

#### Unit 8.5: Commit and artifact gates
- [ ] Write the failing tests `TestCommitGateRefusesDirtyWorktree`, `TestCommitGateRefusesShaMismatch`, `TestCommitGateStoresSha` and `TestDesignArtifactGateRegistersArtifact`. They use a real temp git repo via `runtime/helpers_test.go`. Red. Implement. Green. Commit.


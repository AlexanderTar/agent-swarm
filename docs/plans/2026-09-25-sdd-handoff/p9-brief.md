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


### P9: Workflow engine
**Workflow:** `tdd-reviewed`, `max_rounds: 4` · **Units:** 5

**Files:**
- `internal/runtime/{workflow.go,workflow_test.go,limits.go,agents.go,text.go,checkpoint.go,reconcile.go}`
- `internal/items/transition.go`
- `internal/hook/handler.go` (uses `SubagentSlots`)

**Interfaces (produces):**
- `StartWorkflowInput`, `WorkflowState`
- `StartWorkflow`, `WorkflowFor`, `advance`, `ResumeWorkflow`, `CancelWorkflow`, `recoverWorkflows`
- `SubagentSlots(ctx, parentID) (used, max int, err error)`
- `BriefForStep(it, spec, stepID, round, ctxLines) BriefInput`
- `RenderBrief` gains the `Units`/`Steps` and `Workflow` sections

**Acceptance:**
- **Briefs.** Engine-spawned agents get daemon-rendered briefs per spec B6, collapsing unit steps when over the length cap.
- **Start.** Start is validated per spec B4.
- **Advance.** Advance applies every `Next` action:
  - Build agents get the rw worktree shared to them. Reviewers get a review worktree at the sha, which is created, shared and then removed.
  - A fix round retries the builder with the rendered findings, grouped by reviewer and unit.
  - The task moves InReview ↔ InProgress, and to Done only by the daemon.
  - `workflow_succeeded` and `workflow_escalated` relays are sent, plus the `workflow.escalated` notification.
- **Idempotency.** Repeated advances are no-ops: `ON CONFLICT DO NOTHING` on the run key, and no spawn unless a row was inserted.
- **Budget.** When the budget is full, runs wait and start in FIFO order. The hook uses `SubagentSlots`, and its existing tests stay green.
- **Relays.** The engine suppresses relays of `accepted`, `progress` and `completed` checkpoints to the orchestrator.
- **Triggers.** Advance runs after a checkpoint, after a session death, after a slot is released, and on start/resume. A stall older than 30 s is recovered.
- **Done gating.** An orchestrator cannot mark a workflow task Done.
- **Resume and cancel** follow spec B7, with extra rounds recorded.

**Verify:**
- `go test ./internal/runtime/... ./internal/items/... ./internal/hook/... -count=1`
- `go build ./... && go vet ./...`

#### Unit 9.1: Slots and briefs
- [ ] Write the failing tests `TestSubagentSlotsMatchesHookCount` and `TestRenderBriefUnitsAndWorkflowSections` (including the cap collapse). Red.
- [ ] Move the hook query into `limits.go`, and implement `BriefForStep` and the new `RenderBrief` sections. Green. Commit.

#### Unit 9.2: Start and spawning steps
- [ ] Write the failing tests:
  - `TestStartWorkflowValidates` (no workflow, one already running, no rw worktree, dependencies open)
  - `TestWorkflowSpawnsBuilderWithSharedWorktree`
  - `TestWorkflowSpawnsReviewersOnReviewWorktree`
  - `TestWorkflowParallelReviewers`
  - `TestAdvanceIsIdempotent`
- [ ] Red. Implement `StartWorkflow` and the spawn half of `advance`, using a per-workflow mutex, one tx per action, and side effects after commit. Green. Commit.

#### Unit 9.3: Rounds, success, escalation, relays
- [ ] Write the failing tests `TestWorkflowHappyPath`, `TestWorkflowFixRound`, `TestWorkflowEscalatesWhenRoundsExhausted`, `TestWorkflowAutoRetryOnCrash` and `TestWorkflowRelaysSuppressed`. Red. Implement. Green. Commit.

#### Unit 9.4: Triggers and recovery
- [ ] Write the failing tests `TestCheckpointTriggersAdvance`, `TestCrashTriggersAdvance`, `TestSlotReleaseSpawnsWaitingRun` and `TestRecoverStalledWorkflow`. Red. Implement. Green. Commit.

#### Unit 9.5: Resume, cancel, Done gating
- [ ] Write the failing tests:
  - `TestOrchestratorCannotMarkWorkflowTaskDone`
  - `TestResumeRetryGrantsExtraRound`
  - `TestResumeAccept`
  - `TestResumeFail`
  - `TestCancelWorkflow`
  - `TestResumeRefusedWhenNotEscalated`
- [ ] Red. Implement. Green. Commit.


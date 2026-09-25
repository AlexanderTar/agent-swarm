# P9 report — Workflow engine

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p9`, branch `pkg/p9`.
Branch point: `b36a5fd` (docs(plan): tick P8 units).

Commits (one per unit, all on `pkg/p9`):

1. `0278001` feat(runtime): P9 unit 9.1 -- SubagentSlots and daemon-rendered brief sections
2. `5c3ba4c` feat(runtime): P9 unit 9.2 -- workflow engine start and spawning
3. `c12a046` feat(runtime): P9 unit 9.3 -- rounds, success, escalation, relays
4. `3a30d68` feat(runtime): P9 unit 9.4 -- triggers and stall recovery
5. `882df3a` feat(runtime): P9 unit 9.5 -- resume, cancel, Done gating

Diff vs branch point: 12 files, +2873/-31 (`git diff --stat b36a5fd..HEAD`).

## Unit 9.1: Slots and briefs

**Built:**
- `internal/runtime/limits.go`: `SubagentSlots(ctx, parentAgentID) (used, max int, err error)` — moves the hook's inline `max_concurrent_subagents` budget query (the exact `NotAZombieSlot`-gated count) into `runtime`, shared by the hook and the engine.
- `internal/hook/handler.go`: the `swarm_spawn` budget block now calls `h.RT.SubagentSlots` instead of its own inline `Settings.Get` + query.
- `internal/runtime/text.go`: `BriefInput` gains `Steps []string`, `Units []items.Unit`, `Workflow string`. `RenderBrief` renders a `## Units`/`## Steps` section and appends `in.Workflow` verbatim as the final section. For a workflow spawn (`in.Workflow != ""`) only, an over-cap brief cascades: truncate `Context` to a single `(context truncated; swarm_read <KEY>)` pointer, then — if still over cap — collapse each unit's steps to `<n>. <title> (steps: swarm_read <KEY>)`. A legacy (non-workflow) spawn never cascades: `ErrBriefTooLong` is unchanged. `BriefForStep(it, spec, stepID, round, ctxLines) BriefInput` builds the content-only fields (`Objective`, `Acceptance`, `Steps`, `Units`, `Verify`, `Context`, `Workflow` via `workflow.Render`); identity fields (`Key`/`Title`/`Name`/`Role`/`ParentName`/`RootKey`/`Worktrees`) stay `Spawn`'s job, matching the existing split.

**TDD evidence:**
- RED: `go test ./internal/runtime/... -run 'TestSubagentSlotsMatchesHookCount|TestRenderBriefUnitsAndWorkflowSections'` → compile failure (`s.SubagentSlots undefined`, `unknown field Workflow in struct literal of type BriefInput`, etc.) — the new API didn't exist yet.
- GREEN: same command → `--- PASS: TestSubagentSlotsMatchesHookCount`, `--- PASS: TestRenderBriefUnitsAndWorkflowSections` (5 subtests).

**Files:** `internal/runtime/limits.go`, `internal/runtime/limits_test.go`, `internal/runtime/text.go`, `internal/runtime/text_test.go`, `internal/hook/handler.go`.

## Unit 9.2: Start and spawning steps

**Built (new file `internal/runtime/workflow.go`):**
- Types: `WorkflowWorktree`, `StartWorkflowInput`, `WorkflowRunView`, `WorkflowState`, plus internal `wfRow`/`wfRunRow` (DB row shapes) and a package-level `workflowLocks sync.Map` (per-workflow mutex, mirrors `internal/worktree`'s `wtLocks`).
- `StartWorkflow(ctx, orch Agent, in StartWorkflowInput) (WorkflowState, error)`: validates (task in caller's root, has `workflow_json`, no running/escalated workflow already, no open dependencies via `it.BlockedBy`, a caller-owned active `rw` worktree named in `in.Worktrees`), inserts the `workflows` row (round 1, running), calls `advance`.
- `advance(ctx, workflowID) error`: under the per-workflow mutex, loads the item + runs, calls `workflow.Next`, dispatches the returned `Action` (`Spawn` implemented fully this unit; `RetryFix`/`AutoRetry`/`Succeed`/`Escalate` stubbed as no-ops, filled in 9.3), then always calls `fillWaitingRuns`.
- Idempotent Spawn: `insertWaitingRun` does `INSERT ... ON CONFLICT(workflow_id, step_id, round, role) DO NOTHING`; a run step inserts one 'waiting' row (no agent yet) and moves the task InReview→InProgress (daemon actor, silently denied/no-op when not applicable); a review step inserts one 'waiting' row per missing role and, only for the roles genuinely inserted (not a replay), creates/reuses one shared review worktree (`Worktree.Review` at the reviewed sha, owned by the orchestrator) and records its id on those rows.
- `fillWaitingRuns`: spawns the oldest 'waiting' run first (FIFO by `created_at`) while `SubagentSlots` has room; each spawn claims its row (`UPDATE ... WHERE state='waiting'`) so a concurrent call can't double-spawn it. A build step shares every rw worktree from the workflow's `worktrees_json` to the new agent and fills `BriefInput.Worktrees` for the first time (previously never populated); a review step shares the one review worktree `ro`.

**TDD evidence:**
- RED: `go test ./internal/runtime/... -run 'TestStartWorkflowValidates|TestWorkflowSpawnsBuilderWithSharedWorktree|TestWorkflowSpawnsReviewersOnReviewWorktree|TestWorkflowParallelReviewers|TestAdvanceIsIdempotent'` → compile failure before `workflow.go` existed (types/functions undefined).
- GREEN (after two small fixes — a test-fixture `enabled_agents` narrowing so `Spawn`'s own kind-defaulting doesn't pick an uninstalled `claude`, and a `COALESCE` on a detached worktree's NULL `branch` in `briefWorktrees`):
```
--- PASS: TestStartWorkflowValidates (4 subtests)
--- PASS: TestWorkflowSpawnsBuilderWithSharedWorktree
--- PASS: TestWorkflowSpawnsReviewersOnReviewWorktree
--- PASS: TestWorkflowParallelReviewers
--- PASS: TestAdvanceIsIdempotent
```

**Files:** `internal/runtime/workflow.go` (new), `internal/runtime/workflow_test.go` (new).

**Note:** `workflow.go` was ~682 lines after this unit alone (new-package scaffolding: types, row mapping, spawn/budget plumbing) — over the ~300-line guideline. Flagged in the unit's own commit message; it wasn't split across files because every later unit builds directly on this same dispatch/row-mapping code.

## Unit 9.3: Rounds, success, escalation, relays

**Built:**
- `internal/runtime/workflow.go`: `applyRetryFix` (bumps `workflows.round`, guarded so a duplicate advance is a no-op; inserts the new round's run row; moves the task InReview→InProgress; retries the *same* builder via `Retry(agent, renderFindings(action.Findings))`; releases+removes the finished round's review worktree(s) via `findFixStepsFor`, reused from `checkpoint.go`/P8). `applyAutoRetry` (retries the crashed run's same agent with a resume note naming the session's real state; falls back to leaving the row 'waiting' if it never got an agent id). `applySucceed`/`applyEscalate` (flip `workflows.state` guarded by `state='running'` so a duplicate advance relays nothing twice — Review Focus 5; `applySucceed` moves the task to Done via `items.Daemon()`, relays `workflow_succeeded`, removes every review worktree; `applyEscalate` relays `workflow_escalated` and raises `workflow.escalated`).
- `internal/runtime/checkpoint.go`: `workflowRunFor` is now read unconditionally (every checkpoint kind, not just `completed`) so relay suppression can see it for `accepted`/`progress` too. Completed/failed checkpoints now write the run's `state`/`ended_at` and (new) trigger `s.advance` after commit. A workflow agent's own `FailedCkp` also marks its session `'failed'` immediately in the same tx — otherwise `AutoRetry`'s `Retry()` call has no session in `retryableStates` to act on yet (reconcile's async pane-close would eventually do this, but the engine needs it synchronously). `accepted`/`progress`/`completed` relays to the parent are suppressed when the agent has a workflow run; `blocked`/`failed`/`handoff` still relay.
- `internal/items/transition.go`: `checkTask`'s `to == Done` case now gates on `it.Workflow != nil` (spec B5): refused for any non-daemon caller, and for the daemon unless the item's latest `workflows` row is `'succeeded'`. Legacy tasks (`it.Workflow == nil`) are untouched — verified by the full existing suite staying green.
- `internal/notifyrules/notifyrules.go`: added `"workflow.escalated"` (`{KEY}: workflow needs a decision — {reason}`, verbatim from spec's "All user-facing copy").

**TDD evidence:**
- RED: `go test ./internal/runtime/... -run 'TestWorkflowHappyPath|TestWorkflowFixRound|TestWorkflowEscalatesWhenRoundsExhausted|TestWorkflowAutoRetryOnCrash|TestWorkflowRelaysSuppressed'` → compile-clean but logic-red (applyRetryFix/AutoRetry/Succeed/Escalate were still 9.2's no-op stubs, so nothing actually advanced past Spawn).
- Debugging along the way (both genuine bugs, fixed before green):
  - `runAgentIDAt` originally picked the *latest* round's agent, which — right after `insertWaitingRun` created the new round's own (still-agentless) row — outranked the real round-1 agent; fixed to query the exact pre-bump round.
  - `Retry()` requires the session already in `retryableStates`; a `FailedCkp` didn't itself retire the session (only reconcile does that, asynchronously) — added the direct session-state write described above. `RetryFix`'s own `Retry()` call has the same dependency on the builder's session already being terminal; since a synchronous test never gives reconcile a chance to run, the fix-round tests simulate that elapsed time by setting the session `'completed'` directly before the reviewer's `changes_requested` checkpoint (see judgment call below).
- GREEN:
```
--- PASS: TestWorkflowHappyPath
--- PASS: TestWorkflowFixRound
--- PASS: TestWorkflowEscalatesWhenRoundsExhausted
--- PASS: TestWorkflowAutoRetryOnCrash
--- PASS: TestWorkflowRelaysSuppressed
```

**Files:** `internal/runtime/workflow.go`, `internal/runtime/workflow_test.go`, `internal/runtime/checkpoint.go`, `internal/items/transition.go`, `internal/notifyrules/notifyrules.go`.

**Judgment call / known limitation:** `RetryFix`'s `Retry()` call needs the builder's *old* session already terminal (spec B4 names `Retry(builder, note)` directly). In the live system this is true by the time a review comes back (reconcile's async pane-close has already retired the idle session), but it isn't strictly guaranteed — if a review somehow lands before reconcile's next tick, `Retry()` returns `notRetryable`. Rather than let that error kill the whole action (leaving the workflow stuck retrying the same failing call forever), `applyRetryFix` catches that specific `items.CodeConflict` error, logs it, and leaves the new round's row `'waiting'` so `fillWaitingRuns` spawns a *fresh* agent for it instead. This trades builder-continuity for liveness in an edge case the spec doesn't explicitly address.

## Unit 9.4: Triggers and recovery

**Built:**
- `internal/runtime/reconcile.go`: `resolveDead` is now a thin wrapper around the renamed `resolveDeadInner`; after any transition it makes, it calls `advanceWaitingForOwner(r.ParentAgentID)` (best-effort, logged on error) — the "budget slot release" trigger. The `'interrupted'` and `'crashed'` (no-terminal-checkpoint) branches additionally look up the dying agent's workflow run and, if it has one, mark it `'failed'` in the same tx and call `s.advance` on that specific workflow after commit — the "session death" trigger for a crash reconcile discovers asynchronously (as opposed to 9.3's synchronous `FailedCkp` path).
- `internal/runtime/workflow.go`: `advanceWaitingForOwner(ctx, ownerAgentID)` re-advances every `'running'` workflow owned by that agent with a `'waiting'` run. `recoverWorkflows(ctx)` (wired into `Reconcile`, right before `sweepFinishedRoots`) re-advances every `'running'` workflow with at least one run recorded, none of them `waiting`/`active`, and `updated_at` older than 30s (`stallThreshold`) — the daemon-restart gap between a checkpoint's commit and the `advance()` that should have followed it. `advance` now bumps `workflows.updated_at` on every action it actually applies (`Wait` excepted) so that clock is accurate.

**TDD evidence:**
- RED: `go test ./internal/runtime/... -run 'TestCheckpointTriggersAdvance|TestCrashTriggersAdvance|TestSlotReleaseSpawnsWaitingRun|TestRecoverStalledWorkflow'` → compile failure (`s.advanceWaitingForOwner undefined`) before the reconcile wiring existed.
- GREEN (all four passed on the first run after implementation — no debugging round needed):
```
--- PASS: TestCheckpointTriggersAdvance
--- PASS: TestCrashTriggersAdvance
--- PASS: TestSlotReleaseSpawnsWaitingRun
--- PASS: TestRecoverStalledWorkflow
```

**Files:** `internal/runtime/reconcile.go`, `internal/runtime/workflow.go`, `internal/runtime/workflow_test.go`.

**Judgment call:** `resolveDead`'s wrapper calls `advanceWaitingForOwner` after *every* branch, including the early "still within `spawnGracePeriod`, nothing happened yet" return. That's a cheap, correctness-safe no-op (one query, returns immediately if nothing is waiting) but does mean every live-session reconcile tick now does one extra query per row. Not measured under load; flagging in case reconcile throughput ever becomes a concern.

## Unit 9.5: Resume, cancel, Done gating

**Built:**
- `internal/runtime/workflow.go`: `ResumeWorkflow(ctx, orch Agent, itemKey, decision, note, requestID string) (WorkflowState, error)` — refuses with `"<KEY>'s workflow isn't waiting on you (state: <state>)."` unless the workflow is `'escalated'`.
  - `decision:"retry"`: bumps `extra_rounds` by 1 always; additionally bumps `round` by 1 when `resumeBumpsRound` (a local re-derivation of `internal/workflow`'s own unexported `pinnedFrom` priority — verdict check, then failed/cancelled, then stale-review — built against `Next`'s tests in `internal/workflow/next_test.go`, specifically `TestNextExtraRoundsSameRound` (changes_requested/blocked: no round bump, extra_rounds alone suffices) vs `TestNextFixIndexCarryForward`/`TestNextPinnedFromFailedOutranksStale` (crash and stale-review: round *does* bump, landing on a fresh Spawn at the new round, not a retry of stale evidence)) says the escalation was a crash or a stale review. Flips the workflow back to `'running'`, calls `advance`, then — if `note` is non-empty — delivers it as a second `assignment_update` to whichever agent `advance` just spawned/retried (on top of any reviewer-findings note `RetryFix` already sent).
  - `decision:"accept"`: workflow → `'succeeded'`, task → Done (`items.Daemon()`).
  - `decision:"fail"`: workflow → `'failed'`, task → Ready (`tryTransition`).
  - `CancelWorkflow(ctx, orch Agent, itemKey, requestID string)`: cancels every active run's agent (`s.Cancel`, best-effort/logged on error), workflow → `'cancelled'`, task → Ready, removes every review worktree.
- `internal/items/transition.go`: `checkTask`'s workflow branch gains a `Ready` case — `InProgress`/`InReview` → `Ready` is allowed for the daemon actor once the item's latest `workflows` row is `'failed'` or `'cancelled'` (resume-fail and cancel both need this; there was no existing transition case for it).
- `internal/notify/notify_test.go`: `TestRulesCoverSection175`'s `want` table gained `workflow.escalated` (see "test named" below).

**TDD evidence:**
- RED: `go test ./internal/runtime/... -run 'TestOrchestratorCannotMarkWorkflowTaskDone|TestResumeRetryGrantsExtraRound|TestResumeAccept|TestResumeFail|TestCancelWorkflow|TestResumeRefusedWhenNotEscalated'` → compile failure (`s.ResumeWorkflow undefined`, `s.CancelWorkflow undefined`) before this unit's code existed. (`TestOrchestratorCannotMarkWorkflowTaskDone` itself was already satisfied by 9.3's transition.go change — it was written new this unit per the brief's own test list, and passes against 9.3's existing code.)
- GREEN (all six passed on the first run after implementation):
```
--- PASS: TestOrchestratorCannotMarkWorkflowTaskDone
--- PASS: TestResumeAccept
--- PASS: TestResumeFail
--- PASS: TestResumeRefusedWhenNotEscalated
--- PASS: TestResumeRetryGrantsExtraRound
--- PASS: TestCancelWorkflow
```

**Files:** `internal/runtime/workflow.go`, `internal/runtime/workflow_test.go`, `internal/items/transition.go`, `internal/notify/notify_test.go`.

**Test named (intentional expectation change):** `internal/notify/notify_test.go`'s `TestRulesCoverSection175` hardcodes the exact §17.5 rule table plus a count check (`len(Rules) != len(want)`); it already has precedent for a rule added after that spec was written (`agent.fallback_used`, with its own explanatory comment). I added `workflow.escalated` to the `want` map the same way, with a comment naming this spec/package as the reason — this is the one existing test this dispatch intentionally changed, and it's exactly the addition the change requires (the rule itself, plus `internal/notifyrules/notifyrules.go`, was added in 9.3's commit for the notification `applyEscalate` raises).

## Full Verify output

```
$ go test ./internal/runtime/... ./internal/items/... ./internal/hook/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	23.005s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	1.735s
ok  	github.com/AlexanderTar/agent-swarm/internal/hook	6.639s

$ go build ./... && go vet ./...
(clean, no output)
```

Also ran `go test ./...` (whole repo) to confirm nothing outside the Verify set regressed: the only failure is the pre-existing baseline (`internal/httpapi TestBoardServedAtRoot`, web bundle not built — named in the contract as safe to ignore). No other package failed.

## Self-review notes

- **Legacy safety (Review Focus 1):** every workflow-aware branch I added — `checkTask`'s `Done`/`Ready` cases, `WriteCheckpoint`'s relay suppression and run-state writes, `resolveDead`'s crash/interrupted workflow-run marking — is gated on `it.Workflow != nil` or `hasRun` (a `workflow_runs` row for the acting agent). Confirmed by the full existing suite staying green with no test modified except the one named above (`notify_test.go`, for a genuinely new notification kind, not a behavior change to anything legacy).
- **Idempotency (Review Focus 2):** `insertWaitingRun`'s `ON CONFLICT DO NOTHING` + `RowsAffected` check is the single choke point every Spawn goes through; `applyRetryFix`/`applySucceed`/`applyEscalate` each guard their own state-flip with a `WHERE` clause matching the pre-transition state, so a duplicate `advance()` call (verified directly in `TestAdvanceIsIdempotent`, and implicitly by every trigger test that calls `advance` more than once along its path) never double-spawns, double-relays, or double-bumps.
- **Sibling teardown / per-agent completion (Review Focus 3):** untouched this dispatch — P8 already implemented `closeCompletedSiblings`/`completedCurrent`'s role+step narrowing; P9 only reads/writes `workflow_runs.state`/`ended_at`, never touches that logic.
- **Relay suppression (Review Focus 5):** `TestWorkflowRelaysSuppressed` exercises a full accepted→progress→blocked→progress→completed→(reviewer)accepted→completed cycle and asserts the orchestrator's relay inbox is *exactly* `[blocked, workflow_succeeded]` — no raw accepted/progress/completed leakage, and the one genuine relay still reaches it.
- **Mutex scope:** `advance`'s per-workflow mutex (package-level `sync.Map`, keyed by workflow id) is held for the whole call — bookkeeping and every side effect (`Spawn`/`Retry`/`Worktree.*`) it triggers — per the advisor's correction; nothing called from inside `advance` calls `advance` synchronously itself (the reconcile/checkpoint triggers all call `advance` from *outside* any lock they hold).
- Ran the full `go test ./...` sweep (not just the Verify set) specifically to catch cross-package fallout from the `notifyrules`/`notify` change; the only thing it found was the one intentional test update.

## Concerns / judgment calls (consolidated)

1. `StartWorkflow`'s "item must be a task in the caller's root" check (spec B4) uses the existing `"<KEY> is outside your assignment."` copy — the spec names this validation but gives no verbatim string for it.
2. `advance`'s bookkeeping uses small, focused transactions (one per DB-state change) rather than one transaction per whole `Action`; `Spawn`/`Retry`/`Worktree.Share`/`Review`/`Remove` each keep their own internal transaction/commit (they're existing `Store`/`Service` methods with that shape already) — this mirrors `DrainQueue`'s own composition style for a multi-step operation, not a literal single SQL transaction per action.
3. Multi-repo review worktree picks the first `rw` worktree by repo name, mirroring `commitGate`'s own tie-break (P8) for which repo's sha a run records.
4. `RetryFix`'s dependency on the builder's session already being terminal (see 9.3's judgment call above) is a real, if narrow, timing edge case in the live system, not just a test artifact — flagging for reviewer awareness even though the fallback (spawn fresh) keeps the workflow live.
5. `resolveDead`'s slot-release trigger runs on every reconcile tick per live row, including the "nothing happened, still in grace period" no-op path (see 9.4's judgment call above) — a minor, unmeasured efficiency cost, not a correctness issue.
6. `workflow.escalated`'s notification title ("Workflow needs a decision") isn't given verbatim by the spec (only the body is) — picked to match the existing table's terse-noun-phrase style.
7. Added `internal/notifyrules/notifyrules.go` (not in the P9 file list, but `internal/notify`'s `Rules` is a straight re-export of it) — required for `applyEscalate`'s `workflow.escalated` notification to render at all; the alternative (skip the notification) would violate the unit's own acceptance criterion.

## Fix round 1 (advisor review)

Ran the advisor before declaring done; it found 4 blocking gaps, 2 should-fix one-liners, and one item needing verification. All addressed in a single new commit (`internal/runtime` unstaged at review time — folded into the same units rather than amended, since nothing had been reported as done yet).

1. **Missing test: design→build status flow.** The dispatch text explicitly required this ("a designer's completed... can move the task to InReview before the build runs... add a test for design→build status flow") and I never wrote a dedicated test for it — the mechanism (`markInProgress` on every run-step Spawn) was already correct from 9.2, but unproven. Added `TestDesignThenBuildStatusFlow`: `design{gates:[artifact:design]} → review-design{ui_reviewer, of:design} → build{gates:[commit]}`. Asserts InReview after design completes, then InProgress once build spawns.
2. **`resumeBumpsRound` was wrong for a blocked verdict.** `internal/workflow.Next` escalates on `blockedReason` unconditionally (no round/budget check at all, checked *before* the changes_requested/extraRounds path) — so treating blocked the same as changes_requested (no round bump) would resume straight into the same blocked row and re-escalate immediately, forever. Fixed the priority: blocked → bump; changes_requested → don't (matches `internal/workflow/next_test.go`'s own blocked-resume cases, e.g. `TestNextBlockedPinsLikeChangesRequested`). Added `TestResumeRetryAfterBlocked`.
3. **`design-reviewed`-shaped templates (a reviewed step with no commit gate) broke on the first review spawn.** `Next` correctly returns `Spawn{SHA: ""}` for a review of a step with no `GateCommit` (nothing sets `run.SHA`), but `applySpawn` fed that empty string straight to `Worktree.Review`, whose `shaPattern` check rejects it — erroring the whole action before it ever reached `fillWaitingRuns`, so the review step never spawned at all. Fixed: `applySpawn`'s review branch returns early (rows inserted, no worktree) when `action.SHA == ""` — a reviewer of a non-commit-gated step reads the registered artifact instead. Covered by `TestDesignThenBuildStatusFlow`'s `review-design` step.
4. **B6 Context was missing artifact paths.** My own 9.2 code comment had deferred this and it never came back. Added `artifactContextLines(ctx, itemID)`: queries `artifacts` for `kind IN ('design','research')` on the item itself and on everything it `blocked_by` depends on, appended to the workflow's own start-context lines before `BriefForStep`. Covered by the same design→build test (asserts the builder's brief contains the registered design artifact's path).
5. **`CancelWorkflow` reused the caller's `requestID` across every per-agent `Cancel` call**, so the second of two parallel active runs would hit `PeekIdempotent` and silently replay the first's result instead of actually being cancelled. Fixed: pass `""` per agent (whole-call idempotency is P10's MCP wrapper's job, not this loop's).
6. **`recoverWorkflows` excluded any workflow with a `'waiting'` run**, which is exactly the case it needs to catch if the slot-release trigger that should have picked it up never fires again (e.g. the owner's last other child already finished before this run went waiting). Narrowed the exclusion to `'active'` only — a workflow with something genuinely in flight still doesn't need the stall scan; one with only a stranded `'waiting'` row now does.
7. **(Verify, not a one-liner) `FailedCkp`'s new synchronous `sessions.state='failed'` write is the first place `Retry()` can run while the writer's own tmux pane is still alive** — every pre-existing `Retry()` call site only ever follows `resolveDead` confirming the pane already dead, but a voluntary `FailedCkp` is written from *inside* that still-live session. Real tmux's `new-session -s <name>` (confirmed in `internal/spawn/tmux.go`) refuses a duplicate name, which would leave `applyAutoRetry`'s row flipped to `active` with `Retry()` having errored underneath it — a stuck run with no session. Fixed the same way `closeCompletedSiblings` closes a torn-down sibling's pane: `WriteCheckpoint` now hoists a `toCloseFailedSelf *siblingTeardown` for this exact case and interrupts+kills that pane *after* commit but *before* the `advance()` call that can reach `Retry()`. Not independently unit-tested (fakeTmux doesn't simulate a real duplicate-session refusal), so this is verified by code inspection against `internal/spawn/tmux.go`'s `new-session -s <name>` behavior, not by a red/green cycle — flagging for reviewer attention as the one item in this round I couldn't prove with a test.

**Tests run:** `go test ./internal/runtime/... -run 'TestDesignThenBuildStatusFlow|TestResumeRetryAfterBlocked' -v` → both `PASS` on first run. Then the full existing workflow test set (33 tests) and the package Verify set again:
```
$ go test ./internal/runtime/... ./internal/items/... ./internal/hook/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	23.014s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	1.961s
ok  	github.com/AlexanderTar/agent-swarm/internal/hook	6.899s

$ go build ./... && go vet ./...
(clean)
```
Whole-repo `go test ./...` re-run: only the same pre-existing baseline failure (`internal/httpapi TestBoardServedAtRoot`).

**Status after this round: DONE_WITH_CONCERNS** — item 7 above (verified by inspection, not by test) and the `RetryFix` builder-session-timing judgment call from 9.3 are the two open concerns; everything else in this round is fixed and tested.

## Fix round 1 (continued)

Second advisor pass after the round above. All three actionable items fixed; three report-only notes added below (no code change, per the advisor's own framing).

1. **`applyAutoRetry` could strand a run `active` with no session.** It claims the row (`state='active', auto_retries+1`) *before* calling `Retry()`; fix round 1's own tmux fix closed the duplicate-session cause of a `Retry()` failure, but `Retry()` can still fail for other reasons (fallback preflight, missing adapter) — and on that error the row was left `active` with no live session: invisible to `liveSessionRows`/`resolveDead` (nothing to reconcile) and excluded from `recoverWorkflows` (which only re-scans non-`active` workflows). Fixed: on a `Retry()` error, fall back to `state='failed'` (keeping the incremented `auto_retries`) and log, rather than propagate the error — the next `advance()` (triggered within `stallThreshold` by `recoverWorkflows`, since the workflow is no longer `active`) either retries again or, once the budget is spent, escalates instead of hanging forever.
2. **Turned finding 7 (the tmux duplicate-session fix) from "verified by inspection" into a real red/green.** Extended `TestWorkflowAutoRetryOnCrash` (`fakeTmux` already available via `newStore`) to assert the coder's old pane name appears in `tm.killed` and a new session was started — `WriteCheckpoint` runs single-threaded and kills the pane (post-commit, before `s.advance`) strictly before `Retry()`/`startSession` can run, so proving both happened is proving the order.
3. **Report notes added** (no code change; the advisor's own framing for these three — first two flagged again from the previous round's "Report notes" section, which hadn't made it into this file yet):
   - `StartWorkflow` refuses a **story** the same way as a task with no workflow (`"<KEY> has no workflow."`) — this reads oddly for spec B8's `swarm_workflow start {item: STORY, worktrees: [{mode: ro}]}` (a story genuinely has no `workflow_json` of its own template-shape; B8's story-level `after_tasks` review is a different code path P9 doesn't implement — B8 is out of this dispatch's scope per the brief, which names only B4/B5/B6/B7). Flagging for P10, which owns the MCP surface and B8's remaining wiring.
   - `StartWorkflowInput.SessionID`/`RequestID`, and `ResumeWorkflow`/`CancelWorkflow`'s `requestID` parameters are accepted but never used for idempotency in this package — a replayed `swarm_workflow resume {decision:"retry"}` would double-bump `extra_rounds` and (if the first call's `advance` already ran) potentially double-trigger a spawn attempt (idempotent at the DB level via `insertWaitingRun`'s `ON CONFLICT`, so not a double-spawn, but still a spurious extra round grant). This is P10's MCP wrapper's job (the same `IdemTx`/`PeekIdempotent` pattern every other `swarm_*` tool uses) — noting so it isn't forgotten, not fixing it here since there's no MCP tool calling these yet.
   - `ResumeWorkflow`'s note delivery queries `state = 'active' ORDER BY round DESC` for "whichever agent advance() just spawned or retried". If that spawn is itself budget-blocked (stays `'waiting'`), this finds nothing (note silently not delivered) — or, in a multi-loop spec, an unrelated older-round step's still-active run. A real but narrow edge case; not fixed (P10 will need to decide whether an undelivered resume-note should surface back to the caller as a warning, which is a product-copy question outside the brief's scope).

**Tests run:**
```
$ go test ./internal/runtime/... -run TestWorkflowAutoRetryOnCrash -v
--- PASS: TestWorkflowAutoRetryOnCrash

$ go test ./internal/runtime/... ./internal/items/... ./internal/hook/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	22.130s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	1.903s
ok  	github.com/AlexanderTar/agent-swarm/internal/hook	6.962s

$ go build ./... && go vet ./...
(clean)
```

**Status: DONE_WITH_CONCERNS.** Remaining concerns are the ones named throughout this report, none blocking: the `RetryFix` builder-session-timing fallback (9.3), the resume-note delivery edge case above, the unused `requestID`/`SessionID` params awaiting P10's idempotency wiring, and `StartWorkflow`'s story-copy mismatch for P10's B8 work.

## Fix round 2 (Opus review)

Opus review of fix round 1: legacy guarantee confirmed intact, engine structure good, but 9 Important findings plus a binding user directive (RetryFix must never fresh-spawn while the old builder's session is still live). All findings, the directive, and the minor cleanup list addressed; test-first per item (RED confirmed by stashing the implementation with the test still in place, then GREEN after restoring it), one or more new commits per finding. 9 commits on `pkg/p9`:

1. `ada1950` -- user directive
2. `9316d1c` -- finding 5
3. `8fca93c` -- finding 6
4. `ce9d9fc` -- finding 1
5. `332bc5c` -- findings 2, 3, 4
6. `f9b9535` -- finding 7
7. `bc2d73f` -- finding 8
8. `024ece8` -- finding 9
9. `15c4ffc` -- minor cleanup

Plus two self-found commits, made while writing this round's own report and after an internal advisor review of it (see the User directive section below): 10. `f8223ab` -- addendum, not in the numbered findings. 11. `9704206` -- self-review fixes (a dead constraint-text match, an untested validator, and the addendum's own overclaimed wording).

Diff this round: 6 files, +1529/-326 through commit 9 (`git diff --stat ada1950^..15c4ffc`); +1687/-324 including both self-found commits (`git diff --stat ada1950^..HEAD`).

### User directive -- RetryFix must never fresh-spawn while the old builder's session is live

`applyRetryFix`'s "fall back to a fresh spawn on `notRetryable`" path (fix round 1's own judgment call, §"Judgment call / known limitation" above) is removed entirely. New `closeSessionForRetry(ctx, a Agent)`: if `a`'s latest session is still live, kill its pane and mark it `Completed` synchronously -- mirroring `resolveAlive`'s own `killCompletedAfter` path rather than waiting for reconcile's async tick -- so `Retry()` (which requires a `retryableStates` session) can act immediately. A fresh spawn is now legitimate only when the builder agent's row itself no longer exists (`agentByID` returns `sql.ErrNoRows`) -- the one case the directive names as still legitimate -- and findings still reach that fresh agent via finding 4's `roundFindingLines`.

Also removed `checkpoint.go`'s `toCloseFailedSelf` mechanism (fix round 1, item 7): re-verified `agents.go:1095`'s `startSession` already kills any stale pane under the same name before `Tmux.Start` (`P0-crash-1`), making the application-level tmux kill redundant -- only the session-state write (`sessions.state='failed'`) was ever load-bearing for `Retry()`'s own precondition.

**Test:** `TestRetryFixClosesLiveBuilderBeforeRetrying` -- unlike the pre-existing `TestWorkflowFixRound`, the coder's session is genuinely still `Running` (no manual close simulation) when the reviewer's `changes_requested` checkpoint lands. Asserts: the coder's tmux name is in `tm.killed`; the OLD session's own row is non-live; round 2's build run has the SAME agent id, state `active`; `LatestSession` returns a different (new) session id; at most one live session for the agent at any point.
```
--- PASS: TestRetryFixClosesLiveBuilderBeforeRetrying
```

**Addendum (found while writing this report's Verify section, not in the original findings list; commit `f8223ab`):** `closeSessionForRetry` only guarantees the OLD session is closed (retryable) before `Retry()` runs -- `Retry()` itself can still fail for an unrelated reason (a fallback preflight, or the retried attempt's own `startSession`/`Tmux.Start` failing). A bare `'waiting'` round-2 row left behind by that failure was invisible to the directive's own intent: `fillWaitingRuns`' generic path doesn't know the row was ever meant for the SAME builder and would spawn an unrelated fresh agent over it on the very next advance, with no retry-budget accounting at all. Fixed the same way `applyAutoRetry`'s own Retry-failure fallback already does: mark the round's row `'failed'` (not agent-claimed) instead, routing it through `Next`'s ordinary crash handling.

An internal advisor pass on this round's own report (before handoff, not one of the reviewer's findings) caught that the FIRST version of this fix and its own report wording overclaimed: marking the row `'failed'` does NOT prevent an eventual fresh spawn -- the directive's own text allows one once the builder "can't be retried for a reason other than still live", which this genuinely is. What the fix actually does is route that fresh spawn through `Next`'s ordinary retry-budget accounting (`AutoRetry` while budget remains, only then `Escalate`/fresh-spawn) instead of bypassing it entirely and I4's findings-bearing brief, rather than blocking it outright. Reworded here and in commit `9704206`'s message; the test below was extended to actually prove the corrected claim.

**Test:** `TestRetryFixMarksFailedWhenRetryErrorsAfterClose` -- `tm.startErr` set only after the initial build spawn succeeds, so `closeSessionForRetry` closes the still-live old session fine, and only the retried attempt's own `Tmux.Start` fails. Asserts the round-2 row ends `'failed'` with no `agent_id` at all (never silently `'waiting'`); then clears `tm.startErr` and calls `advance` once more, asserting the SAME crash-handling path reaches a compliant fresh spawn -- a DIFFERENT agent than the original builder, `auto_retries=1` (budget-accounted), and the round-1 findings rendered into that fresh agent's very first brief (I4).
```
--- PASS: TestRetryFixMarksFailedWhenRetryErrorsAfterClose
```

**Residual, not fixed (flagged, not blocking):** after a post-close `Retry()` failure, the OLD builder's `agents` row is left `state='active'` (`closeSessionForRetry` marks the session terminal but never marks the agent itself finished, and nothing else revisits it once its session isn't live). Its latest session ends up in a non-live state either way (the closed old one, or the new failed attempt `Retry`'s own `startSession` call left behind) -- `NotAZombieSlot`'s own `s.generation = MAX(generation)` + non-live-state check already excludes an agent in this shape from the budget count, so this is harmless to slots/scheduling, but `swarm_read`/the agent list would keep showing it as an active agent indefinitely. Not fixed in this round -- narrow, budget-harmless, and outside every numbered finding's own scope.

### Finding 5 -- `resumeBumpsRound` re-implemented the planner, and got it wrong

Deleted the ~65-line hand-rolled re-derivation of `pinnedFrom`'s verdict/failed/stale priority (it matched `changes_requested` before checking for a failed/cancelled run, so a mixed case -- one parallel reviewer requesting changes while another crashed and exhausted its retries -- granted only `extra_rounds` and immediately re-escalated on the same crash forever, since `Next`'s own failure-handling loop runs *before* verdict evaluation). Replaced with a single delegating call: `workflow.Next(spec, runs, round, extraRounds+1).Kind == workflow.ActionEscalate` -- if granting one more extra round still escalates, the round itself must bump too.

Verified against both of the reviewer's probe cases (`scratchpad/p9probe/internal/runtime/zz_probe_test.go`, read but not executed -- a separate throwaway repo copy) by manual trace against this repo's own `internal/workflow/next.go`: the mixed-failure case and the stale-changes_requested case both now correctly bump the round instead of re-escalating.

**Test:** `TestResumeRetryMixedFailureAndChangesRequested` -- two-reviewer spec; build completes; `reviewer` requests changes; `ui_reviewer` crashes twice, exhausting its default retry budget of 1. Confirms final escalation, then `ResumeWorkflow(retry)` yields `running`/round 2 (the old code never bumped, since it matched `changes_requested` first) and a fresh round-2 `build` row with a non-empty agent.
```
--- PASS: TestResumeRetryMixedFailureAndChangesRequested
```
The pre-existing `TestResumeRetryAfterBlocked` (blocked-verdict case) continues to pass unchanged, confirming the delegation didn't regress the case fix round 1 already fixed.

### Finding 6 -- pause treated as a crash

`reconcile.go`'s pause-interrupt-deadline branch (`r.State.Pausing() && s.getInterrupted(r.SessionID) != nil` -- exclusively a deliberate `swarm_control pause` hitting its deadline; `getInterrupted` is only ever set by `TickPause`'s own `interrupt()`) used to also mark the workflow run `'failed'` and trigger `s.advance`, the same signal used for a real crash -- letting `AutoRetry` immediately fire a fresh `Retry()` on the paused agent, defeating the pause. The branch now leaves the run untouched (workflow_runs, and the trailing `advance` trigger, both removed from this branch) -- a human's later `swarm_control resume` is what should start it going again.

**Test:** `TestPauseDeadlineLeavesWorkflowRunActive`, modeled on the existing `TestDeadlineInterruptsAndNeverRecordsPaused` pattern. Debugging note: the first RED failure message was initially confusing ("session state = running, want interrupted") until tracing showed `LatestSession`'s `generation DESC` ordering was silently returning a NEW session created by the wrongly-firing `AutoRetry`, not that the interrupted session itself reverted -- fixed the test to check the original session's state directly by id and separately confirm `LatestSession` still returns that same session.
```
--- PASS: TestPauseDeadlineLeavesWorkflowRunActive
```

### Finding 1 -- runs stranded `active` forever

Three named paths (a `watchStartup`→`failSession` startup failure, a direct `swarm_control cancel`, a daemon crash between `applyAutoRetry`'s DB claim and its `Retry()` call) each leave a `workflow_runs` row `'active'` with a dead session and nothing left to ever trigger it: `Next` just Waits on an active run, and `recoverWorkflows`' stall scan used to exclude any workflow with an active run at all, stranded or not.

New `healStrandedActiveRuns(ctx, runs)`, called from `advance()` before `Next` on every call: for each `'active'` run whose agent's `LatestSession` is no longer live, heals it to `'failed'` (session `Failed`/`Crashed` -- matches `AutoRetry`'s own budget) or `'cancelled'` (session `Cancelled` -- `Next` escalates a cancelled run immediately, it never auto-retries one; confirmed by reading `internal/workflow/next.go`'s own `RunStateCancelled` branch before implementing, per the advisor's flagged collision risk below). `Interrupted` (finding 6's deliberate pause) and `Paused` are explicitly left untouched -- not a crash signal, and the exact case finding 6 just fixed. `recoverWorkflows`' own exclusion narrows from "has an active run" to "has an active run with a session in a LIVE state", so a stranded workflow with no other trigger left is picked up by the 30s stall scan.

An internal advisor consultation before this item flagged a specific risk: implementing I1 as "any non-live session heals" would silently re-break I6 (an `Interrupted` session is non-live), since `TestPauseDeadlineLeavesWorkflowRunActive` advances the clock >30s and calls `Reconcile` twice, exercising `recoverWorkflows`. The heal rule above was written narrow (only `Failed`/`Crashed`/`Cancelled`) specifically to avoid that collision; `TestPauseDeadlineLeavesWorkflowRunActive` re-passing in the same run confirms no regression.

**Tests:** one per named path.
```
--- PASS: TestHealStrandedActiveRunStartupFailure
--- PASS: TestHealStrandedActiveRunDirectCancel
--- PASS: TestHealStrandedActiveRunCrashBetweenClaimAndRetry
```

### Findings 2, 3, 4 -- spawn-before-claim, review worktree replay, findings/note reach fresh spawns

**Finding 2** (spawn before the row is claimed): `spawnRunAgent` now claims its row (`agent_id` + `state -> active`) immediately after `Spawn` returns an agent, *before* any `Worktree.Share` call -- a `Share` error is now logged, not fatal (the agent is already claimed and running; an unreachable worktree is a lesser failure than stranding or duplicating the whole run). A `Spawn` error itself (`agents.go`'s `startSession` can fail `Tmux.Start` *after* the agent row already committed, per the comment at `agents.go:922` -- leaving no id to recover) now marks the row `'failed'` instead of leaving it `'waiting'` to retry every 30s forever, each attempt orphaning one more agent: it genuinely counts toward `auto_retries` via `applyAutoRetry`'s existing `AgentID==""` branch, so a repeated failure escalates. `StartWorkflow` now validates every `{worktree, mode}` entry up front (`validateWorktrees`), maps the `workflows_one_live` unique-index race to the existing "already has a running workflow" refusal, and runs its post-commit `advance` under `context.WithoutCancel(ctx)` (logged, not fatal, on error -- the committed start is still recovered by the stall scan).

**Finding 3** (review worktree only attached on first insert): `applySpawn` no longer gates the worktree-attach step on `newRoles` (roles this specific call happened to insert) -- a crash between `insertWaitingRun` committing a role's row and the attach step running used to leave every PRE-EXISTING role's row permanently without one on replay (`newRoles` empty, the whole block skipped). It now re-checks every role at `(step, round)` still missing a `review_worktree_id` on every call (`reviewRolesMissingWorktree`), idempotently.

**Finding 4** (findings/resume note only reach an existing agent): `spawnRunAgent` renders round-1's `changes_requested`/`blocked` findings into a round > 1 fresh spawn's brief Context (`roundFindingLines`, reusing `checkpoint.go`'s own R3 lookup `findFixStepsFor`/`fixRoundFindings` -- not a third copy). A normal fix loop always retries the same agent via `applyRetryFix`'s `Retry()` note; a fresh spawn is only reached via a resume's round bump after a crashed/stale escalation, and that agent has no prior brief to update. `ResumeWorkflow` persists the resume note into `workflows.context_json` (`appendWorkflowContext`) before `advance` runs, reusing the same channel `spawnRunAgent` already folds into every brief, and only falls back to `deliverNote`'s message when the round *wasn't* bumped (the pre-existing agent being retried, whose brief already went out and won't be regenerated) -- avoiding double delivery.

**Tests:**
```
--- PASS: TestShareFailureDoesNotDoubleSpawn
--- PASS: TestRepeatedSpawnFailureEscalates
--- PASS: TestApplySpawnReplayAttachesReviewWorktree
```
`TestResumeRetryAfterBlocked` extended: the reviewer's blocked checkpoint now carries a structured finding and the resume passes a note; asserts the fresh round-2 builder's brief contains both.
```
--- PASS: TestResumeRetryAfterBlocked
```

### Finding 7 -- FIFO budget fairness across an owner's workflows

An orchestrator's subagent budget (`SubagentSlots`) is one shared pool across every workflow it owns, not per-workflow -- a freed slot used to go to whichever sibling workflow's own `advance` happened to run first. `advanceWaitingForOwner` (the slot-release trigger) now orders its workflows oldest-waiting-run-first (`GROUP BY w.id ORDER BY MIN(r.created_at)` instead of an arbitrary `DISTINCT`). `fillWaitingRuns` itself now yields (`olderWaitingRunElsewhere`) when the same owner has an older waiting run in a different running workflow -- covering every OTHER trigger (a checkpoint, the stall scan) that can reach a newer sibling workflow's `advance` directly, out of turn. Documented as best-effort fairness (a TOCTOU race between two concurrent triggers on two different, unlocked workflows can still let both spawn or both yield) -- `SubagentSlots` itself still caps total usage correctly either way.

**Test:** `TestFIFOAcrossWorkflows` -- three tasks, budget 1, an unrelated occupier agent holds the one slot so TASK-2's and TASK-3's build rows both land `waiting` (TASK-2 strictly older). Frees the slot directly (not through any trigger that itself decides ordering), then calls `s.advance` on TASK-3 alone (simulating an out-of-turn trigger) and asserts it yields; only then calls `s.advance` on TASK-2 and asserts it spawns.
```
--- PASS: TestFIFOAcrossWorkflows
```

### Finding 8 -- Resume/Cancel root check, locking, ordering, guards

`ResumeWorkflow` and `CancelWorkflow` now refuse a foreign orchestrator (`it.RootID != orch.RootItemID`, reusing `StartWorkflow`'s own `"<KEY> is outside your assignment."` copy) -- never reachable via a normal inbox relay in practice, but nothing enforced it. `CancelWorkflow` now holds `lockForWorkflow` across its whole body (the same lock `advance`/`fillWaitingRuns` hold -- verified `s.Cancel` in `agents.go` never itself calls `s.advance`, so this is not reentrant) and flips the workflow's state to `'cancelled'` FIRST, before cancelling any agent: a slot freed mid-cancel can no longer let a later trigger spawn a fresh run for a workflow still being torn down. It also now marks every active/waiting run `'cancelled'` directly (`Cancel` only ever touched the agent/session, never `workflow_runs` itself -- left alone, `swarm_read` would keep reporting a cancelled workflow's last runs as active/waiting forever). `ResumeWorkflow`'s three decision UPDATEs are now all guarded with `AND state = 'escalated'`, reporting the current (post-race) state instead of double-applying on a concurrent race. `accept`/`fail` now also release any still-outstanding review worktree (new shared `removeAllReviewWorktrees` helper, also now used by `applySucceed` and `CancelWorkflow`, deduping the `{wt, agent}`-collection-then-`releaseAndRemove` pattern that was inlined three times).

**Tests:**
```
--- PASS: TestResumeCancelRefuseForeignOrchestrator
--- PASS: TestCancelRacingSlotReleaseSpawnsNothing
```
`TestCancelRacingSlotReleaseSpawnsNothing`: budget 1, TASK-1 holds the slot, TASK-2's build row waits. Cancels TASK-1, then calls `advanceWaitingForOwner` directly (simulating the slot-release trigger reaching this owner right after cancel) -- asserts TASK-1's workflow/run both end `cancelled`/`cancelled` and exactly one new agent starts (TASK-2's, never a second TASK-1 spawn).

### Finding 9 -- `deliverNote` duplicated `Retry`'s message INSERT

`Retry`'s own note-delivery block (`agents.go`) hand-rolled the exact same `assignment_update` INSERT `deliverNote` (`workflow.go`) already did. `Retry` now calls `s.deliverNote(ctx, a.ID, note)` directly (logging on error instead of silently swallowing it, a strict improvement); `deliverNote` itself now goes through `s.enqueue` (which already owns `seq`/`wake_class`/`priority`/`state`/`created_at` -- confirmed `assignment_update` is in `ImmediateKinds`, so `WakeClassFor` still resolves to `"immediate"`, matching the old hand-rolled value byte for byte) instead of its own manual seq lookup + INSERT. Pure refactor, no new test written; covered by existing assertions on both call sites (`agents_test.go`'s Retry-note tests, `workflow_test.go`'s deliverNote/finding-note tests) -- all still pass unchanged, which is itself the regression check for a behavior-preserving dedup.

### Minor cleanup

- `toCloseFailedSelf` removed -- folded into the user-directive item above (same root cause: `startSession` already kills a stale pane before `Tmux.Start`).
- `fillWaitingRuns`'/`spawnRunAgent`'s "claims its row first" comments rewritten to describe the new claim-before-Share ordering.
- `latestWorkflowRow`/`workflowRowByID` deduped via a shared `wfRowColumns` column list + `scanWfRow` scan helper (they differed only in their WHERE/ORDER clause).
- Resume `accept`/`fail` now release review worktrees (folded into finding 8's commit, same code path).
- `StartWorkflow` logs (doesn't fail) a post-commit `advance` error, and maps the `workflows_one_live` constraint race to the friendly refusal copy (folded into finding 2's commit) -- the first version's match string was dead code (matched the index name, but sqlite reports the column: `"UNIQUE constraint failed: workflows.item_id"`); fixed in `9704206` and pinned by `TestWorkflowsOneLiveErrMapsConstraintText`.
- Added `TestConcurrentAdvanceSpawnsOnce`: two goroutines calling `advance()` on the same freshly-seeded (never-yet-advanced) workflow concurrently spawn exactly one agent -- `lockForWorkflow`'s own mutex, verified under `-race` (5 consecutive runs, all green).
- Two listed items turned out not to apply to this worktree's current code, checked rather than assumed: `workflowSucceeded`/`workflowFailedOrCancelled` don't exist under those names anywhere in this package (`grep` came back empty -- nothing to merge into a `latestWorkflowState`-style helper); `TestCheckpointTriggersAdvance`'s doc comment is already correctly positioned directly above its own function (no gap, no stray reference elsewhere in the file).

**Not implemented, per the coordinator's explicit instruction:** P10's `request_id` idempotency. `StartWorkflowInput.SessionID`/`RequestID` and `ResumeWorkflow`/`CancelWorkflow`'s `requestID` parameters remain accepted but unused for idempotency (unchanged from fix round 1's own note on this) -- left for P10's MCP wrapper.

### Verify (coordinator's exact command set, final re-run after every commit in this round)

```
$ go test ./internal/runtime/... ./internal/items/... ./internal/hook/... ./internal/workflow/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	25.847s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	2.213s
ok  	github.com/AlexanderTar/agent-swarm/internal/hook	7.103s
ok  	github.com/AlexanderTar/agent-swarm/internal/workflow	0.524s

$ go build ./... && go vet ./...
(clean, no output)

$ go test ./...
(all packages ok, EXCEPT internal/httpapi's TestBoardServedAtRoot -- confirmed pre-existing:
 reproduces identically at ada1950~1, the commit immediately before this whole fix round began,
 and no commit in this round touches internal/httpapi at all. Same baseline exception this
 report's own Verify section already named after unit 9.5 and fix round 1.)

$ go test -race ./internal/runtime/ -run Workflow
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	20.039s
```
A broader `go test -race ./internal/runtime/... -count=1` (every runtime test, not just the `Workflow`-named subset, since this round touched locking/concurrency directly in findings 1/7/8) was also run to be thorough, mid-round (before the final two self-found commits, which don't touch locking):
```
$ go test -race ./internal/runtime/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	337.401s
```
Clean pass, no `WARNING: DATA RACE` anywhere in the output.

**Status after this round: DONE_WITH_CONCERNS.** All 9 numbered findings and the binding user directive addressed and tested, including a gap in the directive's own guarantee (and, on a second pass, a wording overclaim about it) found and fixed before handoff rather than reported as done prematurely. Every minor cleanup item addressed (two confirmed not applicable to this worktree's current code, checked rather than assumed). Open, non-blocking concerns, consistent with prior rounds' own convention of naming rather than hiding residual edge cases:
- Finding 7's FIFO fairness is documented best-effort, not a hard guarantee (a TOCTOU race between two concurrent triggers on two different, unlocked workflows can still let both spawn or both yield -- `SubagentSlots` itself still caps total usage correctly either way).
- The addendum's residual: a post-close `Retry()` failure (for a reason other than "still live") leaves the OLD builder's `agents` row `state='active'` with a non-live latest session -- budget-harmless (`NotAZombieSlot` excludes it), but `swarm_read`/the agent list would keep showing it as active indefinitely.
- P10's `request_id` idempotency remains explicitly out of scope, per the coordinator's own instruction.
- The pre-existing `internal/httpapi` baseline failure, unrelated to this package.

## Files changed (full list)

- `internal/runtime/workflow.go` (new)
- `internal/runtime/workflow_test.go` (new)
- `internal/runtime/limits.go`, `internal/runtime/limits_test.go`
- `internal/runtime/text.go`, `internal/runtime/text_test.go`
- `internal/runtime/checkpoint.go`
- `internal/runtime/reconcile.go`
- `internal/hook/handler.go`
- `internal/items/transition.go`
- `internal/notifyrules/notifyrules.go`
- `internal/notify/notify_test.go`

## Fix round 2 (re-review findings)

Commit on `pkg/p9`: `4820c88dfbcb09fb72fda1894a18089754853836` (`fix(runtime): keep workflow retries bound to their builder`). The integration ledger was not edited.

- **Builder continuity and crash recovery:** `applyRetryFix` now assigns the original builder's `agent_id` and marks the next-round row active *before* closing the old session or calling `Retry`. A Retry failure marks that same row failed while retaining its agent ID; `AutoRetry` therefore retries the original agent. `healStrandedActiveRuns` recognizes a claimed fix run whose latest session is an older Completed session. The recovery scan considers only each agent's latest session when deciding whether an active run is live. `closeSessionForRetry` now propagates a tmux kill failure instead of marking a still-running pane Completed. This closes the reported crash and Retry-error paths that could spawn two live builders.
- **Post-commit triggers:** completed and failed checkpoint advances use `context.WithoutCancel(ctx)` after their transaction. Reconcile's post-transition workflow and slot-release advances do the same. A test cancels the request immediately after the failed checkpoint commits and confirms AutoRetry still starts attempt 2 on the same agent.
- **Review replay:** `spawnRunAgent` now creates or reuses the shared review worktree whenever a waiting review row has a SHA but no `review_worktree_id`, then attaches it before rendering the brief and spawning. This covers the real `advance` replay path where `Next` returns Wait because all review role rows already exist.
- **Failed pane after exhaustion:** the failed workflow checkpoint closes its own pane after commit, before AutoRetry advances. The re-review's pane concern was confirmed: the second failed checkpoint previously escalated while leaving its terminal session's pane running, which reconcile would not scan. The new test checks the pane close on both the retrying and exhausted attempts. This self-close is gated to workflow runs; the legacy sibling teardown path keeps its prior context and behavior.

Red/green evidence: `TestRetryFixMarksFailedWhenRetryErrorsAfterClose` failed because round 2 had an empty agent ID; `TestReplayAdvanceAttachesReviewWorktree` failed because a replayed reviewer spawned with no worktree; `TestFailedWorkflowCheckpointClosesPaneAfterRetryExhausted` failed on the second failure because no pane kill occurred. All pass after the fix. `TestFailedCheckpointAdvanceSurvivesRequestCancellation` was also confirmed red by temporarily restoring the old `advance(ctx, ...)` call (run remained failed with zero retries), then green with the committed call. `TestRecoverClaimedFixRunWithOlderCompletedSession` covers the Completed-session recovery path.

Final verification:

```
go test ./internal/runtime/... ./internal/items/... ./internal/hook/... ./internal/workflow/... -count=1  PASS
go build ./... && go vet ./...                                                        PASS
go test -race ./internal/runtime/ -run Workflow -count=1                             PASS
git diff --check                                                                      PASS
go test ./... -count=1                                                                 FAIL: internal/httpapi TestBoardServedAtRoot (GET /kanban = 503); all other packages passed
```

The full-suite failure is the same documented pre-existing `internal/httpapi` baseline failure; this commit does not touch that package. No temporary probe files remain in the worktree. Remaining non-blocking concerns from fix round 1 still apply: cross-workflow FIFO is best-effort, and an exhausted Retry can leave an agent record marked active even with no live session. P10 request ID idempotency remains outside P9.

## Fix round 3 (scoped re-review)

Commit on `pkg/p9`: `b6079f9d8b9cc59435e946ef9856261341059da1` (`fix(runtime): atomically bind fix rounds and retire exhausted agents`). Integration ledger left to the controller.

- `applyRetryFix` resolves the old builder before the write, then advances the workflow round and inserts the next run already bound to that builder in one SQL transaction. An injected INSERT failure rolls the round back; after a successful commit the new row is active and names the original builder. The prior `TestRecoverClaimedFixRunWithOlderCompletedSession` covers recovery after the bound commit and before the new retry starts. The reviewer scratch state with a committed round change and an unbound inserted row is now unreachable through `applyRetryFix`.
- Escalation marks failed-run agents finished when they have no live session. This aligns agent-list state with `liveDescendants` and allows worktree reclaim. `startSession` now records every error after its session INSERT as a failed session, including adapter launch, pre-run, and tmux kill errors that previously left `spawning` rows; the tmux Start path uses the same cleanup.
- Tests added for transaction rollback/bound commit, exhausted fix retry descendant cleanup, and early start failure followed by exhausted auto-retry. The stale `changes_requested` case now has a runtime regression test. Review replay asserts a successful read, active row, nonempty agent ID, and nonempty review worktree. The sequential cancel test was renamed to describe its actual ordering check.

Red evidence before implementation: the reviewer probes `TestRecoverRoundBumpNeverSpawnsSecondBuilder` and `TestExhaustedFixRetryFinalizesBuilder` failed with two live coder sessions and one active descendant respectively. The first probe models a partial database state that atomic commit now prevents; it was replaced by an injected transaction-failure test. Green targeted run: `go test ./internal/runtime -run 'TestRetryFixRoundAndBoundRunCommitTogether|TestExhaustedFixRetryFinalizesBuilder|TestExhaustedAutoRetryEarlyStartFailureFinalizesBuilder|TestStaleChangesRequestedEscalatesWithoutFixRound|TestReplayAdvanceAttachesReviewWorktree|TestCancelledWorkflowIgnoresLaterSlotRelease' -count=1` (covered by the package run below; individual subsets also passed).

Final verification:

```
go test ./internal/runtime/... ./internal/items/... ./internal/hook/... ./internal/workflow/... -count=1  PASS
go build ./... && go vet ./...                                                        PASS
go test -race ./internal/runtime/ -run Workflow -count=1                             PASS
git diff --check                                                                      PASS
go test ./... -count=1                                                                 FAIL: internal/httpapi TestBoardServedAtRoot (GET /kanban = 503); all other packages passed
```

The full-suite failure is the same pre-existing `internal/httpapi` baseline failure documented in prior rounds; this commit does not touch that package. Cross-workflow FIFO remains best-effort, and P10 request-ID idempotency remains outside P9. A persistent tmux kill error while closing the old builder can leave its bound run active until the external tmux problem is resolved; it cannot spawn a second builder from that run.

## Fix round 4 (scoped reclaim finding)

Commit on `pkg/p9`: `edb698932d11fa7325e6d07f2aca66910a082f53` (`fix(runtime): release exhausted workflow agent reservations`). Integration ledger left to the controller.

- `applyEscalate` now releases every unreleased worktree reservation held by a failed-run agent that is finished and has no live session. This happens in the same transaction that escalates the workflow and retires exhausted agents. A still-live agent retains its reservation, and the query is scoped to failed runs of the escalating workflow.
- `TestExhaustedFixRetryFinalizesBuilder` now exercises the real reclaim candidate gate after the owner finishes and the grace period elapses. Before the fix it failed with `candidates=[]` because the retired builder retained its shared worktree reservation. It passes after the fix.

Verification after the change:

```
go test ./internal/runtime -run '^TestExhaustedFixRetryFinalizesBuilder$' -count=1  PASS (red before fix, green after)
go test ./internal/runtime/... ./internal/items/... ./internal/hook/... ./internal/workflow/... -count=1  PASS
go build ./...                                                                   PASS
go vet ./...                                                                     PASS
go test -race ./internal/runtime/ -run Workflow -count=1                        PASS
git diff --check                                                                 PASS
go test ./... -count=1                                                            FAIL: internal/httpapi TestBoardServedAtRoot (GET /kanban = 503); all other packages passed
```

The full-suite failure is the same documented `internal/httpapi` baseline failure. This commit changes only `internal/runtime/workflow.go` and its test. The prior rounds' cross-workflow FIFO and P10 request-ID limitations remain unchanged.

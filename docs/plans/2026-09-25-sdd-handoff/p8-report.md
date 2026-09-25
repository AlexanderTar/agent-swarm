# P8 report: Checkpoint semantics

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p8`, branch `pkg/p8`.

Commits (in order):
1. `bba7917` feat(runtime): verdict and findings on completed checkpoints (8.1)
2. `3cccd4b` feat(runtime): close siblings by role and workflow step (8.2)
3. `5a0f0b2` feat: per-agent completedCurrent and wake every parent on dep unblock (8.3)
4. `5f9aab6` docs(spec): tdd gate round scope and B6 retry-session line (8.4 first commit)
5. `54359fa` feat(runtime): tdd and verify gates for workflow agents (8.4)
6. `af84a37` feat(runtime): commit and artifact gates for workflow agents (8.5)

## Unit 8.1 — Verdicts and findings

`CheckpointInput` gains `Verdict string` and `Findings []workflow.Finding` (reused `workflow.Finding`, not a second type). `Verify` gains `Unit int`. New `workflowRun` struct (ID, WorkflowID, StepID, Role, State, SHA, Round, CreatedAt) and `workflowRunFor(ctx, tx, agentID)` reading the latest `workflow_runs` row by `(round DESC, created_at DESC)`. `isReviewerRole`, `validVerdict`, `hasMajorOrCritical`, `stepFor` helpers. Verdict rules:
- `verdict != "" && role not reviewer/ui_reviewer` → refused ("Only reviewers set a verdict."), checked unconditionally (not just on `completed`).
- On `completed`, a reviewer/ui_reviewer **with a workflow run** must set a valid verdict ("Reviewers must complete with verdict: pass, changes_requested or blocked."); `pass` with a `major`/`critical` finding is refused.
- A reviewer **without** a run (legacy) needs no verdict at all — matches the existing `TestTDDGateSkippedForExemptTasksAndReviewRoles` test unchanged.

Stored on `checkpoints.verdict`/`findings_json`; copied onto `workflow_runs.verdict`/`findings_json` when `hasRun && verdict != ""`. Added `applyGates` as a dispatch stub (empty switch — 8.4/8.5 filled in the cases) so legacy-vs-workflow branching in `WriteCheckpoint` could land in one place: `if in.Kind == CompletedCkp { run, hasRun = workflowRunFor(...) }` then `if hasRun { verdict + applyGates } else if TddExempt == "" { <original verifyOK block, byte-for-byte, unmoved> }`.

Also wired `verdict`/`findings`/`verification[].unit` through `swarm_checkpoint`'s MCP schema and handler, and added the `wf`/`wfr` id prefixes (schema already had `workflows`/`workflow_runs` from migration 0011; nothing had claimed their id prefixes yet).

RED: `go test ./internal/runtime/... -run 'TestVerdictRequiredForWorkflowReviewer|TestVerdictRefusedForCoder|TestPassVerdictRefusesMajorFindings'` → compile failure (`unknown field Verdict/Findings`).
GREEN: same command → PASS (3/3), 0.48s.

## Unit 8.2 — Close siblings by role and step

**Judgment call (flagged, not silently made — see Concerns):** the literal spec text ("same role and same step... for legacy agents without a run: same role") would break the orchestrator-override scenario `closeCompletedSiblings` exists for (`TestCompletedChecksClosesTheOtherLiveSessionOnTheSameItem`, an orchestrator's `completed` closing a live coder session — the s11-tool-stubs incident fix) and violate Review Focus 1/Global Constraint 2 (legacy byte-for-byte). Implemented: `if callerRole != RoleOrchestrator { require same role; if callerHasRun, also require same step }` — an orchestrator caller keeps closing every live sibling regardless of role, exactly as before; every other role is now filtered to same-role (+same-step when it has a run).

I grepped for an existing test matching the brief's "a coder's completed closes a reviewer" description and found none — the only existing cross-role sibling-teardown test is the orchestrator/coder one above, which I left untouched (it still passes unmodified). `TestTryTransitionLogsADeniedTransitionInsteadOfSilence`'s incidental reviewer→coder teardown (not itself asserted on) also still passes.

New unit tests: `TestReviewerCompletedDoesNotCloseBuilder`, `TestBuilderCompletedDoesNotCloseReviewer`, `TestSameRoleSiblingStillClosed` (a same-role, same-step sibling from an earlier round is still closed — round isn't part of the filter, only role+step).

RED: three new tests, first two fail on the actual assertion (session closed when it shouldn't be), third needed a schema fix (two runs can't share `(workflow_id, step_id, round, role)` — used round 2 for the second coder).
GREEN: `go test ./internal/runtime/... -run 'TestReviewerCompletedDoesNotCloseBuilder|TestBuilderCompletedDoesNotCloseReviewer|TestSameRoleSiblingStillClosed'` → PASS (3/3).

## Unit 8.3 — Per-agent completion and dependency wake-ups

**`completedCurrent` (`internal/items/transition.go`):** legacy tasks (`it.Workflow == nil`) keep the original `MAX(attempt)`-across-the-item formula byte-for-byte. Workflow tasks (`it.Workflow != nil`) instead: look only at `coder`/`debugger`/`mechanical`-role checkpoints, pick the temporally latest `completed` one among them, and require its `attempt` to equal *that same agent's own* `MAX(attempt)` — not the item-wide max, which mixed a sibling reviewer's independent attempt counter into the check. New test `TestCompletedCurrentIsPerAgent` (in `internal/items`) reproduces the bug (reviewer attempt=5 vs coder's completed@1) and the fix-round-supersedes-itself case (same coder posts progress@2 after completed@1 → denied again).

I checked this against the "landmine" concern (an orchestrator-only `completed` no longer reaching Done): no existing test exercises that path through `completedCurrent`/`checkTask`'s `Done` case for a workflow task, and gating the whole new formula behind `it.Workflow != nil` means the concern doesn't arise for legacy tasks at all (the only place it *could* arise, since P8 doesn't yet wire the real orchestrator-completes-a-workflow-task flow — that's P9's engine).

**`OnDepUnblocked` (`internal/runtime/reconcile.go`):** was `SELECT ... LIMIT 1`-shaped (via `QueryRowContext`), waking one arbitrary active agent's ancestor. Now queries every active agent on the unblocked item, resolves each one's nearest live ancestor, and relays once per **distinct** resolved ancestor (dedup by ancestor id, per dependant item) — `nearestLiveAncestor` already handles "no parent at all" by returning `ok=false` immediately, so no separate check was needed. New test `TestDepUnblockedWakesAllParents`: two agents on the same blocked item, two different live parents, both must get exactly one relay. (Debugging note: my first attempt at this test put the second "parent" agent on the *same* item and role as the agent that later completes it — 8.2's own sibling-closing fix then tore it down mid-test before `OnDepUnblocked` even ran. Fixed by giving it a different role, not a bug in the implementation.)

RED: `TestCompletedCurrentIsPerAgent` — denied when it should have succeeded (reviewer's attempt=5 blocked the coder's completed@1). `TestDepUnblockedWakesAllParents` — helper's relay count was 0.
GREEN: `go test ./internal/items/... -run TestCompletedCurrentIsPerAgent` and `go test ./internal/runtime/... -run TestDepUnblockedWakesAllParents` → both PASS.

## Unit 8.4 — tdd and verify gates

First commit is `docs(spec): ...` applying ruling-tdd-followups.md's three edits (tdd scope → "this step's attempts in the current round" language throughout B5's tdd bullet; the fix-round package-wide copy adapted from "in this attempt" to "in this round" in both the prose and the "All user-facing copy" list; B6's rendered brief line now says the retry is a new session of the same agent and to `swarm_read` prior checkpoints).

Implementation:
- `verifySince(ctx, tx, agentID, since)` — every `verify_json` entry an agent recorded at or after `since`, ordered. Both gates use `run.CreatedAt` as `since` (the round's start, inserted before any checkpoint of that round — so a same-round `AutoRetry` crash re-attempt keeps its earlier evidence for free, and a new round, which gets a new `workflow_runs` row, starts fresh).
- `priorRoundFindings(ctx, tx, workflowID, round)` — flattens every reviewer/ui_reviewer finding at `round`, plus `rowsExist` to distinguish "no reviewer ran that round" from "reviewer ran, zero findings."
- `hasRedBeforeGreen(entries, unit)`, `anyUnitHasRedBeforeGreen(entries)`, pure.
- `tddOK(entries, required []int, packageWide bool) (ok bool, missing []int)` — pure gate logic: if `required` is non-empty, each named unit needs its own pair (batched-copy error names exactly the missing ones); else if `packageWide`, any single unit's pair anywhere satisfies it; else (non-batched, round 1) a single untagged pair.
- `tddGate` computes `required`/`packageWide`: round ≤ 1 → every unit (or none, non-batched); round > 1 with no prior-round reviewer rows → same "every unit" fallback; round > 1 with reviewer rows but zero findings → nothing required (verify gate covers unchanged units); round > 1 with findings → union of tagged units (+ packageWide flag if any finding has `unit:0`).
- `verifyDeclaredOK(entries, want)` — whitespace-normalized containment; `verifyGate` reports every `it.Verify` command not matched, joined with `"; "`.

Tests: `TestTDDGateNeedsRedBeforeGreen` (subtests: green-only fails; red-then-green across two checkpoints passes; a red recorded under a *different* `attempt` number, same round, still counts — proving round-scope not attempt-scope), `TestTDDGatePerUnit` (unit 2 named alone in the error; note a *failed* completed checkpoint writes nothing at all — the whole transaction rolls back on a gate error — so unit 1's evidence had to be recorded via an earlier `progress` checkpoint, not folded into the failing `completed` attempt), `TestTDDGateSkippedWhenExempt`, `TestVerifyGateMatchesDeclaredCommands`, `TestLegacyCoderKeepsVerifyOK`. Added one extra (not brief-named) diligence test, `TestTDDGateFixRoundScopesToNamedUnits`, since the fix-round scoping logic is the crux of both controller rulings and none of the 5 required tests exercise `round > 1` at all.

RED: all 5 required tests compiled and ran; the ones expecting an error initially got `err == nil` (gate was a no-op stub); success-path ones already passed (nothing to enforce yet).
GREEN: `go test ./internal/runtime/... -run 'TestTDDGateNeedsRedBeforeGreen|TestTDDGatePerUnit|TestTDDGateSkippedWhenExempt|TestVerifyGateMatchesDeclaredCommands|TestLegacyCoderKeepsVerifyOK'` → PASS (8/8 incl. subtests). Diligence test also PASS.

## Unit 8.5 — Commit and artifact gates

- `rwWorktreesFor(ctx, tx, agentID)` — every held `rw` worktree reservation (repo name, path), ordered by repo name.
- `gitHead(ctx, path)` — `git rev-parse HEAD` via `s.Exec`/`execx.Run` (same pattern as this file's existing `changedFiles`).
- `commitGate`: refuses if `git` is empty, or any declared entry claims `dirty:true`; then, independently of what the checkpoint claims, re-checks every actual rw worktree via `s.Worktree.DirtyStrict` and real `rev-parse HEAD` against the matching repo's declared sha — a caller can't just claim `dirty:false`. Stores the matched HEAD sha onto `workflow_runs.sha` (first non-empty one, repo-name order, for the common single-repo case).
- `registerArtifactAsDaemon(ctx, tx, itemID, agentID, kind, path)` — new internal daemon-side registration (no orchestrator-only check, unlike `RegisterArtifact`; no revision/section-approval machinery, since these are single-file design/research notes, not specs/plans). First registration only; a path already on the item is a no-op.
- `artifactGate(ctx, tx, it, a, in, kind, dir)` — requires a readable regular file among `artifacts` under `~/.swarm/<dir>/<ROOT-KEY>/`, else the exact spec copy naming the missing artifact kind; on success, registers it.

Tests use real temp git repos (`gitRepoNoSigning` + `commitFile`/`gitOutput`, both already in the package) and a new `seedRWWorktreeAt` helper (the existing `seedWorktreeReservation` points at an empty `t.TempDir()`, not a real git checkout, so it couldn't be reused for real `git status`/`rev-parse` checks): `TestCommitGateRefusesDirtyWorktree` (checkpoint claims clean, real tree is dirty — the gate catches it), `TestCommitGateRefusesShaMismatch`, `TestCommitGateStoresSha`, `TestDesignArtifactGateRegistersArtifact` (missing → exact error naming the root key's path; present → registered, one row in `artifacts`).

RED: all 4 ran and failed exactly at the assertion expecting an error (gate not yet dispatched) or the stored-sha check (empty).
GREEN: `go test ./internal/runtime/... -run 'TestCommitGateRefusesDirtyWorktree|TestCommitGateRefusesShaMismatch|TestCommitGateStoresSha|TestDesignArtifactGateRegistersArtifact'` → PASS (4/4).

## Verify (full package set, run after 8.5)

```
$ go build ./... && go vet ./...
(clean)

$ go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	17.4s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	1.9s
ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	11.3s
```

Also ran `go test ./...` (whole repo, not just the package Verify set) to confirm nothing outside the three packages regressed from the `internal/ids` prefix addition: every package passes except the one pre-existing baseline failure named in the contract, `internal/httpapi TestBoardServedAtRoot` (web bundle not built) — unrelated to this package, untouched by it.

## Files changed

- `internal/runtime/checkpoint.go` — the whole gate/verdict/sibling/workflowRun machinery (hot file, as flagged).
- `internal/runtime/model.go` — `Verify.Unit`.
- `internal/runtime/reconcile.go` — `OnDepUnblocked` wakes every distinct parent.
- `internal/items/transition.go` — `completedCurrent` per-agent for workflow tasks.
- `internal/mcpserver/tools.go` — `swarm_checkpoint` schema: `verdict`, `findings`, `verification[].unit`.
- `internal/ids/ids.go` — `wf`/`wfr` id prefixes.
- `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md` — the three ruling-tdd-followups.md edits.
- Tests: `internal/runtime/checkpoint_test.go`, `internal/runtime/helpers_test.go`, `internal/runtime/reconcile_test.go`, `internal/items/transition_test.go`, `internal/items/helpers_test.go`.

Not touched: `internal/runtime/artifacts.go` (no changes ended up needed there — `registerArtifactAsDaemon` lives in `checkpoint.go` next to its only caller and reuses `artifacts.go`'s exported `SplitSections`/`sha256Hex`, so nothing in that file's own API needed to change).

## Self-review notes / concerns

1. **Judgment call, flagged for a ruling:** the orchestrator-exemption in `closeCompletedSiblings` (8.2). The literal spec text reads as unconditional same-role(+step) filtering; implementing it literally regresses the s11-tool-stubs incident fix for legacy tasks. I kept the orchestrator override intact (its own doc comment already frames it that way) and narrowed the filter for every other role. This is the single highest-risk interpretive call in the package — worth an explicit ruling if the reviewer disagrees.
2. **`completedCurrent`'s workflow-task branch is gated on `it.Workflow != nil` alone**, per Review Focus 1 (not on "does it have a `workflows` row" — P9 doesn't exist yet, so there's no `workflows` row to check against in P8; `workflow_json IS NULL` is the only legacy/workflow discriminator this package has). This is consistent with the brief's own framing ("workflow_json IS NULL is THE legacy test").
3. `commitGate`'s "which repo's sha lands on the run" when a task shares more than one rw worktree is a genuine judgment call (spec doesn't say) — first non-empty by repo-name order. Untested beyond the single-repo case (matches the brief's 4 named tests, all single-repo).
4. Did not implement the `workflows`-row `checkTask` Done-gating bullet from spec B5 ("if the item has a `workflows` row, → Done is allowed only for the daemon actor...") — it's not in P8's Acceptance list or unit tests, and requires the engine (P9) to actually create `workflows` rows for real tasks. Left untouched, as scoped.

## Fix round 1 (Opus review + controller rulings R1-R3)

Findings source: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p8-fix1-findings.md`.

Commits (in order):
1. `eb2bd26` docs(spec): fix round 1 rulings R1-R3 (siblings, completedCurrent, tdd fix round) — amends spec B5 per the controller's rulings before any implementation, plus copy for findings 4/5/7, and exports `workflow.SHA7`.
2. `c4e991e` fix(runtime): applyGates refuses an unknown workflow step (finding 4)
3. `2233c6c` fix(runtime): closeCompletedSiblings' R1 (finding 2)
4. `5627948` fix(items): completedCurrent counts every build role (finding 3, R2)
5. `9d7d646` fix(runtime): tdd gate fix-round detection is per step (finding 1, R3)
6. `b0b9b35` test(runtime): commit gate no-rw-worktree / no-git-entry coverage (finding 5)
7. `5db73da` fix(runtime): validate verdict enum value on any checkpoint kind (finding 6)
8. `1c92f5e` fix: validate finding severity server-side; file optional (finding 7)
9. `c18c1f3` fix(runtime): artifact gate path handling and revision recording (finding 8)
10. `de68a57` fix(runtime): surface verifySince's json.Unmarshal error (finding 9)
11. `953baea` test(runtime): same-role-different-step and OnDepUnblocked-same-parent coverage (finding 10)

Finding 11 (export `workflow.SHA7`, fix the `GateTDD` comment, remove the `_ = it` dead code) landed inside commits 1–3 above — I did those three mechanical cleanups first, before starting the ruling-by-ruling work, so there's no separate commit for it. Verified: `git log --all -S "_ = it" -- internal/runtime/checkpoint_test.go` shows it introduced in the original 8.2 commit and removed in commit 2 above; `workflow.SHA7` is exported in `internal/workflow/next.go` and reused from `internal/runtime/checkpoint.go`'s `commitGate` (no second copy); `internal/workflow/spec.go`'s `GateTDD` comment now says "same round" not "same attempt".

### R1 — closeCompletedSiblings (finding 2)

Spec amended first: the filter now depends on the **item** (legacy vs workflow), not the caller's role. Legacy tasks keep the orchestrator override byte-for-byte; workflow tasks are always same-role(+step), orchestrator included — no exemption.

Implementation: `closeCompletedSiblings` gained an `isWorkflowItem bool` parameter (from `it.Workflow != nil` at the call site); the filter condition changed from `callerRole != RoleOrchestrator` to `!(callerRole == RoleOrchestrator && !isWorkflowItem)`.

RED: `TestOrchestratorCompletedOnWorkflowTaskDoesNotCloseBuilder` — coder's session showed `completed` (closed) when it should have stayed `running`.
GREEN: same test, plus re-ran `TestCompletedChecksClosesTheOtherLiveSessionOnTheSameItem` (the legacy orchestrator-override test) to confirm it's still untouched.
```
go test ./internal/runtime/... -run 'TestOrchestratorCompletedOnWorkflowTaskDoesNotCloseBuilder|TestCompletedChecksClosesTheOtherLiveSessionOnTheSameItem|TestReviewerCompletedDoesNotCloseBuilder|TestBuilderCompletedDoesNotCloseReviewer|TestSameRoleSiblingStillClosed' -count=1
PASS (5/5)
```

### R2 — completedCurrent build roles (finding 3)

Spec amended first: "build roles" = any role except `reviewer`/`ui_reviewer`/`orchestrator` (was `coder`/`debugger`/`mechanical` only), so `designer`/`researcher` steps can finish.

Implementation: both `ag.role IN (...)` clauses in the workflow-task branch of `completedCurrent` changed to `ag.role NOT IN ('reviewer','ui_reviewer','orchestrator')`.

RED: `TestCompletedCurrentCountsDesignerOnAWorkflowTask` — `move(..., InReview, daemon)` denied ("remains In progress") for a designer's `completed`.
GREEN: same test + `TestCompletedCurrentIsPerAgent` + `TestTaskTransitions` (both legacy-shape tests, confirming no regression).
```
go test ./internal/items/... -run 'TestCompletedCurrentCountsDesignerOnAWorkflowTask|TestCompletedCurrentIsPerAgent|TestTaskTransitions' -count=1
PASS (3/3)
```

### R3 — tdd gate fix-round detection is per step (finding 1)

Spec amended first: a run is in a fix round only when a review step's loop (`Loop.Fix`, falling back to `Of`) actually retries its `step_id` **and** that review step has a `changes_requested`/`blocked` row at round−1; findings come only from that step's rows at that round. Otherwise (no such review step, or one exists but never blocked) it's a **first run** — every unit — regardless of the workflow's own round counter.

Implementation: replaced `priorRoundFindings` (workflow-wide, round-scoped, verdict-blind) with `findFixStepFor` (walks `it.Workflow.Steps` for the review step targeting `run.StepID`) + `fixRoundFindings` (step- and round-scoped, returns `hasBlocking` alongside the merged findings). `tddGate`'s branching now keys off `findFixStepFor`'s result and `hasBlocking`, not `run.Round <= 1`.

Five required scenarios, all written test-first:
- `TestTDDGatePackageWideFindingNeedsAnyPairRoundCopy` — round 2, one package-wide finding, no pair at all → the generic round copy (not the unit-list copy); one pair anywhere then passes.
- `TestTDDGateRoundEvidenceDoesNotCarryForward` — round 1's red/green (and its own workflow_runs row) don't satisfy round 2's requirement; fresh round-2 evidence does.
- `TestTDDGateMultiLoopFirstRunNeedsEveryUnit` — `design → review-design → build → review-build`, build's own first run lands at round 2; **strengthened with a decoy**: a `review-design` (different step) `changes_requested` finding at round 1 tagged `unit:1`, to actually prove the new step-scoped lookup ignores it (my first draft of this test passed against the OLD code too, since nothing decoyed it — caught this by checking discrimination before trusting green).
- `TestTDDGateFixRoundMergesFindingsFromBothReviewers` — `reviewer` (changes_requested, unit 1) + `ui_reviewer` (pass, unit 2) at the same round/step merge; unit 2 still required.
- Added `TestTDDGateReviewStepPassedOnlyNeedsEveryUnit` (not brief-named, but the direct negative of "hasBlocking"): a review step that only ever passed at round-1, with findings, is NOT a fix round — every unit still required. Also caught by the same discrimination check.

RED (both discriminating tests, after strengthening):
```
go test ./internal/runtime/... -run 'TestTDDGateMultiLoopFirstRunNeedsEveryUnit|TestTDDGateReviewStepPassedOnlyNeedsEveryUnit' -count=1 -v
FAIL both -- "expected unit 2 to be required" / "...still be required"
```
GREEN: full tdd-gate group.
```
go test ./internal/runtime/... -run 'TestTDDGate' -count=1 -v
PASS (13/13 incl. subtests)
```

### Finding 4 — applyGates fails closed on an unknown step

`stepFor`'s zero-value `Step{}` has no `Gates`, so a corrupted/stale `workflow_runs.step_id` silently enforced nothing. Added the `ok` check with the spec's new copy.

RED: `TestApplyGatesRefusesUnknownWorkflowStep` — `err == nil` (silent pass).
GREEN: same test, PASS.

### Finding 5 — commit gate copy (no-rw-worktree, no-git-entry, drop trailing periods)

**Process note (disclosed, not hidden):** I implemented this one alongside finding 4 in the same edit pass (both touch `applyGates`/`commitGate`) before writing its tests — not strictly test-first. I added `TestCommitGateRefusesWithNoRWWorktree` and `TestCommitGateRefusesMissingGitEntryForRepo` afterward to cover it explicitly; both pass, confirming the implementation. Also dropped the trailing period from the sha-mismatch message to match the spec's literal copy (`TestCommitGateRefusesShaMismatch`'s `want` string updated accordingly — an intentional copy fix, not a weakened assertion).
```
go test ./internal/runtime/... -run TestCommitGate -count=1 -v
PASS (5/5)
```

### Finding 6 — verdict enum validation on any checkpoint kind

Moved the `validVerdict` check out of the `completed`+`hasRun`+reviewer-only path to right after the role check, so any non-empty invalid verdict on any checkpoint kind is refused with the spec copy instead of hitting `checkpoints.verdict`'s raw SQL CHECK constraint.

RED: `TestInvalidVerdictValueRefused` — a `progress` checkpoint with `Verdict: "bogus"` returned the raw SQLite CHECK-constraint error (`"constraint failed: CHECK constraint failed: verdict IS NULL OR verdict IN (...)"`), not the spec copy.
GREEN: same test + the three existing verdict tests, PASS (4/4).

### Finding 7 — finding severity validation

`workflow.Finding.Severity` now validated server-side against `critical|major|minor|nit` (new `validSeverity`), independent of the MCP schema's own advisory enum. Schema `findings[].severity` gained the enum; `file` is no longer `required` (package-wide findings have none).

RED: `TestFindingSeverityValidated` — `Severity: "urgent"` accepted silently.
GREEN: same test, PASS; also confirms a valid severity with no `file` is accepted.

### Finding 8 — artifact gate path handling + revision recording

- `registerArtifactAsDaemon` rewritten to match `RegisterArtifact`'s own revision-bump pattern: content-identical → dedupe (no new revision); content changed → bump `head_revision` and insert a new `artifact_revisions` row. Previously it no-opped on ANY already-registered path, silently dropping a fix round's revised design/notes.
- `artifactGate` now `filepath.Clean`s the incoming path and resolves a leading `~/` via a new, deterministically-testable `expandHome(p, home string)` (parameterized rather than calling `os.UserHomeDir()` inside the pure function). The "needs your design file" error now names the daemon's real, resolved directory (`filepath.Join(s.Home, dir, it.RootKey)`) instead of a hardcoded `~/.swarm/...` string that would mislead when `SWARM_HOME` differs.
- **Process note:** my first draft of the `~/`-expansion test tried to go through a real `WriteCheckpoint` call using the real `$HOME`, and it correctly self-skipped every run in this sandbox (`s.Home` is a `t.TempDir()`, never nested under the real home) — so it would never have produced real RED/GREEN evidence. Redesigned as a pure, deterministic unit test of `expandHome` instead (`TestExpandHomeResolvesTilde`), plus a separate `filepath.Clean` integration test that doesn't depend on the real environment (`TestArtifactGateCleansDotDotSegments`, using a `dir/sub/../flow.md`-style detour).

RED: `TestRegisterArtifactAsDaemonRecordsRevisionsAndDedupes` — "new content should bump revision: head=1 revisions=1" (stayed at 1; old code no-opped). `TestArtifactGateCleansDotDotSegments`/`TestExpandHomeResolvesTilde` — compile failure (`expandHome` undefined) until implemented.
GREEN:
```
go test ./internal/runtime/... -run 'TestExpandHomeResolvesTilde|TestArtifactGateCleansDotDotSegments|TestRegisterArtifactAsDaemonRecordsRevisionsAndDedupes|TestDesignArtifactGateRegistersArtifact' -count=1 -v
PASS (4/4)
```
(`TestDesignArtifactGateRegistersArtifact`'s own expected error string was updated to the new real-path copy — an intentional copy fix, not a weakened assertion.)

### Finding 9 — surface json.Unmarshal errors

Fixed `verifySince`'s silently-discarded `json.Unmarshal` error (a malformed `verify_json` row used to just vanish from the gate's evidence instead of surfacing as a data problem). `fixRoundFindings` (new in this fix round, replacing `priorRoundFindings`) already surfaces its own unmarshal error. Left `priorVerify` (the pre-existing legacy `verifyOK` path, predates P8) untouched — out of scope, and touching it risks the legacy byte-for-byte guarantee for no requested benefit.

RED: `TestVerifySinceSurfacesUnmarshalErrors` — a malformed `verify_json` row was silently ignored; the completed checkpoint (which should have failed since only THIS malformed row could have carried evidence) succeeded instead of erroring.
GREEN: same test, PASS.

### Finding 10 — two coverage tests

`TestSameRoleDifferentStepNotClosed` (8.2/R1: same role, different step → not closed) and `TestDepUnblockedDedupesTwoAgentsUnderOneParent` (8.3: two agents resolving to the same parent → exactly one relay). Both passed immediately against the already-implemented behavior from earlier units/rulings — legitimate coverage additions, not bug fixes. Ran individually to confirm they'd actually exercise the code path (not vacuously pass), then as part of the full suite.

### Finding 11 — mechanical cleanups

Done first, folded into commits 1–3 (see note above): exported `workflow.SHA7`, removed `internal/runtime/checkpoint.go`'s duplicate `sha7`, fixed `workflow/spec.go`'s `GateTDD` comment, removed the dead `it, err := s.Items.Get(...)` / `_ = it` in `TestSameRoleSiblingStillClosed`.

## Final verification (as specified)

```
$ go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... ./internal/workflow/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	18.954s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	2.030s
ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	12.344s
ok  	github.com/AlexanderTar/agent-swarm/internal/workflow	1.248s

$ go build ./... && go vet ./...
(clean)

$ go test ./... -count=1
... every package ok, except:
--- FAIL: TestBoardServedAtRoot (internal/httpapi) -- the same pre-existing baseline
    failure named in the implementer contract (web bundle not built), unrelated
    to this package, unaffected by any fix-round-1 change.
```

## Remaining concerns after fix round 1

- Finding 5's implementation predates its own tests (disclosed above) — the behavior is now covered, but flagging the process deviation from strict test-first for transparency.
- `TestTDDGateMultiLoopFirstRunNeedsEveryUnit` and `TestTDDGateReviewStepPassedOnlyNeedsEveryUnit` initially passed against buggy code because my first draft didn't plant a decoy that would actually diverge old vs. new behavior — caught by checking discrimination before trusting a green result, not by the review. Worth double-checking any other green test in this codebase that "just passes" was actually exercising the intended failure mode, not merely not crashing.
- No further known gaps; all 3 rulings and 11 findings from `p8-fix1-findings.md` are addressed with commits and either RED→GREEN or explicit passing coverage.

## Fix round 2 (Opus re-review: all 11 round-1 findings + R1-R3 verified ADDRESSED, legacy guarantee intact)

Findings source: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p8-fix2-findings.md`.

Commits (in order):
1. `033c1a9` fix(runtime): story after_tasks steps and multi-review fix-round merging (findings 1-2)
2. `1fe8bd7` test(runtime): pin two unpinned R3 tdd-gate behaviours (finding 3)
3. `a79ec70` fix(runtime): stale approvals on daemon artifact revision bump; drop dead nit (findings 4-5)

### Finding 1 (Important, new breakage) — story after_tasks reviewers refused

`stepFor` only ever walked `spec.Steps`, but a resolved story spec keeps its one review step in `AfterTasks` with `Steps` empty — `workflow.Next` and `workflow.Render` both already promoted `AfterTasks` into a one-element `Steps` slice before looking anything up; `stepFor` never did, so every story `after_tasks` reviewer's `completed` checkpoint (with a workflow run) was refused `"workflow step \"story-review\" not found on STORY-1; ask your orchestrator."` — the exact same guard fix round 1's finding 4 added, now firing on a legitimate path.

Fix: extracted the promotion both `Next` and `Render` already duplicated into `workflow.Spec.EffectiveSteps()` (one shared method, per the finding's suggestion to "reuse the same promotion... rather than a third copy"), then switched `Next`, `Render`, and the new `stepFor` to all call it. Confirmed behavior-preserving for `Next`/`Render` by re-running `./internal/workflow/...` before touching `checkpoint.go` at all.

RED: `TestApplyGatesAcceptsStoryAfterTasksReviewStep` — `err` was the "workflow step ... not found" refusal instead of nil.
```
go test ./internal/runtime/... -run TestApplyGatesAcceptsStoryAfterTasksReviewStep -count=1 -v
FAIL -- "a story after_tasks reviewer with a workflow run must be able to complete: workflow step \"story-review\" not found on STORY-1; ask your orchestrator."
```
GREEN: same test, plus `./internal/workflow/...` re-run to confirm the refactor didn't change `Next`/`Render` behavior.
```
go test ./internal/runtime/... -run TestApplyGatesAcceptsStoryAfterTasksReviewStep -count=1 -v
PASS
go test ./internal/workflow/... -count=1
ok
```

### Finding 2 (Minor) — findFixStepFor only matched the first review step

Two review steps can share a fix target (e.g. `review-code` and `review-ui` both reviewing `build`). `findFixStepFor` returned only the first match by `Steps` order — if that one happened to pass while the *other* requested changes on a specific unit, the gate fell back to "every unit" instead of narrowing to what was actually named, since the passing step's `fixRoundFindings` call (zero findings, no blocking) was the only one ever consulted.

Fix: renamed to `findFixStepsFor` (plural), returns every matching review step; `tddGate` now loops over all of them, merging findings and OR-ing `hasBlocking` across the set — the same "any reviewer's findings count, even a passing one's" principle B4's `mergeFindings` already applies within a single step's several reviewer roles, extended here across several review steps sharing one fix target.

RED: `TestTDDGateMergesAcrossMultipleReviewStepsForSameBuildStep` — `review-code` (first in `Steps` order) passed clean; `review-ui` requested changes on unit 1 only; got the "every unit" fallback (`unit(s) 2` still demanded) instead of narrowing to unit 1.
```
go test ./internal/runtime/... -run TestTDDGateMergesAcrossMultipleReviewStepsForSameBuildStep -count=1 -v
FAIL -- "only unit 1 should be required...: TDD evidence missing for unit(s) 2: ..."
```
GREEN: same test + the full tdd-gate group (13 tests, confirms no regression across every earlier fix-round-1 scenario).
```
go test ./internal/runtime/... -run 'TestTDDGate' -count=1 -v
PASS (13/13)
```

### Finding 3 (Minor) — pin two previously-unpinned R3 behaviors

Both were already correctly implemented (fix round 1) but had no test naming them directly:
- `TestTDDGateBlockedWithZeroFindingsStillNeedsAPair` — a genuine fix round (`blocked` verdict, zero structured findings) still requires one red-before-green pair somewhere; passed immediately (not a bug fix, coverage only).
- `TestTDDGateUnitTaggedFindingOnNonBatchedTaskIsPackageWide` — a stray `unit:5` tag on a finding for a task with no `units` at all is treated as package-wide (an untagged pair satisfies it, not "unit 5" specifically); also passed immediately.

Both run individually first to confirm they actually exercise a real code path (not vacuously green), per the lesson from fix round 1 about un-discriminating tests:
```
go test ./internal/runtime/... -run 'TestTDDGateBlockedWithZeroFindingsStillNeedsAPair|TestTDDGateUnitTaggedFindingOnNonBatchedTaskIsPackageWide' -count=1 -v
PASS (2/2)
```

### Finding 4 (Minor) — registerArtifactAsDaemon must staleApprovals

`registerArtifactAsDaemon`'s revision-bump branch (added in fix round 1's finding 8) never called `staleApprovals` the way `RegisterArtifact` (`artifacts.go`) does on every revision — an open `approve_section` request stayed `open` even after a fix round's revised design/notes superseded the content it was bound to.

Fix: added `prevSectionsJSON` to the existing artifact-lookup query (mirrors `RegisterArtifact`'s own shape exactly: `r.sections_json` alongside `r.sha256`), and on the bumped-revision path only (`bumped bool`, false for a fresh registration or a deduped no-op — neither of which could have any open approval yet, or anything that changed), unmarshal the previous sections and call `s.staleApprovals(ctx, tx, artifactID, sectionsChanged(prevSections, sections))` after the new `artifact_revisions` row is inserted.

RED: `TestRegisterArtifactAsDaemonStalesOpenApprovals` — registered v1, opened an `approve_section` request bound to it, registered v2 (content changed) — request stayed `open` instead of going `stale`.
```
go test ./internal/runtime/... -run TestRegisterArtifactAsDaemonStalesOpenApprovals -count=1 -v
FAIL -- "open approve_section request state = \"open\", want stale"
```
GREEN: same test, plus the fix round 1 artifact-revision tests re-run to confirm dedupe/bump still work correctly with the new `prevSectionsJSON`/`bumped` plumbing.
```
go test ./internal/runtime/... -run 'TestRegisterArtifactAsDaemonStalesOpenApprovals|TestRegisterArtifactAsDaemonRecordsRevisionsAndDedupes|TestDesignArtifactGateRegistersArtifact' -count=1 -v
PASS (3/3)
```

### Finding 5 (Nit) — dead `p != root` in artifactGate

`root` (the gate's own directory) is itself a directory; any candidate path equal to `root` would always fail the immediately-following `fi.IsDir()` check anyway, so the `p != root` disjunct in `if p != root && !strings.HasPrefix(p, prefix)` never changed which paths were accepted — removed, leaving just the `HasPrefix` check (which was already doing all the real work). No test needed (a no-op removal); re-ran the artifact-gate group to confirm nothing regressed.
```
go test ./internal/runtime/... -run 'TestArtifactGate|TestDesignArtifactGate|TestExpandHome|TestRegisterArtifactAsDaemon' -count=1 -v
PASS (5/5)
```

## Final verification (fix round 2, as specified)

```
$ go test ./internal/runtime/... ./internal/items/... ./internal/mcpserver/... ./internal/workflow/... -count=1
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	19.131s
ok  	github.com/AlexanderTar/agent-swarm/internal/items	2.148s
ok  	github.com/AlexanderTar/agent-swarm/internal/mcpserver	12.484s
ok  	github.com/AlexanderTar/agent-swarm/internal/workflow	0.542s

$ go build ./... && go vet ./...
(clean)

$ go test ./... -count=1
... every package ok, except the same pre-existing baseline failure named in the
    implementer contract: internal/httpapi TestBoardServedAtRoot (web bundle not
    built) -- unrelated to this package, unaffected by any fix-round-2 change.
```

## Remaining concerns after fix round 2

- None known. All 5 items from `p8-fix2-findings.md` (1 Important breakage, 3 Minor, 1 Nit) are addressed with commits; findings 1, 2 and 4 have RED→GREEN evidence above, finding 3 has explicit passing coverage for two previously-unpinned behaviors (verified individually, not just in the full suite, to avoid the fix-round-1 lesson about vacuous greens), and finding 5 is a verified no-op cleanup.
- `workflow.Spec.EffectiveSteps()` is now the single shared source of the story after_tasks promotion, used by `Next`, `Render`, and `stepFor` — worth keeping in mind for P9 (the engine) as the canonical way to resolve "what are this spec's steps" rather than re-deriving `len(Steps)==0 && AfterTasks != nil` a fourth time.

# Worktree cleanup enforcement — implementation plan

Date: 2026-09-23
Spec: `docs/specs/2026-09-23-worktree-cleanup-enforcement.md` (read that first — this
plan sequences it, it does not restate its rationale)
Worktree: `../agent-swarm--worktree-cleanup-enforcement`, branch `feat/worktree-cleanup-enforcement`

## Rulings on ambiguities not resolved by the spec's own §10

**Test-file placement.** The spec's §6 file list assigns "Tests 1–7, 12–16" to
`worktree_test.go` and "Tests 8–11" to `reconcile_test.go`, but doesn't mention
where Test 17 goes, and Tests 3, 14 and 16 are fundamentally about the
runtime-level gate/loop (live-session exclusion, per-candidate fault
tolerance, sibling-orchestrator eligibility) — logic that only exists in
`internal/runtime`, per the spec's own §4.1 package split ("runtime owns the
gate ... worktree does git"). Ruling, made once, applied consistently:

- `internal/worktree/worktree_test.go`: Tests **1, 2, 4, 5, 6, 7, 12, 13, 15,
  17** — every scenario whose assertion is about git rules or the
  lock/transition mechanics of `ReclaimOne`/`Candidates`/`Sweep`, addressable
  without an agents/sessions gate.
- `internal/runtime/reconcile_test.go`: Tests **3, 8, 9, 10, 11, 14, 16** —
  every scenario whose assertion is about candidacy (who the gate excludes or
  admits) or the reclaim loop's own fault tolerance.

This changes which file asserts what, never what is asserted — all 17 unit
scenarios, all 9 guard scenarios and the end-to-end scenario are still real,
separately-runnable tests.

**`cfg.Worktrees()`.** §4.6 writes `WorktreesDir: cfg.Worktrees()` at the
daemon's `hook.Handler` construction site, but `cfg` there is `daemonConfig`
(`cmd/swarm/daemon.go`), not `install.Config` — `daemonConfig` has no
`Worktrees()` method and importing `internal/install` into `daemon.go` for one
call is not worth it. Use `filepath.Join(cfg.Home, "worktrees")` instead —
byte-identical to `install.Config.Worktrees()`'s own implementation and to
`worktree.Service.worktreesDir()`. `path/filepath` is already imported there.

**Scenario 14's "unreadable repo".** As literally written this doesn't
produce a *failure*: `DirtyStrict` failing just reads as dirty and retains
(a *kept*, not a *failed* — this is decision-2-correct behavior, not a bug).
The only real error paths inside `remove()` are `repoPath()` failing, the
final `UPDATE`, or `retain`'s transaction (including `OnRetained`). Rig the
middle candidate's failure via a stubbed `OnRetained` that errors only for
that worktree's ID (its git state is `dirty`, so it hits `retain`, whose
`OnRetained` call then fails) — this is a real error `ReclaimWorktrees` must
not let stop the other two.

## Fixture pitfalls (from advisor review, confirmed against the code before
## implementation — record here so no task rediscovers them)

- **`finished_at` matters.** The gate requires `a.finished_at IS NOT NULL AND
  a.finished_at <= now-grace`. The existing
  `TestSweepRunsOnlyWhenTheWholeTreeIsFinished` pattern (`UPDATE agents SET
  state = 'finished'`) never sets `finished_at`, so copying it verbatim would
  make every negative gate test (3, 9, 10) pass vacuously — the row is
  already excluded by `finished_at IS NULL`, whether or not the guard under
  test does anything. Every negative test needs `finished_at` set to a value
  past the grace window, and a control assertion: flip the one condition the
  test is actually about (live session ends / descendant finishes / other
  reservation releases) and assert the worktree *becomes* a candidate.
  Scenario 8 already has this shape by construction (it's the grace window
  itself); give 3, 9, 10 the same before/after shape.
- **`retain`'s transition guard must read `wt` before mutating it.** The
  existing code (`worktree.go:203`) sets `wt.State, wt.RetainedReason` at the
  top of `retain`, before the new `changed` computation would run — write
  `changed := wt.State != "retained" || wt.RetainedReason != reason` as the
  *first* line of the function, before those two assignments, not after.
- **`Candidates`' WHERE clause needs the `w` alias**, the same way `ForAgent`
  already does it (`query(ctx, "w WHERE owner_agent_id = ? OR ...", ...)`):
  `query()` appends its argument after `FROM worktrees `, so the gate SQL
  (which starts `WHERE w.state ...`) must be called as
  `Candidates(ctx, "w WHERE w.state IN (...) AND EXISTS (...)", args...)` or
  the `w.` column references don't resolve.
- **`newStore` (runtime test fixture) does not wire `OnRetained`.** Any
  runtime-side test that needs a real "Worktree kept" notification (E2E step
  5, and any runtime test touching a retain transition) must set
  `s.Worktree.OnRetained = s.OnWorktreeRetained` itself after `newStore`
  returns.
- **Seed pre-retained fixture rows directly**, not by calling `Remove`/
  `ReclaimOne` to get there: scenarios 12 and 13 want a worktree that starts
  `retained` (`UPDATE worktrees SET state='retained', retained_reason=...`),
  not one retained live through a call that would itself raise a
  notification and pollute an "exactly N notifications" assertion later in
  the same test (the E2E scenario especially).

## Task sequence

Each task: write the failing test(s), run them and confirm the fail mode
matches the change, implement, rerun, commit. Commits are per logical unit,
not per test.

### 0. Bring the spec into the worktree

Copy `docs/specs/2026-09-23-worktree-cleanup-enforcement.md` into this
worktree (already done before this plan file was written) and commit both
files as the first commit — the branch was cut from `main`, which has neither
file, and the plan's own `Spec:` reference must resolve inside this worktree.

### 1. `internal/worktree/worktree.go` — hardening edits (§4.4)

1a. `markRemoved` + the path-missing branch in `remove` (spec's code block,
    §4.4(a)). Test: `TestRemoveOfAVanishedPathMarksRemovedWithoutAnyGitCall`
    (scenario 5) — a worktree row whose `Path` doesn't exist; assert
    `state='removed'`, `removed_at` set, and (via a recording `execx.Runner`,
    the `recordingRunner`/`fake.Calls()` pattern already in the test file)
    zero subprocess calls.

1b. `atDetachedSHA` + the detached-branch guard in `remove` (§4.4(b)). Tests:
    `TestRemoveDeletesADetachedWorktreeStillAtItsSHA` (scenario 6, extends the
    existing `TestReviewCreatesADetachedWorktreeAtTheSHA` pattern) and
    `TestRemoveRetainsADetachedWorktreeThatMovedOffItsSHA` (scenario 7 — make
    a local commit in the detached worktree, then remove; expect
    `retained/unmerged`).

1c. `retain`'s transition guard (§4.4(c)) — mind the ordering pitfall above.
    Test: `TestRetainDoesNotRenotifyOnAnUnchangedReason` (scenario 13) — call
    `s.Remove` twice on a still-dirty worktree with `OnRetained` stubbed to
    count calls; assert the count is 1, not 2. (No pre-existing test asserts
    the old repeat-notify behavior — `TestOnRetainedFires` only removes once —
    so there is nothing to update, only this new test to add.)

Run: `go test ./internal/worktree/... -run 'Remove|Retain' -v`

### 2. `internal/worktree/worktree.go` — `ReclaimOne` and `Candidates` (§4.2)

Extract `Sweep`'s loop body into `ReclaimOne` (same lock-then-`remove`
sequence); `Sweep` calls it. Export `query` as `Candidates` (identical body,
still appending its argument after `FROM worktrees `).

Tests:
- `TestReclaimOneRemovesACleanMergedWorktree` (scenario 1 — call `ReclaimOne`
  directly on a clean, merged worktree; assert `removed`).
- `TestReclaimOneRetainsAnUnmergedWorktree` (scenario 2, via `ReclaimOne`).
- `TestReclaimOneRetainsADirtyWorktree` (scenario 4, via `ReclaimOne`).
- `TestReclaimOneReEvaluatesAnAlreadyRetainedWorktree` (scenario 12 — seed a
  worktree already `retained/unmerged` directly via SQL, merge the branch,
  call `ReclaimOne`; assert `removed`).
- `TestReclaimOneRacingShareBehavesLikeRemove` (scenario 15 — same rig as
  `TestShareRefusesAConcurrentClaimRace`, calling `ReclaimOne` instead of
  `Remove`).
- `TestSweepStillMatchesPreRefactorBehaviorViaReclaimOne` (scenario 17 —
  thin, explicit regression alongside the existing `TestSweepRemoves...` and
  `TestSweepRefusesAConcurrentShareDuringRemoval`, which already exercise the
  refactored path; this one exists for direct traceability to scenario 17).
- `TestCandidatesAppliesAnArbitraryWhereClause` — smoke test that
  `Candidates` is `query` exported unchanged (one worktree, trivial clause).

§8.4 paths that belong at this package level (through `ReclaimOne`, not just
the pre-existing `Remove` tests):
- `TestReclaimOneRetainsOnRemoveFailure` — Decline: `git worktree remove`
  exits non-zero → `retain("remove_failed")`, directory untouched, via
  `ReclaimOne`.
- `TestReclaimOneTreatsAGitStatusFailureAsDirty` — Error: `git status` fails
  → dirty → retained, via `ReclaimOne` (the existing
  `TestRemoveTreatsAGitFailureAsDirty` covers `Remove`; this is the same rig
  through the new exported method, since `Remove` and `ReclaimOne` must agree).

Run: `go test ./internal/worktree/... -run 'Reclaim|Sweep|Candidates' -v`

### 3. `internal/runtime/reconcile.go` — the gate and the loop (§4.3)

Add `reclaimGrace`, the gate SQL (verbatim from §4.3, called through
`Candidates` with the `"w WHERE ..."` alias form). Factor the live-descendant
query (currently inlined in `owesNothing` at `:819`) into a small unexported
helper, e.g. `func (s *Store) liveDescendants(ctx, q txQuerier, agentID
string) (int, error)`, and have both `owesNothing` and the new gate call it —
two call sites with the same literal SQL is exactly the kind of duplication
worth collapsing once, and the spec explicitly says "reused verbatim".

`ReclaimWorktrees(ctx) error`:
1. `Candidates` with the gate WHERE + `now - reclaimGrace` arg.
2. For each candidate, in a loop that checks `ctx.Err()` first (Cancellation,
   §8.4): if cancelled, stop and return with results collected so far.
3. Run the live-descendant check; skip (log `keeping %s (%s)`, reason
   `"has an active descendant"` or similar) on non-zero.
4. Otherwise call `s.Worktree.ReclaimOne`; log per outcome per §5.2's exact
   strings; never return early on a single failure — collect and continue
   (scenario 14).
5. One summary log line at the end: `worktree: reclaim pass: %d reclaimed,
   %d kept, %d failed`.

`ReclaimWorktreesLoop(ctx, every)`: same shape as `ReconcileLoop`.

Tests (`internal/runtime/reconcile_test.go`, using `newStore`/`clockStore`,
`seedRepo`, real agent fixtures with `finished_at` explicitly set — see the
fixture pitfalls above):
- `TestReclaimExcludesAnOwnerWithALiveSession` (scenario 3 — owner `finished`
  + `finished_at` set past grace, but has a live `running` session; assert
  via a recording `execx.Runner` on `s.Worktree.Run` that zero git commands
  ran; control: end the session, rerun, assert it reclaims).
- `TestReclaimWaitsOutTheGraceWindow` (scenario 8 — finished 10 min ago: not
  a candidate; advance the clock past `reclaimGrace`: candidate).
- `TestReclaimExcludesAnOwnerWithALiveDescendant` (scenario 9 — control:
  finish the descendant, rerun, assert it reclaims).
- `TestReclaimExcludesAWorktreeWithAnotherAgentsUnreleasedReservation`
  (scenario 10 — control: release it, rerun, assert it reclaims).
- `TestReclaimIgnoresTheOwnersOwnUnreleasedReservation` (scenario 11).
- `TestReclaimContinuesPastOneCandidatesFailure` (scenario 14 — three dirty
  candidates via a stubbed `OnRetained` that errors only for the middle
  worktree's id; assert first and third end up `retained/dirty`, the error
  is recorded/returned but does not stop the pass, and the summary line's
  `%d failed` is 1).
- `TestReclaimIncludesASiblingOrchestratorsWorktree` (scenario 16 — two
  top-level agents, same `root_item_id`, `parent_agent_id = NULL` on both;
  `B` active; assert `A`'s worktree is still reclaimed).

§8.4 paths at this level:
- `TestReclaimProcessesFiveHundredCandidatesInOnePass` (Exhaust — 500
  vanished-path worktree rows, all owned by one already-finished agent past
  grace, so each hits the path-missing branch with zero git calls and the
  pass stays fast; assert all 500 end `removed` and one summary log line).
- `TestReclaimStopsPromptlyOnCancellation` (Cancellation — a `Run` hook that
  cancels the context on the first `status` call; assert the remaining
  candidates are untouched, i.e. still their original state, and
  `ReclaimWorktrees` returns without error or with `ctx.Err()`, matching
  "returns promptly with the results collected so far").

Run: `go test ./internal/runtime/... -run 'Reclaim' -v`

### 4. `internal/hook/worktreeguard.go` (new) — the `PreToolUse` deny (§4.5)

New file: `swarmWorktreeMutation`, `worktreeGuardOrchestrator`,
`worktreeGuardWorker`, `blocksWorktreeMutation` — copied from the spec's code
block verbatim (it's already exact Go).

`internal/hook/handler.go`: add `WorktreesDir string` to `Handler`; wire the
three-line block into `PreToolUse`'s `in.Command != ""` branch, after the
`isClaudeCommand` check and before `AttrCheck` (spec §4.5's exact snippet).

Tests, new file `internal/hook/worktreeguard_test.go`, modeled on
`TestPreToolUseBlocksNestedClaudeShellCommand`'s JSON-in/deny-out shape:
- G1–G6, G9: table-driven over `blocksWorktreeMutation` directly (pure
  function, no `Handle` plumbing needed) — command, `worktreesDir`, want-block.
- G7: through `h.Handle(..., "PreToolUse", ...)` with `ParentAgentID == ""`
  (the seeded agent's default) and G1's command; assert deny +
  `worktreeGuardOrchestrator` reason.
- G8: same but `UPDATE agents SET parent_agent_id = 'agt_1' WHERE id =
  'agt_1'` first (the self-FK trick `TestParentedAgentQuestionToolIsBlocked...`
  already uses); assert deny + `worktreeGuardWorker`, and assert the reason
  string does **not** contain `swarm_worktree`.
- G5 also needs a `Handler{WorktreesDir: ""}` case to prove existing literals
  (empty by default) keep compiling and keep allowing.

Run: `go test ./internal/hook/... -run 'BlocksWorktreeMutation|WorktreeGuard' -v`

### 5. `cmd/swarm/daemon.go` — wiring (§4.6)

Two edits: `WorktreesDir: filepath.Join(cfg.Home, "worktrees")` on the
`hook.Handler` literal (line ~266), and
`func(ctx context.Context) { dm.rt.ReclaimWorktreesLoop(ctx, 10*time.Minute) },`
added to the `loops` slice beside `ReconcileLoop`/`WakeLoop`. No new test file
— `cmd/swarm` has no existing unit-test coverage of the loops slice; `go
build ./...` and `go vet ./...` are the verification for this file per §8.1.

### 6. Skill copy (§5.3, §5.4)

`skills/swarm/SKILL.md` line 22, `skills/swarm-orchestrator/SKILL.md` lines
16 and 49 — exact replacement text from the spec. No test; verify by reading
the diff.

### 7. End-to-end scenario (§8.3)

New test, `internal/runtime/reconcile_test.go`:
`TestReclaimWorktreesEndToEndOverTwoPasses` — seven worktrees modeling
scenarios 1, 2, 3, 4, 5, 6, 13 in one fixture DB + fixture repos
(`s.Worktree.OnRetained = s.OnWorktreeRetained` wired explicitly — see the
fixture pitfalls above), one `ReclaimWorktrees` pass, assertions per §8.3
steps 3–5, then a second pass. For "DB byte-identical" (step 6), dump
`SELECT * FROM worktrees ORDER BY id` (all columns) before and after the
second pass and compare the two dumps.

### 8. Full verification pass (§8.1, plus a whole-repo check)

```bash
cd ../agent-swarm--worktree-cleanup-enforcement
go build ./...
go test ./internal/worktree/... -run 'Reclaim|Retain|Remove|Sweep' -v
go test ./internal/runtime/... -run 'Reclaim' -v
go test ./internal/hook/... -run 'BlocksWorktreeMutation|WorktreeGuard' -v
go test ./internal/worktree/... ./internal/runtime/... ./internal/hook/... ./cmd/...
go vet ./...
go test ./...
```

No `~/.swarm/swarm.db` access anywhere in this plan. §8.5's manual smoke is
optional/informational and not executed as part of this plan.

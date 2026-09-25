# P6 fix round 1 report

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p6fix` (branch `pkg/p6fix`)
Findings: `p6-fix1-findings.md`

## Important: pinnedFrom's stale filter breaks the resume path

**Root cause.** f97734e made a same-round stale review (recorded sha no
longer matching what it reviewed) escalate instead of respawning — correct,
since the `(step, round, role)` key already has a row. But `pinnedFrom`'s new
staleness filter then dropped that exact row as "not a real signal" when
computing where the *next* round should pin from. The only way such a row
can survive to the previous round at all is an orchestrator resume after
that exact escalation — so filtering it out left `pinnedFrom` with no
signal, and it fell through to "pin everything," re-spawning an unrelated,
already-passed earlier step instead of waiting on/resuming the fix step.

**Fix** (`internal/workflow/next.go`): rewrote `pinnedFrom` as three ordered
phases instead of one filtered loop:
1. `pinFromVerdict` — any `changes_requested`/`blocked` row at the previous
   round pins its fix step, **regardless of staleness** (dropped the sha
   filter).
2. `pinFromStale` — only runs if phase 1 found nothing: any review row at
   the previous round whose sha no longer matches what its `Of` completed
   with (any verdict/state — pass, still active, even a crashed reviewer)
   also pins its fix step. This is the second bullet of the Important
   finding: a stale *pass* row (the shape left behind once a same-round
   stale-review escalation is resumed) must pin too, not fall through to
   "pin everything."
3. Existing failed/cancelled fallback, then "pin everything."

**Judgment call**, confirmed with the advisor before writing code: phase 2
had to be strictly lower-priority than phase 1 and only consulted when phase
1 found nothing at all, scanning *all* review steps — not "the first stale
row wins." Otherwise `TestNextCarriedStaleReviewIsDroppedAndRespawned/S10`
(a stale-but-Pass `ra` row at fix index 0, alongside a genuine, non-stale CR
on `rb` at fix index 2) would wrongly pin at 0 instead of 2, since S10's own
`ra` staleness is structurally identical to the new stale-pass scenario —
the only difference is which pair carries it. I verified this by tracing
S10 through both phases before implementing, then confirmed with a full
test run (S10 still green, both new subtests green).

**Tests**: renamed `TestNextPinnedFromIgnoresStaleReviews` to
`TestNextPinnedFromStaleReviewStillPinsFixStep` (the premise inverted: a
stale review no longer gets ignored for pinning purposes). Flipped its
"clearly distinguishing" subtest to expect `Wait b` instead of `Spawn a`.
Added a third subtest for the stale-pass-row case (`Wait b`, extraRounds=1
to reflect the resume). Left the "S12 literal" subtest as-is for this
commit (verified it still passes unchanged); deleted in the minors commit
per the finding.

**Dedup** (mentioned in the Important-fix diff, formally the Minor #2
ask): implementing the phase split required a clean "runs for a step given
the pin state" primitive, so I introduced `completedSHA(runs []Run) string`
and reused the existing `currentRuns` closure at the one other call site
(the failure-handling loop, which used to call `stepSHA`). Deleted
`stepSHA` and `completedSHAAt` — both now redundant.

RED (before fix):
```
$ go test ./internal/workflow/... -run 'TestNextPinnedFromStaleReviewStillPinsFixStep' -v -count=1
--- FAIL: TestNextPinnedFromStaleReviewStillPinsFixStep (0.00s)
    --- PASS: .../S12_literal... (0.00s)
    --- FAIL: .../a_stale_CR_pins_at_its_own_fix_step_(b)...
        next_test.go:1388: Next() = {Kind:spawn StepID:a ...}, want {Kind:wait StepID:b ...}
    --- FAIL: .../a_stale_PASS_row_(stale-escalation,_then_resumed)...
        next_test.go:1413: Next() = {Kind:spawn StepID:a ...}, want {Kind:wait StepID:b ...}
```

GREEN (after fix): full `go test ./internal/workflow/... -count=1 -v` passed,
including `S10` and all other pre-existing stale/pin tests.

Commit: `7c75604 fix(workflow): pinnedFrom trusts a stale review's step id, not its sha`

## Minors

1. **Fresh blocked hidden by stale sibling** (next.go:186-192). `staleReviews`
   returned as soon as it hit the first same-round stale row, discarding any
   fresh rows already collected — so a genuinely fresh, blocked sibling
   reviewer's verdict got hidden behind the stale-review escalation reason
   whenever iteration happened to reach the stale row's role second.

   Fix: `staleReviews` now always finishes collecting the full fresh list
   (and remembers only the *first* same-round stale escalate, not the last)
   before returning; `Next` checks `blockedReason(fresh)` before falling
   back to the stale escalate. Factored the "`<role> blocked[: summary]`"
   reason-building (previously inlined once) into a shared `blockedReason`
   helper, used at both the stale-escalate-priority check and the ordinary
   blocked check.

   New test: `TestNextFreshBlockedNotHiddenByStaleSibling` (RED confirmed
   before the fix: got the stale-review reason instead of
   `"reviewer blocked: secrets in repo"`).

2. **stepSHA/completedSHAAt duplication.** Already resolved as a side effect
   of the Important-fix commit (see "Dedup" above) — no separate change
   needed here.

3. **S12 "literal" subtest.** Deleted per the finding: its own comment
   already said it "can't tell the two implementations apart on its own,"
   so it wasn't exercising anything the sibling subtests don't already
   cover.

4. **Spec gap.** Added the stale-review reason format
   `"<role> reviewed <sha7>, but <of> is now at <sha7>"` to B4's `Escalate`
   row and to "All user-facing copy," and added a sentence to B7's `resume`
   description stating that `retry` after a stale-review escalation re-runs
   the fix step, then the review at the fresh sha — the same as an ordinary
   `changes_requested`, never a re-review of the stale sha.

Commits:
- `71bd100 fix(workflow): a fresh blocked verdict wins over a stale sibling's escalation`
- `8d43e54 docs(spec): document the stale-review escalate reason and its resume path`
- `e5877d4 docs(spec): move the stray period outside the stale-review copy quote`
  (advisor caught the quoted copy string had a period the code doesn't
  emit — fixed as its own commit rather than amending.)

## Verify

```
$ go test ./internal/workflow/... -count=1 && go vet ./internal/workflow/...
ok  	github.com/AlexanderTar/agent-swarm/internal/workflow	0.192s
```
(vet produced no output — clean.)

## Files changed

- `internal/workflow/next.go`
- `internal/workflow/next_test.go`
- `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`

## Self-review / concerns

- Only `internal/workflow/` and the spec file were touched, per the
  dispatch's scope.
- No existing test was deleted or weakened except the explicitly-named S12
  literal subtest, authorized by the findings (contract line 10).
- `go build ./...` ran clean each verify pass (chained before `go vet` and
  `go test`).
- I did not re-run the reviewer's probe file
  (`scratchpad/p6probe/workflow/probe_test.go`) as an actual test in the
  package — it's a scratch/manual `t.Logf` probe, not an assertion-based
  test, and it lives outside `internal/workflow/`. I used it only to
  understand the exact repro shape and mirrored its scenarios into proper
  assertion tests in `next_test.go`.

## Fix round 2

Finding: `p6-fix2-findings.md` — one regression from round 1's Important
fix. `pinnedFrom`'s new stale-row phase (added to pin at a stale review's
fix step when nothing else explains the round bump) ran *before* the
pre-existing failed/cancelled scan. An unrelated stale review row on an
already-passed, unrelated pair could therefore steal the pin away from the
step that actually crashed, resuming at the wrong step.

**Repro** (twoPairsSpec `a`/`ra`, `b`/`rb`): round 1 — `a` re-completes at
`a1new` after `ra` already passed it at `a1` (so `ra` is now stale, but
harmlessly — `a`'s fresher sha carries forward fine on its own); `b`
crashes with retries exhausted. Round 1 correctly escalates the crash. On
resume (`Next(two, runs, 2, 1)`), pinnedFrom's stale-row phase (checked
before the failed/cancelled phase) found `ra`'s unrelated staleness first
and pinned at `a` (`ra`'s fix step) — wrongly re-spawning `a`, a step that
had already passed review. It should instead pin at `b` (the step that
actually crashed), leaving `a`/`ra` carried forward, where `ra`'s ordinary
earlier-round staleness gets dropped and respawned by the existing per-step
review evaluation (not by pinnedFrom).

**Fix** (`internal/workflow/next.go`): reordered `pinnedFrom`'s phases so
the failed/cancelled scan (extracted into `pinFromFailed`) runs *before*
the stale-row scan (`pinFromStale`) — order is now: verdict (CR/blocked,
unconditional of staleness) → failed/cancelled → stale-row → pin
everything. `pinFromFailed` also now skips a review step's row when that
row is stale, mirroring the same skip `Next`'s failure-handling loop
already applies (next.go's `currentRuns`/`completedSHA` check at the top of
`Next`): a review step whose own row is both stale and crashed (e.g. a
reviewer that crashed reviewing a since-superseded sha) isn't real crash
evidence for that step — it's the same stale-review-resume shape
`pinFromStale` already handles, and must still pin at the review's fix
step, not the review's own index. This is exactly the finding's second
requirement ("the stale-crashed-rb resume must still reach phase 2 and
return Spawn b").

**Test**: `TestNextPinnedFromFailedOutranksStale`, two subtests, each
asserting both the round-1 escalation reason and the resume's pinned
action:
1. `b` crashed (exhausted); `ra`'s unrelated staleness must not steal the
   pin from `b` — this is the reported regression.
2. `rb` crashed *and* is stale — the resume must still re-run `b` (its fix
   step), not just re-spawn `rb` — this is what `pinFromFailed`'s
   stale-skip exists for; it already passed even before this round's fix
   (since the pre-fix phase order happened to reach the stale phase before
   the failed phase for this specific shape), so it locks in the *new*
   ordering doesn't regress it.

RED (before fix, both subtests run — only the first was actually red):
```
$ go test ./internal/workflow/... -run 'TestNextPinnedFromFailedOutranksStale' -v -count=1
--- FAIL: TestNextPinnedFromFailedOutranksStale (0.00s)
    --- FAIL: .../b_crashed_(exhausted);_ra's_unrelated_staleness_must_not_steal_the_pin_from_b (0.00s)
        next_test.go:1448: resume: Next() = {Kind:spawn StepID:a Roles:[coder] Round:2 ...},
            want {Kind:spawn StepID:ra Roles:[reviewer] Round:2 SHA:a1new ...}
    --- PASS: .../rb_crashed_AND_is_stale;_the_resume_must_still_re-run_b_(its_fix_step),_not_just_re-spawn_rb (0.00s)
```

GREEN (after fix):
```
$ go test ./internal/workflow/... -count=1 && go vet ./internal/workflow/...
ok  	github.com/AlexanderTar/agent-swarm/internal/workflow	0.202s
```
Full verbose run confirmed no regressions across all existing tests
(`TestNext`, `TestNextThreeLoops`, `TestNextFixIndexCarryForward`,
`TestNextBlockedPinsLikeChangesRequested`, `TestNextCarriedStaleReviewIsDroppedAndRespawned`,
`TestNextPinnedFromStaleReviewStillPinsFixStep`, etc. — all pass).

Commit: `4e66a0e fix(workflow): pinnedFrom's crash phase outranks its stale-row phase`

Also updated the top-of-file `Next` doc comment to describe the corrected
phase order (crash phase before stale-row phase) in the same commit.

### Self-review

- Verified by tracing the pre-f97734e base (`23b5368`) `pinnedFrom`
  manually before writing the fix, to confirm the expected "Spawn ra
  @a1new" result was reachable through the *existing* carried-forward
  stale-drop-and-respawn machinery once the pin index was corrected — not
  something pinnedFrom itself needed new logic for.
- Only `internal/workflow/next.go` and `internal/workflow/next_test.go`
  touched; no spec changes needed for this round.
- No existing test deleted or weakened.

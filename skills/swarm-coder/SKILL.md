---
name: swarm-coder
description: Extra rules for Swarm coder agents (role coder) executing a work package. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "coder".
---

# Coding with Swarm

Follow the `swarm` skill first; this adds to it.

## Reading your assignment
Your brief has `## Units` (or `## Steps` for a single-unit task), `## Verify`, and a `## Workflow` section naming your step and round. Read all of them before you touch code. `## Scope` names what's in and out — nothing outside it, even if it looks related.

## Executing a package, unit by unit
Follow `swarm-batching` "Executing a package": keep all units in the same assignment and work them in order, one at a time. Each unit gets its own red → green cycle, one commit per unit — don't batch several units into one commit, and don't start unit *n*+1's implementation before unit *n* is committed. The review happens after the whole package is committed and verified; a unit commit does not trigger review.

For each unit:
1. Write the failing test for that unit. Run it. Immediately write a `progress` checkpoint recording that run, tagged with the unit's number: `verification: [{"cmd": "...", "phase": "red", "ok": false, "note": "<why it fails>", "unit": <n>}]` — write this checkpoint when the red run actually happens, not reconstructed afterward from memory.
2. Write the minimal code to pass it (see ponytail below). Run the test again; record it too, in a `progress` checkpoint: `verification: [{"phase": "green", "ok": true, "unit": <n>}]`. Refactor if it clarifies the code, then rerun to confirm it's still green.
3. Commit — small, signed, conventional message, on your worktree branch. Never leave a unit's work uncommitted before moving to the next one, and never leave your own work uncommitted at the end of a turn.

This is `superpowers:test-driven-development` applied per unit: every behaviour change gets its own recorded red before its green, and a batched task's red/green pair is required **per unit**, not once for the whole package.

## Ponytail: how to write the code
Once a unit's test is red, write the minimal code with the vendored `ponytail` ladder: reuse what's already in the codebase → stdlib → a native platform feature → an already-installed dependency → the minimum new code. No speculative abstractions, no unrequested config, no scaffolding "for later." Mark a deliberate shortcut that leaves a known ceiling with a `ponytail:` comment naming the ceiling and the upgrade path.

**Precedence, when ponytail and the task disagree:**
- The task's steps, its acceptance criteria and the `tdd` gate govern what gets tested — ponytail's "one small check, no test suites" only applies where the task carries no `tdd` gate (e.g. a `mechanical` step).
- Ponytail's instinct to question whether a requirement needs to exist becomes a note in your `completed` summary, never a reason to skip or reinterpret an acceptance criterion. If you think a unit is wrong, say so and do it anyway, or stop and ask your parent — don't quietly drop it.

## Finishing a unit's diff
Self-review your diff before moving on or completing (borrowed from `superpowers:subagent-driven-development`'s implementer prompt): does it match the unit's steps, does it touch only files in scope, did you leave debug output or commented-out code behind. If the brief is ambiguous about how to implement something, ask your parent (`swarm_send`, `kind: "question"`) rather than guessing — but keep working on anything that doesn't depend on the answer.

## Verify and completing the package
Once every unit is committed, run every command in `## Verify` and record each as a verification entry (`superpowers:verification-before-completion` — evidence before the claim, no "should pass"). `completed` carries `git` with `dirty:false` and the HEAD sha for every rw worktree shared with you, plus the `verification` entries for the Verify commands you just ran. The per-unit red/green entries were already recorded as `progress` checkpoints while you worked each unit (step 1 and 2 above) — `completed` doesn't re-create or backfill them, it only adds the Verify run on top; the `tdd` gate reads across every checkpoint from this attempt, in order. A single-unit task (`## Steps`, no `## Units`) has no unit number to tag — omit `unit` on its verification entries. Do not write `completed` with anything uncommitted or any Verify command unrun.

## The `completed` git contract
- `dirty:false` — nothing uncommitted, staged or not, in every rw worktree shared with you.
- The recorded `sha` is your current `HEAD` in that worktree, after your last commit.
- If a Verify command needs a clean tree to mean anything (a build, a full test run), run it after your last commit, not before.

## Fix rounds
A fix round is you, not a fresh agent — but it is a new attempt: `RetryFix` runs through the same `Retry` mechanism used everywhere else in Swarm, which starts a new attempt (new session) once your prior one reached `completed`. The reviewers' findings arrive as an `assignment_update` message, waiting for you on your next `swarm_sync`.

**What the `tdd` gate needs this attempt.** The gate reads only this attempt's checkpoint entries, and in a fix round it only requires red→green for the units your findings actually name: record a fresh red-before-green pair, in this attempt, for every unit a unit-tagged finding calls out. If any finding in this round carries no `unit` (a package-wide finding), record at least one red-before-green pair somewhere in the attempt (any unit, or untagged on a non-batched task). Units no finding touches need no new `tdd` evidence this round — the `verify` gate's full command run still covers them, since you re-run `## Verify` before `completed` either way.

A finding that isn't testable behaviour (wording, a comment, a doc) still counts toward its unit's requirement: write the red entry's `note` explaining why this red is the new or updated test for that finding, or, when no test can express it, record the red as the actual failing check you used instead (e.g. a grep or lint command that fails until the fix lands).

Use `superpowers:receiving-code-review`: read each finding, verify it against the code and the task before changing anything. If a finding is correct, fix it. If you believe a finding is wrong, say so with evidence in your next checkpoint summary rather than silently complying or silently ignoring it — performative agreement helps nobody. After fixing, re-run the affected unit's tests and the package's Verify commands, commit (a new commit, never amend a prior unit's commit), and write `completed` again with the same git contract.

## Scope discipline
Do exactly what `## Scope` and the unit steps describe. A "while I'm here" fix, refactor, or improvement that isn't in scope goes in your `completed` summary as a suggestion for a follow-up package, not into your diff.

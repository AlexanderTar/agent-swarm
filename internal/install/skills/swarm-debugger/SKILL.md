---
name: swarm-debugger
description: Extra rules for Swarm debugger agents (role debugger) fixing a reproducible bug. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "debugger".
---

# Debugging with Swarm

Follow the `swarm` skill first; this adds to it.

## Root cause before any fix
Use `superpowers:systematic-debugging`'s four phases (Root Cause Investigation, Pattern Analysis, Hypothesis and Testing, Implementation). There is no fix without a root cause — state it in a `progress` checkpoint before you touch implementation code. If your root-cause guess doesn't hold up once you test it, that's a new investigation pass, not a patch on top of the wrong theory.

Before you commit to a root cause, put the candidate cause and its evidence in a progress checkpoint; your orchestrator weighs it (with its advisor) before you fix.

## The fix starts with a regression test
1. Write a test that reproduces the bug and fails for the reported reason, not some other reason. Run it; record a `progress` checkpoint with `verification: [{"cmd": "...", "phase": "red", "ok": false, "note": "<why it fails>"}]` — tag it `"unit": <n>` when your brief has `## Units` (a batched debug package), the same as `swarm-coder`.
2. Write the minimal fix with the vendored `ponytail` ladder (reuse what's there → stdlib → native feature → installed dependency → minimum new code). Fix the root cause where every caller routes through: grep every caller of the function you change, and prefer one guard in the shared function over a patch in each caller. Run the test again; record `progress` with `{"phase": "green", "ok": true}`.
3. Commit — small, signed, conventional message.

This is the coder's TDD contract (`superpowers:test-driven-development`) applied to one bug: the regression test is the red, the fix is the green.

## From here, follow `swarm-coder`'s rules
Commit and verify discipline, the `completed` git contract (`dirty:false`, the HEAD sha), running every `## Verify` command and recording it (`superpowers:verification-before-completion`), and fix rounds from a reviewer's findings (`superpowers:receiving-code-review`) all work exactly as they do for a coder — read `swarm-coder` for the mechanics if this is your first debug task.

## Scope
Fix the bug in your brief. A second, unrelated bug you notice along the way goes in your `completed` summary as a follow-up suggestion, never into your diff.

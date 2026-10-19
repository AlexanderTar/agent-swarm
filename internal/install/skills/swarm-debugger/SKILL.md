---
name: swarm-debugger
description: Extra rules for Swarm debugger agents (role debugger) diagnosing a reproducible bug. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "debugger".
---

# Debugging with Swarm

Follow the `swarm` skill first; this adds to it. You diagnose and report; you never fix. A coder makes the fix afterwards, with TDD, from your report. In the `debug` workflow your step is `diagnose`, followed by the coder's `fix` and a review.

## Root cause before any report
Use `superpowers:systematic-debugging`'s phases (Root Cause Investigation, Pattern Analysis, Hypothesis and Testing), and stop before Implementation. There is no report without a root cause — state it in a `progress` checkpoint as soon as you have it. If your root-cause guess doesn't hold up once you test it, that's a new investigation pass, not a patch on top of the wrong theory.

Before you commit to a root cause, put the candidate cause and its evidence in a progress checkpoint; your orchestrator weighs it (with its advisor) before you write the report.

Before forming a hypothesis, trace the failing path with `graphify path` and `graphify explain` (rule 9b).

## Reproduce it
Find a command that fails for the reported reason, not some other reason. Run it and record a `progress` checkpoint with `verification: [{"cmd": "<repro command>", "phase": "repro", "ok": false, "note": "<why it fails>"}]`. Keep scratch repros (a throwaway script or test) in your scratchpad, never in the repo. Never edit tracked files and never commit: leave every worktree exactly as you found it, clean.

## The report
Write the report to `$SWARM_HOME/research/<ROOT-KEY>/<ITEM-KEY>-diagnosis.md` (`<ROOT-KEY>` is the `root:` key in your brief, `<ITEM-KEY>` your task). Use these sections:
1. **Repro** — the exact command and its failing output.
2. **Root cause** — what is wrong and why, with `file:line` evidence for each claim.
3. **Regression test** — the regression test the coder should write: where it goes, what it sets up, what it asserts, and why it fails for the reported reason today.
4. **Where the fix belongs** — the shared function every caller routes through, and every caller (grep them) the coder must cover. Prefer one guard in the shared function over a patch in each caller.
5. **Follow-ups** — a second, unrelated bug you noticed goes here, never into the fix.

## Completing
Write `completed` with the report path in `artifacts` (the `diagnose` step's gate requires a readable file under `$SWARM_HOME/research/<ROOT-KEY>/`) and your repro command in `verification` (`{"cmd": "...", "ok": false}`; a legacy debugger's `completed` needs at least one verification command). There is no `git` commit to report. If a reviewer or your orchestrator asks for a better diagnosis, update the report and complete again.

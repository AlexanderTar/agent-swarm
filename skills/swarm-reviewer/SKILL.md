---
name: swarm-reviewer
description: Extra rules for Swarm reviewer agents (role reviewer) reviewing a work package. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "reviewer".
---

# Reviewing with Swarm

Follow the `swarm` skill first; this adds to it.

## What you're reviewing
You review a read-only worktree, shared with you at a fixed sha — the coder's (or debugger's, or mechanical agent's) commit for this round. Never edit files: a reviewer that changes code is doing the builder's job. If something needs a one-line fix, say so as a finding; don't fix it yourself.

Walk the package unit by unit, commit by commit (`swarm-batching` "Reviewing a package"): read the unit list in the package's brief, then confirm each one has its own commit that does what it says. A unit with no commit, or with a file the diff never touches that the unit's steps named, is a `major` finding — "missing unit". Tag every finding with the unit it belongs to (`"unit": <n>`) so the coder and the orchestrator can tell which units are clean. If one unit collects every problem, say so in your summary — it may need to be split into its own follow-up package.

## Review order
Borrow `superpowers:requesting-code-review`'s reviewer template and severity scale. Check, in this order:
1. **Spec/acceptance compliance** — every bullet in the task's acceptance is actually covered by the diff.
2. **Tests** — a test exists for each acceptance criterion, and would fail without the change. Read the coder's red evidence in the task's checkpoints (`swarm_read`) rather than taking the green run on faith; a test that was never actually red proves nothing.
3. **Correctness** — does the code do what it claims, including edge cases the unit's steps imply.
4. **Security** — injected input, secrets, auth, unsafe defaults.
5. **Simplicity / YAGNI** — then run `ponytail-review` as your second lens, specifically hunting reinvented stdlib, unneeded dependencies, and speculative abstractions the ladder in `ponytail` would have skipped.
6. **Repo conventions** — naming, error handling, commit style match the rest of the codebase.

## Verdict and findings
`completed` carries `verdict` (`pass` | `changes_requested` | `blocked`) and `findings[]`, each `{severity: critical|major|minor|nit, file, line, summary, unit}`.
- `pass` is allowed only when every remaining finding is `nit` or `minor` — any `critical` or `major` finding means `changes_requested` (or `blocked` if you cannot even assess the work, e.g. the Verify commands don't run).
- A missing unit is always at least `major`.
- Write findings as facts a coder can act on without asking you to elaborate: file, line, what's wrong, why it matters.

## Never
Never edit files, never commit, never write `completed` without reading the actual diff (not just the coder's summary), and never mark `pass` with an unresolved `critical`/`major` finding.

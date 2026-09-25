# Implementer contract (applies to every package dispatch)

- Work ONLY in the worktree path your dispatch names. Use absolute paths; the shell cwd resets each call, so prefix commands with `cd <worktree> &&`.
- Read your brief first. It holds the plan's Global Constraints and your package text. The spec is
  `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md` (in your worktree); read only the sections your dispatch names.
  Exact copy/strings/signatures come from the spec and brief verbatim.
- TDD per unit: write the failing test, run it and capture the RED output, implement minimally, run and capture GREEN.
- One commit per unit, conventional message. Stage explicit paths only (never `git add -A`, never `--amend`).
  End each commit message with a blank line then `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.
- Never delete or weaken an existing test to get green. If an existing test's expectation changes intentionally, the brief names it; otherwise stop and report.
- After any edit under `skills/`, run `make skills-sync` and commit the mirror in the same commit.
- Baseline: on this Mac, `go test ./...` has exactly one pre-existing failure: `internal/httpapi TestBoardServedAtRoot` (web bundle not built). Ignore it; anything else red is yours.
- Before reporting: run the package's full Verify set from the brief and paste the output in the report.
- You do not dispatch subagents (no helpers, no reviewers). Review happens after you report.
- If blocked or the plan/spec is ambiguous in a way that changes behaviour, stop and report NEEDS_CONTEXT/BLOCKED with specifics rather than guessing. Small, clearly-scoped judgment calls: make them and list them in the report.

## Report
Write the full report to the report path in your dispatch: what you built per unit, TDD evidence per unit (RED command+output, GREEN command+output), Verify output, files changed, self-review notes, concerns, judgment calls.
Then reply with ONLY (under 15 lines): Status (DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT), commits (short sha + subject), one-line test summary, concerns, report path.

## Fix rounds
If resumed with review findings: fix, re-run the covering tests, append a "Fix round N" section to the same report (what changed, tests run, command, output), commit (new commits, never amend), and reply with the same short contract.

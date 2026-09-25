# Handoff: self-contained tasks, workflow engine and role skills (PR #20)

Paused on 2026-09-25 at the user's request. This file is the pick-up point.
Plan: `docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md`. Spec:
`docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`.
Session artifacts (ledger, briefs, implementer reports, review findings,
rulings) are copied to `docs/plans/2026-09-25-sdd-handoff/`; `progress.md`
there is the full ledger.

## Where things are

Integration branch: `claude/happy-ptolemy-fke4jx`, worktree
`/Users/alexandertar/GitHub/agent-swarm--self-contained-tasks`. Kept local:
the user asked not to push to PR #20 for now. Local `main` (13 unpushed
commits) was merged in at `23b5368`.

| Package | State | Where |
|---|---|---|
| P1 skill distribution (+ fix: skills refresh never touches the real home) | merged | integration branch |
| P2 vendored skills | merged | integration branch |
| P3 builder/reviewer skills | merged | integration branch |
| P4 support skills + kickoff | merged | integration branch |
| P5 migrations, P6 workflow model (+ resume fix) | merged | integration branch |
| P7 roles, guards, item fields | merged | integration branch |
| P8 checkpoint semantics | merged | integration branch |
| Symlink check (skill link mode per CLI) | merged | integration branch |
| PM muse spawn isolation (user request) | merged | integration branch |
| P9 workflow engine | fix round 1 committed (`b749c63..9704206`); partial Opus re-review says needs fixes: the user directive is still open (claim the fix-round row for the old agent before closing its session and calling `Retry`), the orphaned `active` builder after a failed `Retry` must be fixed, and I3 isn't fixed on the real replay path. Details: `p9-rereview1-partial.md` and the last P9 entry in `progress.md`. Probes: `probes/*.go.txt` (rename to `_test.go` inside `internal/runtime` to run) | `pkg/p9`, worktree `../agent-swarm--p9` |
| PA agy skills root (user request) | merged at `689302e`; round 5 corrected nested symlink cycles and passed scoped review | integration branch |
| P10, P11, P12 | not started | — |

Every merge was verified with `go build ./... && go vet ./... && go test ./...`;
the only failure is the pre-existing `internal/httpapi TestBoardServedAtRoot`
(web bundle not built).

## Resume steps

1. PA: finish `pa-fix4-findings.md` items, run a scoped re-review (round 5 is
   the last; after it, rule on any residuals), merge `pkg/pa`.
2. P9: dispatch fix round 2 from `p9-rereview1-partial.md` (user directive
   gap, orphaned `active` builder, I3 on the replay path, I2's checkpoint
   trigger context, and confirm the `toCloseFailedSelf` removal doesn't leave a
   pane running). Then run a full scoped re-review of `b749c63..HEAD` against
   `p9-fix1-findings.md`, and merge `pkg/p9`.
3. P10 → P11 / P12 per the plan's merge order; then the final whole-branch
   review.
4. After merge, the user runs `swarm install` once so PA's repair moves the
   live agy skills out of `~/.swarm/run/launch/ses_01M36FPV68…`.

## Working rules the user set in this session

- Opus reviewers, Sonnet implementers.
- At most 2-3 subagents running at once.
- Keep everything local; don't push to PR #20.

## Rulings made on the user's behalf (full list with costs in `progress.md`)

- tdd gate: fix rounds need red→green only for units the findings name; scope is
  the step's attempts in the current round (R3 in spec B5 defines "fix round").
- `closeCompletedSiblings`: orchestrator exemption only for legacy tasks.
- `completedCurrent`: workflow branch counts any non-reviewer, non-orchestrator role.
- Muse: operator MCP servers dropped; real `XDG_DATA_HOME` stays shared (plugin
  store integrity); `~/.swarm` and other agents' dirs excluded from the isolated HOME.
- agy: skills root is `~/.gemini/config/skills`; repair only on explicit
  `swarm install` / `swarm migrate`; `copyTree` dereferences links and (round 4)
  skips dangling links and cycles with a log line.

## Things the user should know

- A cmd/swarm test run on 2026-09-24 19:34 overwrote the live skill roots; they
  were restored to `main`'s install state at 19:45 (P1 fix prevents a repeat).
- The live agy skills link was repointed by a probe and restored at 21:05.
- An agy process ran against the real home from the primary checkout at
  2026-09-25 08:33:58 (log `cli-20260925_083358.log`), not from this session.
- Optional cleanup: stale muse registry file
  `~/.local/share/muse/runtime/muse/sessions/01a0d78b-b80a-78d2-a9e1-464013cc0eac.json`.

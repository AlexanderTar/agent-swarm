# Claude remediation follow-up — 2026-09-23 08:56 BST

Session `83e3eeed` progressed past research: spec + 6-task TDD plan committed on
branch `worktree-superpowers-remediation` (worktree `.claude/worktrees/superpowers-remediation`).
User said "Proceed"; implementation pending execution-method choice.

New findings vs the earlier 3-front summary:
- Delivery matrix: codex `setupEnv` carries zero state (worst case); agy fix misses plugin cache; cursor unverified. Fix = per-adapter parity + check the isolated path, not real home.
- Enforcement split: spawned agents gateable (close `POST /api/items` bypass at `checkpoint.go:395` via `ArtifactsFor`); interactive sessions need a Claude Code hook (logged as follow-up, not built).
- Their 6 tasks: codex carry-over, agy plugin cache, parity guard test, cursor verify, completed-gate, SKILL.md path fix.

CONFLICT — needs parent decision: their spec moves SKILL.md spike paths to `~/.swarm/specs|plans/`;
repo CLAUDE.md and our muse spec/plan (Task 9/R2, committed) say `docs/specs/` + `docs/plans/`.
Do not implement R2 until this is resolved. Overlap note: their parity-guard test must
account for muse (no isolated HOME by design).

RESOLVED — 2026-09-23 09:52 BST, by the parent (asked directly in session `83e3eeed`,
which had independently reached the same fork point earlier in the same conversation and
already gotten an explicit answer): `~/.swarm/specs|plans/` wins, not `docs/specs/`/`docs/plans/`.
Rationale given: matches the existing `~/.swarm/worktrees` centralization decision — one
consistent swarm-owned location outside any repo the orchestrator may not own, never
`docs/superpowers/` or `~/.superpowers/`. `worktree-superpowers-remediation` (Task 6,
commit `f082c0a`) already ships `skills/swarm-orchestrator/SKILL.md:45` this way, including
its `internal/install/skills` go:embed mirror, and is being merged to `main` now.
Task 9/R2: skip the `docs/specs/`+`docs/plans/` rewrite as planned — the target state it
should verify (not re-implement) is `~/.swarm/specs/<YYYY-MM-DD>-<slug>.md` /
`~/.swarm/plans/<YYYY-MM-DD>-<slug>.md`, "never write them into a repo," on both the skill
file and its embed mirror. If `main` doesn't already have that wording by the time R2 runs,
something regressed — grep for `~/.superpowers` or `docs/specs/<.*SPIKE-KEY` in
`skills/swarm-orchestrator/SKILL.md` first rather than assuming this note is stale.
R1/R3 overlap: R3 (`internal/runtime/text.go`'s kickoff MUST-mandate) is untouched by and
complementary to `worktree-superpowers-remediation`'s Task 5 (a hard DB-level gate on
`completed` requiring a registered plan/debug_report for epic/bug roots) — no conflict, both
land. R1's per-kind delivery probes for codex/agy should find them already fixed (Tasks 1-2)
once merged; only muse itself is new territory for R1.

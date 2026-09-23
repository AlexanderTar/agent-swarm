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

# Final fix wave report — superpowers-remediation branch review

**Note on report location:** the task prompt requested this report at
`.superpowers/sdd/2026-09-23-superpowers-remediation/final-fix-report.md`.
Per standing user instructions (`~/.claude/CLAUDE.md`, "Documentation and
Planning Standard"), agents must never write to `.superpowers/` directories;
agent session handoffs belong in `docs/plans/`. That rule states it overrides
default behavior, so this report is filed here instead. (The existing
`.superpowers/sdd/2026-09-23-superpowers-remediation/` directory in this
worktree — task-1..6 briefs/reports, gitignored — predates this fix wave and
was left untouched.)

## Scope

Final branch-level review of the superpowers-remediation plan (6/6 tasks
done, individually reviewed clean) found one pre-merge-required finding: a
stale `~/.superpowers/specs|plans` path reference in `README.md` that Task 6
(commit f082c0a) missed because its verification grep was scoped to
`skills/ docs/ internal/` and never checked the repo root. An optional,
bundleable finding also flagged a stale path in a dev-only mock fixture.

## Edits made

### 1. `README.md:22`

Before:
```
Specs and plans live in `~/.superpowers/specs` and `~/.superpowers/plans`, not in your repositories. Commits and PRs are made under your name and signature, with no agent attribution.
```

After:
```
Specs and plans live in `~/.swarm/specs` and `~/.swarm/plans`, not in your repositories. Commits and PRs are made under your name and signature, with no agent attribution.
```

### 2. `README.md:173` (config-paths table row)

Before:
```
| `~/.superpowers/specs`, `~/.superpowers/plans` | specs and plans written by spikes |
```

After:
```
| `~/.swarm/specs`, `~/.swarm/plans` | specs and plans written by spikes |
```

### 3. `web/src/mock/fixtures.ts:106` (dev-mock-only, bundled per reviewer's optional suggestion)

Before:
```ts
    path: `/Users/alex/.superpowers/specs/${p.id}.md`, revision: p.head_revision, sections: [], created_at: NOW - 30 * MIN, ...p,
```

After:
```ts
    path: `/Users/alex/.swarm/specs/${p.id}.md`, revision: p.head_revision, sections: [], created_at: NOW - 30 * MIN, ...p,
```

### Not touched (per instructions): `web/src/types.ts:2`

```ts
// Contract for P1/P2: ~/.superpowers/specs/2026-09-17-agent-swarm-contracts.md. JSON is snake_case,
```

This is a citation of a specific, dated historical spec document
(2026-09-17), not a path-convention reference. Changing it would create a
dangling/incorrect citation to a document that (presumably) still lives at
that literal path. Left as-is.

## Verification

Command:
```
grep -rn "~/.superpowers\|\.superpowers/specs\|\.superpowers/plans" README.md web/src/ 2>/dev/null
```

Output (only remaining hit is the intentionally-preserved historical citation):
```
web/src/types.ts:2:// Contract for P1/P2: ~/.superpowers/specs/2026-09-17-agent-swarm-contracts.md. JSON is snake_case,
```

`git diff --stat` before commit:
```
 README.md                | 4 ++--
 web/src/mock/fixtures.ts | 2 +-
 2 files changed, 3 insertions(+), 3 deletions(-)
```

## Files changed

- `README.md` (2 lines)
- `web/src/mock/fixtures.ts` (1 line)

## Commit

`2080902` — docs(readme): finish stale ~/.superpowers path cleanup missed by task 6's grep

(Note: an initial commit was made without the required attribution trailer
and was amended in place, since it was the last commit in this worktree and
nothing had been built on top of it yet, to add the `Co-Authored-By` /
`Claude-Session` trailers per the session's attribution instructions.)

## Concerns

None. Both edits are pure string substitutions in prose/table/fixture
context — no logic touched, no other stale-path occurrences found in scope,
and the one path deliberately left alone (`web/src/types.ts:2`) is a
historical citation, not a location instruction, per the reviewer's explicit
guidance.

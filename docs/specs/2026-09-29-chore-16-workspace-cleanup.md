# CHORE-16 — Auto-clean agent work dirs and reclaim worktrees reliably

## Context

- `~/.swarm/work/<agent-name>` is created on every launch (`internal/runtime/agents.go:1409`, `internal/runtime/limits.go:242`) and never removed. As of 2026-09-29: 750 dirs, 369 non-empty (agent scratch: logs, review notes, `.claude/`, `.mcp.json`), 25 with no agent row at all.
- `~/.swarm/worktrees` holds 107 trees (35 GB). DB: 57 `active`, 43 `retained/unmerged`, 3 `retained/dirty`.
- Root causes for stranded worktrees, measured:
  1. `worktree.remove()` treats a tree as merged only if `HEAD` is an ancestor of `wt.BaseRef` (often another task branch or a bare SHA, never fetched) or `@{u}` equals HEAD (swarm branches rarely have an upstream). Squash-merged PRs break ancestry entirely.
  2. `reclaimGateWhere` blocks on any unreleased reservation of a non-owner agent — including reviewers that are long `finished` (10 live rows today).
  3. Nothing runs `git worktree prune`, and nothing reports tree dirs on disk with no DB row.
- Only repo: `agent-swarm` (worktree `~/.swarm/worktrees/agent-swarm--cleanup-work-dirs`, branch `chore-16/cleanup-work-dirs`).

## Locked decisions

- Work dir removal gate: agent `finished`/`acknowledged`, `finished_at` older than `reclaimGrace` (1h), no session in a live state, path is exactly `<home>/work/<agent.name>`, not a symlink. Retry recreates the dir via `MkdirAll`, so removal is safe.
- Orphan work dirs (no agent row with that name) are removed when their mtime is older than `reclaimGrace`.
- Merged evidence for a worktree (any one suffices, checked after dirty check):
  1. HEAD is ancestor of `wt.BaseRef` (unchanged),
  2. HEAD is ancestor of the repo's remote default branch (`origin/<default>`), fetched at most once per repo per reclaim pass,
  3. HEAD is ancestor of the `head` branch/`merged_sha` of an `item_merges` row with `state='merged'` for the worktree's root item,
  4. For GitHub remotes: HEAD is ancestor of (or equal to) the head OID of a merged PR in `gh pr list --state merged` for that repo (cached per repo per pass; missing OIDs fetched with `git fetch origin <oid>`; gh failure = no evidence, never an error).
  Dirty trees are never removed. Detached trees keep the existing `atDetachedSHA` rule, extended with evidence 2–4.
- A reservation held by a `finished`/`acknowledged` agent with no live session does not block reclaim; reclaim releases it.
- After any removal in a repo, run `git worktree prune` in that repo once per pass.
- Disk dirs under `~/.swarm/worktrees` with no non-removed DB row are logged, never deleted automatically.
- New CLI `swarm cleanup [--dry-run] [--no-grace]` runs the work-dir pass and the worktree reclaim pass once against the DB, printing each path with `removed`/`kept (<reason>)`/`would remove`.
- One-time cleanup of existing dangling dirs = run `swarm cleanup --dry-run --no-grace`, show the user, then run without `--dry-run` after user confirmation.

## DB models

No DB changes: reuses `worktrees`, `worktree_reservations`, `agents`, `sessions`, `item_merges`.

## Model / API types

```go
// internal/worktree
type MergeEvidence interface {
    // Merged reports extra evidence that wt's HEAD (sha) is merged. Never errors: unknown = false.
    Merged(ctx context.Context, wt Worktree, repoPath, head string) bool
}
// Service gains: Evidence MergeEvidence (nil = base rules only); PruneRepo(ctx, repoID) error.

// internal/runtime
func (s *Store) ReclaimWorkDirs(ctx context.Context, opt CleanupOptions) ([]CleanupResult, error)
func (s *Store) ReclaimWorktreesWith(ctx context.Context, opt CleanupOptions) ([]CleanupResult, error)
type CleanupOptions struct{ DryRun, NoGrace bool }
type CleanupResult struct{ Path, Action, Reason string } // Action: removed|kept|would_remove
```

`ReclaimWorktrees(ctx)` stays as the loop entry and calls `ReclaimWorktreesWith(ctx, CleanupOptions{})`. The work-dir pass runs in the same 10-minute loop.

## Screens

No UI changes: CLI text output only.

```
$ swarm cleanup --dry-run
work      would_remove  ~/.swarm/work/foo-coder
worktree  would_remove  ~/.swarm/worktrees/agent-swarm--x
worktree  kept (dirty)  ~/.swarm/worktrees/endurio-chat--go-cutover
worktree  untracked     ~/.swarm/worktrees/stray-dir
summary: 612 work dirs, 31 worktrees would be removed; 18 kept; 1 untracked
```

## All user-facing copy

- Log: `workdir: removed <path>`, `workdir: keeping <path> (<reason>)`, `workdir: pass: %d removed, %d kept`.
- Log: `worktree: untracked dir <path> (no swarm record)`.
- CLI summary line as in the sketch.

## File list

- Change: `internal/worktree/worktree.go`, `internal/runtime/reconcile.go`, `cmd/swarm/daemon.go`, `cmd/swarm/runtime_cmds.go` (or new `cmd/swarm/cleanup.go`).
- New: `internal/runtime/workdirs.go`, `internal/worktree/evidence.go` (+ tests).
- Skills: `skills/swarm-orchestrator/SKILL.md`, `skills/swarm/SKILL.md`, then `make skills-sync`.

## Verification

1. `go vet ./...`
2. `go test ./internal/worktree/... ./internal/runtime/... ./cmd/swarm/...`
3. `go test ./...`
4. `make skills-sync` then a clean diff.
- Scenarios (tests): squash-merged branch via merged PR evidence is removed; branch based on a task branch but contained in `origin/main` is removed; dirty tree is kept; finished reviewer reservation no longer blocks; live-session agent's work dir kept; symlinked work dir kept; orphan work dir older than grace removed; gh unavailable → base rules only; `--dry-run` deletes nothing.

## Explicitly out of scope

- Deleting branches (local or remote).
- Auto-deleting untracked worktree dirs.
- Removing `retained/unmerged` trees with no merged evidence (listed for the user instead).
- Restarting the running daemon.

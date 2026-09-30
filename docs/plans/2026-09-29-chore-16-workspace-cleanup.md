# CHORE-16 plan — workspace cleanup

Spec: `docs/specs/2026-09-29-chore-16-workspace-cleanup.md`. Repo: agent-swarm, branch `chore-16/cleanup-work-dirs`.

## Package A — Worktree reclaim robustness (tdd-reviewed, coder)

1. Default-branch evidence: failing test in `internal/worktree` — tree branched off a task-branch `base_ref`, HEAD contained in `origin/main` of a fixture remote → `remove()` returns `removed`. Implement per-pass fetch cache + ancestry vs `origin/<default>`. Commit.
2. Merged-PR evidence: failing tests — (a) `item_merges` merged row for root whose head contains HEAD → removed; (b) fake gh runner returns merged PR head OID containing HEAD (squash-merged into main) → removed; gh error → retained `unmerged`. Implement `MergeEvidence` in runtime, wired into `worktree.Service`. Commit.
3. Finished-reservation gate: failing test in `internal/runtime` — worktree shared with a finished reviewer (no live session) is reclaimed and the reservation released. Change `reclaimGateWhere`. Commit.
4. Prune + untracked report: failing test — after a removal, `git worktree prune` runs once per repo; a dir under `<home>/worktrees` with no DB row is logged `untracked`, not deleted. Commit.

Verify: `go vet ./...`, `go test ./internal/worktree/... ./internal/runtime/...`.

## Package B — Work dir cleanup + `swarm cleanup` CLI (tdd-reviewed, coder; after A)

1. `ReclaimWorkDirs`: failing tests — finished agent past grace removed; live session kept; within grace kept; symlink kept; path outside `<home>/work` never touched. Implement in `internal/runtime/workdirs.go`; call from the 10-minute reclaim loop. Commit.
2. Orphan dirs: failing test — dir with no agent row older than grace removed, fresh one kept. Commit.
3. `swarm cleanup [--dry-run] [--no-grace]`: failing CLI test — dry-run lists `would_remove` and deletes nothing; real run removes. Implement using `CleanupOptions`. Commit.

Verify: `go vet ./...`, `go test ./internal/runtime/... ./cmd/swarm/...`, `go test ./...`.

## Package C — Worktree prudence in skills (mechanical; parallel with A)

1. `skills/swarm-orchestrator/SKILL.md`: remove each worktree you created with `swarm_worktree remove` once its branch is merged into the integration tree and again after `finishing`; reuse an existing review worktree at the same sha instead of creating another; release reviewer shares when the review ends. `skills/swarm/SKILL.md`: `~/.swarm/work/<name>` is deleted after the agent finishes — keep anything durable in specs/plans/kb/artifacts. Run `make skills-sync`. Commit.

Verify: `make skills-sync && git diff --exit-code`.

## Integration

Merge A, C, then B into `chore-16/cleanup-work-dirs`; run `go vet ./...` and `go test ./...`; final reviewer. Then build the binary, run `swarm cleanup --dry-run --no-grace`, show the user the list, and run the real cleanup only after user confirmation.

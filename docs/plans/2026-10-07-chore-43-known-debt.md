# CHORE-43 known debt

CHORE-43 fixed BUG-34, 35, 37–43, 45–48 and made top-level orchestrators
always top-level. Spec: `docs/specs/2026-10-07-chore-43-open-bugs.md`.

## Open items

- **ended_without_checkpoint covers sibling closes only.** The relay is sent
  from `closeCompletedSiblings` (`internal/runtime/checkpoint.go`), which was
  BUG-40's real cause. The pane-exit and provider-stop path in
  `internal/runtime/reconcile.go` (around the finished/crashed handling) was
  not traced, so check whether it already relays to the parent. The relay also
  goes only to the direct parent, not `nearestLiveAncestor`.
- **Cross-root top-level spawn ignores worktrees.** In the swarm_spawn
  cross-root branch (`internal/mcpserver/orchestrator.go`), `in.Worktrees` is
  dropped silently. Refuse it or say so in the result.
- **Partial node_modules on clone failure.** `internal/worktree/deps.go` leaves
  a partially copied dir if `cp` fails midway. It is ignored and logged, but
  removing it would be cleaner.
- **Worktree path refs match exactly.** `internal/runtime/worktree_ref.go` does
  not `filepath.Clean` or resolve symlinks, so a trailing slash or a
  `/tmp`-vs-`/private/tmp` path won't resolve.
- **Review-tree clones run synchronously.** `Review` can block for up to 120 s
  while cloning large `node_modules`.

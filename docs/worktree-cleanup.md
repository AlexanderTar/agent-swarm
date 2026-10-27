# Worktree reclaim

Finished roots used to keep worktrees forever. A squash merge is not an ancestor of the default branch, Sweep selected only `state='active'`, and beta-build edits of tracked files looked like dirty work. `swarm cleanup --dry-run` showed them as `unmerged` or `dirty`, and the next pass left them there.

## What counts as merged

Any one of these is enough, after the dirty check (build churn below is the exception that is removed while dirty):

1. `HEAD` is an ancestor of the worktree's `base_ref`.
2. `HEAD` is an ancestor of `refs/remotes/origin/<default>` after `git fetch origin` (once per repo per reclaim pass).
3. The branch's content is already on that remote default branch. `git merge-tree --write-tree <ref> HEAD` exits 0 and the tree id it prints equals `git rev-parse '<ref>^{tree}'`. This is the squash case. It needs git 2.38 or newer (`merge-tree --write-tree`).
4. An `item_merges` row with `state='merged'` for this root and repo, a live sibling worktree branch of the same root and repo, or a merged GitHub PR head contains `HEAD` (`internal/runtime/evidence.go`).

A conflict, a merge-tree whose tree differs from the default branch, or any error — including `unknown option` on older git — is not evidence. A conflicting merge-tree means unmerged, never merged. Do not read a non-zero exit, or a different tree id, as "already landed".

From the worktree, with `<default>` the repo's default branch (usually `main`):

```sh
git merge-tree --write-tree "refs/remotes/origin/<default>" HEAD
git rev-parse "refs/remotes/origin/<default>^{tree}"
```

Exit 0 and the same id: merged. Exit non-zero: unmerged. Exit 0 and a different id: unmerged, the branch still has content the default branch does not.

## Build churn

Discard the tree (force-remove; the branch is kept) when all of these hold:

- the owner agent's state is `finished` or `acknowledged`
- `git rev-list --count origin/<default>..HEAD` is 0 (no commit of the tree's own). When `origin/<default>` does not exist, compare to the local default branch instead. This count stays above 0 after a squash: the branch commit is not an ancestor, even when the merge-tree check says the content is already on the default branch. Use the count for build churn only, not as merge evidence
- `git status --porcelain` lists only modifications of tracked files. Each line's first two columns are `M` or a space, and at least one is `M` (`M `, ` M`, or `MM`)

Keep it retained when porcelain has an untracked path (`??`), a deletion (`D` in either column), or `git rev-list --count origin/<default>..HEAD` is not 0. A live owner (`queued` or `active`) is never build churn.

## Retained rows are re-checked

The sweep of a done or cancelled root, and the hourly reclaim candidate query (`reclaimGateWhere` in `internal/runtime/reconcile.go`), both select `state IN ('active', 'retained')`. A tree kept as `unmerged` or `dirty` is judged again and removed once the evidence above says it is safe. A tree that still has its own commits stays retained.

## Seeing why a tree is kept

```sh
swarm cleanup --dry-run
```

A kept worktree is a line `worktree kept <path> (<reason>)`. `--dry-run` writes nothing. `dirty` is uncommitted work that is not build churn. `unmerged` is commits that none of the evidence above covers. Run it with `--home` pointing at a scratch directory when you are not inspecting the live home (docs/isolated-runs.md). Never pass `--live`.

## Discard by hand

```sh
swarm cleanup --discard <path-or-id>
```

Removes that worktree when its owner is not live and no live agent holds a reservation. The root item can still be in progress. The branch is kept. A directory under the home's `worktrees/` with no swarm row is removed with `git worktree remove --force` in the repo whose `git worktree list --porcelain` names it.

## Tests

```sh
go test ./internal/worktree/ ./cmd/swarm/ -count=1 -run 'TestContentAlreadyOnTheRemoteDefaultCountsAsMerged|TestUniqueContentIsNotMerged|TestConflictingContentIsNotMerged|TestMergeTreeFailureIsNotEvidence|TestSweepRemovesARetainedTreeOfADoneRootOnceMerged|TestSweepKeepsARetainedTreeThatIsStillUnmerged|TestReclaimOneDiscardsTrackedModificationsWithNoOwnCommits|TestReclaimOneKeepsUntrackedFilesAsDirty|TestReclaimOneKeepsADeletionAsDirty|TestReclaimOneKeepsOwnCommitsEvenWhenOnlyTrackedFilesChanged|TestDiscardAllowsAnInProgressRootWhenNobodyLiveHoldsIt|TestDiscardRefusesALiveOwnerEvenWhenTheRootIsInProgress|TestDiscardRemovesAPathWithNoSwarmRow|TestCleanupDiscardRefusesALiveOwnerThenDiscardsWhenTheOwnerIsFinished'
```

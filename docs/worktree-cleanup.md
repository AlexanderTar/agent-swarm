# Worktree reclaim

Finished roots used to keep worktrees forever. A squash merge is not an ancestor of the default branch, a retained row was never judged again, and beta-build edits of tracked files looked like dirty work. `swarm cleanup --dry-run` showed them as `unmerged` or `dirty`. The reclaim pass judges them again; Sweep stays on `state='active'` so the 5-second tick does not re-fetch a tree that is still unmerged.

## What counts as merged

Any one of these is enough, after the dirty check (build churn below is the exception that is removed while dirty):

1. `HEAD` is an ancestor of the worktree's `base_ref`.
2. `HEAD` is an ancestor of `refs/remotes/origin/<default>` after `git fetch origin` (once per repo per reclaim pass).
3. The branch's content is already on that remote default branch. `git merge-tree --write-tree <ref> HEAD` exits 0 and the tree id it prints equals `git rev-parse '<ref>^{tree}'`. This is the squash case. It needs git 2.38 or newer (`merge-tree --write-tree`). The command writes that result tree into the repo's object store, so a pass that runs it is not strictly free of git writes.
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
- the root item's status is `done` or `cancelled`. An in-progress root keeps the tree: Retry can flip that finished owner back to active, and the edits are still its work
- `git rev-list --count origin/<default>..HEAD` is 0 (no commit of the tree's own). When `origin/<default>` does not exist, compare to the local default branch instead. This count stays above 0 after a squash: the branch commit is not an ancestor, even when the merge-tree check says the content is already on the default branch. Use the count for build churn only, not as merge evidence
- `git status --porcelain` lists only modifications of tracked files. Each line's first two columns are `M` or a space, and at least one is `M` (`M `, ` M`, or `MM`)

Keep it retained when the root is still in progress, when porcelain has an untracked path (`??`), a deletion (`D` in either column), or `git rev-list --count origin/<default>..HEAD` is not 0. A live owner (`queued` or `active`) is never build churn.

## Retained rows are re-checked

`Sweep` (`internal/worktree/worktree.go`) selects only `state = 'active'`. It runs from `Reconcile` every 5 seconds (`cmd/swarm/daemon.go`), outside `BeginPass`, so it does not re-fetch a tree that is already retained. The reclaim pass (`ReclaimWorktreesLoop`, every 10 minutes) selects `state IN ('active', 'retained')` through `reclaimGateWhere` in `internal/runtime/reconcile.go`, inside one `BeginPass`, so each repo is fetched once per pass. A tree kept as `unmerged` or `dirty` is judged again there and removed once the evidence above says it is safe. A tree that still has its own commits stays retained.

## Seeing why a tree is kept

```sh
swarm cleanup --dry-run
```

A kept worktree is a line `worktree kept <path> (<reason>)`. `--dry-run` deletes nothing and writes no worktree rows. It can still run `git merge-tree --write-tree`, which stores a tree object in the repo, so the pass is not strictly free of git writes. `dirty` is uncommitted work that is not build churn. `not_a_worktree` is a folder that lost its `.git` and holds files other than watchman cookies. `unmerged` is commits that none of the evidence above covers. Run it with `--home` pointing at a scratch directory when you are not inspecting the live home (docs/isolated-runs.md). Never pass `--live`.

## Discard by hand

```sh
swarm cleanup --discard <path-or-id>
```

Removes that worktree when its owner is not live and no live agent holds a reservation. The root item can still be in progress. An owner is live when `agents.state` is `queued` or `active`, or when that agent has a session in `spawning`, `running`, `pause_requested`, `quiescing`, or `stopping` — the same session list as `reclaimGateWhere`. A finished agent whose session is still quiescing is refused. The branch is kept.

The path is `filepath.Clean`'d before the row lookup, so a trailing slash, `//`, or `/./` still hits the row and its gates. A spelling `Clean` does not fold onto the stored path, such as a symlink, is refused when any non-removed row resolves to the same directory: pass the recorded path or the id. A directory under the home's `worktrees/` with no swarm row is removed with `git worktree remove --force` in the repo whose `git worktree list --porcelain` names it.

## Watchman cookies and half-deleted trees

Cause (BUG-132): watchman, when it watches a worktree, keeps writing `.watchman-cookie-<host>-<pid>-<n>` files into it. `git worktree remove` deletes `.git` and the tracked files, then fails with `failed to delete ...: Directory not empty` when a cookie lands mid-delete. The folder is left with no `.git`, so every later pass ran `git status` in a non-git folder, read the error as dirty, and kept the row as `dirty` forever.

What `remove` does now (`internal/worktree/worktree.go`):

- Safety checks run first (§12.1 via `keepReason`: stray, discardable, `DirtyStrict`, merged). Only then the bulk delete, which is Go-side: `watchman --no-spawn watch-del <path>` (`--no-spawn` so a remove never starts a watchman server; errors and a missing watchman are ignored), delete `.watchman-cookie-*`, `os.RemoveAll`, then `git worktree prune` in the repo on every delete, including inside a reclaim pass (whose loop additionally prunes once per repo, as a second sweep). Prune is one cheap admin-entry drop, not a bulk delete, so the 30 s execx cap below cannot strand it; skipping it while a pass was open left `git worktree list` naming the path and a later add on the same path or branch failed with "already registered". No `git worktree remove` subprocess runs, so the cap cannot SIGKILL the delete. A cookie landing mid-delete is cleared and the delete retried once. Cookies are never dirt: `DirtyStrict`, the build-churn check, and the interrupted-delete check ignore `??` lines for them.
- A row stranded `retained/remove_failed` by an older SIGKILLed `git worktree remove` reads as dirty (tracked files show as `D`), but the deletions ARE the delete: when porcelain lists only tracked deletions (` D` / `D ` lines, plus watchman cookies) and the branch is otherwise merged, the next remove retries the delete instead of keeping the tree. Any modification, add, rename, untracked path, or unmerged branch still retains: real work is never deleted by a retry.
- When the kill landed after the `.git` file was already deleted, git can no longer run inside the tree, so the row reads as `not_a_worktree`. A `remove_failed` row under the worktrees directory whose branch is merged judged from the repo side (`merge-base --is-ancestor <branch> <base_ref>` in the repo, or merged evidence) retries the delete too; every delete prunes, so a missing registration never blocks the retry. An unmerged branch (including a squash-merge with no evidence row) still retains, as does dirt beyond bare deletions when `.git` is still present, as does a folder outside the worktrees directory — look at the folder, then `swarm cleanup --discard <path-or-id>`.
- A folder with no `.git`, or whose `.git` file points at a worktree that `git worktree list --porcelain` no longer names, is a stray, not a worktree. Cookies only (plus a dangling `.git` file, or empty): `os.RemoveAll`, `git worktree prune`, row `removed`. Anything else: row `retained` with reason `not_a_worktree`, folder untouched. The retained row and `swarm cleanup` show the reason (`worktree kept <path> (not_a_worktree)`).
- The reclaim pass re-judges retained rows, so an old stuck `dirty` row with a cookie-only folder is cleared on the next pass without manual steps.

What to do with a `not_a_worktree` row: look at the folder, then `swarm cleanup --discard <path-or-id>`, which deletes the folder and prunes the repo (no `git worktree remove` on a non-git folder).

Never `rm -rf` a worktree by hand while watchman runs: it can recreate the folder with cookies. Never read a `git status` failure in a folder with no `.git` as work to keep; check `.git` first.

Reproduce: `go test ./internal/worktree -run 'WatchmanCookies|NonGit|NotAWorktree'` and `go test ./internal/runtime -run StuckDirtyRow`.

## Bulk deletes and the 30 s execx cap

Cause (BUG-137): `worktree.Service.Run` is `execx.Run` (`cmd/swarm/daemon.go`), which SIGKILLs any command at 30 s when the context carries no deadline (`internal/execx/execx.go`). MCP and reconcile contexts carry none. `git worktree remove [--force]` on a tree holding multi-GB ignored dirs (node_modules, ios/Pods, Gradle output) outlasts the cap and is killed mid-delete; `daemon.err.log` shows `worktree: remove <path> failed: git: signal: killed`. The tree is left half-deleted, the next pass reads the deletions as dirty, and the row is retained forever.

Rules:

- Never run a bulk delete (or a build) through `execx.Run`. Delete in Go (`os.RemoveAll`, then `git worktree prune`, which only drops the repo's admin entry) or use `execx.RunFor` with an explicit timeout (`internal/execx/timeout.go`; adapters already use `RunFor(240 * time.Second)`).
- `git: signal: killed` in `daemon.err.log` reads as the 30 s cap firing, not a git bug: find which call ran under `execx.Run` with no deadline and move it off that runner.
- Every `git worktree remove --force` call site (remove, undoAdd, Discard, discardUntracked in `internal/worktree/worktree.go`) shares the Go-side helper; a new call site must too.

Reproduce: `go test ./internal/worktree -run 'WithoutWorktreeRemoveSubprocess|InterruptedDelete|RemoveFailed|PrunesWhilePassOpen|UndoAddDeletes'`.

## Headless Chrome and `code_sign_clone` leaks

Chrome (and every Chromium app: Comet, Edge, ...) copies its app bundle into `$(getconf DARWIN_USER_DIR)/../X/<bundle id>.code_sign_clone/code_sign_clone.<id>` at every launch and deletes the copy only on a clean quit. UI reviewer and designer agents start headless Chrome (`--headless=new --remote-debugging-port=... --user-data-dir=<their scratchpad or work dir>`) and used to end without quitting it, so each left a running Chrome and a clone of ~1.3k inodes. 264 clones (~350k inodes) and a 3.7-day orphan were found on 2026-10-28.

Rules:
- An agent that starts Chrome keeps `--user-data-dir` inside its own scratchpad or work dir and closes it before its `completed` or `handoff` checkpoint: CDP `Browser.close`, or SIGTERM and wait for exit. Never SIGKILL first: a killed Chrome never deletes its clone. The `swarm-ui-reviewer` and `swarm-designer` skills carry this rule; any new skill that tells agents to start a browser must too.
- Reclaiming a work dir or scratchpad (`ReclaimWorkDirs`, `ReclaimScratchpads`) first terminates every process whose command line has `--user-data-dir=` equal to or inside that dir (SIGTERM, up to 3 s wait, then SIGKILL). It matches on the path boundary and only inside the dir being removed, so the user's own browser profile is never touched. Each terminated process is a `terminated` line (`workdir terminated <dir> (chrome pid N)`). Dry runs and kept dirs signal nothing.
- `ReclaimChromeClones` (daemon reclaim loop and `swarm cleanup`) removes `<root>/*.code_sign_clone/code_sign_clone.*` dirs that are older than the one-hour grace (any age with `--no-grace`) and held by nothing: no process has a file open under the clone (`lsof -w -n -Fn`) and no process command line runs from it. If `lsof` fails with no output nothing is removed; its routine non-zero exit with partial output (processes it may not read) is still used, and the command-line check keeps any clone a running Chrome was started from. Output lines are `chrome-clone removed|would_remove|kept <path> (reason)`.
- Tests and scratch runs must not sweep the developer's real clones: `Store.CloneRoot` (env `SWARM_CHROME_CLONE_ROOT` for `swarm cleanup`) points the sweep at a temp dir, and `Store.Procs` / `Store.Signal` / `Store.OpenPaths` inject the process table, signals and open files.

Detect a leak by hand: `ls "$(getconf DARWIN_USER_DIR)/../X"/*.code_sign_clone | wc -l` and `ps -axo pid,etime,command | grep -- --user-data-dir`. Fix it with `swarm cleanup --dry-run` then `swarm cleanup`. Guards: `go test ./internal/runtime/ -run 'Chrome'`, `go test ./internal/install/ -run TestBrowserSkillsCarryChromeCloseRule`.

## Tests

```sh
go test ./internal/worktree/ ./internal/runtime/ ./cmd/swarm/ -count=1 -run 'TestContentAlreadyOnTheRemoteDefaultCountsAsMerged|TestUniqueContentIsNotMerged|TestConflictingContentIsNotMerged|TestMergeTreeFailureIsNotEvidence|TestReclaimOneRemovesARetainedTreeOnceTheBranchIsMerged|TestReclaimWorktreesRemovesARetainedTreeAfterASquashLands|TestSweepKeepsARetainedTreeThatIsStillUnmerged|TestSecondSweepOfAStillUnmergedRetainedTreeDoesNoFetch|TestReclaimOneDiscardsTrackedModificationsWithNoOwnCommits|TestReclaimOneKeepsTrackedEditsWhileTheRootIsInProgress|TestReclaimOneKeepsUntrackedFilesAsDirty|TestReclaimOneKeepsADeletionAsDirty|TestReclaimOneKeepsOwnCommitsEvenWhenOnlyTrackedFilesChanged|TestDiscardAllowsAnInProgressRootWhenNobodyLiveHoldsIt|TestDiscardRefusesALiveOwnerEvenWhenTheRootIsInProgress|TestDiscardRefusesATrailingSlashPathOfALiveOwner|TestDiscardRefusesASymlinkedSpellingOfALiveOwnerTree|TestDiscardRefusesAFinishedOwnerWhoseSessionIsStillQuiescing|TestDiscardRemovesAPathWithNoSwarmRow|TestCleanupDiscardRefusesALiveOwnerThenDiscardsWhenTheOwnerIsFinished'
```

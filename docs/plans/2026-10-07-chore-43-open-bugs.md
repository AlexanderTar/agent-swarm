# CHORE-43 plan: open agent-swarm bugs

Spec: `docs/specs/2026-10-07-chore-43-open-bugs.md`. Each package is one Swarm
task run by one coder (or debugger) in its own worktree, units in strict TDD
order (failing test → watch it fail → minimal fix → watch it pass → commit).

Order: A, C, D start in parallel. B starts after A merges (both touch the
swarm_spawn handler). Merge order into `chore-43-swarm-bugs`: A, C, D, B.

## A — Cross-root orchestrator spawn (BUG-46/47/48), tdd-reviewed

1. `swarm_spawn` orchestrator on another root's root item → top-level start.
   Test `TestSpawnOrchestratorOnOtherRootStartsTopLevel` in
   `internal/mcpserver/orchestrator_test.go`: agent row has NULL parent, result
   `top_level: true`, brief header `parent: none`. Implement in the swarm_spawn
   handler: compare `it.RootID` with caller `a.RootItemID`; if root item, call
   `s.RT.StartOrchestrator`. Update `skills/swarm-orchestrator/SKILL.md`. Commit.
2. Every other cross-root spawn refused with the spec copy.
   Tests for story-in-other-root (orchestrator) and task-in-other-root (reviewer). Commit.
3. `detachCrossRootParents` reconcile step. Test in `internal/runtime/reconcile_test.go`
   seeds a child agent whose parent is in another root: after one tick parent is NULL,
   brief rewritten, one `assignment_update` queued, one `agent.detached` event; second
   tick is a no-op. Commit.

Verify: `go test ./internal/mcpserver/... ./internal/runtime/...`, `make skills-sync && git diff --exit-code`.

## B — Worktree refs and review deps (BUG-34/39/42/45), tdd-reviewed

1. `ResolveWorktreeRef` (id or path) + error copy; `BriefWorktrees` errors on unknown. Tests. Commit.
2. Use the resolver in `swarm_spawn`, `swarm_workflow start`, `swarm_worktree share/release/remove`. Tests per tool with a path ref. Commit.
3. `Review` clones `node_modules` per spec decision 4 (`internal/worktree/deps.go`). Tests: lockfile match → copied; mismatch → not; tree not dirty; `remove` still succeeds. Commit.

Verify: `go test ./internal/worktree/... ./internal/runtime/... ./internal/mcpserver/...`.

## C — Session lifecycle (BUG-35/37/40), debug

1. Launch dir GC replacing `reclaimOldCodexLaunchHomes`. Tests: terminal >24 h removed, terminal <24 h kept, live kept, orphan >7 d removed. Commit.
2. Root-cause BUG-37 (4 wakes for one blocked session); `WakeOnQuotaReset` skips sessions with nothing pending. Reproducing test first. Commit.
3. Root-cause BUG-40 (researcher sessions finished mid-task); add `ended_without_checkpoint` relay. Reproducing test first. Commit.

Verify: `go test ./internal/runtime/...`.

## D — Workflow gates and coder guidance (BUG-41/43/38), tdd-reviewed

1. Fix-round tdd gate per spec decision 8. Tests: findings on unit 2 only → evidence for unit 2 suffices; findings without unit → one pair suffices; round 1 unchanged. Commit.
2. Progress checkpoint `next` on a workflow run step + `skills/swarm-coder/SKILL.md`. Test on the response. Commit.
3. BUG-38 regression test: chore integrated with `git: []` accepted. Commit.

Verify: `go test ./internal/runtime/... ./internal/workflow/...`, `make skills-sync && git diff --exit-code`.

## Integration

`make skills-sync && git diff --exit-code`, `go vet ./...`, `go test ./...`; final reviewer on the integrated SHA.

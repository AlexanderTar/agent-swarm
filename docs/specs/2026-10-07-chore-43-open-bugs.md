# CHORE-43: Fix open agent-swarm bugs (BUG-34–48) and cross-root orchestrator parenting

## Context

The user asked to address every open Swarm bug in BUG-34..48 and to detach the live
EPIC-20 orchestrator from its EPIC-7 parent, making sure a top-level item's
orchestrator is always top-level no matter who spawned it.

Board state (2026-10-07):

| Bug | State | Handling |
|---|---|---|
| BUG-36 | Done (resolved by CHORE-33) | none |
| BUG-44 | endurio-chat, not Swarm | skipped by user decision |
| BUG-38 | already fixed by 72b893e (CHORE-31, 2026-10-03 22:02); filed against a stale daemon | regression test only (package D) |
| BUG-42, BUG-45 | same defect | one fix (package B) |
| BUG-34, 35, 37, 39, 40, 41, 43, 46, 47, 48 | Draft | packages A–D |

EPIC-20 detach was done directly on 2026-10-07 (backup taken first):
`UPDATE agents SET parent_agent_id=NULL` for `apple-watch-provider-orchestrator`
and its brief header rewritten to `parent: none`. `swarm_read` now shows
`parent: null`. Package A makes this self-healing and prevents recurrence.

Repo: agent-swarm only. Integration worktree `agent-swarm--chore-43-swarm-bugs`.

Collision warning: A owns `internal/mcpserver/orchestrator.go` (swarm_spawn role
branch), `internal/runtime/agents.go`, a new reconcile function. B owns a new
resolver file `internal/runtime/worktree_ref.go`, `internal/worktree/*`,
`BriefWorktrees`. B's edit to the swarm_spawn handler is limited to the
worktree-ref lines. C owns `internal/runtime/wake.go`, `reclaim*` in
`reconcile.go`, session-end paths. D owns the tdd gate, checkpoint response
text, `skills/swarm-coder`. A and D both edit `skills/`; different files.

## Locked decisions

1. **Top-level orchestrators are top-level (BUG-46/47/48, user decision).**
   `swarm_spawn {role: orchestrator}` on an item whose root differs from the
   caller's root:
   - item is a top-level root (epic/bug/chore/spike) → start it through the same
     path as a user start (`StartOrchestrator`), with no parent. Result adds
     `"top_level": true`. The caller gets no relays from it.
   - item is not a root → refused: `"<KEY> belongs to <ROOT>, another top-level item. Spawn agents only inside your own tree; to start <ROOT>'s orchestrator, spawn it on <ROOT>."`
   Any non-orchestrator role on another root's item → refused:
   `"<KEY> belongs to <ROOT>, another top-level item. Spawn agents only inside your own tree."`
2. **Self-heal.** A reconcile step `detachCrossRootParents` runs every tick: for
   each agent whose `parent_agent_id` points at an agent with a different
   `root_item_id`, set `parent_agent_id = NULL`, rewrite the brief header
   `· parent: <name> ·` to `· parent: none ·`, and enqueue an
   `assignment_update` message with the new brief plus the line
   `"You are now a top-level orchestrator: ask the user with your native question tool."`
   Idempotent; one `agent.detached` event per agent.
3. **Worktree refs (BUG-34/39).** Every MCP input that takes a worktree
   (`swarm_spawn`, `swarm_workflow start`, `swarm_worktree share/release/remove/review`)
   accepts the id (`wt_…`) or the absolute path returned by `swarm_worktree create`.
   Unknown → `"Unknown worktree <ref>. Pass the worktree id or path that swarm_worktree create returned."`
   `BriefWorktrees` returns that error instead of silently dropping the entry.
4. **Review worktree dependencies (BUG-42/45).** After `worktree.Service.Review`
   checks out the sha, it clones `node_modules` dirs into the new tree when a
   source tree of the same repo has the identical lockfile
   (`pnpm-lock.yaml`, `package-lock.json`, `yarn.lock`, `bun.lockb`, compared
   by bytes). Source order: active worktree of that repo whose HEAD is the sha,
   then any active worktree of that repo, then the repo's primary checkout.
   Dirs cloned: `node_modules` at depth 0–2 (`node_modules`, `*/node_modules`,
   `*/*/node_modules`), never descending into a `node_modules`. On darwin use
   `cp -cR` (APFS clone), elsewhere `cp -R`. Bounded by 120 s; any failure is
   logged and the review tree is still returned. No lockfile match → nothing
   copied, logged once. Ignored files must not make the tree read as dirty or
   block `remove`.
5. **Launch dir GC (BUG-35).** Reconcile removes the whole
   `<home>/run/launch/<session>` dir for sessions in a terminal state
   (`completed/failed/crashed/cancelled`) whose `ended_at` (or last update) is
   older than 24 h, and dirs with no sessions row older than 7 days. Never a
   live, paused or interrupted session's dir. Replaces
   `reclaimOldCodexLaunchHomes`.
6. **Quota-reset wakes (BUG-37).** `WakeOnQuotaReset` wakes a session only when
   it has something to do: unacked inbox messages, open requests to resurface,
   or a recorded usage stall (it hit the limit in this window). A session with
   none is marked woken for the cutoff without pasting anything. The debugger
   also explains and fixes why one blocked session got four wakes in a row.
7. **Silent session end (BUG-40).** When a worker session ends (state
   finished/completed by pane exit or provider stop) with no
   `completed`/`failed`/`blocked`/`handoff` checkpoint since its `accepted`,
   the parent gets a `relay` `event: "ended_without_checkpoint"` with the
   agent name, item, last checkpoint summary and a hint to `swarm_control retry`.
   The debugger root-causes why researcher sessions end mid-task first.
8. **TDD gate in fix rounds (BUG-43, revised 2026-10-07 after root cause).**
   Root cause from TASK-546's rows: the gate already scoped to finding units,
   but every unit-tagged finding, even a minor/nit one with nothing to test
   (stale comment, commit hygiene), demanded a red→green pair. Revised rule: in
   a fix round the `tdd` gate requires a pair this round only for units with a
   major or critical finding. Units with only minor/nit findings need none. A
   major/critical finding with no unit tag requires one pair (any unit). A round
   whose findings are all minor/nit needs no tdd pair (commit and verify still
   apply). A bare blocked verdict with no findings still needs one pair.
9. **Progress is not a turn boundary (BUG-41).** A `progress` checkpoint from a
   workflow run step returns `"next": "Keep working in this turn: continue with the next unit. Don't end your turn until you write completed, blocked or failed."`
   and `skills/swarm-coder` says the same (after `make skills-sync`).
10. **BUG-38.** Add a regression test that an orchestrator's `integrated` on a
    chore with `git: []` and verification is accepted. No behaviour change.

## DB models

No schema change. Package A writes `agents.parent_agent_id`/`agents.brief` and
one `agent.detached` event; package C reads `sessions.state`/timestamps.

## Model / API types

- `swarm_spawn` result: `{"agent": string, "session": string, "queued": bool, "top_level"?: true}`.
- `runtime.ResolveWorktreeRef(ctx, ref string) (worktreeID string, err error)` in `internal/runtime/worktree_ref.go`.
- Relay payload: `{"event": "ended_without_checkpoint", "agent": string, "item": string, "last_checkpoint": string, "next": "swarm_read the agent, then swarm_control retry or reassign."}`.

## Screens

No UI change. The menubar shows the detached orchestrator's requests because it is
top-level (existing behaviour for parentless agents).

## All user-facing copy

Exactly the strings in Locked decisions 1, 2, 3, 7 and 9.

## File list

- A: `internal/mcpserver/orchestrator.go`, `internal/runtime/agents.go`, `internal/runtime/reconcile.go` (new func), tests, `skills/swarm-orchestrator/SKILL.md` (cross-root spawn line).
- B: `internal/runtime/worktree_ref.go` (new), `internal/runtime/workflow.go` (`BriefWorktrees`), `internal/mcpserver/*` worktree inputs, `internal/worktree/worktree.go` (+ `deps.go` new), tests.
- C: `internal/runtime/reconcile.go` (launch GC), `internal/runtime/wake.go`, session-end path, tests.
- D: tdd gate (`internal/runtime/checkpoint.go` / `internal/workflow`), checkpoint response, `skills/swarm-coder/SKILL.md`, tests.

## Verification

1. Per package: `go test ./internal/<touched pkgs>/...`.
2. Integration: `make skills-sync && git diff --exit-code`, `go vet ./...`, `go test ./...`.
3. Scenarios (tests): cross-root orchestrator spawn on a root starts top-level; on a story refused; reconcile detaches a seeded cross-root parent once; worktree path accepted by spawn and workflow start; unknown ref error; review tree gets node_modules when lockfiles match and none when they differ; terminal launch dir >24 h removed, live kept; quota wake skips idle-blocked session; silent end relays parent; fix-round gate passes with evidence only for the finding's unit.
4. After finish: `make install`, daemon kickstart, `swarm_read apple-watch-provider-orchestrator` shows `parent: null`.

## Explicitly out of scope

- BUG-44 (endurio-chat).
- Allowing cross-root `swarm_send` (the fix removes the need).
- Running dependency installs in review trees.
- Any menubar/web UI changes.

# CHORE-31: Board cleanup — fix open agent-swarm bugs, then close stale roots

## Context

The user asked to close stale roots (CHORE-9, CHORE-15, CHORE-17, CHORE-23, BUG-9,
EPIC-13, EPIC-9) and to "address ALL open/draft bugs first". Board evidence
(swarm.db, 2026-10-03):

| Root | Status | State |
|---|---|---|
| BUG-9, CHORE-17, CHORE-23, EPIC-13 | In review | approved finish request bound to the latest `integrated`; orchestrator wrote `completed`, never `finishing` |
| CHORE-9, CHORE-15 | In progress | `completed` only, never `integrated` |
| EPIC-9 | Blocked | obsolete: EPIC-17 (#64) removed RevenueCat Customer Center |

No tool can move these to Done: `checkRoot` gives Done only to the daemon via an
approved finish plus `finishing`; `resolved_by` accepts Draft/Ready only.

Open Draft bugs in agent-swarm: BUG-28, BUG-29, BUG-30, BUG-31, BUG-32, BUG-33.
Already handled without code: BUG-1 resolved by EPIC-2 (Eve removed in the Go
migration), BUG-27 resolved by CHORE-27 (duplicate of BUG-22, fixed in ee4cac0).
BUG-7 (endurio-chat) is skipped by user decision.

EPIC-7 needs context and subtasks added, but a top-level orchestrator can't
write into another root's tree. The user chose to extend Swarm for this.

Collision warning: package A owns `internal/items/*`, `internal/runtime/checkpoint.go`
and `internal/mcpserver/orchestrator.go`. Package B owns `cmd/swarm/*`,
`internal/hook/*`, `internal/adapter/*` and the worktree reclaim code in
`internal/runtime/reconcile.go`/`workdirs.go`. Package C owns `skills/**` and runs after A.

## Locked decisions

1. **BUG-33 widen resolve (user decision 2026-10-03).** `resolved_by` closes a
   top-level epic/bug/chore in Draft, Ready, In progress, In review or Blocked as
   Done. Done, Cancelled and Awaiting approval stay refused. All other rules of
   the 2026-10-03 resolved-by spec stay: target is a different Done top-level
   epic/bug/chore; actors are the user and top-level orchestrators; side effects
   cancel non-Done descendants (and their agents/workflows) and stale open
   accept requests; one `item_resolved` event.
2. **BUG-33 prevention.** A root orchestrator's `completed` checkpoint on its own
   root is refused while an approved finish request is bound to the current
   `integrated` checkpoint and no `finishing` checkpoint has been written since.
   Message: "The user approved the finish. Write a `finishing` checkpoint (prs,
   merged or kept), not `completed`."
3. **BUG-31.** An orchestrator's `integrated` checkpoint on a chore root may omit
   `git` when it carries `verification` and the chore has no worktree commits.
   The finish request then needs no PR or merge: a `finishing` checkpoint with
   only `kept` entries, or with none at all for a no-repo chore, closes it.
   Epics and bugs still require `git`.
4. **EPIC-7 cross-root edits (user decision 2026-10-03).** A top-level
   orchestrator may `swarm_items create` stories/tasks under, and `update`
   title/brief/acceptance of, items in another top-level root whose root status
   is Draft or Ready and which has no live orchestrator agent. Each such write
   records an `item_cross_root_edit` event `{actor_root, actor}`. Status changes
   on foreign roots remain refused (except `resolved_by`).
5. **BUG-32.** `swarm start KEY --repo PATH` reads `repos_version` from the wrapped
   `{"item": {...}}` GET body (same fix as 909bd43). Stub-daemon test fakes return
   the real wrapped shape.
6. **BUG-29.** Claude's PreCompact hook output carries no `hookSpecificOutput`; the
   reminder goes in top-level `systemMessage`. Other agents' PreCompact output is
   unchanged.
7. **BUG-30.** Worktree reclaim never leaves a half-deleted tracked tree. The
   coder finds the root cause first (systematic-debugging: concurrent reclaim
   passes, or a partial delete on a large tree). Required outcome: concurrent
   reclaim passes on one tree are serialized, and a failed or partial remove
   leaves the record `retained` with the error and is reported, never counted
   `removed`.
8. **BUG-28.** The swarm-coder skill says a unit's red evidence is a `progress`
   checkpoint mid-turn, never a stopping point: the coder continues red, green,
   commit, next unit in the same turn and ends its turn only on `completed`,
   `blocked`, or a question.

## DB models

None. No schema or migration changes (events use the existing events table).

## Model / API types

No new wire types. Behaviour changes only:
- `items.resolveTx` eligibility set widens (decision 1).
- `swarm_items` create/update accept a foreign-root `parent`/`key` under decision 4.
- `swarm_checkpoint kind:"integrated"` accepts empty `git` for a chore (decision 3).

## Screens

None. No UI changes.

## User-facing copy

- Refusal (decision 2): "The user approved the finish. Write a `finishing` checkpoint (prs, merged or kept), not `completed`."
- Refusal (decision 1): "Only an open item can be resolved by another item; %s is %s."
- Refusal (decision 4): "%s is outside %s, and its root is %s or has a live orchestrator."

## File list

- A: `internal/items/transition.go`, `internal/items/store.go`, `internal/runtime/checkpoint.go`, `internal/mcpserver/orchestrator.go`, their `_test.go` files.
- B: `cmd/swarm/runtime_cmds.go` (+ test fakes), `internal/hook/handler.go` / `internal/adapter/claude.go` (+ tests), `internal/runtime/reconcile.go`, `internal/runtime/workdirs.go`, `cmd/swarm/cleanup.go` (+ tests).
- C: `skills/swarm-coder/SKILL.md`, `skills/swarm-orchestrator/SKILL.md`, mirrors via `make skills-sync`.

## Verification

1. `go vet ./...`
2. `go test ./...`
3. `make skills-sync && git diff --exit-code`
4. After install + daemon restart: `swarm resolve --by CHORE-31 CHORE-9 CHORE-15 CHORE-17 CHORE-23 BUG-9 EPIC-13 EPIC-9 BUG-28 BUG-29 BUG-30 BUG-31 BUG-32 BUG-33` closes each as Done.

## Explicitly out of scope

BUG-7 (endurio-chat), web/menubar UI for resolve, auto-closing bugs on finish,
EPIC-7 implementation work (only its context and subtasks are added).

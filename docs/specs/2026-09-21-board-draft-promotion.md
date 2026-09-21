# Board stays Draft while the orchestrator works

## Context
An orchestrator (EPIC-2, `full-go-api-migration-orchestrator`) built its tree with `swarm_items create`, which has no `status` argument, so every story and task was created `draft`. Workers then checkpointed normally (169 checkpoints on EPIC-2), but the board barely moved: 37 tasks and 13 stories still `draft`, EPIC-2 progress `0/14`.

Root cause chain (verified against `~/.swarm/swarm.db` and `~/.swarm/logs/daemon.err.log`, 78 "stays put, denied" lines):
1. `internal/items/transition.go` `checkTask`/`checkRoot` allow only `Ready -> InProgress` for the daemon. `Draft -> Ready` needs the user or an orchestrator actor.
2. `internal/runtime/checkpoint.go` `tryTransition` swallows the denial (logs only), so the checkpoint is recorded and the status silently stays put.
3. `deriveStory` (`transition.go`) skips any story that is not already Ready/InProgress/InReview/Done, so a Draft story never derives from its tasks.
4. `swarm_items create` (`internal/mcpserver/orchestrator.go`) cannot set status; `skills/swarm-orchestrator/SKILL.md` never says to promote items before spawning.
5. 72 of 80 agents in EPIC-2 were spawned on stories, so their `accepted` checkpoints attach to the story (which returns `generic` for every daemon transition) instead of a task.

The materializer (`internal/runtime/materialize.go`) already creates all non-root items as `Ready`; only hand-built trees hit this.

Affected repo: agent-swarm only. Collision: none in flight touches `orchestrator.go` spawn/items tools.

## Locked decisions
- Fix in `swarm_spawn` (mcpserver), not `runtime.Spawn`: `httpapi startOrchestrator` also calls `runtime.Spawn` for the epic root and must not change.
- Promotion uses the **orchestrator actor** (`items.Orchestrator(a.ID, a.RootItemID)`), never `items.Daemon()`: the daemon may not do `Draft -> Ready`.
- Only promote when the item is `Draft`. This makes idempotent replays (item already `in_progress`) a no-op.
- Do not widen `deriveStory` to include Draft; that would auto-flip user-authored Draft stories.
- `swarm_items create` keeps default `Draft`; `status` is opt-in (`draft` or `ready`).
- Assumption: `swarm_spawn` does NOT refuse story spawns in this change (behaviour change, not approved). Skill doc steers orchestrators to task keys instead.
- Not redeploying the live daemon and not promoting the existing 37 draft items in this change.

## DB models
None. No schema, index or migration changes.

## Model / API types
`swarm_items` schema gains one optional field, already in the schema string but ignored on create:
```go
// itemsTool, op "create": pass through
items.CreateInput{ ..., Status: items.Status(in.Status) }
```
`items.CreateInput.Status` already accepts only `""`, `draft`, `ready` (`store.go:209-212`); anything else returns `New items start as Draft or Ready.`

`spawnTool`, after the I12 `BlockedBy` check and `callerAgent`, before `s.RT.Spawn`:
```go
// promoteDraft moves a Draft task, and its Draft parent story, to Ready as the
// caller's orchestrator so the daemon may later move them to In progress.
func promoteDraft(ctx context.Context, s *Server, it items.Item, actor items.Actor) error
```
Behaviour: only when `it.Type == items.Task && it.Status == items.Draft`; transition the task to Ready, then if `it.ParentKey != ""` and that parent is a Story in Draft, transition it to Ready. Errors propagate to the caller.

## Screens
None. The board (web + menubar) already renders item status; it will reflect the corrected statuses.

## User-facing copy
None new. Existing denial copy is unchanged.

## File list
- Change: `internal/mcpserver/orchestrator.go` (`spawnTool` promotion; `itemsTool` create passes `Status`)
- Change: `internal/mcpserver/orchestrator_test.go` (new tests)
- Change: `skills/swarm-orchestrator/SKILL.md` (spawn on task keys; `swarm_items create status: "ready"`; promote before spawn)
- Reused unchanged: `internal/items/*`, `internal/runtime/*`
- Deleted: none

## Verification
1. `go test ./internal/mcpserver/ -run 'TestSpawnPromotes|TestItemsCreate.*Status' -v` fails before the change, passes after.
2. `go test ./internal/mcpserver/ ./internal/items/ ./internal/runtime/` all green.
3. `go vet ./...` and `go build ./...` clean.
4. Scenarios covered by tests: spawn on Draft task under Draft story promotes both to Ready; spawn on already-Ready task changes nothing; spawn on a Draft task whose parent is not a story (epic/bug root) promotes only the task; replay of the same `request_id` does not error; `swarm_items create status:"ready"` creates Ready; `status:"in_progress"` is refused.
5. Manual (after a separate, approved redeploy): a fresh orchestrator spawning on a Draft task shows `in_progress` on the board once the worker writes `accepted`.

## Explicitly out of scope
- Refusing story spawns in `swarm_spawn`.
- Promoting or repairing the 37 draft tasks / 13 draft stories currently in EPIC-2.
- Surfacing swallowed denials in `tryTransition` as a user-visible warning.
- Redeploying `~/.swarm/bin`.

# Close a Draft/Ready root as Done "resolved by" another item (TASK-505)

## Context
Bugs fixed by another item (e.g. BUG-13..25 fixed by CHORE-27) stay Draft forever: `check()`/`checkRoot`
(`internal/items/transition.go`) only lets a root reach Done through the finish flow. This adds an audited
"resolved by" close.

## Locked decisions
1. Eligible: a top-level epic/bug/chore in Draft or Ready, moving to Done, only when the patch names
   `resolved_by` = key of a different top-level epic/bug/chore whose status is Done; otherwise refuse with a
   clear message. Spikes are excluded (they have `close_spike`).
2. Actors: user (HTTP PATCH / CLI) and any top-level orchestrator. For this one transition only,
   `orchestratorScope` is exempted (the target is by definition outside the caller's tree). Workers and the
   daemon cannot.
3. Persist: new nullable `items.resolved_by_id` (FK `items.id`) via a new migration; item JSON gains
   `resolved_by` (key) in HTTP and MCP outputs.
4. Side effects: same as cancel (`cancelDescendants`, `staleAccepts`) plus one event, `item_resolved`,
   payload `{resolved_by, actor}`, so it is audited on the board.
5. Surfaces: `items.Patch.ResolvedBy`; `PATCH /api/items/{key}` body `resolved_by`; MCP `swarm_items` update
   arg `resolved_by` (status `done` required with it); CLI `swarm resolve --by KEY2 KEY...` (flags before
   args, closes each, prints a result per key).
6. Docs: the swarm-orchestrator skill gains one bullet describing this as a deliberate audited exception to
   "a root reaches Done only through the finish answer"; run `make skills-sync`.

## Refusals
Missing/unknown target, self, target not Done, target a spike or non-root, subject a spike or non-root,
subject not Draft/Ready, `resolved_by` without status `done`, worker or daemon actor.

## Verification
`go vet ./...`, `go test ./...`, `make skills-sync && git diff --exit-code`.

## Out of scope
Web board UI, menubar UI, auto-closing bugs from chore finishing.

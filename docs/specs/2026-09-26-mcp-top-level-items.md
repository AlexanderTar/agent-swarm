# MCP `swarm_items create` — Top-Level Items Specification

- **Date**: 2026-09-26
- **Status**: Locked. All decisions below were made by the user 2026-09-26 (see "Locked decisions").
- **Repos / dirs**: `agent-swarm` (`internal/items`, `internal/mcpserver`, `internal/runtime`, `internal/notifyrules`, `apps/menubar`, `skills/swarm-orchestrator`)
- **Companion plan**: `docs/plans/2026-09-26-mcp-top-level-items.md`
- **Worktree**: `agent-swarm-mcp-items` on branch `feat/mcp-top-level-items`

---

## 1. Context

Today an orchestrator's `swarm_items` `op:"create"` can only create an item **inside** its own top-level tree: `internal/items/store.go` (~406-408) refuses any create with no `parent` from an orchestrator actor ("Orchestrators can only create items inside their own top-level item."). The only way work gets a *new* top-level item today is: the user creates one on the board, the daemon materializes one from an approved spike (`internal/runtime/materialize.go`), or `swarm new` opens a spike via `POST /api/spikes` (`POST /api/items`, `internal/httpapi/items.go` ~191, already refuses spikes and is otherwise unaffected by this change).

The gap this closes: an orchestrator (including a spike orchestrator, which is also top-level) that discovers new, out-of-scope work partway through its own item has no way to hand that work back to the user as a proposal — it can only ask the user out-of-band or awkwardly stuff it into its own tree. This spec lets a **top-level** orchestrator or spike propose a new top-level item (epic/bug/chore/spike) directly through `swarm_items create`, always landing as `Draft` so the user decides whether and when it starts.

## 2. Locked decisions (do not reopen)

1. **No "Needs you" approval gate.** Proposing a top-level item does not create a HITL request row. Starting the resulting Draft item (which only the user can do — orchestrators cannot start other orchestrators, that remains out of scope) *is* the approval gate.
2. **Always Draft.** A propose call that passes `status: "ready"` is refused outright with a fixed message; the daemon never auto-starts a proposed item.
3. **Origin.** The proposed item's `origin_spike_id` column (already generic — see `internal/items/store.go`'s `OriginSpikeID` field, used today for spike→epic/bug/chore materialization) is set to the *caller's own root item id* — the calling orchestrator's top-level item, not necessarily a spike. No schema change, no migration: the board's existing "Started from KEY" rendering (`web/src/copy.ts`, already reused unchanged) already resolves whatever item that id points to.
4. **No rate limit** on how often an orchestrator may propose.
5. **`swarm_workflow start` promotes Draft.** Fold in the existing gap that `swarm_workflow start` does not promote a Draft task (and its Draft parent story) to Ready the way `swarm_spawn` already does (`internal/mcpserver/orchestrator.go`'s `promoteDraft`, ~line 53). This is unrelated in code path to top-level proposing, but closes the same class of "an agent created a Draft item and nothing ever promotes it" gap, and is bundled into this change per the user.

Additional design decisions locked with the above:

- **Permission**: only a **top-level** orchestrator (`Actor.ParentAgentID == ""`) or a spike (which is always top-level) may propose. A child orchestrator (`ParentAgentID != ""`) is refused: *"Only a top-level orchestrator can propose a top-level item. Relay it to your parent."* Workers are already excluded (`swarm_items` is `orchestratorRole`-only). Types `story` and `task` still always require a `parent` (unchanged `parentHint` refusal), regardless of actor.
- **Repos**: any `repos` passed on a propose call land in `suggested_repos` only, never `confirmed_repos` — the normal `confirm_repos` ask/approve gate still applies once/if the item starts and its orchestrator needs them confirmed.
- **Idempotency**: reuses the existing `runtime.IdemTx` keyed on `(session_id, request_id)`, exactly like every other `swarm_items` op. No new idempotency mechanism.
- **Notification**: reuses `item.created`/`.bug`/`.chore` (`internal/notifyrules`, raised via `internal/runtime/materialize.go`'s `notifyItemCreated`, exported and generalized here to also cover this path) plus a new `item.created.spike` kind (spikes could not be created as roots by this path before). Body/args are unchanged in shape: `{ORIGIN}` (the `{SPIKE-KEY}` arg slot, reused — it already just means "the key of whatever produced this root") `produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.`

## 3. Types / JSON schema

### `swarm_items` tool, `op: "create"`, no `parent`

Request (unchanged fields omitted for brevity — see the tool's existing schema in `internal/mcpserver/orchestrator.go`):

```json
{
  "op": "create",
  "type": "epic|bug|chore|spike",
  "intent": "feature|debug|chore",
  "title": "string, 1-200 chars",
  "brief": "string",
  "acceptance": ["string", "..."],
  "repos": ["repo_id", "..."],
  "request_id": "idempotency key, recommended"
}
```

- `intent` is new on the wire. It maps to `items.CreateInput.SpikeIntent` (already-existing internal field/validation — spikes already require one of `feature|debug|chore`, unchanged). Only meaningful (and required) when `type: "spike"`.
- `parent` omitted (or `""`) is what selects this path. Passing `parent` keeps today's behavior exactly (create inside the caller's own tree; unaffected by this spec beyond the `Actor.ParentAgentID` plumbing, which is a no-op there).
- `status` must be omitted or `"draft"`. `status: "ready"` is refused (decision 2).
- `type` must be one of `epic|bug|chore|spike` for this path; `story`/`task` are refused with the existing parent-required message regardless of actor.

Response: the created `Item` (unchanged shape), `status: "draft"`, `suggested_repos` populated from `repos` (if any), `repos: []`, `confirmed_repos_version: 0` (unchanged zero-value), `origin_spike_id` set to the caller's root item id.

### Go types (internal/items)

```go
// Actor gains one field; existing constructors/callers are unaffected
// (default "" preserves current behavior everywhere else).
type Actor struct {
    Kind          string
    AgentID       string
    Role          string
    RootID        string
    Via           string
    ParentAgentID string // "" for a top-level orchestrator or any non-orchestrator actor
}
```

No other type changes. `CreateInput`, `Item` already carry every field this path needs (`SuggestedRepos`, `OriginSpikeID`, `SpikeIntent`).

### Go types (internal/runtime)

```go
// NotifyItemCreated is exported (was notifyItemCreated) and generalized:
// originKey is whatever produced the root (a spike's key, or a proposing
// orchestrator's own root key) — the {SPIKE-KEY} template arg slot is
// reused unchanged.
func (s *Store) NotifyItemCreated(ctx context.Context, tx *sql.Tx, originKey, rootKey, rootTitle string, rootType items.Type) error
```

## 4. Exact copy

| Situation | Message |
|---|---|
| Child orchestrator tries to propose a top-level item | `Only a top-level orchestrator can propose a top-level item. Relay it to your parent.` |
| `status: "ready"` passed on a propose call | `A proposed top-level item starts as Draft. The user starts it.` |
| `story`/`task` with no parent (any actor) | unchanged: `A story needs a parent epic.` / `A task needs a parent story, bug, spike or chore.` |
| Notification title (new kind `item.created.spike`) | `Spike ready` |
| Notification body (all `item.created*` kinds, unchanged template) | `{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.` |

No other user-facing copy changes. No new i18n keys (this codebase has none for this surface — copy lives in Go string literals and `web/src/copy.ts`, neither of which needs a new entry beyond the notification title above).

## 5. File list

**Changed:**
- `internal/items/model.go` — add `Actor.ParentAgentID`.
- `internal/items/store.go` — top-level-propose gate in `CreateTx` (permission, forced Draft, repos→suggested, origin).
- `internal/mcpserver/orchestrator.go` — `swarm_items` schema gains `intent`; `create` handler sets `actor.ParentAgentID`, maps `intent`→`SpikeIntent`, raises the notification when `parent == ""`; tool `Description` updated.
- `internal/runtime/materialize.go` — `notifyItemCreated` → exported `NotifyItemCreated`, generalized signature, `item.created.spike` case added; `Materialize`'s call site updated.
- `internal/runtime/workflow.go` — `StartWorkflow` promotes a Draft task (and Draft parent story) to Ready before starting, mirroring `promoteDraft`.
- `internal/notifyrules/notifyrules.go` — new `item.created.spike` rule row.
- `apps/menubar/Sources/SwarmBarKit/Notifier.swift` — `category(forKind:)` maps `item.created.spike` to `swarm.item`.
- `skills/swarm-orchestrator/SKILL.md` — document proposing a top-level item and that it stays Draft until the user starts it.

**Reused unchanged:** `web/src/copy.ts` "Started from KEY" rendering; `items.CreateInput`/`Item`/`SpikeIntent`/`SuggestedRepos`/`OriginSpikeID` fields; `runtime.IdemTx`; `promoteDraft` (spawn path, untouched — `StartWorkflow` gets its own inline equivalent since it lives in `internal/runtime`, not `internal/mcpserver`).

**Deleted:** none.

## 6. Verification

1. `go test ./... -count=1`
2. `go vet ./...`
3. `gofmt -l .` (must be empty)
4. `(cd apps/menubar && swift test)`
5. `(cd web && pnpm test)` only if web files touched (they are not, by this spec — skip unless a later revision touches `web/`)
6. `make skills-sync` clean (no diff)
7. Scenarios covered by new/updated Go tests (see companion plan): top-level orchestrator proposes an epic/bug/chore/spike successfully (Draft, suggested repos, origin set, notification raised); child orchestrator refused; `status:"ready"` refused; story/task still refused without parent for every actor kind; `swarm_workflow start` promotes a Draft task and its Draft parent story to Ready before starting.

## 7. Explicitly out of scope

- Agents starting other orchestrators (starting a Draft item stays user-only).
- Any "Needs you" / HITL request row for a proposal.
- Rate limiting proposals.
- `POST /api/items` / `POST /api/spikes` (`internal/httpapi`) — unaffected; those are the user/board-driven creation paths and already behave correctly.
- Fixing `item.created.chore`'s pre-existing, unrelated kind-trimming inconsistency in `internal/notify/notify.go` (`strings.TrimSuffix(in.Kind, ".bug")` does not also strip `.chore`) — noted, not touched, to keep this diff scoped.
- Any change to `web/` (no web-side kind/category table exists for `item.created*`).

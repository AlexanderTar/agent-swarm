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

- **Permission**: only a **top-level** orchestrator (`runtime.Agent.ParentAgentID == ""`, already on hand at the `swarm_items` call site — no new field on `items.Actor`) or a spike (which is always top-level) may propose. The child-orchestrator guard lives in `internal/mcpserver` (where `ParentAgentID` is already available on the caller's resolved `Agent`), not inside `internal/items`: a child orchestrator (`ParentAgentID != ""`) is refused before `items.CreateTx` even runs: *"Only a top-level orchestrator can propose a top-level item. Relay it to your parent."* Workers are already excluded (`swarm_items` is `orchestratorRole`-only). Types `story` and `task` still always require a `parent` (unchanged `parentHint` refusal), regardless of actor. `internal/items` itself only needs to stop refusing *every* orchestrator create with no parent — the caller-side guard is what narrows that back down to top-level-only.
- **Repos**: any `repos` passed on a propose call land in `suggested_repos` only, never `confirmed_repos` — the normal `confirm_repos` ask/approve gate still applies once/if the item starts and its orchestrator needs them confirmed.
- **Idempotency**: reuses the existing `runtime.IdemTx` keyed on `(session_id, request_id)`, exactly like every other `swarm_items` op. No new idempotency mechanism.
- **Notification**: reuses `item.created`/`.bug`/`.chore` (`internal/notifyrules`, raised via `internal/runtime/materialize.go`'s `notifyItemCreated`, exported and generalized here to also cover this path) plus a new `item.created.spike` kind (spikes could not be created as roots by this path before). Body/args are unchanged in shape: `{ORIGIN}` (the `{SPIKE-KEY}` arg slot, reused — it already just means "the key of whatever produced this root") `produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.`
- **Kind trimming, root cause not bandage**: `internal/notify/notify.go`'s `raise` only strips a `.bug` suffix (`strings.TrimSuffix(in.Kind, ".bug")`) before writing the DB `kind` column and SSE payload, even though the doc comment on `notifyrules.Rules` says every `item.created.*` variant is meant to collapse to the one kind `item.created` the menubar's category map and rest of the system understand. `.chore` already leaks through untrimmed today (an existing, unrelated bug — the menubar's `Notifier.category(forKind:)` has no case for it and silently falls back to `swarm.info`, losing the "Start orchestrator" action). Adding `.spike` the same ad-hoc way would be a second copy of the same leak. This spec fixes the trim at its root instead: `if strings.HasPrefix(in.Kind, "item.created.") { kind = "item.created" }`, which also fixes the pre-existing `.chore` leak as a side effect. No menubar Swift change is needed — `Notifier.category(forKind:)`'s existing `case "item.created": return "swarm.item"` already covers every `item.created.*` variant once the persisted kind is uniformly trimmed; this spec's job there is only to run `swift test` and confirm it.

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

No type changes. `items.Actor` is untouched — the child-orchestrator guard uses `runtime.Agent.ParentAgentID`, already resolved at the `swarm_items` call site, before `CreateTx` runs. `CreateInput`, `Item` already carry every field this path needs (`SuggestedRepos`, `OriginSpikeID`, `SpikeIntent`).

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
- `internal/items/store.go` — top-level-propose gate in `CreateTx` (drop the blanket orchestrator refusal; forced Draft, repos→suggested, origin; `story`/`task` parent requirement unchanged and now explicit first).
- `internal/mcpserver/orchestrator.go` — `swarm_items` schema gains `intent`; `create` handler refuses a child orchestrator (`a.ParentAgentID != ""`) proposing with no `parent`, maps `intent`→`SpikeIntent`, raises the notification when `parent == ""`; `swarm_workflow`'s `case "start"` (in `internal/mcpserver/workflow.go`, see below) reuses the existing `promoteDraft` helper; tool `Description` updated.
- `internal/mcpserver/workflow.go` — `case "start"` calls `promoteDraft` (from `orchestrator.go`) on the target item before `StartWorkflow`, mirroring the `swarm_spawn` precedent exactly (same helper, same package).
- `internal/runtime/materialize.go` — `notifyItemCreated` → exported `NotifyItemCreated`, generalized signature (`originKey string` replaces `spike items.Item`), `item.created.spike` case added; `Materialize`'s call site updated.
- `internal/notify/notify.go` — kind-trim fix: `strings.HasPrefix(in.Kind, "item.created.")` replaces the `.bug`-only `TrimSuffix`, so `.chore` and `.spike` both collapse to `item.created` like the doc comment always claimed.
- `internal/notifyrules/notifyrules.go` — new `item.created.spike` rule row.
- `skills/swarm-orchestrator/SKILL.md` — document proposing a top-level item and that it stays Draft until the user starts it.

**Reused unchanged:** `web/src/copy.ts` "Started from KEY" rendering; `items.CreateInput`/`Item`/`SpikeIntent`/`SuggestedRepos`/`OriginSpikeID` fields; `runtime.IdemTx`; `promoteDraft` (now used from two call sites, `swarm_spawn` and `swarm_workflow start`); `apps/menubar/Sources/SwarmBarKit/Notifier.swift`'s existing `case "item.created": return "swarm.item"` (already covers every variant once the kind-trim fix lands — no Swift change, just `swift test` to confirm).

**Deleted:** none.

## 6. Verification

1. `go test ./... -count=1`
2. `go vet ./...`
3. `gofmt -l .` (must be empty)
4. `(cd apps/menubar && swift test)`
5. `(cd web && pnpm test)` only if web files touched (they are not, by this spec — skip unless a later revision touches `web/`)
6. `make skills-sync` clean (no diff)
7. Scenarios covered by new/updated Go tests (see companion plan): top-level orchestrator proposes an epic/bug/chore/spike successfully (Draft, suggested repos, origin set, notification raised); child orchestrator refused via mcpserver's guard; `status:"ready"` refused; story/task still refused without parent for every actor kind; `swarm_workflow start` promotes a Draft task and its Draft parent story to Ready before starting; the proposed item cannot be moved to Ready by its own proposer (`orchestratorScope` — user-only, decision 1); the `.bug`/`.chore`/`.spike` kind-trim fix covered directly in `internal/notify`'s own tests.

## 7. Explicitly out of scope

- Agents starting other orchestrators (starting a Draft item stays user-only).
- Any "Needs you" / HITL request row for a proposal.
- Rate limiting proposals.
- `POST /api/items` / `POST /api/spikes` (`internal/httpapi`) — unaffected; those are the user/board-driven creation paths and already behave correctly, and neither does anything beyond `Items.Create` that this path would need to replicate.
- Any change to `web/` (no web-side kind/category table exists for `item.created*`).

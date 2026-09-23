# Orchestrator catalog tool

Classification: **bounded** (superpowers:brainstorming). New MCP tool following the
exact shape of `swarm_role_overrides`/`swarm_worktree`; reuses existing
`catalog.Service`/`settings.Store` plumbing untouched. No new subsystem, no DB
change, no new dependency. Approval: the task brief that requested this work
gave explicit go-ahead to decide the design questions and implement, with
review happening after the fact on the reported worktree/commits (no
synchronous human partner in this delegated session to gate on beforehand).

## Context

Orchestrators have no MCP-visible way to learn what agent kinds/models are
valid before guessing at `swarm_spawn`'s `agent`/`model` or
`swarm_role_overrides`' `set`. Both already validate server-side
(`settings.Store.ValidateDefault` → `catalog.Service.ModelsFor`), so a wrong
guess round-trips as a `bad_request` error. This adds a read-only lookup tool
so an orchestrator can check before it leaps.

Repo: `internal/mcpserver` (tool registration), `internal/catalog` (model
data, untouched), `internal/settings` (live Settings, untouched),
`skills/swarm-orchestrator/SKILL.md` + its installed copy (guidance).

## Locked decisions

1. **New tool `swarm_catalog`, not a `swarm_read` op.** `swarm_read` is
   `Unbound: true` and visible to every bound role (`ToolsFor`, server.go) —
   folding catalog data into it would leak it past the orchestrator-only
   scope this data needs (confirmed: no non-orchestrator tool takes an
   `agent`/`model` param — `swarm_read`'s own `agentOut` only *reports* a
   `model`, never accepts one). `itemsTool`'s doc comment already
   establishes read vs. write as a deliberate split in this file; the same
   split argues for a dedicated tool over overloading an existing one with
   an implicit "catalog" concern it wasn't built for.
2. **Orchestrator-only**, matching `swarm_role_overrides`/`swarm_worktree`.
   Added to `orchestratorTools()` in `internal/mcpserver/orchestrator.go`;
   `Tools()` and `ToolsFor()` pick it up automatically, same as every other
   entry in that slice.
3. **No input.** Schema is an empty object (`objSchema("")`); nothing to
   scope by — the tool always returns the caller's whole visible catalog.
4. **Handler reuses `settings.Store.ModelsFor` and `settings.Store.Get`
   directly**, both reached via `s.RT.Settings` (not `s.Settings` on the
   outer `mcpserver.Server`, which is always nil in `newTestServer` — every
   existing tool reaches state through `RT`). `ModelsFor` is the exact
   function `ValidateDefault` itself calls to check a `swarm_spawn`/
   `swarm_role_overrides` agent+model pair — reusing it, rather than
   `catalog.Service.Entries`, means this tool needs nothing beyond what
   validation already needs. **Revised from the original plan** (which
   called for `catalog.Service.Entries`): `Entries` only emits a kind that
   has a registered `Fetcher` on the `catalog.Service` instance, which is a
   production/install-detection wiring concern, not something
   `Settings.EnabledAgents` implies — confirmed empirically when
   `internal/mcpserver`'s own test fixture (`newTestServer`,
   helpers_test.go) turned out to construct `catalog.Service{}` with no
   `Fetchers` at all, so `Entries(ctx)` returns `[]` there regardless of
   `model_catalog` row content. Coupling this tool to `Entries` would have
   meant either wiring a new fake `Fetcher` into a shared fixture ~30 other
   tests depend on (blast radius for one tool) or leaving the tool
   untestable against real seeded catalog data. `ModelsFor` needs neither:
   it reads the `model_catalog` row directly, exactly like `ValidateDefault`
   does today. No new cache, no call to `Refresh` (that execs agent
   binaries and publishes `catalog.changed`; the hourly `Catalog.Loop`
   already owns freshness — a read tool must not trigger a side-effecting
   refresh) — this was already true either way.
5. **Filter to `Settings.EnabledAgents`.** `ValidateDefault` (what both
   `swarm_spawn` and `swarm_role_overrides` actually check against) rejects
   any agent not in `EnabledAgents` regardless of whether it's installed —
   so a disabled-but-installed agent's models are not a valid choice and are
   left out. `Installed`/`auth_ok`/`catalog_stale` (whether a binary was
   found, auth worked, or the last fetch is fresh) are `Entries`-only,
   install-detection signals `ValidateDefault` never consults; not needed
   here and not returned (see point 4 — this tool never calls `Entries`).
6. **Strip `Hidden` models.** Confirmed precedent: the menubar app's
   `CatalogRulesTests.testAliasesComeFirstAndHiddenModelsAreLeftOut` already
   filters `Hidden` client-side before showing a model to a human. An
   orchestrator picking a model for `swarm_spawn`/`swarm_role_overrides` is
   the same kind of chooser; hidden models (deprecated/internal aliases)
   should not be offered as an available choice either.
7. **Response is a small dedicated wire type, not `catalog.AgentCatalogEntry`**
   (revised from the original plan along with point 4 — that type's
   `installed`/`auth_ok`/`catalog_*` fields all come from `Entries`, which
   this tool doesn't call): `{"kind","models","default_model"}` per enabled
   agent (`catalogAgentWire` in orchestrator.go), reusing `catalog.CatalogModel`
   as-is for `models` (its own JSON tags: `id,label,aliases,efforts,
   default_effort,effort_encoding,advisor_capable,is_default`, `hidden`
   never emitted since those rows are filtered out). Plus `"roles"`: the
   live global `Settings.Roles` map (`map[Role]RoleDefault`), reusing
   `roleOverridesOut` (tools.go) for the same nil-guard `swarm_read`/
   `swarm_role_overrides` already use on the identical type — this is what
   an override falls through to when cleared.
8. **Skill guidance**: update `skills/swarm-orchestrator/SKILL.md` (+
   `internal/install/skills/swarm-orchestrator/SKILL.md` via
   `make skills-sync`) to tell an orchestrator to call `swarm_catalog` before
   passing an explicit `agent`/`model` to **either** `swarm_spawn` or
   `swarm_role_overrides` — both already have their own guidance lines in
   this file (spawn: line ~13, role_overrides: line ~14); the new line sits
   beside them and names both by tool name so neither guess path is missed.

## API

`swarm_catalog` — orchestrator role only, no input.

Request: `{}`

Response:
```json
{
  "agents": [
    {
      "kind": "claude",
      "models": [
        {"id": "sonnet", "label": "Sonnet", "aliases": ["claude-sonnet-5"],
         "efforts": ["low","medium","high"], "default_effort": "medium",
         "effort_encoding": "flag", "advisor_capable": false, "is_default": true}
      ],
      "default_model": "sonnet"
    }
  ],
  "roles": {
    "coder": {"agent": "claude", "model": "sonnet"},
    "reviewer": {"agent": "claude", "model": "opus"}
  }
}
```
(`RoleDefault.Effort` is `json:"effort,omitempty"` — omitted here since both
are "", not emitted as `""`.)

`agents` is filtered to `Settings.EnabledAgents`, in `EnabledAgents` order;
each entry's `models` array has `Hidden` entries removed (the `hidden` field
itself is never emitted, matching `CatalogModel`'s `omitempty`, but is never
`true` in what's returned since those rows are dropped). A kind whose catalog
was never fetched returns `models: []`, `default_model: ""` rather than being
omitted — still confirms the kind is enabled. `roles` is the live global
`Settings.Roles` — what a role resolves to when the caller has no
`role_overrides` entry for it (checkable separately via `swarm_read` on the
caller's own name, unchanged by this batch).

## File list

- `internal/mcpserver/orchestrator.go` — add `catalogTool(s)`, add it to
  `orchestratorTools()`'s returned slice.
- `internal/mcpserver/orchestrator_test.go` — new tests next to the
  `swarm_role_overrides` block.
- `internal/mcpserver/server_test.go` — bump the orchestrator/spike-orchestrator
  tool-count golden numbers (14→15, 15→16) and comments.
- `skills/swarm-orchestrator/SKILL.md` + `internal/install/skills/swarm-orchestrator/SKILL.md`
  (synced via `make skills-sync`) — one guidance line.
- Reused unchanged: `internal/catalog/*`, `internal/settings/*`,
  `internal/mcpserver/tools.go`'s `roleOverridesOut` and `objSchema` (the
  handler takes no input, so `decode` doesn't apply here).

## Verification

- `go test ./internal/mcpserver/...` (new + existing, including the updated
  golden tool-count tests).
- `go vet ./...` in the worktree.
- Manual schema sanity: `swarm_catalog`'s registered `InputSchema` is a bare
  `{"type":"object","properties":{},"additionalProperties":true}`.

## Explicitly out of scope

- Any change to `swarm_spawn`/`swarm_role_overrides` validation (they keep
  validating server-side regardless of whether a caller called this tool
  first).
- Any change to `/api/catalog`, `refreshCatalog`, or the menubar app.
- Exposing this tool to non-orchestrator roles, or making it `Unbound`.
- A new cache layer — `catalog.Service`'s existing cache/refresh cadence is
  reused untouched.
- DB / migrations: none. Screens: none (MCP-only). User-facing copy: none
  beyond the tool description string and the one skill line.

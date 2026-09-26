# Spec: fix agy (and cursor) model passthrough

## Context

Swarm assigns agy agents a catalog *base* model id (e.g. `gemini-3.8-flash`),
but agy 1.2.11 only accepts effort-suffixed ids (`gemini-3.8-flash-high|
-medium|-low`). The launch fails to resolve the flag, agy logs a warning, and
falls back silently to whatever model is set in
`~/.gemini/antigravity-cli/settings.json` -- ignoring Swarm's per-agent
choice entirely. The same raw passthrough exists for `Agy.Wake` (no `--model`
at all) and for `Cursor.Launch`/`Cursor.Resume`.

`catalog.CatalogModel.LaunchModel(effort)` already implements the base+effort
-> exact launch id mapping (`internal/catalog/model.go`), but nothing in
`internal/runtime` or `internal/adapter` calls it; only its own unit tests
do.

## Locked decisions

- Resolution happens once, in `internal/runtime`, at the single place that
  turns an `Agent` into an `adapter.Spec`/`adapter.WakeTarget`: `startSession`
  (`internal/runtime/agents.go`) for launch/resume/retry/usage-fallback (all
  four route through `startSession`), and the two wake call sites in
  `internal/runtime/wake.go` (`WakeDue`'s native-wake step and
  `WakeOnQuotaReset`).
- The resolver looks the model up via `s.Catalog.ModelsFor(ctx, kind)` +
  `catalog.Find` (the existing lookup pattern used throughout
  `internal/runtime`) and, on a hit, returns `m.LaunchModel(effort)`. On a
  miss (model not in catalog, catalog unavailable, or `s.Catalog == nil`) it
  returns the input model unchanged -- a user-configured raw suffixed id
  (`gemini-3.8-flash-high`) must keep working.
- This is agent-kind-agnostic: it runs for every kind, not just agy. For
  Claude/Codex/Fake, `EffortEncoding != "slug"`, so `LaunchModel` is a no-op
  (`return m.ID`) -- the behavior for those kinds does not change.
- `WakeTarget` gains a `Model` field; `Agy.Wake` passes `--model <field>`
  when non-empty. Other adapters' `Wake` do not read it (Claude/Codex wake
  via a side-channel that never took `--model`; adding the field does not
  change their behavior since they ignore it).
- Cursor: `internal/catalog/parse.go`'s `ParseCursorModels` builds ids like
  `gpt-5.3-codex` / `gpt-5.3-codex-high` as separate catalog entries (their
  own `ID`), not via `LaunchIDs`/`EffortEncoding: "slug"` on one entry (see
  `ParseCursorModels`, `DefaultLevel` handling). So `CatalogModel.LaunchModel`
  is a no-op for cursor's entries today (`EffortEncoding` empty/"flag") and
  `cursor.go`'s raw `s.Model` passthrough is already correct. Routing cursor
  through the same resolver is still correct (it's a no-op for cursor) and
  future-proofs it if a cursor catalog entry ever adopts slug encoding.

## Model / API types

```go
// internal/adapter/adapter.go
type WakeTarget struct {
    SessionID, ProviderSessionID, TmuxName, Notice, Model string
}
```

```go
// internal/runtime/agents.go (new unexported method on *Store)
func (s *Store) resolveLaunchModel(ctx context.Context, kind AgentKind, model, effort string) string
```
Behavior: `s.Catalog.ModelsFor(ctx, kind)` -> `catalog.Find(models, model)` ->
on hit, `m.LaunchModel(effort)`; on any miss/error/nil-Catalog, `model`
unchanged.

## File list

- `internal/runtime/agents.go`: add `resolveLaunchModel`; `startSession`'s
  `adapter.Spec{Model: ...}` uses it instead of raw `a.Model`.
- `internal/runtime/wake.go`: `wakeRow` gains `Model, Effort`; the
  `wakeCandidates` query selects `a.model, a.effort`; `WakeDue`'s native-wake
  call and `WakeOnQuotaReset`'s query+call resolve through
  `s.resolveLaunchModel` before building `adapter.WakeTarget`.
- `internal/adapter/adapter.go`: `WakeTarget.Model` field.
- `internal/adapter/agy.go`: `Wake` passes `--model` when `w.Model != ""`.
- Reused unchanged: `catalog.CatalogModel.LaunchModel`, `catalog.Find`,
  `internal/adapter/cursor.go` (no code change; documented no-op finding
  above).

## Verification

- `go test ./... -count=1`
- `go vet ./...`
- `test -z "$(gofmt -l .)"`
- New/updated tests (see companion plan) prove: agy launch/resume resolve
  base+effort -> suffixed id; an already-suffixed id passes through
  unchanged; a claude/fake agent is unaffected; agy wake argv carries
  `--model`; cursor is a documented no-op, not a silent gap.

## Explicitly out of scope

- Changing `ParseCursorModels`/cursor's catalog shape to use
  `LaunchIDs`/slug encoding -- cursor's current per-effort-entry catalog
  already produces the right launch id without this resolver.
- Any change to `agy models`/effort validation UX in Preflight.

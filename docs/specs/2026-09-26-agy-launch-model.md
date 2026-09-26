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
  `internal/runtime`) and, only when the hit is slug-encoded
  (`m.EffortEncoding == "slug"`), returns `m.LaunchModel(effort)`. On a miss
  (model not in catalog, catalog unavailable, or `s.Catalog == nil`) or a
  flag-encoded hit, it returns the input model unchanged.
- **Corrected finding (code review, 2026-09-26):** the spec originally
  claimed "Claude/Codex/Fake are a no-op because `EffortEncoding != 'slug'`
  makes `LaunchModel` return `m.ID`" -- true for a *miss*, but wrong for a
  *hit* via `catalog.Find`'s alias matching. `ParseClaudePage` sets
  `Aliases: ["opus"]` (etc.) with `EffortEncoding: "flag"` on the dated
  catalog ID, so `catalog.Find(models, "opus")` succeeds and
  `LaunchModel` returns that dated ID, not `"opus"` -- silently pinning a
  default Claude agent to whatever snapshot the catalog last cached instead
  of the rolling alias. A user-configured raw suffixed id
  (`gemini-3.8-flash-high`) must also keep working, which a miss already
  guarantees. The `m.EffortEncoding != "slug"` guard above is what actually
  keeps Claude/Codex/Fake/bare-cursor-alias models unchanged; it must gate
  on the *encoding of the matched entry*, not on whether a catalog entry was
  found at all.
- `WakeTarget` gains a `Model` field; `Agy.Wake` passes `--model <field>`
  when non-empty. Other adapters' `Wake` do not read it (Claude/Codex wake
  via a side-channel that never took `--model`; adding the field does not
  change their behavior since they ignore it).
- Cursor: **correction after reading `ParseCursorModels`/`slugDialect.group`
  in `internal/catalog/parse.go`** -- cursor's own catalog parser groups
  effort variants (e.g. `gpt-5.3-codex` + `gpt-5.3-codex-high`) under
  `EffortEncoding: "slug"` and `LaunchIDs`, exactly the same shape as agy.
  So `cursor.go`'s raw `s.Model` passthrough was silently wrong for any
  cursor model with effort siblings too, not just agy's -- confirmed live by
  reverting the `resolveLaunchModel` call in `startSession` and watching
  `TestSpawnResolvesCursorLaunchModel` fail with the bare base id in argv.
  Because the resolver in this fix is kind-agnostic, the same `startSession`
  change fixes cursor for free; no cursor-specific code change was needed,
  only the regression test.

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
on a slug-encoded hit (`m.EffortEncoding == "slug"`), `m.LaunchModel(effort)`;
on any miss/error/nil-Catalog/flag-encoded hit, `model` unchanged.

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

- Any change to `agy models`/effort validation UX in Preflight.
- Any cursor-specific code change -- the finding above shows the shared
  `startSession` fix already covers cursor's identical slug-encoding bug.

# Plan: muse catalog label and usage label fixes

Two independent, contained fixes. TDD order for each.

## Task 1 — Bug 1: friendly muse model labels

1. Write `internal/catalog/parse_muse_test.go`:
   - `TestParseMuseModelsFriendlyLabels`: feed the real catalog JSON (the
     four rows from `~/.local/share/muse/model-catalog/6d657461__p746268.json`,
     inlined as a fixture literal) through `ParseMuseModels`, assert labels
     `"Spark 1.3"`, `"Spark 1.3 Contributor"`, `"Spark 1.2"`,
     `"Spark 1.2 Contributor"`.
   - `TestParseMuseModelsPrefersDisplayLabel`: a row with
     `display_label != model_id` (e.g. `"display_label": "Spark Preview"`)
     wins verbatim over the transform.
   - `TestParseMuseModelsTransformNoPrefix`: an id with no `muse-` prefix
     (e.g. `"other-model-2"`) still gets segment-title-cased
     (`"Other Model 2"`), proving the fallback doesn't panic/no-op on an
     unexpected shape.
2. Run `go test ./internal/catalog/... -run ParseMuseModels -v`, confirm it
   fails (Label still raw id).
3. Implement in `parse_muse.go`:
   - Add `DisplayLabel string \`json:"display_label"\`` to `museRow`.
   - Add `museLabel(id, displayLabel string) string`: if
     `displayLabel != "" && displayLabel != id`, return it; else
     `strings.TrimPrefix(id, "muse-")`, split on `-`, title-case first byte
     of each segment, join with `" "`.
   - Use `Label: museLabel(r.ModelID, r.DisplayLabel)` in the `CatalogModel`
     literal.
4. Run the tests again, confirm green.
5. `git add internal/catalog/parse_muse.go internal/catalog/parse_muse_test.go`
   and commit.

## Task 2 — Bug 2: honest CLI usage label

1. Write the failing case in `cmd/swarm/runtime_cmds_test.go` (find the
   existing `cmdUsage`/usage-table test near line 346's fixture and add a
   case): a `/api/usage` fixture row shaped like the real muse row
   (`meters: []`, `error: null`, `fetched_at: 0`, `attempted_at: <nonzero>`,
   `stale: true`) must print `no usage source` in the NOTE column, not
   `stale`. Keep/add a second case with a real aged-out fetch
   (`fetched_at` nonzero but old, `stale: true`) still printing `stale`, so
   the two paths stay distinguishable.
2. Run the test, confirm it fails (current code always prints `stale`).
3. Implement:
   - `internal/httpapi/runtime.go`: confirm `usageWire` already carries
     `fetched_at` (it does, per the existing test fixture) — no change
     needed there.
   - `cmd/swarm/runtime_cmds.go`: add `FetchedAt int64 \`json:"fetched_at"\``
     to `usageRow`. In `cmdUsage`'s note branch:
     ```go
     note := ""
     if u.Error != nil {
         note = *u.Error
     } else if u.Stale && u.FetchedAt == 0 {
         note = "no usage source"
     } else if u.Stale {
         note = "stale"
     }
     ```
4. Add the one-line doc comment to `DefaultSources` in
   `internal/usage/usage.go` noting muse is deliberately absent (no
   quota/usage endpoint exists — verified live against `muse --help`) and
   pointing at `internal/usage/muse.go`'s `MuseSessionUsage` as the
   per-session alternative with no kind-level caller.
5. Run tests, confirm green.
6. `git add cmd/swarm/runtime_cmds.go cmd/swarm/runtime_cmds_test.go internal/usage/usage.go`
   and commit.

## Final verification

From the worktree root:
```
go build ./...
go vet ./...
go test ./...
```
Expect all green except the pre-documented `internal/httpapi.TestBoardServedAtRoot`
(missing built `web/dist` in a fresh worktree).

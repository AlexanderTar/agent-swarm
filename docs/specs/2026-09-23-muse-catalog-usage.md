# Spec: muse catalog label and usage label fixes

## Context

Two confirmed bugs in muse (5th agent kind: claude/codex/agy/cursor/muse):

1. **Catalog label**: `internal/catalog/parse_muse.go`'s `ParseMuseModels` sets
   `CatalogModel.Label: r.ModelID` — the raw model id, unlike claude/codex
   which use a real display-name field. The menubar's model picker shows the
   raw id, truncated by its fixed-width dropdown.
2. **Usage label**: `swarm usage` prints `muse    stale` with no window or
   percentage. `internal/usage/muse.go`'s `MuseSessionUsage`/`ParseMuseExport`
   sum per-session token counts from `muse export` — a fundamentally
   different shape from every other kind's quota-percentage meters.

## Investigation findings (verified live, 2026-09-23)

**Bug 1**: Read the real catalog file
(`~/.local/share/muse/model-catalog/6d657461__p746268.json`). Every row has
`display_label`, but it is byte-identical to `model_id` in all four current
rows (`muse-spark-1.3`, `muse-spark-1.3-contributor`, `muse-spark-1.2`,
`muse-spark-1.2-contributor`) — not a real display name today, but a
provider-supplied field `parse_muse.go` doesn't even read. No other
name-shaped field exists in the row.

**Bug 2**: `internal/usage/usage.go`'s `DefaultSources` builds exactly four
`Source`s (claude, codex, agy, cursor) — muse is entirely absent, not merely
broken. `Poller.pollOnce` iterates only `p.Sources`, so muse is never
auto-polled. The live `usage_snapshots` row for muse
(`muse|[]||muse||0|<timestamp>`) is byte-for-byte `recordAttemptOnly`'s
output (`fetched_at=0`, `error=NULL`) — the code path `RefreshOne` takes when
`sourceFor(kind)` returns nil. This is the *designed* "no source configured"
path (see the comment on `recordAttemptOnly`), not a crash or silent
failure. `Stale` computes true forever because `FetchedAt` stays epoch 0.

Two structural facts rule out wiring `MuseSessionUsage` into `DefaultSources`
as a `Source`:
- `Source.Fetch` is `func(ctx) ([]Meter, string, error)` — per-kind, no
  session id slot. `MuseSessionUsage` needs a specific session id.
- `Meter{Label, Window, UsedPct, ResetsAt}` is a quota-percentage shape.
  Token counts (`MuseUsage{Turns, InputTokens, ...}`) have no honest mapping
  to `UsedPct` — fabricating one is expressly out of scope.

`muse --help` was read live: its subcommand list (`resume, exec, config,
export, trace, skills, plugins, sandbox, schema, serve, session-message,
mcp, auth, login, logout, init`) has nothing usage/quota/billing-shaped,
confirming `usage/muse.go`'s existing comment. `muse export --last --redacted`
was run live from this repo's workspace and produced a real 33-event export
(mechanism works); that particular session had no usage-bearing turns, but
`ParseMuseExport`'s existing test already covers a real fixture with
nonzero token sums (captured 2026-09-23), so the parser is proven against
real data separately from this probe.

**Conclusion for bug 2**: there is no quota-style usage data source for
muse. Per-session token export (`MuseSessionUsage`) is the best available
data, has no natural home in the kind-level poller, and stays unwired. The
actual bug is narrower: the CLI's `stale` label is misleading for a kind
that was never fetched (word implies "had data, aged out"). Fix that label,
not the data model.

## Locked decisions

- Bug 1: prefer `display_label` when it differs from `model_id` (mirrors
  claude/codex trusting the provider's display name); otherwise
  transform the id: strip a leading `muse-`, split on `-`, upper-case each
  segment's first byte, join with spaces (`muse-spark-1.3-contributor` →
  `Spark 1.3 Contributor`). No `muse-`/`Muse` prefix in the label — matches
  claude's alias labels ("Sonnet 5", not "Claude Sonnet 5") and keeps the
  label short enough not to truncate in the fixed-width picker.
- Bug 2: no change to `internal/usage/muse.go`, `DefaultSources`, or the
  Swift menubar (the menubar already renders "Usage unavailable." correctly
  for a kind with `meters: []`, independent of the `stale` field). Fix only
  the CLI's note text in `cmd/swarm/runtime_cmds.go`'s `cmdUsage`: a snapshot
  with no error and `fetched_at == 0` (the unique fingerprint of
  `recordAttemptOnly` — `storeFailure` always sets an error string,
  `storeSuccess` always sets a real `fetched_at`) prints `no usage source`
  instead of `stale`. A genuinely-stale kind (real `fetched_at` that aged
  out) still prints `stale`.
- Add a doc-comment line to `DefaultSources` noting muse is deliberately
  absent and why, so this isn't rediscovered as a bug again.
- `MuseSessionUsage` and its tests are left as-is (CLAUDE.md: no deleting
  tests); reported as a per-session tool with no current caller.

## File list

- `internal/catalog/parse_muse.go` — read `display_label`, add label
  transform.
- `internal/catalog/parse_muse_test.go` — new file, real catalog ids +
  transform + display_label-wins cases.
- `internal/usage/usage.go` — one-line doc comment on `DefaultSources`.
- `cmd/swarm/runtime_cmds.go` — `usageRow` gains `FetchedAt`; `cmdUsage`'s
  note branch distinguishes "no usage source" from "stale".
- `cmd/swarm/runtime_cmds_test.go` — one case for the new note text.

## Explicitly out of scope

- Any change to `internal/usage/muse.go`'s data model or wiring it into the
  poller/`Source` interface.
- Any Swift/menubar change (already correct).
- A hardcoded muse model-name lookup table (id format is regular enough for
  a transform).

## Verification

`go build ./...`, `go vet ./...`, `go test ./...` from the worktree.
Pre-existing `internal/httpapi.TestBoardServedAtRoot` failure (no built
`web/dist` in a fresh worktree) is expected, not in scope.

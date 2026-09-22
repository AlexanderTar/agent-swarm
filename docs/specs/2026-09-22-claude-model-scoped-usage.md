# Surface Claude's model-scoped (Fable) weekly usage in the menubar

## Context

User observed Claude Code's own native `/status` panel shows a distinct
"Current week (Fable)" row alongside "Current week (all models)" — usage
data the daemon's menubar app never surfaces at all. Investigated via:

1. Disassembling the installed CLI binary
   (`~/.local/share/claude/versions/<ver>`, a Bun-bundled minified JS blob):
   found the real render logic builds this row from
   `rate_limits.model_scoped[]` — `{display_name, utilization, resets_at}`
   entries — which itself is projected (`LJe` function in the CLI) from a
   `limits[]` array filtered to entries whose `scope.model.display_name` is
   set, e.g. `{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":0,...}`.
   This is NOT the `anthropic-ratelimit-unified-*` header set
   `internal/usage/claude.go` already polls — it comes from a completely
   different endpoint: `GET /api/oauth/usage?at_wall=1&skip_spend=1`.
2. This is the exact endpoint `claude.go`'s own doc comment already
   documents as abandoned ("runs its own, much stricter and stateful rate
   limit... gets worse on every retry... confirmed live: 2m23s -> 59m11s
   after one retry") — but that comment also flags the `skip_spend=1` query
   param (which the real CLI always sends) as never having been tried.
3. Live-verified (single no-retry call, explicit user approval, using the
   same `Claude Code-credentials` keychain OAuth token this package already
   reads): `GET https://api.anthropic.com/api/oauth/usage?at_wall=1&skip_spend=1`
   returned `200 OK`, no `Retry-After`, no punitive behavior on this one
   call, and its `limits[]` array contained exactly:
   ```json
   {"kind":"weekly_scoped","group":"weekly","percent":0,"severity":"normal",
    "resets_at":"2026-09-29T17:00:00+00:00",
    "scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},
    "is_active":false}
   ```
   alongside the already-known `five_hour`/`seven_day` top-level fields
   (present as separate top-level keys too, redundant with the unified
   headers — this spec ignores those top-level fields and only reads
   `limits[]`, since the existing header-based path already covers
   five_hour/seven_day reliably).
4. Confirmed `apps/menubar/Sources/SwarmBarKit/MenuLabel.swift`'s
   `UsageSection.rows()` (~line 118) already maps `snap.meters` generically
   with no hardcoded IDs, count, or per-kind logic — **no menubar/Swift
   change is needed at all**. Whatever meters `internal/usage/claude.go`'s
   `Fetch()` returns just render.

Affected repo: `agent-swarm`, worktree `~/GitHub/agent-swarm--fable-usage`,
branch `feat/claude-model-scoped-usage`, based on updated `main` (includes
both the wake-immediately and ack-escalation fixes from earlier today). No
other worktree touches `internal/usage/claude.go` at time of writing.

## Locked decisions

- **Endpoint**: `GET {BaseURL}/api/oauth/usage?at_wall=1&skip_spend=1`
  (exact query string — both params are required; `skip_spend=1` is the
  one prior investigation never tried and this spec's live test confirmed
  behaves well). `BaseURL` is the same field `Claude` already has
  (`"https://api.anthropic.com"` in production).
- **Auth/headers**: same bearer token (`c.ReadToken`), same `User-Agent:
  claude-code/<version>` the existing `/v1/messages` probe already sends.
  The real CLI does NOT send `anthropic-version`/`anthropic-beta` on this
  particular endpoint (confirmed: the live test call succeeded without
  them) — do not add them speculatively.
- **Timeout**: 5 seconds, matching the real CLI's own `timeout:5000` for
  this exact call (found in the disassembly).
- **Retry policy: none.** A single attempt, ever, per `Fetch()` call. This
  is the one non-negotiable constraint given this endpoint's documented
  history — no exponential backoff, no immediate retry-on-429, nothing.
  If it fails, the model-scoped meters are simply absent for that poll
  cycle; try again next poll cycle same as normal.
- **Failure isolation**: any error from this second call (network error,
  non-200, malformed JSON, timeout) must NOT fail `Fetch()` as a whole.
  The primary `five_hour`/`seven_day` snapshot already obtained from the
  existing `/v1/messages` header probe is returned regardless — this is
  purely additive, best-effort data.
- **Filtering, generic not hardcoded**: keep every `limits[]` entry where
  `scope != null && scope.model != null && scope.model.display_name != ""`.
  Do NOT hardcode "Fable" as a string match — the real CLI itself filters
  by a server-controlled allowlist this package has no access to, so
  filtering generically on "has a named model scope" is the closest
  equivalent and automatically picks up any future per-model gate Anthropic
  adds the same way, not just Fable.
- **Scale**: this endpoint's `percent`/`utilization` fields are already
  0–100 (confirmed: `"percent": 10` for a 10% used five_hour window in the
  live test body, vs. the header probe's `anthropic-ratelimit-unified-5h-utilization: 0.18`
  which is 0–1 and gets `*100`'d in `claudeSnapshotFromHeaders`). Do NOT
  multiply by 100 again for these new meters.
- **Meter shape** for each kept `limits[]` entry:
  - `ID`: `"model_scoped:" + slug(display_name)` where `slug` lowercases
    and replaces non-alphanumeric runs with `_` (simple, no external dep
    needed — e.g. `strings.ToLower` + a small regex or manual replace is
    enough; check if a similar slugging helper already exists elsewhere in
    this package or `internal/` before writing a new one).
  - `Label`: `"Weekly (" + display_name + ")"` (matches the existing
    `"Weekly (all models)"` label style already used for `seven_day` in
    `claudeWindowHeaderNames`).
  - `Window`: `"weekly"`.
  - `UsedPct`: the entry's `percent` field, used as-is (already 0–100).
  - `ResetsAt`: parsed from the entry's `resets_at` string (RFC3339,
    possibly with fractional seconds — the live-captured example had none
    in this field, `"2026-09-29T17:00:00+00:00"`, but the top-level
    `five_hour.resets_at` in the same body DID have fractional seconds,
    `"2026-09-22T22:00:00.474610+00:00"` — write the parser to handle
    both; verify with a real unit test using both exact strings rather
    than assuming `time.RFC3339` tolerates fractional seconds).
- **`HeadlineID` unchanged**: these new meters are never candidates for
  `Snapshot.HeadlineID` — the existing headline selection logic in
  `Fetch()`/`claudeSnapshotFromHeaders` is untouched. This matters for
  `internal/usagegate`'s `Exhausted()`, which only ever inspects the
  headline meter (see its own doc comment, already written anticipating
  exactly this: "several of the meters a source emits are per-model or
  per-pool... hitting one of those does not block every other model").
- **No menubar/Swift changes.** Confirmed unnecessary per Context §4.
- **No new settings/config.** This is always-on, best-effort, same as the
  existing probe.

## Where this hooks in

`internal/usage/claude.go`'s `Fetch()` (~line 150) currently: reads token
→ checks expiry → gets CLI version → POSTs to `/v1/messages` → parses
headers via `claudeSnapshotFromHeaders` → returns `Snapshot`. Add, after
the primary snapshot is successfully obtained (right before the final
`return snap, nil`):

```go
if extra := c.fetchModelScopedMeters(ctx, token, version); len(extra) > 0 {
    snap.Meters = append(snap.Meters, extra...)
}
return snap, nil
```

New method `(c *Claude) fetchModelScopedMeters(ctx context.Context, token, version string) []Meter`:
builds the GET request to `c.BaseURL+"/api/oauth/usage?at_wall=1&skip_spend=1"`
with a 5s-timeout context derived from `ctx` (`context.WithTimeout`), sets
`Authorization`/`User-Agent`/`Content-Type` headers, does exactly one
`c.HTTP.Do(req)` (or `httpClientOrDefault(c.HTTP)`, matching the existing
`Fetch()` pattern), and on ANY failure (error, non-200, decode failure)
logs via `c.logf` and returns `nil` — never returns an error, since this
helper's whole contract is "best-effort, never breaks the caller."

Response body shape to decode (only the fields needed):
```go
type oauthUsageLimit struct {
    Percent  float64 `json:"percent"`
    ResetsAt string  `json:"resets_at"`
    Scope    *struct {
        Model *struct {
            DisplayName string `json:"display_name"`
        } `json:"model"`
    } `json:"scope"`
}
type oauthUsageResponse struct {
    Limits []oauthUsageLimit `json:"limits"`
}
```

## Verification

New tests in `internal/usage/claude_test.go`:

1. `TestClaudeAddsModelScopedMetersFromOauthUsage` (or similar): a fake
   HTTP server handling BOTH `/v1/messages` (existing `claudeRealHeaders`
   fixture) AND `GET /api/oauth/usage` (return a `limits[]` fixture
   containing one `weekly_scoped`/Fable-shaped entry and one entry with no
   `scope` — e.g. a plain `weekly_all` kind) — assert the returned
   `Snapshot.Meters` includes the Fable-derived meter with the right
   `ID`/`Label`/`UsedPct`/`ResetsAt`, and does NOT include a meter for the
   scope-less entry.
2. `TestClaudeModelScopedFetchFailureDoesNotBreakThePrimarySnapshot`: the
   `/api/oauth/usage` handler returns a 429 (or times out, or 500, or
   malformed JSON — pick at least one) — assert `Fetch()` still succeeds
   and returns the primary 5h/7d meters from headers, with no error and no
   extra meters.
3. `TestClaudeModelScopedFetchNeverRetries`: the `/api/oauth/usage` handler
   counts requests — assert it's hit exactly once per `Fetch()` call, even
   on failure.
4. A resets_at parsing test covering both the fractional-seconds and
   plain-seconds RFC3339 shapes seen in the live capture.
5. Full `go test ./internal/usage/... ./internal/usagegate/...` green, no
   regression to any existing `claude_test.go` test (the existing header
   parsing path must be completely unaffected when the second endpoint
   isn't reachable in a test's fake server — confirm any EXISTING test
   that doesn't set up an `/api/oauth/usage` handler still passes, since
   the fake test server will 404 that path and the new code must treat a
   404 the same as any other failure: silently skip, no error).
6. `go build ./... && go vet ./...` clean, `gofmt -l` empty on touched
   files.

## File list

- `internal/usage/claude.go` — changed (`Fetch()`, new
  `fetchModelScopedMeters` method, new response-shape types).
- `internal/usage/claude_test.go` — changed, new tests added.
- No menubar/Swift files.

## Explicitly out of scope

- Any change to the primary `/v1/messages` header parsing path.
- Any change to `internal/usagegate` (already correctly designed for
  per-model meters, per its own doc comment — nothing to do there).
- Any change to `apps/menubar/` — confirmed unnecessary.
- Re-enabling the plain (non-`at_wall`, non-`skip_spend`) `/api/oauth/usage`
  variant, or the `cedar_ember` variant — only the exact `at_wall=1&skip_spend=1`
  query string this spec verified live.
- Retrying, backing off, or otherwise being clever about this endpoint's
  failure modes — the locked decision is "one attempt, silently skip on
  any failure," full stop.

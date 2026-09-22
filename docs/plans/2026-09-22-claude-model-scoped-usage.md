# Plan: surface Claude's model-scoped (Fable) weekly usage

Spec: `docs/specs/2026-09-22-claude-model-scoped-usage.md`
Worktree: `~/GitHub/agent-swarm--fable-usage`
Branch: `feat/claude-model-scoped-usage`

## Task 0 — read before writing anything

- `internal/usage/claude.go` in full (already read during design, re-read
  for exact current line numbers/shape before editing).
- `internal/usage/claude_test.go` in full — the `claudeRealHeaders`
  fixture and `TestClaudeMapsEveryMeterFromTheRealHeaders` and friends are
  the pattern to match for the new tests' fake-server setup.
- `internal/usage/helpers_test.go` — check for any existing test helper
  (fake HTTP server builder, meter-assertion helper) to reuse rather than
  duplicate.
- Check whether any slug/sanitize helper already exists in `internal/`
  (grep for `func slug(` or similar) before writing a new one for the
  meter ID.

## Task 1 — failing test: model-scoped meter appears

File: `internal/usage/claude_test.go`

Write a test using `httptest.NewServer` with a `http.ServeMux` (or an
`if r.URL.Path == ...` switch, matching whatever pattern Task 0 found) that
handles:
- `POST /v1/messages` → write `claudeRealHeaders(w)` (existing fixture).
- `GET /api/oauth/usage` → assert query string is exactly
  `at_wall=1&skip_spend=1` (fail the test if not, so this locks in the
  spec's required query params), then write a JSON body:
  ```json
  {"limits":[
    {"kind":"weekly_scoped","percent":0,"resets_at":"2026-09-29T17:00:00+00:00",
     "scope":{"model":{"display_name":"Fable"}}},
    {"kind":"weekly_all","percent":2,"resets_at":"2026-09-29T17:00:00.474636+00:00",
     "scope":null}
  ]}
  ```

Call `src.Fetch(ctx)` and assert:
- `snap.Meters` contains a meter with `ID == "model_scoped:fable"`,
  `Label == "Weekly (Fable)"`, `Window == "weekly"`, `UsedPct == 0`, and a
  non-nil `ResetsAt` matching `2026-09-29T17:00:00Z`.
- `snap.Meters` does NOT contain any meter derived from the `weekly_all`
  (scope-null) entry.
- The existing `five_hour`/`seven_day` meters from
  `TestClaudeMapsEveryMeterFromTheRealHeaders` are still present unchanged
  (this new meter is additive, not a replacement).

Run: `go test ./internal/usage/... -run TestClaudeAddsModelScopedMeters -v`
(name it however fits, matching the spec's suggested name). Confirm it
fails — no `/api/oauth/usage` call happens yet, so no such meter exists.

## Task 2 — implement `fetchModelScopedMeters` and wire it into `Fetch`

File: `internal/usage/claude.go`

1. Add the two small response-shape structs from the spec's "Where this
   hooks in" section (`oauthUsageLimit`, `oauthUsageResponse`).
2. Add `func (c *Claude) fetchModelScopedMeters(ctx context.Context, token, version string) []Meter`:
   - Build `ctx, cancel := context.WithTimeout(ctx, 5*time.Second); defer cancel()`.
   - `req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/oauth/usage?at_wall=1&skip_spend=1", nil)` —
     on error, `c.logf(...)` and `return nil`.
   - Headers: `Authorization: Bearer "+token`, `User-Agent: "claude-code/"+version`,
     `Content-Type: application/json`. Do NOT add `anthropic-version` or
     `anthropic-beta` (spec explicitly says the real CLI omits them here).
   - One `resp, err := httpClientOrDefault(c.HTTP).Do(req)` call. On error
     (including a context-deadline timeout), `c.logf(...)` and `return nil`.
     `defer resp.Body.Close()`.
   - If `resp.StatusCode != 200`, `c.logf(...)` and `return nil`. **Do not
     inspect `Retry-After` or do anything with it** — there is no retry
     path here, so there's nothing to schedule.
   - Decode the body into `oauthUsageResponse`; on decode error, `c.logf(...)`
     and `return nil`.
   - For each `limits[]` entry where `l.Scope != nil && l.Scope.Model != nil
     && l.Scope.Model.DisplayName != ""`: parse `l.ResetsAt` (try
     `time.RFC3339` first, fall back to `time.RFC3339Nano` if that fails —
     confirm via Task 1's test which one actually succeeds for both the
     fractional and non-fractional example strings, adjust accordingly),
     build a `Meter{ID: "model_scoped:" + slug(name), Label: "Weekly (" + name + ")",
     Window: "weekly", UsedPct: l.Percent, ResetsAt: &parsed}`.
   - Return the collected slice (may be empty, never nil-vs-empty
     distinction matters here — either is fine since the caller checks
     `len(extra) > 0`).
3. Write the small `slug` helper only if Task 0 found no existing one —
   lowercase + replace runs of non `[a-z0-9]` with `_`, trim leading/
   trailing `_`. Keep it to a few lines; this does not need a general-
   purpose slugify package.
4. In `Fetch()`, right before the final `return snap, nil` on the success
   path (after `claudeSnapshotFromHeaders` succeeds), add:
   ```go
   if extra := c.fetchModelScopedMeters(ctx, token, version); len(extra) > 0 {
       snap.Meters = append(snap.Meters, extra...)
   }
   ```
   Do NOT call this before confirming the primary snapshot succeeded —
   if `claudeSnapshotFromHeaders` returns `ok == false` or the primary
   request errored, `Fetch()` already returns early; the model-scoped call
   must only happen on the success path, after `snap` is built.

Run: `go build ./internal/usage/...` clean.

Run: `go test ./internal/usage/... -run TestClaudeAddsModelScopedMeters -v`
Confirm it now passes.

## Task 3 — remaining spec coverage

Add the other tests from the spec's Verification section:
- `TestClaudeModelScopedFetchFailureDoesNotBreakThePrimarySnapshot` (429,
  or a deliberately slow handler that exceeds the 5s timeout — pick
  whichever is faster to write reliably in a unit test; a slow-handler
  timeout test may need the test to inject a shorter timeout or accept a
  slower test run — consider a 500 response instead of an actual timeout
  if that's simpler and equally valid coverage of "non-200 fails closed").
- `TestClaudeModelScopedFetchNeverRetries` (counter-based handler,
  assert count == 1 after `Fetch()`).
- A resets_at parsing test for both example strings from the spec.
- Confirm an EXISTING test with no `/api/oauth/usage` handler at all
  (a bare `httptest.NewServer` that 404s anything not `/v1/messages`)
  still passes unchanged — if any existing test's fake server setup
  needs adjusting because it now also receives (and 404s) the new second
  request, fix that test's server to handle it gracefully rather than
  leaving a flaky assumption; explain in the report if any pre-existing
  test needed this kind of touch-up.

Run: `go test ./internal/usage/... -v 2>&1 | tail -60` and confirm the
full package's test count/pass rate.

## Task 4 — full verification

1. `go build ./... && go vet ./...`
2. `go test ./internal/usage/... ./internal/usagegate/...`
3. `go test ./...` from the worktree root (if `web/dist` is empty and
   `TestBoardServedAtRoot` 503s, run `cd web && pnpm install
   --frozen-lockfile && pnpm build` first — known fresh-worktree
   environmental gap).
4. `gofmt -l internal/usage/claude.go internal/usage/claude_test.go` —
   must be empty.

## Task 5 — commit

One commit, explicit paths (no `git add -A`):

```
git add internal/usage/claude.go internal/usage/claude_test.go docs/specs/2026-09-22-claude-model-scoped-usage.md docs/plans/2026-09-22-claude-model-scoped-usage.md
git commit -m "feat(usage): surface Claude's model-scoped weekly usage (Fable)

Claude Code's own /status panel shows a distinct \"Current week (Fable)\"
row this daemon never surfaced. Disassembling the installed CLI found
it comes from GET /api/oauth/usage?at_wall=1&skip_spend=1's limits[]
array, not the unified rate-limit headers this package already polls --
a different endpoint than the one this package's own doc comment
documents as abandoned for being punitive on retry, and specifically
using the skip_spend=1 param that prior investigation never tried. Live
single-call verification (user-approved) confirmed it returns cleanly.
Added as a second, best-effort, single-attempt call inside Fetch():
any failure silently drops the extra meters without affecting the
primary 5h/7d snapshot. No menubar changes needed -- UsageSection.rows()
already renders meters generically."
```

## Report back

Summarize: files changed with a one-line diff summary each, full test
output (pass/fail counts) for `go test ./internal/usage/... ./internal/usagegate/...`
and `go test ./...`, the exact commit hash, which RFC3339 parsing approach
actually worked for both example timestamp shapes, and any deviation from
this plan with why.

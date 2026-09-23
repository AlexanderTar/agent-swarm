# Plan: muse usage source over MSP

Spec: `docs/specs/2026-09-23-muse-usage-source.md`.
Worktree: `~/GitHub/agent-swarm--muse-usage-source`, branch
`feat/muse-usage-source`. Commit after each task.

## Task 1 — scripted MSP host fixture + happy path (RED → GREEN)

**Produces:** `fakeMuseHost(t, script map[string][]string) execx.Starter` in
`internal/usage/muse_test.go`, plus `TestMuseFetchMapsSubscriptionUsage`.

The fixture must be *stateful*, unlike `fakeAppServer`: the source only sends
`turn/start` after `session/start` is answered, so a dumb playback would
deadlock. Read each request line from Stdin, write the scripted reply keyed
by its `method`, substituting the request's `id`.

Assertions: two meters — `{ID:"5h", Label:"5h", Window:"5h", UsedPct:0,
ResetsAt: time.UnixMilli(1790208865000)}` and `{ID:"weekly", Label:"Weekly",
Window:"weekly", UsedPct:9, ResetsAt: time.UnixMilli(1790553600000)}`;
headline `"5h"`.

Run `go test ./internal/usage/ -run TestMuse` → fails (no `Muse` type).

**Then:** implement `Muse` in `internal/usage/muse.go`:

```go
type museUsageWindow struct {
    UsedPercent        float64 `json:"usedPercent"`
    WindowDurationMins int     `json:"windowDurationMins"`
    ResetsAtMs         int64   `json:"resetsAtMs"`
}
type museSubscriptionUsage struct {
    Window museUsageWindow `json:"window"`
    Weekly museUsageWindow `json:"weekly"`   // no windowDurationMins
    Tier   string          `json:"tier"`
}
type museRPCEnvelope struct {
    ID     *int            `json:"id"`
    Method string          `json:"method"`
    Result json.RawMessage `json:"result"`
    Params json.RawMessage `json:"params"`
    Error  *struct{ Code int; Message string } `json:"error"`
}
```

Flow inside `probe`:
`initialize` (id 1, `clientInfo{name:"swarm", version:"0.0.0"}`) → wait for
id 1 → send `initialized` notification (`"params": {}`) → `session/start`
(id 2, `commandId`+`sessionId` from `uuid.NewV7()`, `workspaceRoot: m.Dir`)
→ wait id 2 → `turn/start` (id 3, `reasoningEffort:"minimal"`,
`input:[{type:"text",text:"ok"}]`) → wait id 3 → poll `usage/read` every
2 s until its `usage` member appears, or the deadline (Locked decision 6:
`usage/changed` is not reliably delivered, `usage/read` is).

Mapping mirrors `codexSnapshotFromRPC`: `codexWindow(300)` → `"5h","5h"`;
weekly is hard-coded `"Weekly","weekly"` because it has no duration.

Run → green. Commit `feat(usage): read muse subscription quota over MSP`.

## Task 2 — handshake name guard (RED → GREEN)

`TestMuseHandshakeUsesAWireLegalClientName`: capture every line the source
writes to Stdin, assert the `initialize` params' `clientInfo.name` matches
`^[a-z0-9_]+$`, and that an `initialized` notification with object params is
sent after the `initialize` response and before `session/start`.

This pins the silent-failure mode found in the probe. Commit.

## Task 3 — truthful failures (RED → GREEN)

- `TestMuseFetchErrsWhenNoUsageIsObserved`: script `turn/completed` with no
  `usage/changed` → `Fetch` returns an error mentioning "no subscription
  usage" and no meters.
- `TestMuseFetchTimesOutAndKillsTheHost`: fixture that answers the handshake
  and then goes silent; `Timeout: 50 * time.Millisecond`; assert error and
  that `Kill` ran.

Commit `feat(usage): fail honestly when muse observes no quota`.

## Task 4 — probe gap cache (RED → GREEN)

- `TestMuseFetchServesTheCacheInsideTheProbeGap`: `Now` from the package's
  `clk`; first `Fetch` spawns (count starts at 1), advance 5 min, second
  `Fetch` returns identical meters with the spawn count still 1.
- `TestMuseFetchProbesAgainAfterTheGap`: advance 16 min → count 2.

Implement with `m.mu`, `m.cached`, `m.at`; `probeGap()` defaults to 15 min,
`now()` defaults to `time.Now`. A failed probe must not poison the cache: on
error, return the error and leave `cached`/`at` alone (so the next poll
retries rather than serving a stale answer for 15 min).

Commit `feat(usage): cap muse quota probes to one per 15 minutes`.

## Task 5 — wire into DefaultSources

- `internal/usage/usage.go`: build
  `museSrc := &Muse{Start: start, Dir: userHome, Now: time.Now}` and append
  `{Agent: runtime.Muse, Fetch: museSrc.Fetch}`.
- Rewrite the "muse is deliberately absent" paragraph of `DefaultSources`'
  doc comment: what the source is, that each fresh observation spends one
  minimal turn, the 15-minute gap, and a pointer to the probe report.
- `internal/usage/poller_test.go`: the four-entries assertion → five.
- `cmd/swarm/runtime_cmds.go`: drop "e.g. muse has no quota endpoint" from
  the `no usage source` comment (the branch stays — it is generic).
- `go mod tidy` to promote `github.com/google/uuid` to a direct require.

Run `go test ./... && go vet ./...`. Commit
`feat(usage): register the muse source in DefaultSources`.

## Task 6 — verify

`go build ./... && go vet ./... && go test ./internal/usage/... ./cmd/swarm/...`
Report the worktree path, branch and commits. Do not merge, push, or touch
the primary checkout.

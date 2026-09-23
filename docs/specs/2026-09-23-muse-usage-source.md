# Muse usage source: read real quota percentages over MSP

Date: 2026-09-23
Branch: `feat/muse-usage-source`
Worktree: `~/GitHub/agent-swarm--muse-usage-source`
Evidence: `docs/specs/2026-09-23-muse-usage-probe.md` (main checkout) — every
command, string and live capture this spec rests on.

## Context

`swarm usage` shows real quota bars for claude/codex/agy/cursor and prints
"no usage source" for muse. A previous investigation concluded no source
existed because `muse --help` has no quota subcommand. That is true of the
subcommand surface and false as a conclusion: Muse Code 1.3.0 serves a
**Muse Session Protocol (MSP)** host over stdio (`muse serve`) whose
`usage/read` method returns real subscription percentages, and whose
`usage/changed` notification carries the same payload. Live capture from this
machine:

```json
{"window": {"usedPercent": 0, "windowDurationMins": 300, "resetsAtMs": 1790208865000},
 "weekly": {"usedPercent": 9, "resetsAtMs": 1790553600000},
 "tier": "27681393394859588",
 "observedAtMs": 1790191416036}
```

The protocol shape is nearly identical to `codex app-server`'s
`account/rateLimits/read` (same `usedPercent` / `windowDurationMins` /
`resetsAt` triple), so `internal/usage/codex.go`'s `fetchViaAppServer` is the
template.

**The catch:** a freshly spawned MSP host has observed nothing and returns
`{}`. The observation only arrives with a provider response frame — i.e.
after one model call. So each *fresh* observation costs one minimal-effort
muse turn.

Collisions/caveats:
- `internal/usage/muse.go` already holds `MuseUsage`/`ParseMuseExport`/
  `MuseSessionUsage` (per-session token counts from `muse export`). Different
  purpose, kept untouched.
- `Poller.fetchMu` serializes all sources, so a slow muse probe delays other
  sources' manual refresh for the probe's duration. Out of scope; capped by
  the timeout below.

## Locked decisions

1. **Source = `muse serve` over stdio**, not a direct `api.meta.ai` call.
   Going direct would mean re-implementing the OAuth→API-key "mint" flow that
   lives inside the binary. `muse serve` uses the CLI's own credentials.
2. **Each fresh observation spends one muse turn.** Prompt: `"ok"`,
   `reasoningEffort: "minimal"`, host started `--no-session-log
   --disable-shell --disable-write`. This is stated plainly in the source's
   doc comment. It is not free and must not be described as free.
3. **Probe gap = 15 minutes**, source-internal. Inside the gap `Fetch`
   returns the cached observation instead of spawning a host. 15 min is the
   poller's own `staleAfter` at the default `UsagePollSec = 300`, so a cached
   answer never reads as stale. Cost at default cadence: **96 turns/day**,
   down from 288.
   Returning the cached observation is honest: MSP's own `usage/read` does
   exactly this — it replays the last observation and never re-fetches.
   `fetched_at` therefore means "last confirmed", and the data is at most
   `gap + one poll` old.
4. **Off switches, unchanged:** sources exist only when `SWARM_USAGE=live`
   (`SourcesFromEnv`), and the poll loop skips any kind absent from
   `Settings.EnabledAgents`.
5. **No usage observed after the turn completes is an error**, not an empty
   success — that is the PAYG / `META_API_KEY` case where the provider sends
   no subscription frames. The poller keeps the previous meters and shows the
   error, which is the truthful outcome.
6. **The probe polls `usage/read`; it does not wait on `usage/changed`.**
   Verified live 2026-09-23: a host driven from Go answers `usage/read`
   correctly (`usedPercent` climbing 0 -> 1 -> 3 across probes) while never
   delivering the `usage/changed` notification on that connection, so a
   notification wait hangs to the deadline. `usage/read` is also the
   documented read and makes no model call of its own.
7. **`clientInfo.name` is `"swarm"`.** MSP requires `^[a-z0-9_]+$`; a hyphen
   makes the handshake fail *silently* (the `initialize` response still
   arrives, then everything answers `notInitialized`). Pinned by a test.
8. **Timeout 60 s.** The observation arrived ~18 s into the live probe.
9. `windowDurationMins: 300` reuses the existing `codexWindow` mapping →
   label `5h`, window `5h`. Weekly → label `Weekly`, window `weekly`.
   Headline is the 5h meter, matching `codexSnapshotFromRPC`.

## API types

MSP wire (exported from the binary, fingerprint
`sha256:7469c9e352e67def4a59df7e439984d7194fa351e1c8b7abb34060fd977ced81`):

```
initialize(params: {clientInfo:{name,version}}) -> InitializeResult
initialized                                      (client notification, object params)
session/start(params: {commandId: uuidv7, sessionId?: uuidv7, workspaceRoot?}) -> {session:{...}}
turn/start(params: {commandId: uuidv7, sessionId, reasoningEffort?, input:[{type:"text",text}]})
usage/changed(params: SubscriptionUsage)         (server notification)
usage/read() -> {usage?: SubscriptionUsage}      (no params)

SubscriptionUsage {
  window: {usedPercent:int, windowDurationMins:int, resetsAtMs:int}
  weekly: {usedPercent:int, resetsAtMs:int}
  tier: string
  observedAtMs: int
}
```

Go (new, in `internal/usage/muse.go`):

```go
type Muse struct {
    Start    execx.Starter
    Dir      string        // workspaceRoot for the probe session
    Timeout  time.Duration // default 60s
    ProbeGap time.Duration // default 15m
    Now      func() time.Time

    mu       sync.Mutex
    cached   []Meter
    headline string
    at       time.Time
}

func (m *Muse) Fetch(ctx context.Context) ([]Meter, string, error)
```

`Fetch` matches `Source.Fetch` exactly, so `DefaultSources` gains a fifth
entry with no interface change.

## File list

Changed:
- `internal/usage/muse.go` — add `Muse` source below the existing export
  helpers (untouched).
- `internal/usage/muse_test.go` — add tests for the new source.
- `internal/usage/usage.go` — `DefaultSources` builds and returns the muse
  source; rewrite the "muse is deliberately absent" paragraph.
- `internal/usage/poller_test.go` — the "four entries" assertion becomes five.
- `cmd/swarm/runtime_cmds.go` — the `no usage source` comment no longer cites
  muse as the example.
- `go.mod` — `github.com/google/uuid` moves from indirect to direct.

Reused unchanged: `execx.Starter`/`execx.Proc`, `codexWindow`, `Meter`,
`Poller`, `recordAttemptOnly` (still the path for any kind with no source).

Deleted: nothing. `MuseUsage`, `ParseMuseExport`, `MuseSessionUsage` and
their tests stay.

## Verification

```
cd ~/GitHub/agent-swarm--muse-usage-source
go test ./internal/usage/... ./cmd/swarm/...
go vet ./...
go build ./...
```

Scenarios covered by tests (scripted `execx.Starter`, no real muse):
1. Happy path — handshake, turn, `usage/read` → two meters (5h 0%, weekly
   9%), headline `5h`, reset stamps from `resetsAtMs`.
2. `clientInfo.name` written to the host matches `^[a-z0-9_]+$`.
3. Host keeps answering `usage/read` with `{}` → error naming the missing
   usage, no meters.
4. Host writes nothing → timeout error and `Kill` ran.
5. Second `Fetch` inside `ProbeGap` returns the cached meters and does **not**
   start a process.
6. Second `Fetch` after `ProbeGap` starts a process again.
7. `DefaultSources` returns five sources.

Live end-to-end (gated, spends one real turn) — run and passing 2026-09-23:
```
MUSE_LIVE_PROBE=1 go test ./internal/usage/ -run TestMuseFetchLive -v
    muse_test.go:372: 5h 5h 3% resets 2026-09-24 00:14:25 +0000 UTC
    muse_test.go:372: weekly weekly 9% resets 2026-09-28 00:00:00 +0000 UTC
--- PASS: TestMuseFetchLive (12.66s)
```

## Out of scope

- Fixing `Poller.fetchMu` so one slow source cannot delay others.
- Any direct `api.meta.ai` call or API-key mint.
- Surfacing `tier` or `observedAtMs` in the UI (`Meter` has no field for
  either).
- Using the per-session token export (`MuseSessionUsage`) for anything.
- Menubar/web UI changes — they render whatever `/api/usage` returns.

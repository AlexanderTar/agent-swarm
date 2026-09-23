# Muse usage/quota source — deep probe (re-investigation)

Date: 2026-09-23
Binary: `Muse Code 1.3.0 (1.3.0-R3401.1)`, `/Users/alexandertar/.local/bin/muse-bin-1.3.0-R3401.1` (Mach-O arm64, 288 MB)
Launcher: `/Users/alexandertar/.local/bin/muse` (bash script)

## VERDICT: (a) A REAL USAGE/QUOTA SOURCE EXISTS

Muse exposes subscription quota **percentages** over its own **Muse Session
Protocol (MSP)** host, via the `usage/read` method and the `usage/changed`
notification. Live-captured from this machine:

```json
{"window": {"usedPercent": 0, "windowDurationMins": 300, "resetsAtMs": 1790208865000},
 "weekly": {"usedPercent": 9, "resetsAtMs": 1790553600000},
 "tier": "27681393394859588",
 "observedAtMs": 1790191416036}
```

That is exactly the `Meter{UsedPct, Window, ResetsAt}` shape the other four
sources produce — a 5-hour-class window (300 min) and a rolling weekly
window, both as integer percent-used with epoch-ms reset stamps. It is
**not** a token count divided by a guessed limit; the percentages come
verbatim from the provider.

**The cost caveat (load-bearing):** the MSP host only *observes* the numbers
when a provider frame arrives, i.e. after at least one model call. A fresh
host with no turn returns `{"usage": …}` omitted. So each fresh observation
costs one minimal-effort muse turn. See "Cost" below.

The previous agent's conclusion ("`muse --help` has no quota subcommand, so
there is no source") is **correct about the CLI subcommand surface and wrong
about the conclusion** — the surface it missed is the MSP wire protocol that
`muse serve` speaks, which the binary itself documents and exports.

---

## 1. Full CLI surface

### `muse --version`

```
Muse Code 1.3.0 (1.3.0-R3401.1)
```

### `muse --help` — commands

```
Commands:
  resume           Resume a previous session (--last or <session-ref>)
  exec             Run one prompt non-interactively (headless)
  config           Validate enterprise configuration documents
  export           Export a session transcript to a file
  trace            Inspect a recorded session or run trace
  skills           List, inspect, enable, or disable skills
  plugins          Validate and manage plugin bundles
  sandbox          Check or set up the OS sandbox
  schema           Export the MSP wire schema (JSON Schema or TypeScript)
  serve            Serve an MSP session host over stdio
  session-message  List or send cross-session messages
  mcp              Log in to or out of an MCP server (OAuth)
  auth             Store provider API credentials
  login            Log in to a provider
  logout           Remove stored provider credentials
  init             Scaffold agent config in this workspace
```

No `usage`/`quota`/`billing`/`status` subcommand — confirmed. **But `schema`
and `serve` are the two that matter.**

### Unlisted subcommands probed directly

```
$ for c in usage status quota limits billing whoami account doctor info rate-limit subscription upgrade; do muse $c; done
```

Every one returned the same message:

```
workspace trust for /private/tmp is undecided and no interactive terminal is
available to ask; pass --trust-workspace to trust it for this run, or start
once from an interactive terminal to record the decision
```

That is the *session-start* error: unrecognised words are treated as a
**prompt**, not as a command. So there is no hidden subcommand.

### `muse config status`

```
Enterprise configuration status
Generation: sha256:eea7ec0993077bcb989b02686b2f3f96a12ed650d4f7f20ff3bd4235c9c4e50c
Sources:
  plane=defaults source_class=system_file state=absent
  plane=policy source_class=system_file state=absent
  plane=defaults source_class=macos_managed_preferences state=absent
  plane=policy source_class=macos_managed_preferences state=absent
```

No usage data.

### `muse login --help`

```
usage: muse login

Log in with your Meta account: approve a code in your browser.
META_API_KEY always takes priority over the account login.
```

### `muse schema --help` — the lead

```
muse schema — export the MSP wire schema embedded in this binary

Usage: muse schema generate-json-schema --out DIR [--experimental]
       muse schema generate-ts --out DIR [--experimental]

Both exports are offline and instant: the bundles are precomputed at build
time, so the output is exact for this binary.
```

### `muse serve --help`

```
muse serve — serve an MSP session host over stdio

The client owns this process's stdin and stdout and is its only connection.
```

---

## 2. Installation internals

```
$ which muse
/Users/alexandertar/.local/bin/muse
$ file $(which muse)
Bourne-Again shell script text executable, ASCII text
```

The launcher downloads and execs the real binary:

```bash
channel="${MUSE_CHANNEL:-muse-stable}"
channel_url="https://api.meta.ai/muse-code/channels/$channel"
auth_url="https://auth.meta.com"
client_id="1031625952748946"
download_host="lookaside.facebook.com"
```

`grep -nE 'quota|usage|limit|billing|subscription|rate_limit|entitle'` on the
launcher: **no matches.**

Real binary: `/Users/alexandertar/.local/bin/muse-bin-1.3.0-R3401.1`.

### Strings that broke the case open

`strings -n 6 muse-bin-1.3.0-R3401.1 | grep -i quota`:

```
Percent of the window's budget used: an integer ≥ 0, verbatim from the
provider — over-quota values above 100 are valid.usedPercentThe window's
length in minutes (> 0).windowDurationMinsWhen the window resets, epoch
milliseconds.resetsAtMsThe current usage window (the provider's 5-hour-class
block): verbatim provider percentages with the reset stamp normalized to
epoch milliseconds (ADR 32563 D2).
```

Adjacent type names in the same blob:

```
SubscriptionUsageWindow usedPercent windowDurationMins resetsAtMs
SubscriptionUsageWeekly SubscriptionUsage window weekly tier observedAtMs
UsageReadResult usage
```

Provider-side (snake_case, the Meta API SSE shape):

```
struct SubscriptionUsageSnapshot with 3 elements
struct SubscriptionWeeklySnapshot with 2 elements
struct SubscriptionUsageEvent
SubscriptionUsageSnapshot window weekly tier
SubscriptionWeeklySnapshot used_percent resets_at
subscription_usage_frame_discarded: out-of-domain frame ignored
```

Other relevant strings: `https://api.meta.ai/v1`, `https://auth.meta.com`,
`https://accountscenter.meta.com/muse_code/?ep=no_payg`, `mint returned an
empty api key`, `PaymentBannerShownRecord … payment_tier`,
`tbh_tui::subscription_stamp tier_id`, `/usage` (TUI slash command:
"`/usage` shows this session's token usage").

---

## 3. The MSP schema (authoritative, exported from the binary)

```
$ muse schema generate-json-schema --out <dir>
wrote manifest.json, msp.schema.json to <dir> (stable surface)
$ cat manifest.json
{"experimental": false,
 "fingerprint": "sha256:7469c9e352e67def4a59df7e439984d7194fa351e1c8b7abb34060fd977ced81",
 "schemaVersion": 1}
```

### `methods["usage/read"]`

```json
{
 "description": "Reads the host's last-observed subscription usage window (5h-class and weekly percent blocks, tier, arrival stamp) without a model call; the usage member is omitted when nothing has been observed (ADR 32563 D2).",
 "result": {"$ref": "#/$defs/UsageReadResult"}
}
```

**No `params`** — it is host-global, not per session. (This is the exact
objection the previous agent raised against wiring muse in: "no session-id
parameter in `Source.Fetch`". It does not apply to `usage/read`.)

### `notifications["usage/changed"]`

```json
{
 "description": "The host's last-observed subscription usage DATA changed (window, weekly, or tier — not a stamp-only refresh), with the same payload shape as usage/read's usage member; the absent-to-present first observation emits (ADR 32563 D3).",
 "params": {"$ref": "#/$defs/SubscriptionUsage"}
}
```

### `$defs`

```json
"SubscriptionUsageWindow": {
 "description": "The current usage window (the provider's 5-hour-class block): verbatim provider percentages with the reset stamp normalized to epoch milliseconds (ADR 32563 D2).",
 "properties": {
  "resetsAtMs": {"type": "integer"},
  "usedPercent": {"description": "Percent of the window's budget used: an integer ≥ 0, verbatim from the provider — over-quota values above 100 are valid.", "type": "integer"},
  "windowDurationMins": {"description": "The window's length in minutes (> 0).", "type": "integer"}
 },
 "required": ["resetsAtMs", "usedPercent", "windowDurationMins"]
}
"SubscriptionUsageWeekly": {
 "properties": {"resetsAtMs": {"type": "integer"},
                "usedPercent": {"description": "Percent of the weekly budget used (integer ≥ 0, may exceed 100).", "type": "integer"}},
 "required": ["resetsAtMs", "usedPercent"]
}
"SubscriptionUsage": {
 "description": "The one usage payload shape: the `usage/read` result's `usage` member and the `usage/changed` params (ADR 32563 D2/D3). The numbers are point-in-time — `observedAtMs` is the host's arrival stamp, so a client renders \"as of\", never implies live data (spec 18742 US-FR-004).",
 "properties": {"observedAtMs": {"type":"integer"}, "tier": {"type":"string"},
                "weekly": {"$ref": "#/$defs/SubscriptionUsageWeekly"},
                "window": {"$ref": "#/$defs/SubscriptionUsageWindow"}},
 "required": ["observedAtMs", "tier", "weekly", "window"]
}
"UsageReadResult": {
 "description": "`usage/read` result: `{usage?}` — omitted, never `null`, when the host has observed nothing (ADR 32563 D2: truthful absence, no \"nothing observed\" error).",
 "properties": {"usage": {"$ref": "#/$defs/SubscriptionUsage"}}
}
```

---

## 4. Live captures against `muse serve`

Wire: newline-delimited JSON-RPC 2.0 on stdio.

### 4a. Handshake gotchas (both cost a probe iteration)

- `usage/read` before the handshake completes → `{"code":-32600,"message":"Not initialized","data":{"kind":"notInitialized"}}`.
- The handshake needs `initialize` **and** the `initialized` notification.
- `clientInfo.name` must match `^[a-z0-9_]+$`. With `"swarm-probe"` (hyphen)
  the `initialize` request still returns a result, but the `initialized`
  notification is silently dropped —
  `tbh-session-wire: dropping pre-handshake notification initialized (FR-008)`
  on stderr — and every subsequent call answers `notInitialized`.
  With `"swarm_probe"` everything works.

### 4b. `initialize` result

```json
{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"muse","version":"1.3.0"},
 "userAgent":"muse-build/1.3.0 (non-interactive; macos-aarch64; build 3c572bc7348890f3f5cb4f24b7efb04bc3d31c09)",
 "museHome":"/Users/alexandertar/.local/share/muse","platformFamily":"unix","platformOs":"macos",
 "schema":{"version":1,"fingerprint":"sha256:7469c9e352e67def4a59df7e439984d7194fa351e1c8b7abb34060fd977ced81"},
 "grantedCapabilities":[],"experimentalApi":false,"sessionDurability":"ephemeral"}}
```

### 4c. `usage/read` on a fresh host — truthful absence

```json
{"jsonrpc":"2.0","id":2,"result":{}}
```

Still `{}` after `session/start` alone (session created, no turn), sampled
three times over 6 s:

```json
{"jsonrpc":"2.0","id":10,"result":{}}
{"jsonrpc":"2.0","id":11,"result":{}}
{"jsonrpc":"2.0","id":12,"result":{}}
```

### 4d. After ONE minimal turn — the real numbers

`session/start` → `turn/start` with `reasoningEffort: "minimal"` and input
`"Reply with the single word: ok"`:

```
TURNSTART: {"jsonrpc":"2.0","id":3,"result":{"commandId":"01a0cfb8-eece-7118-b88e-3c09a27de61b","status":"accepted","turnId":"01a0cfb8-eece-7118-b88e-3c09a27de61b","startedNewTurn":true,"disposition":"started"}}

USAGE/CHANGED: {"window":{"usedPercent":0,"windowDurationMins":300,"resetsAtMs":1790208865000},
                "weekly":{"usedPercent":9,"resetsAtMs":1790553600000},
                "tier":"27681393394859588","observedAtMs":1790191416036}

USAGE/READ: {"jsonrpc":"2.0","id":20,"result":{"usage":{"window":{"usedPercent":0,"windowDurationMins":300,"resetsAtMs":1790208865000},"weekly":{"usedPercent":9,"resetsAtMs":1790553600000},"tier":"27681393394859588","observedAtMs":1790191416036}}}
USAGE/READ: {"jsonrpc":"2.0","id":21,"result":{"usage":{...identical...}}}
USAGE/READ: {"jsonrpc":"2.0","id":22,"result":{"usage":{...identical...}}}
```

`windowDurationMins: 300` = the 5-hour block. `resetsAtMs 1790208865000` =
2026-09-24T00:14:25Z. `weekly.resetsAtMs 1790553600000` =
2026-09-28T00:00:00Z.

Note `usage/read` returns the **same** last observation repeatedly and never
re-fetches — its own contract is "last observed", not "live".

### 4e. `usage/changed` is not reliably delivered — poll `usage/read` instead

The same sequence driven from Go (`exec.Cmd` pipes instead of Python's
`subprocess`) never received a `usage/changed` notification: the turn was
accepted and then the stream stayed silent for 90–120 s, while other
notifications (`session/started`) *did* arrive. Adding a `usage/read` poll to
the same connection showed the observation landing normally:

```
>> {"id":110,"jsonrpc":"2.0","method":"usage/read"}
<< {"jsonrpc":"2.0","id":110,"result":{"usage":{"window":{"usedPercent":1,"windowDurationMins":300,
   "resetsAtMs":1790208865000},"weekly":{"usedPercent":9,"resetsAtMs":1790553600000},
   "tier":"27681393394859588","observedAtMs":1790193023791}}}
```

`usedPercent` climbed 0 → 1 → 3 across successive probes, i.e. the turns were
real and the numbers move. So the wireable read is **`usage/read`, polled**,
not the notification. Root cause of the delivery gap not chased further —
the poll is the documented read and needs no notification at all.

---

## 5. Local data and config files — everything read, nothing found

### `find ~/.local/share/muse -type f` (510 files)

```
~/.local/share/muse/session-index.db        (+ -wal, -shm)
~/.local/share/muse/tui-history.jsonl
~/.local/share/muse/feature-config/7075626c6963.json
~/.local/share/muse/model-catalog/6d657461__p746268.json
~/.local/share/muse/plugins/installed.json
~/.local/share/muse/local-tracing/bootstrap/cli-*.log   (many)
~/.local/share/muse/runtime/muse/ms-*.sock + .lease     (session-mailbox sockets)
~/.local/share/muse/sessions/2026/09/23/<uuid>/session.jsonl …
```

**`session-index.db`** (`sqlite3 .schema`) — tables `schema_meta` and
`sessions`. Columns: session_id, session_stream_id, session_dir,
session_log_path, layout, workspace_root, workspace_key, provider_id,
model_id, git_branch, title, …, `prompt_count`, status, …, `msp_turn_count`,
… **No usage, quota, token or percentage column.**

**`feature-config/7075626c6963.json`** (hex "public"):

```json
{"schema_version": 1, "ttl_seconds": 3600,
 "gates": {"local_session_messaging": true, "monitor": false, "plugins": true,
           "subscription_launch": true, "subscription_upsell": true, "tag": false,
           "voice": true, "voice_default_on": true, "web_fetch": true,
           "workflow_api_v2_rollout": false, "workflow_tool": true},
 "killed_slash_commands": []}
```

Subscription *gates*, no subscription *numbers*.

**`model-catalog/6d657461__p746268.json`** — `context_limit: 1007997`,
`output_limit: 128000`, `cost: null` per model. Per-request context sizes,
not an account quota. (`cost` is null, so not even a price to multiply.)

**`~/.config/muse/settings.json`** — read in full. `schema_version`,
`provider: "meta"`, `model`, `reasoning_effort`, `tui`, `mcpServers`,
`runtime_capabilities`. No account id, no quota, no API base URL.

**`~/.config/muse/auth.json`** — structure only (values withheld):

```
/schema_version = 2
/providers/meta/mechanism = oauth
/providers/meta/storage = keychain
/providers/meta/obtained_via = device_code
/providers/meta/api_base_url = https://api.meta.ai/v1
/providers/meta/user_full_name, /providers/meta/user_email
```

The token itself is in the macOS Keychain; the file carries no usage data.

### Session logs

`grep -rl 'usedPercent\|used_percent' ~/.local/share/muse/sessions` → **no
matches.** A full key census across every `session.jsonl` turns up only:

```
1072  .payload.event.record.quantity.{input,output,cached,reasoning}_tokens
 639  .payload.event.usage
 520  .payload.event.usage.{input,output,cached,reasoning}_tokens
 516  .payload.event.usage.{cache_write,cache_read}_tokens
 250  .payload.event.estimated_tokens
 119  .payload.event.usage.{rss_self_bytes,cpu_self_ms,fds_open,…}   ← process metrics
```

Raw token counters only — exactly what `ParseMuseExport` already reads. **No
percentage, no limit, no reset stamp anywhere on disk.**

### `runtime/muse/ms-*.sock` — not an MSP host

```
$ cat ~/.local/share/muse/runtime/muse/ms-161ccdc9f061.sock.lease
{"schema_version":1,"endpoint_hint":"ms-161ccdc9f061.sock","process_generation_hint":"pid=87333","pid":87333}
```

These are the **session-mailbox** sockets behind `muse session-message`
(`tbh.local.session_mailbox` in the binary), not MSP hosts. No free
usage read from a running TUI via this path.

### `muse export --last --redacted` — run for real, full structure

```
$ muse export --last --redacted --out <scratch>/export.json
wrote session export to <scratch>/export.json

$ python3 -c "print(sorted(json.load(open('export.json')).keys()))"
['diagnostics', 'events', 'export_schema_version', 'exporter_version',
 'redaction', 'session_build', 'session_terminated_abnormally', 'sessions']

$ grep -io 'subscription[a-z_]*\|used_percent\|usedPercent\|resets_at\|quota' export.json | sort | uniq -c
(no output)
```

Zero quota-shaped fields in the whole export document — not just in the
subset `ParseMuseExport` reads. A live `muse exec --json` run (31 records,
`run.terminal.completed` with `text: "ok"`) matched nothing either.

So the export path is a genuine dead end for quota, as previously concluded.

---

## 6. Network / API angle

The binary's own provider client posts to `https://api.meta.ai/v1` and the
usage numbers ride in on a `response.subscription_usage` SSE frame of the
streaming response (the `SubscriptionUsageEvent` struct above;
`subscription_usage_frame_discarded` is its reject path). The only `/v1/…`
literal paths in the binary are `/v1/asr/duplex`, `/v1/logs`, `/v1/traces`,
`/v1/metrics` — **there is no standalone account/quota REST endpoint.**

No unauthenticated probing was attempted. Talking to `api.meta.ai` directly
would also require re-implementing the OAuth→API-key "mint" flow the binary
performs (`mint returned an empty api key`), which is exactly the
invent-an-auth-flow the task forbids. Going through `muse serve` uses the
CLI's own credentials and its own supported protocol.

---

## 7. Web research

- **Meta's own subscription doc**
  (https://dev.meta.ai/docs/muse-code/subscriptions) documents plans,
  upgrade/downgrade and billing cycles and **no** programmatic usage read —
  the only CLI mention is "`/upgrade` to open Accounts Center in your
  browser".
- **Third-party quota trackers** independently reached the same conclusion
  and the same workaround. L-K-M/LLimit PR #32 ("Add Meta Muse (Muse Code) as
  a quota provider"): *"Muse Code has no standalone usage endpoint, so the
  client makes one minimal streaming probe to POST /v1/responses and parses
  the `response.subscription_usage` SSE event — the same window/weekly
  snapshot the CLI's `/usage` panel renders."* It names
  `SubscriptionWindowSnapshot{window_duration_mins, used_percent, resets_at}`
  and `SubscriptionWeeklySnapshot{used_percent, resets_at}` — the exact
  structs found in this binary's strings.
- alondero/buildmesh issue #1678 states flatly that *"Meta does not expose an
  account-level quota API or CLI subcommand"* — true for REST/CLI, and the
  reason the MSP route is the one that works.

Sources:
- [Subscriptions — Meta Model API](https://dev.meta.ai/docs/muse-code/subscriptions)
- [L-K-M/LLimit PR #32](https://github.com/L-K-M/LLimit/pull/32)
- [alondero/buildmesh issue #1678](https://github.com/alondero/buildmesh/issues/1678)

---

## 8. Cost — the one thing that must not be glossed

`usage/read` itself is free and makes no model call. But a **fresh** host has
observed nothing, and the observation only arrives with a provider response
frame. So:

- Reading quota from a newly spawned `muse serve` costs **one minimal-effort
  turn** (a tiny prompt, `reasoningEffort: "minimal"`, shell and writes
  disabled).
- That turn itself consumes a sliver of the quota being measured.
- At the poller's default `UsagePollSec = 300`, a naive wiring would spend
  288 turns/day purely on measurement. Unacceptable.

Side effect worth knowing: the `muse` launcher re-checks
`https://api.meta.ai/muse-code/channels/$MUSE_CHANNEL` every
`MUSE_UPDATE_INTERVAL_SECONDS` (default 3600), so a probe can trigger a
release download. Noted, not solved here.

The mitigation that stays honest is to mirror `usage/read`'s own contract:
probe at most once per long interval and otherwise serve the **last
observation**, which is literally what MSP itself returns between frames.
`observedAtMs` is the truth stamp; the snapshot is "as of", never "live".

## 9. What is NOT a source (recorded so it is not re-litigated)

- `muse export` / `session.jsonl` token counters — raw tokens, no limit to
  divide by. Not a percentage. Already correctly rejected.
- `model-catalog` `context_limit` — a per-request context size, not an
  account quota.
- `session-index.db` — no usage columns.
- Any `muse <word>` guess — those are prompts, not commands.

## 10. Follow-up

Wireable. Spec and plan: `docs/specs/2026-09-23-muse-usage-source.md` and
`docs/plans/2026-09-23-muse-usage-source.md` (branch
`feat/muse-usage-source`).

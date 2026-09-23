# Muse as first-class agent + superpowers remediation

Date: 2026-09-23. Owner decision: approach A (one vertical slice, dependency order).

## 1. Context

agent-swarm spawns four agent kinds (claude, codex, agy, cursor) through a uniform
seam: `kinds.AgentKind` → `adapter.Adapter` → `catalog.Fetcher` → `install` wiring
(skills, MCP, plugins) → web UI picker. Muse Code (`muse` CLI v1.3.0, Meta provider)
is installed and authenticated on this machine but swarm knows nothing about it.

A parallel research session ("Superpowers skills remediation", Claude) found three
superpowers gaps: (a) delivery — plugin reach to spawned sessions unverified for
non-claude kinds; (b) instruction correctness — `skills/swarm-orchestrator/SKILL.md:45`
tells agents to write specs/plans under `~/.superpowers/` and "never write them into
a repo", contradicting repo `CLAUDE.md` (`docs/specs/`, `docs/plans/`); (c) enforcement —
superpowers use is advisory text only. This spec covers both the muse slice and the
remediation, since muse delivery must not repeat the gaps.

Probes already run (read-only, no paid calls): muse has no `models` subcommand and
accepts unknown `--model` ids silently; the dynamic model source is the CLI-maintained
cache `~/.local/share/muse/model-catalog/<hex-provider>__p<hex-profile>.json`
(auto-refreshed on every run; 4 Spark rows with per-model effort tiers and
`is_default`); usage is available offline via `muse export --session <id> --redacted`
(per-turn `event/usage` objects: input/output/reasoning/cache tokens, 96 objects in
one live session); superpowers plugin 6.4.1 is already installed in muse
(native-local, enabled); `muse skills list --json` enumerates skills dynamically;
`muse session-message send --target <uuid|name>` exists for the wake probe.

## 2. Locked decisions

- Provider: Meta with stored credentials (`AuthOK` mirrors claude/codex). Echo provider
  out of scope except as test double.
- Runtime: persistent interactive TUI in tmux (`--yolo --trust-workspace`), same
  session model as other agents. No `muse exec` headless path.
- Models: `MuseFetcher` reads `model-catalog/*.json`; union with `settings.json`
  `model` as backstop. No network fetch. `DefaultEffort: "high"` (CLI default).
- Launch flags (Phase-0 rule: every flag must pass a probe first):
  `muse -i <kickoff> --model <id> --reasoning-effort <tier> --yolo --trust-workspace`.
- MCP: merge `swarm` entry into muse `settings.json` `mcpServers` at install
  (shape proven by the live user config); per-launch override only if a probe shows
  a `--settings`/`--mcp-config` flag works — else install-time merge stands.
- Instructions: workspace `AGENTS.md` (trusted-workspace loading). No isolated HOME.
- Wake: probed 2026-09-23 — `session-message send` to a live message_capable TUI
  session fails with `external_agent_ingress_closed` (no opt-in found); Wake stays
  on the tmux-paste fallback. Re-probe if a newer muse documents ingress opt-in.
- Superpowers remediation covers all three fronts; daemon-side blocking enforcement
  gate is explicitly out of scope (instruction-level MUST + kickoff mandate only).
- Muse is opt-in everywhere: role defaults stay claude; `EnabledAgents` gains muse
  only when installed.

## 3. DB models

None. Agent enablement lives in settings JSON (`enabled_agents`); no migration.

## 4. Model / API types

```go
// kinds
const Muse AgentKind = "muse" // Display() "Muse"; append to AgentKinds after Cursor
// install
KindMuse Kind = "muse"; agentBinaries[KindMuse] = "muse"
// catalog
type MuseFetcher struct { DataDir string /* ~/.local/share/muse/model-catalog */; SettingsFile string }
func ParseMuseModels(catalogJSON []byte, settingsJSON []byte) ([]CatalogModel, string, error)
// adapter
type Muse struct{ base } // Kind() kinds.Muse; Launch: yolo TUI argv above
```

```ts
export type AgentKind = "claude" | "codex" | "agy" | "cursor" | "muse" | "fake";
AGENT_LABEL.muse = "Muse"; AGENT_LOGIN_CMD.muse = "muse login";
```

Effort mapping: catalog row `reasoning_effort_variants[].tier` → `CatalogModel.Efforts`;
`LaunchModel` stays the raw Spark slug; `--reasoning-effort` carries the tier.

## 5. Screens

No new screens. Agent picker, spawn sheets, settings roles, and advisor pickers gain
one option row:

```
[○ Claude] [○ Codex] [○ agy] [○ Cursor] [○ Muse]
```

Empty/uninstalled state: option disabled with `muse login` hint (existing pattern).

## 6. User-facing copy

- Label: "Muse". Login hint: `muse login`.
- Catalog source strings: `"muse model-catalog"`.
- Probe skip text: `"live probe against real muse CLI + API; set MUSE_LIVE_PROBE=1"`.
- `swarm-orchestrator/SKILL.md` spike paths rewritten to repo standard (see §7 R2).

## 7. File list

Muse slice (new): `internal/adapter/muse.go`, `internal/adapter/muse_test.go`,
`internal/adapter/muse_wake_probe_test.go` (live, gated), `internal/catalog/parse_muse.go`
(or extend parse.go — reuse if the shape fits), catalog fetcher + tests.
Muse slice (touched): `internal/kinds/kinds.go`, `internal/install/config.go`
(`KindMuse`, `Kinds`, `SkillsDir`, `agentBinaries`, plugin maps in `plugins.go`),
`internal/catalog/fetch.go` (`DefaultFetchers`), `internal/settings/settings.go`
(defaults keep claude; muse eligible), `internal/runtime/model.go` (if per-kind launch
mapping lives there), web `types.ts`, `copy.ts`, `logic/catalog.ts` (+ tests),
`components/icons.tsx` (only if kind-indexed; reuse otherwise).
Remediation: `skills/swarm-orchestrator/SKILL.md` (R2 path fix),
`skills/swarm/SKILL.md` (only if same contradiction — probe first),
per-kind delivery probes (R1; fix what they prove, muse first),
kickoff template superpowers MUST (R3; file TBD by probe — runtime agents/materialize path).
Reused unchanged: MCP server tools, daemon HTTP API, DB schema, spawn/tmux core.

## 8. Verification

Order: `go test ./internal/catalog/ ./internal/adapter/ ./internal/install/ ./internal/settings/`
then `go test ./...`, then web `pnpm test` (catalog, AgentFields, spawn sheets).
Probes: `MUSE_LIVE_PROBE=1 go test ./internal/adapter/ -run TestMuse` (paid; never CI).
Acceptance: `swarm install` on a muse-only machine enables muse; picker shows Muse;
spawned muse lists `swarm` + `superpowers:*` via `muse skills list --json`;
`MuseUsage` reader returns exact nonzero token sums for a live session export
(unit-proven on a real redacted export); poller registration deferred — quota
Meters cannot represent token counts and no per-session token pipeline exists
for any kind yet (all-kinds follow-up, not muse-only surface);
wake delivers or documented fallback does.

## 9. Explicitly out of scope

- Echo provider support; cost/price table (catalog `cost` is null); isolated HOME
  for muse; daemon-side superpowers enforcement gate; changing existing role defaults;
  `muse exec` headless mode; Windows paths.

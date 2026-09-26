# Worker defaults, native Gemini usage, kebab-case agent names

Date: 2026-09-26. Branch `fix/worker-defaults-gemini-names` off `origin/main` 753a288. Worktree `../agent-swarm-worker-defaults`. One repo (agent-swarm): Go daemon, web board, menubar tests, skills.

## Context

Three independent defects, fixed together because they surfaced from the same live run.

### 1. Worker kind/model provenance

Live case: `announce-support-and-ui_reviewer` (agt_01M3EFX1EJAKBTKN1E0P3CNDSJ, role `ui_reviewer`) ran on `claude/opus` at 2026-09-26 09:14 UTC while the user's `roles.ui_reviewer` setting has been `codex/gpt-6-astra` since 2026-09-21 (every `settings.changed` event since then). It was spawned by `swarm_workflow start` (TASK-272), and its two predecessors (`attachment-recovery-ui-reviewer`, `-2`) by direct `swarm_spawn` calls with no `agent`/`model` field (orchestrator transcript 42b25071…, lines 1064 and 1159).

Root cause: the parent orchestrator `support-chat-attachments-orchestrator-2` (agt_01M34MNERA9DEC26FGWJPQE17W) stores `role_overrides = {"coder":{"agent":"claude","model":"sonnet"},"ui_reviewer":{"agent":"claude","model":"opus"}}`, written by its start request on 2026-09-22 13:25 (board Start dialog / `swarm new --role`; `web/src/components/AgentFields.tsx:60-70` only sends roles the user changed). `runtime.Spawn` (`internal/runtime/agents.go:889-896`) applies the parent's override before `cfg.Roles`, so every ui_reviewer under that orchestrator inherits claude/opus. The orchestrator did not choose; the choice is a setup override that is legitimate under the rule, but invisible: the agent row, board and logs say nothing about why the kind differs from settings, and it kept applying for four days.

Separate gaps that let an orchestrator choose on its own:
- `swarm_spawn` accepts `agent`/`model`/`effort` with no justification (`internal/mcpserver/orchestrator.go:540-640`).
- `swarm_role_overrides set` lets an orchestrator pin a role for all its future children with no justification.
- The skill text (`skills/swarm-orchestrator/SKILL.md:28-30`) invites orchestrators to pass explicit agent/model overrides.

### 2. Menubar shows the agy "Claude & GPT" meter instead of Gemini

`internal/usage/agy.go` `ParseAgyQuota` emits meters for both agy groups (`Gemini Models` and `Claude and GPT models`) and picks the busier 5h bucket as headline. Live snapshot 2026-09-26: `gemini_5h` 6 %, `cgpt_5h` 100 % → headline `cgpt_5h`. The menubar renders whatever the daemon sends (`apps/menubar/Sources/SwarmBarKit/MenuLabel.swift:35-52` uses `snap.headline`; `UsageSection.rows` lists every meter), so the label shows 100 % for agy and the panel lists the extra-model rows. The daemon's wire decides this.

Side effect of the same bug: `internal/usagegate/usagegate.go` `Exhausted` reads the headline meter, so agy was treated as out of usage while its Gemini quota was at 6 %: notification at 2026-09-26 07:28 "research-q3-logs-metrics-researcher switched to Claude because agy is out of usage."

### 3. Generated agent names contain underscores

`internal/runtime/agents.go:90-99` `defaultName` kebabs the item title but appends `string(role)` raw, so a `ui_reviewer` becomes `<slug>-ui_reviewer`. Every other name path already goes through `ids.Kebab` (`resolveName` for typed names, `StartSpike`, worktree names).

## Locked decisions

User rules, not to be reopened:
- **L1.** A new worker's agent kind and model always come from the user's settings (the role default), unless the user explicitly requested an override. The request can come at orchestrator setup (spawn brief, or `swarm new`/`start` flags / board Start dialog role overrides) or interactively via the agent (the user told the orchestrator to use X).
- **L2.** A usage fallback stays allowed (automatic, not the orchestrator's choice) but must be visible.
- **L3.** All generated agent names are kebab-case: lowercase, `[a-z0-9-]`, repeats collapsed, trimmed. Existing DB rows are not renamed.
- **L4.** The menubar always shows native Gemini usage for agy; the extra (Claude & GPT inside agy) models are not shown.
- **L5.** Never delete tests; port them when a contract changes.

Design decisions (this spec):
- **D1.** `swarm_spawn` with any of `agent`/`model`/`effort` requires a non-empty `override_reason` stating what the user asked for; otherwise the call is refused. Rejecting beats silently ignoring: an LLM caller corrects on an error, while a silent ignore leaves it believing it got X.
- **D2.** `swarm_role_overrides` `op:"set"` requires a non-empty `reason`, logged. `op:"clear"` needs none.
- **D3.** One nullable column `agents.kind_reason` records why a worker's kind/model is not the plain settings default. Set by `runtime.Spawn` (explicit override, parent role override), and by every usage-fallback substitution (`Spawn`, `startQueued`, `Retry`). NULL means "came from the user's settings".
- **D4.** Board/HTTP start of orchestrators and spikes is unchanged: those are the user's own choices.
- **D5.** agy: drop the known extra group `Claude and GPT models` in `ParseAgyQuota`; keep unknown groups (robust to a new or renamed Gemini group); headline is `gemini_5h` when present, else the busiest remaining 5h meter. This fixes the menubar and the false exhaustion gate in one place.
- **D6.** Name fix in `defaultName` only: `ids.Kebab(slug + "-" + string(role))`. `ids.Kebab` is the one shared normalizer.

Assumption: `kind_reason` is shown on the board agent row only; the menubar agent list does not render it (its `Wire.swift` decoder ignores unknown keys).

## DB models

Migration `internal/db/schema/0016_agent_kind_reason.sql`:

```sql
-- Why a worker's kind/model differs from the user's role default
-- (user override, parent role override, usage fallback). NULL = settings.
ALTER TABLE agents ADD COLUMN kind_reason TEXT;
```

No index, no backfill.

## Model / API types

Go:

```go
// runtime.Agent
KindReason string // "" = came from the user's settings

// runtime.SpawnInput
OverrideReason string // required by swarm_spawn when Kind/Model/Effort is set

// runtime (agents.go)
func joinReason(a, b string) string // "a; b", skipping empties
func fallbackReason(orig AgentKind) string // "<Display> is out of usage"
```

`runtime.SetRoleOverride` keeps its signature; the `reason` check and log live in the MCP handler (`s.Log`), the only orchestrator-facing caller.

`kind_reason` values:
- explicit spawn override: `User override: <override_reason>`
- parent role override applied: `Role override set on <parent name>`
- usage fallback: `<Original kind display> is out of usage` appended with `; ` to any earlier reason.

MCP `swarm_spawn` schema adds `"override_reason":{"type":"string"}`. MCP `swarm_role_overrides` schema adds `"reason":{"type":"string"}`.

HTTP `AgentNode` (`internal/httpapi/runtime.go` `agentNodeWire`) adds `KindReason *string \`json:"kind_reason"\`` (null when empty). Web `types.ts` `AgentNode` adds `kind_reason: string | null`.

agy meters: IDs/labels unchanged for the Gemini group (`gemini_5h`, `gemini_weekly`, labels `Gemini 5h`, `Gemini weekly`); `cgpt_*` meters no longer emitted.

## Screens

Board agent row (`web/src/components/AgentRow.tsx`), only when `kind_reason` is non-null:

```
[icon] announce-support-and-ui-reviewer · UI reviewer        [Pause] [Cancel]
       Role override set on support-chat-attachments-orchestrator-2
       ● Running
```

The reason line is `text-xs text-muted`, truncated, with the full text as `title`. Absent (no empty line) when null. Not shown in the menubar.

Menubar: agy segment shows `gemini_5h` percent; tooltip `Gemini 5h 6%`; usage panel for agy lists `Gemini weekly` and `Gemini 5h` only. No layout change.

## User-facing copy

- `swarm_spawn` refusal: `Pass override_reason with agent, model or effort, saying what the user asked for. Leave agent, model and effort empty to use the user's role default.`
- `swarm_role_overrides set` refusal: `Pass reason with op "set", saying what the user asked for. Role defaults come from the user's settings unless the user asks otherwise.`
- `kind_reason` strings as listed under Model / API types.
- Daemon log on spawn with a reason: `spawn: <name> on <kind>/<model>: <kind_reason>`.
- Daemon log on role override set: `role override: <orchestrator> set <role> to <agent>/<model>: <reason>`.

## File list

Changed:
- `internal/db/schema/0016_agent_kind_reason.sql` (new)
- `internal/runtime/model.go` (Agent.KindReason)
- `internal/runtime/agents.go` (SpawnInput.OverrideReason, Spawn reason, defaultName, scan sites, Retry fallback reason)
- `internal/runtime/inbox.go` (scan sites)
- `internal/runtime/limits.go` (startQueued fallback reason)
- `internal/mcpserver/orchestrator.go` (swarm_spawn override_reason, swarm_role_overrides reason)
- `internal/httpapi/runtime.go` (kind_reason on AgentNode)
- `internal/usage/agy.go` (drop cgpt group, Gemini headline)
- `web/src/types.ts`, `web/src/components/AgentRow.tsx`, `web/src/mock/daemon.ts`, `web/src/logic/agentActions.ts` fixtures as needed
- `skills/swarm-orchestrator/SKILL.md` + `make skills-sync` → `internal/install/skills/…`
- Tests: `internal/runtime/agents_test.go`, `internal/runtime/fallback_test.go`, `internal/mcpserver/*_test.go`, `internal/usage/agy_test.go`, `internal/usagegate/usagegate_test.go` (if it needs a real agy case), `web/src/components/AgentRow.test.tsx`, `apps/menubar/Tests/…`

Reused unchanged: `ids.Kebab`/`ids.Unique`, `settings`, `resolveUsageFallback`, `usagegate.Exhausted`, menubar `MenuLabel`/`UsageSection`.

Deleted: nothing.

## Verification

1. `go test ./... -count=1`, `go vet ./...`, `test -z "$(gofmt -l .)"`.
2. `(cd web && pnpm test)`; `(cd apps/menubar && swift test)`.
3. `make skills-sync && git diff --exit-code internal/install/skills`.
4. Scenarios (unit tests):
   - spawn with no agent/model → role default kind/model, `kind_reason` empty;
   - spawn with agent/model + reason → kept, `kind_reason = "User override: …"`;
   - MCP `swarm_spawn` with agent but no `override_reason` → refused with the copy above;
   - parent role override → applied, `kind_reason = "Role override set on <parent>"`;
   - usage fallback on spawn → fallback kind, reason contains `is out of usage`; same for a queued agent drained by `startQueued`;
   - `swarm_role_overrides set` without reason → refused; with reason → set;
   - ui_reviewer default name → `<slug>-ui-reviewer`;
   - agy quota with a 100 % cgpt group and 6 % Gemini → only Gemini meters, headline `gemini_5h`, gate not exhausted.
5. Existing agents: tmux names and name lookups read the stored `agents.name`/`sessions.tmux_name`, so only new names change.

## Explicitly out of scope

- Clearing the live `role_overrides` on `support-chat-attachments-orchestrator-2` (user decision).
- Renaming existing agents or tmux sessions.
- A reason UI in the board Start dialog or the menubar agent list.
- Changing fallback selection, thresholds, or the claude/codex/cursor/muse usage parsers.
- Showing agy's Claude & GPT quota anywhere in Swarm.

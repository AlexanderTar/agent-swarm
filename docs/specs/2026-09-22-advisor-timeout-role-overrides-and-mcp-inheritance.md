# Specification: Advisor Timeout, Model-to-Agent Resolution, Claude MCP Inheritance, and UI Role Overrides

**Date:** 2026-09-22  
**Status:** Approved  
**Author:** Antigravity  

---

## 1. Context

In live swarm usage on macOS, four interrelated issues were identified:
1. **Advisor Execution Failure:** A Codex orchestrator called `swarm_advise` (running `codex exec -s read-only -m gpt-6-astra`). The process was killed at 30.021s (`signal: killed`). `advisor.Service` configured a 240s timeout, but `cmd/swarm/daemon.go` passed `Run: execx.Run`, which enforces a hardcoded 30s timeout cap (`context.WithTimeout(ctx, 30*time.Second)`). In addition, `codex exec` inherited user MCP servers from `~/.codex/config.toml`, which failed because `codex exec` has `approval: never`.
2. **Worker Spawn Rejection on Explicit Model:** When an orchestrator called `swarm_spawn` with `model: "gpt-5.6-terra"` and `role: "implementer"`, the spawn was rejected with `Claude is out of usage, and no other agent is available right now.` `s.RT.Spawn()` defaulted `in.Kind` to `cfg.EnabledAgents[0]` (`claude`) instead of resolving the agent from `in.Model`. Because Claude had an active session limit, `resolveUsageFallback()` blocked the worker.
3. **Claude User MCP Inheritance:** Codex and Cursor workers inherit user MCP servers with automatic approval, but Claude worker sessions wrote a temporary `claude-mcp.json` containing *only* `swarm`, dropping the user's configured MCP servers (`agentmemory`, `context7`, `neon`, `notion`, `railway`, `vercel`).
4. **UI Role Overrides:** When launching an orchestrator from the UI (web `SpawnSheet` and `NewSpikeSheet`), the user can only configure the orchestrator and advisor models. There is no way to override worker roles (`coder`, `reviewer`, `ui_reviewer`, `researcher`, `debugger`, `mechanical`), forcing workers to use global settings defaults.

Affected repository worktree: `/Users/alexandertar/GitHub/agent-swarm--fix-advisor-and-role-overrides`.

---

## 2. Locked Decisions

1. **Advisor Timeout:** `execx.Run` must respect caller context deadlines if already present. `cmd/swarm/daemon.go` must explicitly wire `execx.RunFor(240 * time.Second)` to `advisor.Service`.
2. **Advisor Codex MCP Isolation:** Codex in advisor mode (`AdvisorCommand`) must pass `-c mcp_servers={}` to ensure no external MCP servers are loaded during read-only advice.
3. **Model-to-Agent Resolution:** In `s.RT.Spawn()`, if `in.Kind == ""` and `in.Model != ""`, the daemon must search `s.Catalog.ModelsFor()` across enabled agents to find the matching agent kind.
4. **Role Normalization:** `role: "implementer"` must be accepted and normalized to `RoleCoder` (`"coder"`).
5. **Role Defaults Precedence:** If `in.Kind` and `in.Model` are omitted:
   - 1st: Parent orchestrator's `role_overrides` for that role.
   - 2nd: Global `settings.roles[role]`.
   - 3rd: `cfg.EnabledAgents[0]` and default catalog model.
6. **Claude MCP Inheritance:** Claude worker/orchestrator sessions must read `~/.claude.json` (if present), extract `mcpServers`, inject/override `swarm` with the session's active binary and args, and write the merged map to `claude-mcp.json`.
7. **UI Role Overrides:** `SpawnSheet` and `NewSpikeSheet` must present a collapsible "Worker Roles" section pre-populated with `settings.roles`. When submitted, these overrides are sent to `POST /api/items/{key}/orchestrator` and `POST /api/spikes`, persisted in `agents.role_overrides`, and inherited by subagents.

---

## 3. DB Models & Migrations

### Migration `internal/db/schema/0007_add_agent_role_overrides.sql`
```sql
-- 0007_add_agent_role_overrides.sql: add role_overrides JSON to agents
ALTER TABLE agents ADD COLUMN role_overrides TEXT;
```

### Table `agents` (Updated Schema)
- `role_overrides TEXT`: JSON object mapping `role` string to `{"agent":"...","model":"...","effort":"..."}`. Nullable.

---

## 4. Model & API Types

### Go Types (`internal/runtime/model.go` & `internal/settings/settings.go`)
```go
type OrchestratorInput struct {
    ItemKey   string
    Kind      AgentKind
    Model     string
    Effort    string
    Advisor   AdvisorChoice
    Name      string
    RepoPaths []string
    Roles     map[Role]settings.RoleDefault // new
}

type SpikeInput struct {
    Name      string
    Intent    string
    Kind      AgentKind
    Model     string
    Effort    string
    Advisor   AdvisorChoice
    Request   string
    Repos     []string
    Roles     map[Role]settings.RoleDefault // new
}

type Agent struct {
    // Existing fields...
    RoleOverrides map[Role]settings.RoleDefault // new
}
```

### HTTP API (`POST /api/items/{key}/orchestrator` & `POST /api/spikes`)
Request Body:
```json
{
  "name": "support-chat-attachments-orchestrator",
  "agent": "codex",
  "model": "gpt-6-astra",
  "effort": "medium",
  "advisor": { "agent": "codex", "model": "gpt-6-astra" },
  "repos": ["repo_1"],
  "repos_version": 1,
  "request_id": "uuid",
  "roles": {
    "coder": { "agent": "codex", "model": "gpt-5.6-terra", "effort": "medium" },
    "reviewer": { "agent": "claude", "model": "claude-sonnet-4-6" }
  }
}
```

### TypeScript Types (`web/src/types.ts`)
```ts
export interface StartOrchestratorBody {
  request_id: string;
  agent?: AgentKind;
  model?: string;
  effort?: string;
  advisor?: AdvisorPayload;
  repos?: string[];
  repos_version?: number;
  name?: string;
  roles?: Partial<Record<SettingsRole, RoleDefault>>;
}

export interface CreateSpikeBody {
  request_id: string;
  name: string;
  intent: SpikeIntent;
  repos?: string[];
  agent?: AgentKind;
  model?: string;
  effort?: string;
  advisor?: AdvisorPayload;
  request?: string;
  roles?: Partial<Record<SettingsRole, RoleDefault>>;
}
```

---

## 5. Screens & UI

### ASCII Sketch: `SpawnSheet` with Collapsible Worker Roles
```
+--------------------------------------------------------------+
| Start orchestrator                                       [X] |
| EPIC-3 · Support, chat attachments and privacy               |
+--------------------------------------------------------------+
| Repositories: [ endurio-chat, endurio-app                  ] |
|                                                              |
| Orchestrator:                                                |
|   Agent:  [ Codex       v ]   Model: [ gpt-6-astra       v ] |
|   Effort: [ medium      v ]                                  |
|   Advisor:[ Codex (gpt-6-astra)                            v]|
|                                                              |
| v Worker Roles (customize defaults for this run)             |
|   +--------------------------------------------------------+ |
|   | Coder:       [ Codex   v ] [ gpt-5.6-terra   v ] [medv]| |
|   | Reviewer:    [ Claude  v ] [ sonnet          v ] [   ] | |
|   | UI Reviewer: [ Claude  v ] [ opus            v ] [   ] | |
|   | Researcher:  [ Claude  v ] [ sonnet          v ] [   ] | |
|   | Debugger:    [ Codex   v ] [ gpt-6-astra     v ] [high]| |
|   | Mechanical:  [ Claude  v ] [ haiku           v ] [   ] | |
|   +--------------------------------------------------------+ |
|                                                              |
| Name: [ support-chat-attachments-orchestrator              ] |
+--------------------------------------------------------------+
| [Cancel]                                 [Start orchestrator]|
+--------------------------------------------------------------+
```

---

## 6. All User-Facing Copy

- `C.workerRoles`: `"Worker Roles"`
- `C.customizeWorkerRoles`: `"Customize worker roles for this orchestrator"`
- `C.workerRolesHelp`: `"Subagents spawned by this orchestrator will use these agent and model defaults."`
- `swarm_spawn` description: `"Spawn a worker agent on an item. Supports explicit 'agent' (claude, codex, cursor, agy) and 'model' overrides; if only 'model' is provided, the agent is automatically resolved from the catalog."`

---

## 7. File List

- `internal/execx/execx.go`: Respect parent context deadline in `Run()`.
- `cmd/swarm/daemon.go`: Wire `advisor.Service` with `execx.RunFor(240 * time.Second)`.
- `internal/advisor/run.go`: Add `-c mcp_servers={}` to `AdvisorCommand` for `runtime.Codex`.
- `internal/adapter/claude.go`: Merge user `~/.claude.json` `mcpServers` into `claude-mcp.json`.
- `internal/db/schema/0007_add_agent_role_overrides.sql`: Add `role_overrides` column to `agents`.
- `internal/runtime/model.go`: Add `Roles` to `OrchestratorInput` and `SpikeInput`; `RoleOverrides` to `Agent`.
- `internal/runtime/agents.go`: Persist `role_overrides` in `StartOrchestrator` / `StartSpike`; read parent overrides and resolve `in.Kind` from `in.Model` in `Spawn()`; normalize `"implementer"` to `"coder"`.
- `internal/mcpserver/orchestrator.go`: Update `swarm_spawn` description and handle `agent` / `model` overrides.
- `internal/httpapi/spawn.go`: Decode `roles` in orchestrator and spike routes.
- `web/src/types.ts`: Update `StartOrchestratorBody` and `CreateSpikeBody`.
- `web/src/components/AgentFields.tsx`: Add worker role override fields and state.
- `web/src/panels/SpawnSheet.tsx`: Forward role overrides.
- `web/src/panels/NewSpikeSheet.tsx`: Forward role overrides.
- `internal/install/skills/swarm-orchestrator/SKILL.md`: Document `agent` / `model` overrides.

---

## 8. Verification Scenarios

1. `go test ./internal/execx/...`: Confirms `Run()` does not truncate parent deadlines > 30s.
2. `go test ./internal/advisor/...`: Confirms `AdvisorCommand` generates `-c mcp_servers={}` for Codex.
3. `go test ./internal/adapter/...`: Confirms `claude.go` merges user `~/.claude.json` `mcpServers` with `swarm`.
4. `go test ./internal/runtime/...`: Confirms `Spawn()` resolves `Kind` from `Model`, maps `implementer` to `coder`, and applies parent `role_overrides`.
5. `go test ./internal/httpapi/...`: Confirms `POST /api/items/{key}/orchestrator` and `POST /api/spikes` accept `roles` and pass them to runtime.
6. `pnpm test` in `web/`: Confirms UI renders role overrides and submits them in payload.

---

## 9. Explicitly Out of Scope

- Changing advisor behavior for Claude or Cursor (already working as designed).
- Adding new roles beyond the existing 8 `kinds.SettingsRoles`.
- Dynamic MCP server addition during a running session without restarting the agent.

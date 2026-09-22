# Specification: Isolated MCP and Durable Custom Instructions

**Date:** 2026-09-22  
**Status:** Approved  
**Author:** Antigravity  

---

## 1. Context

In live Swarm execution across the four supported agents (`claude`, `agy`, `cursor-agent`, `codex`), two major issues degrade session reliability and token economy:

1. **User MCP Tool Leaks:** Codex, Cursor, and Claude (via commit `f165a09`) inherit user-configured MCP servers (such as `notion`, `neon`, `vercel`, `context7`, and `agentmemory`). In autonomous subagent loops, these external MCP servers cause:
   - Severe token overhead (thousands of tokens injected per turn for irrelevant tool schemas).
   - Process failures and stalls due to background OAuth refreshes on revoked tokens (e.g. Notion OAuth refresh errors crashing turn setup).
   - Agent confusion and hallucinations from unexpected extra tools.
2. **Global Instruction Contamination & Lack of Durable Instructions:** User-level instruction files (`~/.agents/AGENTS.md`, `~/.claude/CLAUDE.md`, `~/.gemini/GEMINI.md`, `~/.cursor/AGENTS.md`) are automatically discovered and loaded by agent CLIs, injecting operator-specific formatting and personal habits into autonomous swarm agents. Furthermore, Swarm provides no centralized mechanism for the user or orchestrator to define durable project instructions that are injected across all swarm agents.

### Goals
- **Isolated MCP:** Ensure that agents spawned by Swarm receive *only* the `swarm` MCP server and zero external/user MCP servers.
- **Durable Instructions:** Allow the user to define global swarm instructions in a dedicated "Instructions" settings tab in the Menubar app, persisted in daemon settings.
- **Interactive MCP Access:** Provide a `swarm_instructions` MCP tool (`get` and `set` operations) so agents can inspect or interactively update durable instructions.
- **Instruction Injection & Global Suppression:** Inject durable instructions into each agent's launch configuration as `AGENTS.md` / system prompt while suppressing user-level global instruction files.

Worktree: `/Users/alexandertar/GitHub/agent-swarm--instructions-and-isolated-mcp`  
Branch: `feat/instructions-and-isolated-mcp`

---

## 2. Locked Decisions

1. **Durable Storage:** Store instructions as `Instructions string` in `settings.Settings` (`internal/settings/settings.go`), persisted under key `"instructions"` in the SQLite `settings` table. Default is `""`.
2. **MCP Isolation (Only `swarm`):**
   - **Claude:** Revert `~/.claude.json` inheritance in `internal/adapter/claude.go`. Write only `swarm` to `claude-mcp.json`. Pass `--strict-mcp-config` so Claude ignores all external/user MCP servers.
   - **Codex:** In `internal/adapter/codex.go`, isolate `CODEX_HOME` in `Launch.Env` to `<launchDir>/codex-home` with symlinked `auth.json` (no `config.toml`). Only `-c mcp_servers.swarm...` is provided.
   - **Cursor:** In `internal/adapter/cursor.go`, set `CURSOR_DATA_DIR` in `Launch.Env` to `<launchDir>/cursor-home`. Write `<launchDir>/cursor-home/mcp.json` containing only `swarm`.
   - **Antigravity (`agy`):** In `internal/adapter/agy.go`, isolate `HOME` to `<launchDir>/agy-home` with symlinked `.gemini/antigravity-cli/{antigravity-oauth-token, settings.json}` and write `.gemini/config/mcp_config.json` containing only `swarm`.
3. **Instruction Injection & Suppression:**
   - **Claude:** Pass `--setting-sources "project,local"` to ignore user `~/.claude/CLAUDE.md`. If `Instructions` is non-empty, write `<launchDir>/AGENTS.md` and pass `--append-system-prompt-file <path>`.
   - **Codex:** If `Instructions` is non-empty, write `<launchDir>/AGENTS.md` and pass `-c model_instructions_file="<path>"`.
   - **Antigravity (`agy`):** If `Instructions` is non-empty, write `<launchDir>/agy-home/.gemini/AGENTS.md`.
   - **Cursor:** If `Instructions` is non-empty, write `<workspace>/AGENTS.md` (or `<cursorHome>/AGENTS.md`).
4. **MCP Tooling (`swarm_instructions`):**
   - Add tool `swarm_instructions` in `internal/mcpserver/tools.go`.
   - `op: "get"`: returns `{"instructions": string}`.
   - `op: "set"`: updates `settings.Instructions`, saves via `settings.Store.Put`, and returns `{"status": "ok"}`.
5. **Menubar Settings UI (`InstructionsTab`):**
   - 5th tab in `apps/menubar/Sources/SwarmBarUI/SettingsView.swift`.
   - Default state: Read-only rendered Markdown inside a scrollable view with an `Edit` button and `Copy` button.
   - Edit state: Monospace `TextEditor` with `Cancel` and `Save` buttons (`Cmd+Enter` to save, `Esc` to cancel).
   - Empty state: Clean placeholder when no instructions are configured with `Add Instructions` button.

---

## 3. DB Models & Migrations

No database migration is required. The `settings` table uses dynamic JSON key-value rows (`key TEXT PRIMARY KEY, value_json TEXT, updated_at INTEGER`). Adding `Instructions string` to `settings.Settings` automatically persists to and reads from key `"instructions"`.

---

## 4. Model & API Types

### Go Types

#### `internal/settings/settings.go`
```go
type Settings struct {
    EnabledAgents          []kinds.AgentKind          `json:"enabled_agents"`
    Roles                  map[kinds.Role]RoleDefault `json:"roles"`
    FallbackDefault        RoleDefault                `json:"fallback_default"`
    Notifications          map[string]NotifyPref      `json:"notifications"`
    MaxOrchestrators       int                        `json:"max_orchestrators"`
    MaxAgents              int                        `json:"max_agents"`
    MaxAgentsPerRoot       int                        `json:"max_agents_per_root"`
    MaxConcurrentSubagents int                        `json:"max_concurrent_subagents"`
    ScanExcludes           []string                   `json:"scan_excludes"`
    ScanIntervalSec        int                        `json:"scan_interval_sec"`
    MenubarCompact         bool                       `json:"menubar_compact"`
    UsagePollSec           int                        `json:"usage_poll_sec"`
    PauseDeadlineSec       int                        `json:"pause_deadline_sec"`
    Instructions           string                     `json:"instructions"` // new
}
```

#### `internal/adapter/adapter.go`
```go
type Spec struct {
    AgentName, SessionID, Token, DaemonURL string
    Model, Effort, Cwd                     string
    ProviderSessionID                      string
    Kickoff                                string
    SettingsDir                            string
    Bin                                    string
    PluginDirs                             []string
    AdvisorModel                           string
    Instructions                           string // new: durable instructions text
    Env                                    map[string]string
}
```

#### `internal/mcpserver/tools.go`
```json
{
  "name": "swarm_instructions",
  "description": "Read or update durable Swarm instructions injected into agents.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "op": { "type": "string", "enum": ["get", "set"] },
      "instructions": { "type": "string" }
    },
    "required": ["op"]
  }
}
```

### Swift Types (`apps/menubar/Sources/SwarmBarKit/SettingsModel.swift`)
```swift
public struct SettingsPayload: Codable, Equatable {
    // Existing fields...
    public var instructions: String?
}
```

---

## 5. Screens & UI

### Menubar Settings Window (740 × 520 pt)

#### State 1: Read-Only (Rendered Markdown)
```
+-------------------------------------------------------------------------------+
|  [Agents]   [Defaults]   [Notifications]   [Limits]   [*Instructions*]        |
+-------------------------------------------------------------------------------+
|                                                                               |
|  Agent Instructions                                            [ Edit ]       |
|  Injected as durable AGENTS.md and system prompts into all swarm sessions.    |
|                                                                               |
|  +-------------------------------------------------------------------------+  |
|  | # Project Guidelines                                                | ^ |  |
|  |                                                                     | | |  |
|  | - **Code Style**: Strictly Go 1.24 + standard library first         | | |  |
|  | - **Testing**: Table-driven tests; no test deletions               | | |  |
|  | - **Architecture**: Worktrees only, subagent-driven development     | | |  |
|  |                                                                     | v |  |
|  +-------------------------------------------------------------------------+  |
|                                                                        [Copy] |
+-------------------------------------------------------------------------------+
```

#### State 2: Edit Mode (Monospace Editor)
```
+-------------------------------------------------------------------------------+
|  [Agents]   [Defaults]   [Notifications]   [Limits]   [*Instructions*]        |
+-------------------------------------------------------------------------------+
|                                                                               |
|  Edit Instructions                                    [ Cancel ]  [  Save  ]  |
|  Paste or edit markdown instructions. Changes apply to newly spawned agents.  |
|                                                                               |
|  +-------------------------------------------------------------------------+  |
|  | # Project Guidelines                                                | ^ |  |
|  |                                                                     | | |  |
|  | - **Code Style**: Strictly Go 1.24 + standard library first         | | |  |
|  | - **Testing**: Table-driven tests; no test deletions               | | |  |
|  | - **Architecture**: Worktrees only, subagent-driven development     | | |  |
|  |                                                                     | v |  |
|  +-------------------------------------------------------------------------+  |
|  Markdown supported · Press Cmd+Enter to save                                 |
+-------------------------------------------------------------------------------+
```

#### State 3: Empty State
```
+-------------------------------------------------------------------------------+
|  [Agents]   [Defaults]   [Notifications]   [Limits]   [*Instructions*]        |
+-------------------------------------------------------------------------------+
|                                                                               |
|  Agent Instructions                                                           |
|  Injected as durable AGENTS.md and system prompts into all swarm sessions.    |
|                                                                               |
|  +-------------------------------------------------------------------------+  |
|  |                                                                         |  |
|  |                     No custom instructions configured                   |  |
|  |           Agents currently run with isolated swarm MCP and              |  |
|  |                   standard system instructions only.                    |  |
|  |                                                                         |  |
|  |                            [ + Add Instructions ]                       |  |
|  |                                                                         |  |
|  +-------------------------------------------------------------------------+  |
|                                                                               |
+-------------------------------------------------------------------------------+
```

---

## 6. All User-Facing Copy

### Menubar App (`apps/menubar/Sources/SwarmBarKit/Copy.swift`)
- `Copy.tabInstructions`: `"Instructions"`
- `Copy.agentInstructions`: `"Agent Instructions"`
- `Copy.editInstructions`: `"Edit Instructions"`
- `Copy.instructionsCaption`: `"Injected as durable AGENTS.md and system prompts into all swarm sessions."`
- `Copy.editInstructionsCaption`: `"Paste or edit markdown instructions. Changes apply to newly spawned agents."`
- `Copy.noInstructionsConfigured`: `"No custom instructions configured"`
- `Copy.noInstructionsSubtitle`: `"Agents currently run with isolated swarm MCP and standard system instructions only."`
- `Copy.addInstructions`: `"Add Instructions"`
- `Copy.copyInstructions`: `"Copy"`
- `Copy.copied`: `"Copied"`
- `Copy.instructionsSaved`: `"Instructions saved."`
- `Copy.instructionsHelp`: `"Markdown supported · Press Cmd+Enter to save"`

---

## 7. File List

- `internal/settings/settings.go`: Add `Instructions` to `Settings`.
- `internal/settings/settings_test.go`: Add tests for `Instructions` load/put.
- `internal/mcpserver/tools.go`: Add `swarm_instructions` tool (`get` and `set` operations).
- `internal/mcpserver/tools_test.go`: Unit tests for `swarm_instructions`.
- `internal/adapter/adapter.go`: Add `Instructions` to `Spec`.
- `internal/adapter/claude.go`: Revert `~/.claude.json` inheritance; add `--strict-mcp-config`, `--setting-sources "project,local"`, and `--append-system-prompt-file`.
- `internal/adapter/claude_test.go`: Update tests for Claude isolated MCP and instructions injection.
- `internal/adapter/codex.go`: Set `CODEX_HOME` in `Launch.Env`; write isolated `auth.json`; pass `-c model_instructions_file`.
- `internal/adapter/codex_test.go`: Update tests for Codex isolated MCP and instructions injection.
- `internal/adapter/agy.go`: Write isolated `.gemini/config/mcp_config.json` and `.gemini/AGENTS.md`; set `HOME` in `Launch.Env`.
- `internal/adapter/agy_test.go`: Update tests for Antigravity isolated MCP and instructions injection.
- `internal/adapter/cursor.go`: Write isolated `CURSOR_DATA_DIR/mcp.json`; write workspace `AGENTS.md`.
- `internal/adapter/cursor_test.go`: Update tests for Cursor isolated MCP and instructions injection.
- `internal/runtime/agents.go`: Pass `settings.Instructions` into `adapter.Spec` in `StartOrchestrator`, `StartSpike`, and `Spawn`.
- `apps/menubar/Sources/SwarmBarKit/Copy.swift`: Add copy constants.
- `apps/menubar/Sources/SwarmBarKit/SettingsModel.swift`: Add `instructions` property and save methods.
- `apps/menubar/Sources/SwarmBarUI/SettingsView.swift`: Add `InstructionsTab` with rendered and edit modes.
- `apps/menubar/Tests/SwarmBarTests/SettingsModelTests.swift`: Unit tests for `instructions` in Swift model.

---

## 8. Verification Scenarios

1. **Settings Persistence:**
   - Run `go test ./internal/settings/...`: verifies `Instructions` field round-trips through SQLite.
2. **MCP Tool:**
   - Run `go test ./internal/mcpserver/...`: verifies `swarm_instructions` `get` returns current instructions and `set` updates them.
3. **Adapter Isolation & Injection:**
   - Run `go test ./internal/adapter/...`:
     - Claude: asserts `--strict-mcp-config` present, `claude-mcp.json` contains *only* `swarm`, `--setting-sources "project,local"`, and `--append-system-prompt-file` points to written `AGENTS.md`.
     - Codex: asserts `CODEX_HOME` set in `Launch.Env`, `-c model_instructions_file` passed, `-c mcp_servers.swarm...` passed.
     - Agy: asserts `HOME` set in `Launch.Env`, `.gemini/config/mcp_config.json` contains *only* `swarm`, `.gemini/AGENTS.md` contains instructions.
     - Cursor: asserts `CURSOR_DATA_DIR` set in `Launch.Env`, `mcp.json` contains *only* `swarm`.
4. **Runtime Integration:**
   - Run `go test ./internal/runtime/...`: verifies `settings.Instructions` is passed to `adapter.Spec` when spawning agents.
5. **Menubar Swift Tests:**
   - Run `swift test` in `apps/menubar`: verifies `SettingsModel` decodes `instructions` and sends `PUT /api/settings` on save.

---

## 9. Explicitly Out of Scope

- Per-agent or per-item instructions overrides (all swarm sessions receive the durable instructions; per-item specifics are passed in item brief/kickoff).
- Adding custom user MCP servers to the isolated environment (the explicit requirement is *only* `swarm` MCP).
- Syntax-highlighted code editor in the menubar (plain monospace `TextEditor` is used).

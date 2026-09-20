---
name: subagent-budget-limits
description: Force child agents to limit the number of subagents they dispatch to stay within budget
---

# Context
When an orchestrator or child agent delegates work, it may spawn many subagents concurrently. If unrestricted, an agent might attempt to spawn dozens of subagents at once (e.g., via `swarm_spawn`), overwhelming system resources, exceeding rate limits, and bypassing the necessary feedback loop of reviewing results before delegating further. This spec introduces a concurrency budget per parent agent. If an agent attempts to spawn a subagent when it is already at its limit, Swarm will block the tool invocation at the `PreToolUse` hook level with a clear explanation, forcing the LLM to either wait for active subagents to finish or run tasks sequentially.

# Locked decisions
1. **Concurrency, not lifetime:** The budget limits active/queued subagents concurrently running under a single parent, not the total number of subagents created over the parent's lifetime. 
2. **Hook-level enforcement:** The limit is strictly enforced in `internal/hook/handler.go` during `PreToolUse`. Blocking at the tool level provides immediate, observable feedback to the LLM.
3. **Database-backed accounting:** For `swarm_spawn`, Swarm natively tracks the parent-child relationship via `agents.parent_agent_id` and the lifecycle via `agents.state`. The active count is queried directly from the database.
4. **Native tool handling:** Native subagent dispatch tools (like Claude's `Task` or Antigravity's `invoke_subagent`) are intercepted in `PreToolUse`. However, since native background lifecycles are difficult to track without deep transcript scanning, they will simply be blocked if the parent is over the configured budget of *Swarm-tracked* agents.
5. **Configuration:** The budget is added to `Settings` as `max_concurrent_subagents`.

# DB / Settings schema changes
**File: `internal/settings/settings.go`**
Add the budget field to the `Settings` struct:
```go
type Settings struct {
	// ... existing fields ...
	MaxConcurrentSubagents int `json:"max_concurrent_subagents"`
}
```

Update `Defaults()` to provide a baseline (e.g., 3):
```go
func Defaults(installed []kinds.AgentKind) Settings {
	// ...
	return Settings{
		// ... existing defaults ...
		MaxConcurrentSubagents: 3,
	}
}
```

Update `validate()` to ensure safe limits:
```go
	case next.MaxConcurrentSubagents < 1 || next.MaxConcurrentSubagents > 16:
		return invalid("Maximum concurrent subagents per parent must be between 1 and 16.")
```

# Types & API shapes
No changes to external APIs. The existing `adapter.HookDecision` remains structurally identical. The LLM receives the rejection reason directly in its tool call output.

# Enforcement logic
**File: `internal/hook/handler.go`**

In `func (h *Handler) decide(...)`, under `case "PreToolUse":`:
1. Check if the intercepted tool is a subagent dispatch command. We match Swarm's MCP tool, as well as native dispatcher tools.
2. If matched, query the `agents` table for the count of subagents with `parent_agent_id = s.AgentID` and `state IN ('queued', 'active')`.
3. Fetch `MaxConcurrentSubagents` from Settings.
4. If the active count meets or exceeds the limit, return a `HookDecision` that blocks the tool with a clear reason.

```go
	case "PreToolUse":
		if s.State.Pausing() && !in.IsSwarmTool {
			return adapter.HookDecision{
				Block:  true,
				Reason: runtime.ControlNotice(s.AgentName, s.ItemKey),
			}, nil
		}

		// Detect subagent dispatch tools
		isSpawn := (in.IsSwarmTool && strings.Contains(in.ToolName, "swarm_spawn")) ||
			in.ToolName == "Task" || 
			in.ToolName == "invoke_subagent"

		if isSpawn {
			cfg, err := h.RT.Settings.Get(ctx)
			if err != nil {
				return adapter.HookDecision{}, err
			}

			var active int
			err = h.DB.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM agents 
				WHERE parent_agent_id = ? AND state IN ('queued', 'active')
			`, s.AgentID).Scan(&active)
			if err != nil {
				return adapter.HookDecision{}, err
			}

			if active >= cfg.MaxConcurrentSubagents {
				return adapter.HookDecision{
					Block:  true,
					Reason: fmt.Sprintf("[swarm] Subagent budget exceeded (max %d active). Run sequentially or wait for active subagents to finish.", cfg.MaxConcurrentSubagents),
				}, nil
			}
		}

		if in.Command != "" {
//...
```

# Prompt / Rule additions
**File: `internal/install/skills/swarm-orchestrator/SKILL.md`**

Update the section under `## Owning an item` to proactively warn orchestrators of the limit, so they plan appropriately:
```markdown
- Delegate with `swarm_spawn`. Pick the role; leave agent/model empty to use the user's defaults. Limit your concurrent subagents. You have a strict budget (default 3 active subagents). Do not spawn more until some finish. If you receive a budget exceeded error, do not retry immediately; end your turn to wait for relays from finishing subagents.
```

# Verification scenarios
1. **Under budget:** An orchestrator with 0 active subagents invokes `swarm_spawn`. The `PreToolUse` hook queries the DB, finds 0 active, and allows the call. The tool executes successfully.
2. **At budget (Blocking):** An orchestrator has 3 subagents in `queued` or `active` state and attempts to spawn a 4th. The `PreToolUse` hook intercepts the call, counts 3 active, matches the `MaxConcurrentSubagents` limit, and returns `Block: true`. The orchestrator receives the error message and halts spawning.
3. **Release after completion:** One of the 3 active subagents finishes, transitioning its state to `finished` (or `failed`/`cancelled`). The orchestrator wakes up from the relay message and attempts to spawn again. The hook counts 2 active subagents and allows the call.

# Out of scope
- **Native tool background accounting:** Tracking the exact background lifecycle of native LLM tools (like `invoke_subagent`) via transcript scanning is out of scope. They are blocked if the Swarm budget is exceeded, but they do not cleanly increment/decrement the database budget tracking natively.
- **Global queue management changes:** This feature focuses on parent-agent limits. `MaxAgents` and `MaxAgentsPerRoot` limits and queueing logic remain untouched.

# Agent Swarm for OpenCode

Minimal wiring so OpenCode agents get the same Board mandate as the other 4 platforms.

## Use

Copy `plugin/opencode.json` MCP + instructions into your project `opencode.json`,
or merge the `swarm` MCP server entry manually:

- MCP server `swarm` runs `node bin/swarm-mcp.mjs` with `SWARM_URL=http://127.0.0.1:7777`.
- `instructions` loads `AGENTS.md` (contains the required `## Board (required)` block),
  `rules/swarm.md`, and `skills/swarm-task/SKILL.md`.

OpenCode reads `AGENTS.md` natively. Swarm deliberately uses no native hook
wiring: planning, joining, claiming, lifecycle changes, handoffs, and KB
promotion are explicit MCP actions governed by that file.

# Agent Swarm for OpenCode

Minimal wiring so OpenCode agents get the same Board mandate as the other 4 platforms.

## Use

Copy `plugin/opencode.json` MCP + instructions into your project `opencode.json`,
or merge the `swarm` MCP server entry manually:

- MCP server `swarm` runs `node bin/swarm-mcp.mjs` with `SWARM_URL=http://127.0.0.1:7777`.
- `instructions` loads `AGENTS.md` (contains the required `## Board (required)` block),
  `rules/swarm.md`, and `skills/swarm-task/SKILL.md`.

## Gap

OpenCode reads `AGENTS.md` natively, so the Board mandate is covered by
`plugin/AGENTS.md`. Unlike Claude Code / Cursor / Codex, there is no
plugin-host session-hook auto-registration here yet — heartbeat every ~5min
and `swarm_task_get` metadata verification rely on the agent following the
mandate plus MCP proxy auto-injection (session id, cwd, pid). Native
OpenCode hook wiring is future work.

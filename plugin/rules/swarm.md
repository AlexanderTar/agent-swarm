# Agent Swarm (Antigravity)

Session boundaries are tracked via the swarm MCP proxy lifecycle.
Use PreInvocation hooks for heartbeats. Check the board at http://127.0.0.1:7777.

<!-- swarm:start -->
Use MCP server `swarm` for task coordination and knowledge search.

## Board (required)
- Session start: `swarm_board` (repo filter), `swarm_task_join` or `swarm_task_stage claim` when starting work.
- During: `swarm_task_stage heartbeat` every ~5min, `swarm_task_update` summary + tags when goal/state changes.
- Tags: lowercase `["<repo>", "<area>", "<kind>"]` e.g. `["agent-swarm","daemon","bugfix"]`.
- Metadata auto-captured (session id, transcript path, model, cwd, pid) — do NOT hand-edit, verify via `swarm_task_get`.
- End: `swarm_handoff` with goal/done/next/decisions/gotchas/verification/files.
<!-- swarm:end -->

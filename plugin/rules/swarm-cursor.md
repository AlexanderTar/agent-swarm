# Agent Swarm (Cursor)

This workspace uses the local Agent Swarm board at http://127.0.0.1:7777.

Use MCP server `swarm` for board access and handoffs:
- `swarm_board` — list tasks on the Kanban board
- `swarm_task_get` / `swarm_task_create` / `swarm_task_stage` — manage tasks
- `swarm_handoff` / `swarm_pickup` — structured agent handoffs
- `swarm_kb_search` / `swarm_kb_get` / `swarm_kb_write` — knowledge base

At session start, call `swarm_board`. Before ending a multi-agent task, call `swarm_handoff`.

## Board (required)
- Session start: `swarm_board` (repo filter), `swarm_task_join` or `swarm_task_stage claim` when starting work.
- During: `swarm_task_stage heartbeat` every ~5min, `swarm_task_update` summary + tags when goal/state changes.
- Tags: lowercase `["<repo>", "<area>", "<kind>"]` e.g. `["agent-swarm","daemon","bugfix"]`.
- Metadata auto-captured (session id, transcript path, model, cwd, pid) — do NOT hand-edit, verify via `swarm_task_get`.
- End: `swarm_handoff` with goal/done/next/decisions/gotchas/verification/files.

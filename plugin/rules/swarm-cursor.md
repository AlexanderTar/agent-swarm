# Agent Swarm (Cursor)

This workspace uses the local Agent Swarm board at http://127.0.0.1:7777.

Use MCP server `swarm` for board access and handoffs:
- `swarm_board` — list tasks on the Kanban board
- `swarm_task_get` / `swarm_task_create` / `swarm_task_stage` — manage tasks
- `swarm_handoff` / `swarm_pickup` — structured agent handoffs
- `swarm_kb_search` / `swarm_kb_get` / `swarm_kb_write` — knowledge base

At session start, call `swarm_board`. Before ending a multi-agent task, call `swarm_handoff`.

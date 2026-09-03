# Agent Swarm (Claude Code)

This plugin syncs your Claude Code sessions to the local Agent Swarm board at http://127.0.0.1:7777.

- Hooks auto-create session tiles and track tool usage
- Use `/handoff` or swarm-handoff skill before switching agents
- MCP tools: swarm_board, swarm_task_stage, swarm_handoff, swarm_pickup, swarm_kb_search

## Board (required)
- Session start: `swarm_board` (repo filter), `swarm_task_join` or `swarm_task_stage claim` when starting work.
- During: `swarm_task_stage heartbeat` every ~5min, `swarm_task_update` summary + tags when goal/state changes.
- Tags: lowercase `["<repo>", "<area>", "<kind>"]` e.g. `["agent-swarm","daemon","bugfix"]`.
- Metadata auto-captured (session id, transcript path, model, cwd, pid) — do NOT hand-edit, verify via `swarm_task_get`.
- End: `swarm_handoff` with goal/done/next/decisions/gotchas/verification/files.
- Pass your model explicitly on create/join (e.g. model: "claude-opus-4-5") — you know your own model; auto-capture is best-effort.

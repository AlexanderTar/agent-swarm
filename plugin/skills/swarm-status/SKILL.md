---
name: swarm-status
description: Check Agent Swarm board status, active tasks, and daemon health. Use at session start and before handoffs.
---

# Swarm Status

- Board UI: http://127.0.0.1:7777
- MCP: `swarm_board` for current tasks
- Your session is tracked automatically via hooks; call `swarm_task_stage` heartbeat during long tasks.

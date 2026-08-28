---
name: swarm-pickup
description: Claim a handoff task from the Agent Swarm board and continue where another agent left off.
---

# Swarm Pickup

1. Call `swarm_pickup` without a key to list open handoffs, or with a specific task key.
2. Read the returned pickup prompt completely.
3. Restate your plan to the user before making changes.
4. Call `swarm_task_stage` with action `heartbeat` periodically during long work.

---
name: swarm-pickup
description: Claim a handoff task from the Agent Swarm board and continue where another agent left off.
---

# Swarm Pickup

1. Call `swarm_pickup` without a key to list open handoffs, or with a specific task key.
2. Read the returned pickup prompt completely, then join the explicit task.
3. Claim the task before making changes. If another agent holds its lease, do not work concurrently.
4. Heartbeat only while you hold the claim token; submit to review or explicitly release it when finished.

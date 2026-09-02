---
name: swarm-pickup
description: Claim a handed-off board item and continue it. Use when the user asks you to pick up swarm work, or when you want the next available handoff.
---

# Swarm Pickup

1. `swarm_pickup` with no key lists open handoffs; with a key claims that one. The claim
   is exclusive and leased — use `swarm_task_join` instead if you are only helping.
2. Read the whole pickup prompt, then the files it names.
3. Restate the plan to the user before changing anything.
4. `swarm_task_stage {key, action: "heartbeat"}` during long work so the lease holds.
5. `swarm_task_update {key, summary}` as the picture changes — the inherited summary is
   the previous agent's, not yours.

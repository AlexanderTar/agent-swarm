---
name: swarm-handoff
description: Hand the current board item to another agent with a structured note. Use when you are stopping on work someone else should continue, or when the user asks for a handoff.
---

# Swarm Handoff

1. Bring the summary current first: `swarm_task_update {key, summary}`. The next agent
   reads that before anything else.
2. Call `swarm_handoff` with the task key and: goal, what is done, next steps, decisions
   taken, gotchas, verification commands, files touched, KB refs, open questions.
   Write each from this conversation — specific paths, specific commands.
3. Anything durable that is not specific to this task goes to `swarm_memory_write`.
4. Confirm it landed in the **ready** column at http://127.0.0.1:7777.

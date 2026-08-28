---
name: swarm-handoff
description: Write a structured handoff note and move the current task to handoff status. Use when the user asks to hand off work to another agent.
---

# Swarm Handoff

1. Gather: goal, what's done, next steps, decisions, gotchas, verification commands, relevant files, KB refs, open questions.
2. Call MCP tool `swarm_handoff` with the task key and structured note.
3. Confirm the task appears in the **ready** column at http://127.0.0.1:7777

Use sentinel sections when editing markdown manually:
`<!-- SWARM:NEXT_STEPS:BEGIN -->` ... `<!-- SWARM:NEXT_STEPS:END -->`

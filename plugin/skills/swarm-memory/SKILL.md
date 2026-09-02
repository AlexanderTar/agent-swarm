---
name: swarm-memory
description: Save or look up a durable fact in the Agent Swarm memory store. Use when you learn something worth keeping past this session (a decision, a gotcha, a non-obvious command) or before solving something that another agent may have hit already.
---

# Swarm Memory

Memories live in `~/.swarm/kb/memory/` and are searchable by every agent on this machine.

## Search first

`swarm_memory_search {query, limit?}` — natural language, e.g. "why does the daemon hold
swarm.db open". Do this before debugging something that smells previously solved.

## Write

`swarm_memory_write {title, body, tags?}` when you learn something that outlives the
session: a decision and its reason, a gotcha with its symptom, a command that is hard to
rediscover.

- `title` — the fact, not the topic: `Stop dev.swarm.daemon before migrating swarm.db`.
- `body` — markdown; what is true, why, and how you found out. Include the exact command
  or error text.
- `tags` — lowercase, e.g. `["agent-swarm", "sqlite", "launchd"]`.

Do not write a memory for something already in the repo's docs, or for state that will be
stale next week. That belongs in `swarm_task_update`.

For the wider knowledge base (task records, transcripts, notes) use `swarm_kb_search`
with an optional `subdir`, `swarm_kb_get {slug}`, and `swarm_kb_write`.

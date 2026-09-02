---
name: swarm-search
description: Search the shared knowledge base under ~/.swarm/kb for prior work — task records, agent transcripts, notes and decisions. Use before starting something another agent may already have done.
---

# Swarm KB Search

1. `swarm_kb_search {query, limit?, subdir?}` — natural language. `subdir` narrows it:
   `tasks` for board item records, `transcripts` for agent conversations, `memory` for
   durable facts, `notes` for hand-written notes.
2. `swarm_kb_get {slug}` reads one document in full, slug from the search hit.
3. `swarm_kb_write {subdir?, filename, title?, tags?, body}` writes and indexes a new
   document.

For durable facts specifically, `swarm_memory_search` / `swarm_memory_write` are the same
store scoped to `memory` — prefer them over passing `subdir: "memory"` here.

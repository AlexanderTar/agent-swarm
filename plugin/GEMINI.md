<!-- swarm:start -->
## Agent Swarm

Board: http://127.0.0.1:7777. Hooks track this session; they never create a board item.
You do, in your own words.

- `swarm_task_create {title, summary, tags}` when you begin work worth coordinating —
  a feature, an investigation, something another agent may continue. Skip trivial,
  throwaway and read-only turns.
- `swarm_task_join {key}` to help with work already on the board. Additive: it takes
  the item away from nobody.
- `swarm_task_update {key, summary, status, tags}` whenever the goal, state or next
  step changes. The summary is what the next agent reads.
- `swarm_task_stage` moves it between columns; `swarm_handoff` before another agent
  takes over; `swarm_pickup` to claim a handoff.
- `swarm_memory_write` / `swarm_memory_search` for facts, decisions and gotchas that
  outlive this session.

Write `title` and `summary` from this conversation, not from a template. Title:
specific, human-readable, under ~60 chars. Summary: 2-5 sentences covering goal,
current state, next step. Tags: lowercase filter labels, e.g.
`["agent-swarm", "daemon", "bugfix"]`.

The daemon binds your MCP calls to the session running in this working directory — you
do not need to pass `sessionId`.
<!-- swarm:end -->

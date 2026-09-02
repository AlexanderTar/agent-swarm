---
name: swarm-task
description: Create, update or join an Agent Swarm board item. Use when you begin work worth coordinating with other agents, when the goal or state of that work changes, or when you want to help with work already on the board at http://127.0.0.1:7777.
---

# Swarm Task

Hooks track your session but never create a board item. You create it, and you write
the title and summary yourself.

## Create

1. Decide whether it is worth a tile. Multi-step work, anything another agent may
   continue, anything the user will want to see progress on. Not a single file read,
   a question, or a throwaway command.
2. Check you are not duplicating: `swarm_board {repo: "<repo path>"}`. If the work is
   already there, join it instead (below).
3. Call `swarm_task_create` with `title`, `summary`, `tags`, and `sessionId` if a
   session briefing gave you one. Optional: `status`, `repoPath`, `branch`.

`title` — specific, human-readable, under 60 characters, no agent or platform name, no
generated slug.

- good: `Stop the board creating a tile per session`
- bad: `codex-session-4f2a1b task`

`summary` — 2 to 5 sentences written from this conversation: what the goal is, where it
stands now, what happens next. No template, no restating the title.

- good: `Hooks call upsertSessionTask on every event, so each session gets its own tile
  and the board is unreadable. Removing that call and moving item creation into
  swarm_task_create. Schema v3 migration is written and passing; the hook route rewrite
  is next. Existing hook-spawned tiles get archived, not rewritten.`
- bad: `This task involves working on the board. Progress is ongoing. Next steps will be
  determined.`

`tags` — lowercase filter labels, e.g. `["agent-swarm", "daemon", "bugfix"]`. Repo name,
area, kind of work.

## Update

Call `swarm_task_update {key, summary}` whenever the goal, state or next step changes —
at minimum when you finish a phase and before you stop. Rewrite the summary; do not
append to it. Also takes `title`, `status`, `tags`, `addTags`, `removeTags`.

## Join

`swarm_task_join {key}` attaches your session to an existing item so several agents show
on one tile. It is additive and takes the item from nobody. Use `swarm_pickup` instead
when you are claiming a handoff exclusively.

---
name: swarm-task
description: Claim, join, tag, and track Agent Swarm board tasks with heartbeat and handoff. Use when starting, updating, or finishing any board task.
---

# Swarm Task

Board UI: http://127.0.0.1:7777

## Board (required)
- Session start: `swarm_board` (repo filter), `swarm_task_join` or `swarm_task_stage claim` when starting work.
- During: `swarm_task_stage heartbeat` every ~5min, `swarm_task_update` summary + tags when goal/state changes.
- Tags: lowercase `["<repo>", "<area>", "<kind>"]` e.g. `["agent-swarm","daemon","bugfix"]`.
- Metadata auto-captured (session id, transcript path, model, cwd, pid) — do NOT hand-edit, verify via `swarm_task_get`.
- End: `swarm_handoff` with goal/done/next/decisions/gotchas/verification/files.

## Claim vs join
- Unclaimed task: `swarm_task_join` claims it, or `swarm_task_stage` with action `claim`.
- Already claimed by another agent: `swarm_task_join` appends your session without stealing the claim — do NOT overwrite.
- If origin is `unknown`, claim/join backfills agent, session, model, cwd, pid automatically.

## Tags
- Always lowercase, deduped. Minimum: repo name, e.g. `["agent-swarm","daemon","bugfix"]`.
- Set on create via `swarm_task_create` tags, update via `swarm_task_update` or `swarm_task_stage` tags/addTags/removeTags.
- Update summary + tags whenever goal, state, or scope changes.

## Heartbeat
- Call `swarm_task_stage` with action `heartbeat` every ~5min during active work.
- Verify metadata via `swarm_task_get` — never hand-edit session id, transcript path, model, cwd, or pid.

---
name: swarm-task
description: Explicitly plan, join, claim, review, and hand off Agent Swarm board tasks. Use when starting, updating, reviewing, or finishing board work.
---

# Swarm Task

Board UI: http://127.0.0.1:7777

Read `AGENTS.md` for the universal contract. Work is explicit: a hook, session, tool call, subagent, reviewer, advisor, or CLI process never creates a task or moves one on your behalf.

## Plan and create tasks

1. Once a plan is approved, the coordinator calls `swarm_task_create` for one main task containing the full plan and context.
2. The coordinator explicitly creates one linked child task for each executable plan subtask.
3. Create no task for coordination chatter, advice, code review, a session, or a spawned agent.

## Claim vs join
- `swarm_task_join` records a participant only. It does not claim, steal a lease, or change a status.
- To work, call `swarm_task_claim`. A task has one active lease; if the claim fails, do not start concurrent work.
- Save the claim token from a successful claim. Use it for heartbeat, updates, submit, release, handoff, and review actions.

## Lifecycle

1. Claim `ready` work to move it to `in_progress`.
2. Heartbeat while actively working, then explicitly submit it for `review` and release the implementation lease.
3. A reviewer claims `review`, then explicitly approves to `done` or requests changes to `ready`.
4. Release or hand off incomplete implementation back to `ready`.
5. The coordinator, not a child task, explicitly advances and completes the main task.

Do not make a transition based on an inferred session event or a tool call.

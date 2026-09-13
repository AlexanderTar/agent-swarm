# Agent Swarm (Cursor)

Use the local Swarm MCP server and board at http://127.0.0.1:7777 for deliberate, plan-backed work. Cursor hooks, sessions, CLI-spawned agents, subagents, advisors, reviewers, tool calls, and handoffs **never create tasks automatically**.

## Plan before delegation

After the plan is approved, the coordinating agent explicitly creates one main task containing the objective, plan overview, context, constraints, and acceptance criteria. It then explicitly creates one child task for every executable plan subtask and links each child to the main task. Do not create tasks for research chatter, code review, advice, agent setup, or a spawned session.

## Participate, then claim

- Call `swarm_task_join` to record that you are observing or contributing. Joining is observe-only: it does not claim work or change a status.
- Before editing or reviewing a task, call `swarm_task_claim`. Only one active worker can hold a task lease. If the claim fails, do not begin work; contribute through notes or wait for an explicit handoff.
- Keep the returned claim token and use it for heartbeats, progress updates, submission, release, and review actions. Renew only while actively working.

## Explicit lifecycle

- A worker claims `ready` work, which moves it to `in_progress`.
- The worker explicitly submits finished implementation for `review` and releases the implementation lease.
- A reviewer explicitly claims review work, then approves it to `done` or requests changes to return it to `ready`.
- A worker explicitly releases or hands off unfinished work back to `ready`.
- The main-task coordinator explicitly updates parent progress and explicitly completes the main task after the required children are done. Child completion never completes the parent automatically.

Never infer task status from a prompt, session boundary, hook, transcript, tool call, or CLI process. Keep the board accurate with explicit actions only.

## Knowledge base

Use `swarm_kb_write` only to promote durable specifications, approved plans, and decisions. Keep handoffs, progress reports, review comments, compact summaries, transcripts, and tool output on the task rather than in the knowledge base.

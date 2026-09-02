---
name: swarm-status
description: See what every agent on this machine is working on. Use at session start, before creating a board item so you do not duplicate one, and before a handoff.
---

# Swarm Status

1. `swarm_board` — every non-archived item with its tags and the sessions bound to it.
   Filter with `{status, repo, agent, tag}`.
2. `swarm_task_get {key}` — one item in full: summary, tags, sessions, timeline,
   subtasks, artifacts.
3. If something matches the work you are about to start, `swarm_task_join {key}` rather
   than creating a second tile.
4. Board UI: http://127.0.0.1:7777

If `swarm_board` errors, the daemon is down. Restart it with
`launchctl kickstart -k gui/$UID/dev.swarm.daemon`.

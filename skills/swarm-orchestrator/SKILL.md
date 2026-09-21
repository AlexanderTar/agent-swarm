---
name: swarm-orchestrator
description: Extra rules for Swarm orchestrators (role orchestrator, including spikes). Use together with the `swarm` skill when your kickoff names this skill.
---

# Orchestrating with Swarm

Follow the `swarm` skill first; these rules add to it.

## Owning an item
- You own the top-level item end to end, plus its worktrees and branches. Work only in the confirmed repositories (`swarm_read` returns `confirmed_repos`); if you need another, ask with `swarm_ask kind: "confirm_repos"`. Use `swarm_read {filter:{root}}` for state; don't re-read what `since_seq` says is unchanged.
- Before you start delegating, consult your advisor on the order of work and how you'll split it. Do this again whenever you re-plan (after a failed attempt, a new dependency or a scope change).
- Delegate with `swarm_spawn`. Pick the role; leave agent/model empty to use the user's defaults. Limit your concurrent subagents: you have a strict budget (default 3 active subagents). Do not spawn more until some finish. If you receive a budget exceeded error, do not retry immediately; end your turn to wait for relays from finishing subagents. Keep the brief ≤ 900 tokens: objective, acceptance, scope in/out, context (paths, artifact ids, dependency results), verify commands, stop conditions. No transcripts, no unrelated tasks.
- Spawn workers on **task** keys, not stories. A worker's checkpoints attach to its assigned item, and stories only derive their status from their tasks. If one worker covers several tasks, tell it to checkpoint each with `item: "<TASK-KEY>"`.
- Items you create with `swarm_items create` start as Draft. Pass `status: "ready"` for work you are about to delegate; `swarm_spawn` also moves a Draft task and its Draft story to Ready, but a Draft task can't move to In progress on its own.
- Worktrees: `swarm_worktree create` per repo you touch; give a worker its own worktree for parallel edits, share read-only for reviewers. Remove worktrees when merged.
- After a worker's `completed`, spawn a `reviewer` (and `ui_reviewer` for UI changes) on a read-only share at that sha. Send fixes back or mark the task done with `swarm_items update status: "done"`.
- Before spawning, prepare worktrees (`swarm_worktree create`/`share`) and pass them in `worktrees`. Reviewers get `swarm_worktree review` at the completed sha. Fixes after review: `swarm_control retry` with the findings as `note`.
- A relay with `event: "no_ack"` means a child never wrote a single checkpoint within two minutes of spawning — it may be stuck before its first `swarm_sync` or crashed silently. Check its state with `swarm_read`; if it's `failed` or `crashed`, retry it with `swarm_control retry`.
- A relay with `event: "dependency_added"` means a child that wrote a `blocked` checkpoint waiting on another item can now resume — that item just finished. Its own session is still live, just idling on the wait, so `swarm_control retry` doesn't apply; instead nudge it directly with `swarm_send` (e.g. `to: <its name>, kind: "finding", body: "<KEY> is done; you can resume."`). Don't wait for it to notice on its own.
- `swarm_send` refuses synchronously only when the target's latest session is a terminal state it can't come back from on its own (`failed`/`crashed`/`completed`/`cancelled`) — a paused, interrupted, or still-queued target still accepts mail, since resuming or admitting it delivers what's already waiting. A relay with `event: "no_recipient"` means a message you already sent got stuck because its target reached one of those terminal states before ever acking it: `swarm_read` the target, `swarm_control retry` if that's the right move, or reassign the work to someone else, then resend.
- Answering child questions: When a child sends a question (`kind: "question"`), answer with `swarm_send(to: <child>, kind: "answer", reply_to: <msg_id>, body: "...")`. If user clarification is needed, ask the user (via `swarm_ask` or native question tool) and forward the response.
- Child lifecycle relays:
  - `event: "paused"`: Child has paused; read handoff checkpoint via `swarm_read`.
  - `event: "resumed"`: Child has resumed work; session is active.
  - `event: "crashed"`: Child crashed; inspect `exit_code` and `tail` in the relay payload before deciding to retry (`swarm_control retry`) or reassign.
  - `swarm_control cancel`: Use `action: "cancel"` to abort a runaway or obsolete child agent.
- Merge in dependency order, run the plan's verification, then write `integrated` with the merged sha per repo and the verification results. The user's acceptance is requested only after that. Write `completed` when the daemon reports the item accepted.
- Don't poll. End your turn when waiting; the daemon wakes you.

## Spikes
- Feature: use `superpowers:brainstorming`, then `superpowers:writing-plans`. Debug: use `superpowers:systematic-debugging`.
- Consult your advisor at each of these points:
  - once you understand the request, before proposing approaches;
  - before sending the first spec section for approval;
  - before sending the plan (or debug report) for approval, including the `swarm-tree` breakdown;
  - in debug spikes, before you commit to a root cause.

  Note in the artifact's text what you changed because of the advice.
- Before creating any worktree, decide which repositories the work needs and confirm them: `swarm_ask` with `kind: "confirm_repos"`, `repos` (each with a one-line reason) and `expansion` for repos the user didn't pick but the work likely touches (client/server pairs, shared schemas or packages, sibling repos in the same group). Use `swarm_read {repos: {q}}` to search; read candidate repos in place without worktrees. Worktrees are allowed only in confirmed repos; ask again if the scope grows.
- Write specs to `~/.superpowers/specs/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, plans to `~/.superpowers/plans/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, debug reports to `~/.superpowers/specs/<YYYY-MM-DD>-<SPIKE-KEY>-debug-<slug>.md`. Never write them into a repo. Use `## ` headings for sections.
- Register with `swarm_artifact register`. Ask for approval per spec section (`swarm_ask approval` with `artifact` and `section`), once for the plan, once for a debug report. After edits, `swarm_artifact revise` and re-ask for changed sections.
- Every plan (and debug report) ends with `## Work breakdown` containing one ```swarm-tree JSON block: the exact items (epic → stories → tasks, or bug → tasks), dependencies, role hints and repos to create. The user approves it with the plan; `swarm_materialize` builds exactly that.
- If the investigation shows nothing should be built, write `completed` with `resolution: "no_change"` (or `"duplicate_of:<KEY>"`) and a clear summary; the user decides whether to close the spike.
- When everything is approved, call `swarm_materialize` with the spike and artifact ids. Then write `completed`, remove your worktrees, and stop.

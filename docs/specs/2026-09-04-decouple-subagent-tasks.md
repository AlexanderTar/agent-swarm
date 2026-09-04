# Decouple Tasks from Subagent Sessions

## Context
In `agent-swarm`, coding agents (Claude, Cursor, Antigravity/AGY, OpenCode, Codex) run sessions. Previously, subagents spawned inside coding agents (for example Claude's `Agent` / `task` tool, Cursor's `subagents`, or Antigravity's `invoke_subagent`) were treated as distinct board sessions. Every subagent lifecycle event (`SubagentStart`, `subagentStart`) and every tool execution (`PreToolUse`, `PostToolUse`, `afterFileEdit`) resolved the subagent ID as a board session ID and invoked `upsertSessionTask`, generating a new top-level `SW-xxx` task tile for every subagent. Furthermore, an overly broad regex in `subagentIdFromTranscriptPath` extracted `transcript_full` from Antigravity transcript paths, improperly treating Antigravity root sessions as subagents named `transcript_full`.

This resulted in over 100 task tiles flooding the board.

This change decouples tasks from subagents:
1. Subagents never create board tasks (`tasks` rows).
2. Subagent starts, stops, and tool executions attach to their parent/root session task as subtasks and task events.
3. Subagent sessions are registered in `task_sessions` attached to the root task ID.
4. Top-level root sessions retain their task tile on the board.
5. Local boards can be cleaned up of orphaned/excessive subagent task rows.

## Locked Decisions
1. **Never create a task for subagents**: Under no circumstances should `SubagentStart`, `subagentStart`, or any subagent tool/message event call `upsertSessionTask` or create a new board task.
2. **Attach to root task**: If the root session has an active/known task, subagent starts add a subtask (`addSubtask`) and record `subagent_start` events; subagent stops complete the subtask (`completeSubtask`) and record `subagent_stop` events; subagent sessions attach to `task_sessions` with `task_id` pointing to the root task.
3. **No-op when parent has no task**: If a subagent fires hooks but no parent task exists, the daemon acknowledges cleanly (`{ ok: true }`) without creating an orphaned task.
4. **Fix transcript subagent parsing**: `subagentIdFromTranscriptPath` must only match paths explicitly inside a `subagents/` folder (e.g. `[/\\]subagents[/\\]([^/\\]+)\.jsonl`), and never fall back to base filenames like `transcript_full`.
5. **Support all 5 agents**: Claude (`agent_id` distinct from `session_id`, `SubagentStart`/`SubagentStop`), Cursor (`parent_conversation_id`, `subagent_id`, `subagents/`), Antigravity (`parentSessionId` / `parent_conversation_id`), OpenCode (`parentSessionId`), Codex (`parentSessionId` / subagent marker).

## DB Models
No changes to SQLite database tables required. Existing schema supports:
- `tasks`: top-level board tasks
- `task_sessions`: junction table tracking multiple sessions attached to a task (`task_id`, `session_id`, `agent_kind`, `cwd`, `model`, `pid`, `transcript_path`)
- `task_events`: task event log (`task_id`, `event_type`, `payload_json`)
- `subtasks`: subtasks on a task (`task_id`, `subject`, `description`, `completed`)

A migration/cleanup function is provided to clean up existing subagent tasks in the database:
- Identifies tasks created for subagents (matching `Subagent · *` or known subagent patterns).
- If parent task exists, moves any subagent session mappings into parent's `task_sessions` and archives/removes the redundant task.
- Unifies split Antigravity sessions previously keyed on `transcript_full`.

## Model / API Types

### `packages/core/src/hooks.ts`
```typescript
/** Check if a hook payload originates from a nested subagent. */
export function isSubagentHook(input: NormalizedHookInput, event?: string): boolean;

/** Resolve the root session ID that owns the task for this hook. */
export function resolveHookRootSessionId(input: NormalizedHookInput): string;
```

### Signature changes in `packages/daemon/src/routes.ts`
- Hook handler checks `isSubagentHook(input, event)`.
- If `isSubagentHook` is true:
  - Finds parent task using `resolveHookRootSessionId(input)`.
  - Attaches subtask / records events / attaches session to parent task.
  - Skips `ensureBoardTask` and `upsertSessionTask`.
- If false (root session):
  - Retains existing root task behavior.

## Screens
The board UI (`http://127.0.0.1:7777`) remains unchanged visually, but cleanly displays only genuine root tasks and user tasks:
```
+-----------------------------------------------------------------------------------+
| Ready              | In Progress               | Review             | Done        |
+--------------------+---------------------------+--------------------+-------------+
| SW-101             | SW-239                    | SW-471             | SW-485      |
| Task title         | Root session task         | Root session task  | Root task   |
| [claude] [repo]    | [antigravity] [repo]      | [claude] [endurio] | [claude]    |
|                    | ┖ Subtasks (2/2 done)     |                    |             |
|                    |   ✔ Research auth         |                    |             |
|                    |   ✔ Run tests             |                    |             |
+--------------------+---------------------------+--------------------+-------------+
```
Subagents do not appear as separate tiles; instead, their progress appears under the parent task's subtasks and event drawer.

## All User-Facing Copy
No new user-facing copy. Existing CLI / status messages remain identical.

## File List
- `packages/core/src/hooks.ts`: Add `isSubagentHook`, fix `subagentIdFromTranscriptPath`, add `resolveHookRootSessionId`.
- `packages/core/src/hooks.test.ts`: Add comprehensive test coverage for subagent detection and root session resolution across all 5 agents.
- `packages/core/src/tasks.ts`: Add board cleanup function to archive or remove obsolete subagent tiles and repair `transcript_full` artifacts.
- `packages/daemon/src/routes.ts`: Update hook routing so subagents never upsert tasks, but instead route events and subtasks to parent tasks.
- `packages/daemon/src/routes.test.ts`: Add end-to-end hook route tests verifying subagents do not create tasks.
- `packages/cli/src/commands.ts`: Expose `swarm board clean` or cleanup helper.

## Verification
1. `pnpm test` across all packages (`@swarm/core`, `@swarm/cli`, `@swarm/daemon`).
2. Run simulated subagent hook calls from Claude, Cursor, Antigravity, OpenCode, Codex to ensure:
   - Root sessions create/revive tasks.
   - Subagents create subtasks and task events on the root task.
   - Subagents never create a new task tile.
   - Subagent without parent session does not crash or create a task.
3. Build and redeploy daemon locally: `pnpm build`, restart daemon service.
4. Clean local board: verify board task count drops from 106 to genuine tasks.

## Explicitly Out of Scope
- Changing how manual tasks are created via MCP `swarm_task_create` or CLI.
- Changing board styling or layout components.

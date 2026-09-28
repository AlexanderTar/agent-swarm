# Spec: orchestrator progress as a native to-do list

## Context

**Request (2026-09-28):** orchestrators should track their top-level item's progress with each
agent kind's native task/todo tool, uniformly across agent kinds. For an epic, bug or chore
the list must show the item's **actual tasks** progressing. For a spike it shows the spike
steps.

**Today:** nothing tracks this. Progress lives in checkpoints (`next[]`), item statuses and
workflow state. No instruction text varies by agent kind; kind differences are prose in the
shared skills.

**Native tools (probed 2026-09-28):**

| Kind | Tool | Shape |
|---|---|---|
| claude 2.1.283 | `TaskCreate` / `TaskUpdate` (default on) | subject, status per task |
| codex 0.158 | `update_plan` | `plan:[{step,status}]`, at most one `in_progress` |
| cursor | `TodoWrite` | `todos:[{id,content,status}]`, `merge` |
| muse 1.4 | `write_todos` | `todos:[{text,status}]` |
| agy 1.2 | `task.md` artifact in its brain dir | markdown checklist `[ ]` `[/]` `[x]` |

**Approach (user chose "C": the agent reports and Swarm owns the list):**
- The daemon **computes** the list and hands it to the orchestrator.
- The orchestrator mirrors the list into its native tool, so it shows in the TUI.
- The same list shows in the menubar and on the web.
- Nothing is scraped from native tools.

**Collision warnings:** the next free migration is 0021. The skills are duplicated in `skills/`
and `internal/install/skills/`, and the two copies must stay byte-identical.

## Locked decisions

1. **Sources.** Only a root item has a list. It is computed by one function,
   `runtime.(*Store).Todos(ctx, rootItemID)`.
   - **Epic, bug, chore:**
     - One entry per task under the root, at any depth; cancelled tasks are skipped.
     - Order: the parent story's `sort_order`, then the task's `sort_order`, then the key.
       Tasks directly under the root come first.
     - Then two fixed entries: `integrate` "Merge + verify" and `accept` "User acceptance".
     - A chore with zero tasks gets `work` "Do the work" before those two.
   - **Spike, feature:** `frame` Frame the request · `research` Research · `design` Design ·
     `spec` Spec approval · `plan` Write plan · `critic` Completeness check ·
     `approve` Plan approval + materialize.
   - **Spike, debug:** `frame` Frame the problem · `evidence` Reproduce + gather evidence ·
     `root_cause` Root cause · `report` Debug report approval · `plan` Plan fix packages ·
     `critic` Completeness check · `approve` Approval + materialize.
   - Any other root type (a spike with intent `chore`) gets no list, returned as `nil`.
2. **Task status mapping:**
   - `done` → `completed`;
   - `in_progress`, `in_review`, `awaiting_approval` → `in_progress`;
   - `draft`, `ready`, `blocked` → `pending`.
   - Several tasks may be `in_progress` at once.
3. **Fixed-entry status, as facts the daemon owns:**
   - `integrate`:
     - `completed` when the root has an `integrated` checkpoint;
     - otherwise `in_progress` once every listed task is completed and there is at least one;
     - otherwise `pending`.
   - `accept`:
     - `completed` when the root is `done`;
     - `in_progress` when `integrate` is completed;
     - otherwise `pending`.
   - `work` (a chore with no tasks):
     - `completed` when `integrate` is completed;
     - otherwise `in_progress`.
4. **Spike statuses.**
   - The orchestrator reports them on `swarm_checkpoint` as `todos: [{id, status}]`.
   - The ids it leaves out keep their previous status.
   - The daemon merges the new list over the newest stored one and stores the full merged
     list.
   - The daemon then overrides two steps with facts it owns:
     - `spec` is `completed` when the spike's newest spec artifact passes
       `checkEverySectionApproved`;
     - `approve` is `completed` when any item has `origin_spike_id` = the spike.
   - Steps never reported default to `pending`.
5. **Validation on `swarm_checkpoint.todos`, in this order:**
   1. The caller's role is not orchestrator → ignore the list. The checkpoint succeeds, and the
      result carries `todos_ignored`.
   2. The item is not a spike → refuse the checkpoint.
   3. There is an unknown id or a bad status → refuse.
   4. More than one step is `in_progress` after the merge → refuse.
6. **Delivery.** `swarm_sync` gets a `todos` field, sent only when it has changed.
   - This applies only to sessions whose agent role is orchestrator and whose item is a root.
   - The daemon computes the list and hashes it: sha256 of the list's JSON.
   - If the hash differs from `sessions.todos_sent_hash`, the daemon sets `todos` and stores the
     hash in the same transaction as the sync.
   - A new session has a NULL hash, so it always receives the list.
7. **Skill rule** (swarm-orchestrator): when `swarm_sync` returns `todos`, replace the native
   list with it, using the labels verbatim:
   - Codex: `update_plan` allows one `in_progress`. Mark the first one, and put `▶ ` in front of
     the label of the others while keeping them `pending`.
   - agy: rewrite `task.md` with `[x]` for completed, `[/]` for in progress, `[ ]` for pending.
   - Spike orchestrators send step statuses with each checkpoint and never add steps.
8. **No history table and no relays.** The list refreshes whenever the orchestrator next syncs,
   and every wake ends in a sync.

## DB models

Migration `internal/db/schema/0021_orchestrator_todos.sql`:

```sql
-- 0021_orchestrator_todos.sql: spike step statuses on checkpoints; last todos hash sent per session.
ALTER TABLE checkpoints ADD COLUMN todos_json TEXT;
ALTER TABLE sessions ADD COLUMN todos_sent_hash TEXT;
```

- `checkpoints.todos_json`:
  - the merged full spike step list, `[{"id":"frame","status":"completed"},…]`;
  - NULL when the checkpoint carried no todos.
- `sessions.todos_sent_hash`: the hex sha256 of the last list delivered on `swarm_sync`.
- After the migration, health reports `schema: 21`.

## Model / API types

Go (`internal/runtime/todos.go`, new):

```go
type TodoStatus string // "pending" | "in_progress" | "completed"

type Todo struct {
	ID      string     `json:"id"`                 // "TASK-12" or a step id like "spec"
	Label   string     `json:"label"`              // "TASK-12 · Add login form" / "Spec approval"
	Status  TodoStatus `json:"status"`
	ItemKey string     `json:"item_key,omitempty"` // task entries only
}

type TodoReport struct {
	ID     string     `json:"id"`
	Status TodoStatus `json:"status"`
}

type TodoProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"` // first in_progress label, else first pending, else ""
}

func (s *Store) Todos(ctx context.Context, rootItemID string) ([]Todo, error) // nil for no list
func ProgressOf(todos []Todo) *TodoProgress                                        // nil for empty
```

- `CheckpointInput` gains `Todos []TodoReport`, and `CheckpointResult` gains
  `TodosIgnored string`.
- `SyncResult` gains `Todos []Todo`, filled only when the list changed.

MCP:
- The `swarm_checkpoint` schema adds
  `"todos":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["id","status"]},"description":"Spike orchestrators only: step statuses by id; omitted ids keep their status."}`.
  The result adds `todos_ignored` when the list was set aside.
- `swarm_sync` output adds `todos` when present.

HTTP:
- `GET /api/items/{key}` adds `"todos": Todo[]`, only for root items that have a list.
- The agents API's `agentNodeWire` adds `"progress": {done,total,current}`, only for orchestrator
  agents whose item has a list.

Web (`web/src/types.ts`):

```ts
export type TodoStatus = 'pending' | 'in_progress' | 'completed';
export interface Todo { id: string; label: string; status: TodoStatus; item_key?: string }
// ItemDetail gains: todos?: Todo[];
```

Swift (`Wire.swift`): `public struct AgentProgress: Codable, Sendable, Equatable { done: Int; total: Int; current: String }`,
and `AgentNode` gains `progress: AgentProgress?`.

## Screens

**Menubar, orchestrator row.** When `progress` is present, the subtitle's middle part changes
from `step ?? itemKey` to `"<done>/<total> · <current>"`, or `"<done>/<total> · Done"` when
`current` is empty:

```
◉ fix-login-redirect                     ●
  Orchestrator · 3/8 · TASK-12 · Add login form · Running
```

- Worker rows are unchanged, and so is an orchestrator with no `progress`.
- The row truncates the subtitle at one line, as it does today.

**Web, item Details panel.** When `todos` is present, a "Progress" block sits above the
checkpoints:

```
Progress  3/8
 ✓ TASK-10 · Schema migration
 ✓ TASK-11 · API handler
 ▶ TASK-12 · Add login form
 ▶ TASK-13 · Session cookie
 ○ TASK-14 · Logout
 ○ Merge + verify
 ○ User acceptance
```

- Each task entry (`item_key` present) links to that item's Details.
- Icons: ✓ completed, ▶ in progress, ○ pending, using existing text tokens only.
- There is no empty state: without `todos`, the block is absent.
- Not shown: progress bars, card badges, list history.

## User-facing copy

Checkpoint errors:
- `todos: unknown step "<id>" for a <feature|debug> spike; use: <comma-separated ids>`
- `todos: status must be pending, in_progress or completed (got "<s>")`
- `todos: at most one step may be in_progress (<id>, <id>)`
- `todos are derived from tasks for <KEY>; don't send them`

Checkpoint result: `todos_ignored: "only the orchestrator keeps a task list"`.

Web: the block heading is `Progress`, followed by ` <done>/<total>`.

Menubar: `Copy.progressDone = "Done"`.

**Skill text** (appended to `swarm-orchestrator` under "Owning an item"):

```
- Progress list: when `swarm_sync` returns `todos`, replace your native to-do list with it — labels verbatim, same order, same statuses. Claude: `TaskCreate`/`TaskUpdate`; Codex: `update_plan` (only one `in_progress` is allowed: mark the first, prefix the other running labels with "▶ " and keep them `pending`); Cursor: `TodoWrite` with `merge: false`; Muse: `write_todos`; agy: rewrite your `task.md` artifact (`[x]` completed, `[/]` in progress, `[ ]` pending). Don't add, rename or drop entries. For an epic, bug or chore the list is the item's tasks and is maintained by Swarm; never send `todos` for it.
```

And under "Spikes":

```
- Report spike step progress on each checkpoint with `todos: [{id, status}]` using the ids from your list (feature: frame, research, design, spec, plan, critic, approve; debug: frame, evidence, root_cause, report, plan, critic, approve). Keep at most one `in_progress`. Swarm ticks `spec` and `approve` itself.
```

## File list

**Changed:**
- `internal/db/schema/0021_orchestrator_todos.sql` (new).
- `internal/runtime/todos.go` and `todos_test.go` (new): `Todos`, `ProgressOf`, templates,
  status mapping, merge and validation.
- `internal/runtime/checkpoint.go`: `CheckpointInput.Todos`, validation, and storing
  `todos_json`.
- `internal/runtime/inbox.go`: `SyncResult.Todos` and the hash compare-and-store.
- `internal/mcpserver/tools.go`: the checkpoint schema and handler, and the sync output.
- `internal/httpapi/items.go`: `todos` on the item detail.
- `internal/httpapi/runtime.go`: `progress` on `agentNodeWire`.
- Web: `web/src/types.ts`, `web/src/panels/Details.tsx`, and
  `web/src/components/TodoList.tsx` (new).
- Menubar: `apps/menubar/Sources/SwarmBarKit/Wire.swift`, `AgentActions.swift` (`subtitle`),
  `Copy.swift`, and tests.
- Skills: `skills/swarm-orchestrator/SKILL.md`, `skills/swarm-spike/SKILL.md` (one line
  pointing at the todos rule), and the byte-identical copies in `internal/install/skills/`.

**Reused unchanged:** `checkEverySectionApproved`, items `sort_order`, and the
`Integrated` checkpoint kind.

**Deleted:** none.

## Verification

1. `gofmt -l .`, `go vet ./...`, `go test ./...`, `cd web && npm test && npm run build`,
   `cd apps/menubar && swift test`.
2. Migration: apply 0021 to a copy of the live DB and confirm the row counts are unchanged and
   that `PRAGMA table_info` shows both columns.
3. Scenarios, each a test:
   - **Epic tree** (story A: tasks 1 and 2; story B: task 3; one cancelled task):
     - the list has the tasks in order, then `integrate` and `accept`;
     - the cancelled task is absent;
     - the status mapping holds for every item status.
   - **Chore with zero tasks** → `work`, `integrate`, `accept`. Its integrated checkpoint →
     `work` and `integrate` completed, `accept` in progress.
   - **Feature spike:**
     - a report of `frame` completed then `research` in progress merges correctly;
     - an unknown id is refused with the exact copy;
     - two `in_progress` steps are refused;
     - `spec` is completed when every section is approved;
     - `approve` is completed after materialize.
   - **Debug spike** uses the debug step ids.
   - **A worker sends todos** → the checkpoint is ok and `todos_ignored` is set.
   - **An orchestrator sends todos on an epic** → refused with the exact copy.
   - **Sync delivery:**
     - the first sync returns `todos`;
     - a second sync with no change omits it;
     - a task moving to done makes the next sync return it again;
     - a new session gets it.
   - **A worker session's sync** never carries `todos`.
   - **HTTP:** the item detail has `todos` for a root and none for a task. An orchestrator
     agent node has `progress`; a worker's has none.
   - **Menubar:** the subtitle reads `3/8 · TASK-12 · …`, reads `8/8 · Done` when finished, and
     stays unchanged without progress.
   - **Web:** `TodoList` renders the icons, the links and the `3/8` heading.
4. Live, after deploy:
   - Start a small chore orchestrator on Claude and on Codex.
   - Confirm that the native list appears in the TUI, then check the menubar subtitle and the
     web Progress block.

## Explicitly out of scope

- Reading or scraping native to-do tools in hooks, transcripts or `task.md`.
- Waking the orchestrator just because a task's status changed.
- Lists for workers, stories or non-root items.
- Progress bars, board card badges and notifications.
- Enforcing that the agent actually updated its native list.

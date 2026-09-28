# Orchestrator To-Do List Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The daemon computes a root item's progress list (tasks for epic/bug/chore, fixed steps for a spike), hands it to the orchestrator on `swarm_sync` when it changes, and shows it on the web Details panel and the menubar orchestrator row.

**Architecture:** One function, `runtime.(*Store).Todos` (tx form `todosTx`), derives the list from item rows, the `integrated` checkpoint, the root status, and — for spikes — the newest `checkpoints.todos_json` plus two daemon-owned facts (spec approved, materialized). `swarm_checkpoint` accepts spike step statuses and stores the merged list; `swarm_sync` sends the list only when its sha256 differs from `sessions.todos_sent_hash`. HTTP exposes `todos` on item detail and `progress` on orchestrator agent nodes; web and menubar render them.

**Tech Stack:** Go (modernc sqlite), React 19 + Vitest, SwiftUI/XCTest (SwarmBarKit), Markdown skills.

**Spec:** `docs/specs/2026-09-28-orchestrator-todos.md` (approved; decisions are locked — do not reopen them).

## Global Constraints

- Migration number is `0021`; after it `db.SchemaVersion` is `21` and health reports `schema: 21`.
- Todo statuses are exactly `pending`, `in_progress`, `completed`.
- Fixed entries: `integrate` "Merge + verify", `accept` "User acceptance", `work` "Do the work" (chore with zero tasks only).
- Feature spike steps: `frame` Frame the request · `research` Research · `design` Design · `spec` Spec approval · `plan` Write plan · `critic` Completeness check · `approve` Plan approval + materialize.
- Debug spike steps: `frame` Frame the problem · `evidence` Reproduce + gather evidence · `root_cause` Root cause · `report` Debug report approval · `plan` Plan fix packages · `critic` Completeness check · `approve` Approval + materialize.
- Task label format: `"<KEY> · <title>"` (U+00B7 middle dot, one space each side).
- Error/result copy is verbatim from the spec "User-facing copy" section (repeated in each task below).
- `skills/<name>/SKILL.md` and `internal/install/skills/<name>/SKILL.md` must stay byte-identical.
- Never write to `~/.swarm/swarm.db`; the migration check runs on a `.backup` copy.
- Git: stage explicit paths only (never `git add -A`), never `--amend`. Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV
  ```
- Ponytail: no abstractions beyond what is written here. No new dependencies.

## Execution order

```
T1 ──► T2 ──► T3 ──► T4 ──► T5 ──► T7 ──► T9
T6 (web)    : any time, parallel with anything
T8 (skills) : any time after T1 is committed, parallel with T2–T7
```

- **T1–T5 run strictly one after another, even though their files are disjoint.** They share one worktree and one Go module: a half-edited `internal/runtime` breaks compiling `internal/mcpserver` and `internal/httpapi` tests. Never run two Go tasks at once.
- **T6** (web) touches only `web/` — safe to run in parallel with any task.
- **T8** (skills) touches `skills/` + `internal/install/`. `go list -deps ./internal/install` shows it imports `internal/db` and `internal/execx` only (not `internal/runtime`), so it is parallel-safe with T2–T7, but not with T1 (T1 edits `internal/db`).
- **T7** (menubar) must follow **T5**: adding `progress` to `apps/menubar/Tests/Fixtures/state.json` makes `TestMenubarContract` (`DisallowUnknownFields` on `agentNodeWire`) fail until T5's Go field exists.
- **T9** (final verification) runs last.

## Resolved spec ambiguities (implementers: treat as locked)

1. **In-progress count uses the list the orchestrator sees.** Validation step 4 counts `in_progress` *after* the daemon overrides (`spec`/`approve` forced to `completed`). The stored `todos_json` is the merged report list *before* overrides.
2. **Chore-intent spike sending `todos`** is refused with the "derived from tasks" copy (`todos are derived from tasks for <KEY>; don't send them`), same as a non-spike.
3. **"Then the key"** means natural key order: `ORDER BY length(key), key` (so `TASK-9` < `TASK-10`). Stories with equal `sort_order` also tie-break by `length(key), key`, which keeps each story's tasks together.
4. **`todos: []`** is the same as omitting it (`len(in.Todos) > 0` gates everything).
5. **Web placement:** checkpoints are a tab, not a section, so `TodoList` sits after `WorkflowSection` and before the Overview/Checkpoints tablist.
6. **Stored list is full:** `todos_json` holds every template step (unreported ones as `pending`), in template order.
7. **`<KEY>` in the "derived from tasks" copy** is the checkpoint's item key (`it.Key`).
8. **Menubar sketch's "· Running"**: the existing `subtitle` omits the state label while running (`AgentActionsTests.swift:195`). Tests follow the code: `Orchestrator · 3/8 · TASK-12 · Add login form`.

---

### Task 1: Migration 0021 + `Todos` / `ProgressOf`

**Files:**
- Create: `internal/db/schema/0021_orchestrator_todos.sql`
- Modify: `internal/db/db.go:17` (`SchemaVersion = 21`)
- Create: `internal/db/schema_0021_orchestrator_todos_test.go`
- Create: `internal/runtime/todos.go`
- Create: `internal/runtime/todos_test.go`

**Interfaces:**
- Consumes: `(*Store).checkEverySectionApproved(ctx, tx txQuerier, artifactID string) error` (materialize.go:25 — widen the param from `*sql.Tx` to `txQuerier`, body unchanged); `items.Type`, `items.Status` constants; test helpers `newStore`, `seedSectionApproval`, `seedTopLevelItem` (checkpoint_test.go:619), `StartSpike`, `StartOrchestrator`, `LatestSession`.
- Produces (used by T2–T5):
  ```go
  type TodoStatus string
  const (TodoPending TodoStatus = "pending"; TodoInProgress = "in_progress"; TodoCompleted = "completed")
  type Todo struct { ID, Label string; Status TodoStatus; ItemKey string } // json: id,label,status,item_key(omitempty)
  type TodoReport struct { ID string; Status TodoStatus }                  // json: id,status
  type Progress struct { Done, Total int; Current string }                 // json: done,total,current
  type todoStep struct{ ID, Label string }
  var spikeSteps map[string][]todoStep                                      // keys "feature","debug"
  func (s *Store) Todos(ctx context.Context, rootItemID string) ([]Todo, error)          // own DB.Tx
  func (s *Store) todosTx(ctx context.Context, tx todoQuerier, rootItemID string) ([]Todo, error)
  func (s *Store) latestTodoReports(ctx context.Context, tx todoQuerier, itemID string) ([]TodoReport, error)
  func (s *Store) spikeTodos(ctx context.Context, tx todoQuerier, spikeID string, steps []todoStep, reports []TodoReport) ([]Todo, error)
  func ProgressOf(todos []Todo) *Progress
  ```
  **Rule:** `Todos` reads through `s.DB` with no transaction (no write lock; `/api/state` calls it per orchestrator per poll). Code already inside a transaction (`WriteCheckpoint`, `Sync`) must call `todosTx(ctx, tx, id)`, never `Todos`, so it reads its own uncommitted writes and never waits on its own lock. The read helpers take `todoQuerier` (`txQuerier` from requests.go:149 plus `QueryContext`); change `checkEverySectionApproved`'s `tx *sql.Tx` parameter to `tx txQuerier` (it only calls `QueryRowContext`; existing callers pass `*sql.Tx` unchanged).

- [ ] **Step 1: Write the failing migration test**

`internal/db/schema_0021_orchestrator_todos_test.go`:

```go
package db

import "testing"

func TestOrchestratorTodosMigrationAddsBothColumnsAndKeepsRows(t *testing.T) {
	raw := openFixtureAtVersion(t, 20)
	if _, err := raw.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_progress', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	before := tableRowCount(t, raw, "items")
	continueMigratingTo(t, raw, 20, 21)
	for table, col := range map[string]string{"checkpoints": "todos_json", "sessions": "todos_sent_hash"} {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s.%s missing after 0021", table, col)
		}
	}
	if after := tableRowCount(t, raw, "items"); after != before {
		t.Errorf("items rows = %d, want %d", after, before)
	}
}
```

- [ ] **Step 2: Run it, watch it fail**

Run: `go test ./internal/db -run TestOrchestratorTodosMigration -count=1 -v`
Expected: FAIL — `version 21 exceeds 20 embedded migrations` (or a Fatal from `applyMigrations`).

- [ ] **Step 3: Add the migration and bump the version**

`internal/db/schema/0021_orchestrator_todos.sql`:

```sql
-- 0021_orchestrator_todos.sql: spike step statuses on checkpoints; last todos hash sent per session.
ALTER TABLE checkpoints ADD COLUMN todos_json TEXT;
ALTER TABLE sessions ADD COLUMN todos_sent_hash TEXT;
```

`internal/db/db.go:17`: `const SchemaVersion = 21`

- [ ] **Step 4: Run the db package**

Run: `go test ./internal/db -count=1`
Expected: PASS (including `db_test.go`'s `user_version == SchemaVersion` checks).

- [ ] **Step 5: Write the failing `Todos` tests**

`internal/runtime/todos_test.go`:

```go
package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func mustTodos(t *testing.T, s *Store, itemID string) []Todo {
	t.Helper()
	got, err := s.Todos(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func todoIDs(ts []Todo) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func todoStatuses(ts []Todo) []TodoStatus {
	out := []TodoStatus{}
	for _, t := range ts {
		out = append(out, t.Status)
	}
	return out
}

func mkItem(t *testing.T, s *Store, typ items.Type, parentKey, title string) items.Item {
	t.Helper()
	it, err := s.Items.Create(context.Background(), items.CreateInput{Type: typ, ParentKey: parentKey, Title: title}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func execSQL(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

// insertCheckpoint writes one raw checkpoint row for (agent, session) on itemID.
func insertCheckpoint(t *testing.T, s *Store, sessionID, agentID, itemID, kind string, todosJSON any) {
	t.Helper()
	execSQL(t, s, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, todos_json, created_at)
		VALUES (?, ?, ?, ?, ?, 1, 'seeded', ?, ?)`, ids.New("ckp"), sessionID, agentID, itemID, kind, todosJSON, db.Millis(s.Now()))
}

func TestTodosEpicTreeOrderAndStatusMapping(t *testing.T) {
	s, _, _ := newStore(t)
	ep := mkItem(t, s, items.Epic, "", "Auth")
	storyB := mkItem(t, s, items.Story, ep.Key, "Story B")
	storyA := mkItem(t, s, items.Story, ep.Key, "Story A")
	execSQL(t, s, `UPDATE items SET sort_order = 1 WHERE id = ?`, storyB.ID)
	t3 := mkItem(t, s, items.Task, storyB.Key, "Three")
	t1 := mkItem(t, s, items.Task, storyA.Key, "One")
	t2 := mkItem(t, s, items.Task, storyA.Key, "Two")
	gone := mkItem(t, s, items.Task, storyA.Key, "Gone")
	direct := mkItem(t, s, items.Task, storyA.Key, "Direct")
	execSQL(t, s, `UPDATE items SET parent_id = ? WHERE id = ?`, ep.ID, direct.ID)
	execSQL(t, s, `UPDATE items SET status = 'cancelled' WHERE id = ?`, gone.ID)
	execSQL(t, s, `UPDATE items SET status = 'in_review' WHERE id = ?`, t1.ID)
	execSQL(t, s, `UPDATE items SET status = 'done' WHERE id = ?`, t2.ID)
	execSQL(t, s, `UPDATE items SET status = 'blocked' WHERE id = ?`, t3.ID)

	got := mustTodos(t, s, ep.ID)
	wantIDs := []string{direct.Key, t1.Key, t2.Key, t3.Key, "integrate", "accept"}
	if !reflect.DeepEqual(todoIDs(got), wantIDs) {
		t.Fatalf("ids = %v, want %v", todoIDs(got), wantIDs)
	}
	wantSt := []TodoStatus{TodoPending, TodoInProgress, TodoCompleted, TodoPending, TodoPending, TodoPending}
	if !reflect.DeepEqual(todoStatuses(got), wantSt) {
		t.Fatalf("statuses = %v, want %v", todoStatuses(got), wantSt)
	}
	if got[1].Label != t1.Key+" · One" || got[1].ItemKey != t1.Key {
		t.Fatalf("task entry = %+v", got[1])
	}
	if got[4].Label != "Merge + verify" || got[5].Label != "User acceptance" || got[4].ItemKey != "" {
		t.Fatalf("fixed entries = %+v %+v", got[4], got[5])
	}
}

func TestTaskTodoStatusCoversEveryItemStatus(t *testing.T) {
	for st, want := range map[items.Status]TodoStatus{
		items.Done: TodoCompleted, items.InProgress: TodoInProgress, items.InReview: TodoInProgress,
		items.AwaitingApproval: TodoInProgress, items.Draft: TodoPending, items.Ready: TodoPending,
		items.Blocked: TodoPending,
	} {
		if got := taskTodoStatus(st); got != want {
			t.Errorf("taskTodoStatus(%s) = %s, want %s", st, got, want)
		}
	}
}

func TestTodosIntegrateInProgressOnceEveryTaskIsDone(t *testing.T) {
	s, _, _ := newStore(t)
	ep := seedEpicWithTwoTasks(t, s)
	execSQL(t, s, `UPDATE items SET status = 'done' WHERE type = 'task'`)
	got := mustTodos(t, s, ep.ID)
	if st := todoStatuses(got)[2:]; !reflect.DeepEqual(st, []TodoStatus{TodoInProgress, TodoPending}) {
		t.Fatalf("integrate/accept = %v", st)
	}
}

func TestTodosChoreWithZeroTasks(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ch := seedTopLevelItem(t, s, items.Chore)
	got := mustTodos(t, s, ch.ID)
	if !reflect.DeepEqual(todoIDs(got), []string{"work", "integrate", "accept"}) ||
		!reflect.DeepEqual(todoStatuses(got), []TodoStatus{TodoInProgress, TodoPending, TodoPending}) {
		t.Fatalf("got %+v", got)
	}
	if got[0].Label != "Do the work" {
		t.Fatalf("work label = %q", got[0].Label)
	}
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ch.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	insertCheckpoint(t, s, ses.ID, orch.ID, ch.ID, "integrated", nil)
	got = mustTodos(t, s, ch.ID)
	if !reflect.DeepEqual(todoStatuses(got), []TodoStatus{TodoCompleted, TodoCompleted, TodoInProgress}) {
		t.Fatalf("after integrated: %v", todoStatuses(got))
	}
	execSQL(t, s, `UPDATE items SET status = 'done' WHERE id = ?`, ch.ID)
	if st := todoStatuses(mustTodos(t, s, ch.ID)); st[2] != TodoCompleted {
		t.Fatalf("accept after done = %s", st[2])
	}
}

func TestTodosFeatureSpikeUsesStoredReportsAndDaemonFacts(t *testing.T) {
	s, _, _ := newStore(t)
	req, ses, _ := seedSectionApproval(t, s) // feature spike + spec with one open section request
	spikeID := req.ItemID
	got := mustTodos(t, s, spikeID)
	want := []string{"frame", "research", "design", "spec", "plan", "critic", "approve"}
	if !reflect.DeepEqual(todoIDs(got), want) {
		t.Fatalf("ids = %v", todoIDs(got))
	}
	if got[0].Label != "Frame the request" || got[6].Label != "Plan approval + materialize" {
		t.Fatalf("labels = %q / %q", got[0].Label, got[6].Label)
	}
	insertCheckpoint(t, s, ses.ID, ses.AgentID, spikeID, "progress",
		`[{"id":"frame","status":"completed"},{"id":"research","status":"in_progress"}]`)
	got = mustTodos(t, s, spikeID)
	if !reflect.DeepEqual(todoStatuses(got)[:4], []TodoStatus{TodoCompleted, TodoInProgress, TodoPending, TodoPending}) {
		t.Fatalf("statuses = %v", todoStatuses(got))
	}
	execSQL(t, s, `UPDATE requests SET state = 'approved' WHERE id = ?`, req.ID)
	if st := todoStatuses(mustTodos(t, s, spikeID)); st[3] != TodoCompleted {
		t.Fatalf("spec after approval = %s", st[3])
	}
	ep := mkItem(t, s, items.Epic, "", "Materialized")
	execSQL(t, s, `UPDATE items SET origin_spike_id = ? WHERE id = ?`, spikeID, ep.ID)
	if st := todoStatuses(mustTodos(t, s, spikeID)); st[6] != TodoCompleted {
		t.Fatalf("approve after materialize = %s", st[6])
	}
}

func TestTodosDebugSpikeAndNoListCases(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, _, _, err := s.StartSpike(ctx, SpikeInput{Name: "Crash", Intent: "debug", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"frame", "evidence", "root_cause", "report", "plan", "critic", "approve"}
	if got := todoIDs(mustTodos(t, s, sp.ID)); !reflect.DeepEqual(got, want) {
		t.Fatalf("debug ids = %v", got)
	}
	execSQL(t, s, `UPDATE items SET spike_intent = 'chore' WHERE id = ?`, sp.ID)
	if got := mustTodos(t, s, sp.ID); got != nil {
		t.Fatalf("chore-intent spike = %+v, want nil", got)
	}
	ep := seedEpicWithTask(t, s)
	task, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustTodos(t, s, task.ID); got != nil {
		t.Fatalf("task = %+v, want nil", got)
	}
	if got := mustTodos(t, s, ep.ID); len(got) != 3 {
		t.Fatalf("epic = %+v", got)
	}
}

func TestProgressOf(t *testing.T) {
	if ProgressOf(nil) != nil {
		t.Fatal("empty list must give nil")
	}
	p := ProgressOf([]Todo{
		{ID: "a", Label: "A", Status: TodoCompleted},
		{ID: "b", Label: "B", Status: TodoPending},
		{ID: "c", Label: "C", Status: TodoInProgress},
	})
	if *p != (Progress{Done: 1, Total: 3, Current: "C"}) {
		t.Fatalf("got %+v", *p)
	}
	p = ProgressOf([]Todo{{ID: "a", Label: "A", Status: TodoCompleted}, {ID: "b", Label: "B", Status: TodoPending}})
	if p.Current != "B" {
		t.Fatalf("first pending: %+v", *p)
	}
	p = ProgressOf([]Todo{{ID: "a", Label: "A", Status: TodoCompleted}})
	if *p != (Progress{Done: 1, Total: 1, Current: ""}) {
		t.Fatalf("all done: %+v", *p)
	}
}
```

Note: the helper is `execSQL`, not `exec` — `helpers_test.go` imports `os/exec`, and a package-level `exec` would collide with that file-scope import.

- [ ] **Step 6: Run, watch it fail**

Run: `go test ./internal/runtime -run 'TestTodos|TestTaskTodoStatus|TestProgressOf' -count=1`
Expected: FAIL — build error `undefined: Todo` / `s.Todos undefined`.

- [ ] **Step 7: Implement `internal/runtime/todos.go`**

```go
package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// TodoStatus is one progress-list entry's status (spec 2026-09-28-orchestrator-todos).
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

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

type Progress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"` // first in_progress label, else first pending, else ""
}

type todoStep struct{ ID, Label string }

var spikeSteps = map[string][]todoStep{
	"feature": {{"frame", "Frame the request"}, {"research", "Research"}, {"design", "Design"},
		{"spec", "Spec approval"}, {"plan", "Write plan"}, {"critic", "Completeness check"},
		{"approve", "Plan approval + materialize"}},
	"debug": {{"frame", "Frame the problem"}, {"evidence", "Reproduce + gather evidence"},
		{"root_cause", "Root cause"}, {"report", "Debug report approval"}, {"plan", "Plan fix packages"},
		{"critic", "Completeness check"}, {"approve", "Approval + materialize"}},
}

func taskTodoStatus(s items.Status) TodoStatus {
	switch s {
	case items.Done:
		return TodoCompleted
	case items.InProgress, items.InReview, items.AwaitingApproval:
		return TodoInProgress
	}
	return TodoPending
}

// todoQuerier is the read surface *sql.DB and *sql.Tx share; the list is
// read-only, so /api/state reads it without taking the immediate write lock.
type todoQuerier interface {
	txQuerier
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Todos is the root item's progress list; nil when the item has none (not a
// root, or a root type with no list). Callers already inside a transaction
// pass their tx to todosTx instead.
func (s *Store) Todos(ctx context.Context, rootItemID string) ([]Todo, error) {
	return s.todosTx(ctx, s.DB, rootItemID)
}

func (s *Store) todosTx(ctx context.Context, tx todoQuerier, rootItemID string) ([]Todo, error) {
	var typ, status, intent string
	var parent sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT type, status, COALESCE(spike_intent, ''), parent_id FROM items WHERE id = ?`,
		rootItemID).Scan(&typ, &status, &intent, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || parent.Valid {
		return nil, err
	}
	switch items.Type(typ) {
	case items.Spike:
		steps, ok := spikeSteps[intent]
		if !ok {
			return nil, nil
		}
		reports, err := s.latestTodoReports(ctx, tx, rootItemID)
		if err != nil {
			return nil, err
		}
		return s.spikeTodos(ctx, tx, rootItemID, steps, reports)
	case items.Epic, items.Bug, items.Chore:
		return s.taskTodos(ctx, tx, rootItemID, items.Type(typ), items.Status(status))
	}
	return nil, nil
}

func (s *Store) taskTodos(ctx context.Context, tx todoQuerier, rootID string, typ items.Type, rootStatus items.Status) ([]Todo, error) {
	// Tasks directly under the root first (p is NULL), then by story order.
	rows, err := tx.QueryContext(ctx, `SELECT t.key, t.title, t.status FROM items t
		LEFT JOIN items p ON p.id = t.parent_id AND p.id <> t.root_id
		WHERE t.root_id = ? AND t.type = 'task' AND t.status <> 'cancelled'
		ORDER BY p.id IS NOT NULL, p.sort_order, length(p.key), p.key, t.sort_order, length(t.key), t.key`, rootID)
	if err != nil {
		return nil, err
	}
	var out []Todo
	allDone := true
	for rows.Next() {
		var key, title, st string
		if err := rows.Scan(&key, &title, &st); err != nil {
			rows.Close()
			return nil, err
		}
		ts := taskTodoStatus(items.Status(st))
		allDone = allDone && ts == TodoCompleted
		out = append(out, Todo{ID: key, Label: key + " · " + title, Status: ts, ItemKey: key})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var integrated bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'integrated')`,
		rootID).Scan(&integrated); err != nil {
		return nil, err
	}
	integrate := TodoPending
	if integrated {
		integrate = TodoCompleted
	} else if len(out) > 0 && allDone {
		integrate = TodoInProgress
	}
	accept := TodoPending
	if rootStatus == items.Done {
		accept = TodoCompleted
	} else if integrate == TodoCompleted {
		accept = TodoInProgress
	}
	if typ == items.Chore && len(out) == 0 {
		work := TodoInProgress
		if integrate == TodoCompleted {
			work = TodoCompleted
		}
		out = append(out, Todo{ID: "work", Label: "Do the work", Status: work})
	}
	return append(out, Todo{ID: "integrate", Label: "Merge + verify", Status: integrate},
		Todo{ID: "accept", Label: "User acceptance", Status: accept}), nil
}

// latestTodoReports is the newest stored spike step list for itemID, nil if none.
func (s *Store) latestTodoReports(ctx context.Context, tx todoQuerier, itemID string) ([]TodoReport, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT todos_json FROM checkpoints WHERE item_id = ? AND todos_json IS NOT NULL
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, itemID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []TodoReport
	return out, json.Unmarshal([]byte(raw), &out)
}

// spikeTodos lays reports over the template and applies the two facts the
// daemon owns: spec approved, and the spike materialized.
func (s *Store) spikeTodos(ctx context.Context, tx todoQuerier, spikeID string, steps []todoStep, reports []TodoReport) ([]Todo, error) {
	byID := map[string]TodoStatus{}
	for _, r := range reports {
		byID[r.ID] = r.Status
	}
	var specID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE item_id = ? AND kind = 'spec'
		ORDER BY created_at DESC, id DESC LIMIT 1`, spikeID).Scan(&specID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if specID != "" && s.checkEverySectionApproved(ctx, tx, specID) == nil {
		byID["spec"] = TodoCompleted
	}
	var materialized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE origin_spike_id = ?)`,
		spikeID).Scan(&materialized); err != nil {
		return nil, err
	}
	if materialized {
		byID["approve"] = TodoCompleted
	}
	out := make([]Todo, 0, len(steps))
	for _, st := range steps {
		status := byID[st.ID]
		if status == "" {
			status = TodoPending
		}
		out = append(out, Todo{ID: st.ID, Label: st.Label, Status: status})
	}
	return out, nil
}

// ProgressOf summarizes a list for the agents API; nil for an empty list.
func ProgressOf(todos []Todo) *Progress {
	if len(todos) == 0 {
		return nil
	}
	p := &Progress{Total: len(todos)}
	firstPending := ""
	for _, t := range todos {
		switch t.Status {
		case TodoCompleted:
			p.Done++
		case TodoInProgress:
			if p.Current == "" {
				p.Current = t.Label
			}
		default:
			if firstPending == "" {
				firstPending = t.Label
			}
		}
	}
	if p.Current == "" {
		p.Current = firstPending
	}
	return p
}
```

- [ ] **Step 8: Run, watch it pass**

Run: `go test ./internal/runtime -run 'TestTodos|TestTaskTodoStatus|TestProgressOf' -count=1 -v`
Expected: PASS. Then `gofmt -l internal/ && go vet ./internal/runtime ./internal/db` — no output.

- [ ] **Step 9: Commit**

```bash
git add internal/db/schema/0021_orchestrator_todos.sql internal/db/db.go internal/db/schema_0021_orchestrator_todos_test.go internal/runtime/todos.go internal/runtime/todos_test.go
git commit -m "feat(runtime,db): compute a root item's progress list

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 2: `swarm_checkpoint` todos — validation, merge, storage

**Files:**
- Modify: `internal/runtime/todos.go` (add `mergeSpikeTodos`)
- Modify: `internal/runtime/checkpoint.go` — `CheckpointInput` (line 73), `CheckpointResult` (line 103), `WriteCheckpoint` (line 1128; the title block ends ~line 1210, the INSERT is ~line 1390)
- Create: `internal/runtime/checkpoint_todos_test.go`

**Interfaces:**
- Consumes (T1): `TodoReport`, `TodoStatus` constants, `spikeSteps`, `latestTodoReports(ctx, tx, itemID)`, `spikeTodos(ctx, tx, spikeID, steps, reports)`, `Todos(ctx, id)` (tests only). Inside `WriteCheckpoint` use only the `tx` forms.
- Produces (T4):
  ```go
  // CheckpointInput gains:
  Todos []TodoReport
  // CheckpointResult gains:
  TodosIgnored string // "only the orchestrator keeps a task list" or ""
  func (s *Store) mergeSpikeTodos(ctx context.Context, tx *sql.Tx, spike items.Item, in []TodoReport) ([]TodoReport, error)
  ```

Copy (verbatim):
- `todos: unknown step "<id>" for a <feature|debug> spike; use: <ids joined by ", ">`
- `todos: status must be pending, in_progress or completed (got "<s>")`
- `todos: at most one step may be in_progress (<id>, <id>)`
- `todos are derived from tasks for <KEY>; don't send them`
- result: `only the orchestrator keeps a task list`

- [ ] **Step 1: Write the failing tests**

`internal/runtime/checkpoint_todos_test.go`:

```go
package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func spikeOrchestrator(t *testing.T, s *Store, intent string) (Session, items.Item) {
	t.Helper()
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Plan it", Intent: intent, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return ses, it
}

func ckTodos(s *Store, sessionID string, todos ...TodoReport) (CheckpointResult, error) {
	return s.WriteCheckpoint(context.Background(), sessionID, CheckpointInput{Kind: Progress, Summary: "step", Todos: todos})
}

func wantErr(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil || err.Error() != msg {
		t.Fatalf("err = %v, want %q", err, msg)
	}
}

func TestCheckpointTodosMergeOverTheNewestList(t *testing.T) {
	s, _, _ := newStore(t)
	ses, sp := spikeOrchestrator(t, s, "feature")
	if _, err := ckTodos(s, ses.ID, TodoReport{"frame", TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := ckTodos(s, ses.ID, TodoReport{"frame", TodoCompleted}, TodoReport{"research", TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT todos_json FROM checkpoints WHERE item_id = ?
		ORDER BY created_at DESC LIMIT 1`, sp.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	want := `[{"id":"frame","status":"completed"},{"id":"research","status":"in_progress"},{"id":"design","status":"pending"},{"id":"spec","status":"pending"},{"id":"plan","status":"pending"},{"id":"critic","status":"pending"},{"id":"approve","status":"pending"}]`
	if raw != want {
		t.Fatalf("todos_json = %s", raw)
	}
	// A report that leaves ids out keeps them.
	if _, err := ckTodos(s, ses.ID, TodoReport{"research", TodoCompleted}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Todos(context.Background(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(todoStatuses(got)[:3], []TodoStatus{TodoCompleted, TodoCompleted, TodoPending}) {
		t.Fatalf("statuses = %v", todoStatuses(got))
	}
}

func TestCheckpointTodosRefusals(t *testing.T) {
	s, _, _ := newStore(t)
	ses, _ := spikeOrchestrator(t, s, "feature")
	_, err := ckTodos(s, ses.ID, TodoReport{"bogus", TodoPending})
	wantErr(t, err, `todos: unknown step "bogus" for a feature spike; use: frame, research, design, spec, plan, critic, approve`)
	_, err = ckTodos(s, ses.ID, TodoReport{"frame", "done"})
	wantErr(t, err, `todos: status must be pending, in_progress or completed (got "done")`)
	_, err = ckTodos(s, ses.ID, TodoReport{"frame", TodoInProgress}, TodoReport{"research", TodoInProgress})
	wantErr(t, err, `todos: at most one step may be in_progress (frame, research)`)
	// Across the merge too.
	if _, err := ckTodos(s, ses.ID, TodoReport{"frame", TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	_, err = ckTodos(s, ses.ID, TodoReport{"research", TodoInProgress})
	wantErr(t, err, `todos: at most one step may be in_progress (frame, research)`)
}

func TestCheckpointTodosCountInProgressAfterDaemonFacts(t *testing.T) {
	s, _, _ := newStore(t)
	req, ses, _ := seedSectionApproval(t, s)
	if _, err := ckTodos(s, ses.ID, TodoReport{"spec", TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE requests SET state = 'approved' WHERE id = ?`, req.ID); err != nil {
		t.Fatal(err)
	}
	// spec is now completed by fact, so plan may be the one in_progress.
	if _, err := ckTodos(s, ses.ID, TodoReport{"plan", TodoInProgress}); err != nil {
		t.Fatalf("plan in_progress after spec approval: %v", err)
	}
}

func TestCheckpointTodosDebugSpikeIds(t *testing.T) {
	s, _, _ := newStore(t)
	ses, _ := spikeOrchestrator(t, s, "debug")
	if _, err := ckTodos(s, ses.ID, TodoReport{"root_cause", TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	_, err := ckTodos(s, ses.ID, TodoReport{"research", TodoPending})
	wantErr(t, err, `todos: unknown step "research" for a debug spike; use: frame, evidence, root_cause, report, plan, critic, approve`)
}

func TestCheckpointTodosFromAWorkerAreIgnored(t *testing.T) {
	s, _, _ := newStore(t)
	_, _, wSes := worker(t, s)
	res, err := ckTodos(s, wSes.ID, TodoReport{"frame", TodoCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if res.TodosIgnored != "only the orchestrator keeps a task list" {
		t.Fatalf("todos_ignored = %q", res.TodosIgnored)
	}
	var n int
	s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM checkpoints WHERE todos_json IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Fatalf("stored %d todos lists, want 0", n)
	}
}

func TestCheckpointTodosRefusedOnATaskDerivedList(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ckTodos(s, ses.ID, TodoReport{"frame", TodoCompleted})
	wantErr(t, err, "todos are derived from tasks for "+ep.Key+"; don't send them")

	cses, sp := spikeOrchestrator(t, s, "feature")
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET spike_intent = 'chore' WHERE id = ?`, sp.ID); err != nil {
		t.Fatal(err)
	}
	_, err = ckTodos(s, cses.ID, TodoReport{"frame", TodoCompleted})
	wantErr(t, err, "todos are derived from tasks for "+sp.Key+"; don't send them")
}

func TestCheckpointWithoutTodosStoresNull(t *testing.T) {
	s, _, _ := newStore(t)
	ses, sp := spikeOrchestrator(t, s, "feature")
	if _, err := ckTodos(s, ses.ID); err != nil { // todos omitted
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM checkpoints WHERE item_id = ? AND todos_json IS NOT NULL`, sp.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("todos_json set without todos")
	}
}
```

- [ ] **Step 2: Run, watch it fail**

Run: `go test ./internal/runtime -run 'TestCheckpointTodos|TestCheckpointWithoutTodos' -count=1`
Expected: FAIL — build error `unknown field Todos in struct literal of type CheckpointInput`.

- [ ] **Step 3: Implement**

`internal/runtime/todos.go` — add (imports: add `fmt`, `slices`, `strings`):

```go
// mergeSpikeTodos validates a spike orchestrator's step report (spec locked
// decision 5, steps 3-4) and returns the full merged list to store, in
// template order. The in_progress count runs on the list the orchestrator
// sees: after the daemon's spec/approve facts.
func (s *Store) mergeSpikeTodos(ctx context.Context, tx *sql.Tx, spike items.Item, in []TodoReport) ([]TodoReport, error) {
	steps := spikeSteps[spike.SpikeIntent]
	ids := make([]string, len(steps))
	for i, st := range steps {
		ids[i] = st.ID
	}
	for _, r := range in {
		if !slices.Contains(ids, r.ID) {
			return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"todos: unknown step %q for a %s spike; use: %s", r.ID, spike.SpikeIntent, strings.Join(ids, ", "))}
		}
		if r.Status != TodoPending && r.Status != TodoInProgress && r.Status != TodoCompleted {
			return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"todos: status must be pending, in_progress or completed (got %q)", r.Status)}
		}
	}
	prev, err := s.latestTodoReports(ctx, tx, spike.ID)
	if err != nil {
		return nil, err
	}
	byID := map[string]TodoStatus{}
	for _, r := range append(prev, in...) {
		byID[r.ID] = r.Status
	}
	merged := make([]TodoReport, 0, len(steps))
	for _, id := range ids {
		st := byID[id]
		if st == "" {
			st = TodoPending
		}
		merged = append(merged, TodoReport{ID: id, Status: st})
	}
	view, err := s.spikeTodos(ctx, tx, spike.ID, steps, merged)
	if err != nil {
		return nil, err
	}
	var running []string
	for _, t := range view {
		if t.Status == TodoInProgress {
			running = append(running, t.ID)
		}
	}
	if len(running) > 1 {
		return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
			"todos: at most one step may be in_progress (%s)", strings.Join(running, ", "))}
	}
	return merged, nil
}
```

`internal/runtime/checkpoint.go`:

1. `CheckpointInput` — after `Title string` add:
```go
	// Todos is a spike orchestrator's step report (spec 2026-09-28-orchestrator-todos
	// locked decision 4); ids it leaves out keep their stored status.
	Todos []TodoReport
```
2. `CheckpointResult` — after `TitleIgnored string` add:
```go
	// TodosIgnored is set when a non-orchestrator sent todos.
	TodosIgnored string
```
3. In `WriteCheckpoint`, directly after the title `if title := ...{ ... }` block closes and before the `Summary` length check, add:
```go
		// spec 2026-09-28-orchestrator-todos locked decision 5, in order.
		var todosJSON any
		if len(in.Todos) > 0 {
			switch {
			case a.Role != RoleOrchestrator:
				out.TodosIgnored = "only the orchestrator keeps a task list"
			case it.Type != items.Spike || spikeSteps[it.SpikeIntent] == nil:
				return &items.Error{Code: items.CodeBadRequest,
					Message: fmt.Sprintf("todos are derived from tasks for %s; don't send them", it.Key)}
			default:
				merged, err := s.mergeSpikeTodos(ctx, tx, it, in.Todos)
				if err != nil {
					return err
				}
				b, err := json.Marshal(merged)
				if err != nil {
					return err
				}
				todosJSON = string(b)
			}
		}
```
4. The `INSERT INTO checkpoints` (~line 1390): add column `todos_json` after `findings_json`, one more `?`, and the arg `todosJSON` after `jsonArray(in.Findings)`:
```go
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
			attempt, resolution, summary, next_json, blockers_json, git_json, verify_json, artifacts_json,
			processed_json, verdict, findings_json, todos_json, daemon_written, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
			ckpID, sessionID, a.ID, it.ID, string(in.Kind), ses.Attempt, nullIf(in.Resolution), in.Summary,
			jsonArray(in.Next), jsonArray(in.Blockers), jsonArray(in.Git), jsonArray(in.Verification),
			jsonArray(in.Artifacts), jsonArray(in.Processed), nullIf(in.Verdict), jsonArray(in.Findings),
			todosJSON, db.Millis(now)); err != nil {
```
Add `"encoding/json"` to checkpoint.go imports if absent.

- [ ] **Step 4: Run, watch it pass; then the whole package**

Run: `go test ./internal/runtime -run 'TestCheckpointTodos|TestCheckpointWithoutTodos' -count=1 -v` → PASS
Run: `go test ./internal/runtime -count=1` → PASS (existing checkpoint tests unaffected).
Run: `gofmt -l internal/ && go vet ./internal/runtime` → no output.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/todos.go internal/runtime/checkpoint.go internal/runtime/checkpoint_todos_test.go
git commit -m "feat(runtime): accept spike step statuses on swarm_checkpoint

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 3: `swarm_sync` delivers `todos` when it changed

**Files:**
- Modify: `internal/runtime/inbox.go` — `SyncResult` (line 231), `Sync` (line 257), new `todosToSend`
- Create: `internal/runtime/inbox_todos_test.go`

**Interfaces:**
- Consumes (T1): `todosTx(ctx, tx, rootItemID)` — **not** `Todos` (Sync already holds a tx). Test helpers: `seedEpicWithTask`, `worker`, `startSessionForTest` (helpers_test.go:189).
- Produces (T4): `SyncResult.Todos []Todo` — nil unless the list changed since this session last received it.

- [ ] **Step 1: Write the failing tests**

`internal/runtime/inbox_todos_test.go`:

```go
package runtime

import (
	"context"
	"testing"
)

func TestSyncSendsTodosOnlyWhenTheListChanged(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Sync(ctx, ses.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Todos) != 3 || first.Todos[0].ID != "TASK-1" {
		t.Fatalf("first sync todos = %+v", first.Todos)
	}
	second, err := s.Sync(ctx, ses.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Todos != nil {
		t.Fatalf("unchanged list resent: %+v", second.Todos)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE key = 'TASK-1'`); err != nil {
		t.Fatal(err)
	}
	third, err := s.Sync(ctx, ses.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Todos) != 3 || third.Todos[0].Status != TodoCompleted || third.Todos[1].Status != TodoInProgress {
		t.Fatalf("after task done = %+v", third.Todos)
	}
	fresh, err := s.startSessionForTest(ctx, orch, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Sync(ctx, fresh.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next.Todos == nil {
		t.Fatal("a new session must receive the list")
	}
}

func TestWorkerSyncNeverCarriesTodos(t *testing.T) {
	s, _, _ := newStore(t)
	_, _, wSes := worker(t, s)
	res, err := s.Sync(context.Background(), wSes.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Todos != nil {
		t.Fatalf("worker got todos: %+v", res.Todos)
	}
}
```

- [ ] **Step 2: Run, watch it fail**

Run: `go test ./internal/runtime -run 'TestSyncSendsTodos|TestWorkerSyncNeverCarriesTodos' -count=1`
Expected: FAIL — build error `first.Todos undefined`.

- [ ] **Step 3: Implement**

`SyncResult` — add field:
```go
	Todos        []Todo // set only when the list changed since this session last got it
```

In `Sync`, replace the closure's last two lines
```go
		out.Messages, err = s.envelopes(ctx, tx, a, immediate)
		return err
```
with
```go
		if out.Messages, err = s.envelopes(ctx, tx, a, immediate); err != nil {
			return err
		}
		if a.Role == RoleOrchestrator && a.ItemID == a.RootItemID {
			out.Todos, err = s.todosToSend(ctx, tx, sessionID, a.ItemID)
		}
		return err
```

Add below `Sync` (imports: add `"crypto/sha256"`, `"encoding/hex"`):
```go
// todosToSend is spec locked decision 6: the root's list when its sha256
// differs from sessions.todos_sent_hash, storing the new hash in the same tx.
func (s *Store) todosToSend(ctx context.Context, tx *sql.Tx, sessionID, rootItemID string) ([]Todo, error) {
	todos, err := s.todosTx(ctx, tx, rootItemID)
	if err != nil || todos == nil {
		return nil, err
	}
	b, err := json.Marshal(todos)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	var sent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT todos_sent_hash FROM sessions WHERE id = ?`, sessionID).Scan(&sent); err != nil {
		return nil, err
	}
	if sent.String == hash {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET todos_sent_hash = ? WHERE id = ?`, hash, sessionID); err != nil {
		return nil, err
	}
	return todos, nil
}
```

- [ ] **Step 4: Run, watch it pass; then the package**

Run: `go test ./internal/runtime -run 'TestSyncSendsTodos|TestWorkerSyncNeverCarriesTodos' -count=1 -v` → PASS
Run: `go test ./internal/runtime -count=1` → PASS
Run: `gofmt -l internal/ && go vet ./internal/runtime` → no output.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/inbox.go internal/runtime/inbox_todos_test.go
git commit -m "feat(runtime): send the orchestrator's todos on swarm_sync when they change

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 4: MCP wiring — `swarm_checkpoint.todos`, `todos_ignored`, `swarm_sync.todos`

**Files:**
- Modify: `internal/mcpserver/tools.go` — `syncTool` (line 52), `checkpointTool` (line 99)
- Create: `internal/mcpserver/tools_todos_test.go`

**Interfaces:**
- Consumes (T2/T3): `runtime.CheckpointInput.Todos []runtime.TodoReport`, `runtime.CheckpointResult.TodosIgnored`, `runtime.SyncResult.Todos []runtime.Todo`. Test helpers: `newOrchestratorServer` (helpers_test.go:475), `newServerWithSession` (coder), `s.call`, `mustJSON`.
- Produces: MCP wire — `swarm_checkpoint` input `todos: [{id,status}]`, output `todos_ignored`; `swarm_sync` output `todos` (only when present).

- [ ] **Step 1: Write the failing tests**

`internal/mcpserver/tools_todos_test.go`:

```go
package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func TestSyncToolCarriesTodosOnlyWhenChanged(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	todos, ok := out.(map[string]any)["todos"].([]runtime.Todo)
	if !ok || len(todos) != 4 { // two tasks + integrate + accept
		t.Fatalf("todos = %s", mustJSON(out))
	}
	out, err = s.call(ctx, seed.Caller, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := out.(map[string]any)["todos"]; present {
		t.Fatalf("unchanged todos resent: %s", mustJSON(out))
	}
}

func TestCheckpointToolPassesSpikeTodosThrough(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	_, a, _, err := s.RT.StartSpike(ctx, runtime.SpikeInput{Name: "Plan it", Intent: "feature", Kind: runtime.Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := Caller{SessionID: ses.ID, AgentID: a.ID, AgentName: a.Name, Role: runtime.RoleOrchestrator}
	if _, err := s.call(ctx, c, "swarm_checkpoint",
		`{"kind":"progress","summary":"framing","todos":[{"id":"frame","status":"in_progress"}]}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.call(ctx, c, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	todos := out.(map[string]any)["todos"].([]runtime.Todo)
	if todos[0].ID != "frame" || todos[0].Status != runtime.TodoInProgress {
		t.Fatalf("todos = %+v", todos)
	}
	_, err = s.call(ctx, c, "swarm_checkpoint", `{"kind":"progress","summary":"x","todos":[{"id":"nope","status":"pending"}]}`)
	if err == nil || !strings.Contains(err.Error(), `todos: unknown step "nope"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckpointToolReportsTodosIgnoredForAWorker(t *testing.T) {
	s, seed := newServerWithSession(t) // a coder
	out, err := s.call(context.Background(), seed.Caller, "swarm_checkpoint",
		`{"kind":"progress","summary":"x","todos":[{"id":"frame","status":"completed"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["todos_ignored"]; got != "only the orchestrator keeps a task list" {
		t.Fatalf("todos_ignored = %v", got)
	}
}

func TestCheckpointSchemaDeclaresTodos(t *testing.T) {
	s := newTestServer(t)
	for _, d := range s.Tools() {
		if d.Name == "swarm_checkpoint" && !strings.Contains(string(d.Schema), `"todos":{"type":"array"`) {
			t.Fatalf("schema = %s", d.Schema)
		}
	}
}
```

- [ ] **Step 2: Run, watch it fail**

Run: `go test ./internal/mcpserver -run 'TestSyncToolCarriesTodos|TestCheckpointToolPassesSpikeTodos|TestCheckpointToolReportsTodosIgnored|TestCheckpointSchemaDeclaresTodos' -count=1`
Expected: FAIL — `todos` missing from sync output / schema; `todos_ignored` nil.

- [ ] **Step 3: Implement in `internal/mcpserver/tools.go`**

`syncTool` handler — replace the final `return map[string]any{...}, nil` with:
```go
			out := map[string]any{
				"messages":      res.Messages,
				"unacked":       res.Unacked,
				"more":          res.More,
				"session_state": res.SessionState,
				"assignment":    assignmentOut(rec.Assignment),
				"recovery":      recoveryBundleOut(rec.Recovery),
				"first_sync":    rec.FirstSync,
			}
			if res.Todos != nil {
				out["todos"] = res.Todos
			}
			return out, nil
```

`checkpointTool`:
1. In the schema string, after the `"title":{...},` line add:
```
			"todos":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["id","status"]},"description":"Spike orchestrators only: step statuses by id; omitted ids keep their status."},
```
2. Input struct: add `Todos []runtime.TodoReport `json:"todos"``.
3. `runtime.CheckpointInput{...}`: add `Todos: in.Todos,`.
4. After the `title_ignored` block:
```go
			if res.TodosIgnored != "" {
				out["todos_ignored"] = res.TodosIgnored
			}
```

- [ ] **Step 4: Run, watch it pass; then the package**

Run: `go test ./internal/mcpserver -run 'Todos' -count=1 -v` → PASS
Run: `go test ./internal/mcpserver -count=1` → PASS
Run: `gofmt -l internal/ && go vet ./internal/mcpserver` → no output.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/tools.go internal/mcpserver/tools_todos_test.go
git commit -m "feat(mcpserver): wire todos through swarm_checkpoint and swarm_sync

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 5: HTTP — `todos` on item detail, `progress` on orchestrator agent nodes

**Files:**
- Modify: `internal/httpapi/items.go` — `getItem` (line 217), inside the `if s.RT != nil {` block
- Modify: `internal/httpapi/runtime.go` — `agentNodeWire` (line 50), `agentNodeOut` (line 476)
- Create: `internal/httpapi/todos_test.go`

**Interfaces:**
- Consumes (T1): `(*runtime.Store).Todos(ctx, rootItemID) ([]runtime.Todo, error)` (handlers hold no tx, so the public form is right here), `runtime.ProgressOf`. Test harness: `newRuntimeServer` (helpers_test.go:512) → seeded `RootKey` epic with one Draft task `TaskKey` titled "Task", orchestrator `root-orchestrator`, worker `task-worker`; `e.get(t, path)`.
- Produces (T7 relies on the wire key): `agentNodeWire.Progress *runtime.Progress `json:"progress,omitempty"``; item detail key `"todos"`.

- [ ] **Step 1: Write the failing tests**

`internal/httpapi/todos_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"testing"
)

func TestItemDetailCarriesTodosForARootOnly(t *testing.T) {
	e, seed := newRuntimeServer(t)
	var root map[string]any
	if err := json.Unmarshal(e.get(t, "/api/items/"+seed.RootKey).Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	todos, ok := root["todos"].([]any)
	if !ok || len(todos) != 3 {
		t.Fatalf("root todos = %v", root["todos"])
	}
	first := todos[0].(map[string]any)
	if first["id"] != seed.TaskKey || first["label"] != seed.TaskKey+" · Task" || first["status"] != "pending" || first["item_key"] != seed.TaskKey {
		t.Fatalf("first todo = %v", first)
	}
	var task map[string]any
	if err := json.Unmarshal(e.get(t, "/api/items/"+seed.TaskKey).Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if _, present := task["todos"]; present {
		t.Fatal("a task must not carry todos")
	}
}

func TestOrchestratorAgentNodeCarriesProgress(t *testing.T) {
	e, seed := newRuntimeServer(t)
	var body map[string]any
	if err := json.Unmarshal(e.get(t, "/api/state").Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var orch map[string]any
	for _, a := range body["agents"].([]any) {
		if n := a.(map[string]any); n["name"] == seed.AgentName {
			orch = n
		}
	}
	if orch == nil {
		t.Fatal("orchestrator missing from /api/state")
	}
	p, ok := orch["progress"].(map[string]any)
	if !ok || p["done"] != float64(0) || p["total"] != float64(3) || p["current"] != seed.TaskKey+" · Task" {
		t.Fatalf("progress = %v", orch["progress"])
	}
	for _, c := range orch["children"].([]any) {
		if _, present := c.(map[string]any)["progress"]; present {
			t.Fatalf("worker node has progress: %v", c)
		}
	}
}
```

- [ ] **Step 2: Run, watch it fail**

Run: `go test ./internal/httpapi -run 'TestItemDetailCarriesTodos|TestOrchestratorAgentNodeCarriesProgress' -count=1`
Expected: FAIL — `root todos = <nil>`, `progress = <nil>`.

- [ ] **Step 3: Implement**

`items.go`, in `getItem`, inside `if s.RT != nil {` right after `out["artifacts"] = artifactWires`:
```go
		todos, err := s.RT.Todos(ctx, it.ID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		if todos != nil {
			out["todos"] = todos
		}
```

`runtime.go`, `agentNodeWire` — after `KindReason`:
```go
	Progress       *runtime.Progress  `json:"progress,omitempty"` // orchestrators of a root with a list only
```

`agentNodeOut`, inside `if s.RT != nil {` after `w.Replacement = ...`:
```go
		if a.Role == runtime.RoleOrchestrator && a.ItemID == a.RootItemID {
			if todos, err := s.RT.Todos(ctx, a.ItemID); err == nil {
				w.Progress = runtime.ProgressOf(todos)
			}
		}
```

- [ ] **Step 4: Run, watch it pass; then the package**

Run: `go test ./internal/httpapi -run 'TestItemDetailCarriesTodos|TestOrchestratorAgentNodeCarriesProgress' -count=1 -v` → PASS
Run: `go test ./internal/httpapi -count=1` → PASS (incl. `TestMenubarContract`; fixture unchanged so far).
Run: `gofmt -l internal/ && go vet ./internal/httpapi` → no output.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/items.go internal/httpapi/runtime.go internal/httpapi/todos_test.go
git commit -m "feat(httpapi): expose todos on item detail and progress on orchestrator nodes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 6: Web — `TodoList` in the Details panel

**Files:**
- Modify: `web/src/types.ts` (add types; `ItemDetail` at line 86)
- Modify: `web/src/copy.ts` (`C` object, line 61)
- Create: `web/src/components/TodoList.tsx`
- Create: `web/src/components/TodoList.test.tsx`
- Modify: `web/src/panels/Details.tsx` (after `<WorkflowSection …/>`, before `<div role="tablist" …>`)
- Modify: `web/src/panels/Details.test.tsx` (add one test)

**Interfaces:**
- Consumes: HTTP `GET /api/items/{key}` → optional `todos: Todo[]` (T5; tests use the mock daemon, so no runtime dependency). `DetailsProps.onSelect(key)`.
- Produces:
  ```ts
  export type TodoStatus = "pending" | "in_progress" | "completed";
  export interface Todo { id: string; label: string; status: TodoStatus; item_key?: string }
  // ItemDetail gains: todos?: Todo[];
  export function TodoList(props: { todos: Todo[]; onSelect(key: string): void }): JSX.Element
  ```

Copy: heading `Progress` followed by ` <done>/<total>` → `C.progress = "Progress"`.

- [ ] **Step 1: Write the failing tests**

`web/src/components/TodoList.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Todo } from "../types";
import { TodoList } from "./TodoList";

const todos: Todo[] = [
  { id: "TASK-10", label: "TASK-10 · Schema migration", status: "completed", item_key: "TASK-10" },
  { id: "TASK-12", label: "TASK-12 · Add login form", status: "in_progress", item_key: "TASK-12" },
  { id: "integrate", label: "Merge + verify", status: "pending" },
];

describe("TodoList", () => {
  it("renders the heading count, icons and task links", async () => {
    const onSelect = vi.fn();
    render(<TodoList todos={todos} onSelect={onSelect} />);
    expect(screen.getByRole("heading", { name: "Progress 1/3" })).toBeInTheDocument();
    const items = screen.getAllByRole("listitem").map((li) => li.textContent);
    expect(items).toEqual(["✓TASK-10 · Schema migration", "▶TASK-12 · Add login form", "○Merge + verify"]);
    expect(screen.queryByRole("button", { name: "Merge + verify" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "TASK-12 · Add login form" }));
    expect(onSelect).toHaveBeenCalledWith("TASK-12");
  });
});
```

`web/src/panels/Details.test.tsx` — add inside `describe("Details panel (§16.9)", …)`:

```tsx
  it("shows the Progress block only when the detail carries todos", async () => {
    const d = createMockDaemon();
    const real = d.handle({ method: "GET", url: "/api/items/EPIC-12", headers: { authorization: `Bearer ${d.db.token}` } });
    d.override("GET /api/items/EPIC-12", () => ({
      ...real,
      body: { ...(real.body as object), todos: [
        { id: "TASK-101", label: "TASK-101 · Login form", status: "in_progress", item_key: "TASK-101" },
        { id: "integrate", label: "Merge + verify", status: "pending" },
      ] },
    }));
    const { user, props } = setup("EPIC-12", {}, d);
    expect(await screen.findByRole("heading", { name: "Progress 0/2" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "TASK-101 · Login form" }));
    expect(props.onSelect).toHaveBeenCalledWith("TASK-101");
  });

  it("has no Progress block without todos", async () => {
    setup("EPIC-12");
    expect(await screen.findByTestId("details-panel")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /^Progress/ })).toBeNull();
  });
```

- [ ] **Step 2: Run, watch it fail**

Run: `cd web && npx vitest run src/components/TodoList.test.tsx src/panels/Details.test.tsx`
Expected: FAIL — `Failed to resolve import "./TodoList"`.

- [ ] **Step 3: Implement**

`web/src/types.ts` — above `export interface ItemDetail`:
```ts
export type TodoStatus = "pending" | "in_progress" | "completed";
export interface Todo { id: string; label: string; status: TodoStatus; item_key?: string }
```
and inside `ItemDetail`, after `crew?: WorkflowCrewMember[];`:
```ts
  todos?: Todo[];                  // root items with a progress list only
```

`web/src/copy.ts` — in `C`, next to `checkpoints: "Checkpoints",`:
```ts
  progress: "Progress",
```

`web/src/components/TodoList.tsx`:
```tsx
import { C } from "../copy";
import type { Todo, TodoStatus } from "../types";

const ICON: Record<TodoStatus, [string, string]> = {
  completed: ["✓", "text-ok"],
  in_progress: ["▶", "text-accent"],
  pending: ["○", "text-muted"],
};

export function TodoList({ todos, onSelect }: { todos: Todo[]; onSelect(key: string): void }) {
  const done = todos.filter((t) => t.status === "completed").length;
  return (
    <section aria-label={C.progress} className="border-t border-line pt-3">
      <h3 className="mb-1 font-semibold">{`${C.progress} ${done}/${todos.length}`}</h3>
      <ul>
        {todos.map((t) => {
          const [icon, tone] = ICON[t.status];
          const key = t.item_key;
          return (
            <li key={t.id} className="flex gap-2">
              <span aria-hidden="true" className={tone}>{icon}</span>
              {key ? (
                <button type="button" onClick={() => onSelect(key)} className="text-left text-accent">{t.label}</button>
              ) : (
                <span>{t.label}</span>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
```

`web/src/panels/Details.tsx` — add import `import { TodoList } from "../components/TodoList";` and, between the `<WorkflowSection … />` element and `<div role="tablist" …>`:
```tsx
      {d.todos && <TodoList todos={d.todos} onSelect={p.onSelect} />}
```

- [ ] **Step 4: Run, watch it pass; then the suite and build**

Run: `cd web && npx vitest run src/components/TodoList.test.tsx src/panels/Details.test.tsx` → PASS
Run: `cd web && npm test && npm run build` → PASS (coverage thresholds included).

- [ ] **Step 5: Commit**

```bash
git add web/src/types.ts web/src/copy.ts web/src/components/TodoList.tsx web/src/components/TodoList.test.tsx web/src/panels/Details.tsx web/src/panels/Details.test.tsx
git commit -m "feat(web): show the orchestrator's progress list in Details

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 7: Menubar — `AgentProgress` and the orchestrator subtitle

**Depends on T5** (see Execution order).

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarKit/Wire.swift` (`AgentNode`, line 67)
- Modify: `apps/menubar/Sources/SwarmBarKit/AgentActions.swift` (`subtitle`, line 262)
- Modify: `apps/menubar/Sources/SwarmBarKit/Copy.swift` (next to `done`, line 113)
- Modify: `apps/menubar/Tests/Fixtures/state.json` (agent `billing-spike-orchestrator`, `agents[3]`)
- Modify: `apps/menubar/Tests/SwarmBarTests/FixtureTests.swift` (`testStateFixtureDecodes`)
- Modify: `apps/menubar/Tests/SwarmBarTests/AgentActionsTests.swift` (`testSubtitle`)

**Interfaces:**
- Consumes (T5): wire key `progress: {done, total, current}` on orchestrator `AgentNode`s, omitted otherwise.
- Produces:
  ```swift
  public struct AgentProgress: Codable, Sendable, Equatable { public var done: Int; public var total: Int; public var current: String }
  // AgentNode gains: public var progress: AgentProgress?  (init param `progress: AgentProgress? = nil`)
  // Copy.progressDone = "Done"
  ```

- [ ] **Step 1: Write the failing tests and fixture**

`Tests/Fixtures/state.json` — in the `billing-spike-orchestrator` object (queued, `"session": null`), add after its `"item_title"` line:
```json
      "progress": { "done": 2, "total": 7, "current": "Design" },
```

`FixtureTests.testStateFixtureDecodes` — add:
```swift
        XCTAssertEqual(s.agents[3].progress, AgentProgress(done: 2, total: 7, current: "Design"))
        XCTAssertNil(s.agents[0].progress, "progress is absent unless the daemon sends it")
```

`AgentActionsTests.testSubtitle` — add after the existing three subtitle asserts (keep them unchanged):
```swift
        XCTAssertEqual(AgentTree.subtitle(state.agents[3]), "Orchestrator · 2/7 · Design · Queued")
        let running = AgentNode(name: "o", model: "m", role: .orchestrator, itemKey: "EPIC-1",
                                progress: AgentProgress(done: 3, total: 8, current: "TASK-12 · Add login form"))
        XCTAssertEqual(AgentTree.subtitle(running), "Orchestrator · 3/8 · TASK-12 · Add login form")
        let finished = AgentNode(name: "o", model: "m", role: .orchestrator, itemKey: "EPIC-1",
                                 progress: AgentProgress(done: 8, total: 8, current: ""))
        XCTAssertEqual(AgentTree.subtitle(finished), "Orchestrator · 8/8 · Done")
        XCTAssertEqual(AgentTree.subtitle(AgentNode(name: "o", model: "m", role: .orchestrator, itemKey: "EPIC-1")),
                       "Orchestrator · EPIC-1", "no progress: unchanged")
```

- [ ] **Step 2: Run, watch it fail**

Run: `cd apps/menubar && swift test --filter 'FixtureTests|AgentActionsTests'`
Expected: FAIL — compile error `cannot find 'AgentProgress' in scope`.
Also run: `go test ./internal/httpapi -run TestMenubarContract -count=1` → PASS (T5 added the Go field; this proves the fixture edit is contract-clean).

- [ ] **Step 3: Implement**

`Wire.swift` — above `public struct AgentNode`:
```swift
/// `progress` on an orchestrator AgentNode: its root item's to-do list summary.
public struct AgentProgress: Codable, Sendable, Equatable {
    public var done: Int
    public var total: Int
    public var current: String

    public init(done: Int, total: Int, current: String) {
        self.done = done; self.total = total; self.current = current
    }
}
```
In `AgentNode`: add `public var progress: AgentProgress?` after `preflightError`; add `progress` to the first `case` line of `CodingKeys`; add init parameter `progress: AgentProgress? = nil` after `preflightError: String? = nil,` and `self.progress = progress` in the body.

`Copy.swift` — after `public static let done = "Done"`:
```swift
    public static let progressDone = "Done"
```

`AgentActions.swift` — replace `subtitle`:
```swift
    /// Line 2: the orchestrator's progress ("3/8 · <current>") when the daemon
    /// sends one, else workflow step for a workflow agent, item key for a
    /// legacy agent, plus the handoff phase while a replacement is in flight,
    /// else the state label when not running.
    public static func subtitle(_ a: AgentNode) -> String {
        let middle = a.progress.map { "\($0.done)/\($0.total) · \($0.current.isEmpty ? Copy.progressDone : $0.current)" }
            ?? a.step ?? a.itemKey
        return ([Copy.roleLabel(a.role), middle] + [handoffStatus(a) ?? DisplayState(a).label].compactMap { $0 })
            .joined(separator: " · ")
    }
```

- [ ] **Step 4: Run, watch it pass; then everything that reads the fixture**

Run: `cd apps/menubar && swift test` → PASS
Run: `go test ./internal/httpapi -run TestMenubarContract -count=1` → PASS

- [ ] **Step 5: Commit**

```bash
git add apps/menubar/Sources/SwarmBarKit/Wire.swift apps/menubar/Sources/SwarmBarKit/AgentActions.swift apps/menubar/Sources/SwarmBarKit/Copy.swift apps/menubar/Tests/Fixtures/state.json apps/menubar/Tests/SwarmBarTests/FixtureTests.swift apps/menubar/Tests/SwarmBarTests/AgentActionsTests.swift
git commit -m "feat(menubar): show orchestrator progress in the agent row subtitle

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 8: Skills — the progress-list rule

**Files:**
- Modify: `skills/swarm-orchestrator/SKILL.md` ("Owning an item" and "Spikes" sections)
- Modify: `skills/swarm-spike/SKILL.md`
- Copy to: `internal/install/skills/swarm-orchestrator/SKILL.md`, `internal/install/skills/swarm-spike/SKILL.md` (byte-identical; gate is `TestEmbeddedMirrorMatchesCanonicalTree`, skills_test.go:288)
- Modify: `internal/install/skills_test.go` (add one test)

**Interfaces:**
- Consumes: nothing from T1–T7 (text only); `install.SkillBody(name) []byte`.
- Produces: skill text agents follow.

- [ ] **Step 1: Write the failing test**

Append to `internal/install/skills_test.go`:

```go
func TestSkillsCarryTheProgressListRule(t *testing.T) {
	orch := string(install.SkillBody("swarm-orchestrator"))
	for _, want := range []string{
		"- Progress list: when `swarm_sync` returns `todos`, replace your native to-do list with it",
		"Codex: `update_plan` (only one `in_progress` is allowed",
		"agy: rewrite your `task.md` artifact (`[x]` completed, `[/]` in progress, `[ ]` pending)",
		"never send `todos` for it.",
		"- Report spike step progress on each checkpoint with `todos: [{id, status}]`",
		"debug: frame, evidence, root_cause, report, plan, critic, approve",
		"Swarm ticks `spec` and `approve` itself.",
	} {
		if !strings.Contains(orch, want) {
			t.Errorf("swarm-orchestrator missing %q", want)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(orch, "\n"), "remove your worktrees with `swarm_worktree` `op: \"remove\"`, and stop.") {
		t.Error("the materialize line must stay last in swarm-orchestrator")
	}
	if spike := string(install.SkillBody("swarm-spike")); !strings.Contains(spike, "`todos`") {
		t.Error("swarm-spike must point at the todos rule")
	}
}
```

- [ ] **Step 2: Run, watch it fail**

Run: `go test ./internal/install -run TestSkillsCarryTheProgressListRule -count=1`
Expected: FAIL — `swarm-orchestrator missing "- Progress list: …"`.

- [ ] **Step 3: Edit the skills**

`skills/swarm-orchestrator/SKILL.md`, "Owning an item": insert this bullet immediately after the `- Don't poll. End your turn when waiting; the daemon wakes you.` line:
```
- Progress list: when `swarm_sync` returns `todos`, replace your native to-do list with it — labels verbatim, same order, same statuses. Claude: `TaskCreate`/`TaskUpdate`; Codex: `update_plan` (only one `in_progress` is allowed: mark the first, prefix the other running labels with "▶ " and keep them `pending`); Cursor: `TodoWrite` with `merge: false`; Muse: `write_todos`; agy: rewrite your `task.md` artifact (`[x]` completed, `[/]` in progress, `[ ]` pending). Don't add, rename or drop entries. For an epic, bug or chore the list is the item's tasks and is maintained by Swarm; never send `todos` for it.
```

Same file, "Spikes": insert this bullet immediately **before** the last line (`- When everything is approved, call \`swarm_materialize\` …`), so that line stays last:
```
- Report spike step progress on each checkpoint with `todos: [{id, status}]` using the ids from your list (feature: frame, research, design, spec, plan, critic, approve; debug: frame, evidence, root_cause, report, plan, critic, approve). Keep at most one `in_progress`. Swarm ticks `spec` and `approve` itself.
```

`skills/swarm-spike/SKILL.md`: insert after the numbered list (after the line starting `7. Plan approval and materialize.`) a blank line and this paragraph — **not** a numbered `8.` item (`TestSpikeSkillSevenStepMethod` expects exactly the seven steps):
```
Report each step's status on every `swarm_checkpoint` with `todos`, as the Spikes section of `swarm-orchestrator` describes.
```

Mirror:
```bash
cp skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md
cp skills/swarm-spike/SKILL.md internal/install/skills/swarm-spike/SKILL.md
```

- [ ] **Step 4: Run, watch it pass; then the package**

Run: `go test ./internal/install -run TestSkillsCarryTheProgressListRule -count=1 -v` → PASS
Run: `go test ./internal/install -count=1` → PASS (incl. `TestEmbeddedMirrorMatchesCanonicalTree`, `TestSpikeSkillSevenStepMethod`).

- [ ] **Step 5: Commit**

```bash
git add skills/swarm-orchestrator/SKILL.md skills/swarm-spike/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-spike/SKILL.md internal/install/skills_test.go
git commit -m "docs(skills): mirror swarm_sync todos into the native to-do list

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 9: Final verification

**Files:** none changed (fix only what a check reports, in the task that owns the file, then re-run this task).

- [ ] **Step 1: Static checks**

Run: `gofmt -l .` → no output. `go vet ./...` → no output.

- [ ] **Step 2: All Go tests**

Run: `go test ./... -count=1` → PASS.

- [ ] **Step 3: Web**

Run: `cd web && npm test && npm run build` → PASS.

- [ ] **Step 4: Menubar**

Run: `cd apps/menubar && swift test` → PASS.

- [ ] **Step 5: Migration on a copy of the live DB (never the live file)**

Run the real migrator (`db.Open` via `swarm lineage`, which opens `<home>/swarm.db` and migrates it) against a scratch copy:

```bash
SCRATCH=$(mktemp -d)
sqlite3 ~/.swarm/swarm.db ".backup $SCRATCH/swarm.db"
sqlite3 "$SCRATCH/swarm.db" "PRAGMA user_version; SELECT COUNT(*) FROM checkpoints; SELECT COUNT(*) FROM sessions; SELECT COUNT(*) FROM items;" > "$SCRATCH/before.txt"
go run ./cmd/swarm lineage --home "$SCRATCH" no-such-agent || true   # migration runs inside db.Open before the name lookup
sqlite3 "$SCRATCH/swarm.db" "PRAGMA user_version; SELECT COUNT(*) FROM checkpoints; SELECT COUNT(*) FROM sessions; SELECT COUNT(*) FROM items;" > "$SCRATCH/after.txt"
diff "$SCRATCH/before.txt" "$SCRATCH/after.txt"
sqlite3 "$SCRATCH/swarm.db" "SELECT name FROM pragma_table_info('checkpoints') WHERE name='todos_json'; SELECT name FROM pragma_table_info('sessions') WHERE name='todos_sent_hash';"
```

Expected: `diff` shows only the first line changing `20` → `21`; the three counts are identical; the last command prints `todos_json` and `todos_sent_hash`. If `lineage` fails with a "no swarm database" error, the copy path is wrong — fix `SCRATCH`, never point `--home` at `~/.swarm`.

- [ ] **Step 6: Report**

No commit (nothing changed). Report each command's result. Live scenarios (spec Verification §4: chore orchestrator on Claude and Codex, TUI list, menubar subtitle, web Progress block) run after deploy and are out of this plan's scope.

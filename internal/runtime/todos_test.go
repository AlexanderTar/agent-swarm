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
	wantIDs := []string{"context", direct.Key, t1.Key, t2.Key, t3.Key, "integrate", "accept"}
	if !reflect.DeepEqual(todoIDs(got), wantIDs) {
		t.Fatalf("ids = %v, want %v", todoIDs(got), wantIDs)
	}
	wantSt := []TodoStatus{TodoCompleted, TodoPending, TodoInProgress, TodoCompleted, TodoPending, TodoPending, TodoPending}
	if !reflect.DeepEqual(todoStatuses(got), wantSt) {
		t.Fatalf("statuses = %v, want %v", todoStatuses(got), wantSt)
	}
	if got[2].Label != t1.Key+" · One" || got[2].ItemKey != t1.Key {
		t.Fatalf("task entry = %+v", got[2])
	}
	if got[5].Label != "Merging and verifying" || got[6].Label != "Finishing: PR or merge" || got[5].ItemKey != "" {
		t.Fatalf("fixed entries = %+v %+v", got[5], got[6])
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
	if st := todoStatuses(got)[3:]; !reflect.DeepEqual(st, []TodoStatus{TodoInProgress, TodoPending}) {
		t.Fatalf("integrate/accept = %v", st)
	}
}

func TestTodosChoreWithZeroTasks(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ch := seedTopLevelItem(t, s, items.Chore)
	got := mustTodos(t, s, ch.ID)
	if !reflect.DeepEqual(todoIDs(got), []string{"context", "work", "integrate", "accept"}) ||
		!reflect.DeepEqual(todoStatuses(got), []TodoStatus{TodoInProgress, TodoPending, TodoPending, TodoPending}) {
		t.Fatalf("got %+v", got)
	}
	if got[0].Label != "Gathering context" || got[1].Label != "Making the changes" {
		t.Fatalf("labels = %q / %q", got[0].Label, got[1].Label)
	}
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: ch.Key, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	insertCheckpoint(t, s, ses.ID, orch.ID, ch.ID, "progress", nil)
	if st := todoStatuses(mustTodos(t, s, ch.ID)); !reflect.DeepEqual(st, []TodoStatus{TodoCompleted, TodoInProgress, TodoPending, TodoPending}) {
		t.Fatalf("after progress: %v", st)
	}
	insertCheckpoint(t, s, ses.ID, orch.ID, ch.ID, "integrated", nil)
	got = mustTodos(t, s, ch.ID)
	if !reflect.DeepEqual(todoStatuses(got), []TodoStatus{TodoCompleted, TodoCompleted, TodoCompleted, TodoInProgress}) {
		t.Fatalf("after integrated: %v", todoStatuses(got))
	}
	execSQL(t, s, `UPDATE items SET status = 'done' WHERE id = ?`, ch.ID)
	if st := todoStatuses(mustTodos(t, s, ch.ID)); st[3] != TodoCompleted {
		t.Fatalf("accept after done = %s", st[3])
	}
}

func TestTodosChoreContextCompletesWithATask(t *testing.T) {
	s, _, _ := newStore(t)
	ch := seedTopLevelItem(t, s, items.Chore)
	task := mkItem(t, s, items.Task, ch.Key, "Bump")
	got := mustTodos(t, s, ch.ID)
	if !reflect.DeepEqual(todoIDs(got), []string{"context", task.Key, "integrate", "accept"}) || got[0].Status != TodoCompleted {
		t.Fatalf("got %+v", got)
	}
	execSQL(t, s, `UPDATE items SET status = 'cancelled' WHERE id = ?`, task.ID)
	got = mustTodos(t, s, ch.ID)
	if !reflect.DeepEqual(todoIDs(got), []string{"context", "work", "integrate", "accept"}) || got[0].Status != TodoInProgress {
		t.Fatalf("cancelled task: %+v", got)
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
	if got[0].Label != "Understanding the request" || got[6].Label != "Reviewing the plan and setting up tasks" {
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
	// BUG-75: a proposal naming the spike as origin is not materialization.
	proposed := mkItem(t, s, items.Chore, "", "Proposed")
	execSQL(t, s, `UPDATE items SET origin_spike_id = ? WHERE id = ?`, spikeID, proposed.ID)
	if st := todoStatuses(mustTodos(t, s, spikeID)); st[6] != TodoPending {
		t.Fatalf("approve after a proposal = %s", st[6])
	}
	ep := mkItem(t, s, items.Epic, "", "Materialized")
	execSQL(t, s, `UPDATE items SET origin_spike_id = ?, materialized = 1 WHERE id = ?`, spikeID, ep.ID)
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
	if got := mustTodos(t, s, sp.ID)[0].Label; got != "Understanding the problem" {
		t.Fatalf("debug frame label = %q", got)
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
	if got := mustTodos(t, s, ep.ID); len(got) != 4 {
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
	if *p != (TodoProgress{Done: 1, Total: 3, Current: "C"}) {
		t.Fatalf("got %+v", *p)
	}
	p = ProgressOf([]Todo{{ID: "a", Label: "A", Status: TodoCompleted}, {ID: "b", Label: "B", Status: TodoPending}})
	if p.Current != "B" {
		t.Fatalf("first pending: %+v", *p)
	}
	p = ProgressOf([]Todo{{ID: "a", Label: "A", Status: TodoCompleted}})
	if *p != (TodoProgress{Done: 1, Total: 1, Current: ""}) {
		t.Fatalf("all done: %+v", *p)
	}
}

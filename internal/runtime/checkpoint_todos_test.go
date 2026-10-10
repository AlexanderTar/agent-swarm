package runtime

import (
	"context"
	"reflect"
	"strings"
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
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "frame", Status: TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "frame", Status: TodoCompleted}, TodoReport{ID: "research", Status: TodoInProgress}); err != nil {
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
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "research", Status: TodoCompleted}); err != nil {
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
	_, err := ckTodos(s, ses.ID, TodoReport{ID: "bogus", Status: TodoPending})
	wantErr(t, err, `todos: unknown step "bogus" for a feature spike; use: frame, research, design, spec, plan, critic, approve`)
	_, err = ckTodos(s, ses.ID, TodoReport{ID: "frame", Status: "done"})
	wantErr(t, err, `todos: status must be pending, in_progress or completed (got "done")`)
	_, err = ckTodos(s, ses.ID, TodoReport{ID: "frame", Status: TodoInProgress}, TodoReport{ID: "research", Status: TodoInProgress})
	wantErr(t, err, `todos: at most one step may be in_progress (frame, research)`)
	// Across the merge too.
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "frame", Status: TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	_, err = ckTodos(s, ses.ID, TodoReport{ID: "research", Status: TodoInProgress})
	wantErr(t, err, `todos: at most one step may be in_progress (frame, research)`)
}

func TestCheckpointTodosCountInProgressAfterDaemonFacts(t *testing.T) {
	s, _, _ := newStore(t)
	req, ses, _ := seedSectionApproval(t, s)
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "spec", Status: TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE requests SET state = 'approved' WHERE id = ?`, req.ID); err != nil {
		t.Fatal(err)
	}
	// spec is now completed by fact, so plan may be the one in_progress.
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "plan", Status: TodoInProgress}); err != nil {
		t.Fatalf("plan in_progress after spec approval: %v", err)
	}
}

func TestCheckpointTodosDebugSpikeIds(t *testing.T) {
	s, _, _ := newStore(t)
	ses, _ := spikeOrchestrator(t, s, "debug")
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "root_cause", Status: TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	_, err := ckTodos(s, ses.ID, TodoReport{ID: "research", Status: TodoPending})
	wantErr(t, err, `todos: unknown step "research" for a debug spike; use: frame, evidence, root_cause, report, plan, critic, approve`)
}

func TestCheckpointTodosFromAWorkerAreIgnored(t *testing.T) {
	s, _, _ := newStore(t)
	_, _, wSes := worker(t, s)
	res, err := ckTodos(s, wSes.ID, TodoReport{ID: "frame", Status: TodoCompleted})
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
	_, err = ckTodos(s, ses.ID, TodoReport{ID: "frame", Status: TodoCompleted})
	wantErr(t, err, "todo statuses for "+ep.Key+" are derived from its tasks; send only labels for: context, work, integrate, accept")

	cses, sp := spikeOrchestrator(t, s, "feature")
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET spike_intent = 'chore' WHERE id = ?`, sp.ID); err != nil {
		t.Fatal(err)
	}
	_, err = ckTodos(s, cses.ID, TodoReport{ID: "frame", Status: TodoCompleted})
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

// CHORE-64: a root orchestrator names the fixed steps for the work at hand; statuses stay derived.
func TestCheckpointTodoLabelsRenameRootSteps(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Clean caches", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, orch.ID)
	if _, err := ckTodos(s, ses, TodoReport{ID: "work", Label: "Clearing Chrome and tmp caches"},
		TodoReport{ID: "accept", Label: "Finishing: accept the cleanup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ckTodos(s, ses, TodoReport{ID: "integrate", Label: "Checking free disk space"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Progress, Summary: "no todos"}); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, key)
	got := mustTodos(t, s, it.ID)
	want := []Todo{{ID: "context", Label: "Gathering context", Status: TodoCompleted},
		{ID: "work", Label: "Clearing Chrome and tmp caches", Status: TodoInProgress},
		{ID: "integrate", Label: "Checking free disk space", Status: TodoPending},
		{ID: "accept", Label: "Finishing: accept the cleanup", Status: TodoPending}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("todos = %+v\nwant %+v", got, want)
	}

	_, err = ckTodos(s, ses, TodoReport{ID: "accept", Status: TodoCompleted})
	wantErr(t, err, "todo statuses for "+key+" are derived from its tasks; send only labels for: context, work, integrate, accept")
	_, err = ckTodos(s, ses, TodoReport{ID: "deploy", Label: "Deploying"})
	wantErr(t, err, "todos: unknown step \"deploy\" for "+key+"; use: context, work, integrate, accept")
	_, err = ckTodos(s, ses, TodoReport{ID: "work", Label: strings.Repeat("x", 81)})
	wantErr(t, err, "todos: a label must be 1–80 characters")
}

// CHORE-64: a spike entry may carry a label; a label-only entry keeps the step's status.
func TestCheckpointTodoLabelsOnSpikeSteps(t *testing.T) {
	s, _, _ := newStore(t)
	ses, sp := spikeOrchestrator(t, s, "feature")
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "frame", Status: TodoInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := ckTodos(s, ses.ID, TodoReport{ID: "frame", Label: "Understanding the finish prompt"}); err != nil {
		t.Fatal(err)
	}
	got := mustTodos(t, s, sp.ID)
	if got[0] != (Todo{ID: "frame", Label: "Understanding the finish prompt", Status: TodoInProgress}) ||
		got[1].Label != "Researching" {
		t.Fatalf("todos = %+v", got[:2])
	}
}

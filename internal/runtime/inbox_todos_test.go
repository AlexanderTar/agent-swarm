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

func TestSyncPairsTodosWithAKindSpecificTodosNext(t *testing.T) {
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
	fake, err := s.Sync(ctx, ses.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fake.Todos == nil || fake.TodosNext != "" {
		t.Fatalf("fake: todos=%v todos_next=%q", fake.Todos, fake.TodosNext)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'claude' WHERE id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE key = 'TASK-1'`); err != nil {
		t.Fatal(err)
	}
	claude, err := s.Sync(ctx, ses.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if claude.Todos == nil || claude.TodosNext != todosNext[Claude] {
		t.Fatalf("claude: todos=%v todos_next=%q", claude.Todos, claude.TodosNext)
	}
	again, err := s.Sync(ctx, ses.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Todos != nil || again.TodosNext != "" {
		t.Fatalf("unchanged: todos=%v todos_next=%q", again.Todos, again.TodosNext)
	}
	for _, k := range AgentKinds {
		if todosNext[k] == "" {
			t.Errorf("no todos_next for %s", k)
		}
	}
	if want := "Update your task list now to match todos exactly: TaskCreate each missing entry, then TaskUpdate every status. Labels verbatim, same order; don't add, rename or drop entries."; todosNext[Claude] != want {
		t.Errorf("claude todos_next = %q", todosNext[Claude])
	}
}

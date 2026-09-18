package runtime

import (
	"context"
	"os/exec"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func seedEpicWithTask(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	// `ready` directly, not through Transition: the daemon's Ready->InProgress hop
	// needs an accepted checkpoint, which these tests are not about.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?)`,
		ep.ID, story.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

func seedEpicWithTwoTasks(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t1, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Implement the fix"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?, ?)`,
		ep.ID, story.ID, t1.ID, t2.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

func seedEpicWithThreeTasks(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t1, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Implement the fix"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t3, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Refactor"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?, ?, ?)`,
		ep.ID, story.ID, t1.ID, t2.ID, t3.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

// startSessionForTest exposes the unexported startSession to other test files
// in the package (Task 15: TDD evidence survives a pause into a new generation).
func (s *Store) startSessionForTest(ctx context.Context, a Agent, attempt, generation int) (Session, error) {
	return s.startSession(ctx, a, attempt, generation, false, "")
}

func gitRepoNoSigning(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

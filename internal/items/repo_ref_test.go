package items_test

import (
	"slices"

	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// TestResolveRepoRefTx (BUG-68): a repo ref is a catalog id, a unique name or
// an absolute path, always resolved to the catalog id; anything else is refused.
func TestResolveRepoRefTx(t *testing.T) {
	s := newStore(t)
	exec(t, s.DB, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at) VALUES
		('repo_a', 'alpha', '/tmp/alpha', 'main', 'manual', 1, 1),
		('repo_b', 'chat', '/tmp/chat1', 'main', 'manual', 1, 1),
		('repo_c', 'chat', '/tmp/chat2', 'main', 'manual', 1, 1)`)
	for ref, want := range map[string]string{
		"repo_a": "repo_a", "alpha": "repo_a", "/tmp/alpha": "repo_a", "/tmp/alpha/": "repo_a", "/tmp/chat2": "repo_c",
	} {
		got, err := items.ResolveRepoRefTx(ctx, s.DB, ref)
		if err != nil || got != want {
			t.Errorf("ResolveRepoRefTx(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	for ref, want := range map[string]string{
		"nope": `Unknown repository "nope". Pass a repository id or name from swarm_read {repos:{q:"nope"}}, or register its local Git path with swarm_repo_register.`,
		"chat": `Repository name "chat" is ambiguous. Pass a repository id or a unique name from swarm_read {repos:{q:"chat"}}, or register its local Git path with swarm_repo_register.`,
	} {
		got, err := items.ResolveRepoRefTx(ctx, s.DB, ref)
		if err == nil || err.Error() != want || code(err) != items.CodeBadRequest {
			t.Errorf("ResolveRepoRefTx(%q) = %q, %v; want %s", ref, got, err, want)
		}
	}
}

// TestCreateCanonicalizesRepoRefs (BUG-68): swarm_items create takes a repo
// name or path as well as an id, for a proposed root and a child task, and
// always stores the catalog id.
func TestCreateCanonicalizesRepoRefs(t *testing.T) {
	s := newStore(t)
	exec(t, s.DB, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at)
		VALUES ('repo_a', 'alpha', '/tmp/alpha', 'main', 'manual', 1, 1)`)
	e := mk(t, s, items.Epic, "", "E")
	st := mk(t, s, items.Story, e.Key, "S")
	orch := items.Orchestrator("agt_1", e.ID)
	proposed, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "New epic", Repos: []string{"alpha"}}, orch)
	if err != nil || !slices.Equal(proposed.SuggestedRepos, []string{"repo_a"}) {
		t.Fatalf("proposed = %v, %v; want suggested [repo_a]", proposed.SuggestedRepos, err)
	}
	task, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "T", Repos: []string{"/tmp/alpha"},
		Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch)
	if err != nil || !slices.Equal(task.Repos, []string{"repo_a"}) {
		t.Fatalf("task = %v, %v; want repos [repo_a]", task.Repos, err)
	}
	_, err = s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: st.Key, Title: "T", Repos: []string{"nope"},
		Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch)
	want := `Unknown repository "nope". Pass a repository id or name from swarm_read {repos:{q:"nope"}}, or register its local Git path with swarm_repo_register.`
	if err == nil || err.Error() != want || code(err) != items.CodeBadRequest {
		t.Fatalf("err = %v, want %s", err, want)
	}
}

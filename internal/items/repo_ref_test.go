package items_test

import (
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
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

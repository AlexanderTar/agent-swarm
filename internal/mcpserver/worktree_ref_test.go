package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

const unknownRefCopy = "Unknown worktree /nope/wt. Pass the worktree id or path that swarm_worktree create returned."

func createRefWorktree(t *testing.T, s *Server, seed orchSeed, branch string) (id, path string) {
	t.Helper()
	out, err := s.call(context.Background(), seed.Caller, "swarm_worktree",
		fmt.Sprintf(`{"op":"create","repo":%q,"branch":%q}`, seed.RepoID, branch))
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(mustJSON(out), &wt); err != nil || wt.Path == "" {
		t.Fatalf("create = %+v, %v", wt, err)
	}
	return wt.WorktreeID, wt.Path
}

func TestWorktreeOpsAcceptPathRef(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	_, path := createRefWorktree(t, s, seed, "task/pathref")
	worker := spawnWorker(t, s, seed)
	for _, call := range []string{
		fmt.Sprintf(`{"op":"share","worktree":%q,"agent":%q,"mode":"ro"}`, path, worker.Name),
		fmt.Sprintf(`{"op":"release","worktree":%q,"agent":%q}`, path, worker.Name),
		fmt.Sprintf(`{"op":"remove","worktree":%q}`, path),
	} {
		if _, err := s.call(ctx, seed.Caller, "swarm_worktree", call); err != nil {
			t.Fatalf("%s: %v", call, err)
		}
	}
	_, err := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"share","worktree":"/nope/wt","agent":"`+worker.Name+`","mode":"ro"}`)
	if err == nil || !strings.Contains(err.Error(), unknownRefCopy) {
		t.Fatalf("unknown ref err = %v, want %q", err, unknownRefCopy)
	}
}

func TestSpawnAcceptsWorktreePathRef(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	_, path := createRefWorktree(t, s, seed, "task/spawnref")
	spawn := func(ref string) error {
		_, err := s.call(ctx, seed.Caller, "swarm_spawn", `{"item":"`+seed.TaskKey+`","role":"coder",
			"brief":{"objective":"Build it","acceptance":["It works."],"scope_in":["web/"],
			"scope_out":[],"context":[],"verify":["go test ./..."],"stop_when":["done"]},
			"worktrees":[{"worktree":`+fmt.Sprintf("%q", ref)+`,"mode":"rw"}]}`)
		return err
	}
	if err := spawn(path); err != nil {
		t.Fatalf("spawn with path ref: %v", err)
	}
	if err := spawn("/nope/wt"); err == nil || !strings.Contains(err.Error(), unknownRefCopy) {
		t.Fatalf("unknown ref err = %v, want %q", err, unknownRefCopy)
	}
}

func TestWorkflowStartAcceptsWorktreePathRef(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	_, path := createRefWorktree(t, s, seed, "wf-pathref")
	if _, err := s.RT.Items.Update(ctx, seed.TaskKey, items.Patch{
		Workflow: &workflow.Spec{Template: "mechanical"},
		Steps:    &[]string{"step 1"},
		Verify:   &[]string{"true"},
		Revision: 1,
	}, items.Daemon()); err != nil {
		t.Fatal(err)
	}
	start := func(ref string) error {
		_, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
			`{"op":"start","item":%q,"worktrees":[{"worktree":%q,"mode":"rw"}]}`, seed.TaskKey, ref))
		return err
	}
	if err := start("/nope/wt"); err == nil || !strings.Contains(err.Error(), unknownRefCopy) {
		t.Fatalf("unknown ref err = %v, want %q", err, unknownRefCopy)
	}
	if err := start(path); err != nil {
		t.Fatalf("start with path ref: %v", err)
	}
}

// BUG-73: swarm_worktree create/review accept a unique catalog name, as swarm_items does.
func TestWorktreeCreateAndReviewAcceptRepoName(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	named := seedRepoIn(t, s, "bug73-named-repo")
	repoOf := func(out any) string {
		t.Helper()
		var wt struct {
			WorktreeID string `json:"worktree_id"`
		}
		if err := json.Unmarshal(mustJSON(out), &wt); err != nil {
			t.Fatal(err)
		}
		var repoID string
		if err := s.RT.DB.QueryRowContext(ctx, `SELECT repo_id FROM worktrees WHERE id = ?`, wt.WorktreeID).Scan(&repoID); err != nil {
			t.Fatal(err)
		}
		return repoID
	}
	out, err := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"create","repo":"bug73-named-repo","branch":"task/by-name"}`)
	if err != nil {
		t.Fatalf("create by name: %v", err)
	}
	if got := repoOf(out); got != named {
		t.Fatalf("create by name repo_id = %q, want %q", got, named)
	}
	var path string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT path FROM repos WHERE id = ?`, named).Scan(&path); err != nil {
		t.Fatal(err)
	}
	out, err = s.call(ctx, seed.Caller, "swarm_worktree",
		fmt.Sprintf(`{"op":"review","repo":"bug73-named-repo","sha":%q}`, headSHA(t, path)))
	if err != nil {
		t.Fatalf("review by name: %v", err)
	}
	if got := repoOf(out); got != named {
		t.Fatalf("review by name repo_id = %q, want %q", got, named)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		fmt.Sprintf(`{"op":"create","repo":%q,"branch":"task/by-id"}`, seed.RepoID)); err != nil {
		t.Fatalf("create by id: %v", err)
	}
	seedRepoIn(t, s, "bug73-twin")
	seedRepoIn(t, s, "bug73-twin")
	_, err = s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"create","repo":"bug73-twin","branch":"task/twin"}`)
	if err == nil || !strings.Contains(err.Error(), `Repository name "bug73-twin" is ambiguous`) {
		t.Fatalf("ambiguous name err = %v", err)
	}
	_, err = s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"create","repo":"bug73-nope","branch":"task/nope"}`)
	if err == nil || !strings.Contains(err.Error(), `Unknown repository "bug73-nope"`) {
		t.Fatalf("unknown name err = %v", err)
	}
}

// BUG-73: swarm_repos accepts a unique catalog name and stores its id.
func TestReposAcceptsRepoNameAndStoresID(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	named := seedRepoIn(t, s, "bug73-hint-repo")
	seedRepoIn(t, s, "bug73-hint-twin")
	seedRepoIn(t, s, "bug73-hint-twin")
	root, err := s.RT.Items.Get(ctx, seed.RootKey)
	if err != nil {
		t.Fatal(err)
	}
	set := func(ref string) error {
		_, err := s.call(ctx, seed.Caller, "swarm_repos",
			fmt.Sprintf(`{"repos":[%q,%q],"repos_version":%d}`, seed.RepoID, ref, root.ReposVersion))
		return err
	}
	if err := set("bug73-hint-twin"); err == nil || !strings.Contains(err.Error(), `Repository name "bug73-hint-twin" is ambiguous`) {
		t.Fatalf("ambiguous name err = %v", err)
	}
	if err := set("bug73-hint-nope"); err == nil || !strings.Contains(err.Error(), `Unknown repository "bug73-hint-nope"`) {
		t.Fatalf("unknown name err = %v", err)
	}
	if err := set("bug73-hint-repo"); err != nil {
		t.Fatalf("set by name: %v", err)
	}
	got, err := s.RT.Items.Get(ctx, seed.RootKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Repos, ",") != seed.RepoID+","+named {
		t.Fatalf("stored repos = %v, want ids [%s %s]", got.Repos, seed.RepoID, named)
	}
}

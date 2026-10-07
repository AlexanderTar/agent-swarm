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

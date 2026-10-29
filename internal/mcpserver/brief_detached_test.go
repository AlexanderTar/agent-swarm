package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TASK-1492 unit 3: a manual swarm_spawn with a detached swarm_worktree
// review tree (ro) renders '@ <sha7> (detached, ro)' with no base clause,
// never an empty branch and never the head as its own base.
func TestSwarmSpawnDetachedReviewBrief(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	sha := headSHA(t, seed.RepoPath)

	reviewOut, err := s.call(ctx, seed.Caller, "swarm_worktree",
		fmt.Sprintf(`{"op":"review","repo":%q,"sha":%q}`, seed.RepoID, sha))
	if err != nil {
		t.Fatal(err)
	}
	var reviewRes struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(mustJSON(reviewOut), &reviewRes); err != nil {
		t.Fatal(err)
	}

	spawnOut, err := s.call(ctx, seed.Caller, "swarm_spawn",
		fmt.Sprintf(`{"item":%q,"role":"reviewer","brief":{"objective":"review it"},"worktrees":[{"worktree":%q,"mode":"ro"}]}`,
			seed.TaskKey, reviewRes.WorktreeID))
	if err != nil {
		t.Fatal(err)
	}
	var spawnRes struct {
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(mustJSON(spawnOut), &spawnRes); err != nil {
		t.Fatal(err)
	}
	agent, err := s.RT.Agent(ctx, spawnRes.Agent)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("agent brief:\n%s", agent.Brief)
	head7 := sha[:7]
	if !strings.Contains(agent.Brief, "@ "+head7+" (detached, ro)") {
		t.Errorf("brief has no '@ %s (detached, ro)' line:\n%s", head7, agent.Brief)
	}
	if strings.Contains(agent.Brief, "@  (") {
		t.Errorf("brief prints an empty '@  (' line:\n%s", agent.Brief)
	}
	if strings.Contains(agent.Brief, "base "+head7) {
		t.Errorf("brief prints the review head %s as its own base:\n%s", head7, agent.Brief)
	}
}

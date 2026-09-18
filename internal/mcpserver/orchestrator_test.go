package mcpserver

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// §8.1: swarm_items can't touch another top-level item's tree.
func TestItemsIsScopedToTheCallersRoot(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"New task","brief":"b","acceptance":["a"]}`); err != nil {
		t.Fatal(err)
	}
	other := seedOtherRoot(t, s)
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+other+`","title":"not mine","revision":1}`); err == nil {
		t.Fatal("an orchestrator cannot touch another root's items")
	}
}

// §8.1: swarm_spawn fills defaults from Settings and validates the brief.
func TestSpawnDefaultsAndValidation(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_spawn", `{"item":"`+seed.TaskKey+`","role":"coder",
		"brief":{"objective":"Build it","acceptance":["It works."],"scope_in":["web/"],
		"scope_out":[],"context":[],"verify":["go test ./..."],"stop_when":["done"]},
		"worktrees":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Agent  string `json:"agent"`
		Queued bool   `json:"queued"`
	}
	json.Unmarshal(mustJSON(out), &res)
	if res.Agent == "" {
		t.Fatalf("result = %+v", res)
	}
	a, err := s.RT.Agent(ctx, res.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind == "" || a.Model == "" {
		t.Fatalf("the agent and model default from Settings: %+v", a)
	}
	if !strings.Contains(a.Brief, "## Objective") || !strings.Contains(a.Brief, "Build it") {
		t.Fatalf("the brief is rendered from the §9.4 template:\n%s", a.Brief)
	}
	// a disabled agent and a bad model are refused
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn",
		`{"item":"`+seed.TaskKey+`","role":"coder","agent":"codex","brief":{"objective":"x"},"worktrees":[]}`); err == nil {
		t.Fatal("a disabled agent must be refused")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn",
		`{"item":"`+seed.TaskKey+`","role":"coder","model":"gone-9","brief":{"objective":"x"},"worktrees":[]}`); err == nil {
		t.Fatal("a model outside the catalog must be refused")
	}
	// §17.3: an over-long brief
	long := strings.Repeat("x", 6100)
	_, err = s.call(ctx, seed.Caller, "swarm_spawn",
		`{"item":"`+seed.TaskKey+`","role":"coder","brief":{"objective":"`+long+`"},"worktrees":[]}`)
	if err == nil || err.Error() != "Brief too long (max 6000 characters). Move detail into an artifact and reference it." {
		t.Fatalf("err = %v", err)
	}
}

// I12: spawning is refused while the item has open dependencies.
func TestSpawnRefusesOpenDependencies(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	blockOn(t, s, seed.TaskKey, seed.OtherTaskKey)
	_, err := s.call(ctx, seed.Caller, "swarm_spawn",
		`{"item":"`+seed.TaskKey+`","role":"coder","brief":{"objective":"x"},"worktrees":[]}`)
	if err == nil || !strings.HasPrefix(err.Error(), "dependencies_open: ") ||
		!strings.Contains(err.Error(), seed.OtherTaskKey) {
		t.Fatalf("err = %v", err)
	}
}

// §17.3: a worktree in an unconfirmed repo.
func TestWorktreeCreateNeedsAConfirmedRepo(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	unconfirmed := seedRepoIn(t, s, "other-repo")
	_, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+unconfirmed+`","branch":"task/x"}`)
	want := "repo_not_confirmed: other-repo is not confirmed for " + seed.RootKey +
		`. Ask with swarm_ask kind "confirm_repos".`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %q", err, want)
	}
	// after confirming, it works
	confirmRepo(t, s, seed.RootKey, unconfirmed)
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+unconfirmed+`","branch":"task/x"}`); err != nil {
		t.Fatal(err)
	}
}

// §8.1: only the owner may act on a worktree.
func TestWorktreeOpsAreOwnerOnly(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/mine"}`)
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		WorktreeID string `json:"worktree_id"`
	}
	if err := json.Unmarshal(mustJSON(out), &wt); err != nil {
		t.Fatal(err)
	}
	if wt.WorktreeID == "" {
		t.Fatal("create returned no worktree_id; the removal below would not be testing ownership")
	}
	stranger := seedOtherOrchestrator(t, s)
	if _, err := s.call(ctx, stranger, "swarm_worktree",
		`{"op":"remove","worktree":"`+wt.WorktreeID+`"}`); err == nil {
		t.Fatal("only the owner may remove a worktree")
	}
}

// I3: share sends an assignment_update with the path; the cwd does not change.
func TestWorktreeShareSendsAnAssignmentUpdate(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, _ := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/share"}`)
	// W1: every JSON key on the wire is snake_case, so the fields need tags. An
	// untagged `WorktreeID` looks for "WorktreeID" and silently decodes to "" (D74),
	// which would make the share below share nothing and still pass.
	var wt struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(mustJSON(out), &wt); err != nil {
		t.Fatal(err)
	}
	if wt.WorktreeID == "" || wt.Path == "" {
		t.Fatalf("create returned %+v; the share below would be a no-op", wt)
	}
	worker := spawnWorker(t, s, seed)
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"share","worktree":"`+wt.WorktreeID+`","agent":"`+worker.Name+`","mode":"ro"}`); err != nil {
		t.Fatal(err)
	}
	var payload string
	s.RT.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'assignment_update'`, worker.ID).Scan(&payload)
	if !strings.Contains(payload, wt.Path) {
		t.Fatalf("payload = %s", payload)
	}
	ses, _ := s.RT.LatestSession(ctx, worker.ID)
	if strings.Contains(ses.Cwd, wt.Path) {
		t.Fatal("a shared worktree does not move the session's cwd (I3)")
	}
}

// I5: review creates a detached worktree at the SHA.
func TestWorktreeReview(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	sha := headSHA(t, seed.RepoPath)
	out, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"review","repo":"`+seed.RepoID+`","sha":"`+sha+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	}
	json.Unmarshal(mustJSON(out), &wt)
	if wt.Branch != "" || !strings.Contains(wt.Path, "--review-"+sha[:7]) {
		t.Fatalf("worktree = %+v", wt)
	}
}

// §8.1: swarm_control only within the caller's subtree.
func TestControlIsSubtreeScoped(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	if _, err := s.call(ctx, seed.Caller, "swarm_control",
		`{"target":"`+worker.Name+`","action":"pause","scope":"session"}`); err != nil {
		t.Fatal(err)
	}
	stranger := seedOtherWorkerName(t, s)
	if _, err := s.call(ctx, seed.Caller, "swarm_control",
		`{"target":"`+stranger+`","action":"cancel"}`); err == nil {
		t.Fatal("an orchestrator may only control its own subtree")
	}
}

// I8: the retry note reaches the worker as an assignment_update.
func TestControlRetryCarriesTheNote(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	s.RT.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, worker.ID)
	if _, err := s.call(ctx, seed.Caller, "swarm_control",
		`{"target":"`+worker.Name+`","action":"retry","note":"The login test never fails first."}`); err != nil {
		t.Fatal(err)
	}
	var payload string
	s.RT.DB.QueryRowContext(ctx, `SELECT payload_json FROM messages WHERE to_agent_id = ?
		AND kind = 'assignment_update' ORDER BY seq DESC LIMIT 1`, worker.ID).Scan(&payload)
	if !strings.Contains(payload, "never fails first") {
		t.Fatalf("payload = %s", payload)
	}
}

// §8.1: swarm_artifact returns the section list and the stale requests.
func TestArtifactToolReturnsSectionsAndStaleRequests(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	p := writeSpec(t, "# Spec\n\n## One\n\na\n\n## Two\n\nb\n")
	out, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"register","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		ArtifactID    string                               `json:"artifact_id"`
		Revision      int                                  `json:"revision"`
		Sections      []struct{ ID, Title, SHA256 string } `json:"sections"`
		StaleRequests []string                             `json:"stale_requests"`
	}
	json.Unmarshal(mustJSON(out), &res)
	if len(res.Sections) != 2 || res.Revision != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.StaleRequests == nil {
		t.Fatal("stale_requests is always an array, never null (W4)")
	}
}

// ---------- coverage: the remaining swarm_items/swarm_control/swarm_worktree/
// swarm_materialize branches the tests above don't reach ----------

// swarm_read's repos/since_seq branches need an orchestrator with a
// confirmed repo, so they live here rather than in tools_test.go even though
// swarm_read is a shared tool. The wire shape here (items/confirmed_repos/
// events/reset/cursor) is invented for this batch — §8.1's literal result
// shape was not available, so the coordinator should check it against the
// spec.
func TestReadToolRefsFilterReposAndSinceSeq(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()

	out, err := s.call(ctx, seed.Caller, "swarm_read", `{"refs":["`+seed.TaskKey+`"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	json.Unmarshal(mustJSON(out), &res)
	if len(res.Items) != 1 || res.Items[0].Key != seed.TaskKey {
		t.Fatalf("refs result = %+v", res)
	}

	// a ref to an agent resolves via agentOut, a ref to an artifact resolves
	// via artifactOut, and a checkpoint on the ref'd item surfaces via
	// checkpointOut.
	worker := spawnWorker(t, s, seed)
	workerSes, err := s.RT.LatestSession(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	workerCaller := Caller{SessionID: workerSes.ID, AgentID: worker.ID, AgentName: worker.Name, Role: runtime.RoleCoder}
	if _, err := s.call(ctx, workerCaller, "swarm_checkpoint", `{"kind":"progress","summary":"working"}`); err != nil {
		t.Fatal(err)
	}
	p := writeSpec(t, "# Spec\n\n## One\n\na\n")
	artOut, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"register","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var art struct {
		ArtifactID string `json:"artifact_id"`
	}
	json.Unmarshal(mustJSON(artOut), &art)
	if art.ArtifactID == "" {
		t.Fatalf("artifact register result = %+v", art)
	}

	out, err = s.call(ctx, seed.Caller, "swarm_read",
		`{"refs":["`+seed.TaskKey+`","`+worker.Name+`","`+art.ArtifactID+`"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var res4 struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
		Artifacts []struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"artifacts"`
		Checkpoints []struct {
			Item string `json:"item"`
		} `json:"checkpoints"`
	}
	json.Unmarshal(mustJSON(out), &res4)
	if len(res4.Agents) != 1 || res4.Agents[0].Name != worker.Name {
		t.Fatalf("agent ref result = %+v", res4)
	}
	if len(res4.Artifacts) != 1 || res4.Artifacts[0].ArtifactID != art.ArtifactID {
		t.Fatalf("artifact ref result = %+v", res4)
	}
	if len(res4.Checkpoints) == 0 {
		t.Fatalf("a checkpoint on the ref'd item must surface: %+v", res4)
	}

	out, err = s.call(ctx, seed.Caller, "swarm_read", `{"filter":{"root":"`+seed.RootKey+`"}}`)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(mustJSON(out), &res)
	if len(res.Items) == 0 {
		t.Fatalf("filter result had no items: %+v", res)
	}

	// §8.1: "repos" input is a directory-search object, and confirmed_repos is
	// always returned for a bound caller regardless of whether "repos" is sent.
	out, err = s.call(ctx, seed.Caller, "swarm_read", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var res2 struct {
		ConfirmedRepos []struct {
			ID string `json:"id"`
		} `json:"confirmed_repos"`
	}
	json.Unmarshal(mustJSON(out), &res2)
	if len(res2.ConfirmedRepos) != 1 || res2.ConfirmedRepos[0].ID != seed.RepoID {
		t.Fatalf("confirmed_repos = %+v", res2)
	}

	// repos: {q} searches the repo directory (a different feature under the
	// same top-level key).
	out, err = s.call(ctx, seed.Caller, "swarm_read", `{"repos":{"q":"proj"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var res2b struct {
		Repos []struct {
			ID string `json:"id"`
		} `json:"repos"`
	}
	json.Unmarshal(mustJSON(out), &res2b)
	if len(res2b.Repos) != 1 || res2b.Repos[0].ID != seed.RepoID {
		t.Fatalf("repos search = %+v", res2b)
	}

	// since_seq well past anything ever issued comes back reset:true.
	out, err = s.call(ctx, seed.Caller, "swarm_read", `{"since_seq":999999999}`)
	if err != nil {
		t.Fatal(err)
	}
	var res3 struct {
		Reset  bool  `json:"reset"`
		Cursor int64 `json:"cursor"`
	}
	json.Unmarshal(mustJSON(out), &res3)
	if !res3.Reset {
		t.Fatalf("since_seq past the latest event must reset: %+v", res3)
	}

	// since_seq 0 (a fresh cursor) never resets and returns a usable cursor.
	out, err = s.call(ctx, seed.Caller, "swarm_read", `{"since_seq":0}`)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(mustJSON(out), &res3)
	if res3.Reset {
		t.Fatalf("a fresh cursor must not reset: %+v", res3)
	}

	// since_seq > 0 with real events published after the cursor exercises the
	// event-scan loop's item.changed and checkpoint.created cases. agent.changed
	// is never actually published anywhere in production (a pre-existing gap
	// outside this batch's file ownership — see the comment in readTool), so it
	// is published directly here to prove the branch still works if that gap is
	// ever closed.
	baseCursor := res3.Cursor
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+seed.OtherTaskKey+`","title":"Renamed","revision":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, workerCaller, "swarm_checkpoint", `{"kind":"progress","summary":"more"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.Events.Publish(ctx, events.AgentChanged, map[string]string{"name": worker.Name}); err != nil {
		t.Fatal(err)
	}
	out, err = s.call(ctx, seed.Caller, "swarm_read", `{"since_seq":`+strconv.FormatInt(baseCursor, 10)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	var res5 struct {
		Reset  bool  `json:"reset"`
		Cursor int64 `json:"cursor"`
		Items  []struct {
			Key string `json:"key"`
		} `json:"items"`
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
		Checkpoints []struct {
			Item string `json:"item"`
		} `json:"checkpoints"`
	}
	json.Unmarshal(mustJSON(out), &res5)
	if res5.Reset {
		t.Fatalf("a recent cursor must not reset: %+v", res5)
	}
	if len(res5.Items) == 0 {
		t.Fatalf("an item.changed event since the cursor must surface the item: %+v", res5)
	}
	if len(res5.Checkpoints) == 0 {
		t.Fatalf("a checkpoint.created event since the cursor must surface the checkpoint: %+v", res5)
	}
	if len(res5.Agents) == 0 {
		t.Fatalf("an agent.changed event since the cursor must surface the agent: %+v", res5)
	}
	if res5.Cursor <= baseCursor {
		t.Fatalf("cursor must advance: %+v", res5)
	}
}

func TestItemsToolUpdate(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+seed.TaskKey+`","title":"Renamed","status":"ready","revision":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var it struct {
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	json.Unmarshal(mustJSON(out), &it)
	if it.Title != "Renamed" {
		t.Fatalf("item = %+v", it)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_items", `{"op":"bogus"}`); err == nil {
		t.Fatal("an unknown swarm_items op must be refused")
	}
}

// §8.1: op is create|update|link|unlink — link/unlink call items.Store's
// AddDep/RemoveDep (the same primitive the blockOn test helper already uses
// directly) and return the item with its blocked_by updated.
func TestItemsToolLinkAndUnlink(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"link","key":"`+seed.TaskKey+`","blocked_by":"`+seed.OtherTaskKey+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var it struct {
		BlockedBy []string `json:"blocked_by"`
	}
	json.Unmarshal(mustJSON(out), &it)
	if !strings.Contains(strings.Join(it.BlockedBy, ","), seed.OtherTaskKey) {
		t.Fatalf("after link, blocked_by = %+v", it)
	}
	out, err = s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"unlink","key":"`+seed.TaskKey+`","blocked_by":"`+seed.OtherTaskKey+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(mustJSON(out), &it)
	if len(it.BlockedBy) != 0 {
		t.Fatalf("after unlink, blocked_by = %+v", it)
	}
}

func TestItemsToolFullUpdate(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_items", `{"op":"update","key":"`+seed.TaskKey+
		`","brief":"new brief","acceptance":["a","b"],"priority":1,"revision":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), "new brief") {
		t.Fatalf("update = %s", mustJSON(out))
	}
}

func TestControlToolResumeAndBadAction(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	s.RT.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE agent_id = ?`, worker.ID)
	out, err := s.call(ctx, seed.Caller, "swarm_control", `{"target":"`+worker.Name+`","action":"resume"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), `"state"`) {
		t.Fatalf("resume result = %s, want a real state (§8.1)", mustJSON(out))
	}
	// §8.1: "ack" is not a swarm_control action (that's the separate
	// POST /api/agents/{name}/ack UI endpoint).
	if _, err := s.call(ctx, seed.Caller, "swarm_control", `{"target":"`+worker.Name+`","action":"ack"}`); err == nil {
		t.Fatal(`"ack" is not a valid swarm_control action`)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_control", `{"target":"`+worker.Name+`","action":"bogus"}`); err == nil {
		t.Fatal("an unknown swarm_control action must be refused")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_control", `{"target":"no-such-agent","action":"cancel"}`); err == nil {
		t.Fatal("an unknown target must be refused")
	}
}

func TestControlToolPauseErrorsOnAFinishedAgent(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	if _, err := s.call(ctx, seed.Caller, "swarm_control", `{"target":"`+worker.Name+`","action":"cancel"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_control", `{"target":"`+worker.Name+`","action":"pause"}`); err == nil {
		t.Fatal("pausing a cancelled (non-live) agent must fail")
	}
}

func TestControlToolResumeErrorsWhenNotPaused(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	if _, err := s.call(ctx, seed.Caller, "swarm_control", `{"target":"`+worker.Name+`","action":"resume"}`); err == nil {
		t.Fatal("resuming a session that is not paused must fail")
	}
}

func TestWorktreeReleaseAndUnknownOp(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	out, _ := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"create","repo":"`+seed.RepoID+`","branch":"task/rel"}`)
	var wt struct {
		WorktreeID string `json:"worktree_id"`
	}
	json.Unmarshal(mustJSON(out), &wt)
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"share","worktree":"`+wt.WorktreeID+`","agent":"`+worker.Name+`","mode":"ro"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"release","worktree":"`+wt.WorktreeID+`","agent":"`+worker.Name+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"bogus"}`); err == nil {
		t.Fatal("an unknown swarm_worktree op must be refused")
	}
}

func TestWorktreeCreateAndShareRefuseUnknownIDs(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"create","repo":"repo_nope","branch":"b"}`); err == nil {
		t.Fatal("an unknown repo must be refused")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"share","worktree":"wt_nope","agent":"no-such-agent","mode":"ro"}`); err == nil {
		t.Fatal("an unknown agent must be refused")
	}
}

// swarm_materialize is visible only to a spike orchestrator; this just
// exercises the handler's wiring to Store.Materialize (the feature/spec/plan
// validation itself is internal/runtime's, already covered there).
func TestMaterializeToolReachesStoreMaterialize(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	c := seed.Caller
	c.SpikeOrchestrator = true
	if _, err := s.call(ctx, c, "swarm_materialize", `{}`); err == nil {
		t.Fatal("materializing an epic with no spec/plan must fail")
	}
}

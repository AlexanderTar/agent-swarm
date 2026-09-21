package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
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

// Task 41: a repeated request_id on swarm_control retry must not start a
// second tmux session or send a second assignment_update note.
func TestControlRetryRequestIDReplaysWithoutStartingASecondSession(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	s.RT.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, worker.ID)
	tm := s.RT.Tmux.(*fakeTmux)
	startedBefore := len(tm.started)
	body := `{"target":"` + worker.Name + `","action":"retry","note":"reviewer finding","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_control", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.started) != startedBefore+1 {
		t.Fatalf("tmux started %d times after first call, want %d", len(tm.started), startedBefore+1)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_control", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.started) != startedBefore+1 {
		t.Fatalf("tmux started %d times after replayed call, want still %d", len(tm.started), startedBefore+1)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
	var n int
	s.RT.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'`,
		worker.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("assignment_update messages = %d, want 1", n)
	}
}

// A distinct request_id (or none at all) genuinely retries again.
func TestControlRetryDistinctRequestIDGenuinelyRetries(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	s.RT.DB.ExecContext(ctx, `UPDATE sessions SET state = 'completed' WHERE agent_id = ?`, worker.ID)
	tm := s.RT.Tmux.(*fakeTmux)
	if _, err := s.call(ctx, seed.Caller, "swarm_control",
		`{"target":"`+worker.Name+`","action":"retry","request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	started1 := len(tm.started)
	s.RT.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE agent_id = ?`, worker.ID)
	if _, err := s.call(ctx, seed.Caller, "swarm_control",
		`{"target":"`+worker.Name+`","action":"retry","request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	if len(tm.started) != started1+1 {
		t.Fatalf("a distinct request_id must genuinely retry: tmux started %d, want %d", len(tm.started), started1+1)
	}
}

// Task 41: a repeated request_id on swarm_control cancel must not
// interrupt/kill the tmux session a second time.
func TestControlCancelRequestIDReplaysWithoutKillingTwice(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	tm := s.RT.Tmux.(*fakeTmux)
	// startSession now kills any stale same-name pane before every Start
	// (P0-crash-1, 2026-09-19), so spawnWorker's own setup already
	// contributes kill calls unrelated to the cancel under test here —
	// baseline after setup, like TestControlResumeRequestIDReplaysWithoutStartingASecondSession
	// already does for tm.started below.
	killedBefore := len(tm.killed)
	body := `{"target":"` + worker.Name + `","action":"cancel","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_control", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != killedBefore+1 {
		t.Fatalf("tmux killed %d times after first call, want %d", len(tm.killed), killedBefore+1)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_control", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.killed) != killedBefore+1 {
		t.Fatalf("tmux killed %d times after replayed call, want still %d", len(tm.killed), killedBefore+1)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

// Task 41: a repeated request_id on swarm_control resume must not start a
// second tmux session.
func TestControlResumeRequestIDReplaysWithoutStartingASecondSession(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	s.RT.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE agent_id = ?`, worker.ID)
	tm := s.RT.Tmux.(*fakeTmux)
	startedBefore := len(tm.started)
	body := `{"target":"` + worker.Name + `","action":"resume","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_control", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.started) != startedBefore+1 {
		t.Fatalf("tmux started %d times after first call, want %d", len(tm.started), startedBefore+1)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_control", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.started) != startedBefore+1 {
		t.Fatalf("tmux started %d times after replayed call, want still %d", len(tm.started), startedBefore+1)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
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

// Task 41: a repeated request_id must not register a second revision, even
// if the replay's op says "revise" -- proving the guard runs before the
// register-vs-revise inference, not just that identical input is a no-op.
func TestArtifactRequestIDReplaysInsteadOfRevisingTwice(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	p := writeSpec(t, "# Spec\n\n## One\n\na\n")
	out1, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"register","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`","request_id":"req-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"revise","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`","request_id":"req-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
	var res struct {
		Revision int `json:"revision"`
	}
	json.Unmarshal(mustJSON(out2), &res)
	if res.Revision != 1 {
		t.Fatalf("replay must not bump the revision: %+v", res)
	}
	out3, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"revise","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`","request_id":"req-2"}`)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(mustJSON(out3), &res)
	if res.Revision != 2 {
		t.Fatalf("a genuinely distinct request_id must still revise: %+v", res)
	}
}

// §8.1: op is "register"|"revise" - the schema enum must allow both, and a
// second call against the same item+path (with op:"revise") bumps the
// revision (fix round 2, item 3).
func TestArtifactToolSchemaAllowsRevise(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	var schema struct {
		Properties struct {
			Op struct {
				Enum []string `json:"enum"`
			} `json:"op"`
		} `json:"properties"`
	}
	for _, d := range s.ToolsFor(seed.Caller) {
		if d.Name == "swarm_artifact" {
			if err := json.Unmarshal(d.Schema, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Contains(schema.Properties.Op.Enum, "revise") {
		t.Fatalf("swarm_artifact's op enum must allow \"revise\": %v", schema.Properties.Op.Enum)
	}

	p := writeSpec(t, "# Spec\n\n## One\n\na\n")
	if _, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"register","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`"}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.call(ctx, seed.Caller, "swarm_artifact",
		`{"op":"revise","item":"`+seed.RootKey+`","kind":"spec","path":"`+p+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Revision int `json:"revision"`
	}
	json.Unmarshal(mustJSON(out), &res)
	if res.Revision != 2 {
		t.Fatalf("a revise of the same item+path must bump the revision: %+v", res)
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
	// Fix R-2 (final review): "item" must be the item's KEY (seed.TaskKey,
	// what the worker was spawned on), not checkpoints.item_id -- the opaque
	// database id checkpointOut used to leak here, which every other reader
	// of an "item" field (including swarm_read's own refs/filter inputs)
	// would fail to resolve back to anything.
	if res4.Checkpoints[0].Item != seed.TaskKey {
		t.Fatalf("checkpoint item = %q, want the item key %q, not a raw id", res4.Checkpoints[0].Item, seed.TaskKey)
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
	// Fix R-2, the since_seq/checkpoint.created call site: same pin as the
	// refs path above, since this one resolves "item" from the event's own
	// p.Item rather than it.Key -- a different variable, so it needs its own
	// regression guard.
	if res5.Checkpoints[0].Item != seed.TaskKey {
		t.Fatalf("checkpoint item = %q, want the item key %q, not a raw id", res5.Checkpoints[0].Item, seed.TaskKey)
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

func countItems(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.RT.DB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Task 41: a repeated request_id must not create a second item.
func TestItemsCreateRequestIDReplaysInsteadOfCreatingTwice(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	before := countItems(t, s)
	body := `{"op":"create","type":"task","parent":"` + seed.StoryKey +
		`","title":"New task","brief":"b","acceptance":["a"],"request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_items", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countItems(t, s); n != before+1 {
		t.Fatalf("items after first call = %d, want %d", n, before+1)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_items", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countItems(t, s); n != before+1 {
		t.Fatalf("items after replayed call = %d, want still %d", n, before+1)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

func TestItemsCreateWithoutOrDistinctRequestIDsEachCreate(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	before := countItems(t, s)
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"A","brief":"b","acceptance":["a"],"request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"B","brief":"b","acceptance":["a"],"request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"C","brief":"b","acceptance":["a"]}`); err != nil {
		t.Fatal(err)
	}
	if n := countItems(t, s); n != before+3 {
		t.Fatalf("items = %d, want %d", n, before+3)
	}
}

// TestItemsUpdateRequestIDReplays proves the guard, not just natural
// idempotence: Update bumps the item's revision, so a genuine (unguarded)
// second call with the same stale revision:1 would fail with StaleRevision.
// A replay must instead return the first call's result untouched.
func TestItemsUpdateRequestIDReplays(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	body := `{"op":"update","key":"` + seed.TaskKey + `","title":"Renamed","revision":1,"request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_items", body)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_items", body)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

// A distinct request_id genuinely re-applies the mutation (and a stale
// revision is refused, exactly as before this task's change).
func TestItemsUpdateDistinctRequestIDGenuinelyMutates(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+seed.TaskKey+`","title":"First","revision":1,"request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+seed.TaskKey+`","title":"Second","revision":1,"request_id":"req-b"}`); err == nil {
		t.Fatal("a genuinely distinct request_id must still hit StaleRevision against an already-bumped revision")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"update","key":"`+seed.TaskKey+`","title":"Second","revision":2,"request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	out, err := s.call(ctx, seed.Caller, "swarm_items", `{"op":"update","key":"`+seed.TaskKey+`","revision":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), "Second") {
		t.Fatalf("out = %s", mustJSON(out))
	}
}

// Task 41: a repeated request_id on link/unlink must not error the second
// time (AddDepTx/RemoveDepTx are themselves already no-op-safe on a repeat,
// but the guard must still short-circuit cleanly).
func TestItemsLinkUnlinkRequestIDReplays(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	linkBody := `{"op":"link","key":"` + seed.TaskKey + `","blocked_by":"` + seed.OtherTaskKey + `","request_id":"req-link"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_items", linkBody)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_items", linkBody)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("link replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
	unlinkBody := `{"op":"unlink","key":"` + seed.TaskKey + `","blocked_by":"` + seed.OtherTaskKey + `","request_id":"req-unlink"}`
	out3, err := s.call(ctx, seed.Caller, "swarm_items", unlinkBody)
	if err != nil {
		t.Fatal(err)
	}
	out4, err := s.call(ctx, seed.Caller, "swarm_items", unlinkBody)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out3)) != string(mustJSON(out4)) {
		t.Fatalf("unlink replay result = %s, want %s", mustJSON(out4), mustJSON(out3))
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

// §8.1 documents one result shape for the whole tool -
// {worktree_id,path,branch,base_sha,state} - not a per-op shape, so share,
// release and remove must return it too wherever the data exists (fix round
// 2, item 5).
func TestWorktreeReleaseAndUnknownOp(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	out, _ := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"create","repo":"`+seed.RepoID+`","branch":"task/rel"}`)
	var wt struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
		Branch     string `json:"branch"`
	}
	json.Unmarshal(mustJSON(out), &wt)

	out, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"share","worktree":"`+wt.WorktreeID+`","agent":"`+worker.Name+`","mode":"ro"}`)
	if err != nil {
		t.Fatal(err)
	}
	var shared struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
		Branch     string `json:"branch"`
		BaseSHA    string `json:"base_sha"`
		State      string `json:"state"`
	}
	json.Unmarshal(mustJSON(out), &shared)
	if shared.WorktreeID != wt.WorktreeID || shared.Path != wt.Path || shared.Branch != wt.Branch || shared.State == "" {
		t.Fatalf("share result = %+v", shared)
	}

	out, err = s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"release","worktree":"`+wt.WorktreeID+`","agent":"`+worker.Name+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var released struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
		State      string `json:"state"`
	}
	json.Unmarshal(mustJSON(out), &released)
	if released.WorktreeID != wt.WorktreeID || released.Path != wt.Path || released.State == "" {
		t.Fatalf("release result = %+v", released)
	}

	if _, err := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"bogus"}`); err == nil {
		t.Fatal("an unknown swarm_worktree op must be refused")
	}
}

// remove's result keeps worktree_id/path/branch/base_sha - only state (and
// possibly retained_reason via state itself) changes - because Service.Remove
// hands back the pre-delete Worktree with just its state field updated
// (fix round 2, item 5).
func TestWorktreeRemoveReturnsTheFullShape(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"create","repo":"`+seed.RepoID+`","branch":"task/rm"}`)
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
		Branch     string `json:"branch"`
	}
	json.Unmarshal(mustJSON(out), &wt)

	out, err = s.call(ctx, seed.Caller, "swarm_worktree", `{"op":"remove","worktree":"`+wt.WorktreeID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var removed struct {
		WorktreeID string `json:"worktree_id"`
		Path       string `json:"path"`
		Branch     string `json:"branch"`
		State      string `json:"state"`
	}
	json.Unmarshal(mustJSON(out), &removed)
	if removed.WorktreeID != wt.WorktreeID || removed.Path != wt.Path || removed.Branch != wt.Branch || removed.State == "" {
		t.Fatalf("remove result = %+v", removed)
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

func countWorktrees(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.RT.DB.QueryRow(`SELECT COUNT(*) FROM worktrees`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Fix round 1, Important #2: a repeated request_id on swarm_worktree create
// must not create a second worktree directory on disk. PathFor suffixes a
// colliding path with -2, -3, ... (internal/worktree/worktree.go:89), so a
// naive replay (no guard at all) wouldn't error -- it would silently create
// a second worktree at a different path. Proven here by counting DB rows.
func TestWorktreeCreateRequestIDReplaysInsteadOfCreatingTwice(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	before := countWorktrees(t, s)
	body := `{"op":"create","repo":"` + seed.RepoID + `","branch":"task/replay","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countWorktrees(t, s); n != before+1 {
		t.Fatalf("worktrees after first call = %d, want %d", n, before+1)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countWorktrees(t, s); n != before+1 {
		t.Fatalf("worktrees after replayed call = %d, want still %d", n, before+1)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

func TestWorktreeCreateWithoutOrDistinctRequestIDsEachCreate(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	before := countWorktrees(t, s)
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/a","request_id":"req-a"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/b","request_id":"req-b"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/c"}`); err != nil {
		t.Fatal(err)
	}
	if n := countWorktrees(t, s); n != before+3 {
		t.Fatalf("worktrees = %d, want %d", n, before+3)
	}
}

// review goes through the same two-phase guard as create.
func TestWorktreeReviewRequestIDReplaysInsteadOfCreatingTwice(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	sha := headSHA(t, seed.RepoPath)
	before := countWorktrees(t, s)
	body := `{"op":"review","repo":"` + seed.RepoID + `","sha":"` + sha + `","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countWorktrees(t, s); n != before+1 {
		t.Fatalf("worktrees after first call = %d, want %d", n, before+1)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	if n := countWorktrees(t, s); n != before+1 {
		t.Fatalf("worktrees after replayed call = %d, want still %d", n, before+1)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

// A repeated request_id on share must not send the assignment_update message
// twice.
func TestWorktreeShareRequestIDReplaysWithoutASecondMessage(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	out, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/share-replay"}`)
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		WorktreeID string `json:"worktree_id"`
	}
	json.Unmarshal(mustJSON(out), &wt)

	body := `{"op":"share","worktree":"` + wt.WorktreeID + `","agent":"` + worker.Name + `","mode":"ro","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
	var n int
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
		WHERE to_agent_id = ? AND kind = 'assignment_update'`, worker.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("assignment_update messages = %d, want 1", n)
	}
}

// A repeated request_id on remove must not attempt the git removal (and its
// DirtyStrict/mergedOrPushed checks against a now-missing path) a second
// time.
func TestWorktreeRemoveRequestIDReplays(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	out, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/remove-replay"}`)
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		WorktreeID string `json:"worktree_id"`
	}
	json.Unmarshal(mustJSON(out), &wt)

	body := `{"op":"remove","worktree":"` + wt.WorktreeID + `","request_id":"req-1"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_worktree", body)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

// release's own SQL is already a genuine no-op the second time (verified,
// not assumed): the UPDATE only matches rows where released_at IS NULL, so a
// second call matches zero rows and leaves released_at exactly as the first
// call set it. No PeekIdempotent/IdemTx wiring needed or added.
func TestWorktreeReleaseCalledTwiceIsAGenuineNoOp(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	out, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"create","repo":"`+seed.RepoID+`","branch":"task/release-replay"}`)
	if err != nil {
		t.Fatal(err)
	}
	var wt struct {
		WorktreeID string `json:"worktree_id"`
	}
	json.Unmarshal(mustJSON(out), &wt)
	if _, err := s.call(ctx, seed.Caller, "swarm_worktree",
		`{"op":"share","worktree":"`+wt.WorktreeID+`","agent":"`+worker.Name+`","mode":"ro"}`); err != nil {
		t.Fatal(err)
	}

	releaseBody := `{"op":"release","worktree":"` + wt.WorktreeID + `","agent":"` + worker.Name + `"}`
	out1, err := s.call(ctx, seed.Caller, "swarm_worktree", releaseBody)
	if err != nil {
		t.Fatal(err)
	}
	var releasedAt1 sql.NullInt64
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT released_at FROM worktree_reservations
		WHERE worktree_id = ? AND agent_id = ?`, wt.WorktreeID, worker.ID).Scan(&releasedAt1); err != nil {
		t.Fatal(err)
	}
	if !releasedAt1.Valid {
		t.Fatal("released_at must be set after the first release")
	}
	out2, err := s.call(ctx, seed.Caller, "swarm_worktree", releaseBody)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("second release result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
	var releasedAt2 sql.NullInt64
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT released_at FROM worktree_reservations
		WHERE worktree_id = ? AND agent_id = ?`, wt.WorktreeID, worker.ID).Scan(&releasedAt2); err != nil {
		t.Fatal(err)
	}
	if releasedAt2.Int64 != releasedAt1.Int64 {
		t.Fatalf("released_at changed on the second call: %v -> %v", releasedAt1, releasedAt2)
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
		t.Fatal("spike is required and must be refused when absent")
	}
}

// materializePlanBody is a minimal swarm-tree'd plan, copied (not imported -
// internal/runtime is a different package and this is a one-caller fixture,
// same "copy don't share" call as helpers_test.go's fakeTmux etc.) from
// internal/runtime/helpers_test.go's planBody.
const materializePlanBody = "# Plan\n\n## Work breakdown\n\n" +
	"```swarm-tree\n" +
	`{"root":{"type":"epic","title":"Ship auth","brief":"","acceptance":["It works."]},
 "children":[{"ref":"s1","type":"story","title":"Server","brief":"","acceptance":[],
   "children":[{"ref":"t1","type":"task","title":"Session cookie","brief":"","acceptance":[],"role_hint":"coder","tdd_exempt":null,"repos":["chat"]}]}],
 "deps":[]}` + "\n```\n\n## Verification\n\ngo test ./...\n"

// TestMaterializeToolResultUsesSnakeCaseKeys is a bonus finding from the fix
// round 2 full pass (not one of the 5 assigned items): runtime.MaterializeResult
// has no json tags (the same class of gap as Advisor/Agent/Checkpoint/Artifact),
// so returning it bare would marshal as {"Root":...,"Created":[...]} instead of
// spec's {"root","created"}. Drives a real materialize to completion to prove
// the wire keys, not just the Go struct shape.
func TestMaterializeToolResultUsesSnakeCaseKeys(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, agent, _, err := s.RT.StartSpike(ctx, runtime.SpikeInput{Name: "Ship auth", Intent: "feature",
		Kind: runtime.Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.RT.Ask(ctx, ses.ID, runtime.AskInput{Kind: "confirm_repos", Prompt: "chat only",
		Repos: []runtime.ReposProposal{{Repo: repo, Reason: "it's the only one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.ConfirmRepos(ctx, req.ID, []string{repo}, "", 0, "board"); err != nil {
		t.Fatal(err)
	}
	spec, err := s.RT.RegisterArtifact(ctx, ses.ID, "register", key, "spec",
		writeSpec(t, "# Spec\n\n## Context\n\nauth is missing\n\n## Decisions\n\ncookies\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RT.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeSpec(t, materializePlanBody), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range spec.Sections {
		r, err := s.RT.Ask(ctx, ses.ID, runtime.AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
			SectionID: sec.ID, Prompt: "Approve " + sec.Title + "."})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RT.Approve(ctx, r.ID, runtime.ApproveInput{SectionSHA256: sec.SHA256,
			ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.RT.Ask(ctx, ses.ID, runtime.AskInput{Kind: "approval", ArtifactID: plan.ArtifactID,
		Prompt: "Approve the plan."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.Approve(ctx, r.ID, runtime.ApproveInput{ArtifactRevision: plan.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}

	c := Caller{SessionID: ses.ID, AgentID: agent.ID, AgentName: agent.Name,
		Role: runtime.RoleOrchestrator, SpikeOrchestrator: true}
	out, err := s.call(ctx, c, "swarm_materialize",
		`{"spike":"`+key+`","spec":"`+spec.ArtifactID+`","plan":"`+plan.ArtifactID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(mustJSON(out), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["root"]; !ok {
		t.Fatalf("result must have a lowercase \"root\" key, got %v", raw)
	}
	if _, ok := raw["created"]; !ok {
		t.Fatalf("result must have a lowercase \"created\" key, got %v", raw)
	}
}

// Task 41: a repeated request_id must not materialize a second tree -- proven
// here by the fact that a genuine second call would fail outright (the spike
// is already "done" after the first), not merely produce a duplicate.
func TestMaterializeRequestIDReplaysInsteadOfMaterializingTwice(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, agent, _, err := s.RT.StartSpike(ctx, runtime.SpikeInput{Name: "Ship auth", Intent: "feature",
		Kind: runtime.Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.RT.Ask(ctx, ses.ID, runtime.AskInput{Kind: "confirm_repos", Prompt: "chat only",
		Repos: []runtime.ReposProposal{{Repo: repo, Reason: "it's the only one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.ConfirmRepos(ctx, req.ID, []string{repo}, "", 0, "board"); err != nil {
		t.Fatal(err)
	}
	spec, err := s.RT.RegisterArtifact(ctx, ses.ID, "register", key, "spec",
		writeSpec(t, "# Spec\n\n## Context\n\nauth is missing\n\n## Decisions\n\ncookies\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RT.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeSpec(t, materializePlanBody), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range spec.Sections {
		r, err := s.RT.Ask(ctx, ses.ID, runtime.AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
			SectionID: sec.ID, Prompt: "Approve " + sec.Title + "."})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RT.Approve(ctx, r.ID, runtime.ApproveInput{SectionSHA256: sec.SHA256,
			ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.RT.Ask(ctx, ses.ID, runtime.AskInput{Kind: "approval", ArtifactID: plan.ArtifactID,
		Prompt: "Approve the plan."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.Approve(ctx, r.ID, runtime.ApproveInput{ArtifactRevision: plan.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}

	c := Caller{SessionID: ses.ID, AgentID: agent.ID, AgentName: agent.Name,
		Role: runtime.RoleOrchestrator, SpikeOrchestrator: true}
	body := `{"spike":"` + key + `","spec":"` + spec.ArtifactID + `","plan":"` + plan.ArtifactID + `","request_id":"req-1"}`
	out1, err := s.call(ctx, c, "swarm_materialize", body)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.call(ctx, c, "swarm_materialize", body)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(out1)) != string(mustJSON(out2)) {
		t.Fatalf("replay result = %s, want %s", mustJSON(out2), mustJSON(out1))
	}
}

// §8.1: spike is a required input field, not inferred from the caller's own
// item - passing it explicitly must actually reach Store.Materialize (fix
// round 2, item 4).
func TestMaterializeToolUsesTheExplicitSpikeField(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	c := seed.Caller
	c.SpikeOrchestrator = true
	// the caller's own item, passed explicitly: reaches Store.Materialize,
	// which then fails for the documented reason (no spec/plan), not for a
	// missing spike.
	_, err := s.call(ctx, c, "swarm_materialize", `{"spike":"`+seed.RootKey+`"}`)
	if err == nil || strings.Contains(err.Error(), "spike is required") {
		t.Fatalf("err = %v; want Store.Materialize's own spec/plan error", err)
	}
	// a spike key that isn't the caller's own item is refused by
	// Store.Materialize's ownership check, proving the field isn't silently
	// re-inferred from the caller's context instead of being used.
	other := seedOtherRoot(t, s)
	_, err = s.call(ctx, c, "swarm_materialize", `{"spike":"`+other+`"}`)
	if err == nil || !strings.Contains(err.Error(), "Only the spike's orchestrator can materialize it") {
		t.Fatalf("err = %v; want the ownership refusal", err)
	}
}

func spawnArgs(item string) string {
	return `{"item":"` + item + `","role":"coder","brief":{"objective":"x"},"worktrees":[]}`
}

func statusOf(t *testing.T, s *Server, key string) items.Status {
	t.Helper()
	it, err := s.RT.Items.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return it.Status
}

func TestSpawnPromotesDraftTaskAndParentStory(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	if statusOf(t, s, seed.TaskKey) != items.Draft || statusOf(t, s, seed.StoryKey) != items.Draft {
		t.Fatal("fixture must start Draft")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn", spawnArgs(seed.TaskKey)); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, s, seed.TaskKey); got != items.Ready {
		t.Fatalf("task = %s, want ready", got)
	}
	if got := statusOf(t, s, seed.StoryKey); got != items.Ready {
		t.Fatalf("story = %s, want ready", got)
	}
	if got := statusOf(t, s, seed.OtherTaskKey); got != items.Draft {
		t.Fatalf("sibling task = %s, must stay draft", got)
	}
}

func TestSpawnOnReadyTaskChangesNothing(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn", spawnArgs(seed.TaskKey)); err != nil {
		t.Fatal(err)
	}
	before, _ := s.RT.Items.Get(ctx, seed.TaskKey)
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn", spawnArgs(seed.TaskKey)); err != nil {
		t.Fatal(err)
	}
	after, _ := s.RT.Items.Get(ctx, seed.TaskKey)
	if after.Revision != before.Revision {
		t.Fatalf("revision %d -> %d: a second spawn must not re-promote", before.Revision, after.Revision)
	}
}

func TestSpawnOnReadyTaskPromotesDraftParentStory(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	// A Ready task under a Draft story: the story must still be promoted.
	created, err := s.call(ctx, seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"Ready task","status":"ready"}`)
	if err != nil {
		t.Fatal(err)
	}
	var it struct {
		Key string `json:"key"`
	}
	json.Unmarshal(mustJSON(created), &it)
	if it.Key == "" || statusOf(t, s, it.Key) != items.Ready {
		t.Fatalf("fixture: task %q must start Ready", it.Key)
	}
	if got := statusOf(t, s, seed.StoryKey); got != items.Draft {
		t.Fatalf("fixture: story = %s, must start draft", got)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn", spawnArgs(it.Key)); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, s, seed.StoryKey); got != items.Ready {
		t.Fatalf("story = %s, want ready", got)
	}
}

func TestItemsCreateStatusReady(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	out, err := s.call(context.Background(), seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"Ready task","status":"ready"}`)
	if err != nil {
		t.Fatal(err)
	}
	var it struct {
		Status string `json:"status"`
	}
	json.Unmarshal(mustJSON(out), &it)
	if it.Status != "ready" {
		t.Fatalf("status = %q, want ready", it.Status)
	}
}

func TestItemsCreateRejectsInProgressStatus(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	if _, err := s.call(context.Background(), seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"x","status":"in_progress"}`); err == nil {
		t.Fatal("a new item can only start draft or ready")
	}
}

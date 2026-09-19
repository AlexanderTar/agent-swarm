//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// e2eRepo is a real, local-only git repo (no remote needed: worktree.Create
// tolerates a failed `git fetch origin` and just logs it) registered with the
// daemon, for scenarios that need real git operations (worktrees, commits).
func e2eRepo(t *testing.T, h *harness, name string) (id, path string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Swarm E2E")
	run("config", "user.email", "e2e@example.invalid")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init", "--no-gpg-sign")

	var repo map[string]any
	h.doT(t, http.MethodPost, "/api/repos", map[string]any{"path": dir}, &repo)
	return repo["id"].(string), dir
}

// requestByKind is requestByID's counterpart keyed by kind, for a scenario
// that needs the full request (section_sha256/artifact_revision) to approve
// with, not just its id.
func (h *harness) requestByKind(t *testing.T, itemKey, kind string) map[string]any {
	t.Helper()
	var list []map[string]any
	h.doT(t, http.MethodGet, "/api/requests", nil, &list)
	for _, r := range list {
		if r["item_key"] == itemKey && r["kind"] == kind && r["state"] == "open" {
			return r
		}
	}
	t.Fatalf("no open %s request on %s", kind, itemKey)
	return nil
}

func (h *harness) waitForRequestFull(t *testing.T, itemKey, kind string, timeout time.Duration) map[string]any {
	t.Helper()
	h.waitForRequest(t, itemKey, kind, timeout)
	return h.requestByKind(t, itemKey, kind)
}

// Scenario 1: happy feature spike. POST /api/spikes with one suggested repo;
// the orchestrator accepts, proposes that repo plus one suggested addition
// via confirm_repos; the user confirms both; the orchestrator creates a
// worktree in the first, asks a question, registers a 3-section spec and a
// plan; the user approves all 3 sections and the plan; materialize creates
// an epic (draft) with 2 stories, 3 tasks and 1 dependency; the spike ends
// Done.
//
// Driven through the harness acting as both the orchestrator (its own
// session, over MCP) and the user (the daemon token, over HTTP) rather than
// through a live scripted swarm-fake-agent process: every value the script
// would need (repo ids, the spike's own item key, file paths) would have to
// reach that process through adapter.Spec/Launch.Env, which today carries
// only what Task 37's scenario-selection fix added (SWARM_FAKE_SCENARIO).
// Threading five more test-specific values through production spawn plumbing
// for e2e's sake isn't worth it when the harness already has direct access
// to all of them — this is the same choice already made for scenarios 3, 19
// and 27. cmd/swarm-fake-agent's scenario runner itself is still built and
// unit-tested (scenario_test.go) exactly as Task 37 asks.
func TestScenario01HappyFeatureSpike(t *testing.T) {
	h := newHarness(t)
	since := time.Now()
	repoAID, repoADir := e2eRepo(t, h, "proj-a")
	repoBID, _ := e2eRepo(t, h, "proj-b")

	var spikeResp map[string]any
	h.doT(t, http.MethodPost, "/api/spikes", map[string]any{
		"request_id": "req-" + unique(), "name": "Happy feature spike " + unique(), "intent": "feature",
		"agent": "fake", "model": "fake-1", "repos": []string{repoAID},
	}, &spikeResp)
	item, _ := spikeResp["item"].(map[string]any)
	agent, _ := spikeResp["agent"].(map[string]any)
	spikeKey, _ := item["key"].(string)
	orch, _ := agent["name"].(string)

	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting the spike"})

	h.mustTool(t, orch, "swarm_ask", map[string]any{
		"kind": "confirm_repos", "prompt": "The change needs the client and the shared schema.",
		"repos":     []map[string]any{{"repo": repoAID, "reason": "the login form lives here"}},
		"expansion": []map[string]any{{"repo": repoBID, "reason": "the session schema is shared"}},
	})
	confirmReq := h.waitForRequestFull(t, spikeKey, "confirm_repos", 5*time.Second)
	var binding struct {
		ReposVersion int `json:"repos_version"`
	}
	if b, ok := confirmReq["binding"].(map[string]any); ok {
		if v, ok := b["repos_version"].(float64); ok {
			binding.ReposVersion = int(v)
		}
	}
	h.doT(t, http.MethodPost, "/api/requests/"+confirmReq["id"].(string)+"/confirm-repos", map[string]any{
		"repos": []string{repoAID, repoBID}, "repos_version": binding.ReposVersion, "via": "board",
	}, nil)

	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoAID, "branch": "spike/login"})
	// §12.1 centralization: the worktree lives under the daemon's shared
	// ~/.swarm/worktrees folder, never as a sibling of the fixture repo.
	wantDir := filepath.Join(h.home, "worktrees") + string(filepath.Separator)
	if wtPath, _ := wtOut["path"].(string); !strings.HasPrefix(wtPath, wantDir) {
		t.Fatalf("worktree path = %q, want a child of %q", wtPath, wantDir)
	}

	h.mustTool(t, orch, "swarm_ask", map[string]any{"kind": "question", "prompt": "Keep the old email flow too?"})
	questionReq := h.waitForRequestFull(t, spikeKey, "question", 5*time.Second)
	h.doT(t, http.MethodPost, "/api/requests/"+questionReq["id"].(string)+"/answer",
		map[string]any{"text": "No, replace it.", "via": "board"}, nil)

	specPath := filepath.Join(t.TempDir(), "spec.md")
	specBody := "# Spec\n\n## Context\n\nwhy\n\n## Data model\n\nrows\n\n## API\n\nroutes\n"
	if err := os.WriteFile(specPath, []byte(specBody), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "register", "item": spikeKey, "kind": "spec", "path": specPath})
	specID, _ := spec["artifact_id"].(string)
	sections, _ := spec["sections"].([]any)
	if len(sections) != 3 {
		t.Fatalf("spec sections = %d, want 3", len(sections))
	}
	for _, s := range sections {
		sec, _ := s.(map[string]any)
		h.mustTool(t, orch, "swarm_ask", map[string]any{
			"kind": "approval", "prompt": "Review \"" + sec["title"].(string) + "\"",
			"artifact": specID, "section": sec["id"],
		})
	}
	for i := 0; i < 3; i++ {
		req := h.requestByKind(t, spikeKey, "approve_section")
		h.doT(t, http.MethodPost, "/api/requests/"+req["id"].(string)+"/approve", map[string]any{
			"section_sha256": req["section_sha256"], "artifact_revision": req["artifact_revision"], "via": "board",
		}, nil)
	}

	planBody := "# Plan\n\n## Work breakdown\n\n```swarm-tree\n" +
		`{"root":{"type":"epic","title":"Ship the login form","brief":"","acceptance":["It works."]},
 "children":[{"ref":"s1","type":"story","title":"Server","brief":"","acceptance":[],
   "children":[{"ref":"t1","type":"task","title":"Session cookie","brief":"","acceptance":[],"role_hint":"coder","repos":["` + filepath.Base(repoADir) + `"]},
               {"ref":"t2","type":"task","title":"Login route","brief":"","acceptance":[],"role_hint":"coder","repos":["` + filepath.Base(repoADir) + `"]}]},
  {"ref":"s2","type":"story","title":"Client","brief":"","acceptance":[],
   "children":[{"ref":"t3","type":"task","title":"Login screen","brief":"","acceptance":[],"role_hint":"coder","repos":["` + filepath.Base(repoADir) + `"]}]}],
 "deps":[{"item":"t2","blocked_by":"t1"}]}` +
		"\n```\n\n## Verification\n\ngo test ./...\n"
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte(planBody), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "register", "item": spikeKey, "kind": "plan", "path": planPath})
	planID, _ := plan["artifact_id"].(string)
	h.mustTool(t, orch, "swarm_ask", map[string]any{"kind": "approval", "prompt": "Review the plan", "artifact": planID})
	planReq := h.requestByKind(t, spikeKey, "approve_plan")
	h.doT(t, http.MethodPost, "/api/requests/"+planReq["id"].(string)+"/approve", map[string]any{
		"artifact_revision": planReq["artifact_revision"], "via": "board",
	}, nil)

	mat := h.mustTool(t, orch, "swarm_materialize", map[string]any{"spike": spikeKey, "spec": specID, "plan": planID})
	rootKey, _ := mat["root"].(string)
	if rootKey == "" {
		t.Fatalf("materialize returned no root: %+v", mat)
	}
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "completed", "summary": "materialized the epic"})

	if got := h.itemStatus(t, rootKey); got != "draft" {
		t.Errorf("epic status = %s, want draft", got)
	}
	if got := h.itemStatus(t, spikeKey); got != "done" {
		t.Errorf("spike status = %s, want done", got)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	h.doT(t, http.MethodGet, "/api/items?view=flat&root="+rootKey, nil, &list)
	var stories, tasks int
	for _, it := range list.Items {
		switch it["type"] {
		case "story":
			stories++
		case "task":
			tasks++
		}
	}
	if stories != 2 || tasks != 3 {
		t.Errorf("stories = %d, tasks = %d, want 2 and 3", stories, tasks)
	}
	var task2 map[string]any
	for _, it := range list.Items {
		if it["title"] == "Login route" {
			task2 = it
		}
	}
	if task2 == nil {
		t.Fatal("task \"Login route\" not found")
	}
	if blocked, _ := task2["blocked_by"].([]any); len(blocked) != 1 {
		t.Errorf("Login route's blocked_by = %v, want one dependency", task2["blocked_by"])
	}

	for _, kind := range []string{"agent.accepted", "request.question", "request.approve_plan"} {
		if !h.waitForNotification(t, kind, since, time.Second) {
			t.Errorf("no %s notification since the scenario started", kind)
		}
	}
	var n int
	h.db(t).QueryRow(`SELECT COUNT(*) FROM notifications WHERE kind = 'request.approve_section' AND created_at >= ?`,
		since.UnixMilli()).Scan(&n)
	if n < 3 {
		t.Errorf("request.approve_section notifications since the scenario started = %d, want at least 3", n)
	}
}

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

// e2eRepo creates a local-only Git repository and registers it through HTTP.
// Worktree.Create tolerates the missing origin remote.
func e2eRepo(t *testing.T, h *harness, name string) (id, path string) {
	path = e2eLocalRepo(t, name)
	var repo map[string]any
	h.doT(t, http.MethodPost, "/api/repos", map[string]any{"path": path}, &repo)
	return repo["id"].(string), path
}

func e2eLocalRepo(t *testing.T, name string) string {
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

	return dir
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

// Scenario 1: happy feature spike. POST /api/spikes with one repository hint;
// the orchestrator registers a newly discovered Git repository through MCP,
// creates its worktree, asks a question, and registers a four-section spec and
// plan. The user approves the three decision sections and the plan; materialize creates
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
	repoAID, _ := e2eRepo(t, h, "proj-a")
	repoBDir := e2eLocalRepo(t, "proj-b")

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

	registered := h.mustTool(t, orch, "swarm_repo_register", map[string]any{"path": repoBDir})
	repoBID, _ := registered["id"].(string)
	canonicalRepoBDir, err := filepath.EvalSymlinks(repoBDir)
	if err != nil {
		t.Fatal(err)
	}
	if repoBID == "" || registered["path"] != canonicalRepoBDir {
		t.Fatalf("repository registration = %v", registered)
	}

	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoBID, "branch": "spike/login"})
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
	specBody := "# Spec\n\n## Context\n\nwhy\n\n## Out of scope\n\nDo not migrate old accounts.\n\n## Data model\n\nrows\n\n## API\n\nroutes\n"
	if err := os.WriteFile(specPath, []byte(specBody), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "register", "item": spikeKey, "kind": "spec", "path": specPath})
	specID, _ := spec["artifact_id"].(string)
	sections, _ := spec["sections"].([]any)
	if len(sections) != 4 {
		t.Fatalf("spec sections = %d, want 4", len(sections))
	}
	var reviewCount int
	for _, s := range sections {
		sec, _ := s.(map[string]any)
		if sec["title"] == "Context" {
			continue
		}
		summary := "Deliver " + sec["title"].(string) + " as specified."
		if sec["title"] == "Out of scope" {
			summary = "| Area | Included | Excluded |\n|---|---|---|\n" +
				"| Sign-up | New accounts use the new flow | No migration of existing accounts |\n" +
				"| Sessions | New sessions use the new cookie | Existing sessions keep their current cookie |\n" +
				"| Verification | Test new sign-up and login | No backfill or legacy data conversion |"
		}
		h.mustTool(t, orch, "swarm_ask", map[string]any{
			"kind": "approval", "prompt": summary,
			"artifact": specID, "section": sec["id"],
		})
		if sec["title"] == "Out of scope" {
			req := h.requestByKind(t, spikeKey, "approve_section")
			if req["prompt"] != summary {
				t.Fatalf("section summary changed: %v", req["prompt"])
			}
		}
		reviewCount++
	}
	for i := 0; i < reviewCount; i++ {
		req := h.requestByKind(t, spikeKey, "approve_section")
		h.doT(t, http.MethodPost, "/api/requests/"+req["id"].(string)+"/approve", map[string]any{
			"section_sha256": req["section_sha256"], "artifact_revision": req["artifact_revision"], "via": "board",
		}, nil)
	}

	planBody := "# Plan\n\n## Work breakdown\n\n```swarm-tree\n" +
		`{"root":{"type":"epic","title":"Ship the login form","brief":"","acceptance":["It works."]},
 "children":[{"ref":"s1","type":"story","title":"Server","brief":"","acceptance":[],
   "children":[{"ref":"t1","type":"task","title":"Session cookie","brief":"","acceptance":[],"role_hint":"coder","repos":["` + filepath.Base(repoBDir) + `"],"workflow":{"template":"tdd-reviewed"},"steps":["Write test","Implement"],"verify":["go test ./..."],"solo":"focused"},
               {"ref":"t2","type":"task","title":"Login route","brief":"","acceptance":[],"role_hint":"coder","repos":["` + filepath.Base(repoBDir) + `"],"workflow":{"template":"tdd-reviewed"},"steps":["Write test","Implement"],"verify":["go test ./..."],"solo":"focused"}]},
  {"ref":"s2","type":"story","title":"Client","brief":"","acceptance":[],
   "children":[{"ref":"t3","type":"task","title":"Login screen","brief":"","acceptance":[],"role_hint":"coder","repos":["` + filepath.Base(repoBDir) + `"],"workflow":{"template":"tdd-reviewed"},"steps":["Write test","Implement"],"verify":["go test ./..."],"solo":"focused"}]}],
 "deps":[{"item":"t2","blocked_by":"t1"}]}` +
		"\n```\n\n## Verification\n\ngo test ./...\n"
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte(planBody), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "register", "item": spikeKey, "kind": "plan", "path": planPath})
	planID, _ := plan["artifact_id"].(string)
	planAsk := h.mustTool(t, orch, "swarm_ask", map[string]any{"kind": "approval", "prompt": "Build server and client, then run go test ./...", "artifact": planID})
	paths, _ := planAsk["review_paths"].(map[string]any)
	if paths["spec"] != specPath || paths["plan"] != planPath {
		t.Fatalf("plan review paths = %v", paths)
	}
	if next, _ := planAsk["next"].(string); !strings.Contains(next, "review_paths.spec and review_paths.plan immediately before asking") {
		t.Fatalf("plan next step does not require full paths before native prompt: %q", next)
	}
	native, _ := planAsk["native_prompt"].(map[string]any)
	question, _ := native["question"].(string)
	if !strings.Contains(question, planAsk["request_id"].(string)) {
		t.Fatalf("plan native prompt has no request ref: %v", native)
	}
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
	if repos, _ := task2["repos"].([]any); len(repos) != 1 || repos[0] != repoBID {
		t.Errorf("Login route repositories = %v, want discovered repository %s", task2["repos"], repoBID)
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
	if n < 2 {
		t.Errorf("request.approve_section notifications since the scenario started = %d, want at least 2", n)
	}
}

//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Scenario 24: repository selection and scope updates without a second approval.
// Selected repos work immediately. The orchestrator updates scope with
// swarm_repos; stale versions and live worktree drops are refused.
func TestScenario24RepositoryScopeWithoutConfirmation(t *testing.T) {
	h := newHarness(t)
	repoAID, _ := e2eRepo(t, h, "repo-a")
	repoBID, _ := e2eRepo(t, h, "repo-b")

	// intent: debug, not feature — a debug spike materializes from a single
	// report artifact (materialize.go), so this scenario doesn't need a
	// separately-approved spec on top of the plan just to reach materialize;
	// repo confirmation itself doesn't care which intent it's exercised on.
	var spikeResp map[string]any
	h.doT(t, http.MethodPost, "/api/spikes", map[string]any{
		"request_id": "req-" + unique(), "name": "Repository scope " + unique(), "intent": "debug",
		"agent": "fake", "model": "fake-1", "repos": []string{repoAID},
	}, &spikeResp)
	item, _ := spikeResp["item"].(map[string]any)
	agent, _ := spikeResp["agent"].(map[string]any)
	spikeKey, _ := item["key"].(string)
	orch, _ := agent["name"].(string)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})

	// The start selection authorizes repo A immediately.
	wt := h.mustTool(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoAID, "branch": "spike/x"})
	var detail struct {
		Item map[string]any `json:"item"`
	}
	h.doT(t, http.MethodGet, "/api/items/"+spikeKey, nil, &detail)
	version := int(detail.Item["repos_version"].(float64))
	if version != 1 {
		t.Fatalf("start repos_version = %d, want 1", version)
	}
	// A live worktree protects repo A from removal.
	_, err := h.toolOut(t, orch, "swarm_repos", map[string]any{"repos": []string{repoBID}, "repos_version": version})
	if err == nil || !strings.Contains(err.Error(), "active worktrees") {
		t.Fatalf("drop busy repo = %v", err)
	}
	h.mustTool(t, orch, "swarm_worktree", map[string]any{"op": "remove", "worktree": wt["worktree_id"]})
	h.mustTool(t, orch, "swarm_worktree", map[string]any{"op": "release", "worktree": wt["worktree_id"], "agent": orch})
	// Scope changes are versioned and do not create a confirmation request.
	h.mustTool(t, orch, "swarm_repos", map[string]any{"repos": []string{repoBID}, "repos_version": version})
	_, err = h.toolOut(t, orch, "swarm_repos", map[string]any{"repos": []string{repoAID}, "repos_version": version})
	if err == nil || !strings.Contains(err.Error(), "request changed") {
		t.Fatalf("stale scope update = %v", err)
	}
	_, err = h.toolOut(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoAID, "branch": "spike/y"})
	if err == nil || !strings.Contains(err.Error(), "repo_not_confirmed") {
		t.Fatalf("removed repo worktree = %v", err)
	}
	h.mustTool(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoBID, "branch": "spike/x"})
	h.doT(t, http.MethodGet, "/api/items/"+spikeKey, nil, &detail)
	version = int(detail.Item["repos_version"].(float64))
	h.mustTool(t, orch, "swarm_repos", map[string]any{"repos": []string{repoAID, repoBID}, "repos_version": version})
	h.doT(t, http.MethodGet, "/api/items/"+spikeKey, nil, &detail)
	confirmed, _ := detail.Item["repos"].([]any)
	if len(confirmed) != 2 {
		t.Fatalf("scoped repos = %v, want both", confirmed)
	}

	// materialize and check the epic keeps the confirmed set
	reportBody := "# Report\n\n## Work breakdown\n\n```swarm-tree\n" +
		`{"root":{"type":"bug","title":"Fix it","brief":"","acceptance":["It works."]},
 "children":[{"ref":"t1","type":"task","title":"T","brief":"","acceptance":[],"role_hint":"coder","repos":["repo-b"],"workflow":{"template":"tdd-reviewed"},"steps":["Write test","Implement"],"verify":["go test ./..."],"solo":"focused"}]}` +
		"\n```\n\n## Verification\n\ngo test ./...\n"
	reportPath := filepath.Join(t.TempDir(), "report.md")
	writeFileT(t, reportPath, reportBody)
	report := h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "register", "item": spikeKey, "kind": "debug_report", "path": reportPath})
	reportID, _ := report["artifact_id"].(string)
	h.mustTool(t, orch, "swarm_ask", map[string]any{"kind": "approval", "prompt": "Review the report", "artifact": reportID})
	reportReq := h.requestByKind(t, spikeKey, "approve_report")
	h.doT(t, http.MethodPost, "/api/requests/"+reportReq["id"].(string)+"/approve", map[string]any{
		"artifact_revision": reportReq["artifact_revision"], "via": "board",
	}, nil)
	mat := h.mustTool(t, orch, "swarm_materialize", map[string]any{"spike": spikeKey, "report": reportID})
	rootKey, _ := mat["root"].(string)
	h.doT(t, http.MethodGet, "/api/items/"+rootKey, nil, &detail)
	rootRepos, _ := detail.Item["repos"].([]any)
	if !slices.Contains(rootRepos, any(repoAID)) || !slices.Contains(rootRepos, any(repoBID)) {
		t.Errorf("epic repos = %v, want both %s and %s", rootRepos, repoAID, repoBID)
	}
}

func writeFileT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

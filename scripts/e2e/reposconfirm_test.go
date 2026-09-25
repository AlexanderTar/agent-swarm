//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Scenario 24: repo confirmation.
//   - A worktree before confirming is refused (repo_not_confirmed).
//   - A confirmed set that differs from the proposal wins: the user confirms
//     repoB only, dropping the proposed repoA; a worktree in the removed
//     repo (repoA) is then refused too.
//   - A later confirm_repos adds a repo back.
//   - The materialized epic keeps the confirmed set (createTree passes the
//     spike's own confirmed items.Item.Repos straight to the new root).
func TestScenario24RepoConfirmation(t *testing.T) {
	h := newHarness(t)
	repoAID, _ := e2eRepo(t, h, "repo-a")
	repoBID, _ := e2eRepo(t, h, "repo-b")

	// intent: debug, not feature — a debug spike materializes from a single
	// report artifact (materialize.go), so this scenario doesn't need a
	// separately-approved spec on top of the plan just to reach materialize;
	// repo confirmation itself doesn't care which intent it's exercised on.
	var spikeResp map[string]any
	h.doT(t, http.MethodPost, "/api/spikes", map[string]any{
		"request_id": "req-" + unique(), "name": "Repo confirmation " + unique(), "intent": "debug",
		"agent": "fake", "model": "fake-1", "repos": []string{repoAID},
	}, &spikeResp)
	item, _ := spikeResp["item"].(map[string]any)
	agent, _ := spikeResp["agent"].(map[string]any)
	spikeKey, _ := item["key"].(string)
	orch, _ := agent["name"].(string)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})

	// a worktree before confirming is refused
	_, err := h.toolOut(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoAID, "branch": "spike/x"})
	if err == nil || !strings.Contains(err.Error(), "repo_not_confirmed") {
		t.Fatalf("worktree before confirm = %v, want repo_not_confirmed", err)
	}

	h.mustTool(t, orch, "swarm_ask", map[string]any{
		"kind": "confirm_repos", "prompt": "Needs repo A.",
		"repos": []map[string]any{{"repo": repoAID, "reason": "here"}},
	})
	confirmReq := h.waitForRequestFull(t, spikeKey, "confirm_repos", 5*time.Second)

	// the confirmed set differs from the proposal: repoB only, dropping repoA
	h.doT(t, http.MethodPost, "/api/requests/"+confirmReq["id"].(string)+"/confirm-repos", map[string]any{
		"repos": []string{repoBID}, "repos_version": 0, "via": "board",
	}, nil)

	// a worktree in the removed repo (repoA) is refused
	_, err = h.toolOut(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoAID, "branch": "spike/x"})
	if err == nil || !strings.Contains(err.Error(), "repo_not_confirmed") {
		t.Fatalf("worktree in the removed repo = %v, want repo_not_confirmed", err)
	}
	// repoB, the one actually confirmed, works
	h.mustTool(t, orch, "swarm_worktree", map[string]any{"op": "create", "repo": repoBID, "branch": "spike/x"})

	// a later confirm_repos adds repoA back
	var detail struct {
		Item map[string]any `json:"item"`
	}
	h.doT(t, http.MethodGet, "/api/items/"+spikeKey, nil, &detail)
	version, _ := detail.Item["repos_version"].(float64)
	h.mustTool(t, orch, "swarm_ask", map[string]any{
		"kind": "confirm_repos", "prompt": "Needs A back too.",
		"repos": []map[string]any{{"repo": repoAID, "reason": "after all"}, {"repo": repoBID, "reason": "still"}},
	})
	req2 := h.waitForRequestFull(t, spikeKey, "confirm_repos", 5*time.Second)
	h.doT(t, http.MethodPost, "/api/requests/"+req2["id"].(string)+"/confirm-repos", map[string]any{
		"repos": []string{repoAID, repoBID}, "repos_version": int(version), "via": "board",
	}, nil)
	h.doT(t, http.MethodGet, "/api/items/"+spikeKey, nil, &detail)
	confirmed, _ := detail.Item["repos"].([]any)
	if len(confirmed) != 2 {
		t.Fatalf("confirmed repos = %v, want both", confirmed)
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

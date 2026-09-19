//go:build e2e

package e2e

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// Scenario 21: a repo with commit.gpgsign=false gives preflight_failed with
// the exact §17.3 sentence.
func TestScenario21SigningPreflight(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", dir)
	run("config", "commit.gpgsign", "false")

	var repo map[string]any
	h.doT(t, http.MethodPost, "/api/repos", map[string]any{"path": dir}, &repo)
	repoID, _ := repo["id"].(string)
	var epic map[string]any
	h.doT(t, http.MethodPost, "/api/items", map[string]any{"type": "epic", "title": "E2E signing " + unique()}, &epic)

	status, raw, err := h.do(http.MethodPost, "/api/items/"+epic["key"].(string)+"/orchestrator", map[string]any{
		"request_id": "req-" + unique(), "agent": "fake", "model": "fake-1",
		"repos": []string{repoID}, "repos_version": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "Commit signing is off for") {
		t.Fatalf("status = %d %s, want 422 \"Commit signing is off for ...\"", status, raw)
	}
}

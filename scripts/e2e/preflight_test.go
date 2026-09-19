//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Scenario 12: preflight failures.
//
//   - Uninstalled agent: gives preflight_failed with "... isn't installed on
//     this Mac." An agent kind with no registered adapter (kinds.AgentKind
//     has no "opencode") hits the same code path as a real CLI missing from
//     PATH (internal/runtime/agents.go Preflight: `a, ok := s.Adapters[in.Kind]`),
//     deterministically, regardless of what happens to be installed on the
//     machine running the suite.
//   - Missing superpowers: NOT independently exercised here. It's gated by
//     adapter.Fake.NoSuperpowers, a struct field fixed when the daemon
//     constructs its one long-lived Fake instance (cmd/swarm/daemon.go); the
//     e2e harness is a separate process with no way to flip it per spawn, and
//     adding one would mean either a second daemon flag with no other use or
//     a live per-request override neither the spec nor this batch's file list
//     calls for. The gating logic itself (`if a.Role == RoleOrchestrator &&
//     !a.SuperpowersInstalled()`) already has direct coverage in Task 12's
//     internal/runtime unit tests, which construct Fake directly.
//   - Missing repo: a repo whose directory disappears out from under it (git
//     init'd, registered, then removed) fails Preflight's repos.IsRepo check.
func TestScenario12PreflightFailures(t *testing.T) {
	h := newHarness(t)

	// uninstalled agent
	var spikeResp map[string]any
	h.doT(t, http.MethodPost, "/api/spikes", map[string]any{
		"request_id": "req-" + unique(), "name": "Uninstalled agent " + unique(), "intent": "debug",
		"agent": "opencode", "model": "x",
	}, &spikeResp)
	agent, _ := spikeResp["agent"].(map[string]any)
	preErr, _ := agent["preflight_error"].(string)
	if !strings.Contains(preErr, "isn't installed on this Mac.") {
		t.Fatalf("preflight_error = %q, want \"isn't installed on this Mac.\"", preErr)
	}
	// §10.7: a preflight failure never spawns a session at all; the agent
	// stays "active" with session:null, offering only retry/cancel.
	if agent["session"] != nil {
		t.Fatalf("a preflight failure must never reach a session: %+v", agent)
	}

	// missing repo: registered, then its directory is gone
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	var repo map[string]any
	h.doT(t, http.MethodPost, "/api/repos", map[string]any{"path": dir}, &repo)
	repoID, _ := repo["id"].(string)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	var epic map[string]any
	h.doT(t, http.MethodPost, "/api/items", map[string]any{"type": "epic", "title": "E2E preflight " + unique()}, &epic)
	epicKey, _ := epic["key"].(string)
	status, raw, err := h.do(http.MethodPost, "/api/items/"+epicKey+"/orchestrator", map[string]any{
		"request_id": "req-" + unique(), "agent": "fake", "model": "fake-1",
		"repos": []string{repoID}, "repos_version": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "Repository is unavailable") {
		t.Fatalf("missing repo = %d %s, want 422 \"Repository is unavailable\"", status, raw)
	}
}

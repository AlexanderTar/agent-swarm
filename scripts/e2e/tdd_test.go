//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scenario 19: TDD gate on workflow tasks.
// Spec B5, line 1270: Existing scenario 19 updated to the new TDD copy.
// A completed checkpoint with no red run in this round is refused with the new copy:
// `TDD evidence missing: record the failing test run (phase: "red", ok: false) before the passing run (phase: "green", ok: true) in this round.`
// Recording red then green is accepted.
func TestScenario19TDDGate(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)

	repoID, _ := e2eRepo(t, h, "repo-tdd-wf")
	h.confirmRepo(t, epic, repoID)
	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{
		"op": "create", "repo": repoID, "branch": "task/tdd-wf",
	})
	wtID, _ := wtOut["worktree_id"].(string)
	wtPath, _ := wtOut["path"].(string)

	var list struct {
		Items []map[string]any `json:"items"`
	}
	h.doT(t, "GET", "/api/items?view=flat&root="+epic, nil, &list)
	var storyKey string
	for _, it := range list.Items {
		if it["type"] == "story" {
			storyKey = it["key"].(string)
			break
		}
	}

	taskOut := h.mustTool(t, orch, "swarm_items", map[string]any{
		"op":       "create",
		"type":     "task",
		"parent":   storyKey,
		"title":    "TDD Workflow Task",
		"workflow": map[string]any{"template": "tdd-reviewed"},
		"verify":   []string{"go test ./..."},
	})
	taskKey := taskOut["key"].(string)
	h.doT(t, http.MethodPatch, "/api/items/"+taskKey, map[string]any{"status": "ready", "revision": h.itemRevision(t, taskKey)}, nil)

	h.workflowStart(t, orch, taskKey, []map[string]any{{"worktree": wtID, "mode": "rw"}}, nil)
	coder, _, _ := h.activeWorkflowRun(t, orch, taskKey, "build")
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})

	if err := os.WriteFile(filepath.Join(wtPath, "file.go"), []byte("package tdd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "file.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "init tdd", "--no-gpg-sign")
	sha := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))

	// Completed checkpoint with no red run in this round is refused with the new copy
	err := h.tool(t, coder, "swarm_checkpoint", map[string]any{
		"kind":         "completed",
		"summary":      "done without red",
		"git":          []map[string]any{{"repo": "repo-tdd-wf", "branch": "task/tdd-wf", "sha": sha, "dirty": false}},
		"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true}},
	})
	wantNewCopy := `TDD evidence missing: record the failing test run (phase: "red", ok: false) before the passing run (phase: "green", ok: true) in this round.`
	if err == nil || err.Error() != wantNewCopy {
		t.Fatalf("completed without red err = %v, want %q", err, wantNewCopy)
	}
	if got := h.itemStatus(t, taskKey); got != "in_progress" {
		t.Fatalf("the task must stay in progress: %s", got)
	}

	// Red then green is accepted
	h.checkpointProgressTDD(t, coder, "go test ./...", 0)
	h.checkpointCompletedTDD(t, coder, "go test ./...", "repo-tdd-wf", "task/tdd-wf", sha, 0)

	// Step build completed, now on step review
	reviewer, _, _ := h.activeWorkflowRun(t, orch, taskKey, "review")
	if reviewer == "" {
		t.Fatal("expected reviewer to be active after passing TDD completed checkpoint")
	}
}

// TestLegacyTaskVerification checks that legacy tasks (no workflow) keep verifyOK:
// missing verification is refused with the legacy copy:
// `"Verification evidence missing: record what was run to verify this work before completing."`
// and passing verification (without red) is accepted.
func TestLegacyTaskVerification(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")

	// Completed checkpoint with no verification at all is refused with the legacy copy
	err := h.tool(t, worker, "swarm_checkpoint", map[string]any{
		"kind":    "completed",
		"summary": "done with no verify",
		"git":     []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
	})
	wantLegacyCopy := "Verification evidence missing: record what was run to verify this work before completing."
	if err == nil || err.Error() != wantLegacyCopy {
		t.Fatalf("err = %v, want %q", err, wantLegacyCopy)
	}
	if got := h.itemStatus(t, task); got != "in_progress" {
		t.Fatalf("the task must stay in progress: %s", got)
	}

	// Legacy tasks accept a single passing command without requiring a red phase
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind":         "completed",
		"summary":      "done with green only",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "green", "ok": true}},
	})
	if got := h.itemStatus(t, task); got != "in_review" {
		t.Fatalf("status = %s, want in_review", got)
	}
}

// TestBatchedWorkflowTaskTDDGateCopy verifies that batched workflow tasks use the unit-tagged copy:
// `TDD evidence missing for unit(s) <n,…>: record red then green with "unit": <n>.`
func TestBatchedWorkflowTaskTDDGateCopy(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)

	repoID, _ := e2eRepo(t, h, "repo-tdd-batch")
	h.confirmRepo(t, epic, repoID)
	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{
		"op": "create", "repo": repoID, "branch": "task/tdd-batch",
	})
	wtID, _ := wtOut["worktree_id"].(string)
	wtPath, _ := wtOut["path"].(string)

	var list struct {
		Items []map[string]any `json:"items"`
	}
	h.doT(t, "GET", "/api/items?view=flat&root="+epic, nil, &list)
	var storyKey string
	for _, it := range list.Items {
		if it["type"] == "story" {
			storyKey = it["key"].(string)
			break
		}
	}

	taskOut := h.mustTool(t, orch, "swarm_items", map[string]any{
		"op":       "create",
		"type":     "task",
		"parent":   storyKey,
		"title":    "Batched TDD Task",
		"workflow": map[string]any{"template": "tdd-reviewed"},
		"units": []map[string]any{
			{"title": "Unit 1", "steps": []string{"u1"}},
			{"title": "Unit 2", "steps": []string{"u2"}},
		},
		"verify": []string{"go test ./..."},
	})
	taskKey := taskOut["key"].(string)
	h.doT(t, http.MethodPatch, "/api/items/"+taskKey, map[string]any{"status": "ready", "revision": h.itemRevision(t, taskKey)}, nil)

	h.workflowStart(t, orch, taskKey, []map[string]any{{"worktree": wtID, "mode": "rw"}}, nil)
	coder, _, _ := h.activeWorkflowRun(t, orch, taskKey, "build")

	if err := os.WriteFile(filepath.Join(wtPath, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "main.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "init main", "--no-gpg-sign")
	sha := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))

	// Missing both units 1 and 2
	err := h.tool(t, coder, "swarm_checkpoint", map[string]any{
		"kind":         "completed",
		"summary":      "done untagged",
		"git":          []map[string]any{{"repo": "repo-tdd-batch", "branch": "task/tdd-batch", "sha": sha, "dirty": false}},
		"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true}},
	})
	wantBoth := `TDD evidence missing for unit(s) 1,2: record red then green with "unit": <n>.`
	if err == nil || err.Error() != wantBoth {
		t.Fatalf("missing both units err = %v, want %q", err, wantBoth)
	}

	// Record unit 1 red then green
	h.checkpointProgressTDD(t, coder, "go test ./...", 1)
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{
		"kind":         "progress",
		"summary":      "unit 1 pass",
		"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true, "unit": 1}},
	})

	// Missing unit 2
	err = h.tool(t, coder, "swarm_checkpoint", map[string]any{
		"kind":         "completed",
		"summary":      "done unit 1 only",
		"git":          []map[string]any{{"repo": "repo-tdd-batch", "branch": "task/tdd-batch", "sha": sha, "dirty": false}},
		"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true, "unit": 1}},
	})
	wantUnit2 := `TDD evidence missing for unit(s) 2: record red then green with "unit": <n>.`
	if err == nil || err.Error() != wantUnit2 {
		t.Fatalf("missing unit 2 err = %v, want %q", err, wantUnit2)
	}

	// Record unit 2 red then green
	h.checkpointProgressTDD(t, coder, "go test ./...", 2)
	h.checkpointCompletedTDD(t, coder, "go test ./...", "repo-tdd-batch", "task/tdd-batch", sha, 2)

	// Step build completes, reviewer active
	reviewer, _, _ := h.activeWorkflowRun(t, orch, taskKey, "review")
	if reviewer == "" {
		t.Fatal("expected reviewer to be active after both units satisfied")
	}
}

//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Scenario 1: Happy path workflow (tdd-reviewed).
// Start workflow with template tdd-reviewed.
// Coder runs, checks in red/green verification, completes.
// Reviewer runs against ro review worktree, passes.
// Task moves to Done, workflow succeeded, workflow_succeeded relayed.
func TestWorkflowHappyPath(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)

	repoID, _ := e2eRepo(t, h, "repo-happy")
	h.confirmRepo(t, epic, repoID)
	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{
		"op": "create", "repo": repoID, "branch": "task/happy",
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
		"title":    "Happy Task",
		"workflow": map[string]any{"template": "tdd-reviewed"},
		"verify":   []string{"go test ./..."},
	})
	taskKey := taskOut["key"].(string)
	h.doT(t, http.MethodPatch, "/api/items/"+taskKey, map[string]any{"status": "ready", "revision": h.itemRevision(t, taskKey)}, nil)

	since := time.Now()
	startOut := h.workflowStart(t, orch, taskKey, []map[string]any{{"worktree": wtID, "mode": "rw"}}, []string{"context-1"})
	if st, _ := startOut["state"].(string); st != "running" {
		t.Fatalf("workflow start state = %q, want running", st)
	}

	// 1. Coder runs on step "build"
	coder, role, round := h.activeWorkflowRun(t, orch, taskKey, "build")
	if role != "coder" || round != 1 {
		t.Fatalf("active run = agent:%s role:%s round:%d, want coder round 1", coder, role, round)
	}

	// Coder commits to worktree
	if err := os.WriteFile(filepath.Join(wtPath, "file.go"), []byte("package happy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "file.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "add happy file", "--no-gpg-sign")
	sha := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))

	// Coder records red verification then green verification with clean git
	h.checkpointProgressTDD(t, coder, "go test ./...", 0)
	h.checkpointCompletedTDD(t, coder, "go test ./...", "repo-happy", "task/happy", sha, 0)

	// 2. Reviewer runs on step "review"
	reviewer, rRole, rRound := h.activeWorkflowRun(t, orch, taskKey, "review")
	if rRole != "reviewer" || rRound != 1 {
		t.Fatalf("active review run = agent:%s role:%s round:%d, want reviewer round 1", reviewer, rRole, rRound)
	}

	// Reviewer completes with verdict pass
	h.checkpointReviewer(t, reviewer, "pass", nil)

	// Workflow completes successfully
	h.waitForWorkflowState(t, orch, taskKey, "succeeded", 5*time.Second)

	// Task moves to Done
	if got := h.itemStatus(t, taskKey); got != "done" {
		t.Fatalf("task status = %q, want done", got)
	}

	// Workflow succeeded relay sent to orchestrator
	if !h.waitForRelay(t, orch, "workflow_succeeded", since, 5*time.Second) {
		t.Fatal("expected workflow_succeeded relay to orchestrator")
	}
}

// Scenario 2: One fix round.
// Coder completes round 1.
// Reviewer returns changes_requested with findings.
// Engine triggers RetryFix, bumps to round 2, builder receives findings and completes.
// Reviewer passes round 2. Workflow succeeds.
func TestWorkflowOneFixRound(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)

	repoID, _ := e2eRepo(t, h, "repo-fix")
	h.confirmRepo(t, epic, repoID)
	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{
		"op": "create", "repo": repoID, "branch": "task/fix",
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
		"title":    "Fix round task",
		"workflow": map[string]any{"template": "tdd-reviewed"},
		"verify":   []string{"go test ./..."},
	})
	taskKey := taskOut["key"].(string)
	h.doT(t, http.MethodPatch, "/api/items/"+taskKey, map[string]any{"status": "ready", "revision": h.itemRevision(t, taskKey)}, nil)

	since := time.Now()
	h.workflowStart(t, orch, taskKey, []map[string]any{{"worktree": wtID, "mode": "rw"}}, nil)

	// Round 1 coder
	coder1, _, round := h.activeWorkflowRun(t, orch, taskKey, "build")
	if round != 1 {
		t.Fatalf("coder round = %d, want 1", round)
	}

	if err := os.WriteFile(filepath.Join(wtPath, "code.go"), []byte("package code\n// v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "code.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "v1 commit", "--no-gpg-sign")
	sha1 := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))

	h.checkpointProgressTDD(t, coder1, "go test ./...", 0)
	h.checkpointCompletedTDD(t, coder1, "go test ./...", "repo-fix", "task/fix", sha1, 0)

	// Round 1 reviewer requests changes
	reviewer1, _, rRound := h.activeWorkflowRun(t, orch, taskKey, "review")
	if rRound != 1 {
		t.Fatalf("reviewer round = %d, want 1", rRound)
	}
	h.checkpointReviewer(t, reviewer1, "changes_requested", []map[string]any{
		{"severity": "major", "file": "code.go", "line": 2, "summary": "fix comment format"},
	})

	// Engine triggers RetryFix, round bumps to 2, task moves back to in_progress
	coder2, _, round2 := h.activeWorkflowRun(t, orch, taskKey, "build")
	if round2 != 2 {
		t.Fatalf("builder round = %d, want 2", round2)
	}
	if coder2 != coder1 {
		t.Fatalf("builder changed across retry: %s vs %s", coder2, coder1)
	}
	if got := h.itemStatus(t, taskKey); got != "in_progress" {
		t.Fatalf("task status in fix round = %q, want in_progress", got)
	}

	// Round 2 coder commits fix
	if err := os.WriteFile(filepath.Join(wtPath, "code.go"), []byte("package code\n// v2 fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "code.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "v2 fix commit", "--no-gpg-sign")
	sha2 := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))

	h.checkpointProgressTDD(t, coder2, "go test ./...", 0)
	h.checkpointCompletedTDD(t, coder2, "go test ./...", "repo-fix", "task/fix", sha2, 0)

	// Round 2 reviewer passes
	reviewer2, _, rRound2 := h.activeWorkflowRun(t, orch, taskKey, "review")
	if rRound2 != 2 {
		t.Fatalf("round 2 reviewer round = %d, want 2", rRound2)
	}
	h.checkpointReviewer(t, reviewer2, "pass", nil)

	// Succeeded
	h.waitForWorkflowState(t, orch, taskKey, "succeeded", 5*time.Second)
	if got := h.itemStatus(t, taskKey); got != "done" {
		t.Fatalf("task status = %q, want done", got)
	}
	if !h.waitForRelay(t, orch, "workflow_succeeded", since, 5*time.Second) {
		t.Fatal("expected workflow_succeeded relay")
	}
}

// Scenario 3: Escalation & resume accept.
// Workflow escalates (e.g. reviewer returns blocked).
// Engine relays workflow_escalated.
// Orchestrator calls swarm_workflow resume with decision: accept.
// Workflow marks succeeded and task moves to Done.
func TestWorkflowEscalationAndResumeAccept(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)

	repoID, _ := e2eRepo(t, h, "repo-esc")
	h.confirmRepo(t, epic, repoID)
	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{
		"op": "create", "repo": repoID, "branch": "task/esc",
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
		"title":    "Escalation task",
		"workflow": map[string]any{"template": "tdd-reviewed"},
		"verify":   []string{"go test ./..."},
	})
	taskKey := taskOut["key"].(string)
	h.doT(t, http.MethodPatch, "/api/items/"+taskKey, map[string]any{"status": "ready", "revision": h.itemRevision(t, taskKey)}, nil)

	since := time.Now()
	h.workflowStart(t, orch, taskKey, []map[string]any{{"worktree": wtID, "mode": "rw"}}, nil)

	coder, _, _ := h.activeWorkflowRun(t, orch, taskKey, "build")

	if err := os.WriteFile(filepath.Join(wtPath, "file.go"), []byte("package esc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "file.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "init esc", "--no-gpg-sign")
	sha := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))

	h.checkpointProgressTDD(t, coder, "go test ./...", 0)
	h.checkpointCompletedTDD(t, coder, "go test ./...", "repo-esc", "task/esc", sha, 0)

	// Reviewer returns blocked verdict
	reviewer, _, _ := h.activeWorkflowRun(t, orch, taskKey, "review")
	h.checkpointReviewer(t, reviewer, "blocked", []map[string]any{
		{"severity": "critical", "file": "file.go", "line": 1, "summary": "architecture blocker"},
	})

	// Workflow escalates
	escOut := h.waitForWorkflowState(t, orch, taskKey, "escalated", 5*time.Second)
	if escReason, _ := escOut["escalation"].(string); !strings.Contains(escReason, "blocked") {
		t.Fatalf("escalation reason = %q, want mentioning blocked", escReason)
	}

	// Relay workflow_escalated sent to orchestrator
	if !h.waitForRelay(t, orch, "workflow_escalated", since, 5*time.Second) {
		t.Fatal("expected workflow_escalated relay to orchestrator")
	}

	// Orchestrator calls resume with decision: accept
	resumeOut := h.workflowResume(t, orch, taskKey, "accept", "override by orchestrator")
	if st, _ := resumeOut["state"].(string); st != "succeeded" {
		t.Fatalf("resume state = %q, want succeeded", st)
	}

	// Task moves to Done
	if got := h.itemStatus(t, taskKey); got != "done" {
		t.Fatalf("task status after resume accept = %q, want done", got)
	}
}

// Scenario 4: Batched package with per-unit evidence.
// Task has units.
// Coder provides per-unit red/green verification (unit: 1, unit: 2).
// Verify gate passes.
func TestWorkflowBatchedPerUnitEvidence(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)

	repoID, _ := e2eRepo(t, h, "repo-batch")
	h.confirmRepo(t, epic, repoID)
	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{
		"op": "create", "repo": repoID, "branch": "task/batch",
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
		"title":    "Batched task",
		"workflow": map[string]any{"template": "tdd-reviewed"},
		"units": []map[string]any{
			{"title": "Unit 1", "steps": []string{"u1 step"}},
			{"title": "Unit 2", "steps": []string{"u2 step"}},
		},
		"verify": []string{"go test ./..."},
	})
	taskKey := taskOut["key"].(string)
	h.doT(t, http.MethodPatch, "/api/items/"+taskKey, map[string]any{"status": "ready", "revision": h.itemRevision(t, taskKey)}, nil)

	h.workflowStart(t, orch, taskKey, []map[string]any{{"worktree": wtID, "mode": "rw"}}, nil)
	coder, _, _ := h.activeWorkflowRun(t, orch, taskKey, "build")

	if err := os.WriteFile(filepath.Join(wtPath, "batch.go"), []byte("package batch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "batch.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "init batch", "--no-gpg-sign")
	sha := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))

	// Record red and green for unit 1 only
	h.checkpointProgressTDD(t, coder, "go test ./...", 1)
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{
		"kind":         "progress",
		"summary":      "pass unit 1",
		"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true, "unit": 1}},
	})

	// Attempting to complete with only unit 1 fails with missing unit 2
	err := h.tool(t, coder, "swarm_checkpoint", map[string]any{
		"kind":         "completed",
		"summary":      "done unit 1 only",
		"git":          []map[string]any{{"repo": "repo-batch", "branch": "task/batch", "sha": sha, "dirty": false}},
		"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true, "unit": 1}},
	})
	wantErr := `TDD evidence missing for unit(s) 2: record red then green with "unit": <n>.`
	if err == nil || err.Error() != wantErr {
		t.Fatalf("completed with missing unit 2 err = %v, want %q", err, wantErr)
	}

	// Now record red and green for unit 2
	h.checkpointProgressTDD(t, coder, "go test ./...", 2)
	h.checkpointCompletedTDD(t, coder, "go test ./...", "repo-batch", "task/batch", sha, 2)

	// Step build completes; reviewer runs and passes
	reviewer, _, _ := h.activeWorkflowRun(t, orch, taskKey, "review")
	h.checkpointReviewer(t, reviewer, "pass", nil)

	// Succeeded
	h.waitForWorkflowState(t, orch, taskKey, "succeeded", 5*time.Second)
	if got := h.itemStatus(t, taskKey); got != "done" {
		t.Fatalf("task status = %q, want done", got)
	}
}

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

func TestSwarmWorkflowToolsOnlyForOrchestrators(t *testing.T) {
	s, orch := newOrchestratorServer(t)

	// Orchestrator sees swarm_workflow.
	var found bool
	var wfTool ToolDef
	for _, d := range s.ToolsFor(orch.Caller) {
		if d.Name == "swarm_workflow" {
			found = true
			wfTool = d
			break
		}
	}
	if !found {
		t.Fatal("orchestrator does not see swarm_workflow")
	}

	// Description is under 60 words.
	if n := len(strings.Fields(wfTool.Description)); n == 0 || n > 60 {
		t.Fatalf("swarm_workflow description word count = %d, want 1..60", n)
	}

	// Schema has required ["op", "item"].
	var schema struct {
		Type       string                 `json:"type"`
		Required   []string               `json:"required"`
		Properties map[string]interface{} `json:"properties"`
	}
	if err := json.Unmarshal(wfTool.Schema, &schema); err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("schema type = %q, want object", schema.Type)
	}
	hasOp, hasItem := false, false
	for _, r := range schema.Required {
		if r == "op" {
			hasOp = true
		}
		if r == "item" {
			hasItem = true
		}
	}
	if !hasOp || !hasItem {
		t.Fatalf("schema required = %v, want ['op', 'item']", schema.Required)
	}

	for _, prop := range []string{"op", "item", "worktrees", "context", "decision", "note", "request_id"} {
		if _, ok := schema.Properties[prop]; !ok {
			t.Fatalf("schema missing property %q", prop)
		}
	}

	// Non-orchestrator (coder) does not see swarm_workflow.
	sCoder, coderSeed := newServerWithSession(t)
	for _, d := range sCoder.ToolsFor(coderSeed.Caller) {
		if d.Name == "swarm_workflow" {
			t.Fatal("coder unexpectedly sees swarm_workflow")
		}
	}

	// Unbound caller does not see swarm_workflow.
	unbound := Caller{Unbound: true}
	for _, d := range s.ToolsFor(unbound) {
		if d.Name == "swarm_workflow" {
			t.Fatal("unbound caller unexpectedly sees swarm_workflow")
		}
	}

	// Non-orchestrator calling swarm_workflow gets not-visible error.
	ctx := context.Background()
	_, err := sCoder.call(ctx, coderSeed.Caller, "swarm_workflow", `{"op":"status","item":"TASK-1"}`)
	if err == nil || !strings.Contains(err.Error(), "not available to this caller") {
		t.Fatalf("call by coder err = %v, want not available to this caller", err)
	}
}

func TestSwarmWorkflowStartStatusResumeCancel(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()

	// Create a worktree for the orchestrator.
	wtOut, err := s.call(ctx, seed.Caller, "swarm_worktree", fmt.Sprintf(
		`{"op":"create","repo":"%s","branch":"wf-branch","base":"main"}`, seed.RepoID))
	if err != nil {
		t.Fatal(err)
	}
	var wtRes struct {
		ID string `json:"worktree_id"`
	}
	if err := json.Unmarshal(mustJSON(wtOut), &wtRes); err != nil {
		t.Fatal(err)
	}

	// Configure TaskKey with a workflow.
	_, err = s.RT.Items.Update(ctx, seed.TaskKey, items.Patch{
		Workflow: &workflow.Spec{Template: "mechanical"},
		Steps:    &[]string{"step 1"},
		Verify:   &[]string{"true"},
		Revision: 1,
	}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}

	// Calling status before start fails because it has no running or past workflow.
	_, err = s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(`{"op":"status","item":"%s"}`, seed.TaskKey))
	if err == nil || !strings.Contains(err.Error(), "has no workflow") {
		t.Fatalf("status before start err = %v, want 'has no workflow'", err)
	}

	// 1. Start workflow.
	startOut, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"start","item":"%s","worktrees":[{"worktree":"%s","mode":"rw"}],"context":["c1"]}`,
		seed.TaskKey, wtRes.ID))
	if err != nil {
		t.Fatalf("start err = %v", err)
	}
	var startRes struct {
		Workflow    string                    `json:"workflow"`
		State       string                    `json:"state"`
		Round       int                       `json:"round"`
		ExtraRounds int                       `json:"extra_rounds"`
		Runs        []runtime.WorkflowRunView `json:"runs"`
		Escalation  string                    `json:"escalation"`
	}
	if err := json.Unmarshal(mustJSON(startOut), &startRes); err != nil {
		t.Fatal(err)
	}
	if startRes.Workflow == "" || startRes.State != "running" || startRes.Round != 1 {
		t.Fatalf("unexpected start result: %+v", startRes)
	}

	// 2. Status workflow.
	statusOut, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(`{"op":"status","item":"%s"}`, seed.TaskKey))
	if err != nil {
		t.Fatalf("status err = %v", err)
	}
	var statusRes struct {
		Workflow    string                    `json:"workflow"`
		State       string                    `json:"state"`
		Round       int                       `json:"round"`
		ExtraRounds int                       `json:"extra_rounds"`
		Runs        []runtime.WorkflowRunView `json:"runs"`
		Escalation  string                    `json:"escalation"`
	}
	if err := json.Unmarshal(mustJSON(statusOut), &statusRes); err != nil {
		t.Fatal(err)
	}
	if statusRes.Workflow != startRes.Workflow || statusRes.State != "running" {
		t.Fatalf("unexpected status result: %+v", statusRes)
	}

	// 3. Resume when running should fail.
	_, err = s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(`{"op":"resume","item":"%s","decision":"accept"}`, seed.TaskKey))
	if err == nil || !strings.Contains(err.Error(), "isn't waiting on you") {
		t.Fatalf("resume while running err = %v, want 'isn't waiting on you'", err)
	}

	// Simulate escalation in DB.
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE workflows SET state = 'escalated', escalation = 'review failed' WHERE id = ?`, startRes.Workflow); err != nil {
		t.Fatal(err)
	}

	// Invalid decision should fail.
	_, err = s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(`{"op":"resume","item":"%s","decision":"maybe"}`, seed.TaskKey))
	if err == nil || !strings.Contains(err.Error(), `decision must be "retry", "accept" or "fail"`) {
		t.Fatalf("resume with invalid decision err = %v, want decision must be retry/accept/fail", err)
	}

	// Resume accept.
	resumeOut, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"resume","item":"%s","decision":"accept","note":"good job"}`, seed.TaskKey))
	if err != nil {
		t.Fatalf("resume accept err = %v", err)
	}
	var resumeRes struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(mustJSON(resumeOut), &resumeRes); err != nil {
		t.Fatal(err)
	}
	if resumeRes.State != "succeeded" {
		t.Fatalf("resume state = %q, want 'succeeded'", resumeRes.State)
	}

	// 4. Cancel workflow on OtherTaskKey.
	_, err = s.RT.Items.Update(ctx, seed.OtherTaskKey, items.Patch{
		Workflow: &workflow.Spec{Template: "mechanical"},
		Steps:    &[]string{"step 1"},
		Verify:   &[]string{"true"},
		Revision: 1,
	}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}

	start2Out, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"start","item":"%s","worktrees":[{"worktree":"%s","mode":"rw"}]}`,
		seed.OtherTaskKey, wtRes.ID))
	if err != nil {
		t.Fatalf("start2 err = %v", err)
	}
	var start2Res struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(mustJSON(start2Out), &start2Res); err != nil {
		t.Fatal(err)
	}

	cancelOut, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(`{"op":"cancel","item":"%s"}`, seed.OtherTaskKey))
	if err != nil {
		t.Fatalf("cancel err = %v", err)
	}
	var cancelRes struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(mustJSON(cancelOut), &cancelRes); err != nil {
		t.Fatal(err)
	}
	if cancelRes.State != "cancelled" {
		t.Fatalf("cancel state = %q, want 'cancelled'", cancelRes.State)
	}
}

func TestSwarmWorkflowIdempotentStart(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()

	wtOut, err := s.call(ctx, seed.Caller, "swarm_worktree", fmt.Sprintf(
		`{"op":"create","repo":"%s","branch":"wf-idem-branch","base":"main"}`, seed.RepoID))
	if err != nil {
		t.Fatal(err)
	}
	var wtRes struct {
		ID string `json:"worktree_id"`
	}
	if err := json.Unmarshal(mustJSON(wtOut), &wtRes); err != nil {
		t.Fatal(err)
	}

	_, err = s.RT.Items.Update(ctx, seed.TaskKey, items.Patch{
		Workflow: &workflow.Spec{Template: "mechanical"},
		Steps:    &[]string{"step 1"},
		Verify:   &[]string{"true"},
		Revision: 1,
	}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}

	reqID := "req-wf-idem-1"
	out1, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"start","item":"%s","worktrees":[{"worktree":"%s","mode":"rw"}],"request_id":"%s"}`,
		seed.TaskKey, wtRes.ID, reqID))
	if err != nil {
		t.Fatalf("first start call err = %v", err)
	}

	var res1 struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
		Round    int    `json:"round"`
	}
	if err := json.Unmarshal(mustJSON(out1), &res1); err != nil {
		t.Fatal(err)
	}

	// Repeated call with the exact same request_id must succeed and return the identical result.
	out2, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"start","item":"%s","worktrees":[{"worktree":"%s","mode":"rw"}],"request_id":"%s"}`,
		seed.TaskKey, wtRes.ID, reqID))
	if err != nil {
		t.Fatalf("second start call (idempotent replay) err = %v", err)
	}

	var res2 struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
		Round    int    `json:"round"`
	}
	if err := json.Unmarshal(mustJSON(out2), &res2); err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("idempotent replay mismatch: res1 = %+v, res2 = %+v", res1, res2)
	}

	// Repeated call with a DIFFERENT request_id must fail with 'already has a running workflow'.
	_, err = s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"start","item":"%s","worktrees":[{"worktree":"%s","mode":"rw"}],"request_id":"req-different"}`,
		seed.TaskKey, wtRes.ID))
	if err == nil || !strings.Contains(err.Error(), "already has a running workflow") {
		t.Fatalf("start with different request_id err = %v, want 'already has a running workflow'", err)
	}
}

func TestSwarmWorkflowIdempotentResume(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()

	wtOut, err := s.call(ctx, seed.Caller, "swarm_worktree", fmt.Sprintf(
		`{"op":"create","repo":"%s","branch":"wf-resume-branch","base":"main"}`, seed.RepoID))
	if err != nil {
		t.Fatal(err)
	}
	var wtRes struct {
		ID string `json:"worktree_id"`
	}
	if err := json.Unmarshal(mustJSON(wtOut), &wtRes); err != nil {
		t.Fatal(err)
	}

	_, err = s.RT.Items.Update(ctx, seed.TaskKey, items.Patch{
		Workflow: &workflow.Spec{Template: "mechanical"},
		Steps:    &[]string{"step 1"},
		Verify:   &[]string{"true"},
		Revision: 1,
	}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}

	startOut, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"start","item":"%s","worktrees":[{"worktree":"%s","mode":"rw"}]}`,
		seed.TaskKey, wtRes.ID))
	if err != nil {
		t.Fatal(err)
	}
	var startRes struct {
		Workflow string `json:"workflow"`
	}
	if err := json.Unmarshal(mustJSON(startOut), &startRes); err != nil {
		t.Fatal(err)
	}

	// Escalate
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE workflows SET state = 'escalated', escalation = 'test' WHERE id = ?`, startRes.Workflow); err != nil {
		t.Fatal(err)
	}

	reqID := "req-resume-idem-1"
	out1, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"resume","item":"%s","decision":"retry","note":"try again","request_id":"%s"}`,
		seed.TaskKey, reqID))
	if err != nil {
		t.Fatalf("first resume call err = %v", err)
	}
	var res1 struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
		Round    int    `json:"round"`
	}
	if err := json.Unmarshal(mustJSON(out1), &res1); err != nil {
		t.Fatal(err)
	}

	// Repeated call with the exact same request_id must succeed and return identical result
	out2, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"resume","item":"%s","decision":"retry","note":"try again","request_id":"%s"}`,
		seed.TaskKey, reqID))
	if err != nil {
		t.Fatalf("second resume call (idempotent replay) err = %v", err)
	}
	var res2 struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
		Round    int    `json:"round"`
	}
	if err := json.Unmarshal(mustJSON(out2), &res2); err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("idempotent replay mismatch: res1 = %+v, res2 = %+v", res1, res2)
	}

	// Resume with DIFFERENT request_id must fail because workflow is now running
	_, err = s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"resume","item":"%s","decision":"retry","note":"try again","request_id":"req-different"}`,
		seed.TaskKey))
	if err == nil || !strings.Contains(err.Error(), "isn't waiting on you") {
		t.Fatalf("resume with different request_id err = %v, want isn't waiting on you", err)
	}
}

func TestSwarmWorkflowIdempotentCancel(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()

	wtOut, err := s.call(ctx, seed.Caller, "swarm_worktree", fmt.Sprintf(
		`{"op":"create","repo":"%s","branch":"wf-cancel-branch","base":"main"}`, seed.RepoID))
	if err != nil {
		t.Fatal(err)
	}
	var wtRes struct {
		ID string `json:"worktree_id"`
	}
	if err := json.Unmarshal(mustJSON(wtOut), &wtRes); err != nil {
		t.Fatal(err)
	}

	_, err = s.RT.Items.Update(ctx, seed.TaskKey, items.Patch{
		Workflow: &workflow.Spec{Template: "mechanical"},
		Steps:    &[]string{"step 1"},
		Verify:   &[]string{"true"},
		Revision: 1,
	}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"start","item":"%s","worktrees":[{"worktree":"%s","mode":"rw"}]}`,
		seed.TaskKey, wtRes.ID))
	if err != nil {
		t.Fatal(err)
	}

	reqID := "req-cancel-idem-1"
	out1, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"cancel","item":"%s","request_id":"%s"}`,
		seed.TaskKey, reqID))
	if err != nil {
		t.Fatalf("first cancel call err = %v", err)
	}
	var res1 struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(mustJSON(out1), &res1); err != nil {
		t.Fatal(err)
	}
	if res1.State != "cancelled" {
		t.Fatalf("cancel state = %q, want 'cancelled'", res1.State)
	}

	// Repeated call with the exact same request_id must succeed and return identical result
	out2, err := s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"cancel","item":"%s","request_id":"%s"}`,
		seed.TaskKey, reqID))
	if err != nil {
		t.Fatalf("second cancel call (idempotent replay) err = %v", err)
	}
	var res2 struct {
		Workflow string `json:"workflow"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(mustJSON(out2), &res2); err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("idempotent replay mismatch: res1 = %+v, res2 = %+v", res1, res2)
	}

	// Cancel with DIFFERENT request_id must fail because workflow is already cancelled
	_, err = s.call(ctx, seed.Caller, "swarm_workflow", fmt.Sprintf(
		`{"op":"cancel","item":"%s","request_id":"req-different"}`,
		seed.TaskKey))
	if err == nil || !strings.Contains(err.Error(), "isn't waiting on you") {
		t.Fatalf("cancel with different request_id err = %v, want isn't waiting on you", err)
	}
}


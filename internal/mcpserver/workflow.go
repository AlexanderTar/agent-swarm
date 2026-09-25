package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

type workflowWorktreeWire struct {
	Worktree string `json:"worktree"`
	Mode     string `json:"mode"`
}

func workflowStateOut(st runtime.WorkflowState) map[string]any {
	runs := st.Runs
	if runs == nil {
		runs = []runtime.WorkflowRunView{}
	}
	return map[string]any{
		"workflow":     st.ID,
		"state":        st.State,
		"round":        st.Round,
		"extra_rounds": st.ExtraRounds,
		"runs":         runs,
		"escalation":   st.Escalation,
	}
}

// workflowTool implements the swarm_workflow MCP tool (Spec B7).
func workflowTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_workflow",
		Description: "Start, inspect, resume or cancel a workflow on a task in your subtree.",
		Roles:       orchestratorRole,
		Schema: json.RawMessage(`{
			"type": "object",
			"required": ["op", "item"],
			"properties": {
				"op": {
					"type": "string",
					"enum": ["start", "status", "resume", "cancel"]
				},
				"item": {
					"type": "string"
				},
				"worktrees": {
					"type": "array",
					"items": {
						"type": "object",
						"required": ["worktree", "mode"],
						"properties": {
							"worktree": {"type": "string"},
							"mode": {"type": "string", "enum": ["rw", "ro"]}
						}
					}
				},
				"context": {
					"type": "array",
					"items": {"type": "string"}
				},
				"decision": {
					"type": "string",
					"enum": ["retry", "accept", "fail"]
				},
				"note": {
					"type": "string"
				},
				"request_id": {
					"type": "string"
				}
			},
			"additionalProperties": true
		}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op        string                 `json:"op"`
				Item      string                 `json:"item"`
				Worktrees []workflowWorktreeWire `json:"worktrees"`
				Context   []string               `json:"context"`
				Decision  string                 `json:"decision"`
				Note      string                 `json:"note"`
				RequestID string                 `json:"request_id"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			if in.Op == "" || in.Item == "" {
				return nil, &items.Error{Code: items.CodeBadRequest, Message: "op and item are required"}
			}

			switch in.Op {
			case "start":
				orch, err := callerAgent(ctx, s, c)
				if err != nil {
					return nil, err
				}
				wts := make([]runtime.WorkflowWorktree, len(in.Worktrees))
				for i, w := range in.Worktrees {
					wts[i] = runtime.WorkflowWorktree{
						WorktreeID: w.Worktree,
						Mode:       w.Mode,
					}
				}
				st, err := s.RT.StartWorkflow(ctx, orch, runtime.StartWorkflowInput{
					ItemKey:   in.Item,
					Worktrees: wts,
					Context:   in.Context,
					SessionID: c.SessionID,
					RequestID: in.RequestID,
				})
				if err != nil {
					return nil, err
				}
				return workflowStateOut(st), nil

			case "status":
				st, ok, err := s.RT.WorkflowFor(ctx, in.Item)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s has no workflow.", in.Item)}
				}
				return workflowStateOut(st), nil

			case "resume":
				if in.Decision != "retry" && in.Decision != "accept" && in.Decision != "fail" {
					return nil, &items.Error{Code: items.CodeBadRequest, Message: `decision must be "retry", "accept" or "fail".`}
				}
				orch, err := callerAgent(ctx, s, c)
				if err != nil {
					return nil, err
				}
				st, err := s.RT.ResumeWorkflow(ctx, orch, in.Item, in.Decision, in.Note, c.SessionID, in.RequestID)
				if err != nil {
					return nil, err
				}
				return workflowStateOut(st), nil

			case "cancel":
				orch, err := callerAgent(ctx, s, c)
				if err != nil {
					return nil, err
				}
				st, err := s.RT.CancelWorkflow(ctx, orch, in.Item, c.SessionID, in.RequestID)
				if err != nil {
					return nil, err
				}
				return workflowStateOut(st), nil

			default:
				return nil, fmt.Errorf("unknown swarm_workflow op %q", in.Op)
			}
		},
	}
}

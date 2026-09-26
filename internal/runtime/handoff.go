package runtime

import (
	"context"
	"fmt"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Batch 3 external surface: the replacement coordinator owns every session
// transition while an operation is in flight (operationOwnsSession), so
// Pause, Resume and Retry consult it before touching a session.

// refuseIfOperationInFlight returns a 409 naming the agent's in-flight
// replacement operation, or nil when the coordinator owns nothing. Callers
// surface this instead of racing the operation driver.
func (s *Store) refuseIfOperationInFlight(ctx context.Context, agentID string) error {
	op, ok, err := s.PendingOperation(ctx, agentID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return &items.Error{Code: items.CodeConflict,
		Message: fmt.Sprintf("A replacement is already in progress: %s.", op.ID)}
}

// ControlsAgent is the swarm_control handoff authority check: an agent
// controls itself and every descendant down its own parent chain, and
// nothing else. A same-root agent in another subtree is not controlled.
func (s *Store) ControlsAgent(ctx context.Context, callerID, targetID string) (bool, error) {
	if callerID == targetID {
		if _, err := s.agentByID(ctx, targetID); err != nil {
			return false, err
		}
		return true, nil
	}
	cur, err := s.agentByID(ctx, targetID)
	if err != nil {
		return false, err
	}
	for cur.ParentAgentID != "" {
		if cur.ParentAgentID == callerID {
			return true, nil
		}
		cur, err = s.agentByID(ctx, cur.ParentAgentID)
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

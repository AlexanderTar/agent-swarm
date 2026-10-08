package runtime

import "context"

// LowTokenFor is the effective low-token value for a: the nearest orchestrator
// (a itself included) with a non-NULL override wins, else the global default.
// It is resolved at read time and never copied onto children.
func (s *Store) LowTokenFor(ctx context.Context, a Agent) (bool, error) {
	cur := a
	for i := 0; i < 32; i++ {
		if cur.Role == RoleOrchestrator && cur.LowToken != nil {
			return *cur.LowToken, nil
		}
		if cur.ParentAgentID == "" {
			break
		}
		p, err := s.agentByID(ctx, cur.ParentAgentID)
		if err != nil {
			return false, err
		}
		cur = p
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return false, nil // fail open to "off"
	}
	return cfg.LowTokenMode, nil
}

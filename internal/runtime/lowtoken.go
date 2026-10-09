package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

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

// LowTokenOffNote is the live note sent when low-token mode turns off.
const LowTokenOffNote = "Low-token mode is now off. Follow your skills as written."

// LowTokenOnNote is the live note sent when low-token mode turns on, for an
// agent of the given role and kind.
func LowTokenOnNote(role Role, kind string) string {
	return "Low-token mode is now on for your orchestrator.\n\n" + LowTokenBlock(role, kind)
}

func liveAgent(a Agent) bool { return a.State == AgentActive || a.State == AgentQueued }

// lowTokenSnapshot maps each live agent in as to its effective low-token value.
func (s *Store) lowTokenSnapshot(ctx context.Context, as []Agent) (map[string]bool, error) {
	out := make(map[string]bool, len(as))
	for _, a := range as {
		if !liveAgent(a) {
			continue
		}
		v, err := s.LowTokenFor(ctx, a)
		if err != nil {
			return nil, err
		}
		out[a.ID] = v
	}
	return out, nil
}

// noteLowTokenChanges enqueues one assignment_update note per live agent in as
// whose effective value differs from before, and returns how many it noted.
func (s *Store) noteLowTokenChanges(ctx context.Context, as []Agent, before map[string]bool) (int, error) {
	after, err := s.lowTokenSnapshot(ctx, as)
	if err != nil {
		return 0, err
	}
	n := 0
	err = s.tx(ctx, func(tx *sql.Tx) error {
		n = 0
		for _, a := range as {
			now, live := after[a.ID]
			if !live || now == before[a.ID] {
				continue
			}
			note := LowTokenOffNote
			if now {
				note = LowTokenOnNote(a.Role, string(a.Kind))
			}
			payload, err := json.Marshal(map[string]string{"note": note})
			if err != nil {
				return err
			}
			if _, err := s.enqueue(ctx, tx, Message{Kind: "assignment_update", Origin: "daemon",
				ToAgentID: a.ID, RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload}); err != nil {
				return err
			}
			if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// SetLowToken sets an orchestrator's override and notes the live agents in its
// subtree whose effective value changed. It returns how many it noted.
func (s *Store) SetLowToken(ctx context.Context, name string, on bool) (int, error) {
	orch, err := s.Agent(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, &items.Error{Code: items.CodeNotFound, Message: fmt.Sprintf("Unknown agent %s.", name)}
	}
	if err != nil {
		return 0, err
	}
	if orch.Role != RoleOrchestrator {
		return 0, &items.Error{Code: items.CodeBadRequest, Message: name + " is not an orchestrator."}
	}
	if !liveAgent(orch) {
		return 0, &items.Error{Code: items.CodeBadRequest, Message: name + " has finished."}
	}
	desc, err := s.descendantAgents(ctx, orch.ID)
	if err != nil {
		return 0, err
	}
	as := append([]Agent{orch}, desc...)
	before, err := s.lowTokenSnapshot(ctx, as)
	if err != nil {
		return 0, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET low_token = ? WHERE id = ?`, on, orch.ID); err != nil {
		return 0, err
	}
	return s.noteLowTokenChanges(ctx, s.reloadAgents(ctx, as), before)
}

// SetLowTokenAll sets the global default, clears every live orchestrator's
// override, and notes the live agents whose effective value changed.
func (s *Store) SetLowTokenAll(ctx context.Context, on bool) (int, error) {
	rows, err := s.queryIDs(ctx, `SELECT id FROM agents WHERE state IN ('active', 'queued')`)
	if err != nil {
		return 0, err
	}
	as := make([]Agent, 0, len(rows))
	for _, id := range rows {
		a, err := s.agentByID(ctx, id)
		if err != nil {
			return 0, err
		}
		as = append(as, a)
	}
	before, err := s.lowTokenSnapshot(ctx, as)
	if err != nil {
		return 0, err
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.Settings.SetLowTokenModeTx(ctx, tx, on); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE agents SET low_token = NULL
			WHERE role = 'orchestrator' AND state IN ('active', 'queued')`)
		return err
	}); err != nil {
		return 0, err
	}
	return s.noteLowTokenChanges(ctx, s.reloadAgents(ctx, as), before)
}

// reloadAgents re-reads as so the post-change snapshot sees the new overrides.
func (s *Store) reloadAgents(ctx context.Context, as []Agent) []Agent {
	out := make([]Agent, 0, len(as))
	for _, a := range as {
		if fresh, err := s.agentByID(ctx, a.ID); err == nil {
			a = fresh
		}
		out = append(out, a)
	}
	return out
}

// noteLowTokenOn enqueues the on-note for a, for a start path that has no
// kickoff to carry the guidance block (muse resume).
func (s *Store) noteLowTokenOn(ctx context.Context, a Agent) error {
	payload, err := json.Marshal(map[string]string{"note": LowTokenOnNote(a.Role, string(a.Kind))})
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := s.enqueue(ctx, tx, Message{Kind: "assignment_update", Origin: "daemon",
			ToAgentID: a.ID, RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload})
		return err
	})
}

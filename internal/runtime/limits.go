package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

// Admit reports whether a new agent of this role may start now (A2, I20).
// Orchestrators count only against max_orchestrators; every other role counts
// against max_agents and max_agents_per_root. A queued agent holds its slot, so
// the FIFO order the drain uses stays stable.
func (s *Store) Admit(ctx context.Context, tx *sql.Tx, role Role, rootItemID string) (bool, error) {
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return false, err
	}
	count := func(query string, args ...any) (int, error) {
		var n int
		return n, tx.QueryRowContext(ctx, query, args...).Scan(&n)
	}
	if role == RoleOrchestrator {
		n, err := count(`SELECT COUNT(*) FROM agents WHERE role = 'orchestrator'
			AND state = 'active'`)
		return n < cfg.MaxOrchestrators, err
	}
	global, err := count(`SELECT COUNT(*) FROM agents WHERE role <> 'orchestrator'
		AND state = 'active'`)
	if err != nil {
		return false, err
	}
	if global >= cfg.MaxAgents {
		return false, nil
	}
	perRoot, err := count(`SELECT COUNT(*) FROM agents WHERE role <> 'orchestrator'
		AND root_item_id = ? AND state = 'active'`, rootItemID)
	if err != nil {
		return false, err
	}
	return perRoot < cfg.MaxAgentsPerRoot, nil
}

// DrainQueue starts queued agents in FIFO order while slots are free. It runs
// from the reconciler (§10.6) and after any agent finishes.
func (s *Store) DrainQueue(ctx context.Context) error {
	// tried stops the loop from re-selecting a row it could not move (D32). Without
	// it, a queued agent whose startQueued returns an error before it changes state
	// is selected again on the next pass, forever, inside the reconciler's tick.
	tried := map[string]bool{}
	for {
		var id string
		err := s.DB.QueryRowContext(ctx, `SELECT id FROM agents WHERE state = 'queued'
			ORDER BY created_at, id LIMIT 1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if tried[id] {
			return nil
		}
		tried[id] = true
		a, err := s.agentByID(ctx, id)
		if err != nil {
			return err
		}
		// A queued child that fails preflight when it is taken off the queue
		// relays spawn_failed to its parent (§11.3).
		admitted, err := s.startQueued(ctx, a)
		if err != nil {
			s.logf("drain: %s could not start: %v", a.Name, err)
		}
		if !admitted && err == nil {
			return nil
		}
	}
}

func (s *Store) startQueued(ctx context.Context, a Agent) (bool, error) {
	ad, ok := s.Adapters[a.Kind]
	if !ok {
		return false, fmt.Errorf("no adapter for %s", a.Kind)
	}

	var itemKey string
	if err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.ItemID).Scan(&itemKey); err != nil {
		return false, err
	}
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		return false, err
	}

	var repoPaths []string
	for _, repoID := range it.Repos {
		var p string
		if err := s.DB.QueryRowContext(ctx, `SELECT path FROM repos WHERE id = ?`, repoID).Scan(&p); err == nil {
			repoPaths = append(repoPaths, p)
		}
	}

	preflightErr := s.Preflight(ctx, PreflightInput{
		Kind:      a.Kind,
		Model:     a.Model,
		Effort:    a.Effort,
		Role:      a.Role,
		RepoPaths: repoPaths,
	})

	var admitted bool
	nowMs := s.now().UnixMilli()
	sesID := ids.New("ses")
	tokBytes := make([]byte, 32)
	if _, err := rand.Read(tokBytes); err != nil {
		return false, err
	}
	tokHashBytes := sha256.Sum256(tokBytes)
	tokHash := hex.EncodeToString(tokHashBytes[:])
	cwd := filepath.Join(s.Home, "work", a.Name)

	err = s.tx(ctx, func(tx *sql.Tx) error {
		ok, err := s.Admit(ctx, tx, a.Role, a.RootItemID)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		admitted = true

		if preflightErr != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'active' WHERE id = ?`, a.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions
				(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, cwd_kind, state, started_at)
				VALUES (?, ?, 1, 1, ?, ?, ?, 'neutral', 'failed', ?)`,
				sesID, a.ID, tokHash, a.Name, cwd, nowMs); err != nil {
				return err
			}
			if a.ParentAgentID != "" {
				var seq int64
				if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq); err != nil {
					return err
				}
				payload, _ := json.Marshal(map[string]any{
					"event":  "spawn_failed",
					"agent":  a.Name,
					"reason": preflightErr.Error(),
				})
				if _, err := tx.ExecContext(ctx, `INSERT INTO messages
					(id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, item_id, payload_json, state, created_at)
					VALUES (?, ?, 'relay', 'immediate', 1, 'daemon', ?, ?, ?, ?, 'pending', ?)`,
					ids.New("msg"), seq, a.ParentAgentID, a.RootItemID, a.ItemID, string(payload), nowMs); err != nil {
					return err
				}
			}
			return nil
		}

		_, err = tx.ExecContext(ctx, `UPDATE agents SET state = 'active' WHERE id = ?`, a.ID)
		return err
	})
	if err != nil {
		return false, err
	}
	if !admitted {
		return false, nil
	}

	if preflightErr != nil {
		if s.Notify != nil {
			_ = s.Notify.Raise(ctx, nil, NotifyInput{
				Kind:      "agent.preflight_failed",
				AgentName: a.Name,
				ItemKey:   it.Key,
				Args:      map[string]string{"reason": preflightErr.Error()},
			})
		}
		return true, nil
	}

	a.State = AgentActive

	ses, err := s.startSession(ctx, a, 1, 1, false, "")
	if err != nil {
		return true, err
	}

	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, ses, ad); err != nil {
			s.logf("drain: watchStartup %s: %v", a.Name, err)
		}
	})

	return true, nil
}

package runtime

import (
	"context"
	"database/sql"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Lineage links each agent generation to its predecessor through the
// agent_lineage table. Agent rows are never renamed, deleted or merged:
// recovery reuses the canonical row in place, and this table records that
// it did. Migration 0014 backfills every pre-migration agent as the root
// of its own chain (predecessor NULL).

// EnsureLineageTx inserts the root lineage row for an agent that has none
// (legacy or imported rows predate the backfill). It never touches the
// agent row itself: no rename, no deletion, history preserved.
func (s *Store) EnsureLineageTx(ctx context.Context, tx *sql.Tx, a Agent) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_lineage WHERE agent_id = ?`, a.ID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_lineage
		(id, agent_id, session_id, generation, predecessor_agent_id, root_item_id, item_id, role, created_at)
		VALUES (?, ?, NULL, 1, NULL, ?, ?, ?, ?)`,
		ids.New("lin"), a.ID, a.RootItemID, a.ItemID, string(a.Role), db.Millis(s.now()))
	return err
}

// EnsureLineage is EnsureLineageTx outside a caller's transaction.
func (s *Store) EnsureLineage(ctx context.Context, agentID string) error {
	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error { return s.EnsureLineageTx(ctx, tx, a) })
}

// appendLineageTx links one generation to the next: a new session on the
// same canonical agent (replacement successor, orchestrator recovery).
// predecessorAgentID is usually the agent itself; it differs only when a
// new row genuinely continues another agent's assignment.
func (s *Store) appendLineageTx(ctx context.Context, tx *sql.Tx, a Agent, sessionID string, generation int, predecessorAgentID string) error {
	var pred any
	if predecessorAgentID != "" {
		pred = predecessorAgentID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_lineage
		(id, agent_id, session_id, generation, predecessor_agent_id, root_item_id, item_id, role, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ids.New("lin"), a.ID, sessionID, generation, pred, a.RootItemID, a.ItemID, string(a.Role), db.Millis(s.now()))
	return err
}

// LineageNode is one row of an agent's lineage chain.
type LineageNode struct {
	AgentID            string
	SessionID          string
	Generation         int
	PredecessorAgentID string
	RootItemID         string
	ItemID             string
	Role               Role
}

// LineageChain returns the agent's lineage rows oldest first: the root it
// was backfilled (or ensured) with, then one node per later generation.
func (s *Store) LineageChain(ctx context.Context, agentID string) ([]LineageNode, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT agent_id, COALESCE(session_id, ''),
		generation, COALESCE(predecessor_agent_id, ''), root_item_id, item_id, role
		FROM agent_lineage WHERE agent_id = ? ORDER BY generation, created_at, rowid`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LineageNode
	for rows.Next() {
		var n LineageNode
		var role string
		if err := rows.Scan(&n.AgentID, &n.SessionID, &n.Generation,
			&n.PredecessorAgentID, &n.RootItemID, &n.ItemID, &role); err != nil {
			return nil, err
		}
		n.Role = Role(role)
		out = append(out, n)
	}
	return out, rows.Err()
}

// ResolveCanonical resolves the canonical agent for an exact assignment:
// (root item, item, role, parent agent). It is a pure read -- it never
// creates, renames, deletes or merges rows. The sole active match wins;
// with no active match the newest recoverable one (anything but an
// acknowledged or user-cancelled row) is canonical, so crashed or stopped
// generations keep their identity. Two or more live contenders are
// ambiguous: a conflict, because two workers on one task are never the
// same agent and picking one would silently merge them.
func (s *Store) ResolveCanonical(ctx context.Context, rootItemID, itemID string, role Role, parentAgentID string) (Agent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM agents
		WHERE root_item_id = ? AND item_id = ? AND role = ?
		  AND COALESCE(parent_agent_id, '') = COALESCE(?, '')
		ORDER BY created_at DESC, rowid DESC`, rootItemID, itemID, string(role), parentAgentID)
	if err != nil {
		return Agent{}, err
	}
	var agentIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Agent{}, err
		}
		agentIDs = append(agentIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Agent{}, err
	}
	if len(agentIDs) == 0 {
		return Agent{}, &items.Error{Code: items.CodeNotFound, Message: "No agent holds this assignment."}
	}
	var active, recoverable []Agent
	for _, id := range agentIDs {
		a, err := s.agentByID(ctx, id)
		if err != nil {
			return Agent{}, err
		}
		live, err := s.agentLive(ctx, a)
		if err != nil {
			return Agent{}, err
		}
		switch {
		case live:
			active = append(active, a)
		case a.State == AgentAcknowledged || !s.autoRestart(ctx, a.ID):
			// History, or stopped by its owner's explicit Cancel: never canonical.
		default:
			recoverable = append(recoverable, a)
		}
	}
	// agentIDs arrived newest first, so both lists stay newest first.
	if len(active) == 1 {
		return active[0], nil
	}
	if len(active) > 1 {
		return Agent{}, &items.Error{Code: items.CodeConflict,
			Message: "More than one live agent holds this assignment; they were not merged."}
	}
	if len(recoverable) > 0 {
		return recoverable[0], nil
	}
	return Agent{}, &items.Error{Code: items.CodeNotFound, Message: "No agent holds this assignment."}
}

// agentLive is the liveness half of ResolveCanonical: a queued agent is
// waiting for its first session, an active one counts only while its latest
// session is live. A crashed or stopped generation is recoverable, not live.
func (s *Store) agentLive(ctx context.Context, a Agent) (bool, error) {
	if a.State == AgentQueued {
		return true, nil
	}
	if a.State != AgentActive {
		return false, nil
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return false, err
	}
	return ses.State.Live(), nil
}

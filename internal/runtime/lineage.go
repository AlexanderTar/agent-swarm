package runtime

import (
	"context"
	"database/sql"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
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

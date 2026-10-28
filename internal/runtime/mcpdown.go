package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// mcpDownAfter is how long a live Claude session with hook activity may go
// without its shim listing tools before the daemon tells the parent its Swarm
// MCP never came up (TASK-1458: Claude froze at startup, its MCP connect timed
// out, and the session stayed Running without sync or checkpoint). It is one
// minute past ackTimeout so a plain slow start raises no_ack first.
const mcpDownAfter = 3 * time.Minute

// MCPSeen records that sessionID's shim sent this JSON-RPC method to /mcp. Like
// busy.go it is in-memory only; BootedAt keeps a restart from reading as
// "no MCP".
func (s *Store) MCPSeen(sessionID, method string) {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.mcpSeen == nil {
		s.mcpSeen = map[string]map[string]bool{}
	}
	if s.mcpSeen[sessionID] == nil {
		s.mcpSeen[sessionID] = map[string]bool{}
	}
	s.mcpSeen[sessionID][method] = true
}

// MCPSeenMethod reports whether sessionID's shim has sent this method.
func (s *Store) MCPSeenMethod(sessionID, method string) bool {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	return s.mcpSeen[sessionID][method]
}

// checkMCPDown relays "mcp_down" once per session to the parent of a Claude
// session that started at least mcpDownAfter ago, has fired hooks since, and
// whose shim never sent tools/list (Claude lists tools right after it
// connects). Other kinds are left out until their clients are checked. Unlike
// no_ack, which fires on every slow start and gets dismissed, this means the
// agent cannot reach Swarm at all.
func (s *Store) checkMCPDown(ctx context.Context, r liveRow) error {
	if r.Kind != Claude || r.ParentAgentID == "" || s.Now().Sub(r.StartedAt) < mcpDownAfter ||
		r.LastSeenAt == nil || !r.LastSeenAt.After(r.StartedAt) ||
		r.StartedAt.Before(s.BootedAt) || s.MCPSeenMethod(r.SessionID, "tools/list") {
		return nil
	}
	already, err := s.alreadyRelayed(ctx, r.ParentAgentID, r.ItemID, "mcp_down", r.StartedAt)
	if err != nil || already {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.notify(ctx, tx, NotifyInput{Kind: "agent.mcp_down", AgentName: r.AgentName,
			ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}}); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"event": "mcp_down", "agent": r.AgentName, "item": r.ItemKey,
			"next": "Its Swarm tools never connected, so it can't sync or checkpoint. swarm_control retry it."})
		if err != nil {
			return err
		}
		_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: r.ParentAgentID,
			RootItemID: r.RootItemID, ItemID: r.ItemID, Payload: payload})
		return err
	})
}

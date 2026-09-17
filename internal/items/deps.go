package items

import (
	"context"
	"database/sql"
	"sort"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

const (
	CycleMessage        = "This dependency would create a cycle."
	HierarchyDepMessage = "A task can't depend on its own story or epic."
)

type GraphNode struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	RootKey  string `json:"root_key"`
	External bool   `json:"external"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

func (s *Store) AddDep(ctx context.Context, key, blockedBy string, by Actor) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		a, err := s.getTx(ctx, tx, key)
		if err != nil {
			return err
		}
		b, err := s.getTx(ctx, tx, blockedBy)
		if err != nil {
			return err
		}
		if err := s.orchestratorScope(ctx, tx, by, a); err != nil {
			return err
		}
		if a.ID == b.ID {
			return errf(CodeConflict, CycleMessage)
		}
		for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
			up, err := isAncestor(ctx, tx, pair[0], pair[1])
			if err != nil {
				return err
			}
			if up {
				return errf(CodeConflict, HierarchyDepMessage)
			}
		}
		cycle, err := reachable(ctx, tx, b.ID, a.ID)
		if err != nil {
			return err
		}
		if cycle {
			return errf(CodeConflict, CycleMessage)
		}
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_deps (item_id, blocked_by_id, created_at) VALUES (?, ?, ?)`,
			a.ID, b.ID, db.Millis(s.Now()))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return s.changed(ctx, tx, a)
	})
}

func (s *Store) RemoveDep(ctx context.Context, key, blockedBy string, by Actor) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		a, err := s.getTx(ctx, tx, key)
		if err != nil {
			return err
		}
		b, err := s.getTx(ctx, tx, blockedBy)
		if err != nil {
			return err
		}
		if err := s.orchestratorScope(ctx, tx, by, a); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM item_deps WHERE item_id = ? AND blocked_by_id = ?`, a.ID, b.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return s.changed(ctx, tx, a)
	})
}

// isAncestor reports whether anc is a strict ancestor of id.
func isAncestor(ctx context.Context, q querier, id, anc string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `WITH RECURSIVE up(id) AS (
			SELECT parent_id FROM items WHERE id = ? AND parent_id IS NOT NULL
			UNION SELECT i.parent_id FROM items i JOIN up ON i.id = up.id WHERE i.parent_id IS NOT NULL)
		SELECT EXISTS (SELECT 1 FROM up WHERE id = ?)`, id, anc).Scan(&found)
	return found, err
}

// reachable walks blocked_by edges from `from` and reports whether it reaches target.
func reachable(ctx context.Context, q querier, from, target string) (bool, error) {
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		next, err := neighbours(ctx, q, `SELECT blocked_by_id FROM item_deps WHERE item_id = ?`, cur)
		if err != nil {
			return false, err
		}
		for _, n := range next {
			if n == target {
				return true, nil
			}
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return false, nil
}

func neighbours(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) Deps(ctx context.Context, key string) (blockedBy, blocks []Item, err error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return nil, nil, err
	}
	blockedBy, err = s.queryItems(ctx, s.DB, `SELECT `+itemCols+` JOIN item_deps d ON d.blocked_by_id = i.id
		WHERE d.item_id = ? ORDER BY i.created_at`, it.ID)
	if err != nil {
		return nil, nil, err
	}
	blocks, err = s.queryItems(ctx, s.DB, `SELECT `+itemCols+` JOIN item_deps d ON d.item_id = i.id
		WHERE d.blocked_by_id = ? ORDER BY i.created_at`, it.ID)
	return blockedBy, blocks, err
}

// Graph returns the dependency graph around key. scope is "root" (default) or
// "neighbourhood" (hops each way, at least 1).
func (s *Store) Graph(ctx context.Context, key, scope string, hops int) (Graph, error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return Graph{}, err
	}
	ids := map[string]bool{}
	switch scope {
	case "", "root":
		members, err := neighbours(ctx, s.DB, `SELECT id FROM items WHERE root_id = ? AND archived_at IS NULL`, it.RootID)
		if err != nil {
			return Graph{}, err
		}
		for _, id := range members {
			ids[id] = true
		}
		for _, id := range members {
			ext, err := neighbours(ctx, s.DB, `SELECT blocked_by_id FROM item_deps WHERE item_id = ?
				UNION SELECT item_id FROM item_deps WHERE blocked_by_id = ?`, id, id)
			if err != nil {
				return Graph{}, err
			}
			for _, e := range ext {
				ids[e] = true
			}
		}
	case "neighbourhood":
		hops = max(hops, 1)
		ids[it.ID] = true
		frontier := []string{it.ID}
		for range hops {
			var next []string
			for _, id := range frontier {
				ns, err := neighbours(ctx, s.DB, `SELECT blocked_by_id FROM item_deps WHERE item_id = ?
					UNION SELECT item_id FROM item_deps WHERE blocked_by_id = ?`, id, id)
				if err != nil {
					return Graph{}, err
				}
				for _, n := range ns {
					if !ids[n] {
						ids[n] = true
						next = append(next, n)
					}
				}
			}
			frontier = next
		}
	default:
		return Graph{}, errf(CodeBadRequest, "scope must be root or neighbourhood.")
	}

	args := make([]any, 0, len(ids))
	for id := range ids {
		args = append(args, id)
	}
	nodes, err := s.queryItems(ctx, s.DB, `SELECT `+itemCols+` WHERE i.id IN (`+placeholders(len(args))+`)
		ORDER BY i.root_id = ? DESC, i.created_at`, append(args, it.RootID)...)
	if err != nil {
		return Graph{}, err
	}
	g := Graph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	keyOf := map[string]string{}
	for _, n := range nodes {
		keyOf[n.ID] = n.Key
		g.Nodes = append(g.Nodes, GraphNode{Key: n.Key, Type: string(n.Type), Title: n.Title, Status: string(n.Status),
			RootKey: n.RootKey, External: n.RootID != it.RootID})
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT item_id, blocked_by_id FROM item_deps
		WHERE item_id IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return Graph{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return Graph{}, err
		}
		if ids[b] {
			g.Edges = append(g.Edges, GraphEdge{From: keyOf[b], To: keyOf[a]})
		}
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g, rows.Err()
}

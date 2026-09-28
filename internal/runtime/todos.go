package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// TodoStatus is one progress-list entry's status (spec 2026-09-28-orchestrator-todos).
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

type Todo struct {
	ID      string     `json:"id"`    // "TASK-12" or a step id like "spec"
	Label   string     `json:"label"` // "TASK-12 · Add login form" / "Spec approval"
	Status  TodoStatus `json:"status"`
	ItemKey string     `json:"item_key,omitempty"` // task entries only
}

type TodoReport struct {
	ID     string     `json:"id"`
	Status TodoStatus `json:"status"`
}

type TodoProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"` // first in_progress label, else first pending, else ""
}

type todoStep struct{ ID, Label string }

var spikeSteps = map[string][]todoStep{
	"feature": {{"frame", "Frame the request"}, {"research", "Research"}, {"design", "Design"},
		{"spec", "Spec approval"}, {"plan", "Write plan"}, {"critic", "Completeness check"},
		{"approve", "Plan approval + materialize"}},
	"debug": {{"frame", "Frame the problem"}, {"evidence", "Reproduce + gather evidence"},
		{"root_cause", "Root cause"}, {"report", "Debug report approval"}, {"plan", "Plan fix packages"},
		{"critic", "Completeness check"}, {"approve", "Approval + materialize"}},
}

func taskTodoStatus(s items.Status) TodoStatus {
	switch s {
	case items.Done:
		return TodoCompleted
	case items.InProgress, items.InReview, items.AwaitingApproval:
		return TodoInProgress
	}
	return TodoPending
}

// todoQuerier is the read surface *sql.DB and *sql.Tx share; the list is
// read-only, so /api/state reads it without taking the immediate write lock.
type todoQuerier interface {
	txQuerier
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Todos is the root item's progress list; nil when the item has none (not a
// root, or a root type with no list). Callers already inside a transaction
// pass their tx to todosTx instead.
func (s *Store) Todos(ctx context.Context, rootItemID string) ([]Todo, error) {
	return s.todosTx(ctx, s.DB, rootItemID)
}

func (s *Store) todosTx(ctx context.Context, tx todoQuerier, rootItemID string) ([]Todo, error) {
	var typ, status, intent string
	var parent sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT type, status, COALESCE(spike_intent, ''), parent_id FROM items WHERE id = ?`,
		rootItemID).Scan(&typ, &status, &intent, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || parent.Valid {
		return nil, err
	}
	switch items.Type(typ) {
	case items.Spike:
		steps, ok := spikeSteps[intent]
		if !ok {
			return nil, nil
		}
		reports, err := s.latestTodoReports(ctx, tx, rootItemID)
		if err != nil {
			return nil, err
		}
		return s.spikeTodos(ctx, tx, rootItemID, steps, reports)
	case items.Epic, items.Bug, items.Chore:
		return s.taskTodos(ctx, tx, rootItemID, items.Type(typ), items.Status(status))
	}
	return nil, nil
}

func (s *Store) taskTodos(ctx context.Context, tx todoQuerier, rootID string, typ items.Type, rootStatus items.Status) ([]Todo, error) {
	// Tasks directly under the root first (p is NULL), then by story order.
	rows, err := tx.QueryContext(ctx, `SELECT t.key, t.title, t.status FROM items t
		LEFT JOIN items p ON p.id = t.parent_id AND p.id <> t.root_id
		WHERE t.root_id = ? AND t.type = 'task' AND t.status <> 'cancelled'
		ORDER BY p.id IS NOT NULL, p.sort_order, length(p.key), p.key, t.sort_order, length(t.key), t.key`, rootID)
	if err != nil {
		return nil, err
	}
	var out []Todo
	allDone := true
	for rows.Next() {
		var key, title, st string
		if err := rows.Scan(&key, &title, &st); err != nil {
			rows.Close()
			return nil, err
		}
		ts := taskTodoStatus(items.Status(st))
		allDone = allDone && ts == TodoCompleted
		out = append(out, Todo{ID: key, Label: key + " · " + title, Status: ts, ItemKey: key})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var integrated bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'integrated')`,
		rootID).Scan(&integrated); err != nil {
		return nil, err
	}
	integrate := TodoPending
	if integrated {
		integrate = TodoCompleted
	} else if len(out) > 0 && allDone {
		integrate = TodoInProgress
	}
	accept := TodoPending
	if rootStatus == items.Done {
		accept = TodoCompleted
	} else if integrate == TodoCompleted {
		accept = TodoInProgress
	}
	if typ == items.Chore && len(out) == 0 {
		work := TodoInProgress
		if integrate == TodoCompleted {
			work = TodoCompleted
		}
		out = append(out, Todo{ID: "work", Label: "Do the work", Status: work})
	}
	return append(out, Todo{ID: "integrate", Label: "Merge + verify", Status: integrate},
		Todo{ID: "accept", Label: "User acceptance", Status: accept}), nil
}

// latestTodoReports is the newest stored spike step list for itemID, nil if none.
func (s *Store) latestTodoReports(ctx context.Context, tx todoQuerier, itemID string) ([]TodoReport, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT todos_json FROM checkpoints WHERE item_id = ? AND todos_json IS NOT NULL
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, itemID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []TodoReport
	return out, json.Unmarshal([]byte(raw), &out)
}

// spikeTodos lays reports over the template and applies the two facts the
// daemon owns: spec approved, and the spike materialized.
func (s *Store) spikeTodos(ctx context.Context, tx todoQuerier, spikeID string, steps []todoStep, reports []TodoReport) ([]Todo, error) {
	byID := map[string]TodoStatus{}
	for _, r := range reports {
		byID[r.ID] = r.Status
	}
	var specID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE item_id = ? AND kind = 'spec'
		ORDER BY created_at DESC, id DESC LIMIT 1`, spikeID).Scan(&specID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if specID != "" && s.checkEverySectionApproved(ctx, tx, specID) == nil {
		byID["spec"] = TodoCompleted
	}
	var materialized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE origin_spike_id = ?)`,
		spikeID).Scan(&materialized); err != nil {
		return nil, err
	}
	if materialized {
		byID["approve"] = TodoCompleted
	}
	out := make([]Todo, 0, len(steps))
	for _, st := range steps {
		status := byID[st.ID]
		if status == "" {
			status = TodoPending
		}
		out = append(out, Todo{ID: st.ID, Label: st.Label, Status: status})
	}
	return out, nil
}

// ProgressOf summarizes a list for the agents API; nil for an empty list.
func ProgressOf(todos []Todo) *TodoProgress {
	if len(todos) == 0 {
		return nil
	}
	p := &TodoProgress{Total: len(todos)}
	firstPending := ""
	for _, t := range todos {
		switch t.Status {
		case TodoCompleted:
			p.Done++
		case TodoInProgress:
			if p.Current == "" {
				p.Current = t.Label
			}
		default:
			if firstPending == "" {
				firstPending = t.Label
			}
		}
	}
	if p.Current == "" {
		p.Current = firstPending
	}
	return p
}

package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

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

// TodoReport is one step an orchestrator reports: a spike step's status, and for any root an
// optional label naming the step for the work at hand (CHORE-64). An epic, bug or chore root
// sends labels only; its statuses are derived.
type TodoReport struct {
	ID     string     `json:"id"`
	Status TodoStatus `json:"status,omitempty"`
	Label  string     `json:"label,omitempty"`
}

// todoLabels maps step id to the label an orchestrator gave it.
func todoLabels(reports []TodoReport) map[string]string {
	out := map[string]string{}
	for _, r := range reports {
		if r.Label != "" {
			out[r.ID] = r.Label
		}
	}
	return out
}

func checkTodoLabel(r TodoReport) error {
	if n := utf8.RuneCountInString(r.Label); n > 80 || r.Label != "" && strings.TrimSpace(r.Label) == "" {
		return &items.Error{Code: items.CodeBadRequest, Message: "todos: a label must be 1–80 characters"}
	}
	return nil
}

// rootTodoSteps are the fixed step ids of an epic, bug or chore list.
func rootTodoSteps(items.Type) []string {
	return []string{"context", "work", "integrate", "accept"}
}

// mergeRootTodoLabels validates a root orchestrator's labels and returns them merged over the
// newest stored ones, in step order.
func (s *Store) mergeRootTodoLabels(ctx context.Context, tx *sql.Tx, root items.Item, in []TodoReport) ([]TodoReport, error) {
	ids := rootTodoSteps(root.Type)
	for _, r := range in {
		if r.Status != "" || r.Label == "" {
			return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"todo statuses for %s are derived from its tasks; send only labels for: %s", root.Key, strings.Join(ids, ", "))}
		}
		if !slices.Contains(ids, r.ID) {
			return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"todos: unknown step %q for %s; use: %s", r.ID, root.Key, strings.Join(ids, ", "))}
		}
		if err := checkTodoLabel(r); err != nil {
			return nil, err
		}
	}
	prev, err := s.latestTodoReports(ctx, tx, root.ID)
	if err != nil {
		return nil, err
	}
	labels := todoLabels(append(prev, in...))
	var out []TodoReport
	for _, id := range ids {
		if labels[id] != "" {
			out = append(out, TodoReport{ID: id, Label: labels[id]})
		}
	}
	return out, nil
}

type TodoProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"` // first in_progress label, else first pending, else ""
}

type todoStep struct{ ID, Label string }

var spikeSteps = map[string][]todoStep{
	"feature": {{"frame", "Understanding the request"}, {"research", "Researching"}, {"design", "Designing"},
		{"spec", "Reviewing the spec"}, {"plan", "Writing the plan"}, {"critic", "Checking for gaps"},
		{"approve", "Reviewing the plan and setting up tasks"}},
	"debug": {{"frame", "Understanding the problem"}, {"evidence", "Reproducing and gathering evidence"},
		{"root_cause", "Finding the root cause"}, {"report", "Reviewing the findings"}, {"plan", "Planning the fixes"},
		{"critic", "Checking for gaps"}, {"approve", "Reviewing the plan and setting up tasks"}},
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
	{ // CHORE-64: every root, not just a chore, gathers context and may have no tasks.
		var progressed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'progress')`,
			rootID).Scan(&progressed); err != nil {
			return nil, err
		}
		ctxDone := len(out) > 0 || progressed // out = the non-cancelled tasks listed above
		ctxTodo := Todo{ID: "context", Label: "Gathering context", Status: TodoInProgress}
		if ctxDone {
			ctxTodo.Status = TodoCompleted
		}
		if len(out) == 0 {
			work := TodoPending
			switch {
			case integrate == TodoCompleted:
				work = TodoCompleted
			case ctxDone:
				work = TodoInProgress
			}
			out = append(out, Todo{ID: "work", Label: "Making the changes", Status: work})
		}
		out = append([]Todo{ctxTodo}, out...)
	}
	out = append(out, Todo{ID: "integrate", Label: "Merging and verifying", Status: integrate},
		Todo{ID: "accept", Label: "Finishing: PR or merge", Status: accept})
	reports, err := s.latestTodoReports(ctx, tx, rootID)
	if err != nil {
		return nil, err
	}
	labels := todoLabels(reports)
	for i := range out {
		if l := labels[out[i].ID]; l != "" && out[i].ItemKey == "" {
			out[i].Label = l
		}
	}
	return out, nil
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
	labels := todoLabels(reports)
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
		label := st.Label
		if l := labels[st.ID]; l != "" {
			label = l
		}
		out = append(out, Todo{ID: st.ID, Label: label, Status: status})
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

// mergeSpikeTodos validates a spike orchestrator's step report (spec locked
// decision 5, steps 3-4) and returns the full merged list to store, in
// template order. The in_progress count runs on the list the orchestrator
// sees: after the daemon's spec/approve facts.
func (s *Store) mergeSpikeTodos(ctx context.Context, tx *sql.Tx, spike items.Item, in []TodoReport) ([]TodoReport, error) {
	steps := spikeSteps[spike.SpikeIntent]
	ids := make([]string, len(steps))
	for i, st := range steps {
		ids[i] = st.ID
	}
	for _, r := range in {
		if !slices.Contains(ids, r.ID) {
			return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"todos: unknown step %q for a %s spike; use: %s", r.ID, spike.SpikeIntent, strings.Join(ids, ", "))}
		}
		if err := checkTodoLabel(r); err != nil {
			return nil, err
		}
		if r.Status == "" && r.Label != "" {
			continue // a label-only entry keeps the step's status
		}
		if r.Status != TodoPending && r.Status != TodoInProgress && r.Status != TodoCompleted {
			return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"todos: status must be pending, in_progress or completed (got %q)", r.Status)}
		}
	}
	prev, err := s.latestTodoReports(ctx, tx, spike.ID)
	if err != nil {
		return nil, err
	}
	byID := map[string]TodoStatus{}
	for _, r := range append(prev, in...) {
		if r.Status != "" {
			byID[r.ID] = r.Status
		}
	}
	labels := todoLabels(append(prev, in...))
	merged := make([]TodoReport, 0, len(steps))
	for _, id := range ids {
		st := byID[id]
		if st == "" {
			st = TodoPending
		}
		merged = append(merged, TodoReport{ID: id, Status: st, Label: labels[id]})
	}
	view, err := s.spikeTodos(ctx, tx, spike.ID, steps, merged)
	if err != nil {
		return nil, err
	}
	var running []string
	for _, t := range view {
		if t.Status == TodoInProgress {
			running = append(running, t.ID)
		}
	}
	if len(running) > 1 {
		return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
			"todos: at most one step may be in_progress (%s)", strings.Join(running, ", "))}
	}
	return merged, nil
}

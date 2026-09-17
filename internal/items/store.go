package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

type Store struct {
	DB     *db.DB
	Events *events.Store
	Now    func() time.Time
}

type CreateInput struct {
	Type           Type
	ParentKey      string
	Title          string
	Brief          string
	Acceptance     []string
	Status         Status // "" means Draft; only Draft or Ready
	Priority       *int   // nil means 2
	RoleHint       string
	TddExempt      string
	Repos          []string // top-level: confirmed repo ids; children: hints (subset of the root's)
	SuggestedRepos []string
	SpikeIntent    string
	OriginSpikeID  string
	LegacyKey      string
	SortOrder      int
}

type Patch struct {
	Title      *string
	Brief      *string
	Acceptance *[]string
	Priority   *int
	Status     *Status
	Revision   int
}

var allowedParents = map[Type][]Type{Story: {Epic}, Task: {Story, Bug, Spike}}
var parentHint = map[Type]string{Story: "A story needs a parent epic.", Task: "A task needs a parent story, bug or spike."}
var tddValues = []string{"docs", "config", "mechanical-rename", "spike-research"}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const itemCols = `i.id, i.key, i.type, COALESCE(i.parent_id, ''), COALESCE(p.key, ''), i.root_id, r.key,
 i.title, i.brief, i.acceptance_json, i.status, COALESCE(i.status_before_block, ''), i.priority,
 COALESCE(i.role_hint, ''), COALESCE(i.tdd_exempt, ''), i.confirmed_repos_json, i.repos_version,
 i.repo_hints_json, i.suggested_repos_json, COALESCE(i.spike_intent, ''), COALESCE(i.origin_spike_id, ''),
 COALESCE(i.legacy_key, ''), i.sort_order, i.revision, i.archived_at, i.created_at, i.updated_at
 FROM items i LEFT JOIN items p ON p.id = i.parent_id JOIN items r ON r.id = i.root_id`

type scanner interface{ Scan(dest ...any) error }

func scanItem(sc scanner) (Item, error) {
	var it Item
	var acc, confirmed, hints, suggested string
	var archived sql.NullInt64
	var created, updated int64
	err := sc.Scan(&it.ID, &it.Key, &it.Type, &it.ParentID, &it.ParentKey, &it.RootID, &it.RootKey,
		&it.Title, &it.Brief, &acc, &it.Status, &it.StatusBeforeBlock, &it.Priority,
		&it.RoleHint, &it.TddExempt, &confirmed, &it.ReposVersion,
		&hints, &suggested, &it.SpikeIntent, &it.OriginSpikeID,
		&it.LegacyKey, &it.SortOrder, &it.Revision, &archived, &created, &updated)
	if err != nil {
		return it, err
	}
	reposCol, repos := "repo_hints_json", hints
	if it.ParentID == "" {
		reposCol, repos = "confirmed_repos_json", confirmed
	}
	for _, c := range []struct {
		name, raw string
		dst       *[]string
	}{{"acceptance_json", acc, &it.Acceptance}, {"suggested_repos_json", suggested, &it.SuggestedRepos}, {reposCol, repos, &it.Repos}} {
		if err := json.Unmarshal([]byte(c.raw), c.dst); err != nil {
			return it, fmt.Errorf("items: %s %s: %w", it.Key, c.name, err)
		}
	}
	it.Acceptance = nonNil(it.Acceptance)
	it.Repos = nonNil(it.Repos)
	it.SuggestedRepos = nonNil(it.SuggestedRepos)
	it.BlockedBy = []string{}
	if archived.Valid {
		at := db.FromMillis(archived.Int64)
		it.ArchivedAt = &at
	}
	it.CreatedAt, it.UpdatedAt = db.FromMillis(created), db.FromMillis(updated)
	return it, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func jsonList(s []string) string {
	b, _ := json.Marshal(nonNil(s))
	return string(b)
}

func (s *Store) getTx(ctx context.Context, q querier, key string) (Item, error) {
	it, err := scanItem(q.QueryRowContext(ctx, `SELECT `+itemCols+` WHERE i.key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return it, errf(CodeNotFound, "No item %s.", key)
	}
	return it, err
}

func (s *Store) getByID(ctx context.Context, q querier, id string) (Item, error) {
	it, err := scanItem(q.QueryRowContext(ctx, `SELECT `+itemCols+` WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return it, errf(CodeNotFound, "No item %s.", id)
	}
	return it, err
}

// write runs fn in a transaction and wakes event subscribers after commit.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	err := s.DB.Tx(ctx, fn)
	if err == nil {
		s.Events.Notify()
	}
	return err
}

func (s *Store) changed(ctx context.Context, tx *sql.Tx, it Item) error {
	_, err := s.Events.Append(ctx, tx, events.ItemChanged, map[string]string{"key": it.Key, "root_key": it.RootKey})
	return err
}

func validateText(title, brief string) error {
	if n := utf8.RuneCountInString(title); n < 1 || n > 200 {
		return errf(CodeBadRequest, "Title must be 1–200 characters.")
	}
	if utf8.RuneCountInString(brief) > 600 {
		return errf(CodeBadRequest, "Brief must be at most 600 characters.")
	}
	return nil
}

func validPriority(p int) error {
	if p < 0 || p > 3 {
		return errf(CodeBadRequest, "Priority must be between 0 and 3.")
	}
	return nil
}

func (s *Store) Create(ctx context.Context, in CreateInput, by Actor) (Item, error) {
	var out Item
	err := s.write(ctx, func(tx *sql.Tx) (err error) {
		out, err = s.CreateTx(ctx, tx, in, by)
		return err
	})
	return out, err
}

func (s *Store) CreateTx(ctx context.Context, tx *sql.Tx, in CreateInput, by Actor) (Item, error) {
	in.Title = strings.TrimSpace(in.Title)
	switch in.Type {
	case Epic, Story, Task, Bug, Spike:
	default:
		return Item{}, errf(CodeBadRequest, "Unknown item type %q.", in.Type)
	}
	if err := validateText(in.Title, in.Brief); err != nil {
		return Item{}, err
	}
	prio := 2
	if in.Priority != nil {
		prio = *in.Priority
	}
	if err := validPriority(prio); err != nil {
		return Item{}, err
	}
	if in.Status == "" {
		in.Status = Draft
	}
	if in.Status != Draft && in.Status != Ready {
		return Item{}, errf(CodeBadRequest, "New items start as Draft or Ready.")
	}
	if in.Type == Spike && in.SpikeIntent != "feature" && in.SpikeIntent != "debug" {
		return Item{}, errf(CodeBadRequest, "Spikes start with an intent. Use New spike.")
	}
	if in.TddExempt != "" {
		if !slices.Contains(tddValues, in.TddExempt) {
			return Item{}, errf(CodeBadRequest, "tdd_exempt must be one of docs, config, mechanical-rename, spike-research.")
		}
		if in.Type != Task {
			return Item{}, errf(CodeBadRequest, "Only tasks can be TDD-exempt.")
		}
		if !by.isOrchestrator() && by.Kind != ActorDaemon {
			return Item{}, errf(CodeBadRequest, "Only an orchestrator or a plan can set tdd_exempt.")
		}
	}

	id := ids.New("itm")
	rootID, parentID := id, sql.NullString{}
	if in.ParentKey == "" {
		if by.isOrchestrator() {
			return Item{}, errf(CodeBadRequest, "Orchestrators can only create items inside their own top-level item.")
		}
		if hint, ok := parentHint[in.Type]; ok {
			return Item{}, errf(CodeBadRequest, "%s", hint)
		}
	} else {
		parent, err := s.getTx(ctx, tx, in.ParentKey)
		if err != nil {
			return Item{}, err
		}
		if !slices.Contains(allowedParents[in.Type], parent.Type) {
			return Item{}, errf(CodeBadRequest, "%s can't be a child of %s.",
				capitalize(article(string(in.Type))), article(string(parent.Type)))
		}
		if err := s.orchestratorScope(ctx, tx, by, parent); err != nil {
			return Item{}, err
		}
		rootID, parentID = parent.RootID, sql.NullString{String: parent.ID, Valid: true}
		if len(in.Repos) > 0 {
			root, err := s.getByID(ctx, tx, rootID)
			if err != nil {
				return Item{}, err
			}
			for _, r := range in.Repos {
				if !slices.Contains(root.Repos, r) {
					return Item{}, errf(CodeBadRequest, "%s isn't confirmed for %s.", r, root.Key)
				}
			}
		}
	}

	key, err := ids.NextKey(ctx, tx, string(in.Type))
	if err != nil {
		return Item{}, err
	}
	confirmed, hints, reposVersion := "[]", jsonList(in.Repos), 0
	if !parentID.Valid {
		confirmed, hints = jsonList(in.Repos), "[]"
		if len(in.Repos) > 0 {
			reposVersion = 1
		}
	}
	now := db.Millis(s.Now())
	_, err = tx.ExecContext(ctx, `INSERT INTO items (id, key, type, parent_id, root_id, title, brief, acceptance_json,
		status, priority, role_hint, tdd_exempt, confirmed_repos_json, repos_version, repo_hints_json, spike_intent,
		suggested_repos_json, origin_spike_id, legacy_key, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		id, key, in.Type, parentID, rootID, in.Title, in.Brief, jsonList(in.Acceptance),
		in.Status, prio, in.RoleHint, in.TddExempt, confirmed, reposVersion, hints, in.SpikeIntent,
		jsonList(in.SuggestedRepos), in.OriginSpikeID, in.LegacyKey, in.SortOrder, now, now)
	if err != nil {
		return Item{}, err
	}
	it, err := s.getByID(ctx, tx, id)
	if err != nil {
		return Item{}, err
	}
	if err := s.changed(ctx, tx, it); err != nil {
		return Item{}, err
	}
	if parentID.Valid {
		if err := s.ReconcileTx(ctx, tx, in.ParentKey); err != nil {
			return Item{}, err
		}
	}
	return it, nil
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// orchestratorScope refuses an orchestrator touching another top-level item's tree.
func (s *Store) orchestratorScope(ctx context.Context, q querier, by Actor, it Item) error {
	if !by.isOrchestrator() || by.RootID == it.RootID {
		return nil
	}
	own, err := s.getByID(ctx, q, by.RootID)
	if err != nil {
		return err
	}
	return errf(CodeBadRequest, "%s is outside %s.", it.Key, own.Key)
}

func (s *Store) Get(ctx context.Context, key string) (Item, error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return it, err
	}
	list := []Item{it}
	err = s.enrich(ctx, s.DB, list)
	return list[0], err
}

func (s *Store) Update(ctx context.Context, key string, p Patch, by Actor) (Item, error) {
	var out Item
	err := s.write(ctx, func(tx *sql.Tx) error {
		it, err := s.getTx(ctx, tx, key)
		if err != nil {
			return err
		}
		if err := s.orchestratorScope(ctx, tx, by, it); err != nil {
			return err
		}
		if p.Revision != it.Revision {
			return errf(CodeConflict, StaleRevision)
		}
		if p.Title != nil || p.Brief != nil || p.Acceptance != nil || p.Priority != nil {
			if p.Title != nil {
				it.Title = strings.TrimSpace(*p.Title)
			}
			if p.Brief != nil {
				it.Brief = *p.Brief
			}
			if p.Acceptance != nil {
				it.Acceptance = *p.Acceptance
			}
			if p.Priority != nil {
				it.Priority = *p.Priority
			}
			if err := validateText(it.Title, it.Brief); err != nil {
				return err
			}
			if err := validPriority(it.Priority); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE items SET title = ?, brief = ?, acceptance_json = ?, priority = ?,
				revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`,
				it.Title, it.Brief, jsonList(it.Acceptance), it.Priority, db.Millis(s.Now()), it.ID, p.Revision)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errf(CodeConflict, StaleRevision)
			}
			if err := s.changed(ctx, tx, it); err != nil {
				return err
			}
		}
		if p.Status != nil {
			if _, err := s.TransitionTx(ctx, tx, it.Key, *p.Status, by); err != nil {
				return err
			}
		} else if err := s.ReconcileTx(ctx, tx, it.Key); err != nil {
			return err
		}
		out, err = s.getByID(ctx, tx, it.ID)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, out.Key)
}

// Ancestors returns the chain from the top-level item down to the parent.
func (s *Store) Ancestors(ctx context.Context, key string) ([]Item, error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return nil, err
	}
	var out []Item
	for id := it.ParentID; id != ""; {
		p, err := s.getByID(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		out = append([]Item{p}, out...)
		id = p.ParentID
	}
	return out, nil
}

func (s *Store) Children(ctx context.Context, key string) ([]Item, error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return nil, err
	}
	return s.queryItems(ctx, s.DB, `SELECT `+itemCols+` WHERE i.parent_id = ? AND i.archived_at IS NULL
		ORDER BY i.sort_order, i.created_at`, it.ID)
}

func (s *Store) queryItems(ctx context.Context, q querier, query string, args ...any) ([]Item, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.enrich(ctx, q, out)
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// enrich fills BlockedBy, Progress, ActiveAgents and OpenRequests in place.
func (s *Store) enrich(ctx context.Context, q querier, its []Item) error {
	if len(its) == 0 {
		return nil
	}
	idx := map[string]*Item{}
	args := make([]any, len(its))
	for i := range its {
		idx[its[i].ID] = &its[i]
		args[i] = its[i].ID
	}
	in := placeholders(len(its))
	each := func(query string, fn func(rows *sql.Rows) error) error {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := fn(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	err := each(`SELECT d.item_id, b.key FROM item_deps d JOIN items b ON b.id = d.blocked_by_id
		WHERE b.status NOT IN ('done', 'cancelled') AND d.item_id IN (`+in+`) ORDER BY b.created_at`,
		func(rows *sql.Rows) error {
			var id, key string
			if err := rows.Scan(&id, &key); err != nil {
				return err
			}
			idx[id].BlockedBy = append(idx[id].BlockedBy, key)
			return nil
		})
	if err != nil {
		return err
	}
	err = each(`SELECT parent_id, SUM(status = 'done'), SUM(status <> 'cancelled') FROM items
		WHERE archived_at IS NULL AND parent_id IN (`+in+`) GROUP BY parent_id`,
		func(rows *sql.Rows) error {
			var id string
			var p Progress
			if err := rows.Scan(&id, &p.Done, &p.Total); err != nil {
				return err
			}
			p.Unit = "tasks"
			if idx[id].Type == Epic {
				p.Unit = "stories"
			}
			idx[id].Progress = &p
			return nil
		})
	if err != nil {
		return err
	}
	err = each(`SELECT item_id, COUNT(*) FROM requests WHERE state = 'open' AND item_id IN (`+in+`) GROUP BY item_id`,
		func(rows *sql.Rows) error {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			idx[id].OpenRequests = n
			return nil
		})
	if err != nil {
		return err
	}
	return each(`WITH RECURSIVE sub(top, id) AS (
			SELECT id, id FROM items WHERE id IN (`+in+`)
			UNION ALL SELECT sub.top, c.id FROM items c JOIN sub ON c.parent_id = sub.id)
		SELECT sub.top, COUNT(s.id) FROM sub
		JOIN agents a ON a.item_id = sub.id
		JOIN sessions s ON s.agent_id = a.id
		 AND s.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		GROUP BY sub.top`,
		func(rows *sql.Rows) error {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			idx[id].ActiveAgents = n
			return nil
		})
}

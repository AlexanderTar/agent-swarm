// Package events stores the SSE feed (§7.1) and wakes subscribers after commits.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

const (
	ItemChanged     = "item.changed"
	RequestOpened   = "request.opened"
	RequestResolved = "request.resolved"
	SettingsChanged = "settings.changed"
	CatalogChanged  = "catalog.changed"
	ReposChanged    = "repos.changed"

	AgentChanged        = "agent.changed"
	CheckpointCreated   = "checkpoint.created"
	NotificationCreated = "notification.created"
	UsageChanged        = "usage.changed"
	TerminalOpen        = "terminal.open"
)

type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"created_at"`
}

type Store struct {
	DB   *db.DB
	Now  func() time.Time
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func New(d *db.DB, now func() time.Time) *Store {
	return &Store{DB: d, Now: now, subs: map[chan struct{}]struct{}{}}
}

// Append writes an event inside tx. Call Notify after the commit.
func (s *Store) Append(ctx context.Context, tx *sql.Tx, typ string, payload any) (int64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO events (type, payload_json, created_at) VALUES (?, ?, ?)`,
		typ, string(body), db.Millis(s.Now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Notify wakes every subscriber without blocking.
func (s *Store) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Publish appends one event in its own transaction and notifies.
func (s *Store) Publish(ctx context.Context, typ string, payload any) (int64, error) {
	var seq int64
	err := s.DB.Tx(ctx, func(tx *sql.Tx) (err error) {
		seq, err = s.Append(ctx, tx, typ, payload)
		return err
	})
	if err == nil {
		s.Notify()
	}
	return seq, err
}

func (s *Store) After(ctx context.Context, after int64, limit int) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT seq, type, payload_json, created_at FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.Seq, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Latest is the highest seq ever issued (AUTOINCREMENT never reuses one).
func (s *Store) Latest(ctx context.Context) (int64, error) {
	var seq int64
	err := s.DB.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'events'`).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return seq, err
}

// Expired reports whether events after `after` were pruned (or the cursor
// comes from another database); the client must then reset.
func (s *Store) Expired(ctx context.Context, after int64) (bool, error) {
	if after <= 0 {
		return false, nil
	}
	latest, err := s.Latest(ctx)
	if err != nil {
		return false, err
	}
	if after > latest {
		return true, nil
	}
	var min sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MIN(seq) FROM events`).Scan(&min); err != nil {
		return false, err
	}
	if !min.Valid {
		return after < latest, nil
	}
	return after < min.Int64-1, nil
}

// Prune deletes events older than maxAge (A6: 7 days). It deletes a strict seq
// prefix, the way Expired reasons about the feed: a regressed clock would
// otherwise punch a hole in the middle that no client can detect.
func (s *Store) Prune(ctx context.Context, maxAge time.Duration) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM events WHERE seq < COALESCE(
		(SELECT MIN(seq) FROM events WHERE created_at >= ?), (SELECT MAX(seq) + 1 FROM events))`,
		db.Millis(s.Now().Add(-maxAge)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Subscribe returns a coalescing wake channel and an idempotent cancel.
func (s *Store) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// Package notify raises, renders and stores the §17.5 notifications and
// publishes notification.created for the SSE feed.
package notify

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/notifyrules"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Rule is one §17.5 row: a notification level, title, body template and the
// menubar category it groups under. A re-export of notifyrules.Rule, kept
// here (not moved wholesale) so internal/runtime's own tests can validate
// the Args a call site builds against the real template without importing
// this package back — notify imports runtime for NotifyInput, so the
// reverse would cycle. notifyrules is the leaf the data actually lives in;
// see its doc comment.
type Rule = notifyrules.Rule

// Rules is the literal §17.5 table, re-exported from notifyrules.
var Rules = notifyrules.Rules

// dedupWindow is how long a repeat of the same dedup_key is suppressed.
const dedupWindow = 30 * time.Second

// placeholderRe finds every {name} in a template, for substitution in Render.
var placeholderRe = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9 _-]*)\}`)

// Render substitutes args into kind's template and fails on a placeholder
// with no argument, so a missed key is a test failure rather than a literal
// {name} in a banner.
func Render(kind string, args map[string]string) (Rule, error) {
	r, ok := Rules[kind]
	if !ok {
		return Rule{}, fmt.Errorf("notify: unknown kind %q", kind)
	}
	var missing []string
	body := placeholderRe.ReplaceAllStringFunc(r.Body, func(m string) string {
		key := m[1 : len(m)-1]
		v, ok := args[key]
		if !ok {
			missing = append(missing, key)
			return m
		}
		return v
	})
	if len(missing) > 0 {
		return Rule{}, fmt.Errorf("notify: %s is missing %s", kind, strings.Join(missing, ", "))
	}
	r.Body = body
	return r, nil
}

// Notification is one row of the notifications table, for List.
type Notification struct {
	ID, Level, Kind, Title, Body  string
	AgentName, ItemKey, RequestID string
	ReadAt                        *time.Time
	CreatedAt                     time.Time
}

// Service raises, lists and resolves notifications. It implements
// runtime.Notifier.
type Service struct {
	DB     *db.DB
	Events *events.Store
	Now    func() time.Time
	Log    func(format string, args ...any)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// querier is the read/write surface both *sql.DB and *sql.Tx share.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Raise writes one notification, deduplicated within 30 s, and publishes
// notification.created. tx may be nil, in which case it opens its own.
func (s *Service) Raise(ctx context.Context, tx *sql.Tx, in runtime.NotifyInput) error {
	if tx != nil {
		return s.raise(ctx, tx, in)
	}
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error { return s.raise(ctx, tx, in) })
	if err == nil {
		s.Events.Notify()
	}
	return err
}

func (s *Service) raise(ctx context.Context, q querier, in runtime.NotifyInput) error {
	r, err := Render(in.Kind, in.Args)
	if err != nil {
		return err
	}
	kind := in.Kind
	if strings.HasPrefix(kind, "item.created.") {
		kind = "item.created"
	}
	dedupKey := fmt.Sprintf("%s:%s:%s", in.Kind, firstNonEmpty(in.AgentName, in.ItemKey), in.RequestID)
	now := s.now()
	var recent int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
		WHERE dedup_key = ? AND created_at >= ?`, dedupKey, db.Millis(now.Add(-dedupWindow))).Scan(&recent); err != nil {
		return err
	}
	if recent > 0 {
		s.logf("notify: deduped %s within %s", dedupKey, dedupWindow)
		return nil
	}
	agentID := s.lookupID(ctx, q, "agents", "name", in.AgentName)
	itemID := s.lookupID(ctx, q, "items", "key", in.ItemKey)
	id := ids.New("ntf")
	if _, err := q.ExecContext(ctx, `INSERT INTO notifications
		(id, level, kind, title, body, agent_id, item_id, request_id, dedup_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.Level, kind, r.Title, r.Body, agentID, itemID, nullIf(in.RequestID), dedupKey,
		db.Millis(now)); err != nil {
		return err
	}
	payload := map[string]any{
		"id": id, "level": r.Level, "kind": kind, "title": r.Title, "body": r.Body,
		"agent_name": in.AgentName, "item_key": in.ItemKey, "request_id": in.RequestID,
		"read_at": nil, "created_at": db.Millis(now),
	}
	if tx, ok := q.(*sql.Tx); ok {
		_, err := s.Events.Append(ctx, tx, events.NotificationCreated, payload)
		return err
	}
	return nil
}

func nullIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// lookupID resolves name/key to an id for a nullable FK column; an unknown
// name (or an empty one, e.g. a request-only notification) stores NULL
// rather than a value the foreign key would reject.
func (s *Service) lookupID(ctx context.Context, q querier, table, col, value string) any {
	if value == "" {
		return nil
	}
	var id string
	if err := q.QueryRowContext(ctx, fmt.Sprintf(`SELECT id FROM %s WHERE %s = ?`, table, col),
		value).Scan(&id); err != nil {
		return nil
	}
	return id
}

// List returns notifications, most recent first, optionally only the unread.
func (s *Service) List(ctx context.Context, unreadOnly bool, limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT n.id, n.level, n.kind, n.title, n.body, COALESCE(a.name, ''), COALESCE(i.key, ''),
		COALESCE(n.request_id, ''), n.read_at, n.created_at
		FROM notifications n LEFT JOIN agents a ON a.id = n.agent_id LEFT JOIN items i ON i.id = n.item_id`
	if unreadOnly {
		query += ` WHERE n.read_at IS NULL`
	}
	query += ` ORDER BY n.created_at DESC, n.id DESC LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var readAt sql.NullInt64
		var created int64
		if err := rows.Scan(&n.ID, &n.Level, &n.Kind, &n.Title, &n.Body, &n.AgentName, &n.ItemKey,
			&n.RequestID, &readAt, &created); err != nil {
			return nil, err
		}
		if readAt.Valid {
			t := db.FromMillis(readAt.Int64)
			n.ReadAt = &t
		}
		n.CreatedAt = db.FromMillis(created)
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkRead marks one notification read.
func (s *Service) MarkRead(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read_at = ?
		WHERE id = ? AND read_at IS NULL`, db.Millis(s.now()), id)
	return err
}

// ReadAll marks every unread notification read and returns how many changed.
func (s *Service) ReadAll(ctx context.Context) (int, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE read_at IS NULL`,
		db.Millis(s.now()))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Unread is the count of unread notifications, for a menubar badge.
func (s *Service) Unread(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE read_at IS NULL`).Scan(&n)
	return n, err
}

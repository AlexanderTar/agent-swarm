package migrate

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema/v1_schema.sql
var v1Schema string

// V1SchemaSQL is the Agent Swarm 1.x schema at version 4, verified against the live
// database on 2026-09-18. Tests build fixtures from it; Task 20 deletes the
// TypeScript that used to be its only record.
func V1SchemaSQL() string { return v1Schema }

// v1TimeLayout is what SQLite's datetime('now') writes, in UTC.
const v1TimeLayout = "2006-01-02 15:04:05"

// V1DSN is the read DSN. S-8: mode=ro reads the -wal file; immutable=1 does not,
// and on 2026-09-18 it reported 14 of SW-673's 36 children.
func V1DSN(path string) string { return "file:" + path + "?mode=ro&_txlock=deferred" }

func OpenV1(path string) (*sql.DB, error) { return sql.Open("sqlite", V1DSN(path)) }

func V1Version(ctx context.Context, d *sql.DB) (int, error) {
	var v int
	err := d.QueryRowContext(ctx, `SELECT version FROM schema_meta LIMIT 1`).Scan(&v)
	return v, err
}

// V1Task is the subset of the v1 `tasks` row the importer reads.
type V1Task struct {
	Key            string
	Title          string
	Status         string
	RepoPath       string
	InitialContext string
	HandoffNote    string
	ParentKey      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ReadV1 loads the named keys. A key that is missing is an error naming every
// missing key: §20's tree is fixed, so a hole in it must stop the migration.
func ReadV1(ctx context.Context, d *sql.DB, keys []string) (map[string]V1Task, error) {
	if len(keys) == 0 {
		return map[string]V1Task{}, nil
	}
	holders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := d.QueryContext(ctx, `
SELECT t.key, t.title, t.status, COALESCE(t.repo_path, ''), COALESCE(t.initial_context, ''),
       COALESCE(t.handoff_note, ''), COALESCE(p.key, ''), t.created_at, t.updated_at
  FROM tasks t LEFT JOIN tasks p ON p.id = t.parent_task_id
 WHERE t.key IN (`+holders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]V1Task{}
	for rows.Next() {
		var t V1Task
		var created, updated string
		if err := rows.Scan(&t.Key, &t.Title, &t.Status, &t.RepoPath, &t.InitialContext,
			&t.HandoffNote, &t.ParentKey, &created, &updated); err != nil {
			return nil, err
		}
		if t.CreatedAt, err = time.ParseInLocation(v1TimeLayout, created, time.UTC); err != nil {
			return nil, fmt.Errorf("%s created_at %q: %w", t.Key, created, err)
		}
		if t.UpdatedAt, err = time.ParseInLocation(v1TimeLayout, updated, time.UTC); err != nil {
			return nil, fmt.Errorf("%s updated_at %q: %w", t.Key, updated, err)
		}
		out[t.Key] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing []string
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("these keys are in the migration table but not in the database: %s",
			strings.Join(missing, ", "))
	}
	return out, nil
}

// statusMap is §20's table. review and archived are deliberately absent: archived
// rows are never imported, and review has no mapping, so it fails loudly.
var statusMap = map[string]string{
	"ready":       "ready",
	"in_progress": "ready", // A7
	"blocked":     "blocked",
	"backlog":     "draft",
	"done":        "done",
}

func MapStatus(v1 string) (string, error) {
	if s, ok := statusMap[v1]; ok {
		return s, nil
	}
	return "", fmt.Errorf("Agent Swarm 1.x status %q has no mapping in the migration table; "+
		"move the item to ready, blocked, backlog or done and run swarm migrate again", v1)
}

// BriefMax is items.brief's CHECK (length(brief) <= 600). SQLite length() counts
// characters, so the cap is applied in runes.
const BriefMax = 600

// Brief is §20's rule: the first 600 characters of initial_context, or of
// handoff_note when the context is empty.
func Brief(context, handoff string) string {
	s := strings.TrimSpace(context)
	if s == "" {
		s = strings.TrimSpace(handoff)
	}
	r := []rune(s)
	if len(r) > BriefMax {
		return string(r[:BriefMax])
	}
	return string(r)
}

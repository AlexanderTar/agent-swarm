// Package db opens the swarm SQLite database and applies embedded migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 3

var (
	ErrLegacy = errors.New("Agent Swarm 1.x data found. Run `swarm migrate` first.")
	ErrTooNew = errors.New("database is newer than this build")
)

//go:embed schema/*.sql
var schemaFS embed.FS

type DB struct{ *sql.DB }

// Open opens (creating if needed) and migrates the database at path.
// _txlock=immediate makes every transaction take the write lock up front, so
// concurrent writers wait on busy_timeout instead of failing on lock upgrade.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)&_txlock=immediate"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d := &DB{raw}
	if err := d.migrate(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	// items has a TEXT primary key, so its rowid can change on VACUUM; rebuild keeps FTS aligned.
	if _, err := d.ExecContext(ctx, `INSERT INTO items_fts(items_fts) VALUES ('rebuild')`); err != nil {
		raw.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) migrate(ctx context.Context) error {
	var legacy int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_meta'`).Scan(&legacy); err != nil {
		return err
	}
	if legacy > 0 {
		return ErrLegacy
	}
	files, err := fs.Glob(schemaFS, "schema/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	var version int
	if err := d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(files) {
		return ErrTooNew
	}
	if version == len(files) {
		return nil
	}

	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Disable foreign keys on this connection during migration so table recreations
	// (e.g. 0003_add_chore.sql) can drop and rename tables without foreign key violations.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")

	for i := version; i < len(files); i++ {
		body, err := schemaFS.ReadFile(files[i])
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", files[i], err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx runs fn in a transaction, committing if it returns nil.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Millis converts t to ms since the epoch; the zero time maps to 0.
func Millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// FromMillis is the inverse of Millis, returning UTC.
func FromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

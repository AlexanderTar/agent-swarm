package runtime

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	sqlite "modernc.org/sqlite"
)

// errInjected is what the fault driver returns for the statement it was told
// to fail.
var errInjected = errors.New("injected sqlite fault")

// faultCtl counts every statement (Exec, Query, Begin) issued on a store's
// connections and fails exactly the failAt-th one. failAt 0 never fails.
type faultCtl struct {
	mu     sync.Mutex
	n      int
	failAt int
	fired  bool
	stmt   string // the statement that was failed, for diagnostics
}

func (c *faultCtl) hit(q string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	if c.failAt > 0 && c.n == c.failAt {
		c.fired = true
		c.stmt = q
		return errInjected
	}
	return nil
}

func (c *faultCtl) disarm() {
	c.mu.Lock()
	c.failAt = 0
	c.mu.Unlock()
}

type faultConnector struct {
	dsn string
	ctl *faultCtl
}

func (f *faultConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := (&sqlite.Driver{}).Open(f.dsn)
	if err != nil {
		return nil, err
	}
	return &faultConn{Conn: inner, ctl: f.ctl}, nil
}

func (f *faultConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type faultConn struct {
	driver.Conn
	ctl *faultCtl
}

func (c *faultConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}

func (c *faultConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if err := c.ctl.hit("BEGIN"); err != nil {
		return nil, err
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}

func (c *faultConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.ctl.hit(q); err != nil {
		return nil, err
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (c *faultConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.ctl.hit(q); err != nil {
		return nil, err
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

// armFaults repoints s.DB at the same database file through the fault driver
// and returns its controller. Only runtime's own statements (and the ones it
// issues on its transactions) are counted; the other components keep their
// original pool and are untouched.
func armFaults(t *testing.T, s *Store) *faultCtl {
	t.Helper()
	var path string
	var seq int
	var name string
	rows, err := s.DB.QueryContext(context.Background(), `PRAGMA database_list`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if name == "main" {
			path = file
		}
	}
	rows.Close()
	if path == "" {
		t.Fatal("no main database file")
	}
	ctl := &faultCtl{}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)&_txlock=immediate"
	raw := sql.OpenDB(&faultConnector{dsn: dsn, ctl: ctl})
	t.Cleanup(func() { raw.Close() })
	s.DB = &db.DB{DB: raw}
	return ctl
}

// faultCase is one operation to sweep: setup builds a fresh store and
// returns the op to fault plus an invariant that must hold afterwards,
// whether or not the op succeeded.
type faultCase func(t *testing.T) (s *Store, op func(ctx context.Context) error, invariant func(ctx context.Context) error)

// sweepFaults runs the case once per statement index n: the n-th SQL
// statement the op issues fails. Every faulted run must neither panic nor
// leave the invariant broken, the same op must still succeed once the fault
// is gone... and the first n past the op's last statement must run clean,
// proving the sweep covered every statement. It returns how many faulted runs
// surfaced the injected error.
func sweepFaults(t *testing.T, maxN int, mk faultCase) (surfaced int) {
	t.Helper()
	for n := 1; n <= maxN; n++ {
		s, op, invariant := mk(t)
		ctl := armFaults(t, s)
		ctl.failAt = n
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("fault at statement %d panicked: %v", n, r)
				}
			}()
			return op(context.Background())
		}()
		ctl.disarm()
		if !ctl.fired {
			if err != nil {
				t.Fatalf("op failed with no fault injected after %d statements: %v", ctl.n, err)
			}
			if err := invariant(context.Background()); err != nil {
				t.Fatalf("invariant after a clean run: %v", err)
			}
			return surfaced
		}
		if err != nil && strings.Contains(err.Error(), errInjected.Error()) {
			surfaced++
		}
		if ierr := invariant(context.Background()); ierr != nil {
			t.Fatalf("fault at statement %d (%.80q) left the store inconsistent: %v (op err: %v)", n, ctl.stmt, ierr, err)
		}
	}
	t.Fatalf("op still issuing statements after %d faults; raise maxN", maxN)
	return 0
}

// wantCount is an invariant helper: query returns a single integer that must
// equal want.
func wantCount(s *Store, query string, want int, args ...any) error {
	var n int
	if err := s.DB.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("%s = %d, want %d", query, n, want)
	}
	return nil
}

// fkClean is the baseline invariant: no dangling foreign keys, whatever a
// faulted operation managed to commit.
func fkClean(s *Store) error {
	rows, err := s.DB.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowid, fkid sql.NullInt64
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		return fmt.Errorf("dangling foreign key in %s -> %s", table, parent)
	}
	return rows.Err()
}

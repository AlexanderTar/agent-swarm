// The continuity maintenance command (§5 agent continuity): inspect an
// agent's canonical identity and generation history straight from the local
// database, and backfill lineage roots for rows that predate them.
//
// It is DB-direct on purpose: it must work when the daemon is down (that is
// exactly when lineage questions come up), so it follows the local-command
// convention (flags + parse, no daemon client) rather than the API-client
// one the live-state commands use.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func cmdLineage(args []string, stdout, stderr io.Writer) int {
	fs, home, _ := flags("lineage", stderr, false)
	repair := fs.Bool("repair", false, "backfill missing lineage roots and change nothing else")
	if code, done := parse(fs, args); done {
		return code
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "swarm: lineage needs exactly one agent NAME")
		return 2
	}
	name := rest[0]
	path := filepath.Join(*home, "swarm.db")
	if _, err := os.Stat(path); err != nil {
		return fail(stderr, fmt.Errorf("no swarm database at %s", path))
	}
	d, err := db.Open(context.Background(), path)
	if err != nil {
		return fail(stderr, err)
	}
	defer d.Close()
	ctx := context.Background()

	if *repair {
		n, err := backfillLineageRoots(ctx, d.DB)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "Backfilled %d lineage root(s).\n", n)
	}
	return printLineage(ctx, d.DB, stdout, name)
}

// backfillLineageRoots inserts one root lineage row per agent that has none
// -- the same shape migration 0014 backfilled, for rows that arrived
// without one. It never touches agent rows.
func backfillLineageRoots(ctx context.Context, d *sql.DB) (int, error) {
	res, err := d.ExecContext(ctx, `INSERT INTO agent_lineage
		(id, agent_id, session_id, generation, predecessor_agent_id, root_item_id, item_id, role, created_at)
		SELECT 'lin_' || a.id || '_repair', a.id, NULL, 1, NULL,
			a.root_item_id, a.item_id, a.role, CAST(strftime('%s', 'now') AS INTEGER) * 1000
		FROM agents a WHERE NOT EXISTS (SELECT 1 FROM agent_lineage l WHERE l.agent_id = a.id)`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

type lineageRow struct {
	generation  int
	sessionID   string
	predecessor string
}

// printLineage resolves the canonical agent for the named agent's exact
// assignment (sole live holder, else newest recoverable -- never a merge)
// and prints its generation history plus any in-flight operation.
func printLineage(ctx context.Context, d *sql.DB, stdout io.Writer, name string) int {
	var a struct {
		id, kind, role, itemID, rootID, parent, state string
	}
	err := d.QueryRowContext(ctx, `SELECT id, kind, role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), state FROM agents WHERE name = ?`, name).Scan(
		&a.id, &a.kind, &a.role, &a.itemID, &a.rootID, &a.parent, &a.state)
	if err == sql.ErrNoRows {
		fmt.Fprintf(stdout, "No agent %s.\n", name)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stdout, "lineage: %v\n", err)
		return 1
	}
	canonical, ambiguous, err := resolveCanonicalName(ctx, d, a.rootID, a.itemID, a.role, a.parent)
	if err != nil {
		fmt.Fprintf(stdout, "lineage: %v\n", err)
		return 1
	}
	var itemKey, rootKey string
	_ = d.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.itemID).Scan(&itemKey)
	_ = d.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.rootID).Scan(&rootKey)
	fmt.Fprintf(stdout, "Agent %s (%s)\n", name, a.id)
	fmt.Fprintf(stdout, "  assignment: %s on %s (root %s)\n", a.role, itemKey, rootKey)
	if ambiguous {
		fmt.Fprintf(stdout, "  canonical: ambiguous (more than one live holder; not merged)\n")
	} else {
		fmt.Fprintf(stdout, "  canonical: %s\n", canonical)
	}
	rows, err := d.QueryContext(ctx, `SELECT generation, COALESCE(session_id, ''),
		COALESCE(predecessor_agent_id, '') FROM agent_lineage
		WHERE agent_id = ? ORDER BY generation, created_at, rowid`, a.id)
	if err != nil {
		fmt.Fprintf(stdout, "lineage: %v\n", err)
		return 1
	}
	var chain []lineageRow
	for rows.Next() {
		var r lineageRow
		if err := rows.Scan(&r.generation, &r.sessionID, &r.predecessor); err != nil {
			rows.Close()
			fmt.Fprintf(stdout, "lineage: %v\n", err)
			return 1
		}
		chain = append(chain, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fmt.Fprintf(stdout, "lineage: %v\n", err)
		return 1
	}
	if len(chain) == 0 {
		fmt.Fprintf(stdout, "  generations: none recorded (run: swarm lineage %s --repair)\n", name)
	} else {
		fmt.Fprintf(stdout, "  generations: %d\n", len(chain))
		for _, r := range chain {
			fmt.Fprintf(stdout, "    gen %d session %s predecessor %s\n",
				r.generation, dash(r.sessionID), dash(r.predecessor))
		}
	}
	var opID, mode, phase string
	err = d.QueryRowContext(ctx, `SELECT id, mode, phase FROM agent_operations WHERE agent_id = ? AND phase IN
		('requested', 'preserving', 'stopping', 'ready', 'queued', 'starting')
		ORDER BY updated_at DESC LIMIT 1`, a.id).Scan(&opID, &mode, &phase)
	if err == nil {
		fmt.Fprintf(stdout, "  pending operation: %s %s %s\n", opID, mode, phase)
	} else if err != sql.ErrNoRows {
		fmt.Fprintf(stdout, "lineage: %v\n", err)
		return 1
	} else {
		fmt.Fprintf(stdout, "  pending operation: none\n")
	}
	return 0
}

// resolveCanonicalName is printLineage's read-only canonical resolution:
// the sole live holder of the exact (root, item, role, parent) assignment,
// else the newest recoverable row. ambiguous is true when more than one
// live holder exists (a conflict, never a merge).
func resolveCanonicalName(ctx context.Context, d *sql.DB, rootID, itemID, role, parent string) (canonical string, ambiguous bool, err error) {
	rows, err := d.QueryContext(ctx, `SELECT id FROM agents
		WHERE root_item_id = ? AND item_id = ? AND role = ?
		  AND COALESCE(parent_agent_id, '') = ? ORDER BY created_at DESC, rowid DESC`,
		rootID, itemID, role, parent)
	if err != nil {
		return "", false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", false, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	var live, recoverable []string
	for _, id := range ids {
		var state string
		if err := d.QueryRowContext(ctx, `SELECT state FROM agents WHERE id = ?`, id).Scan(&state); err != nil {
			return "", false, err
		}
		if state == "queued" {
			live = append(live, id)
			continue
		}
		if state == "active" {
			var sesState string
			err := d.QueryRowContext(ctx, `SELECT state FROM sessions WHERE agent_id = ?
				ORDER BY generation DESC, attempt DESC LIMIT 1`, id).Scan(&sesState)
			if err == nil && isLiveSessionState(sesState) {
				live = append(live, id)
				continue
			}
		}
		if state == "acknowledged" {
			continue
		}
		var auto sql.NullInt64
		if err := d.QueryRowContext(ctx, `SELECT auto_restart FROM agents WHERE id = ?`, id).Scan(&auto); err == nil && auto.Valid && auto.Int64 == 0 {
			continue
		}
		recoverable = append(recoverable, id)
	}
	if len(live) == 1 {
		return live[0], false, nil
	}
	if len(live) > 1 {
		return "", true, nil
	}
	if len(recoverable) > 0 {
		return recoverable[0], false, nil
	}
	return "", false, fmt.Errorf("no agent holds this assignment")
}

func isLiveSessionState(state string) bool {
	switch state {
	case "spawning", "running", "pause_requested", "quiescing", "stopping":
		return true
	}
	return false
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

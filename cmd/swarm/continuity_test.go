package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// seedLineageDB opens a migrated database at home and inserts one epic with
// an orchestrator that has no lineage row (as if it arrived without one).
func seedLineageDB(t *testing.T, home string) {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'epic one', 'in_progress', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'epic-one-orchestrator', 'fake', 'fake-1', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM agent_lineage`); err != nil {
		t.Fatal(err)
	}
}

func TestLineageShowsAgent(t *testing.T) {
	home := t.TempDir()
	seedLineageDB(t, home)
	var out bytes.Buffer
	if code := run([]string{"lineage", "--home", home, "epic-one-orchestrator"}, &out, &out); code != 0 {
		t.Fatalf("exit = %d, output = %q", code, out.String())
	}
	body := out.String()
	for _, want := range []string{"epic-one-orchestrator", "agt_1", "orchestrator", "EPIC-1", "pending operation: none"} {
		if !strings.Contains(body, want) {
			t.Fatalf("output missing %q:\n%s", want, body)
		}
	}
}

func TestLineageRepairBackfillsRoots(t *testing.T) {
	home := t.TempDir()
	seedLineageDB(t, home)
	var out bytes.Buffer
	if code := run([]string{"lineage", "--home", home, "--repair", "epic-one-orchestrator"}, &out, &out); code != 0 {
		t.Fatalf("exit = %d, output = %q", code, out.String())
	}
	if !strings.Contains(out.String(), "Backfilled 1 lineage root(s).") {
		t.Fatalf("repair output = %q", out.String())
	}
	// Second run: idempotent, and the agent row itself is untouched.
	out.Reset()
	if code := run([]string{"lineage", "--home", home, "--repair", "epic-one-orchestrator"}, &out, &out); code != 0 {
		t.Fatalf("exit = %d, output = %q", code, out.String())
	}
	if !strings.Contains(out.String(), "Backfilled 0 lineage root(s).") {
		t.Fatalf("second repair output = %q", out.String())
	}
	if !strings.Contains(out.String(), "generations: 1") {
		t.Fatalf("chain output = %q", out.String())
	}
}

func TestLineageUnknownAgent(t *testing.T) {
	home := t.TempDir()
	seedLineageDB(t, home)
	var out bytes.Buffer
	if code := run([]string{"lineage", "--home", home, "nobody"}, &out, &out); code != 1 {
		t.Fatalf("exit = %d, want 1; output = %q", code, out.String())
	}
}

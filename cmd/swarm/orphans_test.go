package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func TestSessionLookupReadsAgentAndState(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at) VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'e', 'in_progress', 1, 2)`,
		`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at) VALUES ('agt_1', 'my-coder', 'fake', 'fake-1', 'coder', 'itm_1', 'itm_1', 'b', 'active', 3)`,
		`INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at) VALUES ('ses_1', 'agt_1', 1, 1, 't', 't', '/x', 'crashed', 'neutral', 3)`,
	} {
		if _, err := d.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	look := sessionLookup(home)
	if ref, ok := look(ctx, "ses_1"); !ok || ref.Agent != "my-coder" || ref.State != "crashed" {
		t.Fatalf("ref = %+v, ok = %v", ref, ok)
	}
	if _, ok := look(ctx, "ses_nope"); ok {
		t.Fatal("unknown session found")
	}
	if _, ok := sessionLookup(t.TempDir())(ctx, "ses_1"); ok {
		t.Fatal("missing database found a session")
	}
}

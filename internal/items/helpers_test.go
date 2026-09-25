package items_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

var ctx = context.Background()

// tick is a clock that moves 1 ms on every call, so created_at orders writes.
type tick struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tick) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

func newStore(t *testing.T) *items.Store {
	t.Helper()
	d := dbtest.Open(t)
	c := &tick{t: time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)}
	return &items.Store{DB: d, Events: events.New(d, c.now), Now: c.now}
}

var user = items.User("board")

// mk creates an item (spikes get intent "feature"); parent may be "".
func mk(t *testing.T, s *items.Store, typ items.Type, parent, title string) items.Item {
	t.Helper()
	in := items.CreateInput{Type: typ, ParentKey: parent, Title: title}
	if typ == items.Spike {
		in.SpikeIntent = "feature"
	}
	it, err := s.Create(ctx, in, items.Daemon())
	if err != nil {
		t.Fatalf("create %s under %q: %v", typ, parent, err)
	}
	return it
}

func mustGet(t *testing.T, s *items.Store, key string) items.Item {
	t.Helper()
	it, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func exec(t *testing.T, d *db.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// seedSession creates an agent on item (in root) and a session in state; it returns the ids.
func seedSession(t *testing.T, d *db.DB, it items.Item, state string) (agentID, sessionID string) {
	t.Helper()
	agentID, sessionID = ids.New("agt"), ids.New("ses")
	exec(t, d, `INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES (?, ?, 'fake', 'm', 'coder', ?, ?, 'b', 'active', 1)`, agentID, agentID, it.ID, it.RootID)
	exec(t, d, `INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 1, ?, 'x', '/tmp', ?, 'neutral', 1)`, sessionID, agentID, sessionID, state)
	return agentID, sessionID
}

// seedCheckpoint writes a checkpoint row for it; at is ms and must increase across calls.
func seedCheckpoint(t *testing.T, d *db.DB, it items.Item, kind string, attempt int, at int64, gitJSON string) string {
	t.Helper()
	agentID, sessionID := seedSession(t, d, it, "running")
	id := ids.New("ckp")
	if gitJSON == "" {
		gitJSON = "[]"
	}
	exec(t, d, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, git_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'did it', ?, ?)`, id, sessionID, agentID, it.ID, kind, attempt, gitJSON, at)
	return id
}

// seedSessionRole is seedSession with a caller-chosen role, for tests that
// need more than one agent role active on the same item (e.g. completedCurrent's
// gated-roles-only rule, spec B5).
func seedSessionRole(t *testing.T, d *db.DB, it items.Item, role, state string) (agentID, sessionID string) {
	t.Helper()
	agentID, sessionID = ids.New("agt"), ids.New("ses")
	exec(t, d, `INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES (?, ?, 'fake', 'm', ?, ?, ?, 'b', 'active', 1)`, agentID, agentID, role, it.ID, it.RootID)
	exec(t, d, `INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 1, ?, 'x', '/tmp', ?, 'neutral', 1)`, sessionID, agentID, sessionID, state)
	return agentID, sessionID
}

// seedCheckpointFor writes a checkpoint row for an already-seeded (agentID,
// sessionID) pair, so a test can post several checkpoints from the SAME
// agent (seedCheckpoint always mints a fresh agent per call).
func seedCheckpointFor(t *testing.T, d *db.DB, it items.Item, agentID, sessionID, kind string, attempt int, at int64) string {
	t.Helper()
	id := ids.New("ckp")
	exec(t, d, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, git_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'did it', '[]', ?)`, id, sessionID, agentID, it.ID, kind, attempt, at)
	return id
}

// setWorkflowJSON marks it as a workflow task (a non-NULL workflow_json is
// THE legacy/workflow discriminator -- steps/units/verify may be '[]' either
// way). The content doesn't matter to completedCurrent, only its presence.
func setWorkflowJSON(t *testing.T, d *db.DB, it items.Item) {
	t.Helper()
	exec(t, d, `UPDATE items SET workflow_json = ? WHERE id = ?`,
		`{"steps":[{"id":"build","run":"coder"},{"id":"review","review":["reviewer"],"of":"build"}]}`, it.ID)
}

// seedRequest writes a request row and returns its id.
func seedRequest(t *testing.T, d *db.DB, it items.Item, kind, state string) string {
	t.Helper()
	id := ids.New("req")
	exec(t, d, `INSERT INTO requests (id, kind, item_id, prompt, state, created_at) VALUES (?, ?, ?, 'p', ?, 1)`, id, kind, it.ID, state)
	return id
}

// later returns a timestamp after every write the tick clock has made so far.
func later(s *items.Store) int64 { return db.Millis(s.Now()) }

// insertIntegrated writes the integrated checkpoint reconcileRoot needs, with an
// agent and session row so the foreign keys hold. created_at is far in the future
// so rootState's `ckpAt < lastChild` guard passes whatever the tick clock did.
func insertIntegrated(t *testing.T, s *items.Store, itemID string) {
	t.Helper()
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_i', 'orch', 'fake', 'm', 'orchestrator', ?, ?, '', 'active', 1);
		INSERT INTO sessions (id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES ('ses_i', 'agt_i', 1, 1, 'h', 'orch', '/tmp', 'running', 'neutral', 1);
		INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, git_json, created_at)
		VALUES ('ckp_i', 'ses_i', 'agt_i', ?, 'integrated', 1, 'merged', '[]', 9999999999999);`, itemID, itemID, itemID)
	if err != nil {
		t.Fatal(err)
	}
}

// eventsOfType returns the payloads of every event of typ, in seq order.
func eventsOfType(t *testing.T, s *items.Store, typ string) []map[string]string {
	t.Helper()
	evs, err := s.Events.After(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]string{}
	for _, e := range evs {
		if e.Type != typ {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

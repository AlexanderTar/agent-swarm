package advisor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// clk is the movable clock every test in this package uses.
type clk struct{ t time.Time }

func newClk() *clk { return &clk{t: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)} }

func (c *clk) Now() time.Time          { return c.t }
func (c *clk) Advance(d time.Duration) { c.t = c.t.Add(d) }

// waitUntil polls a condition for up to 5 s. Every concurrent test in this package
// uses it instead of a sleep, and `go test -race` is required (Task 26 Step 4).
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the condition")
}

func countAgents(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// advisorSeed is one agent + session + item, enough for an advice request.
type advisorSeed struct {
	AgentID, SessionID, AgentName, ItemKey string
}

// newAdvisorService builds a Service on a migrated temp DB with one seeded
// session, a stubbed Run and no network or CLI anywhere. Run is replaced by every
// test that cares; the default returns a valid empty answer so a test that forgets
// still fails on its own assertion rather than on a nil call.
func newAdvisorService(t *testing.T) (*Service, advisorSeed) {
	t.Helper()
	s, seeds := newAdvisorServiceWithSessions(t, 1)
	return s, seeds[0]
}

func newAdvisorServiceWithSessions(t *testing.T, n int) (*Service, []advisorSeed) {
	t.Helper()
	d := dbtest.Open(t)
	at := newClk()
	s := &Service{DB: d, Events: events.New(d, at.Now), Home: t.TempDir(), UserHome: t.TempDir(),
		Adapters: map[runtime.AgentKind]adapter.Adapter{runtime.Fake: adapter.NewFake(adapter.Deps{
			Home: t.TempDir(), UserHome: t.TempDir(), Bin: "/usr/local/bin/swarm", Log: func(string, ...any) {}})},
		MaxConcurrent: 2, Timeout: 5 * time.Minute,
		Now: at.Now, Log: func(string, ...any) {},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"result":"x"}`), nil
		}}
	var out []advisorSeed
	for i := range n {
		out = append(out, seedAdvisorSession(t, s, i))
	}
	return s, out
}

// seedAdvisorSession inserts one agents row (kind fake, role coder, state
// active), one sessions row (state running) and the item they reference, all
// with raw SQL (internal/runtime's own seeds live in its own _test.go files
// and are not importable). The agent's *advisor* is claude, not fake: fake
// cannot run as a read-only advisor (AdvisorCommand has no case for it), and
// the seeded Run stub is never actually exec'd anyway.
func seedAdvisorSession(t *testing.T, s *Service, i int) advisorSeed {
	t.Helper()
	ctx := context.Background()
	suffix := strconv.Itoa(i)
	itemID, agentID, sessionID := ids.New("itm"), ids.New("agt"), ids.New("ses")
	itemKey := "TASK-" + suffix
	agentName := "advisee-" + suffix
	now := db.Millis(s.now())

	if _, err := s.DB.ExecContext(ctx, `INSERT INTO items
		(id, key, type, root_id, title, status, created_at, updated_at)
		VALUES (?, ?, 'task', ?, 'Advisee task', 'in_progress', ?, ?)`,
		itemID, itemKey, itemID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO agents
		(id, name, kind, model, role, item_id, root_item_id,
		 advisor_kind, advisor_model, advisor_effort, advisor_mode, brief, state, created_at)
		VALUES (?, ?, 'fake', 'fake-1', 'coder', ?, ?, 'claude', 'claude-haiku-4-5', '', 'simulated', 'brief', 'active', ?)`,
		agentID, agentName, itemID, itemID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions
		(id, agent_id, attempt, generation, token_hash, tmux_name, cwd, state, cwd_kind, started_at)
		VALUES (?, ?, 1, 1, ?, ?, ?, 'running', 'neutral', ?)`,
		sessionID, agentID, sessionID, agentName, filepath.Join(s.Home, "work", agentName), now); err != nil {
		t.Fatal(err)
	}
	return advisorSeed{AgentID: agentID, SessionID: sessionID, AgentName: agentName, ItemKey: itemKey}
}

// seedOtherAdvisorAgent seeds a second, unrelated agent+session with no advice
// of its own, for the "another agent's advice never appears" check.
func seedOtherAdvisorAgent(t *testing.T, s *Service) string {
	t.Helper()
	return seedAdvisorSession(t, s, 9000).AgentName
}

// adviceState is the newest advice row's state for one session, or "" if there is
// none. This is what the busy rule is written against.
func adviceState(t *testing.T, s *Service, sessionID string) string {
	t.Helper()
	var st string
	err := s.DB.QueryRowContext(context.Background(),
		`SELECT state FROM advice WHERE session_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`,
		sessionID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

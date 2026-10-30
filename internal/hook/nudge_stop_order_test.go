package hook

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/adapter"
	"github.com/AlexanderTar/swarmrun/internal/events"
	"github.com/AlexanderTar/swarmrun/internal/items"
	"github.com/AlexanderTar/swarmrun/internal/runtime"
	"github.com/AlexanderTar/swarmrun/internal/settings"
)

// stopOrderClock advances 1 ms on every read, same as the runtime test clock,
// so message timestamps stay ordered.
type stopOrderClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStopOrderClock() *stopOrderClock {
	return &stopOrderClock{t: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
}

func (c *stopOrderClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

func (c *stopOrderClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// paneStub is the reconcile surface this test needs: one live pane owned by
// the session.
type paneStub struct {
	sessionID string
	name      string
}

func (p paneStub) Start(context.Context, string, string, map[string]string, []string) error {
	return nil
}
func (p paneStub) Panes(context.Context) ([]runtime.Pane, error) {
	return []runtime.Pane{{Session: p.name, Command: "fake"}}, nil
}
func (p paneStub) Capture(context.Context, string, int) (string, error) { return "working\n", nil }
func (p paneStub) PasteLine(context.Context, string, string) error      { return nil }
func (p paneStub) Keys(context.Context, string, ...string) error        { return nil }
func (p paneStub) Env(context.Context, string, string) (string, error)  { return p.sessionID, nil }
func (p paneStub) Kill(context.Context, string) error                   { return nil }
func (p paneStub) RenameWindow(context.Context, string, string) error   { return nil }
func (p paneStub) RenameSession(context.Context, string, string) error  { return nil }

// A tool that runs longer than the stale-hook fallback and then Stops is still
// this stretch, but only when the handler notes that Stop before TurnEnded.
// PreToolUse, more than 15 min, Stop, a wake seconds later, a tool, then 6 min
// is one nudge. Noting the Stop after TurnEnded marks idle at the tool start
// and the same sequence nudges zero times.
func TestStopNotedBeforeTurnEndedKeepsTheNudgeClock(t *testing.T) {
	clk := newStopOrderClock()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(context.Background(), `UPDATE agents SET kind = 'fake' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	h.Now = clk.Now
	h.RT.Now = clk.Now
	h.RT.Tmux = paneStub{sessionID: ses, name: "login-form-coder"}
	h.RT.Events = events.New(h.DB, clk.Now)
	h.RT.Items = &items.Store{DB: h.DB, Events: h.RT.Events, Now: clk.Now}
	h.RT.Settings = &settings.Store{DB: h.DB, Events: h.RT.Events, Now: clk.Now}
	h.Adapters = adapter.All(adapter.Deps{Home: t.TempDir(), UserHome: t.TempDir(),
		Bin: "/usr/local/bin/swarm", Log: func(string, ...any) {}})

	hook := func(ev, stdin string) {
		t.Helper()
		if _, err := h.Handle(context.Background(), runtime.Fake, ev, ses, []byte(stdin)); err != nil {
			t.Fatal(err)
		}
	}
	hook("PreToolUse", `{"tool_name":"Read","tool_input":{"file_path":"x"}}`)
	clk.Advance(20 * time.Minute)
	hook("Stop", `{}`)
	clk.Advance(30 * time.Second)
	hook("UserPromptSubmit", `{"prompt":""}`)
	hook("PreToolUse", `{"tool_name":"Read","tool_input":{"file_path":"y"}}`)
	clk.Advance(6 * time.Minute)
	if err := h.RT.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE to_agent_id = 'agt_1'
		AND kind = 'assignment_update' AND origin = 'daemon' AND payload_json LIKE '%"event":"progress_nudge"%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("20 min tool, Stop, 30s wake, 6 min more: %d nudges, want 1", n)
	}
}

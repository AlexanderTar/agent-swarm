package advisor

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// Tasks 26 and 27 add `adapter`, `dbtest`, `events`, `runtime`, `database/sql` and
// `errors` to this import block, along with the helpers that use them
// (newAdvisorService and friends). Go refuses an unused import, so this file only
// imports what Task 25's own helpers need until then.
//
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

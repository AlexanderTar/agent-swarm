package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// Two concurrent drivers of one operation (a request's own synchronous walk
// racing a reconcile tick's ResumeOperations) must launch exactly one
// successor and land the operation on succeeded. Without phase
// compare-and-swap plus the per-agent driver lock, the loser re-read the
// replaced session and overwrote succeeded with blocked.
func TestConcurrentDriversLaunchOneSuccessor(t *testing.T) {
	for i := 0; i < 8; i++ {
		s, tm, _ := newStore(t)
		ctx := context.Background()
		_, w, wSes := worker(t, s)
		if err := s.SetSessionState(ctx, wSes.ID, Interrupted); err != nil {
			t.Fatal(err)
		}
		panes(tm)
		now := db.Millis(s.Now())
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
			(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
			VALUES ('op_race', ?, 'recover', 'ready', 'k', ?, ?, ?, ?)`,
			w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for d := 0; d < 2; d++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = s.advanceOperation(ctx, "op_race")
			}()
		}
		wg.Wait()
		var phase string
		if err := s.DB.QueryRowContext(ctx, `SELECT phase FROM agent_operations WHERE id = 'op_race'`).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if phase != string(PhaseSucceeded) || n != 2 {
			t.Fatalf("run %d: phase = %q, sessions = %d; want succeeded with exactly one successor", i, phase, n)
		}
	}
}

// Cancel racing a driver about to launch: Cancel wins. Either the operation
// is cancelled before the launch (no successor at all), or the launch
// finished first and Cancel stopped that successor. Never a cancelled
// operation with a live successor it launched anyway.
func TestCancelRacingDriverNeverLeavesLiveSuccessor(t *testing.T) {
	for i := 0; i < 8; i++ {
		s, tm, _ := newStore(t)
		ctx := context.Background()
		_, w, wSes := worker(t, s)
		if err := s.SetSessionState(ctx, wSes.ID, Interrupted); err != nil {
			t.Fatal(err)
		}
		panes(tm)
		now := db.Millis(s.Now())
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
			(id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
			VALUES ('op_cancel_race', ?, 'recover', 'ready', 'k', ?, ?, ?, ?)`,
			w.ID, wSes.ID, wSes.Generation, now, now); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = s.advanceOperation(ctx, "op_cancel_race") }()
		go func() { defer wg.Done(); _, _ = s.Cancel(ctx, w.Name, "", "") }()
		wg.Wait()
		latest, err := s.LatestSession(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		if latest.State.Live() {
			t.Fatalf("run %d: latest session %s is %s after Cancel; Cancel must win", i, latest.ID, latest.State)
		}
	}
}

package runtime

import (
	"context"
	"fmt"
	"testing"
)

// A spawned worker is committed together with its assignment message; the
// session is started after that commit (and recovered by reconcile if it fails).
const unassignedWorkers = `SELECT COUNT(*) FROM agents a WHERE a.role = 'coder' AND NOT EXISTS
	(SELECT 1 FROM messages m WHERE m.to_agent_id = a.id AND m.kind = 'assignment')`

func TestFaultSweepSpawn(t *testing.T) {
	surfaced := sweepFaults(t, 120, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		ep := seedEpicWithTask(t, s)
		orch, _, err := s.StartOrchestrator(context.Background(), OrchestratorInput{ItemKey: ep.Key, Kind: Fake, Model: "fake-1"})
		if err != nil {
			t.Fatal(err)
		}
		op := func(ctx context.Context) error {
			_, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
				ParentAgentID: orch.ID, Brief: BriefInput{Objective: "build it"}})
			return err
		}
		return s, op, func(context.Context) error { return wantCount(s, unassignedWorkers, 0) }
	})
	if surfaced == 0 {
		t.Fatal("no injected fault surfaced as an error from Spawn")
	}
}

// mkSweep adapts a (store, op) pair plus a state invariant into a faultCase;
// every sweep also demands no dangling foreign keys.
func mkSweep(s *Store, op func(context.Context) error, inv func() error) (*Store, func(context.Context) error, func(context.Context) error) {
	return s, op, func(context.Context) error {
		if inv != nil {
			if err := inv(); err != nil {
				return err
			}
		}
		return fkClean(s)
	}
}

// sweepOK runs a sweep and requires at least one injected fault to surface
// as the op's own error: the op does not swallow DB failures wholesale.
func sweepOK(t *testing.T, maxN int, mk faultCase) {
	t.Helper()
	if sweepFaults(t, maxN, mk) == 0 {
		t.Fatal("no injected fault surfaced as an error")
	}
}

func TestFaultSweepStartSpikeAndCancel(t *testing.T) {
	sweepOK(t, 150, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		return mkSweep(s, func(ctx context.Context) error {
			_, _, _, err := s.StartSpike(ctx, SpikeInput{Name: "Sweep", Intent: "feature", Kind: Fake, Model: "fake-1"})
			return err
		}, func() error {
			return wantCount(s, `SELECT COUNT(*) FROM agents a WHERE NOT EXISTS (SELECT 1 FROM messages m WHERE m.to_agent_id = a.id AND m.kind = 'assignment')`, 0)
		})
	})
	sweepOK(t, 80, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, w, _ := worker(t, s)
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.Cancel(ctx, w.Name, "", "")
			return err
		}, func() error {
			// Cancel is all-or-nothing on the agent row: never half 'finished'
			// without its finished_at stamp.
			return wantCount(s, `SELECT COUNT(*) FROM agents WHERE id = ? AND state = 'finished' AND finished_at IS NULL`, 0, w.ID)
		})
	})
}

func TestFaultSweepRetry(t *testing.T) {
	sweepOK(t, 120, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, w, wSes := worker(t, s)
		if err := s.SetSessionState(context.Background(), wSes.ID, Crashed); err != nil {
			t.Fatal(err)
		}
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.Retry(ctx, w.Name, "pick it back up", "", "")
			return err
		}, nil)
	})
}

func TestFaultSweepPauseResume(t *testing.T) {
	sweepOK(t, 80, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, w, _ := worker(t, s)
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.Pause(ctx, w.Name, "session")
			return err
		}, func() error {
			// A pause is a state change plus its control message, or neither.
			return wantCount(s, `SELECT COUNT(*) FROM sessions se WHERE se.agent_id = ? AND se.state = 'pause_requested'
				AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.to_agent_id = se.agent_id AND m.kind = 'control')`, 0, w.ID)
		})
	})
	sweepOK(t, 120, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, w, wSes := worker(t, s)
		if _, err := s.DB.ExecContext(context.Background(),
			`UPDATE sessions SET state = 'paused', provider_session_id = 'p1' WHERE id = ?`, wSes.ID); err != nil {
			t.Fatal(err)
		}
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.Resume(ctx, w.Name, "", "")
			return err
		}, nil)
	})
}

func TestFaultSweepAskAndAnswer(t *testing.T) {
	sweepOK(t, 80, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Ask", Intent: "feature", Kind: Fake, Model: "fake-1"})
		if err != nil {
			t.Fatal(err)
		}
		ses, _ := s.LatestSession(context.Background(), a.ID)
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Keep the email?"})
			return err
		}, func() error {
			return wantCount(s, `SELECT COUNT(*) FROM requests WHERE state <> 'open'`, 0)
		})
	})
	sweepOK(t, 80, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Answer", Intent: "feature", Kind: Fake, Model: "fake-1"})
		if err != nil {
			t.Fatal(err)
		}
		ses, _ := s.LatestSession(context.Background(), a.ID)
		req, err := s.Ask(context.Background(), ses.ID, AskInput{Kind: "question", Prompt: "Keep the email?"})
		if err != nil {
			t.Fatal(err)
		}
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.Answer(ctx, req.ID, "Yes.", "board")
			return err
		}, func() error {
			// An answered request always has its user_answer message.
			return wantCount(s, `SELECT COUNT(*) FROM requests r WHERE r.id = ? AND r.state = 'answered'
				AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.request_id = r.id AND m.kind = 'user_answer')`, 0, req.ID)
		})
	})
}

func TestFaultSweepCheckpoints(t *testing.T) {
	sweepOK(t, 120, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, _, wSes := worker(t, s)
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
			return err
		}, nil)
	})
	sweepOK(t, 160, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		_, _, wSes := worker(t, s)
		if _, err := s.WriteCheckpoint(context.Background(), wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
			t.Fatal(err)
		}
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: CompletedCkp, Summary: "done",
				Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}})
			return err
		}, nil)
	})
}

func TestFaultSweepQueuedRetryOperation(t *testing.T) {
	// Retry while the predecessor is still stopping queues a durable intent.
	sweepOK(t, 120, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, _, _ := newStore(t)
		orch, w, wSes := worker(t, s)
		orchSes, _ := s.LatestSession(context.Background(), orch.ID)
		if err := s.SetSessionState(context.Background(), wSes.ID, Stopping); err != nil {
			t.Fatal(err)
		}
		return mkSweep(s, func(ctx context.Context) error {
			_, err := s.Retry(ctx, w.Name, "queued", orchSes.ID, "r1")
			return err
		}, func() error {
			// The intent is one operation or none, never a duplicate.
			var n int
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
				return err
			}
			if n > 1 {
				return fmt.Errorf("operations = %d, want at most 1", n)
			}
			return nil
		})
	})
	// ResumeOperations then drives that intent to a successor launch.
	sweepOK(t, 250, func(t *testing.T) (*Store, func(context.Context) error, func(context.Context) error) {
		s, tm, _ := newStore(t)
		orch, w, wSes := worker(t, s)
		orchSes, _ := s.LatestSession(context.Background(), orch.ID)
		ctx := context.Background()
		if err := s.SetSessionState(ctx, wSes.ID, Stopping); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Retry(ctx, w.Name, "queued", orchSes.ID, "r1"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSessionState(ctx, wSes.ID, Crashed); err != nil {
			t.Fatal(err)
		}
		panes(tm)
		return mkSweep(s, s.ResumeOperations, func() error {
			// However far the launch got, an agent never has two live sessions.
			return wantCount(s, `SELECT CASE WHEN COUNT(*) > 1 THEN 1 ELSE 0 END FROM sessions
				WHERE agent_id = ? AND state IN ('spawning', 'running')`, 0, w.ID)
		})
	})
}

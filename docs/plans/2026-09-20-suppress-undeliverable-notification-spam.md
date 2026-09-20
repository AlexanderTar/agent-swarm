# Implementation Plan: Suppress Duplicate Undeliverable Notifications & Enforce Paste Retry Backoff

- **Date**: 2026-09-20
- **Companion Spec**: [`docs/specs/2026-09-20-suppress-undeliverable-notification-spam.md`](file:///Users/alexandertar/GitHub/agent-swarm-undeliverable-fix/docs/specs/2026-09-20-suppress-undeliverable-notification-spam.md)
- **Branch**: `feat/undeliverable-notification-fix`

---

## Task 1: Enforce 30-Second `pasteRetry` Between Failed Paste Attempts

### 1.1 Write Failing Test
File: `internal/runtime/wake_test.go`
Add test `TestPasteRetryIntervalEnforced`:
```go
func TestPasteRetryIntervalEnforced(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "RetryGap", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable

	// First attempt after delay
	at.Advance(25 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, _ := s.getPasteAttempts(ses.ID)
	if attempts != 1 {
		t.Fatalf("first attempt: expected 1, got %d", attempts)
	}

	// 5 seconds later (within pasteRetry 30s): must be skipped
	at.Advance(5 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, _ = s.getPasteAttempts(ses.ID)
	if attempts != 1 {
		t.Fatalf("within 30s: expected 1 attempt, got %d", attempts)
	}

	// 26 seconds later (total 31s > 30s): second attempt fires
	at.Advance(26 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	attempts, _ = s.getPasteAttempts(ses.ID)
	if attempts != 2 {
		t.Fatalf("after 31s: expected 2 attempts, got %d", attempts)
	}
}
```

### 1.2 Run Failing Test
Command: `go test -v -run TestPasteRetryIntervalEnforced ./internal/runtime/...`
Expected result: Fails because attempt count increments to 2 after 5 seconds.

### 1.3 Implement In-Memory Timestamp Tracking & Check in `WakeDue`
File: `internal/runtime/wake.go`
- Update `Store` to track `lastPasteAttemptAt map[string]time.Time`.
- Update `recordPasteAttempt(ctx context.Context, sessionID string, n int, at time.Time) error` and `recordPasteAttemptMem(sessionID string, n int, at time.Time)`:
  - When `n > 0`, store `s.lastPasteAttemptAt[sessionID] = at`.
  - When `n == 0`, delete `s.lastPasteAttemptAt[sessionID]` (in `markWoken`, pass `time.Time{}`).
  - In `tryPaste`: pass `s.Now()` to `s.recordPasteAttempt(ctx, r.SessionID, attempts, s.Now())`.
- Update `getPasteAttempts(sessionID string) (int, *time.Time)`.
- Update `wakeCandidates`:
  - Add `ses.started_at` to the `SELECT` query.
  - Scan into `var startedAt int64` and set `r.StartedAt = db.FromMillis(startedAt)`.
  - Populate `r.PasteAttempts, r.LastPasteAttemptAt = s.getPasteAttempts(r.SessionID)`.
- Update `WakeDue`:
  Replace:
  ```go
  if r.PasteAttempts > 0 && r.LastWakeAt != nil && s.Now().Sub(*r.LastWakeAt) < pasteRetry {
      continue
  }
  ```
  With:
  ```go
  if r.PasteAttempts > 0 && r.LastPasteAttemptAt != nil && s.Now().Sub(*r.LastPasteAttemptAt) < pasteRetry {
      continue
  }
  ```

### 1.4 Run and Verify Pass
Command: `go test -v -run TestPasteRetryIntervalEnforced ./internal/runtime/...`
Expected result: PASS.

---

## Task 2: Suppress Duplicate `agent.undeliverable` Notifications

### 2.1 Write Failing Test
File: `internal/runtime/wake_test.go`
Add test `TestUndeliverableNotificationOnlyFiresOncePerBatch`:
```go
func TestUndeliverableNotificationOnlyFiresOncePerBatch(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "DedupUndeliverable", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable

	// 15 cycles of 31s: reaches 10 attempts and continues for 5 more
	for i := 0; i < 15; i++ {
		at.Advance(31 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	f := s.Notify.(*fakeNotifier)
	f.mu.Lock()
	defer f.mu.Unlock()
	undeliverableCount := 0
	for _, n := range f.raised {
		if n.Kind == "agent.undeliverable" {
			undeliverableCount++
		}
	}
	if undeliverableCount != 1 {
		t.Fatalf("expected exactly 1 undeliverable notification, got %d", undeliverableCount)
	}
}
```

### 2.2 Run Failing Test
Command: `go test -v -run TestUndeliverableNotificationOnlyFiresOncePerBatch ./internal/runtime/...`
Expected result: Fails because multiple `agent.undeliverable` notifications are raised.

### 2.3 Implement `alreadyNotifiedUndeliverable` in `wake.go`
File: `internal/runtime/wake.go`
```go
func (s *Store) alreadyNotifiedUndeliverable(ctx context.Context, agentID string, since time.Time) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
		WHERE agent_id = ? AND kind = 'agent.undeliverable' AND created_at >= ?`,
		agentID, db.Millis(since)).Scan(&count)
	return count > 0, err
}
```
In `tryPaste`:
```go
	if attempts >= maxPasteAttempts {
		since := r.OldestMessageAt
		if r.StartedAt.After(since) {
			since = r.StartedAt
		}
		already, err := s.alreadyNotifiedUndeliverable(ctx, r.AgentID, since)
		if err != nil {
			return err
		}
		if !already {
			if err := s.notify(ctx, nil, NotifyInput{Kind: "agent.undeliverable", AgentName: r.AgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "N": strconv.Itoa(r.Pending)}}); err != nil {
				return err
			}
			var recorded int
			_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
				WHERE agent_id = ? AND kind = 'agent.undeliverable' AND created_at >= ?`,
				r.AgentID, db.Millis(since)).Scan(&recorded)
			if recorded == 0 {
				_, _ = s.DB.ExecContext(ctx, `INSERT INTO notifications
					(id, level, kind, title, body, agent_id, item_id, dedup_key, created_at)
					VALUES (?, 'attention', 'agent.undeliverable', 'Couldn\'t deliver messages', 'Couldn\'t deliver messages', ?, ?, ?, ?)`,
					ids.New("ntf"), r.AgentID, r.ItemKey, fmt.Sprintf("agent.undeliverable:%s:", r.AgentName), db.Millis(s.Now()))
			}
		}
	}
```

### 2.4 Run and Verify Pass
Command: `go test -v -run TestUndeliverableNotificationOnlyFiresOncePerBatch ./internal/runtime/...`
Expected result: PASS.

---

## Task 3: Full Verification & Integration

### 3.1 Run Full Test Suite
Command: `go test ./internal/...`
Verify all runtime, notify, and integration tests pass without regression.

### 3.2 Commit Changes
```sh
git add internal/runtime/wake.go internal/runtime/wake_test.go docs/specs/2026-09-20-suppress-undeliverable-notification-spam.md docs/plans/2026-09-20-suppress-undeliverable-notification-spam.md
git commit -m "fix(runtime): suppress duplicate undeliverable notifications and enforce paste retry backoff"
```

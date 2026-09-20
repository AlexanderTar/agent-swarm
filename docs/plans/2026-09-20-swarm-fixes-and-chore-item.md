# Plan: Swarm Fixes and Chore Item

Companion to `docs/specs/2026-09-20-swarm-fixes-and-chore-item.md`.
Each step follows strict TDD: failing test → run and watch fail → minimal implementation → run and watch pass → commit.

---

## Step 1 — Fix Idle Notification Spam in `internal/runtime`

Files:
- `internal/runtime/reconcile_test.go`
- `internal/runtime/reconcile.go`

### 1.1 Failing Test (`internal/runtime/reconcile_test.go`)
Add test verifying that when an agent remains quiet past 30 minutes, it is notified once, and advancing another 30 minutes without activity does NOT raise a second notification:

```go
func TestStaleNotificationOnlyFiresOncePerSilencePeriod(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Quiet", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	tm.captures[a.Name] = []string{"working on it…\n"} // not idle, so not waiting

	// Advance past 30 min -> 1 notification
	at.Advance(31 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.stale"); n != 1 {
		t.Fatalf("first stale check: count = %d, want 1", n)
	}

	// Advance past dedupWindow (35s) and another 30 min without new activity -> STILL 1 notification
	at.Advance(35 * time.Second)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	at.Advance(30 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.stale"); n != 1 {
		t.Fatalf("subsequent stale checks without activity: count = %d, want 1", n)
	}

	// New activity clears the silence period
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`,
		db.Millis(at.Now()), ses.ID); err != nil {
		t.Fatal(err)
	}
	at.Advance(31 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.stale"); n != 2 {
		t.Fatalf("after new activity and 30 min silence: count = %d, want 2", n)
	}
}
```

Run test:
```bash
go test -run TestStaleNotificationOnlyFiresOncePerSilencePeriod ./internal/runtime/...
```
Fails because count is > 1.

### 1.2 Implementation (`internal/runtime/reconcile.go`)
Add check for existing stale notification since `r.lastActivity()`:

```go
func (s *Store) alreadyNotifiedStale(ctx context.Context, agentID string, since time.Time) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
		WHERE agent_id = ? AND kind = 'agent.stale' AND created_at >= ?`,
		agentID, db.Millis(since)).Scan(&count)
	return count > 0, err
}
```

In `resolveAlive`:
```go
	if s.Now().Sub(r.lastActivity()) >= staleAfter {
		already, err := s.alreadyNotifiedStale(ctx, r.AgentID, r.lastActivity())
		if err != nil {
			return err
		}
		if !already {
			return s.notify(ctx, nil, NotifyInput{Kind: "agent.stale", AgentName: r.AgentName, ItemKey: r.ItemKey,
				Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}})
		}
	}
```

Run test: passes.

---

## Step 2 — Fix TCC App Data Prompts: Stable Codesign Identity in `Makefile`

Files:
- `Makefile`

### 2.1 Implementation
Update `build` and `install-daemon` targets in `Makefile`:

```makefile
build: web-build
	$(GO) build -o bin/swarm ./cmd/swarm
	codesign --force --sign "$${SWARM_SIGN_IDENTITY:--}" --identifier dev.swarm.daemon bin/swarm

install-daemon: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/swarm $(PREFIX)/bin/swarm
	codesign --force --sign "$${SWARM_SIGN_IDENTITY:--}" --identifier dev.swarm.daemon $(PREFIX)/bin/swarm
```

### 2.2 Verification
Run:
```bash
make build
codesign -dvvv bin/swarm 2>&1 | grep "Identifier=dev.swarm.daemon"
```

---

## Step 3 — Quota Reset Wakeup Ping in `internal/runtime` and `internal/usage`

Files:
- `internal/runtime/wake_test.go`
- `internal/runtime/wake.go`
- `cmd/swarm/daemon.go`

### 3.1 Failing Test (`internal/runtime/wake_test.go`)
Test that `WakeOnQuotaReset(ctx, kind, cutoff)` wakes live/idle sessions of that kind and debounces:

```go
func TestWakeOnQuotaReset(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "RateLimited", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"─────\n❯ \n─────\n"} // idle

	// Mark session waiting
	s.DB.ExecContext(ctx, `UPDATE sessions SET waiting = 1 WHERE id = ?`, ses.ID)

	cutoff := at.Now().Add(-2 * time.Minute)
	n, err := s.WakeOnQuotaReset(ctx, Fake, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("woken count = %d, want 1", n)
	}
	if len(tm.pasted[a.Name]) == 0 || tm.pasted[a.Name][0] != IdleToken {
		t.Fatalf("pasted = %v, want idle token", tm.pasted[a.Name])
	}

	// Debounce: calling again with same cutoff must NOT wake again
	n2, err := s.WakeOnQuotaReset(ctx, Fake, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second call: woken count = %d, want 0 (debounced)", n2)
	}
}
```

Run test: fails (`WakeOnQuotaReset` undefined).

### 3.2 Implementation (`internal/runtime/wake.go`)
```go
// WakeOnQuotaReset wakes all live or waiting sessions belonging to kind that have
// not already been woken for this cutoff cycle (last_wake_at < cutoff).
func (s *Store) WakeOnQuotaReset(ctx context.Context, kind AgentKind, cutoff time.Time) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ses.id, ses.tmux_name, ses.state, ses.waiting
		FROM sessions ses JOIN agents a ON a.id = ses.agent_id
		WHERE a.kind = ? AND ses.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		AND (ses.last_wake_at IS NULL OR ses.last_wake_at < ?)`,
		string(kind), db.Millis(cutoff))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	ad, ok := s.Adapters[kind]
	woken := 0
	for rows.Next() {
		var sessionID, tmuxName, state string
		var waiting bool
		if err := rows.Scan(&sessionID, &tmuxName, &state, &waiting); err != nil {
			return woken, err
		}
		// Attempt native wake or paste idle token if pane is idle
		if ok {
			delivered, _ := ad.Wake(ctx, adapter.WakeTarget{SessionID: sessionID, TmuxName: tmuxName, Notice: "[swarm] Quota reset window passed. Resuming."})
			if delivered {
				s.markWoken(ctx, sessionID, true)
				woken++
				continue
			}
		}
		// Fallback to idle paste if pane is alive
		capture, err := s.Tmux.Capture(ctx, tmuxName, 15)
		if err == nil && ok && ad.Idle(capture) {
			if err := s.Tmux.PasteLine(ctx, tmuxName, IdleToken); err == nil {
				s.markWoken(ctx, sessionID, false)
				woken++
			}
		}
	}
	return woken, nil
}
```

Run test: passes.

### 3.3 Wire in Daemon Loop (`cmd/swarm/daemon.go`)
In `daemon.go`, check active reset cutoffs every minute and invoke `WakeOnQuotaReset` 1 minute after cutoff.

---

## Step 4 — Top-Level `CHORE` Item

Files:
- `internal/db/db.go`
- `internal/db/schema/0003_add_chore.sql`
- `internal/ids/ids_test.go`, `internal/ids/ids.go`
- `internal/items/model.go`, `internal/items/store.go`, `internal/items/store_test.go`
- `internal/runtime/artifacts.go`, `internal/runtime/materialize.go`
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`
- `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift`
- `apps/menubar/Sources/SwarmBarKit/Copy.swift`
- `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`
- `apps/menubar/Tests/SwarmBarTests/NewOrchestratorFormTests.swift`

### 4.1 Migration & IDs (`internal/db/db.go`, `internal/db/schema/0003_add_chore.sql`, `internal/ids/ids.go`)
1. In `internal/db/db.go`: ensure migration runs on a dedicated connection with `PRAGMA foreign_keys = OFF` before the transaction so table recreation does not trigger foreign key constraint errors.
2. Create `0003_add_chore.sql` with table migration.
3. Add `"chore": true` to `keyTypes` in `ids.go`.
4. Add test in `ids_test.go` asserting `NextKey(ctx, tx, "chore")` returns `"CHORE-1"`.

### 4.2 Items Store (`internal/items`)
1. In `model.go`: add `Chore Type = "chore"`.
2. In `store.go`:
   - Add `Chore` to `allowedParents[Task]`: `Task: {Story, Bug, Spike, Chore}`.
   - Top-level items: `Epic`, `Bug`, `Spike`, `Chore` require `ParentKey == ""`.
3. In `store_test.go`: test creating a `Chore` and creating child tasks under it.

### 4.3 Runtime Materialize & Tree Shapes
1. In `artifacts.go`: update `parentTypesFor`:
   - `case "chore": return []string{"task"}`.
   - `validateTreeShape`: allow `t.Root.Type == "chore"`.
2. In `materialize.go`: allow `rootType = items.Chore`.

### 4.4 Menubar App (`apps/menubar`)
1. In `Wire.swift`:
   ```swift
   public enum SpikeIntent: String, Codable, Sendable, CaseIterable {
       case chore
       case feature
       case debug
   }
   ```
2. In `Copy.swift`:
   ```swift
   public static let choreIntent = "Chore"
   public static let choreCaption = "Creates a top-level chore orchestrator for maintenance, refactoring, or general work."
   ```
3. In `NewOrchestratorForm.swift`:
   - Default `intent: SpikeIntent = .chore`.
   - Update `intentCaption` to return `Copy.choreCaption` for `.chore`.
4. In `NewOrchestratorView.swift`:
   - Add `Text(Copy.choreIntent).tag(SpikeIntent.chore)` to the segmented picker.
5. In `NewOrchestratorFormTests.swift`:
   - Add test asserting default intent is `.chore`.

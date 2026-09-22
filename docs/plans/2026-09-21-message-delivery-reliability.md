# Message Delivery Reliability Implementation Plan

Spec: `docs/specs/2026-09-21-message-delivery-reliability.md` (read it first; decisions there are locked).
Order is the spec's priority order, except that "count only pending" (spec fix 3) lands before "redefine undeliverable" (spec fix 2), because the alert rewrite builds on it.

Rules for every task: strict TDD (write the failing test, run it and watch it fail for the stated reason, minimal change, run it green, commit). Stage explicit paths only; never `git add -A`; never `git commit --amend`. Do not delete or weaken an existing test; where a test's name or comment no longer fits, rename it or update its assertions. Commit messages below carry no attribution trailer because swarm agents must not add one (`skills/swarm/SKILL.md` rule 10); add the one your harness requires if you are not a swarm agent.

Module path used in imports: `github.com/AlexanderTar/agent-swarm`.

## Task 0: Sibling worktree and testdata

The three live pane captures are untracked in the primary checkout. Copy them, do not regenerate them.

```bash
cd /Users/alexandertar/GitHub/agent-swarm
git fetch origin && git worktree add ../agent-swarm--delivery-reliability -b fix/message-delivery-reliability origin/main
cp internal/adapter/testdata/claude/pane-idle-cursor-*.txt ../agent-swarm--delivery-reliability/internal/adapter/testdata/claude/
cd ../agent-swarm--delivery-reliability && go test ./internal/adapter/... ./internal/runtime/... ./internal/hook/... ./internal/mcpserver/...   # baseline must be green
```

If baseline is red, stop and report which tests; do not fix unrelated failures here.

The three files (produces): `pane-idle-cursor-default-fg.txt`, `pane-idle-cursor-grey-prompt.txt`, `pane-idle-cursor-reset-fg.txt`. Stage them in Task 1's commit.

## Task 1: claudeIdle accepts the reverse-video cursor cell (spec fix 1)

Files: `internal/adapter/claude.go` (line 109), `internal/adapter/claude_test.go`.
Consumes: `pane(t, agent, name)` helper (`claude_test.go:18`), `newClaude(testDeps(t))`, `claudeIdle`.
Produces: `claudeIdle` matching the new prompt shapes; no signature change.

- [ ] **1.1 Failing test.** Append to `internal/adapter/claude_test.go`:

```go
// 2026-09-21, live: Claude 2.1.278 draws an idle, empty, suggestion-less
// prompt as "❯ NBSP + reverse-video cursor cell". claudeIdle only allowed an
// SGR-2 suggestion after the prompt, so idle agents were never pasted and a
// message sat pending for 11+ minutes (msg_01M31G7G6PT7G7DKKPH91EZK5H). The
// three captures come from three live agents via
// `tmux capture-pane -p -e -J`.
func TestClaudeIdleAcceptsTheReverseVideoCursorCell(t *testing.T) {
	a := newClaude(testDeps(t))
	for _, f := range []string{
		"pane-idle-cursor-default-fg.txt",
		"pane-idle-cursor-grey-prompt.txt",
		"pane-idle-cursor-reset-fg.txt",
	} {
		if !a.Idle(pane(t, "claude", f)) {
			t.Errorf("%s: idle prompt with a cursor cell must count as idle", f)
		}
	}
	for _, s := range []string{
		"\x1b[39m❯\u00a0\x1b[7m \x1b[0m",     // bare cursor cell
		"\x1b[39m❯\u00a0\x1b[7m \x1b[0m   ",  // padded by tmux
		"\x1b[38;5;246m❯\u00a0\x1b[7m\x1b[39m ", // SGR between the cell attribute and the space
	} {
		if !claudeIdle.MatchString(s) {
			t.Errorf("expected idle: %q", s)
		}
	}
	for _, s := range []string{
		"\x1b[39m❯\u00a0half typed",                        // draft
		"\x1b[39m❯\u00a0\x1b[7mh\x1b[0malf typed",           // cursor on the first character
		"\x1b[39m❯\u00a0half typed\x1b[7m \x1b[0m",          // cursor after the draft
	} {
		if claudeIdle.MatchString(s) {
			t.Errorf("a human draft must not count as idle: %q", s)
		}
	}
	if a.Idle(pane(t, "claude", "pane-input-nonempty.txt")) {
		t.Error("pane-input-nonempty.txt must stay non-idle")
	}
}
```

- [ ] **1.2 Run, watch it fail.** `go test ./internal/adapter/ -run TestClaudeIdleAcceptsTheReverseVideoCursorCell`. Expected FAIL: `pane-idle-cursor-*.txt: idle prompt with a cursor cell must count as idle` (three lines) and `expected idle: ...` lines. The draft cases pass already.

- [ ] **1.3 Minimal change.** In `internal/adapter/claude.go` replace the `claudeIdle` line (109) with, and extend the comment above it by one sentence: "2026-09-21: Claude 2.1.278 also draws an empty prompt as `❯ NBSP` + SGR 7 + one space (the cursor cell); that cell must be a space so a draft with the cursor on a character still fails."

```go
	claudeIdle = regexp.MustCompile("(?m)^(?:\x1b\\[[0-9;]*m)*\u276f[\u00a0 ]" +
		"(?:\x1b\\[2m.*|\x1b\\[7m(?:\x1b\\[[0-9;]*m)*[ \u00a0]?(?:\x1b\\[[0-9;]*m)*)?\\s*$")
```

- [ ] **1.4 Run green.** `gofmt -l internal/adapter` (no output) then `go test ./internal/adapter/...`. Expected: PASS, including the older `TestClaudeIdle` and `TestClaudeIdleAgainstTheCapturedPanes`.

- [ ] **1.5 Commit.**

```bash
git add internal/adapter/claude.go internal/adapter/claude_test.go internal/adapter/testdata/claude/pane-idle-cursor-default-fg.txt internal/adapter/testdata/claude/pane-idle-cursor-grey-prompt.txt internal/adapter/testdata/claude/pane-idle-cursor-reset-fg.txt
git commit -m "fix(adapter): treat Claude's reverse-video cursor prompt as idle"
```

## Task 2: Only pending messages count as new (spec fix 3)

Files: `internal/runtime/wake.go` (lines 43-46), `internal/hook/handler.go` (line 216), `internal/runtime/wake_test.go`, `internal/hook/handler_test.go`.
Consumes: `newStore`, `panes`, `notifiedCount`, `tm.clk`, `tm.env`; hook `seed`, `contextOf`.
Produces: `wakeCandidates` and `hook.load` with `state = 'pending'`. No signature change.

- [ ] **2.1 Failing runtime test.** Append to `internal/runtime/wake_test.go`:

```go
// 2026-09-21: wakeCandidates counted state != 'acked', which includes messages
// the agent had already synced, so agents that read but never acked were
// reported as "hasn't picked up N message(s)" (four false alerts that day).
func TestReadButUnackedMessageIsNeverUndeliverable(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "ReadNotAcked", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"}) // never pasteable
	if _, err := s.Sync(ctx, ses.ID, nil, 20); err != nil { // read, never acked
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		at.Advance(31 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("undeliverable raised %d times for a message the agent had already read", n)
	}
	if len(tm.pasted) != 0 {
		t.Fatalf("nothing should be pasted for a read message: %v", tm.pasted)
	}
}
```

- [ ] **2.2 Failing hook test.** Append to `internal/hook/handler_test.go`:

```go
// A message the agent has already synced (state 'delivered') is not "new": no
// nudge on PostToolUse and no Stop block. Only 'pending' counts.
func TestReadButUnackedMessagesNeitherNudgeNorBlockStop(t *testing.T) {
	h, ses := seed(t, 2, runtime.Running)
	ctx := context.Background()
	if _, err := h.DB.ExecContext(ctx, `UPDATE messages SET state = 'delivered', delivery_count = 1`); err != nil {
		t.Fatal(err)
	}
	post, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, post); got != "" {
		t.Fatalf("a delivered message must not nudge: %q", got)
	}
	stop, err := h.Handle(ctx, runtime.Claude, "Stop", ses, []byte(`{"session_id":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(stop) != 0 {
		t.Fatalf("a delivered message must not block Stop: %s", stop)
	}
}
```

- [ ] **2.3 Run, watch both fail.** `go test ./internal/runtime/ -run TestReadButUnackedMessageIsNeverUndeliverable` fails with `undeliverable raised 1 times ...`. `go test ./internal/hook/ -run TestReadButUnackedMessagesNeitherNudgeNorBlockStop` fails with `a delivered message must not nudge: "[swarm] 2 new message(s) ..."`.

- [ ] **2.4 Minimal change.**
  - `internal/runtime/wake.go` lines 43-46: change each `m.state != 'acked'` to `m.state = 'pending'` (three subqueries). Update the doc comments of `wakeRow` and `wakeCandidates` ("un-acked" → "pending").
  - `internal/hook/handler.go` line 216: `state <> 'acked'` → `state = 'pending'`.

- [ ] **2.5 Run green.** `go test ./internal/runtime/... ./internal/hook/...`. Expected PASS. `TestUndeliverableAfterTenRetries`, `TestUndeliverableNotificationOnlyFiresOncePerBatch` and `TestStopBlocksForAPendingHandoffThenForMessagesUpToThreeTimes` keep passing because their messages are `pending`.

- [ ] **2.6 Commit.**

```bash
git add internal/runtime/wake.go internal/runtime/wake_test.go internal/hook/handler.go internal/hook/handler_test.go
git commit -m "fix(runtime,hook): count only pending messages as new for wake, nudge and Stop"
```

## Task 3: `agent.undeliverable` on database facts (spec fix 2)

Files: `internal/runtime/wake.go`, `internal/runtime/wake_test.go`.
Consumes: `alreadyNotifiedUndeliverable`, `s.notify`, `NotifyInput`, `enq` (`inbox_test.go:15`), `notifiedCount`.
Produces: `const undeliverableAfter`, `func (s *Store) raiseUndeliverable(ctx context.Context, r wakeRow) error`; removes `maxPasteAttempts`.

- [ ] **3.1 Failing tests.** Append to `internal/runtime/wake_test.go`:

```go
// The alert is derived from the database: a pending immediate message older
// than undeliverableAfter on a live session. Not from paste-attempt counts.
func TestUndeliverableFiresAtFiveMinutesOfPendingNotBefore(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "FiveMinutes", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})

	at.Advance(299 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("at 299 s: %d alerts, want 0", n)
	}
	at.Advance(2 * time.Second)
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("at 301 s: %d alerts, want 1", n)
	}
	for i := 0; i < 15; i++ {
		at.Advance(31 * time.Second)
		if err := s.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("after 15 more ticks: %d alerts, want exactly 1", n)
	}
}

// Losing the in-memory paste counters (a daemon restart) must not change the
// alert: it lives in the database.
func TestUndeliverableSurvivesLosingTheAttemptCounters(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Restart", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})
	at.Advance(301 * time.Second)
	s.WakeDue(ctx)
	s.bookkeepingMu.Lock()
	s.pasteAttempts, s.lastPasteAttemptAt = nil, nil
	s.bookkeepingMu.Unlock()
	for i := 0; i < 3; i++ {
		at.Advance(31 * time.Second)
		s.WakeDue(ctx)
	}
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("%d alerts after a counter reset, want exactly 1", n)
	}
}

// Failed paste attempts on message 1 must not carry into message 2: the second
// batch alerts only once its own oldest pending message is five minutes old.
func TestUndeliverableSecondBatchStartsItsOwnClock(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "TwoBatches", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "zsh"})
	for i := 0; i < 9; i++ { // 9 failed paste attempts, still under five minutes of pending
		at.Advance(31 * time.Second)
		s.WakeDue(ctx)
	}
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, a.ID)
	enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"second"}`, 1)
	at.Advance(31 * time.Second)
	s.WakeDue(ctx)
	if n := notifiedCount(s, "agent.undeliverable"); n != 0 {
		t.Fatalf("a fresh message raised %d alerts", n)
	}
	at.Advance(301 * time.Second)
	s.WakeDue(ctx)
	if n := notifiedCount(s, "agent.undeliverable"); n != 1 {
		t.Fatalf("second batch: %d alerts, want 1", n)
	}
}
```

- [ ] **3.2 Run, watch them fail.** `go test ./internal/runtime/ -run 'TestUndeliverable'`. Expected FAIL: `TestUndeliverableFiresAtFiveMinutesOfPendingNotBefore` reports `at 301 s: 0 alerts, want 1` (the old rule needs ten failed attempts). The second-batch test fails on the leaked counter. `TestUndeliverableAfterTenRetries` and `TestUndeliverableNotificationOnlyFiresOncePerBatch` still pass.

- [ ] **3.3 Minimal change** in `internal/runtime/wake.go`:
  1. Replace `const maxPasteAttempts = 10` with `const undeliverableAfter = 5 * time.Minute`.
  2. Add `NewestPendingAt time.Time` to `wakeRow`, and a fourth subquery in `wakeCandidates` (Task 4 uses it; add the field now so the struct changes once):

```go
		(SELECT MAX(m.created_at) FROM messages m
			WHERE m.to_agent_id = a.id AND m.state = 'pending' AND m.wake_class = 'immediate')
```
     scanned into `var newest sql.NullInt64` after `oldest`, and `r.NewestPendingAt = db.FromMillis(newest.Int64)` next to `r.OldestMessageAt`. (Scan order: `..., &oldest, &newest`.)
  3. Add `raiseUndeliverable` (this is the block that used to sit inside `tryPaste`, moved verbatim, plus the early-return dedupe):

```go
// raiseUndeliverable raises agent.undeliverable once per batch. A notification
// for this agent created at or after the oldest pending message (or the
// session start, whichever is later) means the batch was already reported.
func (s *Store) raiseUndeliverable(ctx context.Context, r wakeRow) error {
	since := r.OldestMessageAt
	if r.StartedAt.After(since) {
		since = r.StartedAt
	}
	already, err := s.alreadyNotifiedUndeliverable(ctx, r.AgentID, since)
	if err != nil || already {
		return err
	}
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
			VALUES (?, 'attention', 'agent.undeliverable', 'Couldn''t deliver messages', 'Couldn''t deliver messages', ?, (SELECT id FROM items WHERE key = ?), ?, ?)`,
			ids.New("ntf"), r.AgentID, r.ItemKey, fmt.Sprintf("agent.undeliverable:%s:", r.AgentName), db.Millis(s.Now()))
	}
	return nil
}
```
  4. In `tryPaste`, replace everything after the `if ok { ... }` paste branch (the `attempts := ...` block through the closing brace of `if attempts >= maxPasteAttempts`) with:

```go
	return s.recordPasteAttempt(ctx, r.SessionID, r.PasteAttempts+1, s.Now())
```
  5. At the top of the `for _, r := range rows` loop in `WakeDue`, before the existing `LastWakeAt` gap check:

```go
		if s.Now().Sub(r.OldestMessageAt) >= undeliverableAfter {
			if err := s.raiseUndeliverable(ctx, r); err != nil {
				return err
			}
		}
```
  6. Update the comment above `TestUndeliverableAfterTenRetries` and rename it `TestUndeliverableAfterFiveMinutesUnsynced` (body unchanged: 11 × 31 s = 341 s crosses the 5-minute mark). Update the `wakeRow.PasteAttempts` comment: it now only spaces retries.

- [ ] **3.4 Run green.** `gofmt -l internal/runtime; go vet ./internal/runtime/; go test ./internal/runtime/...`. Expected PASS. `TestPasteRetryIntervalEnforced` (uses `getPasteAttempts`) still passes: failed attempts are still recorded.

- [ ] **3.5 Commit.**

```bash
git add internal/runtime/wake.go internal/runtime/wake_test.go
git commit -m "fix(runtime): raise agent.undeliverable from pending-message age, not paste attempts"
```

## Task 4: Paste cooldown after a successful wake (spec fix 4)

Files: `internal/runtime/wake.go`, `internal/runtime/wake_test.go`.
Consumes: `wakeRow.NewestPendingAt` (added in Task 3), `wakeGap`, `pasteRetry`.
Produces: cooldown gate in `WakeDue`.

- [ ] **4.1 Failing test.** Append to `wake_test.go`:

```go
// After a successful paste the agent gets pasteRetry (30 s) to respond before
// it is pasted again, unless a newer pending message arrived. Previously only
// the 5 s wakeGap applied, so a mid-turn agent got a paste every ~20 s.
func TestNoRepasteWithinTheCooldownUnlessAMessageIsNewer(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	at := tm.clk
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Cooldown", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"}) // default capture is idle

	at.Advance(25 * time.Second)
	s.WakeDue(ctx)
	if len(tm.pasted) != 1 {
		t.Fatalf("first paste: %d", len(tm.pasted))
	}
	for _, step := range []time.Duration{6, 14, 9} { // +6 s, +20 s, +29 s
		at.Advance(step * time.Second)
		s.WakeDue(ctx)
		if len(tm.pasted) != 1 {
			t.Fatalf("re-pasted inside the cooldown: %d pastes", len(tm.pasted))
		}
	}
	at.Advance(2 * time.Second) // +31 s
	s.WakeDue(ctx)
	if len(tm.pasted) != 2 {
		t.Fatalf("after the cooldown: %d pastes, want 2", len(tm.pasted))
	}

	// A message created after the last wake is woken for at the next tick, not
	// after the cooldown. (It must be strictly newer than last_wake_at.)
	at.Advance(time.Second)
	enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"newer"}`, 1)
	at.Advance(6 * time.Second)
	s.WakeDue(ctx)
	if len(tm.pasted) != 3 {
		t.Fatalf("a newer message must be woken at the next tick: %d pastes", len(tm.pasted))
	}
}
```

- [ ] **4.2 Run, watch it fail.** `go test ./internal/runtime/ -run TestNoRepasteWithinTheCooldownUnlessAMessageIsNewer`. Expected FAIL: `re-pasted inside the cooldown: 2 pastes`.

- [ ] **4.3 Minimal change.** In `WakeDue`, replace the existing first gate

```go
		if r.LastWakeAt != nil && s.Now().Sub(*r.LastWakeAt) < wakeGap {
			continue
		}
```
with

```go
		cool := pasteRetry
		if r.HasControl {
			cool = wakeGap
		}
		if r.LastWakeAt != nil && s.Now().Sub(*r.LastWakeAt) < cool && !r.NewestPendingAt.After(*r.LastWakeAt) {
			continue
		}
```

- [ ] **4.4 Run green.** `go test ./internal/runtime/...`. `TestAtMostOneWakeEveryFiveSeconds`, `TestNativeWakeWithoutASyncFallsBackToThePaste` (31 s gap) and `TestPasteDelays` must stay green; if `TestPasteDelays` fails because it re-wakes inside 30 s for a control message, the control branch (`wakeGap`) is the cause; fix the branch, not the test.

- [ ] **4.5 Commit.**

```bash
git add internal/runtime/wake.go internal/runtime/wake_test.go
git commit -m "fix(runtime): cool down idle pastes for 30 s unless a newer message is pending"
```

## Task 5: Bounded redelivery with an `unacked` list (spec fix 5)

Files: `internal/runtime/inbox.go`, `internal/runtime/inbox_test.go`, `internal/mcpserver/tools.go`, `internal/mcpserver/tools_test.go`.
Consumes: `Store.Sync`, `unackedFor`, `envelopes`, `enq`.
Produces: `UnackedRef`, `SyncResult.Unacked`, `maxFullDeliveries`, `maxUnackedRefs`, `staleUnackedFor`, and `"unacked"` in the `swarm_sync` result.

- [ ] **5.1 Failing runtime tests.** Append to `internal/runtime/inbox_test.go`:

```go
func TestSyncStopsReturningABodyAfterThreeDeliveries(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Bounded", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	first, _ := s.Sync(ctx, ses.ID, nil, 20)
	n := len(first.Messages)
	id := first.Messages[0].MsgID
	for i := 2; i <= 3; i++ {
		res, _ := s.Sync(ctx, ses.ID, nil, 20)
		if len(res.Messages) != n || len(res.Unacked) != 0 {
			t.Fatalf("sync %d: %d messages, %d unacked; want %d, 0", i, len(res.Messages), len(res.Unacked), n)
		}
	}
	fourth, _ := s.Sync(ctx, ses.ID, nil, 20)
	if len(fourth.Messages) != 0 || len(fourth.Unacked) != n {
		t.Fatalf("sync 4: %d messages, %d unacked; want 0, %d", len(fourth.Messages), len(fourth.Unacked), n)
	}
	var ref *UnackedRef
	for i := range fourth.Unacked {
		if fourth.Unacked[i].MsgID == id {
			ref = &fourth.Unacked[i]
		}
	}
	if ref == nil || ref.DeliveryCount != 3 || ref.Kind == "" {
		t.Fatalf("unacked ref = %+v", ref)
	}
	var count int
	s.DB.QueryRowContext(ctx, `SELECT delivery_count FROM messages WHERE id = ?`, id).Scan(&count)
	if count != 3 {
		t.Fatalf("delivery_count = %d, want it capped at 3", count)
	}
	fifth, _ := s.Sync(ctx, ses.ID, []string{id}, 20)
	for _, u := range fifth.Unacked {
		if u.MsgID == id {
			t.Fatal("an acked message must leave the unacked list")
		}
	}
}

func TestSyncAlwaysReturnsControlMessagesInFull(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "ControlFull", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	enq(t, s, a.ID, a.RootItemID, "control", `{"action":"pause"}`, 0)
	for i := 1; i <= 6; i++ {
		res, _ := s.Sync(ctx, ses.ID, nil, 20)
		found := false
		for _, m := range res.Messages {
			found = found || m.Kind == "control"
		}
		if !found {
			t.Fatalf("sync %d dropped the control message", i)
		}
	}
}

// Stale unacked messages must not eat the limit and hide new work.
func TestStaleUnackedMessagesDoNotStarveNewOnes(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "NoStarve", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	for i := 0; i < 25; i++ {
		enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"old"}`, 1)
	}
	for i := 0; i < 3; i++ {
		s.Sync(ctx, ses.ID, nil, 50)
	}
	fresh := enq(t, s, a.ID, a.RootItemID, "answer", `{"body":"new"}`, 1)
	res, _ := s.Sync(ctx, ses.ID, nil, 20)
	if len(res.Messages) != 1 || res.Messages[0].MsgID != fresh.ID {
		t.Fatalf("messages = %+v, want only the new one", res.Messages)
	}
	if len(res.Unacked) < 25 {
		t.Fatalf("unacked = %d, want the 25 stale findings listed", len(res.Unacked))
	}
}
```

- [ ] **5.2 Failing tool test.** Append to `internal/mcpserver/tools_test.go`:

```go
func TestSyncToolReturnsAnUnackedListEvenWhenEmpty(t *testing.T) {
	s, seed := newServerWithSession(t)
	out, err := s.call(context.Background(), seed.Caller, "swarm_sync", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustJSON(out)), `"unacked":[]`) {
		t.Fatalf("out = %s", mustJSON(out))
	}
}
```

- [ ] **5.3 Run, watch them fail.** `go test ./internal/runtime/ ./internal/mcpserver/ -run 'Bounded|ControlFull|NoStarve|Unacked|Redelivers'`. Expected: compile failure (`res.Unacked undefined`, `UnackedRef undefined`). That is the red state. (`TestSyncRedeliversUntilAcked` must remain unchanged and green afterwards: it syncs twice.)

- [ ] **5.4 Minimal change** in `internal/runtime/inbox.go`:

```go
const maxFullDeliveries = 3
const maxUnackedRefs = 50

// UnackedRef is a delivered, non-control message that has used its full
// deliveries: the agent sees its id and kind, never the body again, and must ack.
type UnackedRef struct {
	MsgID         string      `json:"msg_id"`
	Seq           int64       `json:"seq"`
	Kind          MessageKind `json:"kind"`
	DeliveryCount int         `json:"delivery_count"`
}
```

  - Add `Unacked []UnackedRef` to `SyncResult` (after `Messages`).
  - In `Sync`, directly after the ack loop and before `unackedFor`, add:

```go
		if out.Unacked, err = s.staleUnackedFor(ctx, tx, a.ID); err != nil {
			return err
		}
```
    (It must run before `envelopes`, otherwise a message reaching its third delivery in this very sync would be listed twice.)
  - In `unackedFor` add to the WHERE clause `AND NOT (state = 'delivered' AND kind <> 'control' AND delivery_count >= ?)` and bind `maxFullDeliveries` before `limit`.
  - Add:

```go
func (s *Store) staleUnackedFor(ctx context.Context, tx *sql.Tx, agentID string) ([]UnackedRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, seq, kind, delivery_count FROM messages
		WHERE to_agent_id = ? AND state = 'delivered' AND kind <> 'control' AND delivery_count >= ?
		ORDER BY seq LIMIT ?`, agentID, maxFullDeliveries, maxUnackedRefs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnackedRef
	for rows.Next() {
		var u UnackedRef
		var kind string
		if err := rows.Scan(&u.MsgID, &u.Seq, &kind, &u.DeliveryCount); err != nil {
			return nil, err
		}
		u.Kind = MessageKind(kind)
		out = append(out, u)
	}
	return out, rows.Err()
}
```
  - Update the `Sync` doc comment ("ack is applied first; un-acked messages are returned by priority then seq until delivered three times, after which they are listed in Unacked").

  In `internal/mcpserver/tools.go` `syncTool`: before the return add `if res.Unacked == nil { res.Unacked = []runtime.UnackedRef{} }`, add `"unacked": res.Unacked,` to the map, and replace the tool `Description` with the spec's text: "Acknowledge handled messages (`ack`) and fetch the agent's inbox: assignments, questions, control notices and advice, newest-priority first. Messages delivered three times without an ack are listed in `unacked` by id and kind only."

- [ ] **5.5 Run green.** `gofmt -l internal; go vet ./...; go test ./internal/runtime/... ./internal/mcpserver/... ./internal/hook/...`. Expected PASS. If an mcpserver test compares the sync tool description string, update that assertion to the new text.

- [ ] **5.6 Commit.**

```bash
git add internal/runtime/inbox.go internal/runtime/inbox_test.go internal/mcpserver/tools.go internal/mcpserver/tools_test.go
git commit -m "feat(runtime): stop redelivering message bodies after three syncs; list them in unacked"
```

## Task 6: Auto-ack the assignment on the `accepted` checkpoint (spec fix 6)

Files: `internal/runtime/checkpoint.go` (inside `case Accepted:`, around line 314), `internal/runtime/checkpoint_test.go`.
Consumes: `worker(t, s)` (`checkpoint_test.go:17`), `WriteCheckpoint`, `Sync`.
Produces: assignment rows `acked` by an `accepted` checkpoint when they were `delivered`.

- [ ] **6.1 Failing test.** Append to `checkpoint_test.go`:

```go
// The assignment duplicates the kickoff brief, and SKILL.md rule 1 makes the
// agent write `accepted` right after its first sync, so accepting acks it.
// Only a delivered assignment: one the agent never synced stays pending.
func TestAcceptedAcksADeliveredAssignmentOnly(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	state := func() string {
		var st string
		s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE to_agent_id = ? AND kind = 'assignment'`, w.ID).Scan(&st)
		return st
	}
	if _, err := s.Sync(ctx, wSes.ID, nil, 20); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "delivered" {
		t.Fatalf("assignment after sync = %q, want delivered", got)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "acked" {
		t.Fatalf("assignment after accepted = %q, want acked", got)
	}
}

func TestAcceptedLeavesAnUnreadAssignmentPending(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	var st string
	s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE to_agent_id = ? AND kind = 'assignment'`, w.ID).Scan(&st)
	if st != "pending" {
		t.Fatalf("an assignment the agent never synced must stay pending, got %q", st)
	}
}
```

- [ ] **6.2 Run, watch the first fail.** `go test ./internal/runtime/ -run 'TestAccepted(Acks|Leaves)'`. Expected FAIL: `assignment after accepted = "delivered", want acked`. The second test already passes.

- [ ] **6.3 Minimal change.** In `checkpoint.go`, inside `case Accepted:` after the `tryTransition` call, before the case ends:

```go
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'acked', acked_at = ?
				WHERE to_agent_id = ? AND kind = 'assignment' AND state = 'delivered'`,
				db.Millis(s.Now()), a.ID); err != nil {
				return err
			}
```

- [ ] **6.4 Run green.** `go test ./internal/runtime/...`. Expected PASS; `TestAcceptedStartsTheItemAndRelaysToTheParent` unaffected.

- [ ] **6.5 Commit.**

```bash
git add internal/runtime/checkpoint.go internal/runtime/checkpoint_test.go
git commit -m "feat(runtime): ack the delivered assignment when the agent writes accepted"
```

## Task 7: Tell agents to ack right after handling (spec copy)

Files: `skills/swarm/SKILL.md` (rule 2).
No code test exists for skill prose; verification is a grep.

- [ ] **7.1 Change.** Replace rule 2 with the exact text in the spec's "All user-facing copy" section:

> 2. When you see a `[swarm]` notice or the line `swarm: inbox (call swarm_sync)`, call `swarm_sync`. Handle messages in order. As soon as you have handled a message, acknowledge it: pass its `msg_id` in `ack` on your next `swarm_sync`, or in `processed` on your next checkpoint. An unacknowledged message comes back on every sync; after three deliveries it stops coming back in full and appears only in the `unacked` list (id and kind), so acknowledge it.

- [ ] **7.2 Verify.** `grep -n "unacked" skills/swarm/SKILL.md` prints rule 2. If the repo has a skills lint (`grep -rn "validate-skills" Makefile`), run it.

- [ ] **7.3 Commit.**

```bash
git add skills/swarm/SKILL.md
git commit -m "docs(skill): ack handled messages in swarm_sync; explain the unacked list"
```

## Task 8: Full verification, then live check

- [ ] **8.1** In the worktree, in this order: `gofmt -l .` (no output), `go vet ./...`, `go test ./...`. All green. Report any pre-existing red separately.
- [ ] **8.2** Follow the local-deploy notes to build and swap the daemon and menubar app, quitting the running app before replacing its bundle (a separate spec covers the stale-icon cause; do not replace a bundle under a running process).
- [ ] **8.3 Live checks** (none of these send keystrokes to an agent; they only read):
  1. `sqlite3 -readonly ~/.swarm/swarm.db "SELECT state, delivery_count FROM messages WHERE id='msg_01M31G7G6PT7G7DKKPH91EZK5H'"` moves from `pending|0` to `delivered` or `acked` within about 25 s of the daemon starting, if `s3-merge-d` is still at its idle prompt.
  2. `tmux -L swarm capture-pane -p -e -J -t <idle agent> -S -15 | grep -c '7m'` is 1 for an idle cursor prompt (the shape now matched).
  3. Over the next hour: `sqlite3 -readonly ~/.swarm/swarm.db "SELECT a.name, datetime(n.created_at/1000,'unixepoch','localtime') FROM notifications n JOIN agents a ON a.id = n.agent_id WHERE n.kind='agent.undeliverable' AND n.created_at > <deploy epoch ms>"` lists only agents that have a `pending` immediate message older than 5 minutes.
- [ ] **8.4 Remove the worktree after merge** with `git worktree remove ../agent-swarm--delivery-reliability` from the main checkout, after confirming `git log fix/message-delivery-reliability -1` is on `main`. Never `rm -rf`.

## Consumes / produces summary

| Task | Consumes | Produces |
|---|---|---|
| 1 | `claudeIdle`, `pane()` | new `claudeIdle` |
| 2 | `wakeCandidates`, `hook.load` | pending-only counts |
| 3 | `alreadyNotifiedUndeliverable`, `notify` | `undeliverableAfter`, `raiseUndeliverable`, `NewestPendingAt` |
| 4 | `NewestPendingAt`, `pasteRetry`, `wakeGap` | cooldown gate in `WakeDue` |
| 5 | `Sync`, `unackedFor`, `envelopes` | `UnackedRef`, `SyncResult.Unacked`, `staleUnackedFor`, `swarm_sync.unacked` |
| 6 | `WriteCheckpoint`, `worker()` | assignment auto-ack |
| 7 | spec copy | SKILL.md rule 2 |

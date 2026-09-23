package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// enq is a shortcut for the test cases below.
func enq(t *testing.T, s *Store, toAgentID, rootItemID string, kind MessageKind, payload string, priority int) Message {
	t.Helper()
	var out Message
	err := s.tx(context.Background(), func(tx *sql.Tx) error {
		m, err := s.enqueue(context.Background(), tx, Message{Kind: kind, Origin: "daemon",
			ToAgentID: toAgentID, RootItemID: rootItemID, Priority: priority,
			Payload: json.RawMessage(payload)})
		out = m
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSyncReturnsMessagesByPriorityThenSeq(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Inbox", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	// the assignment is already there; add a normal finding then a control
	enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"later"}`, 1)
	enq(t, s, a.ID, a.RootItemID, "control", `{"action":"pause","deadline_at":"2026-09-17T12:02:00Z","scope":"session"}`, 0)
	res, err := s.Sync(ctx, ses.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("got %d messages", len(res.Messages))
	}
	if res.Messages[0].Kind != "control" {
		t.Fatalf("priority 0 comes first, got %s", res.Messages[0].Kind)
	}
	if res.Messages[1].Seq >= res.Messages[2].Seq {
		t.Fatal("within a priority the order is by seq")
	}
	if res.SessionState != Running && res.SessionState != Spawning {
		t.Fatalf("session_state = %s", res.SessionState)
	}
	if res.More {
		t.Fatal("more should be false")
	}
	// the envelope carries the recipient's name and the root key
	if res.Messages[0].To.Name != a.Name || res.Messages[0].RootItem == "" {
		t.Fatalf("envelope = %+v", res.Messages[0])
	}
}

func TestSyncRedeliversUntilAcked(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Redeliver", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	first, _ := s.Sync(ctx, ses.ID, nil, 20)
	id := first.Messages[0].MsgID
	second, _ := s.Sync(ctx, ses.ID, nil, 20)
	if len(second.Messages) != len(first.Messages) {
		t.Fatal("an un-acked message comes back")
	}
	var count int
	s.DB.QueryRowContext(ctx, `SELECT delivery_count FROM messages WHERE id = ?`, id).Scan(&count)
	if count != 2 {
		t.Fatalf("delivery_count = %d, want 2", count)
	}
	third, _ := s.Sync(ctx, ses.ID, []string{id}, 20)
	for _, m := range third.Messages {
		if m.MsgID == id {
			t.Fatal("an acked message must not come back")
		}
	}
}

func TestSyncRefusesAckingSomeoneElsesMessage(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Mine", Intent: "feature", Kind: Fake, Model: "fake-1"})
	_, b, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Theirs", Intent: "feature", Kind: Fake, Model: "fake-1"})
	sesA, _ := s.LatestSession(ctx, a.ID)
	mb := enq(t, s, b.ID, b.RootItemID, "finding", `{"body":"x"}`, 1)
	if _, err := s.Sync(ctx, sesA.ID, []string{mb.ID}, 20); err == nil {
		t.Fatal("acking another agent's message is an error")
	}
	var state string
	s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE id = ?`, mb.ID).Scan(&state)
	if state != "pending" {
		t.Fatalf("the other agent's message changed to %q", state)
	}
	if _, err := s.Sync(ctx, sesA.ID, []string{"msg_nope"}, 20); err == nil {
		t.Fatal("acking an unknown message is an error")
	}
}

func TestSyncLimitAndMore(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Many", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	for i := 0; i < 5; i++ {
		enq(t, s, a.ID, a.RootItemID, "finding", `{"body":"x"}`, 1)
	}
	res, err := s.Sync(ctx, ses.ID, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 2 || !res.More {
		t.Fatalf("got %d messages, more = %v", len(res.Messages), res.More)
	}
	// limit 0 means the default of 20
	all, _ := s.Sync(ctx, ses.ID, nil, 0)
	if len(all.Messages) != 6 {
		t.Fatalf("default limit returned %d", len(all.Messages))
	}
}

// I19: deferred messages never wake anyone and arrive folded into one digest.
// 2026-09-22: relay is no longer a deferred kind (every relay checkpoint now
// wakes immediately), so this test's original vehicle -- three deferred
// `relay` rows -- can no longer occur in production. digest is the only kind
// still deferred; this retargets the synthetic enq() rows to it so the
// foldDigest mechanism itself (kept, per spec, as out-of-scope-to-remove)
// stays covered.
func TestDeferredRelaysAreFoldedIntoOneDigest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Digest", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Sync(ctx, ses.ID, nil, 20) // clear the assignment from the picture
	var seedIDs []string
	for i, key := range []string{"TASK-1", "TASK-2", "TASK-3"} {
		m := enq(t, s, a.ID, a.RootItemID, "digest",
			`{"event":"progress","agent":"w`+string(rune('1'+i))+`","item":"`+key+`","checkpoint":{"summary":"step `+string(rune('1'+i))+` done"}}`, 1)
		seedIDs = append(seedIDs, m.ID)
	}
	res, err := s.Sync(ctx, ses.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	var digests int
	for _, m := range res.Messages {
		if m.Kind == "digest" {
			digests++
			var p struct {
				Lines []string `json:"lines"`
			}
			json.Unmarshal(m.Payload, &p)
			if len(p.Lines) != 3 {
				t.Fatalf("digest lines = %v", p.Lines)
			}
			if !strings.Contains(p.Lines[0], "TASK-1 · w1 · step 1 done") {
				t.Fatalf("digest line = %q, want 'KEY · agent · summary'", p.Lines[0])
			}
			if len(m.Payload) > 1500 {
				t.Fatalf("the digest is capped at 1500 characters, got %d", len(m.Payload))
			}
		}
	}
	// the three synthetic deferred rows fold into exactly one new digest
	if digests != 1 {
		t.Fatalf("digest count = %d, want 1", digests)
	}
	// the three seed rows are acked by the new digest's own id (the new digest
	// itself is excluded -- it was just delivered by this same Sync, not acked)
	for _, id := range seedIDs {
		var state string
		s.DB.QueryRowContext(ctx, `SELECT state FROM messages WHERE id = ?`, id).Scan(&state)
		if state != "acked" {
			t.Fatalf("seed digest %s state = %q, want acked", id, state)
		}
	}
}

func TestSyncRefusesAnUnknownSession(t *testing.T) {
	s, _, _ := newStore(t)
	if _, err := s.Sync(context.Background(), "ses_nope", nil, 20); err == nil {
		t.Fatal("an unknown session must be refused")
	}
}

func TestIdempotentEdgeCases(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Idem2", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	calls := 0
	run := func(reqID string) (json.RawMessage, error) {
		var out json.RawMessage
		err := s.tx(ctx, func(tx *sql.Tx) error {
			var err error
			out, err = s.Idempotent(ctx, tx, ses.ID, reqID, "swarm_x", func() (any, error) {
				calls++
				return map[string]int{"n": calls}, nil
			})
			return err
		})
		return out, err
	}
	if _, err := run(""); err != nil {
		t.Fatal(err)
	}
	if _, err := run(""); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("an empty request_id must run every time, calls = %d", calls)
	}
	if _, err := run(strings.Repeat("x", 65)); err == nil {
		t.Fatal("a request_id over 64 characters must be refused")
	}
}

func TestPendingCountCountsUnackedMessages(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Pending", Intent: "feature", Kind: Fake, Model: "fake-1"})
	n, err := s.PendingCount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pending = %d, want 1 (the assignment)", n)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	res, _ := s.Sync(ctx, ses.ID, nil, 20)
	if _, err := s.Sync(ctx, ses.ID, []string{res.Messages[0].MsgID}, 20); err != nil {
		t.Fatal(err)
	}
	n, err = s.PendingCount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pending after ack = %d, want 0", n)
	}
}

// 2026-09-22: every relay now wakes immediately regardless of its event --
// progress and handoff (historically deferred, folded into a digest) moved
// into the immediate case along with the events that were always immediate.
func TestWakeClassFor(t *testing.T) {
	immediate := []MessageKind{"control", "question", "answer", "finding",
		"approval_result", "user_answer", "repos_confirmed",
		"assignment_update", "advice", "assignment", "relay"}
	for _, kind := range immediate {
		if got := WakeClassFor(kind); got != "immediate" {
			t.Errorf("%s = %s, want immediate", kind, got)
		}
	}
	if got := WakeClassFor("digest"); got != "deferred" {
		t.Errorf("digest = %s, want deferred", got)
	}
}

// §8.1 swarm_send: "parent" resolves, and cross-root sends are refused.
func TestSendResolvesParentAndRefusesCrossRoot(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, _ := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	worker, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	wSes, _ := s.LatestSession(ctx, worker.ID)
	id, err := s.Send(ctx, wSes.ID, "parent", "finding", "the login test fails on an empty email", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var to string
	s.DB.QueryRowContext(ctx, `SELECT to_agent_id FROM messages WHERE id = ?`, id).Scan(&to)
	if to != orch.ID {
		t.Fatalf("to = %q, want the parent %q", to, orch.ID)
	}
	// another root
	_, other, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Other", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if _, err := s.Send(ctx, wSes.ID, other.Name, "finding", "hello", "", ""); err == nil {
		t.Fatal("a send to another top-level item must be refused")
	}
	if _, err := s.Send(ctx, wSes.ID, "nobody", "finding", "hello", "", ""); err == nil {
		t.Fatal("an unknown name must be refused")
	}
	if _, err := s.Send(ctx, wSes.ID, "parent", "finding", strings.Repeat("x", 4001), "", ""); err == nil {
		t.Fatal("a body over 4000 characters must be refused")
	}
	// the origin is always 'agent' — no MCP path may write user_action (L7)
	var origin string
	s.DB.QueryRowContext(ctx, `SELECT origin FROM messages WHERE id = ?`, id).Scan(&origin)
	if origin != "agent" {
		t.Fatalf("origin = %q", origin)
	}
}

// A send to a target with no live session must be refused synchronously,
// not silently enqueued into a black hole nobody will ever ack (the
// production incident this covers: an orchestrator relayed to a child whose
// session had already crashed/completed/failed).
func TestSendRefusesATargetWithNoLiveSession(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)

	var before int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ?`, w.ID).Scan(&before)

	// the worker's only session ends without ever being retried or replaced
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'crashed' WHERE id = ?`, wSes.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Send(ctx, orchSes, w.Name, "finding", "still there?", "", ""); err == nil {
		t.Fatal("a send to a target with no live session must be refused")
	} else if !strings.Contains(err.Error(), w.Name) {
		t.Fatalf("error should name the unreachable target, got %q", err.Error())
	}

	var after int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ?`, w.ID).Scan(&after)
	if after != before {
		t.Fatalf("a refused send must not enqueue anything: before=%d after=%d", before, after)
	}

	// the reverse direction (dead session sending) still resolves fine —
	// only the TARGET's liveness is checked, not the sender's.
	if _, err := s.Send(ctx, wSes.ID, "parent", "finding", "hello", "", ""); err != nil {
		t.Fatalf("a live target must still accept a send from an ended session: %v", err)
	}
}

// Bugs 1 & 4: a paused or interrupted target's latest session is not "live"
// by SessionState.Live()'s narrow definition, but Resume (pause.go) delivers
// to it on the very next generation, and a queued agent's first session is
// delivered once DrainQueue admits it (limits.go) -- none of these three are
// the "no live session" black hole Send()'s check exists to catch, so all
// three must succeed, and Reconcile must never raise no_recipient for them.
func TestSendSucceedsToPausedInterruptedAndQueuedTargets(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)
	panes(tm, Pane{Session: orch.Name, Command: "swarm-fake-agent"})
	tm.env[orch.Name] = map[string]string{"SWARM_SESSION": orchSes}
	tm.captures[orch.Name] = []string{"working…\n"}

	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, wSes.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, orchSes, w.Name, "finding", "still there?", "", ""); err != nil {
		t.Fatalf("send to a paused target must succeed: %v", err)
	}

	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'interrupted' WHERE id = ?`, wSes.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, orchSes, w.Name, "finding", "hello again", "", ""); err != nil {
		t.Fatalf("send to an interrupted target must succeed: %v", err)
	}

	// Reconcile must not raise no_recipient while the target is still
	// interrupted, before it gets flipped back to paused below for the
	// unrelated queuing setup.
	at.Advance(3 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_recipient"); n != 0 {
		t.Fatalf("no_recipient must not fire for an interrupted target: count = %d", n)
	}

	// Back to paused for the queuing setup below: an interrupted session no
	// longer holds a concurrency slot (2026-09-22 zombie-slot fix, limits.go),
	// so it can't be what forces the second worker to queue. Paused still does.
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = ?`, wSes.ID); err != nil {
		t.Fatal(err)
	}
	setLimits(t, s, 2, 5) // orch + w already fill the shared pool
	t2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: "STORY-1",
		Title: "Second task"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id = ?`, t2.ID); err != nil {
		t.Fatal(err)
	}
	queued, isQueued, err := s.Spawn(ctx, SpawnInput{ItemKey: t2.Key, Role: RoleCoder, Kind: Fake,
		Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if !isQueued || queued.State != AgentQueued {
		t.Fatalf("second worker must queue: queued=%v state=%s", isQueued, queued.State)
	}
	if _, err := s.Send(ctx, orchSes, queued.Name, "finding", "you're up next", "", ""); err != nil {
		t.Fatalf("send to a queued target must succeed: %v", err)
	}

	at.Advance(3 * time.Minute)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.no_recipient"); n != 0 {
		t.Fatalf("no_recipient must not fire for a paused/interrupted/queued target: count = %d", n)
	}
}

// I11: a replayed request_id returns the first result, also after a restart.
func TestIdempotentReplaysTheStoredResult(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Idem", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	calls := 0
	run := func() (json.RawMessage, error) {
		var out json.RawMessage
		err := s.tx(ctx, func(tx *sql.Tx) error {
			var err error
			out, err = s.Idempotent(ctx, tx, ses.ID, "req-1", "swarm_checkpoint", func() (any, error) {
				calls++
				return map[string]string{"checkpoint_id": "ckp_1"}, nil
			})
			return err
		})
		return out, err
	}
	first, err := run()
	if err != nil {
		t.Fatal(err)
	}
	second, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("the body ran %d times", calls)
	}
	if string(first) != string(second) {
		t.Fatalf("replay = %s, first = %s", second, first)
	}
	// a different session with the same request_id is a different caller
	_, b, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Other", Intent: "feature", Kind: Fake, Model: "fake-1"})
	bSes, _ := s.LatestSession(ctx, b.ID)
	s.tx(ctx, func(tx *sql.Tx) error {
		_, err := s.Idempotent(ctx, tx, bSes.ID, "req-1", "swarm_checkpoint", func() (any, error) {
			calls++
			return map[string]string{"checkpoint_id": "ckp_2"}, nil
		})
		return err
	})
	if calls != 2 {
		t.Fatalf("the second caller must run the body, calls = %d", calls)
	}
}

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

// The orchestrator mirror of notifyNoAck's child-silent case: a message
// delivered maxFullDeliveries times without ever being acked must actively
// escalate, not just fall silently into SyncResult.Unacked.
func TestUnackedMessageEscalatesAtThirdDelivery(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Clear the orchestrator's own kickoff assignment first so the only
	// pending message left is the relay under test (same idiom as
	// TestProgressCheckpointWakesImmediately).
	s.Sync(ctx, orchSes.ID, nil, 20)
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway report"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFullDeliveries; i++ {
		if _, err := s.Sync(ctx, orchSes.ID, nil, 20); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.message_unacked"); n != 1 {
		t.Fatalf("agent.message_unacked raised %d times, want 1", n)
	}
	n := notified(t, s, "agent.message_unacked")
	if n.AgentName != orch.Name {
		t.Fatalf("notification agent = %q, want %q", n.AgentName, orch.Name)
	}
	if n.ItemKey != "TASK-1" {
		t.Fatalf("notification item = %q, want TASK-1", n.ItemKey)
	}
}

// clearInbox acks every pending message for agentID via a raw update, the
// same idiom TestProgressCheckpointWakesImmediately uses to strip a
// kickoff assignment out of the way before driving the message under test.
func clearInbox(t *testing.T, s *Store, agentID string) {
	t.Helper()
	if _, err := s.DB.ExecContext(context.Background(),
		`UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, agentID); err != nil {
		t.Fatal(err)
	}
}

// A live parent above the non-acking recipient gets the relay, same pattern
// notifyNoAck/OnDepUnblocked use for a silent child.
func TestUnackedMessageEscalationRelaysToLiveAncestor(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	root, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mid, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		ParentAgentID: root.ID, Brief: BriefInput{Objective: "mid"}})
	if err != nil {
		t.Fatal(err)
	}
	midSes, err := s.LatestSession(ctx, mid.ID)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		ParentAgentID: mid.ID, Brief: BriefInput{Objective: "leaf"}})
	if err != nil {
		t.Fatal(err)
	}
	leafSes, err := s.LatestSession(ctx, leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Sync(ctx, midSes.ID, nil, 20)
	clearInbox(t, s, mid.ID)
	if _, err := s.WriteCheckpoint(ctx, leafSes.ID, CheckpointInput{Kind: Progress, Summary: "midway report"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFullDeliveries; i++ {
		if _, err := s.Sync(ctx, midSes.ID, nil, 20); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.message_unacked"); n != 1 {
		t.Fatalf("agent.message_unacked raised %d times, want 1", n)
	}
	var toRoot int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'relay'
		AND payload_json LIKE '%"event":"message_unacked"%'`, root.ID).Scan(&toRoot)
	if toRoot != 1 {
		t.Fatalf("relay to nearest live ancestor count = %d, want 1", toRoot)
	}
}

// A top-level orchestrator has no ancestor to relay to: only the
// notification fires, silently, no error.
func TestUnackedMessageEscalationSkipsRelayForTopLevelOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Sync(ctx, orchSes.ID, nil, 20)
	clearInbox(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway report"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFullDeliveries; i++ {
		if _, err := s.Sync(ctx, orchSes.ID, nil, 20); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.message_unacked"); n != 1 {
		t.Fatalf("agent.message_unacked raised %d times, want 1", n)
	}
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'
		AND payload_json LIKE '%"event":"message_unacked"%'`).Scan(&relays)
	if relays != 0 {
		t.Fatalf("a top-level orchestrator has no ancestor to relay to, got %d relays", relays)
	}
}

// Acking on/before the 2nd delivery must suppress the escalation entirely --
// it never has a 3rd delivery to fire on.
func TestAckingBeforeThirdDeliverySuppressesEscalation(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Sync(ctx, orchSes.ID, nil, 20)
	clearInbox(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway report"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.Sync(ctx, orchSes.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(first.Messages))
	}
	id := first.Messages[0].MsgID
	if _, err := s.Sync(ctx, orchSes.ID, []string{id}, 20); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Sync(ctx, orchSes.ID, nil, 20); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.message_unacked"); n != 0 {
		t.Fatalf("agent.message_unacked raised %d times, want 0 for an acked message", n)
	}
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'
		AND payload_json LIKE '%"event":"message_unacked"%'`).Scan(&relays)
	if relays != 0 {
		t.Fatalf("an acked message must never relay, got %d", relays)
	}
}

// control-kind messages are exempt from maxFullDeliveries capping everywhere
// else in this file; the escalation must respect the same exemption.
func TestControlMessagesNeverEscalate(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "ControlNoEscalate", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Sync(ctx, ses.ID, nil, 20)
	clearInbox(t, s, a.ID)
	enq(t, s, a.ID, a.RootItemID, "control", `{"action":"pause"}`, 0)
	for i := 0; i < 6; i++ {
		if _, err := s.Sync(ctx, ses.ID, nil, 20); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.message_unacked"); n != 0 {
		t.Fatalf("a control message must never escalate, got %d notifications", n)
	}
}

// After the escalating 3rd delivery, further Sync calls for the same
// still-unacked message (now surfaced only via SyncResult.Unacked) must not
// raise a second notification or a second relay.
func TestUnackedEscalationFiresExactlyOnce(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	orchSes, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Sync(ctx, orchSes.ID, nil, 20)
	clearInbox(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Progress, Summary: "midway report"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := s.Sync(ctx, orchSes.ID, nil, 20); err != nil {
			t.Fatal(err)
		}
	}
	if n := notifiedCount(s, "agent.message_unacked"); n != 1 {
		t.Fatalf("agent.message_unacked raised %d times across 6 syncs, want exactly 1", n)
	}
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'
		AND payload_json LIKE '%"event":"message_unacked"%'`).Scan(&relays)
	if relays != 0 {
		t.Fatalf("relays = %d, want 0 (top-level orchestrator has no ancestor)", relays)
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

func TestSummarizeForEveryKind(t *testing.T) {
	cases := []struct {
		name    string
		kind    MessageKind
		payload string
		want    string // substring the output must contain
	}{
		{"assignment", "assignment", `{"brief":"Fix the flaky retry test","item_key":"TASK-42"}`,
			`"Fix the flaky retry test"`},
		{"question body", "question", `{"body":"Should this run before the migration?"}`,
			`"Should this run before the migration?"`},
		{"answer body", "answer", `{"body":"Yes, run it first."}`, `"Yes, run it first."`},
		{"control pause", "control", `{"action":"pause","deadline_at":"2026-09-23T12:00:00Z","scope":"root"}`,
			"pause"},
		{"approval_result approved", "approval_result", `{"decision":"approved"}`, "approved"},
		{"approval_result changes", "approval_result",
			`{"decision":"changes_requested","comment":"tighten the retry loop"}`,
			`"tighten the retry loop"`},
		{"user_answer", "user_answer", `{"request_id":"req_1","text":"Use main."}`, `"Use main."`},
		{"advice pending", "advice", `{"advice_id":"a1","question":"Should I retry?","state":"pending"}`,
			`"Should I retry?"`},
		{"advice answered", "advice",
			`{"advice_id":"a1","question":"Should I retry?","answer":"Yes.","state":"answered"}`,
			`"Yes."`},
		{"relay checkpoint", "relay",
			`{"event":"progress","agent":"s3-fix-b","item":"TASK-9","checkpoint":{"summary":"starting on the auth regression"}}`,
			`"starting on the auth regression"`},
		{"relay failed", "relay", `{"event":"failed","agent":"s3-fix-c","item":"TASK-9"}`, "failed"},
		{"digest", "digest", `{"lines":["TASK-1 · a · did x","TASK-2 · b · did y"]}`, "TASK-1"},
		{"assignment_update note", "assignment_update", `{"note":"Retrying with the fallback model"}`,
			`"Retrying with the fallback model"`},
		{"unrecognized kind falls back to raw JSON preview", MessageKind("bogus"),
			`{"weird_field":"some value that is not in any known shape"}`,
			"weird_field"},
		{
			"body containing newline is sanitized",
			"question", `{"body":"line one\nline two"}`, "line one line two",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := summarizeFor(MessageKind(c.kind), json.RawMessage(c.payload))
			if !strings.Contains(got, c.want) {
				t.Errorf("summarizeFor(%s, %s) = %q, want substring %q", c.kind, c.payload, got, c.want)
			}
			if strings.Contains(got, "\n") {
				t.Errorf("summarizeFor(%s) contains a literal newline: %q", c.kind, got)
			}
		})
	}
}

// TestSummarizeForFallbackTruncatesRuneSafe: the unrecognized-kind fallback
// truncates raw JSON at ~120 bytes; a payload whose 120th byte lands
// mid multi-byte rune (an em dash, 3 bytes in UTF-8) must not mangle it --
// same bug class Task 1 fixed in Inbox's own truncation.
//
// utf8.ValidString alone can't catch this: sanitizeOneLine (called after
// truncation) ranges over the string rune-by-rune, and Go's range already
// turns any incomplete trailing byte sequence into U+FFFD (a *valid*
// replacement rune) -- so the output is always valid UTF-8 regardless of
// the bug. The actual signature of the bug is that U+FFFD appearing in the
// output at all: a rune-safe cut never produces it here (it either keeps
// the whole em dash or drops it, never a partial one).
//
// Swept over n rather than one fixed offset: the JSON prefix
// (`{"weird_field":"`) shifts where any single fixed-n em dash actually
// lands relative to byte 120, so one magic n could pass by luck even with
// the bug present. Sweeping guarantees at least one n crosses the boundary
// (verified against the pre-fix code: n=102 and n=103 both produced
// U+FFFD; n=101 landed on a clean boundary and did not).
func TestSummarizeForFallbackTruncatesRuneSafe(t *testing.T) {
	for n := 95; n <= 120; n++ {
		longVal := strings.Repeat("x", n) + "—" + "yyy"
		payload, _ := json.Marshal(map[string]string{"weird_field": longVal})
		got := summarizeFor(MessageKind("some_unrecognized_kind"), payload)
		if !utf8.ValidString(got) {
			t.Fatalf("n=%d: summarizeFor fallback produced invalid UTF-8: %q", n, got)
		}
		if strings.ContainsRune(got, utf8.RuneError) {
			t.Fatalf("n=%d: summarizeFor fallback truncated mid-rune, mangled the em dash into U+FFFD: %q", n, got)
		}
	}
}

func TestInboxNoticeListsPendingMessagesOldestFirst(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Inbox", Intent: "feature", Kind: Fake, Model: "fake-1"})
	enq(t, s, a.ID, a.RootItemID, "question", `{"body":"first question here?"}`, 1)
	enq(t, s, a.ID, a.RootItemID, "question", `{"body":"second question here?"}`, 1)
	notice, err := s.InboxNotice(ctx, a.ID, a.Name, "TASK-42")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "first question here?") || !strings.Contains(notice, "second question here?") {
		t.Errorf("InboxNotice missing message content: %q", notice)
	}
	if strings.Index(notice, "first question here?") > strings.Index(notice, "second question here?") {
		t.Errorf("InboxNotice not oldest-first: %q", notice)
	}
	// v2: real newlines between items are the template's own structure, not
	// message content (spec Locked decision 2) -- each item is its own line.
	if !strings.Contains(notice, "\n- ") {
		t.Errorf("InboxNotice should render one item per line: %q", notice)
	}
}

func TestInboxNoticeCapsAtEightItemsWithMoreCount(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Inbox", Intent: "feature", Kind: Fake, Model: "fake-1"})
	// StartSpike already leaves one pending assignment; 9 more questions
	// make 10 pending, so a limit of 8 leaves moreCount == 2.
	for i := 0; i < 9; i++ {
		enq(t, s, a.ID, a.RootItemID, "question", `{"body":"queued question"}`, 1)
	}
	items, more, err := s.pendingInboxItems(ctx, a.ID, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 8 {
		t.Errorf("pendingInboxItems returned %d items, want 8", len(items))
	}
	if more != 2 {
		t.Errorf("pendingInboxItems moreCount = %d, want 2", more)
	}
	notice, err := s.InboxNotice(ctx, a.ID, a.Name, "TASK-42")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "+2 more") {
		t.Errorf("InboxNotice should mention +2 more: %q", notice)
	}
	if len(notice) > maxInboxNotice {
		t.Errorf("InboxNotice %d bytes, want <= %d", len(notice), maxInboxNotice)
	}
}

// While the target's kind is confirmed exhausted, a daemon relay must be held
// (one suppressed_relays row), not enqueued; a repeat hold bumps the same row.
func TestHoldIfExhaustedSuppressesRelay(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	s.Usage = fakeUsage{Fake: true}
	hold := func() bool {
		t.Helper()
		var held bool
		err := s.tx(ctx, func(tx *sql.Tx) error {
			var err error
			held, err = s.holdIfExhausted(ctx, tx, orch.ID, "no_ack", json.RawMessage(`{"event":"no_ack"}`))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	if !hold() {
		t.Fatal("holdIfExhausted = false for an exhausted kind, want true")
	}
	if !hold() {
		t.Fatal("second holdIfExhausted = false, want true")
	}
	var msgs, rows, count int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ?`, orch.ID).Scan(&msgs)
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays WHERE agent_id = ?`, orch.ID).Scan(&rows)
	s.DB.QueryRowContext(ctx, `SELECT count FROM suppressed_relays WHERE agent_id = ? AND event = 'no_ack'`, orch.ID).Scan(&count)
	if rows != 1 || count != 2 {
		t.Fatalf("suppressed rows = %d (count %d), want 1 row with count 2", rows, count)
	}
	_ = msgs
}

// While the ancestor's kind is exhausted, an unacked-message escalation still
// notifies but holds the relay.
func TestEscalateUnackedHeldWhileExhausted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, _ := worker(t, s)
	s.Usage = fakeUsage{Fake: true}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		return s.escalateUnacked(ctx, tx, w, Message{Kind: "finding",
			ItemID: w.ItemID, RootItemID: w.RootItemID, Payload: json.RawMessage(`{"body":"hi"}`)})
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := notifiedCount(s, "agent.message_unacked"); n != 1 {
		t.Fatalf("agent.message_unacked count = %d, want 1 (notify still fires)", n)
	}
	var relays, rows int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'
		AND payload_json LIKE '%"event":"message_unacked"%'`).Scan(&relays)
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays WHERE event = 'message_unacked'`).Scan(&rows)
	if relays != 0 {
		t.Fatalf("message_unacked relays = %d, want 0 held while exhausted", relays)
	}
	if rows != 1 {
		t.Fatalf("suppressed message_unacked rows = %d, want 1", rows)
	}
}

// A digest folds pre-existing deferred rows: it compresses load rather than
// adding it, so it still delivers while the kind is exhausted.
func TestFoldDigestStillDeliversWhileExhausted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "DigestHeld", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Sync(ctx, ses.ID, nil, 20)
	for i, key := range []string{"TASK-1", "TASK-2"} {
		enq(t, s, a.ID, a.RootItemID, "digest",
			`{"event":"progress","agent":"w`+string(rune('1'+i))+`","item":"`+key+`","checkpoint":{"summary":"step done"}}`, 1)
	}
	s.Usage = fakeUsage{Fake: true}
	res, err := s.Sync(ctx, ses.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	digests := 0
	for _, m := range res.Messages {
		if m.Kind == "digest" {
			digests++
		}
	}
	if digests != 1 {
		t.Fatalf("digest count while exhausted = %d, want 1 (fold still delivers)", digests)
	}
}

// Nil Usage (usage polling off) never holds: today's behavior is unchanged.
func TestHoldIfExhaustedNilUsageNeverHolds(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	var held bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		held, err = s.holdIfExhausted(ctx, tx, orch.ID, "no_ack", json.RawMessage(`{}`))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("holdIfExhausted = true with nil Usage, want false (fail open)")
	}
}

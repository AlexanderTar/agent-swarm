package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
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

// I19: deferred relays never wake anyone and arrive folded into one digest.
func TestDeferredRelaysAreFoldedIntoOneDigest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Digest", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Sync(ctx, ses.ID, nil, 20) // clear the assignment from the picture
	for i, key := range []string{"TASK-1", "TASK-2", "TASK-3"} {
		enq(t, s, a.ID, a.RootItemID, "relay",
			`{"event":"progress","agent":"w`+string(rune('1'+i))+`","item":"`+key+`","checkpoint":{"summary":"step `+string(rune('1'+i))+` done"}}`, 1)
	}
	res, err := s.Sync(ctx, ses.ID, nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	var digests int
	for _, m := range res.Messages {
		if m.Kind == "relay" {
			t.Fatalf("a deferred relay must not be returned on its own: %s", m.Payload)
		}
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
	if digests != 1 {
		t.Fatalf("digest count = %d, want 1", digests)
	}
	// the folded rows are acked by the digest's own id
	var pending int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay' AND state <> 'acked'`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("%d deferred relays are still un-acked after the digest", pending)
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

func TestWakeClassFor(t *testing.T) {
	immediate := []struct {
		kind  MessageKind
		event string
	}{
		{"control", ""}, {"question", ""}, {"answer", ""}, {"finding", ""},
		{"approval_result", ""}, {"user_answer", ""}, {"repos_confirmed", ""},
		{"assignment_update", ""}, {"advice", ""}, {"assignment", ""},
		{"relay", "accepted"}, {"relay", "completed"}, {"relay", "failed"}, {"relay", "blocked"},
		{"relay", "crashed"}, {"relay", "interrupted"}, {"relay", "paused"},
		{"relay", "dependency_added"}, {"relay", "spawn_failed"},
	}
	for _, c := range immediate {
		if got := WakeClassFor(c.kind, c.event); got != "immediate" {
			t.Errorf("%s/%s = %s, want immediate", c.kind, c.event, got)
		}
	}
	for _, c := range []struct {
		kind  MessageKind
		event string
	}{{"relay", "progress"}, {"relay", "handoff"}, {"digest", ""}} {
		if got := WakeClassFor(c.kind, c.event); got != "deferred" {
			t.Errorf("%s/%s = %s, want deferred", c.kind, c.event, got)
		}
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

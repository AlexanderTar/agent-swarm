package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
)

var ctx = context.Background()

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newStore(t *testing.T) (*events.Store, *clock) {
	c := &clock{t: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)}
	return events.New(dbtest.Open(t), c.now), c
}

func TestPublishAndAfterInOrder(t *testing.T) {
	s, _ := newStore(t)
	for _, key := range []string{"EPIC-1", "TASK-1", "TASK-2"} {
		if _, err := s.Publish(ctx, events.ItemChanged, map[string]string{"key": key, "root_key": "EPIC-1"}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.After(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Seq != 2 || evs[1].Seq != 3 || evs[0].Type != "item.changed" {
		t.Fatalf("After = %+v", evs)
	}
	var p map[string]string
	json.Unmarshal(evs[0].Payload, &p)
	if p["key"] != "TASK-1" {
		t.Fatalf("payload = %s", evs[0].Payload)
	}
	if evs, _ := s.After(ctx, 0, 2); len(evs) != 2 {
		t.Fatalf("limit ignored: %d", len(evs))
	}
	if n, _ := s.Latest(ctx); n != 3 {
		t.Fatalf("Latest = %d", n)
	}
}

func TestAppendIsRolledBackWithTheTransaction(t *testing.T) {
	s, _ := newStore(t)
	tx, _ := s.DB.BeginTx(ctx, nil)
	if _, err := s.Append(ctx, tx, events.SettingsChanged, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if evs, _ := s.After(ctx, 0, 10); len(evs) != 0 {
		t.Fatalf("rolled-back event visible: %+v", evs)
	}
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := s.Append(ctx, tx, events.SettingsChanged, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if evs, _ := s.After(ctx, 0, 10); len(evs) != 1 || string(evs[0].Payload) != "null" {
		t.Fatalf("committed event = %+v", evs)
	}
}

func TestSubscribersAreWokenAndCanLeave(t *testing.T) {
	s, _ := newStore(t)
	ch, cancel := s.Subscribe()
	other, cancelOther := s.Subscribe()
	cancelOther()
	s.Publish(ctx, events.CatalogChanged, []int{})
	s.Publish(ctx, events.CatalogChanged, []int{}) // coalesces; must not block
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber not woken")
	}
	select {
	case <-other:
		t.Fatal("cancelled subscriber woken")
	default:
	}
	cancel()
	cancel() // idempotent
}

func TestExpiredAndPrune(t *testing.T) {
	s, c := newStore(t)
	s.Publish(ctx, events.ItemChanged, nil) // seq 1, day 0
	c.t = c.t.Add(8 * 24 * time.Hour)
	s.Publish(ctx, events.ItemChanged, nil) // seq 2, day 8
	s.Publish(ctx, events.ItemChanged, nil) // seq 3
	for after, want := range map[int64]bool{0: false, 1: false, 2: false, 3: false, 99: true} {
		if got, _ := s.Expired(ctx, after); got != want {
			t.Errorf("before prune Expired(%d) = %v", after, got)
		}
	}
	n, err := s.Prune(ctx, 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	for after, want := range map[int64]bool{0: false, 1: false, 2: false, 3: false} {
		if got, _ := s.Expired(ctx, after); got != want {
			t.Errorf("after prune Expired(%d) = %v", after, got)
		}
	}
	c.t = c.t.Add(8 * 24 * time.Hour)
	s.Prune(ctx, 7*24*time.Hour) // table now empty; latest stays 3
	if got, _ := s.Expired(ctx, 1); !got {
		t.Error("cursor 1 must be expired once everything after it was pruned")
	}
	if got, _ := s.Expired(ctx, 3); got {
		t.Error("cursor at latest is never expired")
	}
	if n, _ := s.Latest(ctx); n != 3 {
		t.Errorf("Latest after prune = %d", n)
	}
}

// A clock that regresses (sleep/wake, NTP step) must not punch a hole in the
// middle of the feed: a reconnecting client would silently miss those events.
func TestPruneDeletesAPrefixOnly(t *testing.T) {
	s, c := newStore(t)
	s.Publish(ctx, events.ItemChanged, nil) // seq 1, day 0
	c.t = c.t.Add(8 * 24 * time.Hour)
	s.Publish(ctx, events.ItemChanged, nil) // seq 2, day 8
	c.t = c.t.Add(-8 * 24 * time.Hour)
	s.Publish(ctx, events.ItemChanged, nil) // seq 3, back at day 0
	c.t = c.t.Add(8 * 24 * time.Hour)

	if n, err := s.Prune(ctx, 7*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	evs, err := s.After(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Seq != 2 || evs[1].Seq != 3 {
		t.Fatalf("After(1) = %+v, want seq 2 and 3", evs)
	}
	if got, _ := s.Expired(ctx, 1); got {
		t.Error("cursor 1 is still served, so it is not expired")
	}
}

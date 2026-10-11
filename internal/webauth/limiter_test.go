package webauth

import (
	"fmt"
	"testing"
	"time"
)

func newLimiter() (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	return NewLimiter(c.now), c
}

func TestLimiterBlocksTheSixthFailurePerClient(t *testing.T) {
	l, c := newLimiter()
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("1.1.1.1"); !ok {
			t.Fatalf("attempt %d blocked before 5 failures", i+1)
		}
		l.Fail("1.1.1.1")
		c.t = c.t.Add(time.Minute)
	}
	ok, retry := l.Allow("1.1.1.1")
	if ok || retry <= 0 || retry > 15*time.Minute {
		t.Fatalf("Allow after 5 failures = %v, %v; want blocked with Retry-After up to 15 min", ok, retry)
	}
	if ok, _ := l.Allow("2.2.2.2"); !ok {
		t.Fatal("one client's failures must not block another")
	}
	c.t = c.t.Add(15 * time.Minute) // the oldest failure is now 20 min old
	if ok, _ := l.Allow("1.1.1.1"); !ok {
		t.Fatal("the block must lift once failures age out of the 15 min window")
	}
}

func TestLimiterSuccessClearsTheClient(t *testing.T) {
	l, _ := newLimiter()
	for i := 0; i < 4; i++ {
		l.Fail("1.1.1.1")
	}
	l.Success("1.1.1.1")
	l.Fail("1.1.1.1")
	if ok, _ := l.Allow("1.1.1.1"); !ok {
		t.Fatal("a success must reset the client's count")
	}
}

func TestLimiterGlobalLock(t *testing.T) {
	l, c := newLimiter()
	for i := 0; i < 19; i++ {
		l.Fail(fmt.Sprintf("10.0.0.%d", i))
	}
	if ok, _ := l.Allow("9.9.9.9"); !ok {
		t.Fatal("19 failures must not lock login")
	}
	l.Fail("10.0.1.1")
	ok, retry := l.Allow("9.9.9.9")
	if ok || retry != 15*time.Minute {
		t.Fatalf("Allow after 20 failures = %v, %v; want locked for 15 min", ok, retry)
	}
	c.t = c.t.Add(10 * time.Minute)
	if ok, retry := l.Allow("9.9.9.9"); ok || retry != 5*time.Minute {
		t.Fatalf("mid-lock Allow = %v, %v; want 5 min left", ok, retry)
	}
	c.t = c.t.Add(5 * time.Minute)
	if ok, _ := l.Allow("9.9.9.9"); !ok {
		t.Fatal("the lock must lift after 15 min")
	}
}

func TestLimiterUnknownClientCountsOnlyAgainstTheGlobalCap(t *testing.T) {
	l, _ := newLimiter()
	for i := 0; i < 10; i++ {
		l.Fail("")
	}
	if ok, _ := l.Allow(""); !ok {
		t.Fatal("an unknown client has no per-client limit, only the global cap")
	}
	for i := 0; i < 10; i++ {
		l.Fail("")
	}
	if ok, _ := l.Allow(""); ok {
		t.Fatal("20 failures from unknown clients must trip the global lock")
	}
	if ok, _ := l.Allow("3.3.3.3"); ok {
		t.Fatal("the global lock applies to everyone")
	}
}

package webauth

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
)

const goodPassword = "correct horse battery"

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newStore(t *testing.T) (*Store, *clock, *db.DB) {
	t.Helper()
	d := dbtest.Open(t)
	c := &clock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	s := New(d, c.now)
	s.cost = bcrypt.MinCost // bcrypt 12 costs ~250 ms a hash; the cost itself is pinned in TestPasswordHashUsesCost12
	return s, c, d
}

func TestHasCredentialsStartsFalseAndSetMakesItTrue(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if ok, err := s.HasCredentials(ctx); err != nil || ok {
		t.Fatalf("HasCredentials = %v, %v; want false", ok, err)
	}
	if err := s.SetCredentials(ctx, "Me@Example.com", goodPassword); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.HasCredentials(ctx); err != nil || !ok {
		t.Fatalf("HasCredentials = %v, %v; want true", ok, err)
	}
	if email, ok := s.Email(ctx); !ok || email != "me@example.com" {
		t.Fatalf("Email = %q, %v; want lowercased me@example.com", email, ok)
	}
}

func TestPasswordHashUsesCost12(t *testing.T) {
	d := dbtest.Open(t)
	s := New(d, time.Now)
	if err := s.SetCredentials(context.Background(), "a@b.co", goodPassword); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := d.QueryRow(`SELECT password_hash FROM web_auth`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != 12 {
		t.Fatalf("cost = %d, %v; want 12", cost, err)
	}
	if strings.Contains(hash, goodPassword) {
		t.Fatal("hash contains the password")
	}
}

func TestSetCredentialsRejectsShortPassword(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.SetCredentials(context.Background(), "a@b.co", "short"); err == nil {
		t.Fatal("want an error for a password under 12 characters")
	}
	if ok, _ := s.HasCredentials(context.Background()); ok {
		t.Fatal("a rejected password must not create credentials")
	}
}

func TestVerify(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if s.Verify(ctx, "a@b.co", goodPassword) {
		t.Fatal("verify before credentials exist must fail")
	}
	if err := s.SetCredentials(ctx, "A@B.co", goodPassword); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, email, pw string
		want            bool
	}{
		{"right", "a@b.co", goodPassword, true},
		{"email case and space ignored", "  A@b.CO ", goodPassword, true},
		{"wrong password", "a@b.co", "wrong password!!", false},
		{"unknown email", "x@y.co", goodPassword, false},
		{"empty", "", "", false},
	} {
		if got := s.Verify(ctx, c.email, c.pw); got != c.want {
			t.Errorf("%s: Verify = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestVerifyHashesEvenForAnUnknownEmail(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "a@b.co", goodPassword); err != nil {
		t.Fatal(err)
	}
	var calls int
	s.compare = func(hash, pw []byte) error { calls++; return bcrypt.CompareHashAndPassword(hash, pw) }
	s.Verify(ctx, "x@y.co", goodPassword)
	s.Verify(ctx, "a@b.co", "wrong password!!")
	if calls != 2 {
		t.Fatalf("bcrypt compares = %d, want 2 (unknown email must cost the same as a wrong password)", calls)
	}
}

func TestSetCredentialsReplacesAndRevokesSessions(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if err := s.SetCredentials(ctx, "a@b.co", goodPassword); err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateSession(ctx, "lan", "ua")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredentials(ctx, "c@d.co", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LookupSession(ctx, tok); ok {
		t.Fatal("a credential reset must revoke every session")
	}
	if s.Verify(ctx, "a@b.co", goodPassword) || !s.Verify(ctx, "c@d.co", "another long password") {
		t.Fatal("reset must replace the old login")
	}
	if n, _ := s.SessionCount(ctx); n != 0 {
		t.Fatalf("SessionCount = %d, want 0", n)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, c, d := newStore(t)
	ctx := context.Background()
	tok, exp, err := s.CreateSession(ctx, "tunnel", "Safari")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 43 {
		t.Fatalf("token %q has under 256 bits", tok)
	}
	if want := c.t.Add(30 * 24 * time.Hour); !exp.Equal(want) {
		t.Fatalf("expires = %v, want %v", exp, want)
	}
	var stored string
	if err := d.QueryRow(`SELECT token_hash FROM web_sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == tok || strings.Contains(stored, tok) {
		t.Fatal("the raw session token must not be stored")
	}
	sess, ok := s.LookupSession(ctx, tok)
	if !ok || sess.Origin != "tunnel" || sess.UserAgent != "Safari" {
		t.Fatalf("LookupSession = %+v, %v", sess, ok)
	}
	if _, ok := s.LookupSession(ctx, tok+"x"); ok {
		t.Fatal("a wrong token must not resolve")
	}
	if _, ok := s.LookupSession(ctx, ""); ok {
		t.Fatal("an empty token must not resolve")
	}
	if n, _ := s.SessionCount(ctx); n != 1 {
		t.Fatalf("SessionCount = %d, want 1", n)
	}
	if err := s.DeleteSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LookupSession(ctx, tok); ok {
		t.Fatal("a deleted session must not resolve")
	}
}

func TestSessionExpiresAfter30Days(t *testing.T) {
	s, c, _ := newStore(t)
	ctx := context.Background()
	tok, _, _ := s.CreateSession(ctx, "lan", "")
	c.t = c.t.Add(30*24*time.Hour - time.Minute)
	if _, ok := s.LookupSession(ctx, tok); !ok {
		t.Fatal("a session just inside 30 days must resolve")
	}
	c.t = c.t.Add(30 * 24 * time.Hour) // far past even a slid expiry
	if _, ok := s.LookupSession(ctx, tok); ok {
		t.Fatal("an expired session must not resolve")
	}
	if n, _ := s.SessionCount(ctx); n != 0 {
		t.Fatalf("SessionCount = %d, want 0: expired rows are not live sessions", n)
	}
}

func TestSessionSlidesOncePerDay(t *testing.T) {
	s, c, d := newStore(t)
	ctx := context.Background()
	tok, first, _ := s.CreateSession(ctx, "lan", "")
	expiry := func() time.Time {
		var ms int64
		if err := d.QueryRow(`SELECT expires_at FROM web_sessions`).Scan(&ms); err != nil {
			t.Fatal(err)
		}
		return db.FromMillis(ms)
	}
	c.t = c.t.Add(23 * time.Hour)
	s.LookupSession(ctx, tok)
	if !expiry().Equal(first) {
		t.Fatal("a lookup within a day of the last renewal must not slide the expiry")
	}
	c.t = c.t.Add(2 * time.Hour) // 25 h after creation
	s.LookupSession(ctx, tok)
	if want := c.t.Add(30 * 24 * time.Hour); !expiry().Equal(want) {
		t.Fatalf("expiry = %v, want %v after the daily slide", expiry(), want)
	}
}

func TestDeleteAllSessions(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, _, _ := s.CreateSession(ctx, "lan", "")
	b, _, _ := s.CreateSession(ctx, "tunnel", "")
	if err := s.DeleteAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{a, b} {
		if _, ok := s.LookupSession(ctx, tok); ok {
			t.Fatal("DeleteAllSessions must revoke every session")
		}
	}
}

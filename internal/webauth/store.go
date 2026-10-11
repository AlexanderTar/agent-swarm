// Package webauth is the single-user web login: one email and password, and the
// cookie sessions it hands to lan and tunnel callers.
package webauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

const (
	BcryptCost  = 12
	MinPassword = 12
	SessionTTL  = 30 * 24 * time.Hour
	slideAfter  = 24 * time.Hour
)

var ErrWeakPassword = errors.New("password must be at least 12 characters")

type Store struct {
	db      *db.DB
	now     func() time.Time
	cost    int
	compare func(hash, password []byte) error // bcrypt.CompareHashAndPassword; tests count calls

	dummyOnce sync.Once
	dummy     []byte
}

func New(d *db.DB, now func() time.Time) *Store {
	return &Store{db: d, now: now, cost: BcryptCost, compare: bcrypt.CompareHashAndPassword}
}

type Session struct {
	Origin, UserAgent              string
	CreatedAt, LastSeen, ExpiresAt time.Time
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SetCredentials replaces the login and revokes every session.
func (s *Store) SetCredentials(ctx context.Context, email, password string) error {
	email = normEmail(email)
	if email == "" {
		return errors.New("email is required")
	}
	if len(password) < MinPassword {
		return ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO web_auth(id, email, password_hash, updated_at) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET email = excluded.email, password_hash = excluded.password_hash, updated_at = excluded.updated_at`,
		email, string(hash), db.Millis(s.now())); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM web_sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) HasCredentials(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_auth`).Scan(&n)
	return n > 0, err
}

// Email is the login's email, lowercased.
func (s *Store) Email(ctx context.Context) (string, bool) {
	var e string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM web_auth WHERE id = 1`).Scan(&e)
	return e, err == nil
}

// Verify checks the login. An unknown email, a wrong email and a wrong password
// each cost one bcrypt compare at the same cost, so timing doesn't say which.
func (s *Store) Verify(ctx context.Context, email, password string) bool {
	var stored, hash string
	err := s.db.QueryRowContext(ctx, `SELECT email, password_hash FROM web_auth WHERE id = 1`).Scan(&stored, &hash)
	known := err == nil && normEmail(email) == stored
	h := []byte(hash)
	if !known {
		h = s.dummyHash()
	}
	ok := s.compare(h, []byte(password)) == nil
	return known && ok
}

func (s *Store) dummyHash() []byte {
	s.dummyOnce.Do(func() { s.dummy, _ = bcrypt.GenerateFromPassword([]byte("swarm-no-such-login"), s.cost) })
	return s.dummy
}

// CreateSession mints a session and returns the raw token once; only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, origin, userAgent string) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	now := s.now()
	exp := now.Add(SessionTTL)
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_sessions(token_hash, origin, user_agent, created_at, last_seen, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, hashToken(token), origin, userAgent, db.Millis(now), db.Millis(now), db.Millis(exp))
	return token, exp, err
}

// LookupSession resolves a live session and, at most once a day, slides its expiry.
func (s *Store) LookupSession(ctx context.Context, token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	now := s.now()
	var sess Session
	var created, seen, expires int64
	err := s.db.QueryRowContext(ctx, `SELECT origin, user_agent, created_at, last_seen, expires_at FROM web_sessions
		WHERE token_hash = ? AND expires_at > ?`, hashToken(token), db.Millis(now)).
		Scan(&sess.Origin, &sess.UserAgent, &created, &seen, &expires)
	if err != nil {
		return Session{}, false
	}
	sess.CreatedAt, sess.LastSeen, sess.ExpiresAt = db.FromMillis(created), db.FromMillis(seen), db.FromMillis(expires)
	if now.Sub(sess.LastSeen) >= slideAfter {
		sess.LastSeen, sess.ExpiresAt = now, now.Add(SessionTTL)
		s.db.ExecContext(ctx, `UPDATE web_sessions SET last_seen = ?, expires_at = ? WHERE token_hash = ?`,
			db.Millis(sess.LastSeen), db.Millis(sess.ExpiresAt), hashToken(token))
	}
	return sess, true
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func (s *Store) DeleteAllSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions`)
	return err
}

// SessionCount counts live (unexpired) sessions.
func (s *Store) SessionCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM web_sessions WHERE expires_at > ?`, db.Millis(s.now())).Scan(&n)
	return n, err
}

// WithCost sets the bcrypt cost; tests use bcrypt.MinCost so a hash isn't 250 ms.
func (s *Store) WithCost(cost int) *Store { s.cost = cost; return s }

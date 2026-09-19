package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// Real "Claude Code-credentials" keychain items nest everything under
// "claudeAiOauth" — this is that real shape, not a flat guess.
func TestClaudeKeychainTokenParsesTheRealNestedShape(t *testing.T) {
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s Claude Code-credentials -a me -w`: {
			Out: `{"claudeAiOauth":{"accessToken":"sk-fake","expiresAt":1789700000000,"refreshToken":"r"}}`,
		},
	}}
	read := claudeKeychainToken(fake.Runner(), "Claude Code-credentials", "me")
	token, expiresAt, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "sk-fake" {
		t.Fatalf("token = %q", token)
	}
	if !expiresAt.Equal(time.UnixMilli(1789700000000)) {
		t.Fatalf("expiresAt = %v", expiresAt)
	}
}

// Falsification: a flat (accessToken/expiresAt at the top level, no wrapper)
// shape must NOT parse as if it were valid — the real keychain item is never
// shaped this way, and this is the exact bug (silently zero-valued expiresAt,
// which reads as "expired" forever) this test guards against reintroducing.
func TestClaudeKeychainTokenRejectsTheFlatShapeAsExpired(t *testing.T) {
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s Claude Code-credentials -a me -w`: {
			Out: `{"accessToken":"sk-fake","expiresAt":1789700000000}`,
		},
	}}
	read := claudeKeychainToken(fake.Runner(), "Claude Code-credentials", "me")
	token, expiresAt, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "" || !expiresAt.Equal(time.UnixMilli(0)) {
		t.Fatalf("flat shape must parse as empty token / epoch expiry (not the real token), got token=%q expiresAt=%v", token, expiresAt)
	}
}

func TestClaudeKeychainTokenPropagatesARunnerFailure(t *testing.T) {
	read := claudeKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("security: item not found")
	}, "Claude Code-credentials", "me")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a runner failure must propagate")
	}
}

func TestClaudeKeychainTokenRejectsBadJSON(t *testing.T) {
	read := claudeKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}, "Claude Code-credentials", "me")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("malformed keychain output must be an error")
	}
}

func TestClaudeKeychainTokenRefusesWithNoRunner(t *testing.T) {
	read := claudeKeychainToken(nil, "Claude Code-credentials", "me")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a nil runner must be an error, not a panic")
	}
}

// Real "cursor-access-token" keychain items are a bare JWT string, not JSON.
// This is a real, valid three-segment JWT whose payload segment decodes to
// {"exp": 1789900000} (unrelated header/signature segments, ignored).
func TestCursorKeychainTokenDecodesTheJWTExpClaim(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOiAxNzg5OTAwMDAwfQ.sig"
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s cursor-access-token -a cursor-user -w`: {Out: jwt},
	}}
	read := cursorKeychainToken(fake.Runner(), "cursor-access-token", "cursor-user")
	token, expiresAt, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != jwt {
		t.Fatalf("token = %q, want the raw JWT unchanged", token)
	}
	if !expiresAt.Equal(time.Unix(1789900000, 0)) {
		t.Fatalf("expiresAt = %v", expiresAt)
	}
}

// Falsification: the old json.Unmarshal-the-whole-thing approach must not
// silently succeed against a bare JWT — this is the exact bug (a JSON parse
// error, "invalid character 'e' looking for beginning of value", live on the
// real machine every time) this test guards against reintroducing.
func TestCursorKeychainTokenRejectsANonJWTAsAnError(t *testing.T) {
	read := cursorKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not-a-jwt"), nil
	}, "cursor-access-token", "cursor-user")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a non-JWT value must be an error, not a silent empty token")
	}
}

func TestCursorKeychainTokenPropagatesARunnerFailure(t *testing.T) {
	read := cursorKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("security: item not found")
	}, "cursor-access-token", "cursor-user")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a runner failure must propagate")
	}
}

func TestCursorKeychainTokenRefusesWithNoRunner(t *testing.T) {
	read := cursorKeychainToken(nil, "cursor-access-token", "cursor-user")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a nil runner must be an error, not a panic")
	}
}

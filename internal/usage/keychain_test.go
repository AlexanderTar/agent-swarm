package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/creds"
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
	read := claudeToken(creds.Keychain(fake.Runner(), "me").Claude, "Claude Code-credentials")
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
	read := claudeToken(creds.Keychain(fake.Runner(), "me").Claude, "Claude Code-credentials")
	token, expiresAt, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "" || !expiresAt.Equal(time.UnixMilli(0)) {
		t.Fatalf("flat shape must parse as empty token / epoch expiry (not the real token), got token=%q expiresAt=%v", token, expiresAt)
	}
}

func TestClaudeKeychainTokenPropagatesARunnerFailure(t *testing.T) {
	read := claudeToken(func(context.Context) ([]byte, error) {
		return nil, errors.New("security: item not found")
	}, "Claude Code-credentials")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a runner failure must propagate")
	}
}

func TestClaudeKeychainTokenRejectsBadJSON(t *testing.T) {
	read := claudeToken(func(context.Context) ([]byte, error) {
		return []byte("not json"), nil
	}, "Claude Code-credentials")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("malformed keychain output must be an error")
	}
}

func TestClaudeKeychainTokenRefusesWithNoRunner(t *testing.T) {
	read := claudeToken(creds.Keychain(nil, "me").Claude, "Claude Code-credentials")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a nil runner must be an error, not a panic")
	}
}

// claudeCLIVersion must read the real, currently-installed CLI, not a
// hardcoded string — this is that real shape, "<version> (Claude Code)",
// confirmed live via `claude --version` (2.1.278 (Claude Code)).
func TestClaudeCLIVersionParsesTheRealOutputShape(t *testing.T) {
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`claude --version`: {Out: "2.1.278 (Claude Code)"},
	}}
	v, err := claudeCLIVersion(fake.Runner())(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "2.1.278" {
		t.Fatalf("version = %q, want 2.1.278", v)
	}
}

func TestClaudeCLIVersionPropagatesARunnerFailure(t *testing.T) {
	_, err := claudeCLIVersion(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exec: \"claude\": executable file not found in $PATH")
	})(context.Background())
	if err == nil {
		t.Fatal("a runner failure must propagate, not fall back to a guess")
	}
}

func TestClaudeCLIVersionRefusesWithNoRunner(t *testing.T) {
	_, err := claudeCLIVersion(nil)(context.Background())
	if err == nil {
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
	read := cursorToken(creds.Keychain(fake.Runner(), "u").Cursor, "cursor-access-token")
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
	read := cursorToken(func(context.Context) ([]byte, error) {
		return []byte("not-a-jwt"), nil
	}, "cursor-access-token")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a non-JWT value must be an error, not a silent empty token")
	}
}

func TestCursorKeychainTokenPropagatesARunnerFailure(t *testing.T) {
	read := cursorToken(func(context.Context) ([]byte, error) {
		return nil, errors.New("security: item not found")
	}, "cursor-access-token")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a runner failure must propagate")
	}
}

func TestCursorKeychainTokenRefusesWithNoRunner(t *testing.T) {
	read := cursorToken(creds.Keychain(nil, "u").Cursor, "cursor-access-token")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a nil runner must be an error, not a panic")
	}
}

func writeAgyOAuthFile(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "antigravity-oauth-token"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Real antigravity-oauth-token files nest the access token and expiry under
// "token" (confirmed live: {token: {access_token, refresh_token, expiry,
// token_type}, auth_method, id_token}), with expiry as an RFC3339 timestamp
// carrying fractional seconds and a zone offset, e.g.
// "2026-09-19T14:16:07.847113+01:00" — not epoch millis like Claude's.
func TestAgyOAuthTokenParsesTheRealNestedShape(t *testing.T) {
	home := t.TempDir()
	writeAgyOAuthFile(t, home, `{"token":{"access_token":"ya29.fake",
	  "refresh_token":"1//fake","expiry":"2026-09-19T14:16:07.847113+01:00",
	  "token_type":"Bearer"},"auth_method":"consumer","id_token":"eyFake"}`)
	token, expiresAt, err := agyOAuthToken(home)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "ya29.fake" {
		t.Fatalf("token = %q", token)
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-09-19T14:16:07.847113+01:00")
	if !expiresAt.Equal(want) {
		t.Fatalf("expiresAt = %v, want %v", expiresAt, want)
	}
}

func TestAgyOAuthTokenPropagatesAMissingFile(t *testing.T) {
	_, _, err := agyOAuthToken(t.TempDir())(context.Background())
	if err == nil {
		t.Fatal("a missing token file must be an error")
	}
}

func TestAgyOAuthTokenRejectsBadJSON(t *testing.T) {
	home := t.TempDir()
	writeAgyOAuthFile(t, home, "not json")
	if _, _, err := agyOAuthToken(home)(context.Background()); err == nil {
		t.Fatal("malformed token file contents must be an error")
	}
}

func TestAgyOAuthTokenRejectsAnUnparsableExpiry(t *testing.T) {
	home := t.TempDir()
	writeAgyOAuthFile(t, home, `{"token":{"access_token":"ya29.fake","expiry":"not-a-timestamp"}}`)
	if _, _, err := agyOAuthToken(home)(context.Background()); err == nil {
		t.Fatal("an unparsable expiry must be an error, not a silently zero-valued (forever-expired-looking) time")
	}
}

// Linux: Claude's token and millisecond expiry come from Claude Code's
// credentials file, read through creds.Files like the keychain item on macOS,
// and Cursor reports that its Linux sign-in can't be read yet.
func TestTokensReadThroughTheLinuxCredentialsFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"claudeAiOauth":{"accessToken":"sk-file","refreshToken":"r","expiresAt":1767225600000,"scopes":["user:inference"]},"mcpOAuth":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := creds.Files(home, func(string) string { return "" })
	token, expiresAt, err := claudeToken(cs.Claude, "Claude Code-credentials")(context.Background())
	if err != nil || token != "sk-file" || !expiresAt.Equal(time.UnixMilli(1767225600000)) {
		t.Fatalf("claude = %q %v %v", token, expiresAt, err)
	}
	if _, _, err := cursorToken(cs.Cursor, "cursor-access-token")(context.Background()); !errors.Is(err, creds.ErrUnsupported) {
		t.Fatalf("cursor err = %v, want the unsupported reason", err)
	}
	os.Remove(path)
	if _, _, err := claudeToken(cs.Claude, "Claude Code-credentials")(context.Background()); !os.IsNotExist(err) {
		t.Fatalf("missing file err = %v", err)
	}
}

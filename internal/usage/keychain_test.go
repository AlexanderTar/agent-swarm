package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// keychainToken only ever reaches the keychain through the injected Runner —
// this test supplies a fake one, so it never calls the real `security`
// binary, matching the safety rule that only DefaultSources ever would.
func TestKeychainTokenParsesTheSecurityOutput(t *testing.T) {
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s Claude Code-credentials -a me -w`: {
			Out: `{"accessToken":"sk-fake","expiresAt":1789700000000}`,
		},
	}}
	read := keychainToken(fake.Runner(), "Claude Code-credentials", "me")
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

func TestKeychainTokenPropagatesARunnerFailure(t *testing.T) {
	read := keychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("security: item not found")
	}, "cursor-access-token", "cursor-user")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a runner failure must propagate")
	}
}

func TestKeychainTokenRejectsBadJSON(t *testing.T) {
	read := keychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}, "cursor-access-token", "cursor-user")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("malformed keychain output must be an error")
	}
}

func TestKeychainTokenRefusesWithNoRunner(t *testing.T) {
	read := keychainToken(nil, "cursor-access-token", "cursor-user")
	if _, _, err := read(context.Background()); err == nil {
		t.Fatal("a nil runner must be an error, not a panic")
	}
}

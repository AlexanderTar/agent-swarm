package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func writeMuseAuthFile(t *testing.T, home, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".config", "muse"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "muse", "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMuseTokenPrefersInlineAuthFileToken(t *testing.T) {
	home := t.TempDir()
	writeMuseAuthFile(t, home, `{"providers":{"meta":{"mechanism":"oauth","access_token":"dca:inline"}}}`)
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s ai.meta.dev.credentials -a meta -w`: {
			Out: `{"access_token":"dca:keychain"}`,
		},
	}}
	read := museKeychainToken(fake.Runner(), home, func(string) string { return "" })
	got, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "dca:inline" {
		t.Fatalf("token = %q, want the inline auth-file token", got)
	}
}

func TestMuseTokenFallsBackToKeychainForOAuthWithoutInlineToken(t *testing.T) {
	home := t.TempDir()
	writeMuseAuthFile(t, home, `{"providers":{"meta":{"mechanism":"oauth"}}}`)
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s ai.meta.dev.credentials -a meta -w`: {
			Out: `{"secret_schema_version":1,"api_key":"LLM|x","access_token":"dca:keychain"}`,
		},
	}}
	read := museKeychainToken(fake.Runner(), home, func(string) string { return "" })
	got, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "dca:keychain" {
		t.Fatalf("token = %q, want the keychain token", got)
	}
}

func TestMuseTokenRejectsNonDeviceTokensWithoutTransmitting(t *testing.T) {
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s ai.meta.dev.credentials -a meta -w`: {
			Out: `{"access_token":"LLM_dashboard_key"}`,
		},
	}}
	read := museKeychainToken(fake.Runner(), t.TempDir(), func(string) string { return "" })
	if _, err := read(context.Background()); err == nil {
		t.Fatal("a non-dca: token must be an error, never a transmittable credential")
	}
}

func TestMuseTokenHonorsAuthPathOverride(t *testing.T) {
	home := t.TempDir()
	custom := filepath.Join(home, "custom-auth.json")
	if err := os.WriteFile(custom, []byte(`{"providers":{"meta":{"mechanism":"oauth","access_token":"dca:custom"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	read := museKeychainToken(nil, home, func(k string) string {
		if k == "MUSE_AUTH_PATH" {
			return custom
		}
		return ""
	})
	got, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "dca:custom" {
		t.Fatalf("token = %q, want the override-file token", got)
	}
}

func TestMuseTokenPropagatesAKeychainFailure(t *testing.T) {
	read := museKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("security: item not found")
	}, t.TempDir(), func(string) string { return "" })
	if _, err := read(context.Background()); err == nil {
		t.Fatal("a keychain failure must propagate, not fall back to a guess")
	}
}

func TestMuseTokenRejectsBadKeychainJSON(t *testing.T) {
	read := museKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}, t.TempDir(), func(string) string { return "" })
	if _, err := read(context.Background()); err == nil {
		t.Fatal("malformed keychain output must be an error")
	}
}

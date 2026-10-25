package creds

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

var bg = context.Background()

func TestKeychainReadsBothItemsThroughSecurity(t *testing.T) {
	f := &execx.Fake{Responses: map[string]execx.Result{
		"security find-generic-password -s Claude Code-credentials -a alice -w":   {Out: `{"claudeAiOauth":{}}`},
		"security find-generic-password -s cursor-access-token -a cursor-user -w": {Out: "a.b.c\n"},
	}}
	s := Keychain(f.Runner(), "alice")
	if b, err := s.Claude(bg); err != nil || string(b) != `{"claudeAiOauth":{}}` {
		t.Errorf("Claude = %q %v", b, err)
	}
	if b, err := s.Cursor(bg); err != nil || string(b) != "a.b.c\n" {
		t.Errorf("Cursor = %q %v", b, err)
	}
	if s.Where != "the keychain" {
		t.Errorf("Where = %q", s.Where)
	}
	if _, err := Keychain(nil, "alice").Claude(bg); err == nil {
		t.Error("a nil runner must be an error, not a panic")
	}
}

func TestFilesReadClaudesCredentialsFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"t"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Files(home, func(string) string { return "" })
	if b, err := s.Claude(bg); err != nil || !strings.Contains(string(b), `"accessToken":"t"`) || s.Where != path {
		t.Errorf("Claude = %q %v, Where = %s", b, err, s.Where)
	}
	// CLAUDE_CONFIG_DIR moves it, as it moves Claude Code's whole config.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, ".credentials.json"), []byte(`moved`), 0o600)
	s = Files(home, func(k string) string { return map[string]string{"CLAUDE_CONFIG_DIR": other}[k] })
	if b, err := s.Claude(bg); err != nil || string(b) != "moved" {
		t.Errorf("CLAUDE_CONFIG_DIR: Claude = %q %v", b, err)
	}
	// A relative value is not a directory Claude Code would use from our cwd.
	s = Files(home, func(string) string { return "rel" })
	if s.Where != path {
		t.Errorf("relative CLAUDE_CONFIG_DIR: Where = %s", s.Where)
	}
	if _, err := Files(t.TempDir(), func(string) string { return "" }).Claude(bg); !os.IsNotExist(err) {
		t.Errorf("missing file: err = %v", err)
	}
}

func TestFilesSayCursorIsUnsupported(t *testing.T) {
	_, err := Files(t.TempDir(), func(string) string { return "" }).Cursor(bg)
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "Cursor") {
		t.Fatalf("err = %v", err)
	}
}

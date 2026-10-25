// Package creds reads the agents' own sign-in tokens from where each CLI
// keeps them: the login keychain on macOS, files on Linux. The usage poller
// and the model catalog both read through it, never through `security` or a
// path of their own. Nothing here caches a token.
package creds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// Reader returns a credential's raw bytes.
type Reader func(context.Context) ([]byte, error)

// Sources are one platform's readers. Where names the store for messages
// ("the keychain").
type Sources struct {
	Where string
	// Claude is Claude Code's OAuth credentials:
	// {"claudeAiOauth": {"accessToken": …, "expiresAt": <ms>}}.
	Claude Reader
	// Cursor is cursor-agent's access token, a bare JWT.
	Cursor Reader
}

// Keychain reads macOS's login keychain through `security`. user is the
// account of Claude's item (the macOS user name).
func Keychain(run execx.Runner, user string) Sources {
	return Sources{
		Where:  "the keychain",
		Claude: keychainItem(run, "Claude Code-credentials", user),
		Cursor: keychainItem(run, "cursor-access-token", "cursor-user"),
	}
}

func keychainItem(run execx.Runner, service, account string) Reader {
	return func(ctx context.Context) ([]byte, error) {
		if run == nil {
			return nil, fmt.Errorf("creds: no runner configured for the %s keychain entry", service)
		}
		return run(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w")
	}
}

// ErrUnsupported: this platform has no known place for the credential.
var ErrUnsupported = errors.New("not supported on this system yet")

// Files reads Linux's credential files. Claude Code keeps the same JSON it
// stores in the macOS keychain in $CLAUDE_CONFIG_DIR/.credentials.json
// (default ~/.claude, mode 0600). Where cursor-agent keeps its sign-in on
// Linux is unverified, so its reader says so instead of guessing a path.
func Files(userHome string, getenv func(string) string) Sources {
	dir := filepath.Join(userHome, ".claude")
	if d := getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(d) {
		dir = d
	}
	claude := filepath.Join(dir, ".credentials.json")
	return Sources{
		Where:  claude,
		Claude: func(context.Context) ([]byte, error) { return os.ReadFile(claude) },
		Cursor: func(context.Context) ([]byte, error) {
			return nil, fmt.Errorf("Cursor usage: reading cursor-agent's sign-in is %w", ErrUnsupported)
		},
	}
}

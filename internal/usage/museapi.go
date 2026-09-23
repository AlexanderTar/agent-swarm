package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

const (
	museKeychainService = "ai.meta.dev.credentials"
	museKeychainAccount = "meta"
	museAuthPathEnv     = "MUSE_AUTH_PATH"
	museTokenPrefix     = "dca:"
)

// museKeychainToken returns the Muse device-code token for the mint call.
// Precedence mirrors CodexBar's MuseCredentials: an inline
// providers.meta.access_token in the CLI auth file wins; otherwise the
// CLI's Keychain item. An oauth auth file without an inline token still
// counts as a login (token from Keychain). Only dca:-prefixed tokens are
// returned; anything else is an error and is never transmitted.
func museKeychainToken(run execx.Runner, userHome string, getenv func(string) string) func(context.Context) (string, error) {
	readKeychain := keychainBytes(run, museKeychainService, museKeychainAccount)
	return func(ctx context.Context) (string, error) {
		if tok, ok := museAuthFileToken(userHome, getenv); ok {
			return requireMuseToken(tok)
		}
		out, err := readKeychain(ctx)
		if err != nil {
			return "", err
		}
		var payload struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			return "", fmt.Errorf("usage: %s credentials: %w", museKeychainService, err)
		}
		return requireMuseToken(payload.AccessToken)
	}
}

func museAuthPath(userHome string, getenv func(string) string) string {
	if getenv != nil {
		if override := strings.TrimSpace(getenv(museAuthPathEnv)); override != "" {
			return override
		}
	}
	return filepath.Join(userHome, ".config", "muse", "auth.json")
}

// museAuthFileToken reports the inline token, if any. The second return is
// false when there is no auth file or no meta login in it.
func museAuthFileToken(userHome string, getenv func(string) string) (string, bool) {
	data, err := os.ReadFile(museAuthPath(userHome, getenv))
	if err != nil {
		return "", false
	}
	var file struct {
		Providers *struct {
			Meta *struct {
				Mechanism   string `json:"mechanism"`
				AccessToken string `json:"access_token"`
			} `json:"meta"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return "", false
	}
	if file.Providers == nil || file.Providers.Meta == nil {
		return "", false
	}
	meta := file.Providers.Meta
	if tok := strings.TrimSpace(meta.AccessToken); tok != "" {
		return tok, true
	}
	if strings.EqualFold(strings.TrimSpace(meta.Mechanism), "oauth") {
		return "", false // login exists; token comes from Keychain
	}
	return "", false
}

func requireMuseToken(raw string) (string, error) {
	tok := strings.TrimSpace(raw)
	if !strings.HasPrefix(tok, museTokenPrefix) {
		return "", fmt.Errorf("usage: %s credentials: not a device-code login (run `muse login` again)", museKeychainService)
	}
	return tok, nil
}

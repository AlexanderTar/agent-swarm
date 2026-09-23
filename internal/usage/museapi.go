package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// MuseAPI reads Muse Code's subscription quota from Meta's mint endpoint
// with the CLI-owned device-code token: POST {BaseURL}/muse-code/key,
// body {}, header x-api-version: 1.0.0. The call spends no model turn, so
// unlike the old MSP probe it needs no probe gap — it polls on the normal
// cadence. The mint response's api_key/base_url/payment fields are
// discarded; only plan identity (for error strings), the subscription
// flags and subs_usage are read.
type MuseAPI struct {
	BaseURL   string // "https://api.meta.ai" in production
	HTTP      *http.Client
	ReadToken func(ctx context.Context) (string, error)
	Now       func() time.Time
}

func (m *MuseAPI) httpClient() *http.Client {
	return httpClientOrDefault(m.HTTP)
}

// museMintResetBound is CodexBar's oversized-date boundary, verbatim:
// resets_at is unix seconds, and values outside (0, bound] omit the stamp
// without discarding the window's percent.
const museMintResetBound = int64(64092211200)

type museMintWindow struct {
	UsedPercent  float64 `json:"used_percent"`
	DurationMins int     `json:"window_duration_mins"`
	ResetsAtSec  int64   `json:"resets_at"`
}

type museMintResponse struct {
	RequirePayment *bool  `json:"require_payment"`
	SubsActive     *bool  `json:"is_subs_active"`
	TierName       string `json:"subs_tier_name"`
	SubsUsage      *struct {
		Window museMintWindow `json:"window"`
		Weekly museMintWindow `json:"weekly"`
	} `json:"subs_usage"`
}

func (m *MuseAPI) Fetch(ctx context.Context) ([]Meter, string, error) {
	token, err := m.ReadToken(ctx)
	if err != nil {
		return nil, "", err
	}
	body, _ := json.Marshal(map[string]any{})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(m.BaseURL, "/")+"/muse-code/key", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("muse: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-api-version", "1.0.0")
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("muse: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", fmt.Errorf("muse: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, "", fmt.Errorf("muse: Muse Code login was rejected. Run `muse login` again.")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, "", &RateLimitError{Err: fmt.Errorf("muse: Muse Code usage requests are rate limited"), RetryAfter: time.Minute}
	case resp.StatusCode >= 500:
		return nil, "", fmt.Errorf("muse: Muse Code API returned HTTP %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("muse: Muse Code API returned HTTP %d", resp.StatusCode)
	}
	var decoded museMintResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, "", fmt.Errorf("muse: could not parse Muse Code subscription usage: expected JSON")
	}
	if decoded.RequirePayment != nil && *decoded.RequirePayment {
		return nil, "", fmt.Errorf("muse: Muse Code requires a payment method")
	}
	if decoded.SubsActive == nil || !*decoded.SubsActive {
		return nil, "", fmt.Errorf("muse: no Muse Code subscription is active on this login")
	}
	if decoded.SubsUsage == nil {
		return nil, "", fmt.Errorf("muse: login response included no quota (subs_usage missing)")
	}
	return museMintMeters(decoded)
}

func museMintReset(sec int64) *time.Time {
	if sec <= 0 || sec > museMintResetBound {
		return nil
	}
	t := time.Unix(sec, 0)
	return &t
}

func clampPercent(p float64) float64 {
	return min(100, max(0, p))
}

func museMintMeters(d museMintResponse) ([]Meter, string, error) {
	w, weekly := d.SubsUsage.Window, d.SubsUsage.Weekly
	if w.DurationMins <= 0 {
		return nil, "", fmt.Errorf("muse: could not parse Muse Code subscription usage: window_duration_mins")
	}
	label, window := codexWindow(w.DurationMins)
	meters := []Meter{
		{ID: window, Label: label, Window: window,
			UsedPct: clampPercent(w.UsedPercent), ResetsAt: museMintReset(w.ResetsAtSec)},
		{ID: "weekly", Label: "Weekly", Window: "weekly",
			UsedPct: clampPercent(weekly.UsedPercent), ResetsAt: museMintReset(weekly.ResetsAtSec)},
	}
	return meters, meters[0].ID, nil
}

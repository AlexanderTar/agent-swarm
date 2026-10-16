package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
)

// GrokUsage is one grok session's token and cost totals, from `grok usage
// <session id>` (live-verified on grok 1.0.60, it also works mid-session).
// Nothing records it per session yet; like MuseSessionUsage it is the reader a
// future per-session cost row would call.
type GrokUsage struct {
	InputTokens, OutputTokens int
	CachedTokens              int
	ReasoningTokens           int
	Turns                     int
	CostUSD                   float64
}

// grokCostTicksPerUSD: costUsdTicks is USD x 1e10.
const grokCostTicksPerUSD = 1e10

type grokUsageDoc struct {
	Session *struct {
		InputTokens      int   `json:"inputTokens"`
		OutputTokens     int   `json:"outputTokens"`
		CachedReadTokens int   `json:"cachedReadTokens"`
		ReasoningTokens  int   `json:"reasoningTokens"`
		TurnCount        int   `json:"turnCount"`
		CostUsdTicks     int64 `json:"costUsdTicks"`
	} `json:"session"`
}

// ParseGrokSessionUsage reads the session totals of a `grok usage` document. A
// document with no session block is an error, never an empty success.
func ParseGrokSessionUsage(raw []byte) (GrokUsage, error) {
	var doc grokUsageDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return GrokUsage{}, err
	}
	s := doc.Session
	if s == nil {
		return GrokUsage{}, fmt.Errorf("grok usage has no session block")
	}
	return GrokUsage{InputTokens: s.InputTokens, OutputTokens: s.OutputTokens, CachedTokens: s.CachedReadTokens,
		ReasoningTokens: s.ReasoningTokens, Turns: s.TurnCount, CostUSD: float64(s.CostUsdTicks) / grokCostTicksPerUSD}, nil
}

// GrokSessionUsage runs `grok usage <sessionID>` under the Swarm grok home
// (without GROK_HOME grok reads ~/.grok and finds no Swarm session).
func GrokSessionUsage(ctx context.Context, run execx.RunnerEnv, swarmHome, sessionID string) (GrokUsage, error) {
	out, err := run(ctx, grokhome.Env(swarmHome), grokhome.Bin, "usage", sessionID)
	if err != nil {
		return GrokUsage{}, err
	}
	return ParseGrokSessionUsage(out)
}

// ---------------------------------------------------------------------------
// Grok: the account meter and the exhaustion fallback.
// ---------------------------------------------------------------------------

// Grok reads the SuperGrok usage limit from the CLI billing proxy
// (GET <BaseURL>/v1/billing?format=credits, live-verified 2026-10-16 on grok
// 1.0.60), signed in with the token grok itself caches in auth.json: the same
// figure as the TUI's /usage, and how CodexBar and the other usage menubars read
// it. creditUsagePercent is the percent used and currentPeriod.end the reset.
//
// It never asks grok to sign in: an earlier version ran `grok agent stdio` and
// called authenticate cached_token, and with no usable auth.json grok answers
// that with its browser sign-in, on every refresh (CHORE-104). Nor does it
// refresh the token itself: the refresh token is single-use and belongs to
// grok. The server keeps accepting a token for a while past its expires_at, so
// the token is always tried; only when billing rejects it does Refresh let
// grok renew it (`grok models` does so silently under grok's own lock,
// live-verified 2026-10-16), and billing is tried once more. Refresh runs only
// for a login that has a refresh token, so grok never falls into its sign-in.
//
// A free account has no percentage and no period, so there is no meter; then
// the last exhaustion a hook saw (GrokExhaustion) is reported as a 100% meter,
// which is all the usage gate needs for grok to fall back.
type Grok struct {
	BaseURL    string // "https://cli-chat-proxy.grok.com" in production
	HTTP       *http.Client
	AuthPaths  []string                                         // auth.json files tried in order; empty skips billing (exhaustion only)
	Refresh    func(ctx context.Context, grokHome string) error // lets grok renew a rejected token; nil never renews
	Exhaustion *GrokExhaustion                                  // nil: no fallback record
	Now        func() time.Time

	mu        sync.Mutex
	deadToken string // still rejected after a renewal: not renewed again until auth.json changes
}

// GrokBillingURL is the CLI billing proxy's base URL.
const GrokBillingURL = "https://cli-chat-proxy.grok.com"

// GrokAuthPaths is where grok's cached login lives: the Swarm grok home's
// auth.json (a link `swarm install` makes to ~/.grok/auth.json), then
// ~/.grok/auth.json itself, so the meter works before that link exists.
func GrokAuthPaths(swarmHome, userHome string) []string {
	return []string{filepath.Join(grokhome.Dir(swarmHome), "auth.json"), filepath.Join(userHome, ".grok", "auth.json")}
}

// GrokRefresh renews grok's cached login by running `grok models` in grokHome:
// any grok process renews an expired token at startup, and this one needs no
// session and no terminal.
func GrokRefresh(run execx.RunnerEnv) func(ctx context.Context, grokHome string) error {
	return func(ctx context.Context, grokHome string) error {
		_, err := run(ctx, map[string]string{"GROK_HOME": grokHome}, grokhome.Bin, "models")
		return err
	}
}

func (g *Grok) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Fetch is a Source.Fetch. Billing wins when it returns a meter; a 429 seen by
// a hook only fills in when it does not.
func (g *Grok) Fetch(ctx context.Context) ([]Meter, string, error) {
	var billErr error
	if len(g.AuthPaths) > 0 {
		var m *Meter
		if m, billErr = g.billing(ctx); m != nil {
			return []Meter{*m}, m.ID, nil
		}
	}
	if g.Exhaustion != nil {
		if until, ok := g.Exhaustion.Active(g.now()); ok {
			m := Meter{ID: "grok_limit", Label: "Limit", Window: "24h", UsedPct: 100, ResetsAt: &until}
			return []Meter{m}, m.ID, nil
		}
	}
	if billErr != nil {
		return nil, "", billErr
	}
	return nil, "", ErrNoData
}

// grokAuthEntry is the part of an auth.json entry the meter needs.
type grokAuthEntry struct {
	Key          string `json:"key"`
	RefreshToken string `json:"refresh_token"`
}

// grokLogin is the token found and the grok home whose auth.json holds it.
type grokLogin struct {
	token, home string
	renewable   bool
}

// grokToken returns the first grok login found in g.AuthPaths. Entries are
// keyed by OIDC scope: the SuperGrok one ("https://auth.x.ai::<client id>")
// wins over the legacy "https://accounts.x.ai/sign-in".
func (g *Grok) grokToken() (grokLogin, error) {
	for _, p := range g.AuthPaths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var entries map[string]grokAuthEntry
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		var best *grokAuthEntry
		for scope, e := range entries {
			if e.Key == "" {
				continue
			}
			if strings.HasPrefix(scope, "https://auth.x.ai::") {
				best = &e
				break
			}
			if scope == "https://accounts.x.ai/sign-in" {
				best = &e
			}
		}
		if best == nil {
			continue
		}
		return grokLogin{token: best.Key, home: filepath.Dir(p), renewable: best.RefreshToken != ""}, nil
	}
	return grokLogin{}, fmt.Errorf("grok: not signed in (run grok login)")
}

// errGrokRejected is billing's answer to a token it no longer accepts.
var errGrokRejected = errors.New("grok: login token rejected; run grok to renew it")

// billing returns the account meter, or (nil, nil) when billing has none
// (free tier).
func (g *Grok) billing(ctx context.Context) (*Meter, error) {
	login, err := g.grokToken()
	if err != nil {
		return nil, err
	}
	m, err := g.billingWith(ctx, login.token)
	g.mu.Lock()
	dead := login.token == g.deadToken
	g.mu.Unlock()
	if !errors.Is(err, errGrokRejected) || !login.renewable || g.Refresh == nil || dead {
		return m, err
	}
	if err := g.Refresh(ctx, login.home); err != nil {
		return nil, fmt.Errorf("grok: renewing the login token: %w", err)
	}
	if login, err = g.grokToken(); err != nil {
		return nil, err
	}
	m, err = g.billingWith(ctx, login.token)
	if errors.Is(err, errGrokRejected) {
		g.mu.Lock()
		g.deadToken = login.token
		g.mu.Unlock()
	}
	return m, err
}

func (g *Grok) billingWith(ctx context.Context, token string) (*Meter, error) {
	base := g.BaseURL
	if base == "" {
		base = GrokBillingURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/billing?format=credits", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-xai-token-auth", "xai-grok-cli")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClientOrDefault(g.HTTP).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (%d)", errGrokRejected, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("grok: billing status %d", resp.StatusCode)
	}
	return grokBillingMeter(body)
}

// grokBillingMeter reads a billing document. creditUsagePercent is left out
// when it is zero, so a period with no percent is 0% used; neither means no
// meter.
func grokBillingMeter(result json.RawMessage) (*Meter, error) {
	var b struct {
		Config struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				Type string `json:"type"`
				End  string `json:"end"`
			} `json:"currentPeriod"`
		} `json:"config"`
	}
	if err := json.Unmarshal(result, &b); err != nil {
		return nil, fmt.Errorf("grok: billing: %w", err)
	}
	period := b.Config.CurrentPeriod
	if b.Config.CreditUsagePercent == nil && period == nil {
		return nil, nil
	}
	m := Meter{ID: "grok_period", Label: "Period", Window: "custom"}
	if b.Config.CreditUsagePercent != nil {
		m.UsedPct = *b.Config.CreditUsagePercent
	}
	if period != nil {
		switch period.Type {
		case "USAGE_PERIOD_TYPE_WEEKLY":
			m.ID, m.Label, m.Window = "grok_weekly", "Weekly", "weekly"
		case "USAGE_PERIOD_TYPE_MONTHLY":
			m.ID, m.Label, m.Window = "grok_monthly", "Monthly", "monthly"
		}
		if end, err := time.Parse(time.RFC3339, period.End); err == nil {
			m.ResetsAt = &end
		}
	}
	return &m, nil
}

// GrokExhaustion remembers the last time grok reported its allowance spent, so
// the usage gate can see it without a billing meter. It is in memory: a daemon
// restart forgets it until grok fails again (ponytail: persist it if a restart
// mid-exhaustion ever matters).
type GrokExhaustion struct {
	mu    sync.Mutex
	until time.Time
}

var (
	// grokExhaustedText matches only quota text: the 429's `usage-exhausted`
	// code and `tokens (actual/limit)` counts, and the TUI's "You hit your ...
	// usage limit" card. A bare `retry_state exhausted` carries neither, and the
	// per-second 429 ("resource-exhausted ... Requests per Second (actual/limit)")
	// clears in a second, so neither marks grok out for a day.
	grokExhaustedText = regexp.MustCompile(`(?i)usage-exhausted|usage limit|tokens \(actual/limit\)`)
	grokResetsAt      = regexp.MustCompile(`(?i)resets at (\d{4}-\d\d-\d\dT[\d:.]+Z)`)
)

// grokDefaultReset is the free tier's rolling window, and the cap applied when
// the signal names no reset.
const grokDefaultReset = 24 * time.Hour

// Note records text as an exhaustion if it reads like one and reports whether
// it did. The reset is the "resets at <time>" it names, else a day from now.
func (e *GrokExhaustion) Note(text string, now time.Time) bool {
	if !grokExhaustedText.MatchString(text) {
		return false
	}
	until := now.Add(grokDefaultReset)
	if m := grokResetsAt.FindStringSubmatch(text); m != nil {
		if t, err := time.Parse(time.RFC3339, m[1]); err == nil && t.After(now) {
			until = t
		}
	}
	e.mu.Lock()
	e.until = until
	e.mu.Unlock()
	return true
}

// Active reports the reset time of an exhaustion that has not yet reset.
func (e *GrokExhaustion) Active(now time.Time) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.until, e.until.After(now)
}

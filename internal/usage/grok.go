package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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

// Grok reads the SuperGrok usage limit from `_x.ai/billing` over a short-lived
// `grok agent stdio` (initialize, authenticate cached_token, _x.ai/billing;
// live-verified on grok 1.0.60, 2026-10-16). creditUsagePercent is the percent
// used and currentPeriod.end the reset. The unprefixed `x.ai/billing` is not a
// method. A free account has no creditUsagePercent, so there is no meter; then
// the last exhaustion a hook saw (GrokExhaustion) is reported as a 100% meter,
// which is all the usage gate needs for grok to fall back.
type Grok struct {
	SwarmHome  string
	StartEnv   execx.StarterEnv // nil skips the billing call (exhaustion only)
	Dir        string           // working directory for grok agent stdio
	Timeout    time.Duration
	Exhaustion *GrokExhaustion // nil: no fallback record
	Now        func() time.Time
}

func (g *Grok) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Grok) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 10 * time.Second
}

// Fetch is a Source.Fetch. Billing wins when it returns a percentage; a 429
// seen by a hook only fills in when it does not.
func (g *Grok) Fetch(ctx context.Context) ([]Meter, string, error) {
	var billErr error
	if g.StartEnv != nil {
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

type grokRPCEnvelope struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// billing returns the account meter, or (nil, nil) when billing has no
// percentage (free tier).
func (g *Grok) billing(ctx context.Context) (*Meter, error) {
	proc, err := g.StartEnv(ctx, grokhome.Env(g.SwarmHome), grokhome.Bin, "agent", "stdio")
	if err != nil {
		return nil, err
	}
	defer proc.Kill()
	deadline := time.After(g.timeout())
	msgs := make(chan grokRPCEnvelope)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		dec := json.NewDecoder(proc.Stdout)
		for {
			var env grokRPCEnvelope
			if err := dec.Decode(&env); err != nil {
				readErr <- err
				return
			}
			select {
			case msgs <- env:
			case <-done:
				return
			}
		}
	}()
	call := func(id int, method string, params any) (json.RawMessage, error) {
		req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		if _, err := proc.Stdin.Write(append(req, '\n')); err != nil {
			return nil, fmt.Errorf("grok: %s: %w", method, err)
		}
		for {
			select {
			case env := <-msgs:
				if env.ID == nil || *env.ID != id {
					continue
				}
				if env.Error != nil {
					return nil, fmt.Errorf("grok: %s: error %d: %s", method, env.Error.Code, env.Error.Message)
				}
				return env.Result, nil
			case err := <-readErr:
				return nil, fmt.Errorf("grok: %s: agent stdio closed: %w", method, err)
			case <-deadline:
				return nil, fmt.Errorf("grok: %s: timed out after %s", method, g.timeout())
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if _, err := call(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}); err != nil {
		return nil, err
	}
	if _, err := call(2, "authenticate", map[string]any{"methodId": "cached_token"}); err != nil {
		return nil, err
	}
	result, err := call(3, "_x.ai/billing", map[string]any{})
	if err != nil {
		return nil, err
	}
	return grokBillingMeter(result)
}

func grokBillingMeter(result json.RawMessage) (*Meter, error) {
	var b struct {
		Config struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      struct {
				Type string `json:"type"`
				End  string `json:"end"`
			} `json:"currentPeriod"`
		} `json:"config"`
	}
	if err := json.Unmarshal(result, &b); err != nil {
		return nil, fmt.Errorf("grok: billing: %w", err)
	}
	if b.Config.CreditUsagePercent == nil {
		return nil, nil
	}
	m := Meter{ID: "grok_period", Label: "Period", Window: "custom", UsedPct: *b.Config.CreditUsagePercent}
	switch b.Config.CurrentPeriod.Type {
	case "USAGE_PERIOD_TYPE_WEEKLY":
		m.ID, m.Label, m.Window = "grok_weekly", "Weekly", "weekly"
	case "USAGE_PERIOD_TYPE_MONTHLY":
		m.ID, m.Label, m.Window = "grok_monthly", "Monthly", "monthly"
	}
	if end, err := time.Parse(time.RFC3339, b.Config.CurrentPeriod.End); err == nil {
		m.ResetsAt = &end
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
	// grokExhaustedText matches a 429 quota error, `retry_state exhausted`
	// and the TUI's "You hit your ... usage limit" card.
	grokExhaustedText = regexp.MustCompile(`(?i)exhausted|usage limit|actual/limit|quota`)
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

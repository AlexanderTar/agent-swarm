package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Claude reads usage from the same `anthropic-ratelimit-unified-*` response
// headers the real Claude Code CLI itself reads (confirmed by disassembling
// the installed CLI: probeQuotaStatus/extractQuotaStatusFromHeaders) —
// carried on ordinary /v1/messages responses, not a dedicated usage
// endpoint. /api/oauth/usage was tried first and abandoned: it runs its own,
// much stricter and stateful rate limit, independent of the account's real
// quota, that gets worse on every retry regardless of the endpoint's own
// quoted Retry-After (confirmed live: 2m23s -> 59m11s after one retry that
// honored it) — plausibly worse still for not including its `skip_spend=1`
// query param, which the real CLI always sends and this package didn't try
// until fetchModelScopedMeters below, which uses it on a single, never-retried
// best-effort call for model-scoped (e.g. Fable) meters this header-based
// path can't see. A minimal (max_tokens: 1) real inference call costs about
// one output token and a handful of input tokens per poll and uses the same
// infrastructure as normal usage, which is what makes it reliable.
type Claude struct {
	BaseURL   string // "https://api.anthropic.com" in production
	HTTP      *http.Client
	Version   func(ctx context.Context) (string, error) // the real, installed `claude --version`, read fresh each call
	ReadToken func(ctx context.Context) (token string, expiresAt time.Time, err error)
	Now       func() time.Time
	Log       func(string, ...any)
}

func (c *Claude) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Claude) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// claudeProbeModel must be a currently-available model; a retired one would
// make the whole probe fail with no rate-limit headers at all.
const claudeProbeModel = "claude-haiku-4-5-20251001"

type claudeProbeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeProbeRequest struct {
	Model     string               `json:"model"`
	MaxTokens int                  `json:"max_tokens"`
	Messages  []claudeProbeMessage `json:"messages"`
}

// parseResetsAt parses an RFC3339 resets_at string, used by agy.go's bucket
// mapping and by fetchModelScopedMeters below for /api/oauth/usage's
// limits[].resets_at. time.RFC3339 alone handles both shapes seen live
// (with and without fractional seconds) — Go's time.Parse accepts a
// fractional second in the value even when the layout doesn't spell one out
// — confirmed via TestParseResetsAtHandlesBothLiveCapturedShapes.
func parseResetsAt(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// parseUnixSecondsHeader reads a header carrying Unix seconds (the
// unified-rate-limit reset headers), unlike parseResetsAt's RFC3339 strings.
func parseUnixSecondsHeader(s string) *time.Time {
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	t := time.Unix(n, 0)
	return &t
}

// parseRetryAfterSeconds reads a Retry-After header (seconds, per the real
// endpoint's response); a missing or malformed value falls back to a
// conservative 5 minutes rather than retrying immediately.
func parseRetryAfterSeconds(v string) time.Duration {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 5 * time.Minute
}

// claudeWindowHeaderNames maps a Meter's ID to the unified header's window
// segment and this package's display label — confirmed live via a real
// max_tokens:1 call: real responses have exactly these two windows, never
// per-model (Opus/Sonnet/Fable) headers, so that former /api/oauth/usage
// granularity has no equivalent here.
var claudeWindowHeaderNames = []struct{ id, header, label, window string }{
	{"five_hour", "5h", "5h", "5h"},
	{"seven_day", "7d", "Weekly (all models)", "weekly"},
}

// claudeSnapshotFromHeaders extracts usage from a /v1/messages response's
// headers, real on a 200 AND on an over-quota 429 alike (the real CLI's own
// extractQuotaStatusFromError does the same: quota-exhausted is usage data,
// not a fetch failure). false means the headers are genuinely absent —
// an unrelated error, not usage information.
func claudeSnapshotFromHeaders(h http.Header) (Snapshot, bool) {
	var meters []Meter
	for _, w := range claudeWindowHeaderNames {
		raw := h.Get("anthropic-ratelimit-unified-" + w.header + "-utilization")
		if raw == "" {
			continue
		}
		util, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		resetsAt := parseUnixSecondsHeader(h.Get("anthropic-ratelimit-unified-" + w.header + "-reset"))
		meters = append(meters, Meter{ID: w.id, Label: w.label, Window: w.window,
			UsedPct: util * 100, ResetsAt: resetsAt})
	}
	if len(meters) == 0 {
		return Snapshot{}, false
	}
	headline := ""
	for _, m := range meters {
		if m.ID == "five_hour" {
			headline = "five_hour"
			break
		}
	}
	if headline == "" {
		headline = h.Get("anthropic-ratelimit-unified-representative-claim")
		if headline != "five_hour" && headline != "seven_day" {
			headline = meters[0].ID
		}
	}
	return Snapshot{Meters: meters, HeadlineID: headline}, true
}

// oauthUsageLimit/oauthUsageResponse decode only the fields this package
// needs from GET /api/oauth/usage?at_wall=1&skip_spend=1's limits[] array
// (see claude.go's package doc for why this endpoint, once abandoned, is
// used again here with skip_spend=1 for this one best-effort, single-shot
// call — never the primary /v1/messages path above).
type oauthUsageLimit struct {
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type oauthUsageResponse struct {
	Limits []oauthUsageLimit `json:"limits"`
}

// slugModelName turns a display name like "Fable" into "fable" for a Meter
// ID: lowercase, non-alphanumeric runs collapsed to a single "_", no
// leading/trailing "_". No existing helper in internal/ does this (checked:
// internal/migrate's slug() replaces spaces with "-" for section headings,
// a different job in a different package).
func slugModelName(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			prevUnderscore = false
			continue
		}
		if !prevUnderscore && b.Len() > 0 {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

// fetchModelScopedMeters is a second, best-effort call to the endpoint
// claude.go's package doc documents as abandoned for the primary path — with
// the skip_spend=1 param the prior investigation never tried, live-verified
// safe for exactly this one-shot, no-retry usage (see
// docs/specs/2026-09-22-claude-model-scoped-usage.md). Any failure (network
// error, non-200, decode error) is logged and swallowed: this must never
// fail Fetch, which already has its full primary snapshot by the time this
// runs.
func (c *Claude) fetchModelScopedMeters(ctx context.Context, token, version string) []Meter {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/oauth/usage?at_wall=1&skip_spend=1", nil)
	if err != nil {
		c.logf("usage: claude: model-scoped: %v", err)
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "claude-code/"+version)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClientOrDefault(c.HTTP).Do(req)
	if err != nil {
		c.logf("usage: claude: model-scoped: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.logf("usage: claude: model-scoped: status %d", resp.StatusCode)
		return nil
	}
	var parsed oauthUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		c.logf("usage: claude: model-scoped: %v", err)
		return nil
	}
	var meters []Meter
	for _, l := range parsed.Limits {
		if l.Scope == nil || l.Scope.Model == nil || l.Scope.Model.DisplayName == "" {
			continue
		}
		name := l.Scope.Model.DisplayName
		meters = append(meters, Meter{
			ID:       "model_scoped:" + slugModelName(name),
			Label:    "Weekly (" + name + ")",
			Window:   "weekly",
			UsedPct:  l.Percent,
			ResetsAt: parseResetsAt(l.ResetsAt),
		})
	}
	return meters
}

// Fetch is §13's Claude source. The token is read fresh every call and never
// persisted — Snapshot carries only meters, never the credential.
func (c *Claude) Fetch(ctx context.Context) (Snapshot, error) {
	token, expiresAt, err := c.ReadToken(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !expiresAt.After(c.now()) {
		return Snapshot{}, fmt.Errorf("claude: oauth token has expired")
	}
	// A fabricated or stale User-Agent is exactly the failure mode the old
	// /api/oauth/usage endpoint punished — kept here since it's still sent
	// on every real request this package makes.
	if c.Version == nil {
		return Snapshot{}, fmt.Errorf("claude: no CLI version source configured")
	}
	version, err := c.Version(ctx)
	if err != nil || version == "" {
		return Snapshot{}, fmt.Errorf("claude: could not determine the installed CLI version: %w", err)
	}
	body, err := json.Marshal(claudeProbeRequest{
		Model: claudeProbeModel, MaxTokens: 1,
		Messages: []claudeProbeMessage{{Role: "user", Content: "."}},
	})
	if err != nil {
		return Snapshot{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/"+version)
	resp, err := httpClientOrDefault(c.HTTP).Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // the usage data is in the headers, not this body

	if snap, ok := claudeSnapshotFromHeaders(resp.Header); ok {
		if extra := c.fetchModelScopedMeters(ctx, token, version); len(extra) > 0 {
			snap.Meters = append(snap.Meters, extra...)
		}
		return snap, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		d := parseRetryAfterSeconds(resp.Header.Get("Retry-After"))
		c.logf("usage: claude: status 429, no rate-limit headers, retry after %s", d)
		return Snapshot{}, &RateLimitError{Err: fmt.Errorf("claude: status 429"), RetryAfter: d}
	}
	c.logf("usage: claude: status %d, no rate-limit headers", resp.StatusCode)
	return Snapshot{}, fmt.Errorf("claude: status %d", resp.StatusCode)
}

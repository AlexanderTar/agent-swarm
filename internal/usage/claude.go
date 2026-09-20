package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
// query param, which the real CLI always sends and this package never
// tried. A minimal (max_tokens: 1) real inference call costs about one
// output token and a handful of input tokens per poll and uses the same
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

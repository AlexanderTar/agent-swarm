package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// claudeRealHeaders is the exact header set captured live from a real
// max_tokens:1 POST to /v1/messages with the real installed CLI's own
// User-Agent and beta header (see internal/usage/claude.go's doc comment
// for why this replaced /api/oauth/usage). Note there is no per-model
// (Opus/Sonnet/Fable) header here — that granularity genuinely has no
// equivalent in this source.
func claudeRealHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	h.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
	h.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	h.Set("anthropic-ratelimit-unified-reset", "1789837200")
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.18")
	h.Set("anthropic-ratelimit-unified-7d-reset", "1790096400")
	h.Set("anthropic-ratelimit-unified-5h-reset", "1789837200")
	h.Set("anthropic-ratelimit-unified-7d-status", "allowed")
	h.Set("anthropic-ratelimit-unified-fallback-percentage", "0.5")
	h.Set("anthropic-ratelimit-unified-overage-disabled-reason", "out_of_credits")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.47")
	h.Set("anthropic-ratelimit-unified-status", "allowed")
}

func TestClaudeMapsEveryMeterFromTheRealHeaders(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		gotBody, _ = io.ReadAll(r.Body)
		claudeRealHeaders(w)
		w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"Hello"}]}`))
	}))
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "oauth-secret", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Meter{}
	for _, m := range snap.Meters {
		byID[m.ID] = m
	}
	if m := byID["five_hour"]; m.Label != "5h" || m.Window != "5h" || m.UsedPct != 18 {
		t.Errorf("five_hour = %+v", m)
	}
	if m := byID["five_hour"]; m.ResetsAt == nil || !m.ResetsAt.Equal(time.Unix(1789837200, 0)) {
		t.Errorf("five_hour.ResetsAt = %v", m.ResetsAt)
	}
	if m := byID["seven_day"]; m.Label != "Weekly (all models)" || m.Window != "weekly" || m.UsedPct != 47 {
		t.Errorf("seven_day = %+v", m)
	}
	if snap.HeadlineID != "five_hour" {
		t.Errorf("headline = %q", snap.HeadlineID)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer oauth-secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := gotHeaders.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := gotHeaders.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != "claude-code/2.1.278" {
		t.Errorf("user-agent = %q", got)
	}
	if got := gotHeaders.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	var req claudeProbeRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != claudeProbeModel || req.MaxTokens != 1 || len(req.Messages) != 1 {
		t.Errorf("request body = %+v", req)
	}
}

// A quota-exhausted 429 is real usage data, not a fetch failure — the real
// CLI's own extractQuotaStatusFromError reads headers off error responses
// the same way it reads them off a 200. This must not be treated as a
// RateLimitError when the headers are present.
func TestClaudeExtractsUsageFromA429WhenTheHeadersArePresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeRealHeaders(w)
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "1")
		w.Header().Set("anthropic-ratelimit-unified-5h-status", "rejected")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"over quota"}}`))
	}))
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "t", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a quota-exhausted 429 with usage headers must not be an error: %v", err)
	}
	byID := map[string]Meter{}
	for _, m := range snap.Meters {
		byID[m.ID] = m
	}
	if byID["five_hour"].UsedPct != 100 {
		t.Errorf("five_hour = %+v, want 100%% used", byID["five_hour"])
	}
}

func TestClaudeKeepsTheOldMetersOnFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		header string
		expiry time.Time
	}{
		{"401", 401, "", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)},
		{"429 with retry-after, no usage headers", 429, "60", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)},
		{"expired token", 200, "", time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)},
	} {
		var called bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if c.header != "" {
				w.Header().Set("Retry-After", c.header)
			}
			w.WriteHeader(c.status)
		}))
		src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
			ReadToken: func(context.Context) (string, time.Time, error) { return "t", c.expiry, nil },
			Now:       func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
		snap, err := src.Fetch(context.Background())
		if err == nil {
			t.Errorf("%s: want an error so the poller keeps the old meters", c.name)
		}
		if len(snap.Meters) != 0 {
			t.Errorf("%s: a failed fetch returns no meters", c.name)
		}
		if c.name == "expired token" && called {
			t.Error("an expired token must not be sent")
		}
		srv.Close()
	}
}

// Falsification: an undetectable CLI version must fail the fetch outright,
// never fall back to a guessed/hardcoded version string.
func TestClaudeRefusesToGuessTheCLIVersion(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		claudeRealHeaders(w)
	}))
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(),
		Version: func(context.Context) (string, error) { return "", errors.New("claude: not found") },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "t", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("an undetectable CLI version must be an error")
	}
	if called {
		t.Fatal("the request must never go out with a guessed version")
	}
}

// This package abandoned /api/oauth/usage: it ran its own, much stricter and
// stateful rate limit, independent of the account's real quota, that got
// worse on every retry regardless of the endpoint's own quoted Retry-After
// (confirmed live: 2m23s -> 59m11s after one retry that honored it). This
// pins that /v1/messages — the same endpoint real usage goes through — is
// what a 429 with no usage headers here now falls back to, never that one.
func TestClaudeReturnsARateLimitErrorWithTheRetryAfterDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1007")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"Rate limited. Please try again later."}}`))
	}))
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "t", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
	_, err := src.Fetch(context.Background())
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("Fetch error = %v, want a *RateLimitError", err)
	}
	if rl.RetryAfter != 1007*time.Second {
		t.Fatalf("RetryAfter = %v, want 1007s", rl.RetryAfter)
	}
}

// Falsification: a missing/malformed Retry-After must not retry immediately
// (0s) — that would hammer the real endpoint and extend its own penalty.
func TestClaudeFallsBackToAConservativeRetryAfterWhenTheHeaderIsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "t", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
	_, err := src.Fetch(context.Background())
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("Fetch error = %v, want a *RateLimitError", err)
	}
	if rl.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v, must not be zero/immediate", rl.RetryAfter)
	}
}

// §13: the token is read each time and never stored.
func TestClaudeTokenIsNeverPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeRealHeaders(w)
	}))
	defer srv.Close()
	var logged strings.Builder
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "super-secret-token", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
		Log: func(f string, a ...any) { fmt.Fprintf(&logged, f, a...) }}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), "super-secret-token") || strings.Contains(logged.String(), "super-secret-token") {
		t.Fatal("the OAuth token must never reach a snapshot or a log line")
	}
}

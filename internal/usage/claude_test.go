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
		// Fetch now also makes a second, best-effort GET to
		// /api/oauth/usage; this fake server only serves /v1/messages, so
		// the second call should 404, which the model-scoped fetch must
		// treat as an ordinary failure (silently skipped, no extra meters)
		// without disturbing what this test asserts about the first call.
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
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

func TestClaudeHeadlinePrioritizesFiveHourEvenWhenClaimIsSevenDay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeRealHeaders(w)
		w.Header().Set("anthropic-ratelimit-unified-representative-claim", "seven_day")
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
	if snap.HeadlineID != "five_hour" {
		t.Errorf("headline = %q, want five_hour", snap.HeadlineID)
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

// claudeOauthUsageBody is the fixture for GET /api/oauth/usage?at_wall=1&skip_spend=1,
// shaped after the live-captured limits[] array: one weekly_scoped/Fable
// entry (a named model scope) and one weekly_all entry with scope: null,
// which must be filtered out generically (no "Fable" string match).
const claudeOauthUsageBody = `{"limits":[
	{"kind":"weekly_scoped","percent":0,"resets_at":"2026-09-29T17:00:00+00:00",
	 "scope":{"model":{"display_name":"Fable"}}},
	{"kind":"weekly_all","percent":2,"resets_at":"2026-09-29T17:00:00.474636+00:00",
	 "scope":null}
]}`

func TestClaudeAddsModelScopedMetersFromOauthUsage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		claudeRealHeaders(w)
		w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"Hello"}]}`))
	})
	mux.HandleFunc("GET /api/oauth/usage", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.RawQuery; got != "at_wall=1&skip_spend=1" {
			t.Errorf("query = %q, want at_wall=1&skip_spend=1", got)
		}
		w.Write([]byte(claudeOauthUsageBody))
	})
	srv := httptest.NewServer(mux)
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
	fable, ok := byID["model_scoped:fable"]
	if !ok {
		t.Fatalf("no model_scoped:fable meter in %+v", snap.Meters)
	}
	if fable.Label != "Weekly (Fable)" || fable.Window != "weekly" || fable.UsedPct != 0 {
		t.Errorf("fable meter = %+v", fable)
	}
	wantResetsAt := time.Date(2026, 9, 29, 17, 0, 0, 0, time.UTC)
	if fable.ResetsAt == nil || !fable.ResetsAt.Equal(wantResetsAt) {
		t.Errorf("fable.ResetsAt = %v, want %v", fable.ResetsAt, wantResetsAt)
	}
	for id := range byID {
		if id != "five_hour" && id != "seven_day" && id != "model_scoped:fable" {
			t.Errorf("unexpected meter %q derived from the scope-less weekly_all entry", id)
		}
	}
	// Additive, not a replacement: the existing header-derived meters must
	// still be present unchanged.
	if m := byID["five_hour"]; m.Label != "5h" || m.Window != "5h" || m.UsedPct != 18 {
		t.Errorf("five_hour = %+v", m)
	}
	if m := byID["seven_day"]; m.Label != "Weekly (all models)" || m.Window != "weekly" || m.UsedPct != 47 {
		t.Errorf("seven_day = %+v", m)
	}
}

// A failure on the model-scoped call (429 here; a decode failure would be
// just as valid coverage of the same fail-closed path) must never break the
// primary snapshot Fetch already has from /v1/messages's headers.
func TestClaudeModelScopedFetchFailureDoesNotBreakThePrimarySnapshot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		claudeRealHeaders(w)
		w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"Hello"}]}`))
	})
	mux.HandleFunc("GET /api/oauth/usage", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "oauth-secret", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
	snap, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("model-scoped failure must not fail Fetch: %v", err)
	}
	byID := map[string]Meter{}
	for _, m := range snap.Meters {
		byID[m.ID] = m
	}
	if _, ok := byID["model_scoped:fable"]; ok {
		t.Error("a failed model-scoped fetch must not produce a model_scoped meter")
	}
	if m := byID["five_hour"]; m.UsedPct != 18 {
		t.Errorf("five_hour = %+v, primary snapshot must be unaffected", m)
	}
	if m := byID["seven_day"]; m.UsedPct != 47 {
		t.Errorf("seven_day = %+v, primary snapshot must be unaffected", m)
	}
}

// The locked decision is one attempt, ever — no retry on failure.
func TestClaudeModelScopedFetchNeverRetries(t *testing.T) {
	var oauthUsageCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		claudeRealHeaders(w)
		w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"Hello"}]}`))
	})
	mux.HandleFunc("GET /api/oauth/usage", func(w http.ResponseWriter, r *http.Request) {
		oauthUsageCalls++
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: func(context.Context) (string, error) { return "2.1.278", nil },
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "oauth-secret", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }}
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if oauthUsageCalls != 1 {
		t.Errorf("/api/oauth/usage was hit %d times, want exactly 1", oauthUsageCalls)
	}
}

// Verifies both RFC3339 timestamp shapes from the live capture: one with
// fractional seconds ("2026-09-22T22:00:00.474610+00:00", the top-level
// five_hour.resets_at in the live body) and one without
// ("2026-09-29T17:00:00+00:00", the live limits[] weekly_scoped entry) —
// through the same parseResetsAt fetchModelScopedMeters uses, not a
// duplicate. This is the empirical answer to which RFC3339 parse approach
// works: plain time.RFC3339, no RFC3339Nano fallback needed, because Go's
// time.Parse accepts a fractional second in the value even when the layout
// doesn't spell one out.
func TestParseResetsAtHandlesBothLiveCapturedShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want time.Time
	}{
		{"no fractional seconds", "2026-09-29T17:00:00+00:00", time.Date(2026, 9, 29, 17, 0, 0, 0, time.UTC)},
		{"fractional seconds", "2026-09-22T22:00:00.474610+00:00", time.Date(2026, 9, 22, 22, 0, 0, 474610000, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseResetsAt(tc.in)
			if got == nil || !got.Equal(tc.want) {
				t.Errorf("parseResetsAt(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
	if got := parseResetsAt(""); got != nil {
		t.Errorf("parseResetsAt(\"\") = %v, want nil", got)
	}
	if got := parseResetsAt("not-a-timestamp"); got != nil {
		t.Errorf("parseResetsAt(garbage) = %v, want nil", got)
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

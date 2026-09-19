package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const claudeBody = `{
  "five_hour": {"utilization": 42.5, "resets_at": "2026-09-17T17:00:00Z"},
  "seven_day": {"utilization": 12.0, "resets_at": "2026-09-22T00:00:00Z"},
  "seven_day_fable": {"utilization": 3.5, "resets_at": "2026-09-22T00:00:00Z"},
  "seven_day_opus": null,
  "limits": [{"kind": "weekly_scoped", "model": "sonnet", "utilization": 7.25,
              "resets_at": "2026-09-22T00:00:00Z"}]
}`

func TestClaudeMapsEveryMeter(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		w.Write([]byte(claudeBody))
	}))
	defer srv.Close()
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: "2.1.274",
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
	if m := byID["five_hour"]; m.Label != "5h" || m.Window != "5h" || m.UsedPct != 42.5 {
		t.Errorf("five_hour = %+v", m)
	}
	if m := byID["seven_day"]; m.Label != "Weekly (all models)" || m.Window != "weekly" {
		t.Errorf("seven_day = %+v", m)
	}
	if m := byID["seven_day_fable"]; m.Label != "Fable weekly" {
		t.Errorf("seven_day_fable = %+v", m)
	}
	if _, ok := byID["seven_day_opus"]; ok {
		t.Error("a null per-model key must be skipped")
	}
	if m := byID["seven_day_sonnet"]; m.Label != "Sonnet weekly" || m.UsedPct != 7.25 {
		t.Errorf("the weekly_scoped limits[] entry = %+v", m)
	}
	if snap.HeadlineID != "five_hour" {
		t.Errorf("headline = %q", snap.HeadlineID)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer oauth-secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := gotHeaders.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != "claude-code/2.1.274" {
		t.Errorf("user-agent = %q", got)
	}
	if got := gotHeaders.Get("Accept"); got != "application/json" {
		t.Errorf("accept = %q", got)
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
		{"429 with retry-after", 429, "60", time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)},
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
		src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: "2.1.274",
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

// §13: the token is read each time and never stored.
func TestClaudeTokenIsNeverPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(claudeBody))
	}))
	defer srv.Close()
	var logged strings.Builder
	src := &Claude{BaseURL: srv.URL, HTTP: srv.Client(), Version: "2.1.274",
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

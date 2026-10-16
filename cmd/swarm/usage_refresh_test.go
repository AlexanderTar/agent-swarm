package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A kind that has never been polled has no usage row, so it is absent from
// GET /api/usage. `swarm usage --refresh` must still refresh every enabled
// agent, not just the ones that already have a row.
func TestUsageRefreshFetchesAnEnabledAgentWithNoRowYet(t *testing.T) {
	var refreshed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/settings":
			w.Write([]byte(`{"enabled_agents":["claude","grok"]}`))
		case "POST /api/usage/refresh":
			if strings.Contains(string(body), `"grok"`) {
				refreshed = append(refreshed, "grok")
			}
			w.WriteHeader(http.StatusAccepted)
		case "GET /api/usage":
			if len(refreshed) == 0 {
				w.Write([]byte(`[]`))
				return
			}
			w.Write([]byte(`[{"agent":"grok","meters":[{"id":"grok_weekly","label":"Weekly","window":"weekly","used_pct":1,"resets_at":null}],
				"headline_id":"grok_weekly","source":"grok","error":null,"fetched_at":1789651920000,"attempted_at":1789651920000,"stale":false}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	home := t.TempDir()
	writeToken(t, home, "tok")
	var out bytes.Buffer
	if code := run([]string{"usage", "--home", home, "--url", srv.URL, "--refresh"}, &out, &out); code != 0 {
		t.Fatalf("code = %d: %s", code, out.String())
	}
	if len(refreshed) != 1 {
		t.Fatalf("grok was not refreshed: %v", refreshed)
	}
	if !strings.Contains(out.String(), "grok") || !strings.Contains(out.String(), "weekly") {
		t.Fatalf("output lacks the grok meter: %q", out.String())
	}
}

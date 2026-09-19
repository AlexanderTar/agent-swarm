package usage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCursorMapsAutoAndAPI(t *testing.T) {
	// billingCycleEnd's real shape, confirmed live against api2.cursor.sh: a
	// quoted epoch-milliseconds string (Connect-RPC's JSON encoding of a
	// protobuf int64), not RFC3339. 1790812800000 ms = 2026-10-01T00:00:00Z.
	body := `{"planUsage":{"autoPercentUsed":63.5,"apiPercentUsed":12},
	          "billingCycleEnd":"1790812800000"}`
	meters, headline, err := ParseCursorUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 2 || headline != "cursor_auto" {
		t.Fatalf("meters = %+v, headline = %q", meters, headline)
	}
	if meters[0].ID != "cursor_auto" || meters[0].Label != "Monthly Auto" || meters[0].Window != "monthly" {
		t.Errorf("auto = %+v", meters[0])
	}
	if meters[1].Label != "Monthly API" {
		t.Errorf("api = %+v", meters[1])
	}
	if meters[0].ResetsAt == nil || !meters[0].ResetsAt.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("resets_at = %v", meters[0].ResetsAt)
	}
}

// A non-200 response is an error, not a silently empty snapshot.
func TestCursorFetchFailsOnANonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &Cursor{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now },
		Log:       func(string, ...any) {},
		ReadToken: func(context.Context) (string, time.Time, error) { return "jwt", now.Add(time.Hour), nil }}
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("a 401 must be an error")
	}
}

// The real endpoint 415s a request with no Content-Type and no body, even for
// a no-argument RPC (confirmed live against api2.cursor.sh) — this is that
// exact bug, guarded against reintroduction.
func TestCursorFetchSendsTheContentTypeAndBodyTheRealEndpointRequires(t *testing.T) {
	var gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if gotContentType != "application/json" || gotBody == "" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		w.Write([]byte(`{"planUsage":{"autoPercentUsed":1,"apiPercentUsed":1},"billingCycleEnd":"1790812800000"}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &Cursor{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now },
		ReadToken: func(context.Context) (string, time.Time, error) { return "jwt", now.Add(time.Hour), nil }}
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch failed (Content-Type=%q body=%q): %v", gotContentType, gotBody, err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody == "" {
		t.Error("body was empty; the real endpoint 415s an empty body")
	}
}

// §13: never call UseSandBankedReset, and a JWT with under 60 s left is stale.
func TestCursorNeverCallsTheForbiddenEndpointAndChecksTheJWT(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Write([]byte(`{"planUsage":{"autoPercentUsed":1,"apiPercentUsed":1},"billingCycleEnd":"1790812800000"}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	src := &Cursor{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return now },
		ReadToken: func(context.Context) (string, time.Time, error) { return "jwt", now.Add(30 * time.Second), nil }}
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Fatal("a JWT with under 60 s left is stale")
	}
	src.ReadToken = func(context.Context) (string, time.Time, error) { return "jwt", now.Add(time.Hour), nil }
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if strings.Contains(p, "UseSandBankedReset") {
			t.Fatalf("the forbidden endpoint was called: %s", p)
		}
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/aiserver.v1.DashboardService/GetCurrentPeriodUsage") {
		t.Fatalf("paths = %v", paths)
	}
}

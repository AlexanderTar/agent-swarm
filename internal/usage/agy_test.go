package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// §13: used_pct = (1 − remainingFraction) × 100, a missing fraction counts
// as 0 remaining. This is the real, nested
// google.internal.cloud.code.v1internal.PredictionService
// RetrieveUserQuotaSummaryResponse shape (confirmed live against
// daily-cloudcode-pa.googleapis.com on 2026-09-19), not a flat buckets[].
func TestAgyBuckets(t *testing.T) {
	body := `{"groups":[
	  {"displayName":"Gemini Models","buckets":[
	    {"window":"5h","remainingFraction":0.75,"disabled":false},
	    {"window":"weekly","remainingFraction":0.5,"disabled":false}]},
	  {"displayName":"Claude and GPT models","buckets":[
	    {"window":"5h","disabled":false},
	    {"window":"weekly","remainingFraction":0.9,"disabled":true}]}]}`
	meters, headline, err := ParseAgyQuota([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	byLabel := map[string]Meter{}
	for _, m := range meters {
		byLabel[m.Label] = m
	}
	if m := byLabel["Gemini 5h"]; m.UsedPct != 25 {
		t.Errorf("Gemini 5h = %+v", m)
	}
	if m := byLabel["Gemini weekly"]; m.UsedPct != 50 {
		t.Errorf("Gemini weekly = %+v", m)
	}
	// Claude & GPT inside agy are extra models: still reported as rows for
	// the usage panel (missing fraction = fully used, disabled skipped), but
	// never the headline.
	if m := byLabel["Claude & GPT 5h"]; m.ID != "cgpt_5h" || m.UsedPct != 100 {
		t.Errorf("Claude & GPT 5h = %+v", m)
	}
	if len(meters) != 3 {
		t.Errorf("meters = %+v, want two Gemini meters and Claude & GPT 5h", meters)
	}
	// the headline is the native Gemini 5h bucket
	if headline != "gemini_5h" {
		t.Errorf("headline = %q", headline)
	}
}

// A missing remainingFraction counts as fully used and a disabled bucket is
// skipped, on the Gemini group itself.
func TestAgyBucketsMissingFractionAndDisabled(t *testing.T) {
	body := `{"groups":[{"displayName":"Gemini Models","buckets":[
	  {"window":"5h","disabled":false},
	  {"window":"weekly","remainingFraction":0.9,"disabled":true}]}]}`
	meters, _, err := ParseAgyQuota([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 1 || meters[0].Label != "Gemini 5h" || meters[0].UsedPct != 100 {
		t.Fatalf("meters = %+v, want one fully used Gemini 5h (disabled weekly skipped)", meters)
	}
}

// The live 2026-09-26 shape: Gemini 5h at 6 %, Claude & GPT 5h at 100 %.
// The headline must stay on Gemini, or the menubar shows 100 % for agy and
// the usage gate calls agy exhausted while its Gemini quota is nearly unused.
func TestAgyHeadlineIsGeminiEvenWhenExtraModelsAreBusier(t *testing.T) {
	meters, headline, err := ParseAgyQuota([]byte(`{"groups":[
	  {"displayName":"Gemini Models","buckets":[
	    {"window":"weekly","remainingFraction":0.65},
	    {"window":"5h","remainingFraction":0.94}]},
	  {"displayName":"Claude and GPT models","buckets":[
	    {"window":"weekly","remainingFraction":0.07},
	    {"window":"5h","remainingFraction":0}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if headline != "gemini_5h" {
		t.Fatalf("headline = %q, want gemini_5h", headline)
	}
	if len(meters) != 4 {
		t.Fatalf("meters = %+v, want Gemini and Claude & GPT rows", meters)
	}
}

// Even when a weekly bucket has higher utilization, the headline must be the 5h bucket.
func TestAgyBucketsPrioritizesFiveHourOverWeekly(t *testing.T) {
	body := `{"groups":[
	  {"displayName":"Gemini Models","buckets":[
	    {"window":"5h","remainingFraction":0.8,"disabled":false},
	    {"window":"weekly","remainingFraction":0.1,"disabled":false}]}]}`
	meters, headline, err := ParseAgyQuota([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 2 {
		t.Fatalf("meters = %+v", meters)
	}
	if headline != "gemini_5h" {
		t.Fatalf("headline = %q, want gemini_5h (5h prioritized over weekly)", headline)
	}
}

// ResetsAt must come through from the real payload's resetTime field, the
// same way claude.go's meters carry it.
func TestAgyBucketsCarriesResetsAt(t *testing.T) {
	body := `{"groups":[{"displayName":"Gemini Models","buckets":[
	  {"window":"5h","remainingFraction":0.9,"resetTime":"2026-09-19T17:28:18Z"}]}]}`
	meters, _, err := ParseAgyQuota([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 1 || meters[0].ResetsAt == nil {
		t.Fatalf("meters = %+v, want a parsed ResetsAt", meters)
	}
	want := time.Date(2026, 9, 19, 17, 28, 18, 0, time.UTC)
	if !meters[0].ResetsAt.Equal(want) {
		t.Errorf("ResetsAt = %v, want %v", meters[0].ResetsAt, want)
	}
}

// An unrecognized group displayName still produces a meter (falls back to
// the raw name as both id and label) rather than silently dropping the group.
func TestAgyBucketsFallsBackForAnUnknownGroup(t *testing.T) {
	body := `{"groups":[{"displayName":"Something New","buckets":[
	  {"window":"5h","remainingFraction":1}]}]}`
	meters, _, err := ParseAgyQuota([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 1 || meters[0].Label != "Something New 5h" || meters[0].ID != "Something New_5h" {
		t.Fatalf("meters = %+v", meters)
	}
}

// The real endpoint is called directly with the OAuth token from ReadToken —
// no local agy process, no CSRF token, and no project ID.
func TestAgyFetchCallsTheRealEndpointShape(t *testing.T) {
	var gotHeaders http.Header
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		gotPath = r.URL.Path
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Write([]byte(`{"groups":[{"displayName":"Gemini Models","buckets":[
		  {"window":"5h","remainingFraction":0.75}]}]}`))
	}))
	t.Cleanup(srv.Close)
	a := &Agy{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "oauth-secret", time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) },
		Log: func(string, ...any) {}}
	meters, headline, err := a.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 1 || meters[0].UsedPct != 25 || headline != "gemini_5h" {
		t.Fatalf("meters = %+v, headline = %q", meters, headline)
	}
	if gotPath != "/v1internal:retrieveUserQuotaSummary" {
		t.Errorf("path = %q", gotPath)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer oauth-secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := gotHeaders.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != "antigravity" {
		t.Errorf("user-agent = %q", got)
	}
	if gotBody != "{}" {
		t.Errorf("body = %q, want the empty-object request agy's real endpoint accepts", gotBody)
	}
}

// A non-200 response is an error, not an empty meter list.
func TestAgyFetchFailsOnANonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	a := &Agy{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "oauth-secret", time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) },
		Log: func(string, ...any) {}}
	if _, _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("a 503 must be an error")
	}
}

// A 401 is agy's real signal that the cached OAuth token was rejected —
// confirmed live against the real endpoint with a missing/bad token.
func TestAgyFetchFailsOnA401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := &Agy{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "oauth-secret", time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) },
		Log: func(string, ...any) {}}
	if _, _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("a 401 must be an error")
	}
}

// An expired token must fail the fetch before any network call is made — the
// same rule claude.go's Fetch applies to its own cached token.
func TestAgyFetchFailsOnAnExpiredToken(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)
	a := &Agy{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "oauth-secret", time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) },
		Log: func(string, ...any) {}}
	if _, _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("an expired token must be an error")
	}
	if called {
		t.Error("an expired token must not reach the network")
	}
}

// A ReadToken failure (the oauth token file missing, unreadable, or
// malformed) must propagate rather than being swallowed.
func TestAgyFetchPropagatesAReadTokenFailure(t *testing.T) {
	a := &Agy{BaseURL: "http://unused.invalid",
		ReadToken: func(context.Context) (string, time.Time, error) {
			return "", time.Time{}, errors.New("agy oauth token: no such file")
		},
		Now: func() time.Time { return time.Now() }, Log: func(string, ...any) {}}
	if _, _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("a ReadToken failure must propagate")
	}
}

func TestAgyFetchRefusesWithNoReadToken(t *testing.T) {
	a := &Agy{BaseURL: "http://unused.invalid", Now: func() time.Time { return time.Now() }}
	if _, _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("a nil ReadToken must be an error, not a panic")
	}
}

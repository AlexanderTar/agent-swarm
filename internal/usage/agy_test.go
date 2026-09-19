package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// §13: used_pct = (1 − remainingFraction) × 100, a missing fraction counts as 0 remaining.
func TestAgyBuckets(t *testing.T) {
	body := `{"buckets":[
	  {"group":"Gemini Models","window":"5h","remainingFraction":0.75,"disabled":false},
	  {"group":"Gemini Models","window":"weekly","remainingFraction":0.5,"disabled":false},
	  {"group":"Claude and GPT models","window":"5h","disabled":false},
	  {"group":"Claude and GPT models","window":"weekly","remainingFraction":0.9,"disabled":true}]}`
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
	if m := byLabel["Claude & GPT 5h"]; m.UsedPct != 100 {
		t.Errorf("a missing remainingFraction counts as fully used: %+v", m)
	}
	if _, ok := byLabel["Claude & GPT weekly"]; ok {
		t.Error("a disabled bucket is skipped")
	}
	// the headline is the busier of the two 5h buckets
	if headline != "cgpt_5h" {
		t.Errorf("headline = %q", headline)
	}
}

// The refresh command runs once, through the injected runner, and the JSON is
// read from the stubbed endpoint. Nothing here touches the real agy CLI or Google.
func TestAgyFetchRefreshesThenReads(t *testing.T) {
	var got []string
	fake := &execx.Fake{Responses: map[string]execx.Result{"agy models": {Out: "ok\n"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"buckets":[{"group":"Gemini Models","window":"5h","remainingFraction":0.75,"disabled":false}]}`))
	}))
	t.Cleanup(srv.Close)
	a := &Agy{BaseURL: srv.URL, HTTP: srv.Client(), UserHome: t.TempDir(),
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			got = append(got, name+" "+strings.Join(args, " "))
			return fake.Runner()(ctx, name, args...)
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
		Log: func(string, ...any) {}}
	meters, headline, err := a.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "agy models" {
		t.Fatalf("commands run = %v, want exactly [agy models]", got)
	}
	if len(meters) != 1 || meters[0].UsedPct != 25 || headline != "gemini_5h" {
		t.Fatalf("meters = %+v, headline = %q", meters, headline)
	}
}

// A non-200 response is an error, not an empty meter list.
func TestAgyFetchFailsOnANonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	a := &Agy{BaseURL: srv.URL, HTTP: srv.Client(), UserHome: t.TempDir(),
		Now: func() time.Time { return time.Now() }, Log: func(string, ...any) {}}
	if _, _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("a 503 must be an error")
	}
}

// A failing refresh is not fatal: the endpoint may still hold usable numbers, and
// losing the whole meter set because a CLI hiccuped is worse than stale data.
func TestAgyFetchSurvivesAFailingRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"buckets":[{"group":"Gemini Models","window":"5h","remainingFraction":0.5,"disabled":false}]}`))
	}))
	t.Cleanup(srv.Close)
	a := &Agy{BaseURL: srv.URL, HTTP: srv.Client(), UserHome: t.TempDir(),
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("exit 1")
		},
		Now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
		Log: func(string, ...any) {}}
	meters, _, err := a.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a failing `agy models` must not fail the fetch: %v", err)
	}
	if len(meters) != 1 {
		t.Fatalf("meters = %+v", meters)
	}
}

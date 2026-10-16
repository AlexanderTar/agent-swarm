package usage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
)

func TestParseGrokSessionUsage(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok-usage-run1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseGrokSessionUsage(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := GrokUsage{InputTokens: 84202, OutputTokens: 478, CachedTokens: 57216, ReasoningTokens: 380, Turns: 1, CostUSD: 0.085448}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseGrokSessionUsageRejectsDocWithoutSession(t *testing.T) {
	if _, err := ParseGrokSessionUsage([]byte(`{"sessionId":"x"}`)); err == nil {
		t.Fatal("a document with no session block must be an error, not zero spend")
	}
}

func TestGrokSessionUsageRunsWithGrokHome(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok-usage-run1.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotEnv map[string]string
	var gotArgs []string
	run := func(_ context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
		gotEnv, gotArgs = env, append([]string{name}, args...)
		return raw, nil
	}
	u, err := GrokSessionUsage(context.Background(), run, "/swarm", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 84202 {
		t.Fatalf("usage not parsed: %+v", u)
	}
	if !reflect.DeepEqual(gotEnv, grokhome.Env("/swarm")) {
		t.Fatalf("env %v want %v", gotEnv, grokhome.Env("/swarm"))
	}
	if want := []string{"grok", "usage", "sess-1"}; !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("argv %v want %v", gotArgs, want)
	}
}

// fakeGrokBilling serves the CLI billing proxy from a fixture file and records
// the request's auth headers. status != 0 answers with that status instead;
// accept != "" answers 401 to any other bearer token.
type fakeGrokBilling struct {
	billing string
	status  int
	accept  string
	calls   int
	auth    string
	tokAuth string
	path    string
}

func (f *fakeGrokBilling) server(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(f.billing)
	if err != nil && f.billing != "" {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		f.auth, f.tokAuth, f.path = r.Header.Get("Authorization"), r.Header.Get("x-xai-token-auth"), r.URL.RequestURI()
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if f.accept != "" && f.auth != "Bearer "+f.accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

var grokNow = time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)

// writeGrokAuth writes an auth.json shaped like grok's (scope-keyed entries)
// with fake tokens, and returns its path.
func writeGrokAuth(t *testing.T, entries map[string]map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func grokEntry(key string, expires time.Time) map[string]any {
	return map[string]any{"key": key, "auth_mode": "oidc", "refresh_token": "fake-refresh", "expires_at": expires.Format(time.RFC3339Nano)}
}

func newGrok(srv *httptest.Server, paths ...string) *Grok {
	return &Grok{BaseURL: srv.URL, HTTP: srv.Client(), AuthPaths: paths, Now: func() time.Time { return grokNow }}
}

func TestGrokAccountMeters(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::client-1": grokEntry("tok-supergrok", grokNow.Add(time.Hour))})
	meters, headline, err := newGrok(fake.server(t), auth).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Date(2026, 10, 23, 13, 43, 37, 735000000, time.UTC)
	if len(meters) != 1 || headline != meters[0].ID {
		t.Fatalf("meters %+v headline %q", meters, headline)
	}
	m := meters[0]
	if m.UsedPct != 1.0 || m.Window != "weekly" || m.ResetsAt == nil || !m.ResetsAt.Equal(reset) {
		t.Fatalf("meter %+v, want 1%% weekly resetting %s", m, reset)
	}
	if fake.auth != "Bearer tok-supergrok" || fake.tokAuth != "xai-grok-cli" || fake.path != "/v1/billing?format=credits" {
		t.Fatalf("request auth %q token-auth %q path %q", fake.auth, fake.tokAuth, fake.path)
	}
}

// The Swarm grok home's auth.json is a link to ~/.grok/auth.json that only
// `swarm install` makes. Without it the meter must still read the user's own
// login (CHORE-104: the missing link sent every refresh into grok's browser
// login).
func TestGrokFallsBackToTheNextAuthFile(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json"}
	user := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::client-1": grokEntry("tok-user", grokNow.Add(time.Hour))})
	missing := filepath.Join(t.TempDir(), "auth.json")
	if _, _, err := newGrok(fake.server(t), missing, user).Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.auth != "Bearer tok-user" {
		t.Fatalf("Authorization %q, want the user's token", fake.auth)
	}
}

func TestGrokPrefersTheSuperGrokEntry(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json"}
	auth := writeGrokAuth(t, map[string]map[string]any{
		"https://accounts.x.ai/sign-in":  grokEntry("tok-legacy", grokNow.Add(time.Hour)),
		"https://auth.x.ai::client-1":    grokEntry("tok-supergrok", grokNow.Add(time.Hour)),
		"https://unrelated.example::abc": grokEntry("tok-other", grokNow.Add(time.Hour)),
	})
	if _, _, err := newGrok(fake.server(t), auth).Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.auth != "Bearer tok-supergrok" {
		t.Fatalf("Authorization %q, want the auth.x.ai entry", fake.auth)
	}
}

// No login, or a token the server rejects with no way to renew it, is an error
// the menubar shows. Nothing that could start a sign-in runs.
func TestGrokWithoutAUsableTokenIsAnError(t *testing.T) {
	for _, c := range []struct {
		name   string
		paths  func(t *testing.T) []string
		status int
		calls  int
		want   string
	}{
		{"no auth file", func(t *testing.T) []string { return []string{filepath.Join(t.TempDir(), "auth.json")} }, 0, 0, "not signed in"},
		{"no grok entry", func(t *testing.T) []string {
			return []string{writeGrokAuth(t, map[string]map[string]any{"https://unrelated.example::abc": grokEntry("x", grokNow.Add(time.Hour))})}
		}, 0, 0, "not signed in"},
		{"rejected, no refresh token", func(t *testing.T) []string {
			e := grokEntry("bad", grokNow.Add(time.Hour))
			delete(e, "refresh_token")
			return []string{writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": e})}
		}, http.StatusUnauthorized, 1, "rejected"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeGrokBilling{billing: "testdata/grok-billing.json", status: c.status}
			g := newGrok(fake.server(t), c.paths(t)...)
			refreshed := 0
			g.Refresh = func(context.Context, string) error { refreshed++; return nil }
			_, _, err := g.Fetch(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to say %q", err, c.want)
			}
			if fake.calls != c.calls || refreshed != 0 {
				t.Fatalf("billing called %d times (want %d), refresh %d times (want 0)", fake.calls, c.calls, refreshed)
			}
		})
	}
}

// The server keeps accepting a token for a while past its expires_at, so an
// expired-looking token is still tried rather than reported.
func TestGrokTriesATokenPastItsExpiry(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok", grokNow.Add(-time.Minute))})
	meters, _, err := newGrok(fake.server(t), auth).Fetch(context.Background())
	if err != nil || len(meters) != 1 {
		t.Fatalf("meters %+v err %v, want the billing meter", meters, err)
	}
}

// A rejected token is renewed by grok itself (`grok models` refreshes the
// cached login silently under grok's own lock, live-verified 2026-10-16), in
// the grok home the token came from, and billing is tried once more.
func TestGrokRejectedTokenIsRenewedByGrokOnce(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json", accept: "tok-new"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok-old", grokNow.Add(-time.Hour))})
	g := newGrok(fake.server(t), filepath.Join(t.TempDir(), "auth.json"), auth)
	var homes []string
	g.Refresh = func(_ context.Context, home string) error {
		homes = append(homes, home)
		raw, _ := json.Marshal(map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok-new", grokNow.Add(6*time.Hour))})
		return os.WriteFile(auth, raw, 0o600)
	}
	meters, _, err := g.Fetch(context.Background())
	if err != nil || len(meters) != 1 || meters[0].UsedPct != 1.0 {
		t.Fatalf("meters %+v err %v, want the billing meter after the renewal", meters, err)
	}
	if want := []string{filepath.Dir(auth)}; !reflect.DeepEqual(homes, want) {
		t.Fatalf("refresh ran in %v, want %v", homes, want)
	}
	if fake.calls != 2 {
		t.Fatalf("billing called %d times, want 2", fake.calls)
	}
}

func TestGrokStillRejectedAfterRenewalIsAnError(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json", status: http.StatusUnauthorized}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok", grokNow.Add(time.Hour))})
	g := newGrok(fake.server(t), auth)
	refreshed := 0
	g.Refresh = func(context.Context, string) error { refreshed++; return nil }
	if _, _, err := g.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err %v, want rejected", err)
	}
	if refreshed != 1 || fake.calls != 2 {
		t.Fatalf("refresh %d times, billing %d times; want 1 and 2", refreshed, fake.calls)
	}
}

func TestGrokRefreshRunsGrokModelsInThatHome(t *testing.T) {
	var gotEnv map[string]string
	var gotArgs []string
	run := func(_ context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
		gotEnv, gotArgs = env, append([]string{name}, args...)
		return nil, nil
	}
	if err := GrokRefresh(run)(context.Background(), "/u/.grok"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"grok", "models"}; !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("argv %v want %v", gotArgs, want)
	}
	if gotEnv["GROK_HOME"] != "/u/.grok" {
		t.Fatalf("GROK_HOME %q", gotEnv["GROK_HOME"])
	}
}

// creditUsagePercent is left out of the response when it is zero; a period
// with no percent is 0% used, not a missing meter.
func TestGrokOmittedPercentWithAPeriodIsZero(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing-zero.json"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok", grokNow.Add(time.Hour))})
	meters, _, err := newGrok(fake.server(t), auth).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 1 || meters[0].UsedPct != 0 || meters[0].Window != "weekly" || meters[0].ResetsAt == nil {
		t.Fatalf("meters %+v, want one 0%% weekly meter", meters)
	}
}

func TestGrokFreeTierHasNoMeter(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing-free.json"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok", grokNow.Add(time.Hour))})
	if _, _, err := newGrok(fake.server(t), auth).Fetch(context.Background()); !errors.Is(err, ErrNoData) {
		t.Fatalf("err %v, want ErrNoData: no percent and no period means no meter", err)
	}
}

const grokQuota429 = "API error (status 429 Too Many Requests): subscription:free-usage-exhausted: You've used all the included free usage for model grok-4.7 for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 619434/600000."

// The 429 a background "prompt suggest" side call gets (SPIKE-52): a rate limit
// that clears in a second, not an exhausted allowance.
const grokPerSecond429 = "API error (status 429 Too Many Requests): resource-exhausted: Requests per Second (actual/limit): 2/2"

func TestGrokExhaustionMeterWhenBillingGivesNone(t *testing.T) {
	now := grokNow
	fake := &fakeGrokBilling{billing: "testdata/grok-billing-free.json"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok", grokNow.Add(time.Hour))})
	ex := &GrokExhaustion{}
	g := newGrok(fake.server(t), auth)
	g.Exhaustion = ex
	if !ex.Note(grokQuota429, now) {
		t.Fatal("a 429 usage-exhausted error must be recorded")
	}
	meters, headline, err := g.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meters) != 1 || headline != meters[0].ID || meters[0].UsedPct != 100 {
		t.Fatalf("meters %+v headline %q, want one 100%% meter", meters, headline)
	}
	r := meters[0].ResetsAt
	if r == nil || !r.After(now) || r.After(now.Add(24*time.Hour)) {
		t.Fatalf("ResetsAt %v, want within 24h of %s", r, now)
	}
}

// Signed out, a recorded exhaustion still reports a full meter.
func TestGrokExhaustionMeterWhenSignedOut(t *testing.T) {
	ex := &GrokExhaustion{}
	ex.Note(grokQuota429, grokNow)
	g := &Grok{AuthPaths: []string{filepath.Join(t.TempDir(), "auth.json")}, Exhaustion: ex, Now: func() time.Time { return grokNow }}
	meters, _, err := g.Fetch(context.Background())
	if err != nil || len(meters) != 1 || meters[0].UsedPct != 100 {
		t.Fatalf("meters %+v err %v, want one 100%% meter", meters, err)
	}
}

func TestGrokExhaustionIgnoredOnceItExpiresOrBillingHasAMeter(t *testing.T) {
	now := grokNow
	ex := &GrokExhaustion{}
	ex.Note(grokQuota429, now)
	if _, ok := ex.Active(now.Add(25 * time.Hour)); ok {
		t.Fatal("an exhaustion past its reset must not be reported")
	}
	// billing is authoritative: a real 1% reading beats an older 429.
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok", grokNow.Add(time.Hour))})
	g := newGrok(fake.server(t), auth)
	g.Exhaustion = ex
	meters, _, err := g.Fetch(context.Background())
	if err != nil || meters[0].UsedPct != 1.0 {
		t.Fatalf("meters %+v err %v, want the billing 1%%", meters, err)
	}
}

func TestGrokExhaustionNoteClassifiesSignals(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name, text string
		want       bool
		until      time.Time
	}{
		{"429 quota", grokQuota429, true, now.Add(24 * time.Hour)},
		{"generic retry_state exhausted", "retry_state: exhausted", false, time.Time{}},
		{"retry_state exhausted update", `{"sessionUpdate":"retry_state","type":"exhausted"}`, false, time.Time{}},
		{"per-second 429", grokPerSecond429, false, time.Time{}},
		{"usage-exhausted code", "subscription:free-usage-exhausted", true, now.Add(24 * time.Hour)},
		{"token counts", "tokens (actual/limit): 619434/600000", true, now.Add(24 * time.Hour)},
		{"usage-limit card", "You hit your SuperGrok usage limit", true, now.Add(24 * time.Hour)},
		{"explicit reset", "subscription:free-usage-exhausted. Your usage window resets at 2026-10-17T00:00:00Z.", true, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)},
		{"network error", "connection reset by peer", false, time.Time{}},
		{"empty", "", false, time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ex := &GrokExhaustion{}
			if got := ex.Note(c.text, now); got != c.want {
				t.Fatalf("Note = %v want %v", got, c.want)
			}
			until, ok := ex.Active(now)
			if ok != c.want || (ok && !until.Equal(c.until)) {
				t.Fatalf("Active = %v,%v want %v,%v", until, ok, c.until, c.want)
			}
		})
	}
}

// A renewal that leaves the same rejected token in auth.json is not retried on
// the next poll; a different token (the user signed in again) is tried as usual.
func TestGrokFailedRenewalIsNotRepeatedForTheSameToken(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json", accept: "tok-new"}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok-dead", grokNow.Add(-time.Hour))})
	g := newGrok(fake.server(t), auth)
	refreshed := 0
	g.Refresh = func(context.Context, string) error { refreshed++; return nil }
	for i := 0; i < 3; i++ {
		if _, _, err := g.Fetch(context.Background()); err == nil {
			t.Fatal("a dead token must stay an error")
		}
	}
	if refreshed != 1 {
		t.Fatalf("refresh ran %d times for one dead token, want 1", refreshed)
	}
	raw, _ := json.Marshal(map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok-new", grokNow.Add(time.Hour))})
	if err := os.WriteFile(auth, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("a new login must be read: %v", err)
	}
}

// A renewal that fails outright (grok missing, grok erroring) also marks the
// token, so grok is not started on every poll.
func TestGrokRenewalErrorIsNotRepeatedForTheSameToken(t *testing.T) {
	fake := &fakeGrokBilling{billing: "testdata/grok-billing.json", status: http.StatusUnauthorized}
	auth := writeGrokAuth(t, map[string]map[string]any{"https://auth.x.ai::c": grokEntry("tok", grokNow.Add(time.Hour))})
	g := newGrok(fake.server(t), auth)
	refreshed := 0
	g.Refresh = func(context.Context, string) error { refreshed++; return errors.New("grok: not found") }
	for i := 0; i < 3; i++ {
		if _, _, err := g.Fetch(context.Background()); err == nil {
			t.Fatal("want an error")
		}
	}
	if refreshed != 1 {
		t.Fatalf("refresh ran %d times, want 1", refreshed)
	}
}

package usage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func writeMuseAuthFile(t *testing.T, home, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".config", "muse"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "muse", "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMuseTokenPrefersInlineAuthFileToken(t *testing.T) {
	home := t.TempDir()
	writeMuseAuthFile(t, home, `{"providers":{"meta":{"mechanism":"oauth","access_token":"dca:inline"}}}`)
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s ai.meta.dev.credentials -a meta -w`: {
			Out: `{"access_token":"dca:keychain"}`,
		},
	}}
	read := museKeychainToken(fake.Runner(), home, func(string) string { return "" })
	got, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "dca:inline" {
		t.Fatalf("token = %q, want the inline auth-file token", got)
	}
}

func TestMuseTokenFallsBackToKeychainForOAuthWithoutInlineToken(t *testing.T) {
	home := t.TempDir()
	writeMuseAuthFile(t, home, `{"providers":{"meta":{"mechanism":"oauth"}}}`)
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s ai.meta.dev.credentials -a meta -w`: {
			Out: `{"secret_schema_version":1,"api_key":"LLM|x","access_token":"dca:keychain"}`,
		},
	}}
	read := museKeychainToken(fake.Runner(), home, func(string) string { return "" })
	got, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "dca:keychain" {
		t.Fatalf("token = %q, want the keychain token", got)
	}
}

func TestMuseTokenRejectsNonDeviceTokensWithoutTransmitting(t *testing.T) {
	fake := &execx.Fake{Responses: map[string]execx.Result{
		`security find-generic-password -s ai.meta.dev.credentials -a meta -w`: {
			Out: `{"access_token":"LLM_dashboard_key"}`,
		},
	}}
	read := museKeychainToken(fake.Runner(), t.TempDir(), func(string) string { return "" })
	if _, err := read(context.Background()); err == nil {
		t.Fatal("a non-dca: token must be an error, never a transmittable credential")
	}
}

func TestMuseTokenHonorsAuthPathOverride(t *testing.T) {
	home := t.TempDir()
	custom := filepath.Join(home, "custom-auth.json")
	if err := os.WriteFile(custom, []byte(`{"providers":{"meta":{"mechanism":"oauth","access_token":"dca:custom"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	read := museKeychainToken(nil, home, func(k string) string {
		if k == "MUSE_AUTH_PATH" {
			return custom
		}
		return ""
	})
	got, err := read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "dca:custom" {
		t.Fatalf("token = %q, want the override-file token", got)
	}
}

func TestMuseTokenPropagatesAKeychainFailure(t *testing.T) {
	read := museKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("security: item not found")
	}, t.TempDir(), func(string) string { return "" })
	if _, err := read(context.Background()); err == nil {
		t.Fatal("a keychain failure must propagate, not fall back to a guess")
	}
}

func TestMuseTokenRejectsBadKeychainJSON(t *testing.T) {
	read := museKeychainToken(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("not json"), nil
	}, t.TempDir(), func(string) string { return "" })
	if _, err := read(context.Background()); err == nil {
		t.Fatal("malformed keychain output must be an error")
	}
}

func TestMuseFetchMapsMintQuotaToMeters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/muse-code/key" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-api-version") != "1.0.0" {
			t.Errorf("x-api-version = %q, want 1.0.0", r.Header.Get("x-api-version"))
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer dca:test" {
			t.Errorf("Authorization = %q, want the device token as bearer", auth)
		}
		fmt.Fprint(w, `{"require_payment":false,"is_subs_active":true,
			"subs_tier_name":"Muse Code Everyday Usage",
			"subs_usage":{"window":{"used_percent":9,"window_duration_mins":300,"resets_at":1790208865},
			"weekly":{"used_percent":12,"resets_at":1790553600}}}`)
	}))
	defer srv.Close()
	m := &MuseAPI{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, error) { return "dca:test", nil }}
	meters, headline, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Meter{}
	for _, mt := range meters {
		byID[mt.ID] = mt
	}
	five := byID["5h"]
	if five.Label != "5h" || five.Window != "5h" || five.UsedPct != 9 {
		t.Errorf("5h meter = %+v, want 5h/5h/9", five)
	}
	if five.ResetsAt == nil || !five.ResetsAt.Equal(time.Unix(1790208865, 0)) {
		t.Errorf("5h resets at %v, want %v", five.ResetsAt, time.Unix(1790208865, 0))
	}
	weekly := byID["weekly"]
	if weekly.Label != "Weekly" || weekly.Window != "weekly" || weekly.UsedPct != 12 {
		t.Errorf("weekly meter = %+v, want Weekly/weekly/12", weekly)
	}
	if headline != "5h" {
		t.Errorf("headline = %q, want 5h", headline)
	}
}

func TestMuseFetchErrsOnMissingSubsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"require_payment":false,"is_subs_active":true,
			"subs_tier_name":"Muse Code Everyday Usage","subs_usage":null}`)
	}))
	defer srv.Close()
	m := &MuseAPI{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, error) { return "dca:test", nil }}
	if _, _, err := m.Fetch(context.Background()); err == nil {
		t.Fatal("null subs_usage must be a no-quota error, never empty meters")
	}
}

func TestMuseFetchMapsErrorStatuses(t *testing.T) {
	for status, want := range map[int]string{
		401: "login was rejected",
		403: "login was rejected",
		500: "HTTP 500",
		503: "HTTP 503",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		m := &MuseAPI{BaseURL: srv.URL, HTTP: srv.Client(),
			ReadToken: func(context.Context) (string, error) { return "dca:test", nil }}
		_, _, err := m.Fetch(context.Background())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: err = %v, want substring %q", status, err, want)
		}
	}
}

func TestMuseFetchRateLimitBacksOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer srv.Close()
	m := &MuseAPI{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, error) { return "dca:test", nil }}
	_, _, err := m.Fetch(context.Background())
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("429 must be a *RateLimitError, got %v", err)
	}
}

func TestMuseFetchOmitsOutOfRangeResetKeepingPercent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"is_subs_active":true,
			"subs_usage":{"window":{"used_percent":9,"window_duration_mins":300,"resets_at":99999999999},
			"weekly":{"used_percent":12,"resets_at":0}}}`)
	}))
	defer srv.Close()
	m := &MuseAPI{BaseURL: srv.URL, HTTP: srv.Client(),
		ReadToken: func(context.Context) (string, error) { return "dca:test", nil }}
	meters, _, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, mt := range meters {
		if mt.ResetsAt != nil {
			t.Errorf("%s: ResetsAt = %v, want nil for out-of-range stamp", mt.ID, mt.ResetsAt)
		}
	}
	if meters[0].UsedPct != 9 || meters[1].UsedPct != 12 {
		t.Errorf("meters = %+v, want percents kept", meters)
	}
}

func TestMuseFetchRejectsInactiveAndBillingStates(t *testing.T) {
	for body, want := range map[string]string{
		`{"is_subs_active":false}`:                       "no Muse Code subscription",
		`{"require_payment":true,"is_subs_active":true}`: "payment method",
		`{"is_subs_active":true}`:                        "no quota",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		m := &MuseAPI{BaseURL: srv.URL, HTTP: srv.Client(),
			ReadToken: func(context.Context) (string, error) { return "dca:test", nil }}
		_, _, err := m.Fetch(context.Background())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("body %s: err = %v, want substring %q", body, err, want)
		}
	}
}

// Live: reads the real keychain and calls the real mint endpoint. Spends
// no model turn. Asserts meters only — never logs the token or identity.
//
//	MUSE_LIVE_MINT=1 go test ./internal/usage/ -run TestMuseMintLive -v
func TestMuseMintLive(t *testing.T) {
	if os.Getenv("MUSE_LIVE_MINT") != "1" {
		t.Skip("set MUSE_LIVE_MINT=1 to hit the real mint endpoint")
	}
	m := &MuseAPI{BaseURL: "https://api.meta.ai", HTTP: http.DefaultClient,
		ReadToken: museKeychainToken(execx.Run, os.Getenv("HOME"), os.Getenv)}
	meters, headline, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, mt := range meters {
		t.Logf("%s %s %.0f%% resets %v", mt.ID, mt.Label, mt.UsedPct, mt.ResetsAt)
	}
	if len(meters) != 2 || headline != "5h" {
		t.Fatalf("meters = %+v headline = %q, want 2 meters headed by 5h", meters, headline)
	}
}

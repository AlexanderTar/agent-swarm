package tunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db/dbtest"
	"github.com/AlexanderTar/swarmrun/internal/httpapi"
	"github.com/AlexanderTar/swarmrun/internal/relay"
)

func newManager(t *testing.T, secret string) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run", "tunnel.secret")
	if secret != "" {
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{DB: dbtest.Open(t), SecretPath: path, Now: time.Now,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "daemon") })}
	t.Cleanup(m.Stop)
	return m, path
}

func relayURL(t *testing.T) string {
	srv := httptest.NewServer(relay.New(testSecret, relay.Options{}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func waitState(t *testing.T, m *Manager, want string) httpapi.TunnelStatus {
	t.Helper()
	eventually(t, "state "+want, func() bool { return m.Status(context.Background()).State == want })
	return m.Status(context.Background())
}

func TestManagerApplyConnectsPersistsAndStops(t *testing.T) {
	m, _ := newManager(t, testSecret)
	url := relayURL(t)
	if st := m.Status(bg); st.State != "disabled" || st.Enabled || st.URL != "" {
		t.Fatalf("fresh: %+v", st)
	}
	if err := m.Apply(bg, true, url); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, "connected")
	if !st.Enabled || st.URL != url || st.PublicURL != "http"+strings.TrimPrefix(url, "ws") || st.ConnectedSince == nil {
		t.Fatalf("connected: %+v", st)
	}
	if err := m.Apply(bg, false, ""); err != nil {
		t.Fatal(err)
	}
	st = m.Status(bg)
	if st.State != "disabled" || st.Enabled || st.URL != url { // the URL is kept for the next enable
		t.Fatalf("disabled: %+v", st)
	}
	// re-enable without a URL uses the remembered one
	if err := m.Apply(bg, true, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "connected")
}

func TestManagerStartResumesPersistedState(t *testing.T) {
	m, path := newManager(t, testSecret)
	url := relayURL(t)
	if err := m.Apply(bg, true, url); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "connected")
	m.Stop()

	m2 := &Manager{DB: m.DB, SecretPath: path, Now: time.Now, Handler: m.Handler}
	t.Cleanup(m2.Stop)
	if err := m2.Start(bg); err != nil {
		t.Fatal(err)
	}
	waitState(t, m2, "connected")
}

func TestManagerRefusals(t *testing.T) {
	url := relayURL(t)
	for name, tc := range map[string]struct {
		secret  string
		enabled bool
		url     string
		want    string
	}{
		"no url yet":     {testSecret, true, "", "relay URL"},
		"plain http":     {testSecret, true, "http://relay.example", "wss://"},
		"junk":           {testSecret, true, "not a url", "wss://"},
		"credentials":    {testSecret, true, "wss://user:pw@relay.example", "wss://"},
		"no secret file": {"", true, url, "tunnel.secret"},
		"short secret":   {"abcd", true, url, "64 hex"},
		"non-hex secret": {strings.Repeat("z", 64), true, url, "64 hex"},
	} {
		m, _ := newManager(t, tc.secret)
		err := m.Apply(bg, tc.enabled, tc.url)
		var pe httpapi.PreflightError
		if err == nil || !errors.As(err, &pe) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want a PreflightError containing %q", name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), tc.secret) && tc.secret != "" {
			t.Errorf("%s: error leaks the secret", name)
		}
		if st := m.Status(bg); st.Enabled || st.State != "disabled" {
			t.Errorf("%s: a refused enable changed state: %+v", name, st)
		}
	}
}

func TestManagerNormalisesHTTPSToWSS(t *testing.T) {
	m, _ := newManager(t, testSecret)
	if err := m.Apply(bg, true, "https://relay.example/"); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(bg); st.URL != "wss://relay.example" || st.PublicURL != "https://relay.example" {
		t.Fatalf("%+v", st)
	}
}

func TestManagerStartWithBrokenSecretDoesNotFail(t *testing.T) {
	m, path := newManager(t, testSecret)
	if err := m.Apply(bg, true, relayURL(t)); err != nil {
		t.Fatal(err)
	}
	m.Stop()
	os.Remove(path)
	m2 := &Manager{DB: m.DB, SecretPath: path, Now: time.Now, Handler: m.Handler}
	t.Cleanup(m2.Stop)
	if err := m2.Start(bg); err != nil {
		t.Fatalf("daemon startup must not fail over the tunnel: %v", err)
	}
	if st := m2.Status(bg); st.State != "error" || st.LastError == "" {
		t.Fatalf("%+v", st)
	}
}

var bg = context.Background()

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/relay"
)

func runTunnel(t *testing.T, srvURL, home string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"tunnel"}, args...)
	full = append(full, "--home", home, "--daemon", srvURL)
	code := run(full, &out, &errOut)
	return code, out.String(), errOut.String()
}

const connectedJSON = `{"enabled":true,"state":"connected","url":"wss://r.example","public_url":"https://r.example",
	"connected_since":"2026-10-11T09:00:00Z","reconnects":2}`

func TestTunnelEnableSendsURLAndPrintsStatus(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{"PUT /api/tunnel": connectedJSON})
	defer srv.Close()
	code, out, errOut := runTunnel(t, srv.URL, home, "enable", "--url", "wss://r.example")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	var body map[string]any
	json.Unmarshal([]byte(lastBody(got, "/api/tunnel")), &body)
	if (*got)[0].method != "PUT" || (*got)[0].via != "cli" || body["enabled"] != true || body["url"] != "wss://r.example" {
		t.Fatalf("request %+v", (*got)[0])
	}
	if !strings.Contains(out, "https://r.example") {
		t.Fatalf("output %q", out)
	}
}

func TestTunnelEnableWithoutURLSendsNone(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{"PUT /api/tunnel": connectedJSON})
	defer srv.Close()
	if code, _, errOut := runTunnel(t, srv.URL, home, "enable"); code != 0 {
		t.Fatalf("exit %d %s", code, errOut)
	}
	var body map[string]any
	json.Unmarshal([]byte(lastBody(got, "/api/tunnel")), &body)
	if _, has := body["url"]; has || body["enabled"] != true {
		t.Fatalf("body %v", body)
	}
}

func TestTunnelEnableRefusalIsShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"error":{"code":"preflight_failed","message":"Run ` + "`swarm auth set`" + ` first: remote access needs a login."}}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	writeToken(t, home, "tok")
	code, out, errOut := runTunnel(t, srv.URL, home, "enable", "--url", "wss://r.example")
	if code != 1 || !strings.Contains(errOut, "swarm auth set") || strings.Contains(out, "enabled") {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
}

func TestTunnelDisable(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{"PUT /api/tunnel": `{"enabled":false,"state":"disabled","url":"wss://r.example","public_url":"https://r.example"}`})
	defer srv.Close()
	code, out, errOut := runTunnel(t, srv.URL, home, "disable")
	var body map[string]any
	json.Unmarshal([]byte(lastBody(got, "/api/tunnel")), &body)
	if code != 0 || body["enabled"] != false || !strings.Contains(strings.ToLower(out), "off") {
		t.Fatalf("exit %d out %q err %q body %v", code, out, errOut, body)
	}
}

func TestTunnelStatusShowsStateURLSinceAndReconnects(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{"GET /api/tunnel": connectedJSON})
	defer srv.Close()
	code, out, errOut := runTunnel(t, srv.URL, home, "status")
	if code != 0 {
		t.Fatalf("exit %d %s", code, errOut)
	}
	since := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04:05")
	for _, want := range []string{"connected", "https://r.example", "Connected since: " + since, "Reconnects: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
}

func TestTunnelStatusErrorAndDisabled(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{"GET /api/tunnel": `{"enabled":true,"state":"error","url":"wss://r.example","public_url":"https://r.example","reconnects":5,"last_error":"relay rejected the secret (401)"}`})
	code, out, _ := runTunnel(t, srv.URL, home, "status")
	srv.Close()
	if code != 0 || !strings.Contains(out, "error") || !strings.Contains(out, "relay rejected the secret (401)") || strings.Contains(out, "Connected since") {
		t.Fatalf("exit %d out %q", code, out)
	}
	srv, _, home = stubDaemon(t, map[string]string{"GET /api/tunnel": `{"enabled":false,"state":"disabled"}`})
	defer srv.Close()
	code, out, _ = runTunnel(t, srv.URL, home, "status")
	if code != 0 || !strings.Contains(out, "disabled") || !strings.Contains(out, "swarm tunnel enable") {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestTunnelUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"tunnel"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage: swarm tunnel") {
		t.Fatalf("exit %d %q", code, errOut.String())
	}
	if code := run([]string{"tunnel", "bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestDoctorGetsATunnelProbe(t *testing.T) {
	d := newDoctor(t.TempDir(), "http://127.0.0.1:1")
	if d.Tunnel == nil {
		t.Fatal("newDoctor must wire the tunnel probe")
	}
}

// The whole path against a real daemon: refused until a login exists, then it
// connects through a real relay and turns off again.
func TestTunnelAgainstARealDaemon(t *testing.T) {
	url, home, _ := startDaemon(t)
	rel := httptest.NewServer(relay.New(strings.Repeat("ab", 32), relay.Options{}))
	defer rel.Close()
	relURL := "ws" + strings.TrimPrefix(rel.URL, "http")

	code, _, errOut := runTunnel(t, url, home, "enable", "--url", relURL)
	if code != 1 || !strings.Contains(errOut, "swarm auth set") {
		t.Fatalf("without a login: exit %d %q", code, errOut)
	}
	if code, out, _ := runTunnel(t, url, home, "status"); code != 0 || !strings.Contains(out, "Tunnel: disabled") {
		t.Fatalf("status = %d %q", code, out)
	}

	if code, out, errOut := runAuth(t, url, home, "me@example.com\n"+goodPW+"\n"+goodPW+"\n", "set"); code != 0 {
		t.Fatalf("auth set: %d %s %s", code, out, errOut)
	}
	code, _, errOut = runTunnel(t, url, home, "enable", "--url", relURL)
	if code != 1 || !strings.Contains(errOut, "tunnel.secret") {
		t.Fatalf("without a secret file: exit %d %q", code, errOut)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "tunnel.secret"), []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runTunnel(t, url, home, "enable", "--url", relURL); code != 0 {
		t.Fatalf("enable: %d %s %s", code, out, errOut)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out, _ := runTunnel(t, url, home, "status")
		if strings.Contains(out, "Tunnel: connected") && strings.Contains(out, "Connected since:") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never connected: %s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// through the relay the daemon answers as a tunnel caller: the board needs a login
	resp, err := http.Get(rel.URL + "/api/items")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous request through the relay = %d, want 401", resp.StatusCode)
	}
	if code, _, _ := runTunnel(t, url, home, "disable"); code != 0 {
		t.Fatal("disable failed")
	}
	if _, out, _ := runTunnel(t, url, home, "status"); !strings.Contains(out, "Tunnel: disabled") {
		t.Fatalf("after disable: %s", out)
	}
}

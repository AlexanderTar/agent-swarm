package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runLAN(t *testing.T, srvURL, home string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{"lan"}, args...)
	full = append(full, "--home", home, "--url", srvURL)
	code := run(full, &out, &errOut)
	return code, out.String(), errOut.String()
}

const listeningJSON = `{"enabled":true,"state":"listening","ip":"192.168.1.20","hostname":"swarm.local","url":"http://swarm.local:7777"}`

func TestLANEnablePrintsTheAddressAndWarnings(t *testing.T) {
	pinGOOS(t, "darwin") // the macOS firewall copy; TestLANWarningsNameThePlatformsFirewall covers Linux
	srv, got, home := stubDaemon(t, map[string]string{"PUT /api/lan": listeningJSON})
	defer srv.Close()
	code, out, errOut := runLAN(t, srv.URL, home, "enable")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	var body map[string]any
	json.Unmarshal([]byte(lastBody(got, "/api/lan")), &body)
	if (*got)[0].method != "PUT" || (*got)[0].via != "cli" || body["enabled"] != true {
		t.Fatalf("request %+v", (*got)[0])
	}
	for _, want := range []string{"http://swarm.local:7777", "plain HTTP", "password", "firewall", "Allow"} {
		if !strings.Contains(out, want) {
			t.Errorf("enable output lacks %q:\n%s", want, out)
		}
	}
}

func TestLANEnableRefusalIsShownWithoutWarnings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"error":{"code":"preflight_failed","message":"Run ` + "`swarm auth set`" + ` first: remote access needs a login."}}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	writeToken(t, home, "tok")
	code, out, errOut := runLAN(t, srv.URL, home, "enable")
	if code != 1 || !strings.Contains(errOut, "swarm auth set") || strings.Contains(out, "LAN access on") {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
}

func TestLANEnableWhileOffline(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{"PUT /api/lan": `{"enabled":true,"state":"waiting","last_error":"Waiting for a network: no default route"}`})
	defer srv.Close()
	code, out, _ := runLAN(t, srv.URL, home, "enable")
	if code != 0 || !strings.Contains(out, "waiting") || !strings.Contains(out, "Waiting for a network") {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestLANDisable(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{"PUT /api/lan": `{"enabled":false,"state":"disabled"}`})
	defer srv.Close()
	code, out, errOut := runLAN(t, srv.URL, home, "disable")
	var body map[string]any
	json.Unmarshal([]byte(lastBody(got, "/api/lan")), &body)
	if code != 0 || body["enabled"] != false || !strings.Contains(strings.ToLower(out), "off") || strings.Contains(out, "plain HTTP") {
		t.Fatalf("exit %d out %q err %q body %v", code, out, errOut, body)
	}
}

func TestLANStatus(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{"GET /api/lan": listeningJSON})
	code, out, errOut := runLAN(t, srv.URL, home, "status")
	srv.Close()
	if code != 0 {
		t.Fatalf("exit %d %s", code, errOut)
	}
	for _, want := range []string{"LAN: listening", "http://swarm.local:7777", "192.168.1.20"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
	srv, _, home = stubDaemon(t, map[string]string{"GET /api/lan": `{"enabled":true,"state":"listening","ip":"192.168.1.20","url":"http://192.168.1.20:7777","last_error":"Another device on this network already uses the name swarm.local."}`})
	defer srv.Close()
	code, out, _ = runLAN(t, srv.URL, home, "status")
	if code != 0 || !strings.Contains(out, "http://192.168.1.20:7777") || !strings.Contains(out, "already uses the name swarm.local") {
		t.Fatalf("exit %d out %q", code, out)
	}
	srv2, _, home2 := stubDaemon(t, map[string]string{"GET /api/lan": `{"enabled":false,"state":"disabled"}`})
	defer srv2.Close()
	code, out, _ = runLAN(t, srv2.URL, home2, "status")
	if code != 0 || !strings.Contains(out, "disabled") || !strings.Contains(out, "swarm lan enable") {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestLANUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"lan"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage: swarm lan") {
		t.Fatalf("exit %d %q", code, errOut.String())
	}
	if code := run([]string{"lan", "bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestDoctorGetsALANProbe(t *testing.T) {
	d := newDoctor(t.TempDir(), "http://127.0.0.1:1")
	if d.LAN == nil {
		t.Fatal("newDoctor must wire the LAN probe")
	}
}

// Against a real daemon the refusal comes from the daemon itself. Enabling for
// real would bind this machine's LAN address and advertise swarm.local, so the
// test stops at the refusal and the status.
func TestLANAgainstARealDaemon(t *testing.T) {
	url, home, _ := startDaemon(t)
	code, out, errOut := runLAN(t, url, home, "enable")
	if code != 1 || !strings.Contains(errOut, "swarm auth set") || strings.Contains(out, "plain HTTP") {
		t.Fatalf("without a login: exit %d out %q err %q", code, out, errOut)
	}
	if code, out, _ := runLAN(t, url, home, "status"); code != 0 || !strings.Contains(out, "LAN: disabled") {
		t.Fatalf("status = %d %q", code, out)
	}
}

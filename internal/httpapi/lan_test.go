package httpapi

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

type fakeLAN struct {
	st      LANStatus
	applied []bool
	err     error
}

func (f *fakeLAN) Status(context.Context) LANStatus { return f.st }
func (f *fakeLAN) Apply(_ context.Context, enabled bool) error {
	f.applied = append(f.applied, enabled)
	return f.err
}

func lanEnv(t *testing.T, fl *fakeLAN, creds bool) *env {
	t.Helper()
	withOwnIPs(t, "192.168.1.20")
	return newEnv(t, func(d *Deps) {
		d.LAN = fl
		d.HasCredentials = func(context.Context) bool { return creds }
	})
}

func TestLANRoutes(t *testing.T) {
	fl := &fakeLAN{st: LANStatus{Enabled: true, State: "listening", IP: "192.168.1.20", Hostname: "swarm.local", URL: "http://swarm.local:7777"}}
	e := lanEnv(t, fl, true)
	status, b := e.api("GET", "/api/lan", nil)
	if got := decode[LANStatus](t, b); status != 200 || got.State != "listening" || got.URL != "http://swarm.local:7777" || got.IP != "192.168.1.20" {
		t.Fatalf("GET = %d %s", status, b)
	}
	if status, _ = e.api("PUT", "/api/lan", map[string]any{"enabled": true}); status != 200 || len(fl.applied) != 1 || !fl.applied[0] {
		t.Fatalf("PUT enable = %d, applied %v", status, fl.applied)
	}
	if status, _ = e.api("PUT", "/api/lan", map[string]any{"enabled": false}); status != 200 || fl.applied[1] {
		t.Fatalf("PUT disable = %d, applied %v", status, fl.applied)
	}
	if status, b = e.api("PUT", "/api/lan", map[string]any{"enabled": "yes"}); status != 400 {
		t.Fatalf("bad body = %d %s", status, b)
	}
}

func TestLANEnableRefusedWithoutCredentials(t *testing.T) {
	fl := &fakeLAN{}
	e := lanEnv(t, fl, false)
	status, b := e.api("PUT", "/api/lan", map[string]any{"enabled": true})
	wantErr(t, status, b, 422, "preflight_failed", "Run `swarm auth set` first: remote access needs a login.")
	if len(fl.applied) != 0 {
		t.Fatalf("applied %v", fl.applied)
	}
	// turning it off is always allowed
	if status, _ = e.api("PUT", "/api/lan", map[string]any{"enabled": false}); status != 200 || len(fl.applied) != 1 {
		t.Fatalf("disable = %d, applied %v", status, fl.applied)
	}
}

func TestLANRoutesAreLocalOnly(t *testing.T) {
	fl := &fakeLAN{}
	e := lanEnv(t, fl, true)
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		for _, rt := range []struct{ method, body string }{{"GET", ""}, {"PUT", `{"enabled":true}`}} {
			for _, tok := range []string{"", daemonToken} {
				r := asRemote(origin, rt.method, "/api/lan", rt.body)
				if tok != "" {
					r.Header.Set("Authorization", "Bearer "+tok)
				}
				rec := serve(e, r)
				wantErr(t, rec.Code, rec.Body.Bytes(), 404, "not_found", "Unknown API route.")
			}
		}
	}
	if len(fl.applied) != 0 {
		t.Fatalf("a remote caller reached Apply: %v", fl.applied)
	}
	if rec := serve(e, localReq("GET", "/api/lan", nil)); rec.Code != 401 {
		t.Fatalf("tokenless = %d", rec.Code)
	}
}

func TestLANWithoutAManagerReportsDisabled(t *testing.T) {
	e := newEnv(t)
	status, b := e.api("GET", "/api/lan", nil)
	if got := decode[LANStatus](t, b); status != 200 || got.State != "disabled" || got.Enabled {
		t.Fatalf("GET = %d %s", status, b)
	}
	if status, _ = e.api("PUT", "/api/lan", map[string]any{"enabled": false}); status != 503 {
		t.Fatalf("PUT = %d", status)
	}
}

func TestSettingsCarryNoLANKeys(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.DB.ExecContext(bg, `INSERT INTO settings (key, value_json, updated_at) VALUES ('lan.enabled', 'true', 1)`); err != nil {
		t.Fatal(err)
	}
	_, b := e.api("GET", "/api/settings", nil)
	if strings.Contains(string(b), "lan") {
		t.Fatalf("GET /api/settings leaks lan keys: %s", b)
	}
	rt := reflect.TypeOf(settings.Settings{})
	for i := 0; i < rt.NumField(); i++ {
		if tag := rt.Field(i).Tag.Get("json"); strings.HasPrefix(tag, "lan") {
			t.Errorf("settings.Settings has a %q field", tag)
		}
	}
	var body map[string]any
	json.Unmarshal(b, &body)
	body["lan.enabled"] = false
	body["lan"] = map[string]any{"enabled": false}
	if status, out := e.api("PUT", "/api/settings", body); status != 200 {
		t.Fatalf("PUT = %d %s", status, out)
	}
	var en string
	e.s.DB.QueryRowContext(bg, `SELECT value_json FROM settings WHERE key='lan.enabled'`).Scan(&en)
	if en != "true" {
		t.Fatalf("PUT /api/settings rewrote the lan row: %s", en)
	}
	if _, b = e.api("GET", "/api/settings", nil); strings.Contains(string(b), "lan") {
		t.Fatalf("settings after PUT: %s", b)
	}
}

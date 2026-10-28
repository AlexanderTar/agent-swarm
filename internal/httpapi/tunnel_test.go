package httpapi

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/settings"
)

type applyCall struct {
	enabled bool
	url     string
}

type fakeTunnel struct {
	st      TunnelStatus
	applied []applyCall
	err     error
}

func (f *fakeTunnel) Status(context.Context) TunnelStatus { return f.st }
func (f *fakeTunnel) Apply(_ context.Context, enabled bool, url string) error {
	f.applied = append(f.applied, applyCall{enabled, url})
	return f.err
}

func tunnelEnv(t *testing.T, ft *fakeTunnel, creds bool) *env {
	t.Helper()
	withOwnIPs(t, "192.168.1.20")
	return newEnv(t, func(d *Deps) {
		d.Tunnel = ft
		d.HasCredentials = func(context.Context) bool { return creds }
	})
}

func TestTunnelRoutes(t *testing.T) {
	since := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	ft := &fakeTunnel{st: TunnelStatus{Enabled: true, State: "connected", URL: "wss://r.example", PublicURL: "https://r.example",
		ConnectedSince: &since, Reconnects: 2}}
	e := tunnelEnv(t, ft, true)

	status, b := e.api("GET", "/api/tunnel", nil)
	got := decode[TunnelStatus](t, b)
	if status != 200 || got.State != "connected" || got.PublicURL != "https://r.example" || got.Reconnects != 2 ||
		got.ConnectedSince == nil || !got.ConnectedSince.Equal(since) {
		t.Fatalf("GET = %d %s", status, b)
	}
	if strings.Contains(string(b), "secret") {
		t.Fatalf("status must never mention a secret: %s", b)
	}

	status, _ = e.api("PUT", "/api/tunnel", map[string]any{"enabled": true, "url": "wss://r.example"})
	if status != 200 || len(ft.applied) != 1 || ft.applied[0] != (applyCall{true, "wss://r.example"}) {
		t.Fatalf("PUT enable = %d, applied %+v", status, ft.applied)
	}
	// disabling needs no url and no credentials check
	if status, _ = e.api("PUT", "/api/tunnel", map[string]any{"enabled": false}); status != 200 || ft.applied[1] != (applyCall{false, ""}) {
		t.Fatalf("PUT disable = %d, applied %+v", status, ft.applied)
	}
	// a manager refusal (no secret file, bad URL) is a 422 with its message
	ft.err = PreflightError("There's no relay secret yet.")
	status, b = e.api("PUT", "/api/tunnel", map[string]any{"enabled": true, "url": "wss://r.example"})
	wantErr(t, status, b, 422, "preflight_failed", "There's no relay secret yet.")
	if status, b = e.api("PUT", "/api/tunnel", map[string]any{"enabled": "yes"}); status != 400 {
		t.Fatalf("bad body = %d %s", status, b)
	}
}

func TestTunnelEnableRefusedWithoutCredentials(t *testing.T) {
	ft := &fakeTunnel{}
	e := tunnelEnv(t, ft, false)
	status, b := e.api("PUT", "/api/tunnel", map[string]any{"enabled": true, "url": "wss://r.example"})
	wantErr(t, status, b, 422, "preflight_failed", "Run `swarm auth set` first: remote access needs a login.")
	if len(ft.applied) != 0 {
		t.Fatalf("applied %+v", ft.applied)
	}
	// turning it off is always allowed
	if status, _ = e.api("PUT", "/api/tunnel", map[string]any{"enabled": false}); status != 200 || len(ft.applied) != 1 {
		t.Fatalf("disable = %d, applied %+v", status, ft.applied)
	}
}

func TestTunnelRoutesAreLocalOnly(t *testing.T) {
	ft := &fakeTunnel{}
	e := tunnelEnv(t, ft, true)
	for _, origin := range []Origin{OriginLAN, OriginTunnel} {
		for _, rt := range []struct{ method, body string }{{"GET", ""}, {"PUT", `{"enabled":true,"url":"wss://evil.example"}`}} {
			for _, tok := range []string{"", daemonToken} {
				r := asRemote(origin, rt.method, "/api/tunnel", rt.body)
				if tok != "" {
					r.Header.Set("Authorization", "Bearer "+tok)
				}
				rec := serve(e, r)
				wantErr(t, rec.Code, rec.Body.Bytes(), 404, "not_found", "Unknown API route.")
			}
		}
	}
	if len(ft.applied) != 0 {
		t.Fatalf("a remote caller reached Apply: %+v", ft.applied)
	}
	// without the daemon token, locally: 401
	if rec := serve(e, localReq("GET", "/api/tunnel", nil)); rec.Code != 401 {
		t.Fatalf("tokenless = %d", rec.Code)
	}
}

func TestTunnelWithoutAManagerReportsDisabled(t *testing.T) {
	e := newEnv(t)
	status, b := e.api("GET", "/api/tunnel", nil)
	if got := decode[TunnelStatus](t, b); status != 200 || got.State != "disabled" || got.Enabled {
		t.Fatalf("GET = %d %s", status, b)
	}
	if status, _ = e.api("PUT", "/api/tunnel", map[string]any{"enabled": false}); status != 503 {
		t.Fatalf("PUT = %d", status)
	}
}

func TestSettingsCarryNoTunnelKeys(t *testing.T) {
	e := newEnv(t)
	for k, v := range map[string]string{"tunnel.enabled": `true`, "tunnel.url": `"wss://r.example"`} {
		if _, err := e.s.DB.ExecContext(bg, `INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, 1)`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	_, b := e.api("GET", "/api/settings", nil)
	if strings.Contains(string(b), "tunnel") {
		t.Fatalf("GET /api/settings leaks tunnel keys: %s", b)
	}
	rt := reflect.TypeOf(settings.Settings{})
	for i := 0; i < rt.NumField(); i++ {
		if tag := rt.Field(i).Tag.Get("json"); strings.HasPrefix(tag, "tunnel") || strings.HasPrefix(tag, "lan") {
			t.Errorf("settings.Settings has a %q field", tag)
		}
	}
	// a PUT that names tunnel keys changes nothing
	var body map[string]any
	json.Unmarshal(b, &body)
	body["tunnel.enabled"], body["tunnel.url"] = false, "wss://evil.example"
	body["tunnel"] = map[string]any{"enabled": false, "url": "wss://evil.example"}
	if status, out := e.api("PUT", "/api/settings", body); status != 200 {
		t.Fatalf("PUT = %d %s", status, out)
	}
	var en, url string
	e.s.DB.QueryRowContext(bg, `SELECT value_json FROM settings WHERE key='tunnel.enabled'`).Scan(&en)
	e.s.DB.QueryRowContext(bg, `SELECT value_json FROM settings WHERE key='tunnel.url'`).Scan(&url)
	if en != "true" || url != `"wss://r.example"` {
		t.Fatalf("PUT /api/settings rewrote tunnel rows: %s %s", en, url)
	}
	_, b = e.api("GET", "/api/settings", nil)
	if strings.Contains(string(b), "tunnel") || strings.Contains(string(b), "evil") {
		t.Fatalf("settings after PUT: %s", b)
	}
}

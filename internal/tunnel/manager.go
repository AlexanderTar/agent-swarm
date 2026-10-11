package tunnel

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/httpapi"
	"github.com/AlexanderTar/agent-swarm/internal/relay"
)

// The switch lives in its own settings-table rows, not in settings.Settings,
// so GET and PUT /api/settings can neither show nor change it.
const (
	rowEnabled = "tunnel.enabled"
	rowURL     = "tunnel.url"
)

// Manager owns the daemon's one Client: it starts from the persisted rows and
// applies changes without a restart. The relay secret is read from SecretPath
// when a connection is (re)started and is never stored in the database.
type Manager struct {
	DB         *db.DB
	Handler    http.Handler
	SecretPath string // ~/.swarm/run/tunnel.secret
	Now        func() time.Time
	Log        func(string, ...any)

	mu      sync.Mutex
	parent  context.Context
	enabled bool
	url     string
	client  *Client
	stop    context.CancelFunc
	done    chan struct{}
	lastErr string // why the last start failed, shown while no client runs
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Start loads the persisted switch and, if on, connects. A broken secret file
// or URL is reported in Status, never as a startup failure.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parent = ctx
	var en string
	if err := m.DB.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, rowEnabled).Scan(&en); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	m.enabled = en == "true"
	var u string
	if err := m.DB.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, rowURL).Scan(&u); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	m.url = strings.Trim(u, `"`)
	if m.enabled {
		if err := m.startLocked(); err != nil {
			m.lastErr = err.Error()
			if m.Log != nil {
				m.Log("tunnel: %v", err)
			}
		}
	}
	return nil
}

// Stop ends the connection and waits for it.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// Apply switches the tunnel. An empty url keeps the stored one. Nothing is
// persisted or changed when it returns an error.
func (m *Manager) Apply(ctx context.Context, enabled bool, rawURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.url
	if rawURL != "" {
		var err error
		if u, err = normalizeURL(rawURL); err != nil {
			return err
		}
	}
	if enabled {
		if u == "" {
			return httpapi.PreflightError("Give the relay URL the first time: swarm tunnel enable --url wss://your-relay.up.railway.app")
		}
		if _, err := m.secret(); err != nil {
			return err
		}
	}
	if err := m.save(ctx, enabled, u); err != nil {
		return err
	}
	m.stopLocked()
	m.enabled, m.url, m.lastErr = enabled, u, ""
	if enabled {
		if m.parent == nil {
			m.parent = context.Background()
		}
		return m.startLocked()
	}
	return nil
}

func (m *Manager) save(ctx context.Context, enabled bool, u string) error {
	return m.DB.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range map[string]string{rowEnabled: boolJSON(enabled), rowURL: `"` + u + `"`} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
				k, v, db.Millis(m.now())); err != nil {
				return err
			}
		}
		return nil
	})
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// secret reads and checks the relay secret file.
func (m *Manager) secret() (string, error) {
	b, err := os.ReadFile(m.SecretPath)
	if err != nil {
		return "", httpapi.PreflightError("There's no relay secret yet. It should be in " + m.SecretPath + " (created when the relay is deployed).")
	}
	s := strings.TrimSpace(string(b))
	if !relay.ValidSecret(s) {
		return "", httpapi.PreflightError("The relay secret in " + m.SecretPath + " must be at least 64 hex characters.")
	}
	return s, nil
}

func (m *Manager) startLocked() error {
	sec, err := m.secret()
	if err != nil {
		return err
	}
	c := &Client{URL: m.url, Secret: sec, Handler: m.Handler, Log: m.Log, Now: m.Now}
	ctx, cancel := context.WithCancel(m.parent)
	done := make(chan struct{})
	m.client, m.stop, m.done = c, cancel, done
	go func() { defer close(done); c.Run(ctx) }()
	return nil
}

func (m *Manager) stopLocked() {
	if m.stop == nil {
		return
	}
	m.stop()
	<-m.done
	m.client, m.stop, m.done = nil, nil, nil
}

// Status reports the switch and, while connected or trying, the live client.
func (m *Manager) Status(context.Context) httpapi.TunnelStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := httpapi.TunnelStatus{Enabled: m.enabled, State: "disabled", URL: m.url, PublicURL: publicURL(m.url)}
	if m.client != nil {
		cs := m.client.Status()
		st.State, st.Reconnects, st.LastError = cs.State, cs.Reconnects, cs.LastError
		if !cs.ConnectedSince.IsZero() {
			t := cs.ConnectedSince
			st.ConnectedSince = &t
		}
	} else if m.enabled && m.lastErr != "" {
		st.State, st.LastError = "error", m.lastErr
	}
	return st
}

// normalizeURL turns what a user pastes into the relay base the client dials:
// wss://host (https:// is accepted), no credentials, path or query. Plain ws://
// is allowed only for a loopback relay.
func normalizeURL(raw string) (string, error) {
	bad := httpapi.PreflightError("The relay URL must look like wss://your-relay.up.railway.app")
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", bad
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		if h := u.Hostname(); h != "localhost" && !net.ParseIP(h).IsLoopback() {
			return "", bad
		}
		u.Scheme = "ws"
	default:
		return "", bad
	}
	u.Path = ""
	return u.String(), nil
}

// publicURL is the https (or http) address people open in a browser.
func publicURL(wsURL string) string {
	switch {
	case strings.HasPrefix(wsURL, "wss://"):
		return "https://" + strings.TrimPrefix(wsURL, "wss://")
	case strings.HasPrefix(wsURL, "ws://"):
		return "http://" + strings.TrimPrefix(wsURL, "ws://")
	}
	return ""
}

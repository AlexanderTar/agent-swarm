package lan

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/httpapi"
)

// The switch lives in its own settings-table row, not in settings.Settings, so
// GET and PUT /api/settings can neither show nor change it.
const rowEnabled = "lan.enabled"

const hostname = "swarm.local"

// Status is what GET /api/lan reports.
type Status = httpapi.LANStatus

// RegStatus is how the registrar is doing.
type RegStatus struct {
	Registered bool   // Bonjour confirmed swarm.local
	Err        string // e.g. the name is taken, or dns-sd can't start
}

// Registrar advertises swarm.local for ip:port while it runs.
type Registrar interface {
	Start(ip net.IP, port int) error
	Stop() error
	Status() RegStatus
}

// Manager owns the daemon's one LAN listener. It binds the Mac's primary LAN
// IPv4 address (never a wildcard), follows that address as it changes, and
// keeps the Bonjour registration pointed at it.
type Manager struct {
	DB        *db.DB
	Port      int
	Handler   http.Handler
	PrimaryIP func() (net.IP, error) // the LAN address, or an error while offline
	Registrar Registrar
	Log       func(string, ...any)
	Now       func() time.Time

	// Tick, if set, replaces the 30 s IP-watch timer (tests).
	Tick <-chan time.Time
	// Listen, if set, replaces net.Listen (tests).
	Listen func(network, addr string) (net.Listener, error)

	mu      sync.Mutex
	parent  context.Context
	enabled bool
	cancel  context.CancelFunc
	ip      net.IP // the address the listener is bound to; nil when not listening
	srv     *http.Server
	lastErr string
	failed  bool // the last bind attempt failed, as opposed to having no address yet
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) logf(f string, a ...any) {
	if m.Log != nil {
		m.Log(f, a...)
	}
}

// Start loads the persisted switch and, if on, starts listening.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parent = ctx
	var en string
	if err := m.DB.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, rowEnabled).Scan(&en); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if m.enabled = en == "true"; m.enabled {
		m.startLocked()
	}
	return nil
}

// Stop ends the watch, the listener and the registration.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// Apply switches LAN access and persists the choice. Enabling while offline
// succeeds: the manager waits for an address.
func (m *Manager) Apply(ctx context.Context, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.DB.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		rowEnabled, strconv.FormatBool(enabled), db.Millis(m.now())); err != nil {
		return err
	}
	m.stopLocked()
	m.enabled = enabled
	if enabled {
		if m.parent == nil {
			m.parent = context.Background()
		}
		m.startLocked()
	}
	return nil
}

func (m *Manager) startLocked() {
	ctx, cancel := context.WithCancel(m.parent)
	m.cancel = cancel
	m.checkLocked()
	tick := m.Tick
	if tick == nil {
		t := time.NewTicker(30 * time.Second)
		go func() { <-ctx.Done(); t.Stop() }()
		tick = t.C
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick:
				m.mu.Lock()
				if ctx.Err() == nil { // Stop or Apply got the lock first
					m.checkLocked()
				}
				m.mu.Unlock()
			}
		}
	}()
}

func (m *Manager) stopLocked() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	m.cancel = nil
	m.unbindLocked()
	m.lastErr, m.failed = "", false
}

// checkLocked brings the listener in line with the current LAN address.
func (m *Manager) checkLocked() {
	ip, err := m.PrimaryIP()
	if err == nil {
		ip, err = usable(ip)
	}
	if err != nil {
		if m.ip != nil {
			m.logf("lan: lost %s: %v", m.ip, err)
		}
		m.unbindLocked()
		m.lastErr, m.failed = "Waiting for a network: "+err.Error(), false
		return
	}
	if m.ip != nil && m.ip.Equal(ip) {
		return
	}
	m.unbindLocked()
	listen := m.Listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp4", net.JoinHostPort(ip.String(), strconv.Itoa(m.Port)))
	if err != nil {
		m.lastErr, m.failed = "Can't listen on "+ip.String()+": "+err.Error(), true
		m.logf("lan: %s", m.lastErr)
		return
	}
	srv := &http.Server{Handler: m.Handler, ReadHeaderTimeout: 10 * time.Second,
		ConnContext: httpapi.ConnContext(httpapi.ListenerLAN)}
	go srv.Serve(ln)
	m.srv, m.ip, m.lastErr, m.failed = srv, ip, "", false
	m.logf("lan: listening on %s", ln.Addr())
	if m.Registrar != nil {
		if err := m.Registrar.Start(ip, m.Port); err != nil {
			m.lastErr = "Can't advertise " + hostname + ": " + err.Error()
			m.logf("lan: %s", m.lastErr)
		}
	}
}

// unbindLocked closes the listener (and with it every open connection) and
// withdraws the advertisement.
func (m *Manager) unbindLocked() {
	if m.srv == nil {
		return
	}
	m.srv.Close()
	m.srv, m.ip = nil, nil
	if m.Registrar != nil {
		m.Registrar.Stop()
	}
}

// Status reports the switch and what the listener and registrar are doing.
func (m *Manager) Status(context.Context) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Enabled: m.enabled, State: "disabled"}
	if !m.enabled {
		return st
	}
	st.LastError = m.lastErr
	if m.ip == nil {
		st.State = "waiting"
		if m.failed {
			st.State = "error"
		}
		return st
	}
	st.State, st.IP = "listening", m.ip.String()
	st.URL = "http://" + net.JoinHostPort(st.IP, strconv.Itoa(m.Port))
	if m.Registrar != nil {
		rs := m.Registrar.Status()
		if rs.Registered {
			st.Hostname = hostname
			st.URL = "http://" + net.JoinHostPort(hostname, strconv.Itoa(m.Port))
		}
		if st.LastError == "" {
			st.LastError = rs.Err
		}
	}
	return st
}

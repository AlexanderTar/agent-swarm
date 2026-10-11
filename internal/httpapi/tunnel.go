package httpapi

import (
	"context"
	"net/http"
	"time"
)

// TunnelStatus is the tunnel as `swarm tunnel status` shows it. It never
// carries the relay secret.
type TunnelStatus struct {
	Enabled        bool       `json:"enabled"`
	State          string     `json:"state"` // disabled | connecting | connected | error
	URL            string     `json:"url,omitempty"`
	PublicURL      string     `json:"public_url,omitempty"`
	ConnectedSince *time.Time `json:"connected_since,omitempty"`
	Reconnects     int        `json:"reconnects"`
	LastError      string     `json:"last_error,omitempty"`
}

// TunnelControl is the daemon's tunnel manager as the API sees it.
type TunnelControl interface {
	Status(ctx context.Context) TunnelStatus
	// Apply persists and applies the switch. An empty url keeps the stored one.
	Apply(ctx context.Context, enabled bool, url string) error
}

// PreflightError is a refusal the user can fix (no secret file, bad URL); the
// API reports it as 422 preflight_failed.
type PreflightError string

func (e PreflightError) Error() string { return string(e) }

func (s *Server) tunnelRoutes() []route {
	return []route{
		{"GET", "/api/tunnel", authDaemon, s.getTunnel},
		{"PUT", "/api/tunnel", authDaemon, s.putTunnel},
	}
}

func (s *Server) getTunnel(w http.ResponseWriter, r *http.Request) {
	if s.Tunnel == nil {
		writeJSON(w, http.StatusOK, TunnelStatus{State: "disabled"})
		return
	}
	writeJSON(w, http.StatusOK, s.Tunnel.Status(r.Context()))
}

func (s *Server) putTunnel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool   `json:"enabled"`
		URL     string `json:"url"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if s.Tunnel == nil {
		s.writeErr(w, apiErr(http.StatusServiceUnavailable, "unavailable", "The tunnel isn't available in this daemon."))
		return
	}
	if body.Enabled && (s.HasCredentials == nil || !s.HasCredentials(r.Context())) {
		s.writeErr(w, apiErr(http.StatusUnprocessableEntity, "preflight_failed",
			"Run `swarm auth set` first: remote access needs a login."))
		return
	}
	if err := s.Tunnel.Apply(r.Context(), body.Enabled, body.URL); err != nil {
		if pe, ok := err.(PreflightError); ok {
			s.writeErr(w, apiErr(http.StatusUnprocessableEntity, "preflight_failed", pe.Error()))
			return
		}
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Tunnel.Status(r.Context()))
}

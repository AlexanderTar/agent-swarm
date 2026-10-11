package httpapi

import (
	"context"
	"net/http"
)

// LANStatus is LAN access as `swarm lan status` shows it.
type LANStatus struct {
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"` // disabled | waiting | listening | error
	IP        string `json:"ip,omitempty"`
	Hostname  string `json:"hostname,omitempty"` // swarm.local, once Bonjour has registered it
	URL       string `json:"url,omitempty"`      // what to open on the phone
	LastError string `json:"last_error,omitempty"`
}

// LANControl is the daemon's LAN manager as the API sees it.
type LANControl interface {
	Status(ctx context.Context) LANStatus
	// Apply persists and applies the switch.
	Apply(ctx context.Context, enabled bool) error
}

func (s *Server) lanRoutes() []route {
	return []route{
		{"GET", "/api/lan", authDaemon, s.getLAN},
		{"PUT", "/api/lan", authDaemon, s.putLAN},
	}
}

func (s *Server) getLAN(w http.ResponseWriter, r *http.Request) {
	if s.LAN == nil {
		writeJSON(w, http.StatusOK, LANStatus{State: "disabled"})
		return
	}
	writeJSON(w, http.StatusOK, s.LAN.Status(r.Context()))
}

func (s *Server) putLAN(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if s.LAN == nil {
		s.writeErr(w, apiErr(http.StatusServiceUnavailable, "unavailable", "LAN access isn't available in this daemon."))
		return
	}
	if body.Enabled && (s.HasCredentials == nil || !s.HasCredentials(r.Context())) {
		s.writeErr(w, apiErr(http.StatusUnprocessableEntity, "preflight_failed",
			"Run `swarm auth set` first: remote access needs a login."))
		return
	}
	if err := s.LAN.Apply(r.Context(), body.Enabled); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.LAN.Status(r.Context()))
}

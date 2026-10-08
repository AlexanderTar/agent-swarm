package httpapi

import "net/http"

type lowTokenBody struct {
	On bool `json:"on"`
}

type lowTokenAgentWire struct {
	Agent    string `json:"agent"`
	LowToken *bool  `json:"low_token"`
	Notified int    `json:"notified"`
}

type lowTokenAllWire struct {
	On       bool `json:"on"`
	Notified int  `json:"notified"`
}

// setAgentLowToken is POST /api/agents/{name}/low-token.
func (s *Server) setAgentLowToken(w http.ResponseWriter, r *http.Request) {
	var body lowTokenBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	name := r.PathValue("name")
	n, err := s.RT.SetLowToken(r.Context(), name, body.On)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lowTokenAgentWire{Agent: name, LowToken: &body.On, Notified: n})
}

// setAllLowToken is POST /api/low-token: the global default.
func (s *Server) setAllLowToken(w http.ResponseWriter, r *http.Request) {
	var body lowTokenBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	n, err := s.RT.SetLowTokenAll(r.Context(), body.On)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lowTokenAllWire{On: body.On, Notified: n})
}

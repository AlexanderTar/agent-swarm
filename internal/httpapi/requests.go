// requests, artifacts, notifications, usage and advice routes (P2 T33, §7).
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/usage"
)

func (s *Server) requestRoutes() []route {
	return []route{
		{"GET", "/api/requests", authDaemon, s.listRequests},
		{"POST", "/api/requests/{id}/answer", authDaemon, s.answerRequest},
		{"POST", "/api/requests/{id}/approve", authDaemon, s.approveRequest},
		{"POST", "/api/requests/{id}/request-changes", authDaemon, s.requestChanges},
		{"POST", "/api/requests/{id}/confirm-repos", authDaemon, s.confirmRepos},
		{"POST", "/api/requests/{id}/close-spike", authDaemon, s.closeSpike},
		{"GET", "/api/artifacts/{id}", authDaemon, s.getArtifact},
		{"GET", "/api/notifications", authDaemon, s.listNotifications},
		{"POST", "/api/notifications/read-all", authDaemon, s.readAllNotifications},
		{"POST", "/api/notifications/{id}/read", authDaemon, s.readNotification},
		{"GET", "/api/usage", authDaemon, s.listUsage},
		{"POST", "/api/usage/refresh", authDaemon, s.refreshUsage},
		{"GET", "/api/agents/{name}/advice", authDaemon, s.agentAdvice},
	}
}

// viaFromBody is W7 for /api/requests/*: via lives in the body, and a missing
// or unknown value means board.
func viaFromBody(v string) string {
	switch v {
	case "menubar", "cli", "board":
		return v
	}
	return "board"
}

// wrapBadRequest maps a plain error (RequestChanges' own "Add a comment…"
// validation isn't an *items.Error) to the wire's 400 bad_request.
func wrapBadRequest(err error) error {
	if err == nil {
		return nil
	}
	var ie *items.Error
	if errors.As(err, &ie) {
		return err
	}
	return apiErr(http.StatusBadRequest, "bad_request", err.Error())
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	if s.rtNotWired(w) {
		return
	}
	q := r.URL.Query()
	list, err := s.openRequestsWire(r.Context(), "", q.Get("kind"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) answerRequest(w http.ResponseWriter, r *http.Request) {
	var body answerBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	req, err := s.RT.Answer(ctx, id, body.Text, viaFromBody(body.Via))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	wire, err := s.RT.RequestWireByID(ctx, req.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire)
}

func (s *Server) approveRequest(w http.ResponseWriter, r *http.Request) {
	var body approveBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	var binding []byte
	if body.Binding != nil {
		binding, _ = json.Marshal(body.Binding)
	}
	req, err := s.RT.Approve(ctx, id, runtime.ApproveInput{SectionSHA256: body.SectionSHA256,
		ArtifactRevision: body.ArtifactRevision, Binding: binding, Via: viaFromBody(body.Via)})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	wire, err := s.RT.RequestWireByID(ctx, req.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire)
}

func (s *Server) requestChanges(w http.ResponseWriter, r *http.Request) {
	var body changesBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	req, err := s.RT.RequestChanges(ctx, id, body.Comment, viaFromBody(body.Via))
	if err != nil {
		s.writeErr(w, wrapBadRequest(err))
		return
	}
	wire, err := s.RT.RequestWireByID(ctx, req.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire)
}

func (s *Server) confirmRepos(w http.ResponseWriter, r *http.Request) {
	var body confirmReposBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	req, err := s.RT.ConfirmRepos(ctx, id, body.Repos, body.Comment, body.ReposVersion, viaFromBody(body.Via), "")
	if err != nil {
		s.writeErr(w, err)
		return
	}
	wire, err := s.RT.RequestWireByID(ctx, req.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire)
}

func (s *Server) closeSpike(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Via string `json:"via"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	req, err := s.RT.CloseSpike(ctx, id, viaFromBody(body.Via))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	wire, err := s.RT.RequestWireByID(ctx, req.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire)
}

// notWired is the guard every P2 route in this file shares: a plain P1
// harness (no P2 services wired) hits it instead of a nil-pointer panic.
func (s *Server) notWired(w http.ResponseWriter, ok bool) bool {
	if ok {
		return false
	}
	s.writeErr(w, apiErr(http.StatusInternalServerError, "internal", "The daemon isn't fully wired yet."))
	return true
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	if s.notWired(w, s.RT != nil) {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	revision := 0
	if rv := q.Get("revision"); rv != "" {
		if n, err := strconv.Atoi(rv); err == nil {
			revision = n
		}
	}
	art, md, err := s.RT.ArtifactMarkdown(ctx, r.PathValue("id"), revision, q.Get("section"))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	aw, err := s.artifactOut(ctx, art)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": aw, "markdown": md, "warnings": art.Warnings})
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	if s.notWired(w, s.Notify != nil) {
		return
	}
	q := r.URL.Query()
	limit := 0
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	list, err := s.Notify.List(r.Context(), q.Get("unread") == "1", limit)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := make([]notificationWire, 0, len(list))
	for _, n := range list {
		out = append(out, notificationOut(n))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) readAllNotifications(w http.ResponseWriter, r *http.Request) {
	n, err := s.Notify.ReadAll(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, readAllWire{Read: n})
}

func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	if err := s.Notify.MarkRead(r.Context(), r.PathValue("id")); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUsage(w http.ResponseWriter, r *http.Request) {
	if s.notWired(w, s.Usage != nil) {
		return
	}
	list, err := s.Usage.Snapshots(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := make([]usageWire, 0, len(list))
	for _, sn := range list {
		out = append(out, usageOut(sn))
	}
	writeJSON(w, http.StatusOK, out)
}

// wrapUsageErr maps *usage.Error (e.g. "limit_reached" for a too-soon manual
// refresh) to the §7 envelope.
func wrapUsageErr(err error) error {
	var ue *usage.Error
	if errors.As(err, &ue) {
		status, ok := statusFor[ue.Code]
		if !ok {
			status = http.StatusInternalServerError
		}
		return apiErr(status, ue.Code, ue.Message)
	}
	return err
}

func (s *Server) refreshUsage(w http.ResponseWriter, r *http.Request) {
	var body usageRefreshBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	// contracts §4's {agent?} is optional at the wire level, but the 60 s
	// refresh gate and the underlying fetch are both per-agent (§13) — there
	// is no "refresh everything" operation to fall back to, so an absent
	// agent is a real 400, not a 500 from an empty-string lookup miss.
	if body.Agent == "" {
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "Choose an agent to refresh."))
		return
	}
	if err := s.Usage.RefreshOne(r.Context(), runtime.AgentKind(body.Agent)); err != nil {
		s.writeErr(w, wrapUsageErr(err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) agentAdvice(w http.ResponseWriter, r *http.Request) {
	if s.notWired(w, s.Advisor != nil) {
		return
	}
	ctx, name := r.Context(), r.PathValue("name")
	list, err := s.Advisor.List(ctx, name)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := make([]adviceWire, 0, len(list))
	for _, a := range list {
		key, err := s.itemKeyByID(ctx, a.ItemID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		out = append(out, adviceOut(a, key))
	}
	writeJSON(w, http.StatusOK, out)
}

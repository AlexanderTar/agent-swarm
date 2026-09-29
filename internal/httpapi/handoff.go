// Batch 3 external surface: the handoff routes (§5). POST accepts a
// replacement intent and returns 202 plus the ReplacementResult; GET reports
// the in-flight operation or 404s when the coordinator owns nothing.
package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// replacementWire is the ReplacementResult: the durable operation the
// coordinator is driving for one agent. Error is set only on blocked.
type replacementWire struct {
	OperationID string `json:"operation_id"`
	Agent       string `json:"agent"`
	Mode        string `json:"mode"`
	Phase       string `json:"phase"`
	RequestKey  string `json:"request_key,omitempty"`
	// Reason is "capacity", "resume" or "retry" for an operation the agent
	// limit started (runtime.OperationReason); omitted otherwise.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

func replacementOut(name string, op runtime.Operation) replacementWire {
	return replacementWire{OperationID: op.ID, Agent: name, Mode: string(op.Mode),
		Phase: string(op.Phase), RequestKey: op.RequestKey,
		Reason: runtime.OperationReason(op.RequestKey), Error: op.Error}
}

// handoffBody: agent set → the successor runs as that agent/model/effort/
// advisor (orchestrators only; note is ignored on that path).
type handoffBody struct {
	RequestID string             `json:"request_id"`
	Note      string             `json:"note"`
	Agent     string             `json:"agent,omitempty"`
	Model     string             `json:"model,omitempty"`
	Effort    string             `json:"effort,omitempty"`
	Advisor   *advisorChoiceBody `json:"advisor,omitempty"` // object or "none"
}

func (s *Server) handoffAgent(w http.ResponseWriter, r *http.Request) {
	if s.RT == nil {
		s.writeErr(w, apiErr(http.StatusInternalServerError, "internal", "The daemon isn't fully wired yet."))
		return
	}
	var body handoffBody
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	name := r.PathValue("name")
	a, err := s.RT.Agent(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeErr(w, apiErr(http.StatusNotFound, "not_found", "No agent named "+name+"."))
		return
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	var op runtime.Operation
	switch {
	case body.Agent == "" && (body.Model != "" || body.Effort != "" || body.Advisor != nil):
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "Choose an agent."))
		return
	case body.Agent != "":
		op, err = s.RT.RequestHandoffTo(r.Context(), a.ID, body.RequestID, runtime.AgentSwitch{
			Kind: runtime.AgentKind(body.Agent), Model: body.Model, Effort: body.Effort,
			Advisor: advisorChoiceFromBody(body.Advisor)})
		err = wrapPreflightErr(err)
	default:
		op, err = s.RT.RequestReplacement(r.Context(), a.ID, runtime.ModeHandoff, body.RequestID, body.Note)
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	// 202: the intent is accepted and durably recorded; the successor walk
	// runs asynchronously, so the phase is reported, never "completed".
	writeJSON(w, http.StatusAccepted, replacementOut(a.Name, op))
}

func (s *Server) replacementAgent(w http.ResponseWriter, r *http.Request) {
	if s.RT == nil {
		s.writeErr(w, apiErr(http.StatusInternalServerError, "internal", "The daemon isn't fully wired yet."))
		return
	}
	name := r.PathValue("name")
	a, err := s.RT.Agent(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeErr(w, apiErr(http.StatusNotFound, "not_found", "No agent named "+name+"."))
		return
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	op, ok, err := s.RT.PendingOperation(r.Context(), a.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if !ok {
		s.writeErr(w, apiErr(http.StatusNotFound, "not_found",
			"No replacement in progress for "+name+"."))
		return
	}
	writeJSON(w, http.StatusOK, replacementOut(a.Name, op))
}

// replacementFor decorates one state node with its in-flight operation, if
// any. Absent means absent (omitted), never null-shaped: older clients keep
// decoding without it.
func (s *Server) replacementFor(ctx context.Context, agentID string) *replacementWire {
	if s.RT == nil {
		return nil
	}
	op, ok, err := s.RT.PendingOperation(ctx, agentID)
	if err != nil || !ok {
		return nil
	}
	var name string
	if a, err := s.RT.AgentByID(ctx, agentID); err == nil {
		name = a.Name
	}
	out := replacementOut(name, op)
	return &out
}

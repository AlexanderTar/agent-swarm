package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// canonicalJSON re-marshals raw through an untyped decode so two JSON values
// that differ only in key order or number formatting compare equal. Empty
// input canonicalizes to "" rather than "null", so an absent binding on
// either side still compares as absent.
func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// AskInput is swarm_ask's input (§8.1).
type AskInput struct {
	Kind       string // "question" | "approval" | "confirm_repos"
	Prompt     string
	Options    []string
	ArtifactID string
	SectionID  string
	Withdraw   string // when set, every other field is ignored
	Repos      []ReposProposal
	Expansion  []ReposProposal
}

// ReposProposal is one repository the orchestrator proposes (or drops) on a
// confirm_repos ask (D42).
type ReposProposal struct {
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
	Source string `json:"source,omitempty"` // "" (keep) or "dropped"
}

// ApproveInput is swarm_approve's input, from the UI or CLI (L7).
type ApproveInput struct {
	SectionSHA256    string
	ArtifactRevision int
	Binding          json.RawMessage // compared for accept_epic/accept_fix only
	Via              string
}

// RequestWire is the contracts §3.3 Request. It is the payload of request.opened
// and request.resolved (R5) and the body of every /api/requests route.
type RequestWire struct {
	ID               string          `json:"id"`
	Kind             RequestKind     `json:"kind"`
	AgentName        *string         `json:"agent_name"`
	ItemKey          string          `json:"item_key"`
	ItemTitle        string          `json:"item_title"`
	RootKey          string          `json:"root_key"`
	ArtifactID       *string         `json:"artifact_id"`
	ArtifactRevision *int            `json:"artifact_revision"`
	SectionID        *string         `json:"section_id"`
	SectionTitle     *string         `json:"section_title"`
	SectionSHA256    *string         `json:"section_sha256"`
	Prompt           string          `json:"prompt"`
	Options          json.RawMessage `json:"options"`
	State            RequestState    `json:"state"`
	Confirmed        []string        `json:"confirmed"`
	Binding          json.RawMessage `json:"binding"`
	ResponseText     *string         `json:"response_text"`
	RespondedVia     *string         `json:"responded_via"`
	RespondedAt      *int64          `json:"responded_at"`
	CreatedAt        int64           `json:"created_at"`
}

// txQuerier is the read surface both *sql.DB and *sql.Tx share.
type txQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// notify raises n when a Notifier is wired; a nil Notify is a silent no-op,
// matching the pattern the rest of the package already uses.
func (s *Store) notify(ctx context.Context, tx *sql.Tx, n NotifyInput) error {
	if s.Notify == nil {
		return nil
	}
	return s.Notify.Raise(ctx, tx, n)
}

func (s *Store) requestTx(ctx context.Context, q txQuerier, id string) (Request, error) {
	var r Request
	var kind, state string
	var options, confirmed, binding string
	var artifactRevision sql.NullInt64
	var respondedAt sql.NullInt64
	var created int64
	err := q.QueryRowContext(ctx, `SELECT id, kind, COALESCE(agent_id,''), COALESCE(session_id,''), item_id,
		COALESCE(artifact_id,''), COALESCE(section_id,''), COALESCE(section_sha256,''), prompt, options_json,
		state, COALESCE(confirmed_json,'[]'), artifact_revision, COALESCE(binding_json,''),
		COALESCE(response_text,''), COALESCE(responded_via,''), responded_at, created_at
		FROM requests WHERE id = ?`, id).Scan(&r.ID, &kind, &r.AgentID, &r.SessionID, &r.ItemID, &r.ArtifactID,
		&r.SectionID, &r.SectionSHA256, &r.Prompt, &options, &state, &confirmed, &artifactRevision, &binding,
		&r.ResponseText, &r.RespondedVia, &respondedAt, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, &items.Error{Code: items.CodeNotFound, Message: fmt.Sprintf("No request %s.", id)}
	}
	if err != nil {
		return r, err
	}
	r.Kind, r.State = RequestKind(kind), RequestState(state)
	r.Options = json.RawMessage(options)
	json.Unmarshal([]byte(confirmed), &r.Confirmed)
	if artifactRevision.Valid {
		r.ArtifactRevision = int(artifactRevision.Int64)
	}
	if binding != "" {
		r.Binding = json.RawMessage(binding)
	}
	if respondedAt.Valid {
		t := db.FromMillis(respondedAt.Int64)
		r.RespondedAt = &t
	}
	r.CreatedAt = db.FromMillis(created)
	return r, nil
}

// RequestByID reads one request outside any write transaction.
func (s *Store) RequestByID(ctx context.Context, id string) (Request, error) {
	return s.requestTx(ctx, s.DB, id)
}

// Requests lists open requests, optionally scoped to one item.
func (s *Store) Requests(ctx context.Context, itemKey string) ([]Request, error) {
	query, args := `SELECT id FROM requests WHERE state = 'open'`, []any{}
	if itemKey != "" {
		it, err := s.Items.Get(ctx, itemKey)
		if err != nil {
			return nil, err
		}
		query += ` AND item_id = ?`
		args = append(args, it.ID)
	}
	rows, err := s.DB.QueryContext(ctx, query+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Request, 0, len(ids))
	for _, id := range ids {
		r, err := s.RequestByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) sectionTitle(ctx context.Context, tx *sql.Tx, artifactID string, revision int, sectionID string) (string, error) {
	if artifactID == "" || sectionID == "" || revision == 0 {
		return "", nil
	}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT sections_json FROM artifact_revisions
		WHERE artifact_id = ? AND revision = ?`, artifactID, revision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var secs []ArtifactSection
	json.Unmarshal([]byte(raw), &secs)
	for _, sec := range secs {
		if sec.ID == sectionID {
			return sec.Title, nil
		}
	}
	return "", nil
}

// RequestWireTx builds the full §3.3 Request for the SSE feed and for
// items.Store.RequestPayload (R5).
func (s *Store) RequestWireTx(ctx context.Context, tx *sql.Tx, id string) (RequestWire, error) {
	r, err := s.requestTx(ctx, tx, id)
	if err != nil {
		return RequestWire{}, err
	}
	key, err := s.itemKey(ctx, tx, r.ItemID)
	if err != nil {
		return RequestWire{}, err
	}
	it, err := s.Items.GetTx(ctx, tx, key)
	if err != nil {
		return RequestWire{}, err
	}
	confirmed := r.Confirmed
	if confirmed == nil {
		confirmed = []string{}
	}
	w := RequestWire{ID: r.ID, Kind: r.Kind, ItemKey: key, ItemTitle: it.Title, RootKey: it.RootKey,
		Prompt: r.Prompt, Options: r.Options, State: r.State, Confirmed: confirmed, Binding: r.Binding,
		CreatedAt: db.Millis(r.CreatedAt)}
	if r.AgentID != "" {
		if a, err := s.agentByIDTx(ctx, tx, r.AgentID); err == nil {
			w.AgentName = &a.Name
		}
	}
	if r.ArtifactID != "" {
		id := r.ArtifactID
		w.ArtifactID = &id
	}
	if r.ArtifactRevision != 0 {
		rv := r.ArtifactRevision
		w.ArtifactRevision = &rv
	}
	if r.SectionID != "" {
		sid := r.SectionID
		w.SectionID = &sid
		if title, err := s.sectionTitle(ctx, tx, r.ArtifactID, r.ArtifactRevision, r.SectionID); err == nil && title != "" {
			w.SectionTitle = &title
		}
	}
	if r.SectionSHA256 != "" {
		sha := r.SectionSHA256
		w.SectionSHA256 = &sha
	}
	if r.ResponseText != "" {
		t := r.ResponseText
		w.ResponseText = &t
	}
	if r.RespondedVia != "" {
		v := r.RespondedVia
		w.RespondedVia = &v
	}
	if r.RespondedAt != nil {
		ms := db.Millis(*r.RespondedAt)
		w.RespondedAt = &ms
	}
	return w, nil
}

// RequestPayload is wired into items.Store.RequestPayload so P1's reconciler
// publishes the same shape (R5).
func (s *Store) RequestPayload(ctx context.Context, tx *sql.Tx, id string) (any, error) {
	return s.RequestWireTx(ctx, tx, id)
}

// RequestWireByID is RequestWireTx outside any caller's transaction: httpapi's
// /api/requests/* routes (P2 T33) call it after Answer/Approve/RequestChanges/
// ConfirmRepos/CloseSpike return the domain Request, to serve the exact §3.3
// wire shape without a second copy of it in internal/httpapi.
func (s *Store) RequestWireByID(ctx context.Context, id string) (RequestWire, error) {
	var out RequestWire
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = s.RequestWireTx(ctx, tx, id)
		return err
	})
	return out, err
}

// OnRequestOpened is wired into items.Store.RequestOpened: the accept_* requests
// the reconciler opens still get their §17.5 notification.
func (s *Store) OnRequestOpened(ctx context.Context, tx *sql.Tx, id string) error {
	w, err := s.RequestWireTx(ctx, tx, id)
	if err != nil {
		return err
	}
	return s.notify(ctx, tx, NotifyInput{Kind: "request." + string(w.Kind), ItemKey: w.ItemKey,
		RequestID: w.ID, Args: map[string]string{"KEY": w.ItemKey}})
}

// finishOpen is the shared tail of every ask* helper: publish request.opened
// with the full wire form, raise the §17.5 notification, and reconcile the item
// (a spike may move to awaiting_approval).
func (s *Store) finishOpen(ctx context.Context, tx *sql.Tx, reqID, agentName, itemKey string, extra map[string]string) (Request, error) {
	w, err := s.RequestWireTx(ctx, tx, reqID)
	if err != nil {
		return Request{}, err
	}
	if _, err := s.Events.Append(ctx, tx, events.RequestOpened, w); err != nil {
		return Request{}, err
	}
	// "name" is here unconditionally, like "KEY": some request kinds' templates
	// use it (confirm_repos, close_spike) and some don't (question,
	// approve_section/plan/report) — an unused key in Args is harmless, but a
	// used one with no value fails notify.Render closed (found while wiring
	// the e2e harness, same class of bug as checkpoint.go's, batch report has
	// the full account).
	args := map[string]string{"KEY": itemKey, "name": agentName}
	for k, v := range extra {
		args[k] = v
	}
	if err := s.notify(ctx, tx, NotifyInput{Kind: "request." + string(w.Kind), AgentName: agentName,
		ItemKey: itemKey, RequestID: reqID, Args: args}); err != nil {
		return Request{}, err
	}
	if err := s.Items.ReconcileTx(ctx, tx, itemKey); err != nil {
		return Request{}, err
	}
	return s.requestTx(ctx, tx, reqID)
}

// Ask is swarm_ask (§8.1).
func (s *Store) Ask(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if in.Withdraw != "" {
		return s.withdraw(ctx, sessionID, in.Withdraw)
	}
	if st, err := s.SessionState(ctx, sessionID); err == nil && st.Pausing() {
		return Request{}, errors.New(pausedTool)
	}
	switch in.Kind {
	case "question":
		return s.askQuestion(ctx, sessionID, in)
	case "approval":
		return s.askApproval(ctx, sessionID, in)
	case "confirm_repos":
		return s.askConfirmRepos(ctx, sessionID, in)
	default:
		return Request{}, &items.Error{Code: items.CodeBadRequest,
			Message: "kind must be question, approval or confirm_repos."}
	}
}

func (s *Store) withdraw(ctx context.Context, sessionID, reqID string) (Request, error) {
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		req, err := s.requestTx(ctx, tx, reqID)
		if err != nil {
			return err
		}
		if req.AgentID != a.ID {
			return &items.Error{Code: items.CodeBadRequest,
				Message: "Only the agent that asked can withdraw this request."}
		}
		if req.State != "open" {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'withdrawn', responded_at = ?
			WHERE id = ?`, db.Millis(s.Now()), reqID); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return err
		}
		w, err := s.RequestWireTx(ctx, tx, reqID)
		if err != nil {
			return err
		}
		if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
			return err
		}
		if err := s.Items.ReconcileTx(ctx, tx, key); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, reqID)
		return err
	})
	return out, err
}

func (s *Store) askQuestion(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if n := utf8.RuneCountInString(in.Prompt); n < 1 || n > 1000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Prompt must be 1–1000 characters."}
	}
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		id := ids.New("req")
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, session_id, item_id,
			prompt, options_json, state, created_at)
			VALUES (?, 'question', ?, ?, ?, ?, ?, 'open', ?)`,
			id, a.ID, sessionID, a.ItemID, in.Prompt, jsonArray(in.Options), db.Millis(s.Now())); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		out, err = s.finishOpen(ctx, tx, id, a.Name, key, map[string]string{"prompt": in.Prompt})
		return err
	})
	return out, err
}

func (s *Store) askApproval(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if in.ArtifactID == "" {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "artifact_id is required."}
	}
	if n := utf8.RuneCountInString(in.Prompt); n < 1 || n > 1000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Prompt must be 1–1000 characters."}
	}
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		var artKind, itemID string
		var headRev int
		err = tx.QueryRowContext(ctx, `SELECT kind, head_revision, item_id FROM artifacts WHERE id = ?`,
			in.ArtifactID).Scan(&artKind, &headRev, &itemID)
		if errors.Is(err, sql.ErrNoRows) {
			return &items.Error{Code: items.CodeNotFound, Message: "Unknown artifact."}
		}
		if err != nil {
			return err
		}
		var reqKind string
		switch artKind {
		case "spec":
			reqKind = "approve_section"
			if in.SectionID == "" {
				return &items.Error{Code: items.CodeBadRequest, Message: "section_id is required for a spec."}
			}
		case "plan":
			reqKind = "approve_plan"
		case "debug_report":
			reqKind = "approve_report"
		default:
			return &items.Error{Code: items.CodeBadRequest, Message: "This artifact kind cannot be approved."}
		}
		var sectionID sql.NullString
		var sectionSHA, sectionTitle string
		if in.SectionID != "" {
			var raw string
			if err := tx.QueryRowContext(ctx, `SELECT sections_json FROM artifact_revisions
				WHERE artifact_id = ? AND revision = ?`, in.ArtifactID, headRev).Scan(&raw); err != nil {
				return err
			}
			var secs []ArtifactSection
			json.Unmarshal([]byte(raw), &secs)
			found := false
			for _, sec := range secs {
				if sec.ID == in.SectionID {
					sectionSHA, sectionTitle, found = sec.SHA256, sec.Title, true
					break
				}
			}
			if !found {
				return &items.Error{Code: items.CodeBadRequest, Message: "Unknown section."}
			}
			sectionID = sql.NullString{String: in.SectionID, Valid: true}
		}
		id := ids.New("req")
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, session_id, item_id,
			artifact_id, section_id, section_sha256, prompt, state, artifact_revision, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
			id, reqKind, a.ID, sessionID, itemID, in.ArtifactID, sectionID, nullIf(sectionSHA), in.Prompt,
			headRev, db.Millis(s.Now())); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, itemID)
		if err != nil {
			return err
		}
		// request.approve_section is the one finishOpen-routed template that
		// needs more than {KEY}/{name}: `{KEY}: Review "{section}".` (§17.5).
		// approve_plan/approve_report need neither section nor an extra arg.
		var extra map[string]string
		if reqKind == "approve_section" {
			extra = map[string]string{"section": sectionTitle}
		}
		out, err = s.finishOpen(ctx, tx, id, a.Name, key, extra)
		return err
	})
	return out, err
}

// resolve is the shared body of the five user-action methods. origin is a
// parameter, not a literal in here: L7's guard test fails any function outside
// {Answer, Approve, RequestChanges, ConfirmRepos, CloseSpike} that contains the
// string "user_action", and resolve is not one of them. That is the point — a
// future handler that reuses resolve cannot smuggle a user action in by
// reaching a shared helper that hard-codes the origin. Each of the five passes
// "user_action" at its own call site, where the guard can see it.
func (s *Store) resolve(ctx context.Context, id, state, responseText, via, origin string,
	check func(Request) error, build func(Request) (MessageKind, any)) (Request, error) {
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		req, err := s.requestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if req.State != "open" {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		if check != nil {
			if err := check(req); err != nil {
				return err
			}
		}
		kind, payloadValue := build(req)
		payload, err := json.Marshal(payloadValue)
		if err != nil {
			return err
		}
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = ?, response_text = ?,
			responded_via = ?, responded_at = ? WHERE id = ?`,
			state, nullIf(responseText), nullIf(via), db.Millis(now), id); err != nil {
			return err
		}
		// accept_epic/accept_fix requests are opened by the daemon's reconciler
		// with no asking agent (items/transition.go's reconcileRoot never sets
		// agent_id): there is nobody to send a result message to, so this is
		// skipped rather than trying to enqueue to an empty to_agent_id.
		if req.AgentID != "" {
			rootID, err := s.rootItemID(ctx, tx, req.ItemID)
			if err != nil {
				return err
			}
			if _, err := s.enqueue(ctx, tx, Message{Kind: kind, Origin: origin, ToAgentID: req.AgentID,
				RootItemID: rootID, ItemID: req.ItemID, RequestID: id, Payload: payload}); err != nil {
				return err
			}
		}
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return err
		}
		w, err := s.RequestWireTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
			return err
		}
		if err := s.Items.ReconcileTx(ctx, tx, key); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, id)
		return err
	})
	return out, err
}

func (s *Store) rootItemID(ctx context.Context, tx *sql.Tx, itemID string) (string, error) {
	var root string
	err := tx.QueryRowContext(ctx, `SELECT root_id FROM items WHERE id = ?`, itemID).Scan(&root)
	return root, err
}

// Answer is swarm_answer's UI/CLI counterpart (L7): it writes a user_answer
// message and closes the request.
func (s *Store) Answer(ctx context.Context, id, text, via string) (Request, error) {
	if utf8.RuneCountInString(text) < 1 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Enter an answer."}
	}
	return s.resolve(ctx, id, "answered", text, via, "user_action", nil,
		func(req Request) (MessageKind, any) {
			return "user_answer", map[string]string{"request_id": req.ID, "text": text}
		})
}

// Approve binds to the artifact's section hash and revision (L7): a stale
// caller conflicts instead of silently approving a since-changed section.
func (s *Store) Approve(ctx context.Context, id string, in ApproveInput) (Request, error) {
	check := func(req Request) error {
		if req.SectionSHA256 != "" && in.SectionSHA256 != req.SectionSHA256 {
			return &items.Error{Code: items.CodeConflict, Message: "This request changed. Review the latest version."}
		}
		if in.ArtifactRevision != 0 && req.ArtifactRevision != 0 && in.ArtifactRevision != req.ArtifactRevision {
			return &items.Error{Code: items.CodeConflict, Message: "This request changed. Review the latest version."}
		}
		// Unconditional, unlike ArtifactRevision above: the brief has no "when
		// supplied" qualifier here, so a caller that omits Binding while the
		// request has one must be refused, not waved through — the acceptance
		// may be bound to an integrated_checkpoint or item_revision that has
		// since moved on.
		//
		// canonicalJSON, not a raw string compare, on both sides: req.Binding
		// is the bytes originally produced by json.Marshal(acceptBinding{...})
		// (internal/items/transition.go), which — being a struct — marshals
		// its fields in declaration order (item_revision, integrated_checkpoint,
		// git). in.Binding is httpapi's approveBody.Binding, typed `any`; any
		// JSON client's object decodes through Go's stdlib into a
		// map[string]interface{} before this call ever sees it, and
		// json.Marshal of a Go map always sorts keys alphabetically
		// (git, integrated_checkpoint, item_revision) — a different byte
		// order than the struct produced, for every possible caller,
		// regardless of what order the client sent. A literal string compare
		// between the two therefore could never succeed for a "matching"
		// binding at all; found while wiring scenario 29 (Batch 6b/P2), the
		// first real exercise of this check.
		if (req.Kind == "accept_epic" || req.Kind == "accept_fix") &&
			canonicalJSON(in.Binding) != canonicalJSON(req.Binding) {
			return &items.Error{Code: items.CodeConflict, Message: "This request changed. Review the latest version."}
		}
		return nil
	}
	return s.resolve(ctx, id, "approved", "", in.Via, "user_action", check,
		func(req Request) (MessageKind, any) {
			return "approval_result", map[string]any{"decision": "approved",
				"section_id": req.SectionID, "section_sha256": req.SectionSHA256}
		})
}

// RequestChanges needs a comment describing what to change.
func (s *Store) RequestChanges(ctx context.Context, id, comment, via string) (Request, error) {
	if comment == "" {
		return Request{}, errors.New("Add a comment describing what to change.")
	}
	if utf8.RuneCountInString(comment) > 2000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Comment must be at most 2000 characters."}
	}
	return s.resolve(ctx, id, "changes_requested", comment, via, "user_action", nil,
		func(req Request) (MessageKind, any) {
			return "approval_result", map[string]any{"decision": "changes_requested",
				"comment": comment, "section_id": req.SectionID}
		})
}

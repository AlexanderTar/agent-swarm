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
		// ponytail: unreachable today — req.Binding is always this package's
		// own json.Marshal output, and in.Binding has already round-tripped
		// through httpapi's readJSON before Approve ever sees it — so this
		// falls back to a raw-byte compare only if that ever stops being
		// true. It fails safe (a garbled binding just won't match, 409, not
		// a false-positive approval), but if a caller of canonicalJSON that
		// *can* see truly malformed input ever appears, give this a real
		// error return instead of a silent best-effort compare.
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// AskInput is swarm_ask's input (§8.1).
type AskInput struct {
	Kind       string // "question" | "approval" | "confirm_repos" | "native_prompt" | "native_answer"
	Prompt     string
	Options    []string
	ArtifactID string
	SectionID  string
	Withdraw   string // when set, every other field is ignored
	Repos      []ReposProposal
	Expansion  []ReposProposal
	// RequestID is I11's idempotency key, scoped to the calling MCP session
	// (empty means "no idempotency, just run once").
	RequestID string
	// ForMsg is kind:"native_prompt"'s target: an approval question message
	// addressed to the caller (spec 2.3 step 1, Task 13b).
	ForMsg string
	// Ref, Decision and Comment are kind:"native_answer"'s fields (Task 13c):
	// Ref names the request or message the caller is forwarding a decision
	// for, Decision is "approve" | "request_changes", Comment is optional
	// free text.
	Ref, Decision, Comment string
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
	IsHITL           bool            `json:"is_hitl"`
	AgentName        *string         `json:"agent_name"`
	TerminalAgent    *string         `json:"terminal_agent"`
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
	// ApprovalEvidence is null for a board/CLI/menubar approval (a user
	// action by definition), and "observed" | "agent_reported" once
	// native_answer records one (spec section 2.3.6, Task 13c).
	ApprovalEvidence *string `json:"approval_evidence"`
	// NativePending is true while an approval request's bound native-question
	// row is still open: Needs you hides the approval row in that window,
	// because the open question row already represents it (spec 2.2.1, Task 13e).
	NativePending bool `json:"native_pending"`
}

// EvidenceObserved/EvidenceAgentReported are native_answer's two evidence
// kinds (spec section 2.3.5): the bound question row's response text either
// started with the chosen label ("observed"), or the adapter never surfaces
// answer text and the forwarded decision is accepted on the agent's word
// ("agent_reported").
const (
	EvidenceObserved      = "observed"
	EvidenceAgentReported = "agent_reported"
)

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
	var isHITL int
	var options, confirmed, binding string
	var artifactRevision sql.NullInt64
	var respondedAt sql.NullInt64
	var created int64
	err := q.QueryRowContext(ctx, `SELECT id, kind, is_hitl, COALESCE(agent_id,''), COALESCE(session_id,''), item_id,
		COALESCE(artifact_id,''), COALESCE(section_id,''), COALESCE(section_sha256,''), prompt, options_json,
		state, COALESCE(confirmed_json,'[]'), artifact_revision, COALESCE(binding_json,''),
		COALESCE(response_text,''), COALESCE(responded_via,''), responded_at, created_at
		FROM requests WHERE id = ?`, id).Scan(&r.ID, &kind, &isHITL, &r.AgentID, &r.SessionID, &r.ItemID, &r.ArtifactID,
		&r.SectionID, &r.SectionSHA256, &r.Prompt, &options, &state, &confirmed, &artifactRevision, &binding,
		&r.ResponseText, &r.RespondedVia, &respondedAt, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, &items.Error{Code: items.CodeNotFound, Message: fmt.Sprintf("No request %s.", id)}
	}
	if err != nil {
		return r, err
	}
	r.Kind, r.State = RequestKind(kind), RequestState(state)
	r.IsHITL = (isHITL != 0)
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

// terminalAgent is the tmux session the user answers a HITL row in: the asking
// agent for a permission dialog (the dialog lives in its pane), the root
// orchestrator of its tree for a question or blocker. nil for non-HITL kinds
// and for a row with no agent.
// approvalTerminalKinds is every approval kind that has an asking agent, so
// its terminal is that tree's root orchestrator, same as a question or
// blocker (spec 2.1's 21-D5 amendment, Task 13e). accept_epic/accept_fix are
// deliberately absent: they have no asking agent at all (opened by the
// daemon's reconciler), so they get their own item-rooted lookup below.
var approvalTerminalKinds = map[RequestKind]bool{
	KindApproveSection: true, KindApprovePlan: true, KindApproveReport: true,
	KindConfirmRepos: true, KindCloseSpike: true,
}

func (s *Store) terminalAgent(ctx context.Context, tx *sql.Tx, r Request) *string {
	if r.Kind == KindAcceptEpic || r.Kind == KindAcceptFix {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT a.name FROM agents a JOIN items i ON i.id = ?
			WHERE a.root_item_id = i.root_id AND a.role = 'orchestrator' AND a.parent_agent_id IS NULL
			  AND a.state IN ('queued','active') LIMIT 1`, r.ItemID).Scan(&name)
		if err != nil {
			return nil
		}
		return &name
	}
	if (!r.IsHITL && !approvalTerminalKinds[r.Kind]) || r.AgentID == "" {
		return nil
	}
	q := `WITH RECURSIVE up(name, parent) AS (
		SELECT name, parent_agent_id FROM agents WHERE id = ?
		UNION ALL
		SELECT a.name, a.parent_agent_id FROM agents a JOIN up ON a.id = up.parent)
		SELECT name FROM up WHERE parent IS NULL LIMIT 1`
	if r.Kind == "prompt" {
		q = `SELECT name FROM agents WHERE id = ?`
	}
	var name string
	if err := tx.QueryRowContext(ctx, q, r.AgentID).Scan(&name); err != nil {
		return nil
	}
	return &name
}

// approvalEvidenceTx is the wire's approval_evidence (spec section 3, Task
// 13c/13e): null for a board/CLI/menubar approval. For a request-kind
// approval it is read off the bound native-question row whose binding_json
// ref names this request id. For a message-ref approval (a bound question
// row itself, Task 13d), it is that row's own binding_json.evidence.
func (s *Store) approvalEvidenceTx(ctx context.Context, tx *sql.Tx, r Request) *string {
	if r.Kind == KindQuestion {
		var b struct {
			Evidence *string `json:"evidence"`
		}
		if len(r.Binding) > 0 {
			json.Unmarshal(r.Binding, &b)
		}
		return b.Evidence
	}
	var ev sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT json_extract(q.binding_json, '$.evidence') FROM requests q
		WHERE q.kind = 'question' AND json_extract(q.binding_json, '$.ref') = ?
		  AND json_extract(q.binding_json, '$.evidence') IS NOT NULL LIMIT 1`, r.ID).Scan(&ev)
	if err != nil || !ev.Valid {
		return nil
	}
	return &ev.String
}

// nativePendingTx is the wire's native_pending (spec section 3, Task 13e):
// true while some open native-question row is bound to this request as its
// ref -- the daemon issued this approval's native prompt and it is still
// waiting on the user's answer in the terminal.
func (s *Store) nativePendingTx(ctx context.Context, tx *sql.Tx, r Request) bool {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM requests q
		WHERE q.kind = 'question' AND q.state = 'open' AND json_extract(q.binding_json, '$.ref') = ?)`,
		r.ID).Scan(&pending)
	if err != nil {
		return false
	}
	return pending
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
	w := RequestWire{ID: r.ID, Kind: r.Kind, IsHITL: r.IsHITL, ItemKey: key, ItemTitle: it.Title, RootKey: it.RootKey,
		Prompt: r.Prompt, Options: r.Options, State: r.State, Confirmed: confirmed, Binding: r.Binding,
		CreatedAt: db.Millis(r.CreatedAt)}
	if r.AgentID != "" {
		if a, err := s.agentByIDTx(ctx, tx, r.AgentID); err == nil {
			w.AgentName = &a.Name
		}
	}
	w.TerminalAgent = s.terminalAgent(ctx, tx, r)
	w.ApprovalEvidence = s.approvalEvidenceTx(ctx, tx, r)
	w.NativePending = s.nativePendingTx(ctx, tx, r)
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
// the reconciler opens still get their §17.5 notification. In production
// w.Kind is always accept_epic/accept_fix (reconcileRoot, internal/items/
// transition.go, is the only caller of RequestOpened, and both those
// templates need only KEY) — Args also carries name/prompt defensively,
// unconditionally like finishOpen just below does for the same reason: an
// unused key is harmless. That covers accept_epic/accept_fix robustly
// against either template changing to want name or prompt too; it does NOT
// make this function correct for every kind in general — approve_section
// needs {section}, confirm_repos needs {N}/{expansion}, close_spike needs
// {resolution}, and none of those are built here. If RequestOpened is ever
// wired to open one of those kinds, this needs its own Args for it.
func (s *Store) OnRequestOpened(ctx context.Context, tx *sql.Tx, id string) error {
	w, err := s.RequestWireTx(ctx, tx, id)
	if err != nil {
		return err
	}
	args := map[string]string{"KEY": w.ItemKey, "prompt": w.Prompt}
	if w.AgentName != nil {
		args["name"] = *w.AgentName
	}
	return s.notify(ctx, tx, NotifyInput{Kind: "request." + string(w.Kind), ItemKey: w.ItemKey,
		RequestID: w.ID, Args: args})
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

// errQuestionUseNativeTool is swarm_ask's refusal for kind:"question" when
// the caller's kind has a hooked native question tool (spec section 1.7):
// the hook opens the Needs-you row itself, so swarm_ask would only ever
// duplicate it. muse and codex are deliberately absent from the copy below,
// unlike the spec's section 4.1 code block: Task 4's live probe
// (2026-09-25/26, docs/plans/2026-09-25-needs-you-and-child-approval-
// routing.md) found muse's request_user_input never dispatches a hook at
// all, and Task 4b's live check for codex's request_user_input never
// completed (every codex model call returned a backend 401), so both join
// cursor's exception instead (spec section 1.7's final verdict, which
// supersedes 4.1's pre-probe draft) until a live retry produces codex's
// T4b fixtures.
const errQuestionUseNativeTool = "Ask the user with your own native question tool " +
	"(claude AskUserQuestion, agy ask_question). " +
	"Swarm shows it in Needs you and closes it when the user answers."

// questionHookKinds are the kinds whose native question tool Swarm
// intercepts via a hook (spec section 1.7). cursor, muse and codex are
// absent on purpose: cursor and muse never dispatch a hook for their
// native question tool at all, and codex's hook is unconfirmed (Task 4b's
// live check never ran), so all three keep swarm_ask kind:"question" as
// their only path to Needs you.
var questionHookKinds = map[AgentKind]bool{Claude: true, Agy: true}

// Ask is swarm_ask (§8.1).
func (s *Store) Ask(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if in.Withdraw != "" {
		return s.withdraw(ctx, sessionID, in.Withdraw, in.RequestID)
	}
	if st, err := s.SessionState(ctx, sessionID); err == nil && st.Pausing() {
		return Request{}, errors.New(pausedTool)
	}
	switch in.Kind {
	case "question":
		refused := false
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
			if err != nil {
				return err
			}
			if err := requireTopLevel(a); err != nil {
				return err
			}
			refused = questionHookKinds[a.Kind]
			return nil
		}); err != nil {
			return Request{}, err
		}
		if refused {
			return Request{}, &items.Error{Code: items.CodeBadRequest, Message: errQuestionUseNativeTool}
		}
		return s.askQuestion(ctx, sessionID, in)
	case "approval":
		return s.askApproval(ctx, sessionID, in)
	case "confirm_repos":
		return s.askConfirmRepos(ctx, sessionID, in)
	case "native_prompt":
		return s.askNativePromptForMsg(ctx, sessionID, in)
	case "native_answer":
		return s.nativeAnswer(ctx, sessionID, in)
	default:
		return Request{}, &items.Error{Code: items.CodeBadRequest,
			Message: "kind must be question, approval, confirm_repos, native_prompt or native_answer."}
	}
}

// closeRequestTx withdraws one open request inside the caller's tx: UPDATE,
// request.resolved event, item reconcile. withdraw() and the orphan sweep share it.
func (s *Store) closeRequestTx(ctx context.Context, tx *sql.Tx, reqID string) error {
	req, err := s.requestTx(ctx, tx, reqID)
	if err != nil {
		return err
	}
	if req.State != "open" {
		return nil // resolved between the caller's query and this tx: nothing to close, no event
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'withdrawn', responded_at = ?
		WHERE id = ? AND state = 'open'`, db.Millis(s.Now()), reqID); err != nil {
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
	return s.Items.ReconcileTx(ctx, tx, key)
}

func (s *Store) withdraw(ctx context.Context, sessionID, reqID, requestID string) (Request, error) {
	var out Request
	_, err := IdemTx(ctx, s, sessionID, requestID, "swarm_ask", &out, func(tx *sql.Tx) error {
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
		if err := s.closeRequestTx(ctx, tx, reqID); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, reqID)
		return err
	})
	return out, err
}

// errRelayToParent is the refusal a parented agent gets from swarm_ask
// (kind question) and swarm_blocker. "parent" is swarm_send's alias for the
// caller's parent (skills/swarm/SKILL.md rule 5), so no parent lookup is needed.
const errRelayToParent = "You report to an orchestrator, not the user. Send this to it with swarm_send (to: \"parent\", kind: \"question\") and keep working on anything you are not blocked on."

// requireTopLevel returns the refusal for an agent that has a parent, nil otherwise.
func requireTopLevel(a Agent) error {
	if a.ParentAgentID == "" {
		return nil
	}
	return &items.Error{Code: items.CodeBadRequest, Message: errRelayToParent}
}

func (s *Store) askQuestion(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if n := utf8.RuneCountInString(in.Prompt); n < 1 || n > 1000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Prompt must be 1–1000 characters."}
	}
	var out Request
	_, err := IdemTx(ctx, s, sessionID, in.RequestID, "swarm_ask", &out, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if err := requireTopLevel(a); err != nil {
			return err
		}
		id := ids.New("req")
		// A prompt forwarded verbatim from a daemon-issued native_prompt
		// carries a ⟦swarm:<ref>⟧ token (spec 2.3 step 3, Task 13b): bind
		// the row to it so native_answer can later find its evidence. A
		// plain question's prompt has no token, so binding_json stays NULL.
		var binding any
		if ref := refFromPrompt(in.Prompt); ref != "" {
			b, err := json.Marshal(map[string]string{"ref": ref})
			if err != nil {
				return err
			}
			binding = string(b)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, binding_json, created_at)
			VALUES (?, 'question', 1, ?, ?, ?, ?, ?, 'open', ?, ?)`,
			id, a.ID, sessionID, a.ItemID, in.Prompt, jsonArray(in.Options), binding, db.Millis(s.Now())); err != nil {
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

// AskQuestion opens an open question request with is_hitl=1.
func (s *Store) AskQuestion(ctx context.Context, sessionID, prompt string, options []string) (Request, error) {
	return s.askQuestion(ctx, sessionID, AskInput{
		Kind:    "question",
		Prompt:  prompt,
		Options: options,
	})
}

// AskBlocker opens an open blocker request with is_hitl=1 and transitions the item to blocked.
func (s *Store) AskBlocker(ctx context.Context, sessionID, prompt string, options []string) (Request, error) {
	if n := utf8.RuneCountInString(prompt); n < 1 || n > 1000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Prompt must be 1–1000 characters."}
	}
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if err := requireTopLevel(a); err != nil {
			return err
		}
		id := ids.New("req")
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at)
			VALUES (?, 'blocker', 1, ?, ?, ?, ?, ?, 'open', ?)`,
			id, a.ID, sessionID, a.ItemID, prompt, jsonArray(options), db.Millis(s.Now())); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		_ = s.tryTransition(ctx, tx, key, items.Blocked)
		out, err = s.finishOpen(ctx, tx, id, a.Name, key, map[string]string{"prompt": prompt})
		return err
	})
	return out, err
}

// AskPrompt opens an open terminal prompt request with is_hitl=1.
func (s *Store) AskPrompt(ctx context.Context, sessionID, prompt string, options []string) (Request, error) {
	if n := utf8.RuneCountInString(prompt); n < 1 || n > 1000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Prompt must be 1–1000 characters."}
	}
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		id := ids.New("req")
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at)
			VALUES (?, 'prompt', 1, ?, ?, ?, ?, ?, 'open', ?)`,
			id, a.ID, sessionID, a.ItemID, prompt, jsonArray(options), db.Millis(s.Now())); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		out, err = s.finishOpen(ctx, tx, id, a.Name, key, map[string]string{"prompt": prompt})
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
	_, err := IdemTx(ctx, s, sessionID, in.RequestID, "swarm_ask", &out, func(tx *sql.Tx) error {
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
		if err != nil {
			return err
		}
		var warnings []string
		if reqKind == "approve_plan" {
			var warningsJSON string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(warnings_json,'[]') FROM artifact_revisions
				WHERE artifact_id = ? AND revision = ?`, in.ArtifactID, headRev).Scan(&warningsJSON); err != nil {
				return err
			}
			json.Unmarshal([]byte(warningsJSON), &warnings)
		}
		np, err := s.nativePromptFor(ctx, tx, out, sectionTitle, warnings)
		if err != nil {
			return err
		}
		out.NativePrompt = &np
		return nil
	})
	return out, err
}

// resolve is the shared body of the five user-action methods, plus
// nativeAnswer's request-ref path (Task 13c). origin is a parameter, not a
// literal in here: L7's guard test fails any function outside {Answer,
// Approve, RequestChanges, ConfirmRepos, CloseSpike, nativeAnswer} that
// contains the string "user_action", and resolve is not one of them. That is
// the point — a future handler that reuses resolve cannot smuggle a user
// action in by reaching a shared helper that hard-codes the origin. Each of
// the first five passes "user_action" at its own call site, where the guard
// can see it. nativeAnswer is the sixth, and the one agent-reachable
// user_action origin: it is guarded by native_answer's evidence check (a
// bound question row genuinely answered via terminal), and an
// agent-reported decision is accepted on the agent's word and flagged, not
// refused (user decision, spec 1.6.2) — never a bare MCP call claiming a
// user action for free.
// after runs inside resolve's tx, right after the state UPDATE and before
// RequestWireTx builds the resolved wire (Task 13c): nativeAnswer uses it to
// write the bound question row's evidence in the same transaction as the
// approval it forwards, so the request.resolved event already carries it.
func (s *Store) resolve(ctx context.Context, id, state, responseText, via, origin string,
	check func(Request) error, build func(Request) (MessageKind, any),
	after ...func(*sql.Tx, Request) error) (Request, error) {
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
		for _, fn := range after {
			if err := fn(tx, req); err != nil {
				return err
			}
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

// approveCheck is Approve's staleness guard, extracted (Task 13c) so
// nativeAnswer can reuse the exact same check without going through Approve
// itself when it wants an after hook: a stale caller conflicts instead of
// silently approving a since-changed section.
func approveCheck(in ApproveInput) func(Request) error {
	return func(req Request) error {
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
}

// Approve binds to the artifact's section hash and revision (L7): a stale
// caller conflicts instead of silently approving a since-changed section.
func (s *Store) Approve(ctx context.Context, id string, in ApproveInput, after ...func(*sql.Tx, Request) error) (Request, error) {
	return s.resolve(ctx, id, "approved", "", in.Via, "user_action", approveCheck(in),
		func(req Request) (MessageKind, any) {
			return "approval_result", map[string]any{"decision": "approved",
				"section_id": req.SectionID, "section_sha256": req.SectionSHA256}
		}, after...)
}

// RequestChanges needs a comment describing what to change.
func (s *Store) RequestChanges(ctx context.Context, id, comment, via string, after ...func(*sql.Tx, Request) error) (Request, error) {
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
		}, after...)
}

// ResolvePrompt marks an open prompt request answered. It only records the
// resolution: the permission dialog is answered in the terminal, so no keys are
// sent to the pane.
func (s *Store) ResolvePrompt(ctx context.Context, id, via string) (Request, error) {
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		req, err := s.requestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if req.State != "open" {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'answered', response_text = NULL,
			responded_via = ?, responded_at = ? WHERE id = ?`,
			nullIf(via), db.Millis(now), id); err != nil {
			return err
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

// ResolveQuestionByPrompt closes the session's open native-question row whose
// prompt equals the one this tool call asked, answered via terminal. No match
// is not an error (the tool may have been blocked, or the row swept); the
// returned Request is the zero value in that case. The resolved Request is
// returned (not just an error) so the hook can read its binding_json ref and
// tell the caller to forward the answer (2026-09-26 fix,
// native-railway-tracing finding).
func (s *Store) ResolveQuestionByPrompt(ctx context.Context, sessionID, prompt, answer string) (Request, error) {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests
		WHERE session_id = ? AND kind = 'question' AND state = 'open' AND prompt = ?
		ORDER BY created_at DESC LIMIT 1`, sessionID, prompt)
	if err != nil || len(ids) == 0 {
		return Request{}, err
	}
	return s.ResolveQuestion(ctx, ids[0], answer, "terminal")
}

// ResolveSessionPrompts resolves open prompt requests of one session once a
// PostToolUse proves the permission dialog was answered. Rows whose prompt
// equals command, or the fallback "Permission requested", match;
// command == "" resolves all of the session's open prompts.
// ponytail: an empty command resolves every open prompt of the session; only
// adapters that send no command on PostToolUse hit it. Add a per-tool match if
// a live run shows a wrong close.
func (s *Store) ResolveSessionPrompts(ctx context.Context, sessionID, command string) error {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests
		WHERE session_id = ? AND kind = 'prompt' AND state = 'open'
		  AND (? = '' OR prompt = ? OR prompt = 'Permission requested')`, sessionID, command, command)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.ResolvePrompt(ctx, id, "terminal"); err != nil {
			s.logf("resolve prompt %s: %v", id, err)
		}
	}
	return nil
}

// ResolveAnsweredInTerminal closes every open question/blocker row of an agent
// after a human-typed prompt: answered, via terminal. Keyed by agent, not
// session: after a pause and resume the row belongs to the old session.
func (s *Store) ResolveAnsweredInTerminal(ctx context.Context, agentID string) error {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests
		WHERE agent_id = ? AND is_hitl = 1 AND kind IN ('question', 'blocker') AND state = 'open'`, agentID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.ResolveQuestion(ctx, id, "Answered in terminal", "terminal"); err != nil {
			s.logf("resolve %s after human prompt: %v", id, err)
		}
	}
	return nil
}

// ResolveQuestion resolves an open question request with response text and via attribution.
func (s *Store) ResolveQuestion(ctx context.Context, id, answer, via string) (Request, error) {
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		req, err := s.requestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if req.State != "open" {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'answered', response_text = ?,
			responded_via = ?, responded_at = ? WHERE id = ?`,
			nullIf(answer), nullIf(via), db.Millis(now), id); err != nil {
			return err
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

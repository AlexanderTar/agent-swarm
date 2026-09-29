package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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
	// NothingToReview is a spec-section approval's optional one-line reason
	// there is nothing for the user to review (docs/specs/2026-09-28-empty-
	// section-auto-approve.md locked decision 1): 3-200 runes. Refused on
	// any approval kind other than approve_section, when the section
	// heading exceeds 80 runes, and when the section's own body (heading
	// stripped, trimmed) is not a single short line of at most 120 runes
	// with no table row, list item or code fence (nothingToReviewBodyOK,
	// tightened post-review from the originally approved 300-rune bound).
	NothingToReview string
	Withdraw        string // when set, every other field is ignored
	Repos           []ReposProposal
	Expansion       []ReposProposal
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
	AnswerText             string // exact native tool return, Cursor and Muse only
	// Header and PresetRef are internal-only (never exposed by the MCP
	// swarm_ask schema): the hook's PreToolUse native-question intercept
	// sets them. Header is the observed native tool call's own header field
	// (Claude/Codex/agy/Muse questions can carry one), used only to
	// disambiguate a child-approval (msg_) match when two children's
	// bodies collide (2026-09-28-approval-summary-enforced, post-review).
	// PresetRef, when non-empty, is a ref the PreToolUse summary gate
	// already resolved via BindNativeQuestion for this same call: askQuestion
	// binds to it directly instead of re-deriving it, so one hook
	// invocation runs bindNativeQuestionTx's scan at most once, not twice.
	Header, PresetRef string
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
	Merge            string // accept_epic/accept_fix only: "auto" | "manual" | "local"
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
	// FinishLocal is true on an accept row when no repo in its binding has a GitHub remote.
	FinishLocal bool `json:"finish_local,omitempty"`
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

// repointRequestsTx moves an agent's open requests onto its newest session.
// Requests are keyed by canonical agent id, so they survive a replacement
// by construction; the session pointer follows so session-scoped views
// (ResolveSessionPrompts, the terminal answer flow) keep working on the
// live generation instead of the retired predecessor.
func (s *Store) repointRequestsTx(ctx context.Context, tx *sql.Tx, agentID, sessionID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE requests SET session_id = ?
		WHERE agent_id = ? AND state = 'open'`, sessionID, agentID)
	return err
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
// absent: they have no asking agent (the reconciler opens them), so
// terminalAgent keeps its item-rooted lookup; nativeAnswerKind adds them for
// native_answer once routed.
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
	var direct struct {
		Evidence *string `json:"evidence"`
	}
	if len(r.Binding) > 0 {
		json.Unmarshal(r.Binding, &direct)
	}
	if direct.Evidence != nil {
		return direct.Evidence
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
	if isAcceptKind(r.Kind) {
		local, err := s.finishLocalTx(ctx, tx, r)
		if err != nil {
			return RequestWire{}, err
		}
		w.FinishLocal = local
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

// OnRequestOpened is wired into items.Store.RequestOpened: it routes the
// accept_* rows the reconciler opens to the root's live orchestrator
// (routeAcceptTx), then raises their §17.5 notification. In production
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
	if err := s.routeAcceptTx(ctx, tx, id); err != nil {
		return err
	}
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

// reaskQuestionNext / blockerOpenNext are the request_open relay's next step
// for a still-open plain question and blocker (epic-approval-lane copy).
const reaskQuestionNext = "Ask the user again with the same text and options: claude, agy and codex with your " +
	"native question tool, cursor and muse with swarm_ask kind:\"question\". Swarm keeps one Needs-you row for it."
const blockerOpenNext = "Your blocker is still open in Needs you. The user's answer arrives as a " +
	"user_answer message; don't ask again."

// relayRequestTx sends req.AgentID one request_open relay: the request id,
// its native prompt (approval kinds) and the next step (spec
// 2026-09-26-epic-approval-lane). It is the one delivery path for a request
// the agent did not get back from its own swarm_ask call: an accept row
// routed to it, a close_spike its checkpoint opened, or an open request
// re-surfaced after a restart or wake. enqueueRaw, not enqueue: the
// exhausted-kind hold keeps only a 500-char sample and would destroy the
// native prompt, and this is one message per open request, not a fan-in
// flood; WakeDue already skips an exhausted kind, so nothing wakes early.
func (s *Store) relayRequestTx(ctx context.Context, tx *sql.Tx, id string) error {
	req, err := s.requestTx(ctx, tx, id)
	if err != nil {
		return err
	}
	a, err := s.agentByIDTx(ctx, tx, req.AgentID)
	if err != nil {
		return err
	}
	key, err := s.itemKey(ctx, tx, req.ItemID)
	if err != nil {
		return err
	}
	payload := map[string]any{"event": "request_open", "agent": a.Name, "item": key,
		"request_id": req.ID, "kind": req.Kind}
	switch req.Kind {
	case KindQuestion:
		payload["question"], payload["options"], payload["next"] = req.Prompt, req.Options, reaskQuestionNext
	case KindBlocker:
		payload["question"], payload["next"] = req.Prompt, blockerOpenNext
	default:
		np, err := s.storedNativePromptTx(ctx, tx, req)
		if err != nil {
			return err
		}
		payload["question"], payload["native_prompt"], payload["next"] = np.Question, np, NativePromptNextStep(req.ID, PromptDecisions(req.Kind, np))
		if isAcceptKind(req.Kind) {
			if payload["chat_block"], err = s.approvalChatBlockTx(ctx, tx, req); err != nil {
				return err
			}
		}
		if req.Kind == KindApproveSection || req.Kind == KindApprovePlan || req.Kind == KindApproveReport {
			payload["summary"] = req.Prompt
			if payload["chat_block"], err = s.approvalChatBlockTx(ctx, tx, req); err != nil {
				return err
			}
		}
		if req.Kind == KindApprovePlan {
			paths, _, err := s.planReviewPathsTx(ctx, tx, req.ItemID, req.ArtifactID)
			if err != nil {
				return err
			}
			payload["review_paths"] = paths
		}
		// A request with a chat_block is two turns (print-then-ask): this relay carries only the
		// block; the question follows in a request_ask relay once the reply is checked.
		if cb, _ := payload["chat_block"].(string); cb != "" {
			delete(payload, "question")
			delete(payload, "native_prompt")
			payload["next"] = PrintNext
			if err := s.startPrintTx(ctx, tx, req.ID); err != nil {
				return err
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	rootID, err := s.rootItemID(ctx, tx, req.ItemID)
	if err != nil {
		return err
	}
	_, err = s.enqueueRaw(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: req.AgentID,
		RootItemID: rootID, ItemID: req.ItemID, RequestID: req.ID, Payload: body})
	return err
}

// liveRootOrchestratorTx returns the live top-level orchestrator of itemID's
// root (the agent terminalAgent names for an accept row) and its newest live
// session; ok is false when there is none.
func (s *Store) liveRootOrchestratorTx(ctx context.Context, tx *sql.Tx, itemID string) (agentID, sessionID string, ok bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT a.id, se.id FROM agents a
		JOIN items i ON i.id = ? AND a.root_item_id = i.root_id
		JOIN sessions se ON se.agent_id = a.id
		WHERE a.role = 'orchestrator' AND a.parent_agent_id IS NULL AND a.state = 'active'
		  AND se.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		ORDER BY se.generation DESC, se.started_at DESC LIMIT 1`, itemID).Scan(&agentID, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return agentID, sessionID, err == nil, err
}

// routeAcceptTx binds a daemon-opened accept_epic/accept_fix row to its
// root's live top-level orchestrator and relays it the native prompt, like
// any approval that orchestrator asked for itself (locked decision 1). With
// no live orchestrator the row stays agentless -- board and CLI still
// resolve it -- and startSession binds and relays it when one starts
// (resurfaceOpenRequests). Any other kind is a no-op.
func (s *Store) routeAcceptTx(ctx context.Context, tx *sql.Tx, id string) error {
	req, err := s.requestTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if req.Kind != KindAcceptEpic && req.Kind != KindAcceptFix {
		return nil
	}
	agentID, sesID, ok, err := s.liveRootOrchestratorTx(ctx, tx, req.ItemID)
	if err != nil || !ok {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET agent_id = ?, session_id = ? WHERE id = ?`,
		agentID, sesID, id); err != nil {
		return err
	}
	return s.relayRequestTx(ctx, tx, id)
}

// resurfaceOpenRequests is the one wake/restart helper (epic-approval-lane
// locked decision 2). For a top-level orchestrator it first binds its root's
// open accept rows to sessionID (a row that opened while no orchestrator
// was live). Then it relays every open request routed to the agent that the
// agent can't already see. On a same-session wake (fresh=false), a
// question/blocker asked in this session, or an approval whose native
// question row is open in it, is visible; a fresh session sees nothing. A
// request whose last request_open relay is still unacked is counted but not
// relayed again: swarm_sync redelivers it. Skipped entirely: permission
// prompts (their pane is gone) and ref-bound question rows (the shadow of an
// approval relayed on its own; msg_ refs keep question_unanswered). n is
// how many requests still wait on the user.
//
// since is the quota-reset cutoff a caller is retrying against (zero for the
// session-start caller, which has no such notion). checkQuotaResets calls
// WakeOnQuotaReset once a minute for up to an hour with the SAME cutoff,
// before knowing whether this tick actually wakes the session (review fix,
// epic-approval-lane): a request that already got a relay at or after since
// counts as pending here too, even if that relay was since acked, so a
// session that never wakes this whole cutoff cycle gets at most one relay
// per request instead of one per tick.
func (s *Store) resurfaceOpenRequests(ctx context.Context, a Agent, sessionID string, fresh bool, since time.Time) (int, error) {
	visible := sessionID
	if fresh {
		visible = ""
	}
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		n = 0
		if fresh {
			// A fresh session never saw the print/ask instructions of its predecessor:
			// retire them, and the request_open relay below restarts the print step.
			rows, err := tx.QueryContext(ctx, `SELECT id FROM requests WHERE agent_id = ? AND state = 'open'`, a.ID)
			if err != nil {
				return err
			}
			var open []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				open = append(open, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, id := range open {
				if err := s.retirePrintRelaysTx(ctx, tx, id); err != nil {
					return err
				}
			}
			if err := s.retireUnansweredChildPrintTx(ctx, tx, a.ID); err != nil {
				return err
			}
		}
		if a.Role == RoleOrchestrator && a.ParentAgentID == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE requests SET agent_id = ?, session_id = ?
				WHERE state = 'open' AND kind IN ('accept_epic', 'accept_fix')
				  AND item_id IN (SELECT id FROM items WHERE root_id = ?)`, a.ID, sessionID, a.RootItemID); err != nil {
				return err
			}
			if err := s.deliverFinishApproval(ctx, tx, a, sessionID); err != nil {
				return err
			}
		}
		// A relay counts as still pending while unackedFor would still
		// redeliver its body (pending, or delivered fewer than
		// maxFullDeliveries times) -- once a relay is stuck at
		// maxFullDeliveries, Sync stops resending it and it must be
		// re-relayed here instead of being treated as pending forever. It
		// also counts as pending, regardless of ack state, once it was
		// created at or after `since`: that ties re-relaying to whether THIS
		// cutoff cycle has already sent one, not to whether the agent's own
		// swarm_sync happened to ack it in between ticks.
		pendingCond := "(m.state <> 'acked' AND NOT (m.state = 'delivered' AND m.delivery_count >= ?))"
		args := []any{maxFullDeliveries}
		if !since.IsZero() {
			pendingCond += " OR m.created_at >= ?"
			args = append(args, db.Millis(since))
		}
		args = append(args, a.ID, visible, visible)
		rows, err := tx.QueryContext(ctx, `SELECT r.id,
			EXISTS (SELECT 1 FROM messages m WHERE m.to_agent_id = r.agent_id AND m.request_id = r.id
				AND m.kind = 'relay' AND (`+pendingCond+`))
			FROM requests r
			WHERE r.agent_id = ? AND r.state = 'open' AND r.kind <> 'prompt'
			  AND NOT (r.kind = 'question' AND json_extract(r.binding_json, '$.ref') IS NOT NULL)
			  AND NOT (r.kind IN ('question', 'blocker') AND r.session_id = ?)
			  AND NOT EXISTS (SELECT 1 FROM requests q WHERE q.kind = 'question' AND q.state = 'open'
				AND q.session_id = ? AND json_extract(q.binding_json, '$.ref') = r.id)
			ORDER BY r.created_at, r.id`, args...)
		if err != nil {
			return err
		}
		var relay []string
		for rows.Next() {
			var id string
			var pending bool
			if err := rows.Scan(&id, &pending); err != nil {
				rows.Close()
				return err
			}
			n++
			if !pending {
				relay = append(relay, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range relay {
			if err := s.relayRequestTx(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// deliverFinishApproval sends a board/CLI finish approval made while no orchestrator was routed
// (locked decision 12) to the root's top-level orchestrator, once: binding the row stops a later
// session start re-sending it, and item_merges rows mean finishing already ran. The origin is the
// user's own approval, which Approve could not deliver (resolve skips an agentless row).
func (s *Store) deliverFinishApproval(ctx context.Context, tx *sql.Tx, a Agent, sessionID string) error {
	fa, ok, err := s.Items.FinishApprovalTx(ctx, tx, a.RootItemID)
	if err != nil || !ok || fa.Merge == "" || fa.AgentID != "" {
		return err
	}
	var rows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_merges WHERE item_id = ? AND integrated_checkpoint = ?`,
		a.RootItemID, fa.CheckpointID).Scan(&rows); err != nil || rows > 0 {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET agent_id = ?, session_id = ? WHERE id = ?`,
		a.ID, sessionID, fa.RequestID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"decision": "approved", "merge": fa.Merge, "section_id": "", "section_sha256": ""})
	_, err = s.enqueue(ctx, tx, Message{Kind: "approval_result", Origin: "user_action", ToAgentID: a.ID,
		RootItemID: a.RootItemID, ItemID: a.RootItemID, RequestID: fa.RequestID, Payload: payload})
	return err
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
// duplicate it. muse is deliberately absent from the copy below: Task 4's
// live probe (2026-09-25/26, docs/plans/2026-09-25-needs-you-and-child-
// approval-routing.md) found its request_user_input never dispatches a hook
// at all, so it joins cursor's exception. codex's request_user_input_async
// was confirmed live on 2026-09-26 (docs/specs/2026-09-26-codex-native-
// approval.md), superseding Task 4b's unfinished check.
const errQuestionUseNativeTool = "Ask the user with your own native question tool " +
	"(claude AskUserQuestion, agy ask_question, codex request_user_input). " +
	"Swarm shows it in Needs you and closes it when the user answers."

// questionHookKinds are the kinds whose native question tool Swarm
// intercepts via a hook (spec section 1.7; codex added 2026-09-26,
// docs/specs/2026-09-26-codex-native-approval.md). cursor and muse are
// absent on purpose: neither dispatches a hook for its native question
// tool at all, so both keep swarm_ask kind:"question" as their only path
// to Needs you.
var questionHookKinds = map[AgentKind]bool{Claude: true, Agy: true, Codex: true}

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
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Repository confirmation asks are no longer used; register a local Git path with swarm_repo_register when needed."}
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
		// A question asked again after a restart or wake (epic-approval-lane
		// decision 2) reuses the agent's open row with the same prompt instead
		// of opening a second Needs-you row: the hook's PostToolUse closes only
		// the newest match, so a duplicate would stay open forever. The row
		// follows the caller's session so ResolveQuestionByPrompt finds it.
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT id FROM requests WHERE agent_id = ? AND kind = 'question'
			AND state = 'open' AND prompt = ? ORDER BY created_at LIMIT 1`, a.ID, in.Prompt).Scan(&existing)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE requests SET session_id = ? WHERE id = ?`, sessionID, existing); err != nil {
				return err
			}
			out, err = s.requestTx(ctx, tx, existing)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id := ids.New("req")
		// A prompt forwarded verbatim from a daemon-issued native_prompt
		// matches the normalized question text of an open approval routed to
		// this agent (2026-09-28-approval-summary-enforced locked decision
		// 2, with the old ⟦swarm:ref⟧ token kept as a fallback for a
		// pre-deploy in-flight session): bind the row to it so native_answer
		// can later find its evidence. A plain question binds nothing, so
		// binding_json stays NULL.
		var binding any
		ref, ok := in.PresetRef, in.PresetRef != ""
		if !ok {
			var err error
			if ref, ok, err = s.bindNativeQuestionTx(ctx, tx, a.ID, in.Header, in.Prompt); err != nil {
				return err
			}
		}
		if ok {
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

// AskQuestionBoundTo is AskQuestion for a caller that already resolved the
// ref via BindNativeQuestion for this exact call (the PreToolUse summary
// gate does, to decide allow/deny before ever recording the row): it binds
// directly to ref instead of re-running bindNativeQuestionTx's scan, so one
// hook invocation causes at most one bind attempt, not two (post-review
// fix). An empty ref behaves exactly like AskQuestion (no binding).
func (s *Store) AskQuestionBoundTo(ctx context.Context, sessionID, prompt string, options []string, ref string) (Request, error) {
	return s.askQuestion(ctx, sessionID, AskInput{
		Kind:      "question",
		Prompt:    prompt,
		Options:   options,
		PresetRef: ref,
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

// OpenDialogPrompt opens (or returns the already-open) prompt row for a
// dialog visible in this session's pane. Dedupe key: (session_id, prompt=title, state=open).
func (s *Store) OpenDialogPrompt(ctx context.Context, sessionID, title string) (Request, bool, error) {
	var out Request
	created := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM requests WHERE session_id = ? AND kind = 'prompt'
			AND state = 'open' AND prompt = ? ORDER BY created_at LIMIT 1`, sessionID, title).Scan(&id)
		if err == nil {
			out, err = s.requestTx(ctx, tx, id)
			return err
		}
		if err != sql.ErrNoRows {
			return err
		}
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		id = ids.New("req")
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at) VALUES (?, 'prompt', 1, ?, ?, ?, ?, '[]', 'open', ?)`,
			id, a.ID, sessionID, a.ItemID, title, db.Millis(s.Now())); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		created = true
		out, err = s.finishOpen(ctx, tx, id, a.Name, key, map[string]string{"prompt": title})
		return err
	})
	return out, created, err
}

// ResolveDialogPrompt closes the open prompt row(s) of this session whose
// prompt == title, as answered via "terminal". No-op when none is open.
func (s *Store) ResolveDialogPrompt(ctx context.Context, sessionID, title string) error {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests WHERE session_id = ? AND kind = 'prompt'
		AND state = 'open' AND prompt = ?`, sessionID, title)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.ResolvePrompt(ctx, id, "terminal"); err != nil {
			return err
		}
	}
	return nil
}

// stripSectionHeadingLine drops a section's own "# " or "## " heading line
// (after skipping any leading blank lines) so nothing_to_review's body guard
// counts only the section's actual content -- never for the headingless
// "document" preamble section, whose first line is real content, not a
// heading to discard (review fix, 2026-09-28-empty-section-auto-approve).
func stripSectionHeadingLine(body string, isDocument bool) string {
	if isDocument {
		return strings.TrimSpace(body)
	}
	lines := strings.SplitAfter(body, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) {
		line := strings.TrimRight(lines[i], "\r\n")
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			i++
		}
	}
	return strings.TrimSpace(strings.Join(lines[i:], ""))
}

// startsWithNumberedListMarker reports whether s (already trimmed) opens
// with a numbered-list marker like "1.".
func startsWithNumberedListMarker(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > 0 && i < len(s) && s[i] == '.'
}

// nothingToReviewBodyOK is the coordinator's tightened auto-approve guard
// (2026-09-28-empty-section-auto-approve, post-review): the body, already
// heading-stripped and trimmed, must be at most 120 runes, a single
// non-empty line, and contain no table row ('|'), list item (a line
// starting with -, *, + or a numbered marker like "1."), or code fence
// (```). A longer or richer body is refused, never auto-approved.
func nothingToReviewBodyOK(body string) bool {
	if utf8.RuneCountInString(body) > 120 {
		return false
	}
	if strings.Contains(body, "|") || strings.Contains(body, "```") {
		return false
	}
	nonEmptyLines := 0
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		nonEmptyLines++
		if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "+") ||
			startsWithNumberedListMarker(t) {
			return false
		}
	}
	return nonEmptyLines <= 1
}

func (s *Store) askApproval(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if in.ArtifactID == "" {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "artifact_id is required."}
	}
	n := utf8.RuneCountInString(in.Prompt)
	if in.SectionID != "" && (strings.TrimSpace(in.Prompt) == "" || n > 2000) {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Spec section summary must be 1–2000 characters."}
	}
	if n < 1 || n > 2000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Prompt must be 1–2000 characters."}
	}
	if n > 300 && !strings.Contains(strings.TrimSpace(in.Prompt), "\n") {
		return Request{}, &items.Error{Code: items.CodeBadRequest,
			Message: "Summary must be a lead sentence plus bullets (see swarm-orchestrator: approval summaries)."}
	}
	if in.NothingToReview != "" {
		if rn := utf8.RuneCountInString(in.NothingToReview); rn < 3 || rn > 200 {
			return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "nothing_to_review must be 3–200 characters."}
		}
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
		if in.NothingToReview != "" && reqKind != "approve_section" {
			return &items.Error{Code: items.CodeBadRequest, Message: "nothing_to_review is only for spec sections."}
		}
		var reviewPaths *ReviewPaths
		if reqKind == "approve_plan" {
			paths, specID, err := s.planReviewPathsTx(ctx, tx, itemID, in.ArtifactID)
			if err != nil {
				return err
			}
			if specID != "" {
				if err := s.checkEverySectionApproved(ctx, tx, specID); err != nil {
					return err
				}
			}
			reviewPaths = &paths
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
			var matched ArtifactSection
			for _, sec := range secs {
				if sec.ID == in.SectionID {
					sectionSHA, sectionTitle, found = sec.SHA256, sec.Title, true
					matched = sec
					break
				}
			}
			if !found {
				return &items.Error{Code: items.CodeBadRequest, Message: "Unknown section."}
			}
			if reqKind == "approve_section" && !RequiredSpecSection(sectionTitle) {
				return &items.Error{Code: items.CodeBadRequest, Message: "This spec section is informational and needs no approval."}
			}
			sectionID = sql.NullString{String: in.SectionID, Valid: true}
			if in.NothingToReview != "" {
				if utf8.RuneCountInString(sectionTitle) > 80 {
					return &items.Error{Code: items.CodeBadRequest,
						Message: "This section's heading is too long for nothing_to_review; ask for approval normally."}
				}
				var content string
				if err := tx.QueryRowContext(ctx, `SELECT content FROM artifact_revisions
					WHERE artifact_id = ? AND revision = ?`, in.ArtifactID, headRev).Scan(&content); err != nil {
					return err
				}
				body := stripSectionHeadingLine(content[matched.Start:matched.End], matched.ID == "document")
				if bn := utf8.RuneCountInString(body); !nothingToReviewBodyOK(body) {
					return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
						"This section has content to review (%d characters); ask for approval normally.", bn)}
				}
			}
		}
		if in.NothingToReview != "" {
			out, err = s.autoApproveSectionTx(ctx, tx, a, itemID, sessionID, in, sectionID, sectionSHA, sectionTitle, headRev)
			return err
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
		np, err := s.nativePromptFor(ctx, tx, out, sectionTitle, warnings, reviewPaths)
		if err != nil {
			return err
		}
		if err := s.freezeNativeQuestionTx(ctx, tx, out.ID, np); err != nil {
			return err
		}
		if err := s.startPrintTx(ctx, tx, out.ID); err != nil {
			return err
		}
		out.NativePrompt = &np
		out.ReviewPaths = reviewPaths
		out.ChatBlock, err = s.approvalChatBlockTx(ctx, tx, out)
		return err
	})
	return out, err
}

// autoApproveSectionTx inserts a spec-section approval already resolved as
// approved (docs/specs/2026-09-28-empty-section-auto-approve.md locked
// decisions 2-4): no native question is ever issued -- finishOpen,
// nativePromptFor and freezeNativeQuestionTx are all skipped -- and delivery
// otherwise matches Approve's resolve() path exactly: the same
// approval_result message, the same events.RequestResolved append, the same
// Items.ReconcileTx, so the section rolls up into spec approval like any
// user approval. Its evidence is "auto_empty" and binding_json records the
// reason under "nothing_to_review"; approvalEvidenceTx already reads
// binding_json.evidence directly off a non-question request, so no wire
// changes are needed to surface it. Next is set to the exact copy the
// caller must print in chat.
func (s *Store) autoApproveSectionTx(ctx context.Context, tx *sql.Tx, a Agent, itemID, sessionID string,
	in AskInput, sectionID sql.NullString, sectionSHA, sectionTitle string, headRev int) (Request, error) {
	id := ids.New("req")
	now := s.Now()
	binding, err := json.Marshal(map[string]string{"evidence": "auto_empty", "nothing_to_review": in.NothingToReview})
	if err != nil {
		return Request{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, session_id, item_id,
		artifact_id, section_id, section_sha256, prompt, state, artifact_revision, binding_json,
		responded_via, responded_at, created_at)
		VALUES (?, 'approve_section', ?, ?, ?, ?, ?, ?, ?, 'approved', ?, ?, 'auto', ?, ?)`,
		id, a.ID, sessionID, itemID, in.ArtifactID, sectionID, nullIf(sectionSHA), in.Prompt,
		headRev, string(binding), db.Millis(now), db.Millis(now)); err != nil {
		return Request{}, err
	}
	payload, err := json.Marshal(map[string]any{"decision": "approved",
		"section_id": in.SectionID, "section_sha256": sectionSHA})
	if err != nil {
		return Request{}, err
	}
	if _, err := s.enqueue(ctx, tx, Message{Kind: "approval_result", Origin: "daemon", ToAgentID: a.ID,
		RootItemID: a.RootItemID, ItemID: itemID, RequestID: id, Payload: payload}); err != nil {
		return Request{}, err
	}
	w, err := s.RequestWireTx(ctx, tx, id)
	if err != nil {
		return Request{}, err
	}
	if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
		return Request{}, err
	}
	key, err := s.itemKey(ctx, tx, itemID)
	if err != nil {
		return Request{}, err
	}
	if err := s.Items.ReconcileTx(ctx, tx, key); err != nil {
		return Request{}, err
	}
	out, err := s.requestTx(ctx, tx, id)
	if err != nil {
		return Request{}, err
	}
	out.Next = fmt.Sprintf(
		"Print this line in chat: Section %q: nothing to review (%s) — auto-approved. Then continue with the next section.",
		sectionTitle, in.NothingToReview)
	return out, nil
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
		if err := s.retirePrintRelaysTx(ctx, tx, id); err != nil {
			return err
		}
		for _, fn := range after {
			if err := fn(tx, req); err != nil {
				return err
			}
		}
		// An accept row routed to the root orchestrator (routeAcceptTx) has an
		// agent and gets its approval_result like any approval; an unrouted one
		// (no live orchestrator) has nobody to tell, so the enqueue is skipped.
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
// An accept row also needs a finish choice (in.Merge), checked against the
// binding's repos and stored in $.merge in the same tx.
func (s *Store) Approve(ctx context.Context, id string, in ApproveInput, after ...func(*sql.Tx, Request) error) (Request, error) {
	merge := func(tx *sql.Tx, req Request) error {
		if !isAcceptKind(req.Kind) {
			return nil
		}
		local, err := s.finishLocalTx(ctx, tx, req)
		if err != nil {
			return err
		}
		if local && in.Merge != "local" {
			return &items.Error{Code: items.CodeBadRequest, Message: `Choose how to finish: merge must be "local" (no repository has a GitHub remote).`}
		}
		if !local && in.Merge != "auto" && in.Merge != "manual" {
			return &items.Error{Code: items.CodeBadRequest, Message: `Choose how to finish: merge must be "auto" or "manual".`}
		}
		return setMergeHook(ctx, id, in.Merge)(tx, req)
	}
	return s.resolve(ctx, id, "approved", "", in.Via, "user_action", approveCheck(in),
		func(req Request) (MessageKind, any) {
			p := map[string]any{"decision": "approved",
				"section_id": req.SectionID, "section_sha256": req.SectionSHA256}
			if isAcceptKind(req.Kind) {
				p["merge"] = in.Merge
			}
			return "approval_result", p
		}, append([]func(*sql.Tx, Request) error{merge}, after...)...)
}

func isAcceptKind(k RequestKind) bool { return k == KindAcceptEpic || k == KindAcceptFix }

// setMergeHook is a resolve after hook recording a finish approval's merge choice.
func setMergeHook(ctx context.Context, id, merge string) func(*sql.Tx, Request) error {
	return func(tx *sql.Tx, _ Request) error {
		_, err := tx.ExecContext(ctx, `UPDATE requests SET binding_json = json_set(binding_json, '$.merge', ?) WHERE id = ?`, merge, id)
		return err
	}
}

// finishLocalTx reports an accept row whose integrated repos all lack a GitHub remote.
func (s *Store) finishLocalTx(ctx context.Context, tx *sql.Tx, req Request) (bool, error) {
	var b struct {
		Git []GitRef `json:"git"`
	}
	json.Unmarshal(req.Binding, &b)
	repos, err := s.finishReposTx(ctx, tx, req.ItemID, b.Git)
	if err != nil || len(repos) == 0 {
		return false, err
	}
	for _, r := range repos {
		if r.GitHub {
			return false, nil
		}
	}
	return true, nil
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
//
// Continuity fallback: when the calling session is retired (a replacement or
// retry repointed its open rows at the successor generation), the same
// prompt is resolved on the agent's live rows instead, mirroring
// ResolveAnsweredInTerminal's agent-keyed rule. A live session with no match
// still resolves nothing.
func (s *Store) ResolveQuestionByPrompt(ctx context.Context, sessionID, prompt, answer string) (Request, error) {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests
		WHERE session_id = ? AND kind = 'question' AND state = 'open' AND prompt = ?
		ORDER BY created_at DESC LIMIT 1`, sessionID, prompt)
	if err != nil {
		return Request{}, err
	}
	if len(ids) == 0 {
		ids, err = s.retiredSessionRequests(ctx, sessionID, "question", prompt)
		if err != nil {
			return Request{}, err
		}
	}
	if len(ids) == 0 {
		return Request{}, nil
	}
	return s.ResolveQuestion(ctx, ids[0], answer, "terminal")
}

// ResolveQuestionReply closes the question row that one entry of a codex
// question reply answers, via terminal (docs/specs/2026-09-26-codex-native-
// approval.md). A question carrying a ⟦swarm:ref⟧ resolves the session's
// agent's newest open question row bound to that ref -- agent-keyed, like
// ResolveAnsweredInTerminal, so a row repointed at a successor session still
// matches; a question without one resolves by exact prompt through
// ResolveQuestionByPrompt. No match is not an error: the zero Request comes
// back.
func (s *Store) ResolveQuestionReply(ctx context.Context, sessionID, question, answer string) (Request, error) {
	// A missing/unknown session row (or any other lookup error) falls back
	// to the plain-prompt resolver, exactly like "no ref"/"no match" below --
	// restored post-review: this used to return the error outright, unlike
	// every other branch here, which treats "can't identify a ref" as
	// nothing more than "resolve by prompt instead."
	var agentID string
	err := s.DB.QueryRowContext(ctx, `SELECT agent_id FROM sessions WHERE id = ?`, sessionID).Scan(&agentID)
	if err != nil {
		return s.ResolveQuestionByPrompt(ctx, sessionID, question, answer)
	}
	// A literal ⟦swarm:ref⟧ token (a prompt built before this deploy)
	// resolves directly by that ref, exactly as before -- no text
	// comparison, so a reworded surrounding message still binds.
	if ref := refFromPrompt(question); ref != "" {
		ids, err := s.queryIDs(ctx, `SELECT id FROM requests WHERE agent_id = ? AND kind = 'question' AND state = 'open'
			AND json_extract(binding_json, '$.ref') = ? ORDER BY created_at DESC LIMIT 1`, agentID, ref)
		if err != nil {
			return Request{}, err
		}
		if len(ids) == 0 {
			return Request{}, nil
		}
		return s.ResolveQuestion(ctx, ids[0], answer, "terminal")
	}
	// docs/specs/2026-09-28-empty-section-auto-approve.md locked decision 6
	// (Opus review of 576a53d, minor 1): resolve through the row the daemon
	// already bound at ask time (askQuestion -> bindNativeQuestionTx, with
	// the adapter's real header disambiguating identical child bodies) --
	// never re-bind by text here, where no header is available. With two
	// children sending byte-identical bodies, a fresh header-less rebind
	// can pick the wrong child's ref and leave the right row open.
	norm := NormalizeQuestion(question)
	if norm != "" {
		rows, err := s.DB.QueryContext(ctx, `SELECT id, prompt FROM requests
			WHERE agent_id = ? AND kind = 'question' AND state = 'open'
			  AND json_extract(binding_json, '$.ref') IS NOT NULL
			ORDER BY created_at DESC`, agentID)
		if err != nil {
			return Request{}, err
		}
		var matches []string
		for rows.Next() {
			var id, prompt string
			if err := rows.Scan(&id, &prompt); err != nil {
				rows.Close()
				return Request{}, err
			}
			if NormalizeQuestion(prompt) == norm {
				matches = append(matches, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return Request{}, err
		}
		rows.Close()
		switch len(matches) {
		case 1:
			return s.ResolveQuestion(ctx, matches[0], answer, "terminal")
		case 0:
			// fall through to the plain-prompt fallback below.
		default:
			// More than one already-bound open row matches this text: never
			// guess which one the reply is for (post-review minor 3). Fall
			// through to the plain-prompt fallback instead, which has its
			// own separate, pre-existing tie-break.
			s.log("resolve_question_reply: %d already-bound rows match %q for agent %s, refusing to guess",
				len(matches), norm, agentID)
		}
	}
	return s.ResolveQuestionByPrompt(ctx, sessionID, question, answer)
}

// retiredSessionRequests is the continuity fallback for the session-scoped
// resolvers: when sessionID is retired (not the agent's live generation),
// return the agent's open rows of the same kind (and prompt, when given)
// instead -- a replacement repoints open requests at the successor, so the
// predecessor's own resolvers would otherwise silently miss rows that are
// still open. A live or unknown session falls back to nothing.
func (s *Store) retiredSessionRequests(ctx context.Context, sessionID, kind, prompt string) ([]string, error) {
	var agentID, state string
	err := s.DB.QueryRowContext(ctx, `SELECT se.agent_id, se.state FROM sessions se WHERE se.id = ?`, sessionID).Scan(&agentID, &state)
	if err != nil {
		return nil, nil
	}
	if SessionState(state).Live() {
		return nil, nil
	}
	query := `SELECT r.id FROM requests r WHERE r.agent_id = ? AND r.kind = ? AND r.state = 'open'`
	args := []any{agentID, kind}
	if prompt != "" {
		query += ` AND (r.prompt = ? OR r.prompt = 'Permission requested')`
		args = append(args, prompt)
	}
	query += ` ORDER BY r.created_at DESC LIMIT 1`
	return s.queryIDs(ctx, query, args...)
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
	if len(ids) == 0 {
		// Continuity fallback for a retired session (see
		// retiredSessionRequests): resolve every open prompt of the agent
		// when the command is blank, else only the matching one.
		if command == "" {
			ids, err = s.retiredSessionRequestsAll(ctx, sessionID)
		} else {
			ids, err = s.retiredSessionRequests(ctx, sessionID, "prompt", command)
		}
		if err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err := s.ResolvePrompt(ctx, id, "terminal"); err != nil {
			s.logf("resolve prompt %s: %v", id, err)
		}
	}
	return nil
}

// retiredSessionRequestsAll is retiredSessionRequests without the prompt
// filter: every open prompt row of a retired session's agent.
func (s *Store) retiredSessionRequestsAll(ctx context.Context, sessionID string) ([]string, error) {
	var agentID, state string
	err := s.DB.QueryRowContext(ctx, `SELECT se.agent_id, se.state FROM sessions se WHERE se.id = ?`, sessionID).Scan(&agentID, &state)
	if err != nil {
		return nil, nil
	}
	if SessionState(state).Live() {
		return nil, nil
	}
	return s.queryIDs(ctx, `SELECT id FROM requests
		WHERE agent_id = ? AND kind = 'prompt' AND state = 'open' ORDER BY created_at`, agentID)
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

package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// NativePrompt is the exact header/question/options an orchestrator shows
// with its own native question tool for a daemon-issued approval (spec
// section 2.3, copy in section 6). The question always ends with a ref
// token so the hook that observes the answer can bind it back to the
// request or message that asked for it.
type NativePrompt struct {
	Header   string   `json:"header"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// approveOptions is every native prompt's fixed choice pair (spec section 6).
var approveOptions = []string{"Approve", "Request changes"}

// refToken is the suffix every daemon-issued native prompt ends with.
func refToken(ref string) string { return " ⟦swarm:" + ref + "⟧" }

var refRe = regexp.MustCompile(`⟦swarm:((?:req|msg)_[0-9A-Za-z]+)⟧`)

// refFromPrompt extracts a ref token's payload from a prompt, or "" when the
// prompt carries no ref token (a plain question, or free text the user typed).
func refFromPrompt(p string) string {
	if m := refRe.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	return ""
}

// truncateWithToken is extractQuestion's 1000-rune cap (handler.go:70-72),
// applied here so the ref token always survives it: body is truncated from
// the end to make room, never the token itself.
func truncateWithToken(body, ref string) string {
	token := refToken(ref)
	limit := 1000 - utf8.RuneCountInString(token)
	if limit < 0 {
		limit = 0
	}
	r := []rune(body)
	if len(r) > limit {
		body = string(r[:limit])
	}
	return body + token
}

// nativePromptFor builds the daemon-issued native prompt for an approval-kind
// request (spec section 6): approve_section, approve_plan, approve_report,
// confirm_repos, close_spike. sectionTitle and warnings are only used by the
// kinds that need them; passing them for the others is harmless.
func (s *Store) nativePromptFor(ctx context.Context, tx *sql.Tx, req Request, sectionTitle string, warnings []string) (NativePrompt, error) {
	switch req.Kind {
	case KindApproveSection:
		q := fmt.Sprintf("Approve Spec section %q (rev %d)?", sectionTitle, req.ArtifactRevision)
		return NativePrompt{Header: "Spike approval", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
	case KindApprovePlan:
		q := fmt.Sprintf("Approve the plan (rev %d)?", req.ArtifactRevision)
		if len(warnings) > 0 {
			var b strings.Builder
			b.WriteString(q)
			b.WriteString("\nWarnings:")
			for _, w := range warnings {
				b.WriteString("\n- " + w)
			}
			q = b.String()
		}
		return NativePrompt{Header: "Spike approval", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
	case KindApproveReport:
		q := fmt.Sprintf("Approve the debug report (rev %d)?", req.ArtifactRevision)
		return NativePrompt{Header: "Spike approval", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
	case KindConfirmRepos:
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return NativePrompt{}, err
		}
		var opts struct {
			Proposed  []ReposProposal `json:"proposed"`
			Expansion []ReposProposal `json:"expansion"`
		}
		json.Unmarshal(req.Options, &opts)
		names := make([]string, 0, len(opts.Proposed)+len(opts.Expansion))
		var dropped []string
		for _, p := range opts.Proposed {
			name, err := s.repoNameTx(ctx, tx, p.Repo)
			if err != nil {
				return NativePrompt{}, err
			}
			if p.Source == "dropped" {
				dropped = append(dropped, name)
				continue
			}
			names = append(names, name)
		}
		for _, p := range opts.Expansion {
			if p.Source == "dropped" {
				continue
			}
			name, err := s.repoNameTx(ctx, tx, p.Repo)
			if err != nil {
				return NativePrompt{}, err
			}
			names = append(names, name)
		}
		q := fmt.Sprintf("Confirm %d repositories for %s: %s?", len(names), key, strings.Join(names, ", "))
		if len(dropped) > 0 {
			q += "\nDropped: " + strings.Join(dropped, ", ") + "."
		}
		return NativePrompt{Header: "Repositories", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
	case KindCloseSpike:
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return NativePrompt{}, err
		}
		q := fmt.Sprintf("Close %s?", key)
		return NativePrompt{Header: "Close spike", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
	default:
		return NativePrompt{}, nil
	}
}

// NativeAnswerNextStep is the PostToolUse hook's instruction once a native
// question row bound to a daemon-issued ref (binding_json.ref) is recorded
// as answered: forward it. Added 2026-09-26 after native-railway-tracing
// bound 10 approvals via the hook and never called native_answer -- nothing
// told it there was a next step, and it had loaded a stale skill from before
// native_answer existed. Returns "" for a request with no ref (a plain
// question, or no match at all -- req is then the zero value).
func NativeAnswerNextStep(req Request) string {
	var binding struct {
		Ref string `json:"ref"`
	}
	if len(req.Binding) > 0 {
		json.Unmarshal(req.Binding, &binding)
	}
	if binding.Ref == "" {
		return ""
	}
	trimmed := strings.TrimSpace(req.ResponseText)
	if matched, _ := labelShape(trimmed, "Approve"); matched {
		return fmt.Sprintf(`[swarm] Recorded "Approve" for %s. Forward it now: `+
			`swarm_ask kind:"native_answer", ref:%q, decision:"approve"`, binding.Ref, binding.Ref)
	}
	if matched, _ := labelShape(trimmed, "Request changes"); matched {
		return fmt.Sprintf(`[swarm] Recorded "Request changes" for %s. Forward it now: `+
			`swarm_ask kind:"native_answer", ref:%q, decision:"request_changes"`, binding.Ref, binding.Ref)
	}
	return fmt.Sprintf(`[swarm] Recorded %q for %s. Forward it now: `+
		`swarm_ask kind:"native_answer", ref:%q, decide approve or request_changes from the user's text %q`,
		trimmed, binding.Ref, binding.Ref, trimmed)
}

// errNoNativeEvidence and errDecisionMismatch are native_answer's refusals
// (spec section 2.3 step 5, section 4.1).
const errNoNativeEvidence = "No answered native prompt for %s in your terminal. Show the native_prompt " +
	"from swarm_ask verbatim with your native question tool, then forward the user's answer."
const errDecisionMismatch = "The user's native answer was %q, not %q."
const errNativeAnswerWrongTarget = "%s is not an approve_section, approve_plan, approve_report, " +
	"confirm_repos, or close_spike request you asked for."

// errChildApprovalNoNativePath is native_prompt/native_answer's refusal for
// a child's approval question (a msg_ ref) when the caller's kind has no
// native question hook (questionHookKinds, spec section 1.7): a kind without
// that hook never dispatches the hook that would bind an answered question
// row to the ref, so native_answer could never find evidence for it and the
// child would wait forever for an approval_result that never comes (finding
// 1, docs/specs/2026-09-25-needs-you-and-child-approval-routing.md).
//
// 2026-09-26 decision: rather than gate the fallback to kinds without the
// hook, the user accepted a uniform, weaker trust model for every child
// approval regardless of parent kind: a plain swarm_send answer from the
// child's own parent, reply_to the child's own approval:true question,
// always counts as the approval decision -- on the parent's word, with no
// hook evidence. Relaying through the parent this way is simpler than
// per-kind rules, and native evidence (this native_prompt/native_answer
// path) stays available as the stronger-audit option for kinds whose
// question tool is hooked; it still refuses for kinds that lack the hook.
// approval_result via observed native evidence still applies unconditionally
// to requests the daemon itself owns (approve_section/plan/report,
// confirm_repos, close_spike) -- only child (msg_ ref) approvals get this
// fallback.
const errChildApprovalNoNativePath = "Your agent kind has no native approval hook, so native_prompt/" +
	"native_answer can never resolve this. Reply to the child directly: " +
	`swarm_send(to: "<child>", kind: "answer", reply_to: %q, body: "<your decision>"); ` +
	"the child treats that answer as the approval."

// requireNativeApprovalHook refuses a child-approval (msg_ ref) native_prompt
// or native_answer call for an agent kind whose native question tool isn't
// hooked -- see errChildApprovalNoNativePath.
func requireNativeApprovalHook(a Agent, msgID string) error {
	if questionHookKinds[a.Kind] || a.Kind == Fake {
		return nil
	}
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(errChildApprovalNoNativePath, msgID)}
}

// decisionLabel maps native_answer's decision enum to the native prompt
// label the bound row's response text must (case-fold) start with to count
// as observed evidence (spec section 2.3.5).
var decisionLabel = map[string]string{"approve": "Approve", "request_changes": "Request changes"}

// otherApprovalLabel returns the option label decisionLabel does not map to,
// given the one it does: the two-choice prompt's other button.
func otherApprovalLabel(label string) string {
	if strings.EqualFold(label, "Approve") {
		return "Request changes"
	}
	return "Approve"
}

// labelShape reports whether trimmed is exactly label (an AskUserQuestion
// picked option, which comes back as the bare label) or label followed by
// ":" and a free-text remark (the native prompt's own convention). Anything
// else -- including text that merely starts with the label's letters, like
// "Approve, but drop endurio-docs" -- is NOT this shape: that is typed free
// text, not a picked option, even though it happens to start with a label's
// word (finding B6-1). remainder is the text after "label:", trimmed.
func labelShape(trimmed, label string) (matched bool, remainder string) {
	if strings.EqualFold(trimmed, label) {
		return true, ""
	}
	prefix := label + ":"
	if len(trimmed) > len(prefix) && strings.EqualFold(trimmed[:len(prefix)], prefix) {
		return true, strings.TrimSpace(trimmed[len(prefix):])
	}
	return false, ""
}

// matchDecisionEvidence classifies the bound question row's response text
// against the chosen decision's label (spec section 2.3.5, revised spec
// section 1.8 D1): text that IS the label, or the label followed by ":" and
// a remark, is observed -- the remainder becomes the free-text comment when
// the caller didn't send one. A blank answer, the adapter's generic
// "Resolved in terminal" fallback, or any other typed free text (including
// text that merely starts with a label's letters without that exact shape,
// or mentions the other label without picking it) is accepted on the
// agent's word (agent_reported) -- the typed text becomes the comment when
// the caller sent none. The one case still refused is a mismatch: text that
// IS the *other* decision's own option label, or that label followed by
// ":", meaning the user visibly picked the opposite choice and the
// orchestrator forwarded the wrong one (finding B6-1: refusal is narrowed to
// this exact-or-"label:" shape so typed free text is never wrongly refused
// or wrongly marked observed just because it starts with a label's word).
func matchDecisionEvidence(responseText, label, callerComment string) (evidence, comment string, err error) {
	trimmed := strings.TrimSpace(responseText)
	if matched, remainder := labelShape(trimmed, label); matched {
		comment = callerComment
		if comment == "" {
			comment = remainder
		}
		return EvidenceObserved, comment, nil
	}
	other := otherApprovalLabel(label)
	if matched, _ := labelShape(trimmed, other); matched {
		return "", "", fmt.Errorf(errDecisionMismatch, trimmed, label)
	}
	comment = callerComment
	if comment == "" && trimmed != "" && trimmed != "Resolved in terminal" {
		comment = trimmed
	}
	return EvidenceAgentReported, comment, nil
}

// nativeAnswer is swarm_ask kind:"native_answer" (spec section 2.3 steps
// 4-6, Task 13c): it forwards the orchestrator's observed decision for a
// request ref into a real Approve/RequestChanges/ConfirmRepos, but only
// once it has verified a matching native-question row (Task 13b's binding)
// really was answered in that agent's own terminal.
func (s *Store) nativeAnswer(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	label, ok := decisionLabel[in.Decision]
	if !ok {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "decision must be approve or request_changes."}
	}
	if in.Ref == "" {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "ref is required."}
	}
	var rowID, responseText, callerID string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		callerID = a.ID
		if strings.HasPrefix(in.Ref, "msg_") {
			if err := requireNativeApprovalHook(a, in.Ref); err != nil {
				return err
			}
		}
		return tx.QueryRowContext(ctx, `SELECT id, COALESCE(response_text,'') FROM requests
			WHERE kind = 'question' AND agent_id = ? AND state = 'answered' AND responded_via = 'terminal'
			  AND json_extract(binding_json, '$.ref') = ?
			ORDER BY responded_at DESC LIMIT 1`, a.ID, in.Ref).Scan(&rowID, &responseText)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(errNoNativeEvidence, in.Ref)}
	}
	if err != nil {
		return Request{}, err
	}
	evidence, comment, err := matchDecisionEvidence(responseText, label, in.Comment)
	if err != nil {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: err.Error()}
	}
	// RequestChanges' own length cap, reapplied here (Task 13c): nativeAnswer
	// builds the changes_requested result with s.resolve directly (so the
	// payload can carry evidence), not through RequestChanges itself. A
	// comment is optional (spec 1.8 D1): the old "Request changes needs a
	// comment" refusal is removed -- keep it simple, not too tight.
	if in.Decision == "request_changes" && utf8.RuneCountInString(comment) > 2000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Comment must be at most 2000 characters."}
	}
	// bindEvidence runs inside the same tx as the state change (resolve's or
	// ConfirmRepos's own), right after the UPDATE: the audit record's (a)
	// (spec 2.3.6) lands atomically with (b), the result message's payload.
	bindEvidence := func(tx *sql.Tx, _ Request) error {
		_, err := tx.ExecContext(ctx, `UPDATE requests SET
			binding_json = json_set(COALESCE(binding_json, '{}'), '$.evidence', ?) WHERE id = ?`,
			evidence, rowID)
		return err
	}

	if strings.HasPrefix(in.Ref, "msg_") {
		// The message ref must name an approval question addressed to the
		// caller (spec 2.3, Task 13b): reuse askNativePromptForMsg's own
		// query rather than trusting the evidence row's binding, which any
		// agent can forge locally by pointing its own AskQuestion at someone
		// else's message id.
		if err := s.verifyApprovalMsgAddressedTo(ctx, in.Ref, callerID); err != nil {
			return Request{}, err
		}
		return s.nativeAnswerForMsg(ctx, in.Ref, in.Decision, comment, evidence, rowID, bindEvidence)
	}

	req, err := s.RequestByID(ctx, in.Ref)
	if err != nil {
		return Request{}, err
	}
	// native_answer only forwards the terminal approval kinds spec 2.3 names
	// (approve_section/plan/report, confirm_repos, close_spike), and only for
	// the agent that asked -- not a plain HITL question, accept_epic,
	// accept_fix, or another agent's request, all of which req.Kind and
	// req.AgentID alone can't otherwise be trusted to exclude once an
	// evidence row exists (a caller can forge its own locally, Task B4
	// finding 3).
	if !approvalTerminalKinds[req.Kind] || req.AgentID != callerID {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(errNativeAnswerWrongTarget, in.Ref)}
	}
	// nativeAnswer is the one agent-reachable user_action origin
	// (requests.go's resolve doc comment): it is guarded by the evidence
	// check above, and an agent-reported decision is flagged, not refused
	// (user decision, spec 1.6.2). request_changes is the same call for
	// every request-ref kind, confirm_repos included -- RequestChanges
	// itself has no kind restriction (requests.go), and nativeAnswer needs
	// s.resolve directly, not RequestChanges, only so the payload can carry
	// "evidence".
	if in.Decision == "request_changes" {
		return s.resolve(ctx, in.Ref, "changes_requested", comment, "terminal", "user_action", nil,
			func(req Request) (MessageKind, any) {
				return "approval_result", map[string]any{"decision": "changes_requested",
					"comment": comment, "section_id": req.SectionID, "evidence": evidence}
			}, bindEvidence)
	}
	if req.Kind == KindConfirmRepos {
		var opts struct {
			Proposed  []ReposProposal `json:"proposed"`
			Expansion []ReposProposal `json:"expansion"`
		}
		json.Unmarshal(req.Options, &opts)
		// Approve confirms proposed + expansion together, minus dropped (spec
		// 1.8 D2): the native prompt already lists every repo in that set, so
		// there is no separate approval step for the expansion repos.
		ids := make([]string, 0, len(opts.Proposed)+len(opts.Expansion))
		for _, p := range opts.Proposed {
			if p.Source != "dropped" {
				ids = append(ids, p.Repo)
			}
		}
		for _, p := range opts.Expansion {
			if p.Source != "dropped" {
				ids = append(ids, p.Repo)
			}
		}
		var binding struct {
			ReposVersion int `json:"repos_version"`
		}
		json.Unmarshal(req.Binding, &binding)
		return s.ConfirmRepos(ctx, in.Ref, ids, comment, binding.ReposVersion, "terminal", evidence, bindEvidence)
	}
	in2 := ApproveInput{SectionSHA256: req.SectionSHA256, ArtifactRevision: req.ArtifactRevision,
		Binding: req.Binding, Via: "terminal"}
	// comment carries any typed free text the user added alongside "Approve"
	// (spec 1.8 D1) into the request's own response_text, the same way
	// request_changes and confirm_repos already do.
	return s.resolve(ctx, in.Ref, "approved", comment, "terminal", "user_action", approveCheck(in2),
		func(req Request) (MessageKind, any) {
			return "approval_result", map[string]any{"decision": "approved",
				"section_id": req.SectionID, "section_sha256": req.SectionSHA256, "evidence": evidence}
		}, bindEvidence)
}

// nativeAnswerForMsg is native_answer's message-ref branch (spec section 2.3
// step 6, 2.4, Task 13d): the child's own approval question. There is no
// separate approval request row for a message ref -- the bound native-
// question row itself is the approval record, so it moves from "answered"
// straight to "approved" or "changes_requested", and the daemon tells the
// child directly with an approval_result reply.
func (s *Store) nativeAnswerForMsg(ctx context.Context, msgID, decision, comment, evidence, rowID string,
	bindEvidence func(*sql.Tx, Request) error) (Request, error) {
	newState := "approved"
	if decision == "request_changes" {
		newState = "changes_requested"
	}
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// A ref can be forwarded once (spec 2.3.8), for the ref as a whole,
		// not just for the one bound row: the same child-approval prompt can
		// be shown (and answered) more than once, leaving two 'answered'
		// question rows bound to the same ref. Consuming one must refuse a
		// later call over the other, so check the ref's outcome -- an
		// approval_result already sent for this message, or any question row
		// with this ref already moved past 'answered' -- before touching
		// rowID at all.
		var x int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages
			WHERE kind = 'approval_result' AND reply_to = ? LIMIT 1`, msgID).Scan(&x)
		if err == nil {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM requests WHERE kind = 'question'
			AND json_extract(binding_json, '$.ref') = ? AND state IN ('approved', 'changes_requested')
			LIMIT 1`, msgID).Scan(&x)
		if err == nil {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		res, err := tx.ExecContext(ctx, `UPDATE requests SET state = ? WHERE id = ? AND state = 'answered'`,
			newState, rowID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		if err := bindEvidence(tx, Request{}); err != nil {
			return err
		}
		var fromAgentID, rootItemID, itemID string
		if err := tx.QueryRowContext(ctx, `SELECT from_agent_id, root_item_id, item_id FROM messages
			WHERE id = ?`, msgID).Scan(&fromAgentID, &rootItemID, &itemID); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"request_id": rowID, "decision": newState,
			"comment": comment, "evidence": evidence})
		if err != nil {
			return err
		}
		if _, err := s.enqueue(ctx, tx, Message{Kind: "approval_result", Origin: "daemon", ToAgentID: fromAgentID,
			RootItemID: rootItemID, ItemID: itemID, ReplyTo: msgID, Payload: payload}); err != nil {
			return err
		}
		w, err := s.RequestWireTx(ctx, tx, rowID)
		if err != nil {
			return err
		}
		if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, rowID)
		return err
	})
	return out, err
}

// errForMsgNotApproval is swarm_ask kind:"native_prompt"'s refusal when
// for_msg does not name an approval question addressed to the caller (Task
// 13b). It deliberately does not accept a blocked relay, unlike Task 10's
// answer-reply_to query: a native prompt only ever exists for an explicit
// approval question.
const errForMsgNotApproval = "reply_to %s is not a question addressed to you with approval:true."

// verifyApprovalMsgAddressedTo is native_answer's message-ref ownership check
// (Task B4 finding 3): the same condition askNativePromptForMsg uses to build
// the prompt in the first place, so a ref can only ever be answered by the
// agent it was shown to.
func (s *Store) verifyApprovalMsgAddressedTo(ctx context.Context, msgID, callerID string) error {
	var x int
	err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM messages
		WHERE id = ? AND to_agent_id = ? AND kind = 'question'
		  AND json_extract(payload_json, '$.approval') = 1`, msgID, callerID).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(errForMsgNotApproval, msgID)}
	}
	return err
}

// askNativePromptForMsg is swarm_ask kind:"native_prompt" with for_msg set
// (spec section 2.3 step 1, 2.4): it builds the child-approval native
// prompt for an approval question the child sent this orchestrator, without
// creating any new request row -- the bound question row native_answer
// later needs is the one the hook creates once the prompt is shown.
func (s *Store) askNativePromptForMsg(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if in.ForMsg == "" {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "for_msg is required."}
	}
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if err := requireNativeApprovalHook(a, in.ForMsg); err != nil {
			return err
		}
		var fromName, body string
		err = tx.QueryRowContext(ctx, `SELECT ag.name, json_extract(m.payload_json, '$.body')
			FROM messages m JOIN agents ag ON ag.id = m.from_agent_id
			WHERE m.id = ? AND m.to_agent_id = ? AND m.kind = 'question'
			  AND json_extract(m.payload_json, '$.approval') = 1`, in.ForMsg, a.ID).Scan(&fromName, &body)
		if errors.Is(err, sql.ErrNoRows) {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(errForMsgNotApproval, in.ForMsg)}
		}
		if err != nil {
			return err
		}
		np := nativePromptForMsg(fromName, body, in.ForMsg)
		out = Request{ID: in.ForMsg, State: "open", NativePrompt: &np}
		return nil
	})
	return out, err
}

// nativePromptForMsg is the child-approval native prompt (spec section 2.4,
// section 6's "child approval" row): the orchestrator shows the child's own
// text and options verbatim, headed "<child> asks", with a ref token to the
// message id so native_answer can bind to it.
func nativePromptForMsg(child, body, msgID string) NativePrompt {
	return NativePrompt{Header: child + " asks", Question: truncateWithToken(body, msgID), Options: approveOptions}
}

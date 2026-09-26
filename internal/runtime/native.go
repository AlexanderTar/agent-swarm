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
			Proposed []ReposProposal `json:"proposed"`
		}
		json.Unmarshal(req.Options, &opts)
		names := make([]string, 0, len(opts.Proposed))
		for _, p := range opts.Proposed {
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

// errForMsgNotApproval is swarm_ask kind:"native_prompt"'s refusal when
// for_msg does not name an approval question addressed to the caller (Task
// 13b). It deliberately does not accept a blocked relay, unlike Task 10's
// answer-reply_to query: a native prompt only ever exists for an explicit
// approval question.
const errForMsgNotApproval = "reply_to %s is not a question addressed to you with approval:true."

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

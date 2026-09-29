package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// NativePrompt is the exact header/question/options an orchestrator shows
// with its own native question tool for a daemon-issued approval (spec
// section 2.3, copy in section 6). The question carries no id or token
// (2026-09-28-approval-summary-enforced): the hook that observes the answer
// binds it back to the request or message that asked for it by normalized
// question text (BindNativeQuestion), falling back to the old ⟦swarm:ref⟧
// token only for a prompt built before that deploy.
type NativePrompt struct {
	Header       string   `json:"header"`
	Question     string   `json:"question"`
	Options      []string `json:"options"`
	Descriptions []string `json:"descriptions,omitempty"` // parallel to Options; finish prompts only
}

// ResolvedInTerminal is the hook's PostToolUse fallback response text for a
// question tool call whose adapter reports no answer content (agy, spec
// 1.7): it proves the prompt was answered, but never the option -- never
// something the user actually typed.
const ResolvedInTerminal = "Resolved in terminal"

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

// HasRefToken reports whether p contains a well-formed ⟦swarm:<ref>⟧ token
// (the same shape refFromPrompt parses), not merely the "⟦swarm:" prefix --
// used by the hook's batched-question guard so an unterminated or malformed
// mention of the token syntax doesn't false-positive.
func HasRefToken(p string) bool { return refRe.MatchString(p) }

// NormalizeQuestion trims a question and collapses any run of whitespace to
// a single space (2026-09-28-approval-summary-enforced locked decision 2):
// the shape BindNativeQuestion compares an observed native question against
// a rebuilt stored one with, so reflowed whitespace never breaks the match.
func NormalizeQuestion(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// NormForMatch lowercases s and keeps only letters and digits, dropping
// everything else (markdown punctuation, whitespace). The PreToolUse summary
// gate uses it to compare a stored approval summary against the assistant
// text an agent printed in chat, so markdown reformatting and line wrapping
// never cause a false deny (spec section "Locked decisions" item 3).
func NormForMatch(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// buildApprovalQuestion assembles a daemon-issued approve_section/
// approve_plan/approve_report native question: a head of the request's
// stored summary (approvalSummaryHead), a blank line, any review paths
// (plan only), and the approve line (with plan warnings already folded in)
// -- all within the existing 1000-rune cap
// (2026-09-28-summary-in-native-question). The full summary is printed in
// chat, not here; head is already capped by approvalSummaryHead, but this
// still shortens it further, ending with "…", if the never-cut tail (paths,
// approve line) alone leaves it no room -- and if paths and the approve line
// alone already overflow 1000 runes, dropping the summary can't help
// either: paths are dropped first (still shown losslessly via review_paths
// and the chat print), then, if the approve line/warnings alone still
// overflow, this falls back to capRunes. A separate helper from capRunes
// because its other callers cap a single body, not a summary glued to a
// fixed, never-cut tail. No ref token is appended (2026-09-28-approval-
// summary-enforced: binding moved to normalized question-text match).
func buildApprovalQuestion(summary, paths, approveLine string) string {
	tail := paths + approveLine
	if utf8.RuneCountInString(tail) > 1000 {
		if paths != "" {
			return buildApprovalQuestion(summary, "", approveLine)
		}
		return capRunes(approveLine, 1000)
	}
	limit := 1000 - utf8.RuneCountInString(tail) - 2 // "\n\n"
	sr := []rune(summary)
	switch {
	case limit <= 0:
		summary = ""
	case len(sr) > limit:
		if limit == 1 {
			summary = "…"
		} else {
			summary = string(sr[:limit-1]) + "…"
		}
	}
	if summary == "" {
		return tail
	}
	return summary + "\n\n" + tail
}

// capRunes shortens s to at most limit runes, ending with "…" when cut.
func capRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 0 {
		return ""
	}
	return string(r[:limit-1]) + "…"
}

// approvalSummaryHead is the portion of a stored approval summary shown
// inline in the native question (2026-09-28-summary-in-native-question
// scope change): the full summary is printed verbatim in chat by the asking
// agent (a later hook task enforces that for the hooked kinds), so the
// native question itself only needs a short head. claude/agy/codex
// (questionHookKinds -- their native question tool is hooked, so Swarm can
// later confirm the chat print) get the summary's first NON-blank line,
// trimmed of surrounding whitespace (so a leading \n or \r\n, or stray
// spaces/\r around it, never leave the head blank or dirty), capped to 200
// runes; cursor/muse (no hook, nothing enforces the chat print) get up to
// 600 runes of the summary, unlined.
func approvalSummaryHead(summary string, kind AgentKind) string {
	if questionHookKinds[kind] {
		for _, line := range strings.Split(summary, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return capRunes(line, 200)
			}
		}
		return ""
	}
	return capRunes(summary, 600)
}

// requestAgentKindTx looks up the kind of the agent a request is filed
// under -- the agent that will show this request's native question -- so
// nativePromptFor can size the summary head to it. Read fresh on every call
// (never cached on Request), including from storedNativePromptTx's replay
// path: if the agent's kind changes between the original ask and a later
// relay/resurface (reassignment, kind fallback), the rebuilt head is sized
// to the CURRENT kind and can legitimately differ from the one first shown
// -- byte-identical replay (TestStoredNativePromptRebuildIsByteIdentical...)
// only holds when the kind hasn't changed. Before 2026-09-28-approval-
// summary-enforced this was safe unconditionally: the hook bound an
// answered question row purely by its trailing ⟦swarm:ref⟧ token, never by
// the rest of the question text. Binding now matches by normalized question
// text (BindNativeQuestion), so a kind change between the original ask and
// the answer is a known limitation: the text actually shown no longer
// equals the freshly rebuilt one, and BindNativeQuestion fails to bind it
// (see TestStoredNativePromptRebuildAgentKindChangeBreaksTextBinding). A
// prompt still carrying the old ref token is unaffected -- the token
// fallback binds regardless of any text drift.
func (s *Store) requestAgentKindTx(ctx context.Context, tx *sql.Tx, agentID string) (AgentKind, error) {
	var kind AgentKind
	err := tx.QueryRowContext(ctx, `SELECT kind FROM agents WHERE id = ?`, agentID).Scan(&kind)
	return kind, err
}

// planPathsBlock formats a plan approval's review paths as the lines shown
// immediately above the approve line: "Spec: <abs path>\n" (when a spec
// exists) followed by "Plan: <abs path>\n". Empty when paths is nil (no
// review paths available, e.g. built outside askApproval/storedNativePromptTx).
func planPathsBlock(paths *ReviewPaths) string {
	if paths == nil {
		return ""
	}
	var b strings.Builder
	if paths.Spec != "" {
		b.WriteString("Spec: " + paths.Spec + "\n")
	}
	b.WriteString("Plan: " + paths.Plan + "\n")
	return b.String()
}

// nativePromptFor builds the daemon-issued native prompt for an approval-kind
// request (spec section 6): approve_section, approve_plan, approve_report,
// confirm_repos, close_spike, accept_epic, accept_fix. sectionTitle and
// warnings are only used by the kinds that need them, and reviewPaths only
// by approve_plan; passing them for the others is harmless. For
// approve_section/approve_plan/approve_report the question leads with the
// request's own stored summary (req.Prompt) so the user sees it even if the
// asking agent never prints it (2026-09-28-summary-in-native-question).
func (s *Store) nativePromptFor(ctx context.Context, tx *sql.Tx, req Request, sectionTitle string, warnings []string, reviewPaths *ReviewPaths) (NativePrompt, error) {
	switch req.Kind {
	case KindApproveSection, KindApprovePlan, KindApproveReport:
		agentKind, err := s.requestAgentKindTx(ctx, tx, req.AgentID)
		if err != nil {
			return NativePrompt{}, err
		}
		head := approvalSummaryHead(req.Prompt, agentKind)
		switch req.Kind {
		case KindApproveSection:
			approveLine := fmt.Sprintf("Approve Spec section %q (rev %d)?", sectionTitle, req.ArtifactRevision)
			q := buildApprovalQuestion(head, "", approveLine)
			return NativePrompt{Header: "Spike approval", Question: q, Options: approveOptions}, nil
		case KindApprovePlan:
			approveLine := fmt.Sprintf("Approve the plan (rev %d)?", req.ArtifactRevision)
			if len(warnings) > 0 {
				var b strings.Builder
				b.WriteString(approveLine)
				b.WriteString("\nWarnings:")
				for _, w := range warnings {
					b.WriteString("\n- " + w)
				}
				approveLine = b.String()
			}
			q := buildApprovalQuestion(head, planPathsBlock(reviewPaths), approveLine)
			return NativePrompt{Header: "Spike approval", Question: q, Options: approveOptions}, nil
		default: // KindApproveReport
			approveLine := fmt.Sprintf("Approve the debug report (rev %d)?", req.ArtifactRevision)
			q := buildApprovalQuestion(head, "", approveLine)
			return NativePrompt{Header: "Spike approval", Question: q, Options: approveOptions}, nil
		}
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
		return NativePrompt{Header: "Repositories", Question: capRunes(q, 1000), Options: approveOptions}, nil
	case KindCloseSpike:
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return NativePrompt{}, err
		}
		q := fmt.Sprintf("Close %s?", key)
		return NativePrompt{Header: "Close spike", Question: capRunes(q, 1000), Options: approveOptions}, nil
	case KindAcceptEpic, KindAcceptFix:
		var key, title, typ string
		if err := tx.QueryRowContext(ctx, `SELECT key, title, type FROM items WHERE id = ?`, req.ItemID).Scan(&key, &title, &typ); err != nil {
			return NativePrompt{}, err
		}
		header := "Finish epic"
		if req.Kind == KindAcceptFix {
			header = "Finish fix"
			if typ == string(items.Chore) {
				header = "Finish chore"
			}
		}
		var b struct {
			Git []GitRef `json:"git"`
		}
		json.Unmarshal(req.Binding, &b)
		repos, err := s.finishReposTx(ctx, tx, req.ItemID, b.Git)
		if err != nil {
			return NativePrompt{}, err
		}
		return finishPrompt(header, key, title, repos), nil
	default:
		return NativePrompt{}, nil
	}
}

// finishPrompt is the finish question's copy (2026-09-29-finish-with-pr): three PR options, or two
// when no repo has a GitHub remote. An empty repo list (a fixture's "git":[]) reads "Not pushed."
// and keeps the PR options; a repo with no catalog row counts as local with no base.
func finishPrompt(header, key, title string, repos []finishRepo) NativePrompt {
	q := fmt.Sprintf("Finish %s %q?", key, title)
	switch len(repos) {
	case 0:
		q += " Not pushed."
	case 1:
		q += fmt.Sprintf(" Branch %s at %s, not pushed.", repos[0].Ref.Branch, shortSHA(repos[0].Ref.SHA))
	default:
		parts := make([]string, len(repos))
		for i, r := range repos {
			parts[i] = fmt.Sprintf("%s %s at %s", r.Ref.Repo, r.Ref.Branch, shortSHA(r.Ref.SHA))
		}
		q += " Not pushed: " + strings.Join(parts, ", ") + "."
	}
	base, github := "", false
	var local []string
	for _, r := range repos {
		switch {
		case r.Base == "":
		case base == "":
			base = r.Base
		case base != r.Base:
			base = "each repo's default branch"
		}
		if r.GitHub {
			github = true
		} else {
			local = append(local, r.Ref.Repo)
		}
	}
	if base == "" {
		base = "the default branch"
	}
	changes := "Say what to change; I'll re-integrate and ask again."
	np := NativePrompt{Header: header, Question: capRunes(q, 1000)}
	if len(repos) > 0 && !github {
		np.Options = []string{"Merge into " + base + " locally", "Request changes"}
		np.Descriptions = []string{"Merge the branch into " + base + " in your local checkout; Done once it's merged.", changes}
		return np
	}
	note := ""
	for _, l := range local {
		note += " " + l + " has no GitHub remote: merged into " + base + " locally."
	}
	np.Options = []string{"Create PR, auto-merge when checks pass", "Create PR, I'll merge it myself", "Request changes"}
	np.Descriptions = []string{
		"Push, open a PR into " + base + ", merge automatically when checks pass." + note,
		"Push and open a PR into " + base + "; Done when you merge it." + note,
		changes,
	}
	return np
}

// ChatBlockInput is everything ApprovalChatBlock formats
// (docs/specs/2026-09-28-approval-chat-block.md). Kind is an approval
// request kind, or "" for a child approval message (Child names the child).
// N/M is the section's position among the revision's required sections;
// 0 means unknown. Path is the absolute spec (section) or report path, ""
// when unknown; Paths is a plan's review paths.
type ChatBlockInput struct {
	Kind         string
	Revision     int
	Summary      string
	SectionTitle string
	N, M         int
	Path         string
	Paths        *ReviewPaths
	Child        string
	ItemKey      string   // finish: root key
	Git          []GitRef // finish: integrated refs, one line each
}

// ApprovalChatBlock is the exact chat message an agent prints immediately
// before an approval's native question. Every emitter (swarm_ask result,
// request_open relay, for_msg result) and the PreToolUse gate's deny reason
// share it.
func ApprovalChatBlock(in ChatBlockInput) string {
	var head, foot string
	switch RequestKind(in.Kind) {
	case KindApproveSection:
		pos := ""
		if in.N > 0 && in.M > 0 {
			pos = fmt.Sprintf(" %d of %d", in.N, in.M)
		}
		head = fmt.Sprintf("### Approval%s · Spec section %q (rev %d)", pos, in.SectionTitle, in.Revision)
		if in.Path != "" {
			foot = fmt.Sprintf("Full section: %s → \"## %s\"", in.Path, in.SectionTitle)
		}
	case KindApprovePlan:
		head = fmt.Sprintf("### Approval · Plan (rev %d)", in.Revision)
		foot = strings.TrimSuffix(planPathsBlock(in.Paths), "\n")
	case KindApproveReport:
		head = fmt.Sprintf("### Approval · Debug report (rev %d)", in.Revision)
		if in.Path != "" {
			foot = "Report: " + in.Path
		}
	case KindAcceptEpic, KindAcceptFix:
		head = "### Approval · Finish " + in.ItemKey
		lines := make([]string, len(in.Git))
		for i, g := range in.Git {
			lines[i] = fmt.Sprintf("%s: %s at %s", g.Repo, g.Branch, shortSHA(g.SHA))
		}
		foot = strings.Join(lines, "\n")
	default:
		head = "### Approval · " + in.Child + " asks"
	}
	out := head + "\n\n" + in.Summary
	if foot != "" {
		out += "\n\n" + foot
	}
	return out
}

// approvalChatBlockTx gathers a stored approval request's ApprovalChatBlock
// inputs: section title and N/M from the asked revision's sections_json
// (so a replay matches the first ask), the artifact's absolute path, and a
// plan's review paths. "" for a kind with no summary.
func (s *Store) approvalChatBlockTx(ctx context.Context, tx *sql.Tx, req Request) (string, error) {
	in := ChatBlockInput{Kind: string(req.Kind), Revision: req.ArtifactRevision, Summary: req.Prompt}
	switch req.Kind {
	case KindApproveSection:
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT sections_json FROM artifact_revisions
			WHERE artifact_id = ? AND revision = ?`, req.ArtifactID, req.ArtifactRevision).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		var secs []ArtifactSection
		json.Unmarshal([]byte(raw), &secs)
		for _, sec := range secs {
			if sec.ID == req.SectionID {
				in.SectionTitle = sec.Title
			}
			if RequiredSpecSection(sec.Title) {
				in.M++
				if sec.ID == req.SectionID {
					in.N = in.M
				}
			}
		}
		if in.N == 0 {
			in.M = 0
		}
		fallthrough
	case KindApproveReport:
		var path string
		err := tx.QueryRowContext(ctx, `SELECT path FROM artifacts WHERE id = ?`, req.ArtifactID).Scan(&path)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if path != "" {
			if in.Path, err = filepath.Abs(path); err != nil {
				return "", err
			}
		}
	case KindApprovePlan:
		paths, _, err := s.planReviewPathsTx(ctx, tx, req.ItemID, req.ArtifactID)
		if err == nil {
			in.Paths = &paths
		}
	case KindAcceptEpic, KindAcceptFix:
		var b struct {
			Checkpoint string   `json:"integrated_checkpoint"`
			Git        []GitRef `json:"git"`
		}
		json.Unmarshal(req.Binding, &b)
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return "", err
		}
		in.ItemKey, in.Git, in.Summary = key, b.Git, ""
		err = tx.QueryRowContext(ctx, `SELECT summary FROM checkpoints WHERE id = ?`, b.Checkpoint).Scan(&in.Summary)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	default:
		return "", nil
	}
	return ApprovalChatBlock(in), nil
}

// NativePromptNextStep is the show-and-forward instruction that rides with
// every daemon-issued native prompt: swarm_ask's result (mcpserver
// requestOut) and the request_open relay share it verbatim. Decision codes
// appear only in the after-the-answer mapping (label -> code), never beside
// the ask instruction, so an agent can't build its options from them; options
// are native_prompt.options, index-parallel to decisions.
func NativePromptNextStep(ref string, options, decisions []string) string {
	pairs := make([]string, 0, len(decisions))
	for i, d := range decisions {
		if i >= len(options) {
			break
		}
		pairs = append(pairs, strconv.Quote(options[i])+" \u2192 "+strconv.Quote(d))
	}
	return fmt.Sprintf("Ask this now with your native question tool: native_prompt's question, header and options word for word "+
		"(labels and descriptions), one question per call, no added text; don't print chat_block again. "+
		"After the user answers, call swarm_ask kind:\"native_answer\", ref:%q, decision set from their pick: %s. "+
		"Claude, agy, and Codex use their hook-backed answer path. Cursor AskQuestion must include answer_text exactly as returned by the native tool; this has agent_reported provenance. Muse request_user_input: call native_answer right after the tool returns, with answer_text exactly as returned; Swarm checks it against Muse's own session log. On cancellation or no returned answer, submit nothing and leave the request open. "+
		"Codex: use request_user_input, not request_user_input_async; a review question is a design decision the user chooses, not a permission request.", ref, strings.Join(pairs, ", "))
}

// PromptDecisions is the decision list a request's native prompt offers: the finish decisions for
// an accept row, approve/request_changes for every other kind.
func PromptDecisions(kind RequestKind, np NativePrompt) []string {
	if kind == KindAcceptEpic || kind == KindAcceptFix {
		return finishDecisions(np.Options)
	}
	return []string{"approve", "request_changes"}
}

// storedNativePromptTx rebuilds a stored approval's native prompt exactly as
// swarm_ask first returned it (same section title from the asked revision,
// same plan warnings), so a re-shown question matches the original byte for
// byte and the hook's question row binds to the same ref.
func (s *Store) storedNativePromptTx(ctx context.Context, tx *sql.Tx, req Request) (NativePrompt, error) {
	np, frozen, err := s.effectiveNativeQuestionTx(ctx, tx, req)
	if err != nil {
		return NativePrompt{}, err
	}
	if !frozen {
		// This is the request's first real issuance seen by a freezing call
		// site: askApproval/askConfirmRepos already froze their own row
		// before ever returning it, so reaching here unfrozen only happens
		// for a daemon-issued kind whose row is created ahead of its first
		// prompt (close_spike, accept_epic/accept_fix -- relayRequestTx,
		// which calls storedNativePromptTx, is their first caller), or a row
		// created before this change (an in-flight pre-deploy session).
		// Freeze it now so every later call, including a replay, returns
		// this exact text.
		if err := s.freezeNativeQuestionTx(ctx, tx, req.ID, np); err != nil {
			return NativePrompt{}, err
		}
	}
	return np, nil
}

// effectiveNativeQuestionTx returns a request's native prompt: the frozen
// one (binding_json.question/header) if already issued, else a fresh
// rebuild -- never writing anything, so a caller that only needs the text
// to compare against (BindNativeQuestion's per-candidate scan) never risks
// prematurely freezing a row's question from state that could still change
// before its real first issuance (locked decision 2's "Remove the per-row
// rebuild on bind" is honored by the frozen fast path; a still-unissued row
// keeps behaving exactly as before, a plain rebuild, until it really is
// issued). frozen reports whether the returned prompt came from binding_json
// (true) or was just rebuilt (false).
func (s *Store) effectiveNativeQuestionTx(ctx context.Context, tx *sql.Tx, req Request) (np NativePrompt, frozen bool, err error) {
	var binding struct {
		Question     string   `json:"question"`
		Header       string   `json:"header"`
		Options      []string `json:"options"`
		Descriptions []string `json:"descriptions"`
	}
	if len(req.Binding) > 0 {
		json.Unmarshal(req.Binding, &binding)
	}
	// A frozen question (2026-09-28-approval-summary-enforced): replay it
	// verbatim, with no DB lookup at all -- Header and Options never vary
	// once frozen either, so nothing here can be affected by state that
	// changed since the question was first issued (a spec revised, a
	// warning added or cleared, the asking agent's kind changed). This is
	// also what keeps BindNativeQuestion's match stable: the same frozen
	// text is what gets compared, not a fresh rebuild that could drift.
	if binding.Question != "" && binding.Header != "" {
		if len(binding.Options) > 0 {
			return NativePrompt{Header: binding.Header, Question: binding.Question, Options: binding.Options,
				Descriptions: binding.Descriptions}, true, nil
		}
		return NativePrompt{Header: binding.Header, Question: binding.Question, Options: approveOptions}, true, nil
	}
	title, err := s.sectionTitle(ctx, tx, req.ArtifactID, req.ArtifactRevision, req.SectionID)
	if err != nil {
		return NativePrompt{}, false, err
	}
	var warnings []string
	var reviewPaths *ReviewPaths
	if req.Kind == KindApprovePlan {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(warnings_json,'[]') FROM artifact_revisions
			WHERE artifact_id = ? AND revision = ?`, req.ArtifactID, req.ArtifactRevision).Scan(&raw); err != nil {
			return NativePrompt{}, false, err
		}
		json.Unmarshal([]byte(raw), &warnings)
		paths, _, err := s.planReviewPathsTx(ctx, tx, req.ItemID, req.ArtifactID)
		if err != nil {
			return NativePrompt{}, false, err
		}
		reviewPaths = &paths
	}
	np, err = s.nativePromptFor(ctx, tx, req, title, warnings, reviewPaths)
	return np, false, err
}

// freezeNativeQuestionTx stores the exact header and question text of a
// request's first-issued native prompt in binding_json (locked decision 2,
// 2026-09-28-approval-summary-enforced): a no-op once already frozen, so it
// never overwrites the originally issued text with a later rebuild.
// A finish prompt (Descriptions set) also freezes its options and descriptions.
func (s *Store) freezeNativeQuestionTx(ctx context.Context, tx *sql.Tx, reqID string, np NativePrompt) error {
	_, err := tx.ExecContext(ctx, `UPDATE requests SET binding_json = json_set(json_set(
		COALESCE(binding_json, '{}'), '$.question', ?), '$.header', ?)
		WHERE id = ? AND json_extract(COALESCE(binding_json, '{}'), '$.question') IS NULL`,
		np.Question, np.Header, reqID)
	if err != nil || len(np.Descriptions) == 0 {
		return err
	}
	opts, _ := json.Marshal(np.Options)
	descs, _ := json.Marshal(np.Descriptions)
	_, err = tx.ExecContext(ctx, `UPDATE requests SET binding_json = json_set(binding_json,
		'$.options', json(?), '$.descriptions', json(?))
		WHERE id = ? AND json_extract(binding_json, '$.options') IS NULL`, string(opts), string(descs), reqID)
	return err
}

// bindCandidate is one open row BindNativeQuestion considers: a rebuilt
// native-question text that matched the observed one, tagged with the ref
// it would bind to and the row's created_at for newest-wins tie-breaking.
type bindCandidate struct {
	ref       string
	createdAt int64
}

// BindNativeQuestion binds a native question tool's observed question text
// (PreToolUse, a Codex async reply, or a Muse session log entry) to an open
// approval-kind request or open child-approval message routed to agentID,
// by normalized text match (2026-09-28-approval-summary-enforced locked
// decision 2). ok is false when nothing binds -- a plain, non-approval
// question. An old prompt still carrying a ⟦swarm:ref⟧ token (a session
// launched before this deploy) binds via that token directly, without a
// text comparison, so in-flight sessions keep working. Opens its own read
// tx -- callers already inside one (e.g. askQuestion) must call
// bindNativeQuestionTx directly instead, to avoid nesting BEGIN IMMEDIATE
// transactions on the same connection pool.
// header is the observed native tool call's own header field, when the
// adapter's question shape carries one (Claude/Codex/agy/Muse all can); ""
// when unavailable. It only ever disambiguates a child-approval (msg_)
// match -- an approval-kind request's header is a fixed, kind-wide string
// ("Spike approval", "Repositories", ...) shared by every request of that
// kind, so it carries no per-request identity and is never checked there.
func (s *Store) BindNativeQuestion(ctx context.Context, agentID, header, question string) (ref string, ok bool) {
	var out string
	var found bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, found, err = s.bindNativeQuestionTx(ctx, tx, agentID, header, question)
		return err
	})
	if err != nil {
		return "", false
	}
	return out, found
}

// bindNativeQuestionTx is BindNativeQuestion's tx-scoped core: the same
// logic, running inside a caller-supplied transaction.
func (s *Store) bindNativeQuestionTx(ctx context.Context, tx *sql.Tx, agentID, header, question string) (ref string, ok bool, err error) {
	if ref := refFromPrompt(question); ref != "" {
		return ref, true, nil
	}
	norm := NormalizeQuestion(question)
	if norm == "" {
		return "", false, nil
	}
	var candidates []bindCandidate

	rows, err := tx.QueryContext(ctx, `SELECT id, created_at FROM requests
		WHERE agent_id = ? AND state = 'open'`, agentID)
	if err != nil {
		return "", false, err
	}
	type reqRow struct {
		id string
		ts int64
	}
	var reqRows []reqRow
	for rows.Next() {
		var r reqRow
		if err := rows.Scan(&r.id, &r.ts); err != nil {
			rows.Close()
			return "", false, err
		}
		reqRows = append(reqRows, r)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	rows.Close()
	for _, r := range reqRows {
		req, err := s.requestTx(ctx, tx, r.id)
		if err != nil {
			return "", false, err
		}
		if !nativeAnswerKind(req.Kind) {
			continue
		}
		// effectiveNativeQuestionTx, not storedNativePromptTx: a bind attempt
		// is a read, and must never freeze a row's question as a side effect
		// of merely being scanned as a candidate -- that could pin the wrong
		// text if the row's real first issuance (a later relay) would have
		// built it from different, still-changing state.
		np, _, err := s.effectiveNativeQuestionTx(ctx, tx, req)
		if err != nil {
			// A row whose prompt can no longer be rebuilt (e.g. a stale
			// spec lookup) is simply not a candidate -- never fails the
			// whole bind.
			continue
		}
		if NormalizeQuestion(np.Question) == norm {
			candidates = append(candidates, bindCandidate{ref: req.ID, createdAt: r.ts})
		}
	}

	msgRows, err := tx.QueryContext(ctx, `SELECT m.id, m.created_at, ag.name,
		COALESCE(json_extract(m.payload_json, '$.body'), '')
		FROM messages m JOIN agents ag ON ag.id = m.from_agent_id
		WHERE m.to_agent_id = ? AND m.kind = 'question'
		  AND json_extract(m.payload_json, '$.approval') = 1
		  AND NOT EXISTS (SELECT 1 FROM messages r WHERE r.kind IN ('approval_result', 'answer') AND r.reply_to = m.id)
		  AND NOT EXISTS (SELECT 1 FROM requests rq WHERE json_extract(rq.binding_json, '$.ref') = m.id
		      AND rq.state IN ('approved', 'changes_requested'))`, agentID)
	if err != nil {
		return "", false, err
	}
	type msgRow struct {
		id, fromName, body string
		ts                 int64
	}
	var msgRowsList []msgRow
	for msgRows.Next() {
		var r msgRow
		if err := msgRows.Scan(&r.id, &r.ts, &r.fromName, &r.body); err != nil {
			msgRows.Close()
			return "", false, err
		}
		msgRowsList = append(msgRowsList, r)
	}
	if err := msgRows.Err(); err != nil {
		return "", false, err
	}
	msgRows.Close()
	normHeader := NormalizeQuestion(header)
	for _, r := range msgRowsList {
		np := nativePromptForMsg(r.fromName, r.body, r.id)
		if NormalizeQuestion(np.Question) != norm {
			continue
		}
		// A supplied header must match this candidate's own ("<child>
		// asks") exactly -- post-review fix: without this, two children
		// with byte-identical bodies, or an orchestrator's own unrelated
		// plain question that happens to equal a child's body, could bind
		// to the wrong (or a wholly unintended) child approval. No header
		// supplied (an adapter/path that can't expose one) falls back to
		// question-only matching, same as before.
		if normHeader != "" && NormalizeQuestion(np.Header) != normHeader {
			continue
		}
		candidates = append(candidates, bindCandidate{ref: r.id, createdAt: r.ts})
	}

	if len(candidates) == 0 {
		return "", false, nil
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.createdAt > best.createdAt {
			best = c
		}
	}
	if len(candidates) > 1 {
		s.log("bind_native_question: %d candidates for agent %s, chose newest %s", len(candidates), agentID, best.ref)
	}
	return best.ref, true, nil
}

// log is a nil-safe wrapper around Store.Log, used by BindNativeQuestion's
// multiple-candidate note (locked decision 2: "log it").
func (s *Store) log(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Store) planReviewPathsTx(ctx context.Context, tx *sql.Tx, itemID, planID string) (ReviewPaths, string, error) {
	var paths ReviewPaths
	var specID string
	if err := tx.QueryRowContext(ctx, `SELECT id, path FROM artifacts WHERE item_id = ? AND kind = 'spec' ORDER BY created_at DESC, id DESC LIMIT 1`, itemID).Scan(&specID, &paths.Spec); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			var intent string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(spike_intent, '') FROM items WHERE id = ?`, itemID).Scan(&intent); err != nil {
				return paths, "", err
			}
			if intent != "chore" { // legacy chore spikes have a plan but no spec
				return paths, "", fmt.Errorf("approval_missing: register and approve the spec before asking to approve the plan")
			}
		} else {
			return paths, "", err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT path FROM artifacts WHERE id = ?`, planID).Scan(&paths.Plan); err != nil {
		return paths, "", err
	}
	var err error
	if specID != "" {
		paths.Spec, err = filepath.Abs(paths.Spec)
		if err != nil {
			return paths, "", err
		}
	}
	paths.Plan, err = filepath.Abs(paths.Plan)
	if err != nil {
		return paths, "", err
	}
	return paths, specID, nil
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
	if d := finishDecisionFor(trimmed); d != "" {
		return fmt.Sprintf(`[swarm] Recorded %q for %s. Forward it now: `+
			`swarm_ask kind:"native_answer", ref:%q, decision:%q`, trimmed, binding.Ref, binding.Ref, d)
	}
	if matched, _ := labelShape(trimmed, "Approve"); matched {
		return fmt.Sprintf(`[swarm] Recorded "Approve" for %s. Forward it now: `+
			`swarm_ask kind:"native_answer", ref:%q, decision:"approve"`, binding.Ref, binding.Ref)
	}
	if matched, _ := labelShape(trimmed, "Request changes"); matched {
		return fmt.Sprintf(`[swarm] Recorded "Request changes" for %s. Forward it now: `+
			`swarm_ask kind:"native_answer", ref:%q, decision:"request_changes"`, binding.Ref, binding.Ref)
	}
	// trimmed == "" or "Resolved in terminal" is the daemon's own placeholder
	// for an adapter with no response text (agy, spec 1.7) -- never something
	// the user typed, so it must not be quoted back as their text.
	if trimmed == "" || trimmed == ResolvedInTerminal {
		return fmt.Sprintf(`[swarm] Recorded an answer for %s. Forward it now: `+
			`swarm_ask kind:"native_answer", ref:%q, decision: the option the user picked`, binding.Ref, binding.Ref)
	}
	return fmt.Sprintf(`[swarm] Recorded %q for %s. Forward it now: `+
		`swarm_ask kind:"native_answer", ref:%q, decide approve or request_changes from the user's text %q; `+
		`if the text is neither an approval nor a change request, ask the user again instead of forwarding.`,
		trimmed, binding.Ref, binding.Ref, trimmed)
}

// errNoNativeEvidence and errDecisionMismatch are native_answer's refusals
// (spec section 2.3 step 5, section 4.1).
const errNoNativeEvidence = "No answered native prompt for %s in your terminal. Show the native_prompt " +
	"from swarm_ask verbatim with your native question tool, then forward the user's answer."
const errDecisionMismatch = "The user's native answer was %q, not %q."
const errNativeAnswerWrongTarget = "%s is not an approve_section, approve_plan, approve_report, " +
	"confirm_repos, close_spike, accept_epic or accept_fix request routed to you."

// errRequestStale is native_answer's refusal for a request the reconciler
// staled after the question was asked (reconcileRoot's binding sweep).
const errRequestStale = "%s is stale: %s changed after the question was asked. Don't forward it; " +
	"Swarm sends a new request when the work is ready again."

// nativeAnswerKind reports whether native_answer forwards a request of kind
// k: the asking agent's own approvals plus accept rows once routeAcceptTx
// has bound them to the root orchestrator (2026-09-26 epic-approval-lane).
func nativeAnswerKind(k RequestKind) bool {
	return approvalTerminalKinds[k] || k == KindAcceptEpic || k == KindAcceptFix
}

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

// nativeDecisions is native_answer's decision enum.
var nativeDecisions = map[string]bool{"approve": true, "request_changes": true,
	"auto_merge": true, "manual_merge": true, "merge_locally": true}

// finishMerge maps a finish decision to binding_json.$.merge.
var finishMerge = map[string]string{"auto_merge": "auto", "manual_merge": "manual", "merge_locally": "local"}

// finishDecisions lists a finish prompt's decisions in option order:
// 3 options → auto_merge, manual_merge, request_changes; 2 options → merge_locally, request_changes.
func finishDecisions(opts []string) []string {
	if len(opts) == 2 {
		return []string{"merge_locally", "request_changes"}
	}
	return []string{"auto_merge", "manual_merge", "request_changes"}
}

// decisionLabels returns the chosen decision's option label and every other label. A msg_ ref and
// every non-accept kind keep approve → "Approve" (others ["Request changes"]) and
// request_changes → "Request changes" (others ["Approve"]).
func decisionLabels(req Request, np NativePrompt, decision string) (string, []string, error) {
	if req.ID == "" || (req.Kind != KindAcceptEpic && req.Kind != KindAcceptFix) {
		switch decision {
		case "approve":
			return "Approve", []string{"Request changes"}, nil
		case "request_changes":
			return "Request changes", []string{"Approve"}, nil
		}
		return "", nil, errors.New("decision must be approve or request_changes.")
	}
	ds := finishDecisions(np.Options)
	if len(np.Descriptions) == 0 {
		// A pre-0022 frozen Approve/Request changes prompt: its Approve is refused (locked decision 11).
		ds = []string{"request_changes"}
		if decision == "request_changes" {
			return "Request changes", []string{"Approve"}, nil
		}
	}
	i := slices.Index(ds, decision)
	if i < 0 || i >= len(np.Options) {
		return "", nil, fmt.Errorf("decision for a finish request must be one of: %s.", strings.Join(ds, ", "))
	}
	others := slices.Delete(slices.Clone(np.Options), i, i+1)
	return np.Options[i], others, nil
}

// finishDecisionFor maps a native answer's text to a finish decision ("" when none).
func finishDecisionFor(trimmed string) string {
	if ok, _ := labelShape(trimmed, "Create PR, auto-merge when checks pass"); ok {
		return "auto_merge"
	}
	if ok, _ := labelShape(trimmed, "Create PR, I'll merge it myself"); ok {
		return "manual_merge"
	}
	fold := strings.ToLower(trimmed)
	head, _, _ := strings.Cut(fold, ":")
	if strings.HasPrefix(fold, "merge into ") && strings.HasSuffix(head, " locally") {
		return "merge_locally"
	}
	return ""
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
func matchDecisionEvidence(responseText, label string, others []string, callerComment string) (evidence, comment string, err error) {
	trimmed := strings.TrimSpace(responseText)
	if matched, remainder := labelShape(trimmed, label); matched {
		comment = callerComment
		if comment == "" {
			comment = remainder
		}
		return EvidenceObserved, comment, nil
	}
	for _, other := range others {
		if matched, _ := labelShape(trimmed, other); matched {
			return "", "", fmt.Errorf(errDecisionMismatch, trimmed, label)
		}
	}
	comment = callerComment
	if comment == "" && trimmed != "" && trimmed != ResolvedInTerminal {
		comment = trimmed
	}
	return EvidenceAgentReported, comment, nil
}

// observedAnswerer is implemented by Muse's adapter (Muse.ObservedAnswer):
// nativeAnswer's Muse branch checks the provider's own session log for a
// settled answer before falling back to the agent's self-report (spec
// docs/specs/2026-09-28-muse-observed-answers.md). Declared as an interface,
// not a concrete adapter.Muse type, purely to keep this package's tests
// able to fake it without an adapter import cycle risk. question is the
// exact native-question text ObservedAnswer's Muse session-log scan matches
// against, normalized (2026-09-28-approval-summary-enforced locked decision
// 2) -- the caller builds it from the same stored-prompt rebuild the hook
// binding path uses, so an old logged question still carrying a ⟦swarm:
// ref⟧ token matches via the fallback the adapter keeps for it.
type observedAnswerer interface {
	ObservedAnswer(providerSessionID, ref, question string, since time.Time) (label, note string, ok bool)
}

// questionTextForRef rebuilds the exact native-question text a ref's
// request or child-approval message would show right now -- the same text
// Muse's ObservedAnswer session-log scan needs to match against (locked
// decision 2). "" (with a nil error) when the ref names nothing rebuildable;
// the caller's own RequestByID/verifyApprovalMsgAddressedTo lookups still
// refuse an invalid ref properly afterward.
func (s *Store) questionTextForRef(ctx context.Context, ref string) (string, error) {
	if strings.HasPrefix(ref, "msg_") {
		var fromName, body string
		err := s.DB.QueryRowContext(ctx, `SELECT ag.name, COALESCE(json_extract(m.payload_json, '$.body'), '')
			FROM messages m JOIN agents ag ON ag.id = m.from_agent_id WHERE m.id = ?`, ref).Scan(&fromName, &body)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return nativePromptForMsg(fromName, body, ref).Question, nil
	}
	var question string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		req, err := s.requestTx(ctx, tx, ref)
		if err != nil {
			return err
		}
		np, err := s.storedNativePromptTx(ctx, tx, req)
		if err != nil {
			return err
		}
		question = np.Question
		return nil
	})
	if err != nil {
		return "", nil // an unrebuildable/unknown ref: nothing to match on, not this function's refusal to make
	}
	return question, nil
}

// refCreatedAt is the anti-forgery lower bound nativeAnswer passes into
// ObservedAnswer: the ref's own row's created_at, so a settled prompt from
// before this ref ever existed (a stale or replayed session.jsonl entry
// that happens to embed the same ref text) can never be treated as its
// evidence. Any lookup failure (bad ref, wrong table) returns the zero
// time, i.e. no lower bound -- the ref is invalid either way and the
// existing lookups a few lines below (RequestByID, verifyApprovalMsgAddressedTo)
// are what actually refuse it with a proper message.
func (s *Store) refCreatedAt(ctx context.Context, ref string) time.Time {
	table := "requests"
	if strings.HasPrefix(ref, "msg_") {
		table = "messages"
	}
	var ms int64
	if err := s.DB.QueryRowContext(ctx, `SELECT created_at FROM `+table+` WHERE id = ?`, ref).Scan(&ms); err != nil {
		return time.Time{}
	}
	return db.FromMillis(ms)
}

// nativeAnswer is swarm_ask kind:"native_answer" (spec section 2.3 steps
// 4-6, Task 13c): it forwards the orchestrator's observed decision for a
// request ref into a real Approve/RequestChanges/ConfirmRepos, but only
// once it has verified a matching native-question row (Task 13b's binding)
// really was answered in that agent's own terminal.
func (s *Store) nativeAnswer(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if !nativeDecisions[in.Decision] {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "decision must be approve or request_changes."}
	}
	if in.Ref == "" {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "ref is required."}
	}
	// Muse's observed-answer check runs here, before any tx: it is a file
	// scan (Muse's own session.jsonl), not a DB read, and must never run
	// inside the state-changing tx below (I2). A settled answer straight
	// from Muse's own session log is observed evidence, not the agent's
	// word (locked decision); no match (no adapter wired, unknown provider
	// session, no settled prompt for this ref) falls through unchanged to
	// the reported path inside the tx.
	var observedResponseText, observedSource string
	var callerKind AgentKind
	if err := s.DB.QueryRowContext(ctx, `SELECT a.kind FROM sessions ses
		JOIN agents a ON a.id = ses.agent_id WHERE ses.id = ?`, sessionID).Scan(&callerKind); err == nil && callerKind == Muse {
		var providerSessionID string
		if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(provider_session_id,'') FROM sessions WHERE id = ?`,
			sessionID).Scan(&providerSessionID); err == nil {
			if oa, ok := s.Adapters[Muse].(observedAnswerer); ok {
				since := s.refCreatedAt(ctx, in.Ref)
				question, _ := s.questionTextForRef(ctx, in.Ref)
				if lbl, note, found := oa.ObservedAnswer(providerSessionID, in.Ref, question, since); found {
					observedResponseText = lbl
					if note != "" {
						observedResponseText = lbl + ": " + note
					}
					observedSource = "muse_session_log"
				}
			}
		}
	}

	var rowID, responseText, callerID, observedSourceUsed string
	var reported bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		callerID = a.ID
		if a.Kind == Muse && observedSource != "" {
			responseText = observedResponseText
			observedSourceUsed = observedSource
			return nil
		}
		reported = a.Kind == Cursor || a.Kind == Muse
		if reported {
			if strings.TrimSpace(in.AnswerText) == "" || in.AnswerText == ResolvedInTerminal {
				return &items.Error{Code: items.CodeBadRequest, Message: "answer_text must contain the exact nonempty native tool answer; a cancelled question cannot be submitted."}
			}
			responseText = in.AnswerText
			return nil
		}
		if in.AnswerText != "" {
			return &items.Error{Code: items.CodeBadRequest, Message: "answer_text is only for Cursor and Muse."}
		}
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
	callerComment := in.Comment
	if reported {
		callerComment = "" // the reported tool answer is the only source of user text
	}
	var evidence, comment string
	// classify matches the answer against the decision's label once the request (and so its
	// prompt's labels) is known: right away for a msg_ ref, after the target checks for a req_ ref.
	classify := func(req Request, np NativePrompt) error {
		label, others, err := decisionLabels(req, np, in.Decision)
		if err != nil {
			return &items.Error{Code: items.CodeBadRequest, Message: err.Error()}
		}
		evidence, comment, err = matchDecisionEvidence(responseText, label, others, callerComment)
		if err != nil {
			return &items.Error{Code: items.CodeBadRequest, Message: err.Error()}
		}
		if reported {
			if in.Comment != "" && in.Comment != comment {
				return &items.Error{Code: items.CodeBadRequest, Message: "comment does not match answer_text."}
			}
			evidence = EvidenceAgentReported
		}
		// RequestChanges' own length cap, reapplied here (Task 13c): nativeAnswer
		// builds the changes_requested result with s.resolve directly (so the
		// payload can carry evidence), not through RequestChanges itself. A
		// comment is optional (spec 1.8 D1): the old "Request changes needs a
		// comment" refusal is removed -- keep it simple, not too tight.
		if in.Decision == "request_changes" && utf8.RuneCountInString(comment) > 2000 {
			return &items.Error{Code: items.CodeBadRequest, Message: "Comment must be at most 2000 characters."}
		}
		return nil
	}
	// bindEvidence runs inside the same tx as the state change (resolve's or
	// ConfirmRepos's own), right after the UPDATE: the audit record's (a)
	// (spec 2.3.6) lands atomically with (b), the result message's payload.
	bindEvidence := func(tx *sql.Tx, _ Request) error {
		if reported {
			_, err := tx.ExecContext(ctx, `UPDATE requests SET binding_json = json_set(COALESCE(binding_json, '{}'),
				'$.evidence', ?, '$.answer_text', ?, '$.answer_source', 'native_tool_report') WHERE id = ?`,
				evidence, in.AnswerText, in.Ref)
			return err
		}
		if observedSourceUsed != "" {
			// answer_text is only recorded when the agent actually sent one
			// (spec Types section): the log itself is the evidence either way.
			if in.AnswerText != "" {
				_, err := tx.ExecContext(ctx, `UPDATE requests SET binding_json = json_set(COALESCE(binding_json, '{}'),
					'$.evidence', ?, '$.answer_source', ?, '$.answer_text', ?) WHERE id = ?`,
					evidence, observedSourceUsed, in.AnswerText, in.Ref)
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE requests SET binding_json = json_set(COALESCE(binding_json, '{}'),
				'$.evidence', ?, '$.answer_source', ?) WHERE id = ?`,
				evidence, observedSourceUsed, in.Ref)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE requests SET
			binding_json = json_set(COALESCE(binding_json, '{}'), '$.evidence', ?) WHERE id = ?`,
			evidence, rowID)
		return err
	}

	if strings.HasPrefix(in.Ref, "msg_") {
		if err := classify(Request{}, NativePrompt{}); err != nil {
			return Request{}, err
		}
		// The message ref must name an approval question addressed to the
		// caller (spec 2.3, Task 13b): reuse askNativePromptForMsg's own
		// query rather than trusting the evidence row's binding, which any
		// agent can forge locally by pointing its own AskQuestion at someone
		// else's message id.
		if err := s.verifyApprovalMsgAddressedTo(ctx, in.Ref, callerID); err != nil {
			return Request{}, err
		}
		// A Muse observed match has no pre-existing bound question row to
		// transition (unlike the hook path) and its answer_text may be empty
		// (the agent need not repeat what the log already proved), so the
		// log's own responseText -- never empty once observedSourceUsed is set --
		// stands in for it, tagged with the matching answer_source.
		msgAnswerText, answerSource := in.AnswerText, "native_tool_report"
		if observedSourceUsed != "" {
			msgAnswerText, answerSource = responseText, observedSourceUsed
		}
		return s.nativeAnswerForMsg(ctx, sessionID, callerID, in.Ref, in.Decision, comment, evidence, rowID, msgAnswerText, answerSource, bindEvidence)
	}

	req, err := s.RequestByID(ctx, in.Ref)
	if err != nil {
		// Unknown ref: an observed mismatch against the approve pair still wins.
		if cerr := classify(Request{}, NativePrompt{}); cerr != nil {
			return Request{}, cerr
		}
		return Request{}, err
	}
	// native_answer only forwards approval kinds (nativeAnswerKind), and only
	// for the agent the request is routed to -- never a plain HITL question
	// or another agent's request, which req.Kind and req.AgentID alone can't
	// otherwise be trusted to exclude once an evidence row exists (a caller
	// can forge its own locally, Task B4 finding 3). An accept row is routed
	// to the root orchestrator by routeAcceptTx, so the same owner check
	// covers it.
	if !nativeAnswerKind(req.Kind) || req.AgentID != callerID {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(errNativeAnswerWrongTarget, in.Ref)}
	}
	// reconcileRoot stales an open accept row in the same tx as any revision
	// bump or new integration: say so instead of resolve's bare
	// "Already resolved.". The binding itself is read from the stored row
	// below (locked decision 5).
	if req.State == "stale" {
		var key string
		if err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, req.ItemID).Scan(&key); err != nil {
			return Request{}, err
		}
		return Request{}, &items.Error{Code: items.CodeConflict, Message: fmt.Sprintf(errRequestStale, in.Ref, key)}
	}
	var np NativePrompt
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		np, _, err = s.effectiveNativeQuestionTx(ctx, tx, req)
		return err
	}); err != nil {
		return Request{}, err
	}
	if err := classify(req, np); err != nil {
		return Request{}, err
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
	if m := finishMerge[in.Decision]; m != "" {
		return s.resolve(ctx, in.Ref, "approved", comment, "terminal", "user_action", approveCheck(in2),
			func(req Request) (MessageKind, any) {
				return "approval_result", map[string]any{"decision": "approved", "merge": m,
					"section_id": "", "section_sha256": "", "evidence": evidence}
			}, bindEvidence, setMergeHook(ctx, in.Ref, m))
	}
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
func (s *Store) nativeAnswerForMsg(ctx context.Context, sessionID, callerID, msgID, decision, comment, evidence, rowID, answerText, answerSource string,
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
			WHERE kind IN ('approval_result', 'answer') AND reply_to = ? LIMIT 1`, msgID).Scan(&x)
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
		if answerText != "" {
			var itemID, body, fromName string
			if err := tx.QueryRowContext(ctx, `SELECT m.item_id, json_extract(m.payload_json, '$.body'), a.name
				FROM messages m JOIN agents a ON a.id = m.from_agent_id WHERE m.id = ?`, msgID).
				Scan(&itemID, &body, &fromName); err != nil {
				return err
			}
			rowID = ids.New("req")
			binding, err := json.Marshal(map[string]string{"ref": msgID, "evidence": evidence,
				"answer_text": answerText, "answer_source": answerSource})
			if err != nil {
				return err
			}
			prompt := nativePromptForMsg(fromName, body, msgID)
			if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
				prompt, options_json, state, binding_json, response_text, responded_via, responded_at, created_at)
				VALUES (?, 'question', 1, ?, ?, ?, ?, ?, 'answered', ?, ?, 'terminal', ?, ?)`, rowID, callerID,
				sessionID, itemID, prompt.Question, jsonArray(prompt.Options), string(binding), answerText,
				db.Millis(s.Now()), db.Millis(s.Now())); err != nil {
				return err
			}
		}

		res, err := tx.ExecContext(ctx, `UPDATE requests SET state = ? WHERE id = ? AND state = 'answered'`,
			newState, rowID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		if answerText == "" {
			if err := bindEvidence(tx, Request{}); err != nil {
				return err
			}
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
		if err := s.retirePrintRelaysTx(ctx, tx, msgID); err != nil {
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
// A for_msg naming the caller's own open request (TASK-356) recovers its
// stored native prompt instead: a request_ask relay acked before its prompt
// was read is otherwise unrecoverable, since the print phase is terminal.
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
		if !isMsgRef(in.ForMsg) {
			return s.ownRequestPromptTx(ctx, tx, a, in.ForMsg, &out)
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
		out = Request{ID: in.ForMsg, State: "open", NativePrompt: &np,
			ChatBlock: ApprovalChatBlock(ChatBlockInput{Child: fromName, Summary: body})}
		return s.startPrintTx(ctx, tx, in.ForMsg)
	})
	return out, err
}

// ownRequestPromptTx recovers the stored native prompt of the caller's own
// open request, leaving the print phase and relays untouched: no print
// restart, no new relay, no phase write. A request owned by another agent
// or no longer open is refused with a clear error.
func (s *Store) ownRequestPromptTx(ctx context.Context, tx *sql.Tx, a Agent, reqID string, out *Request) error {
	req, err := s.requestTx(ctx, tx, reqID)
	if err != nil {
		return err
	}
	if req.AgentID != a.ID {
		return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("request %s is not addressed to you.", reqID)}
	}
	if req.State != "open" {
		return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("request %s is not open (state=%s).", reqID, req.State)}
	}
	np, err := s.storedNativePromptTx(ctx, tx, req)
	if err != nil {
		return err
	}
	*out = Request{ID: req.ID, Kind: req.Kind, State: req.State, NativePrompt: &np}
	return nil
}

// nativePromptForMsg is the child-approval native prompt (spec section 2.4,
// section 6's "child approval" row): the orchestrator shows the child's own
// text and options verbatim, headed "<child> asks", with a ref token to the
// message id so native_answer can bind to it.
func nativePromptForMsg(child, body, msgID string) NativePrompt {
	return NativePrompt{Header: child + " asks", Question: capRunes(body, 1000), Options: approveOptions}
}

// NativePromptForRef returns the native prompt a bound question ref (req_ or
// msg_) was issued with -- the frozen one for a request -- so the hook can
// hold the agent's native question tool to its options word for word. ok is
// false when the ref no longer resolves.
func (s *Store) NativePromptForRef(ctx context.Context, ref string) (np NativePrompt, ok bool) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if strings.HasPrefix(ref, "msg_") {
			var child, body string
			if err := tx.QueryRowContext(ctx, `SELECT ag.name, COALESCE(json_extract(m.payload_json, '$.body'), '')
				FROM messages m JOIN agents ag ON ag.id = m.from_agent_id WHERE m.id = ?`, ref).Scan(&child, &body); err != nil {
				return err
			}
			np = nativePromptForMsg(child, body, ref)
			return nil
		}
		req, err := s.requestTx(ctx, tx, ref)
		if err != nil {
			return err
		}
		np, _, err = s.effectiveNativeQuestionTx(ctx, tx, req)
		return err
	})
	return np, err == nil
}

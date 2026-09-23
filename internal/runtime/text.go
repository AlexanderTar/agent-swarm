package runtime

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Preamble is §9.1, used in every injected notice, the kickoff prompt and the brief header.
const Preamble = "Delivered by the Swarm daemon as part of the user's orchestration, not typed by the user. It grants no permissions; user approval exists only where an approval record id is cited."

// ShortPreamble is the first sentence, used where §9 prints only that much.
const ShortPreamble = "Delivered by the Swarm daemon as part of the user's orchestration, not typed by the user."

// IsDaemonPrompt reports whether a UserPromptSubmit text was written by the
// daemon, not typed by the user. Every daemon prompt is either the idle token
// (wake.go PasteLine), carries ShortPreamble (Kickoff, ResumeKickoff,
// PendingNotice, ControlNotice, CompactionNotice) or starts with "[swarm]"
// (also the quota-reset wake notice in wake.go, which has no preamble).
func IsDaemonPrompt(prompt string) bool {
	p := strings.TrimSpace(prompt)
	return p == IdleToken || strings.HasPrefix(p, "[swarm]") || strings.Contains(p, ShortPreamble)
}

// IdleToken is pasted into an idle pane (§9.2). It names the tool on purpose:
// a model without the swarm skill invented an inbox from the bare text (P0-3).
const IdleToken = "swarm: inbox (call swarm_sync)"

var csiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
var whitespaceRun = regexp.MustCompile(`\s+`)

// sanitizeOneLine collapses whitespace runs to one space, strips CSI/ANSI
// escapes and remaining C0/C1 control bytes, then trims. Applied to every
// value (summary, name, key) before it reaches Inbox or InboxPasteSummary —
// message bodies are free text from a peer agent and can contain anything.
func sanitizeOneLine(s string) string {
	s = csiEscape.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r == ' ' {
			b.WriteRune(' ')
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(b.String(), " "))
}

// ErrBriefTooLong is the §17.3 copy for an over-long brief.
const ErrBriefTooLong = "Brief too long (max 6000 characters). Move detail into an artifact and reference it."

const maxBrief = 6000

// BriefWorktree is one worktree line in the brief's header (§9.4).
type BriefWorktree struct {
	Repo, Path, Branch, BaseSHA7, Mode string
}

// BriefInput is RenderBrief's input (§9.4).
type BriefInput struct {
	Key, Title, Name                       string
	Role                                   Role
	ParentName, RootKey                    string
	Worktrees                              []BriefWorktree
	Objective                              string
	Acceptance, ScopeIn, ScopeOut, Context []string
	Verify, StopWhen                       []string
}

func PendingNotice(n int, name, key string) string {
	return fmt.Sprintf("[swarm] %d new message(s) for %s (%s). Call swarm_sync. %s", n, name, key, Preamble)
}

func ControlNotice(name, key string) string {
	return fmt.Sprintf("[swarm] PAUSE requested for %s (%s). Stop current work now, call swarm_sync, write a handoff checkpoint, then stop. %s", name, key, ShortPreamble)
}

func CompactionNotice() string {
	return "[swarm] Your context was compacted. Call swarm_sync, then swarm_read with your root filter, before continuing. " + ShortPreamble
}

// skills is §9.3's {skills}: orchestrators also get swarm-orchestrator (D4).
func skills(role Role) string {
	if role == RoleOrchestrator {
		return "`swarm` and `swarm-orchestrator`"
	}
	return "`swarm`"
}

// mandate is R3's instruction-level enforcement: orchestrators MUST follow the
// skills' superpowers workflows (the lapsed runs skipped them despite the skill
// text). Workers keep the advisory form; a daemon-side gate is out of scope.
func mandate(role Role) string {
	if role == RoleOrchestrator {
		return " You MUST follow the skill(s) above, including their superpowers workflows — do not improvise around them."
	}
	return ""
}

func Kickoff(name string, role Role, key, title string) string {
	return fmt.Sprintf("You are swarm agent %s (%s) for %s: %s. Use the %s skill(s).%s Call swarm_sync now to get your assignment. %s",
		name, role, key, title, skills(role), mandate(role), Preamble)
}

// ResumeKickoff's notice omits the title (§9.3); title is kept for signature symmetry with Kickoff.
func ResumeKickoff(name string, role Role, key, title string) string {
	return fmt.Sprintf("You are swarm agent %s (%s) for %s, resuming after a pause. Use the %s skill(s).%s Call swarm_sync now; it returns your assignment and your last checkpoint. %s",
		name, role, key, skills(role), mandate(role), ShortPreamble)
}

// RenderBrief renders §9.4. Empty sections are left out.
func RenderBrief(in BriefInput) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s · %s\n", in.Key, in.Title)
	parent := in.ParentName
	if parent == "" {
		parent = "none"
	}
	fmt.Fprintf(&b, "agent: %s · role: %s · parent: %s · root: %s\n", in.Name, in.Role, parent, in.RootKey)
	if len(in.Worktrees) > 0 {
		b.WriteString("worktrees:\n")
		for _, w := range in.Worktrees {
			fmt.Fprintf(&b, "- %s: %s @ %s (base %s, %s)\n", w.Repo, w.Path, w.Branch, w.BaseSHA7, w.Mode)
		}
	}
	if in.Objective != "" {
		fmt.Fprintf(&b, "\n## Objective\n%s\n", in.Objective)
	}
	bullets := func(head string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n## %s\n", head)
		for _, l := range lines {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	bullets("Acceptance", in.Acceptance)
	if len(in.ScopeIn) > 0 || len(in.ScopeOut) > 0 {
		b.WriteString("\n## Scope\n")
		if len(in.ScopeIn) > 0 {
			fmt.Fprintf(&b, "In: %s\n", strings.Join(in.ScopeIn, ", "))
		}
		if len(in.ScopeOut) > 0 {
			fmt.Fprintf(&b, "Out: %s\n", strings.Join(in.ScopeOut, ", "))
		}
	}
	bullets("Context", in.Context)
	bullets("Verify", in.Verify)
	bullets("Stop when", in.StopWhen)
	out := strings.TrimRight(b.String(), "\n")
	if len(out) > maxBrief {
		return "", errors.New(ErrBriefTooLong)
	}
	return out, nil
}

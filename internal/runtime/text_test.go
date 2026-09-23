package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "text", name+".golden"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNoticesMatchGoldens(t *testing.T) {
	cases := []struct {
		name, got string
	}{
		{"pending", PendingNotice(3, "login-form-coder", "TASK-101")},
		{"control", ControlNotice("login-form-coder", "TASK-101")},
		{"compaction", CompactionNotice()},
		{"kickoff-worker", Kickoff("login-form-coder", RoleCoder, "TASK-101", "Build the login form")},
		{"kickoff-orchestrator", Kickoff("auth-epic-orchestrator", RoleOrchestrator, "EPIC-12", "Ship auth")},
		{"resume", ResumeKickoff("login-form-coder", RoleCoder, "TASK-101", "Build the login form")},
	}
	for _, c := range cases {
		if w := golden(t, c.name); c.got != w {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, w)
		}
	}
}

// §9.2: "Notices never ask for an exact reply phrase" (P0-3: an advisor called
// 'reply with X' texts classic injection signatures).
func TestNoticesNeverAskForAReplyPhrase(t *testing.T) {
	banned := regexp.MustCompile(`(?i)reply with|respond with|say exactly|answer with the (exact|word)|the exact phrase`)
	all := []string{
		PendingNotice(1, "a", "TASK-1"), ControlNotice("a", "TASK-1"), CompactionNotice(),
		Kickoff("a", RoleCoder, "TASK-1", "t"), ResumeKickoff("a", RoleCoder, "TASK-1", "t"), IdleToken,
	}
	for _, s := range all {
		if banned.MatchString(s) {
			t.Errorf("notice asks for an exact reply: %q", s)
		}
	}
}

// Every injected string names its sender, so a model can tell it from a user turn.
func TestEveryNoticeCarriesThePreamble(t *testing.T) {
	for _, s := range []string{
		PendingNotice(1, "a", "TASK-1"), ControlNotice("a", "TASK-1"), CompactionNotice(),
		Kickoff("a", RoleCoder, "TASK-1", "t"), ResumeKickoff("a", RoleCoder, "TASK-1", "t"),
	} {
		if !strings.Contains(s, ShortPreamble) {
			t.Errorf("notice without the preamble: %q", s)
		}
	}
}

// R3 (superpowers enforcement): orchestrator kickoffs carry a MUST-level skills
// mandate — the lapsed spec-less runs skipped the skill's superpowers workflows
// despite the skill text. Workers keep the advisory form; their flow differs.
func TestOrchestratorKickoffMandatesSkillWorkflows(t *testing.T) {
	got := Kickoff("a", RoleOrchestrator, "EPIC-1", "T")
	if !strings.Contains(got, "MUST follow") || !strings.Contains(got, "superpowers") {
		t.Errorf("orchestrator kickoff lacks the skills mandate: %q", got)
	}
	if got := Kickoff("a", RoleCoder, "TASK-1", "t"); strings.Contains(got, "MUST follow") {
		t.Errorf("worker kickoff must not carry the orchestrator mandate: %q", got)
	}
	if got := ResumeKickoff("a", RoleOrchestrator, "EPIC-1", "T"); !strings.Contains(got, "MUST follow") {
		t.Errorf("orchestrator resume lacks the skills mandate: %q", got)
	}
}

// §9.2: the token names the tool, because a model without the skill invented an inbox (P0-3).
func TestIdleTokenNamesTheTool(t *testing.T) {
	if IdleToken != "swarm: inbox (call swarm_sync)" {
		t.Fatalf("IdleToken = %q", IdleToken)
	}
}

func TestRenderBriefMatchesTheTemplate(t *testing.T) {
	got, err := RenderBrief(BriefInput{
		Key: "TASK-101", Title: "Build the login form", Name: "login-form-coder",
		Role: RoleCoder, ParentName: "auth-epic-orchestrator", RootKey: "EPIC-12",
		Worktrees: []BriefWorktree{{Repo: "agent-swarm",
			Path:   "/Users/u/GitHub/agent-swarm--task-101-login-form",
			Branch: "task/task-101-login-form", BaseSHA7: "1a2b3c4", Mode: "rw"}},
		Objective:  "Add the login form and wire it to the session endpoint.",
		Acceptance: []string{"The form validates an empty email.", "A successful login redirects to /home."},
		ScopeIn:    []string{"web/src/views/Login.tsx"},
		ScopeOut:   []string{"the session endpoint itself"},
		Context:    []string{"art_01J9Z: the approved spec", "TASK-98: session cookie helper landed"},
		Verify:     []string{"pnpm test web/src/views/Login.test.tsx"},
		StopWhen:   []string{"The task is marked done by the orchestrator."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := golden(t, "brief-worker"); got != w {
		t.Errorf("brief:\n%s\n--- want ---\n%s", got, w)
	}
}

func TestRenderBriefRefusesOverLongBriefs(t *testing.T) {
	_, err := RenderBrief(BriefInput{Key: "TASK-1", Title: "t", Name: "a", Role: RoleCoder,
		RootKey: "TASK-1", Objective: strings.Repeat("x", 6100)})
	if err == nil || err.Error() != "Brief too long (max 6000 characters). Move detail into an artifact and reference it." {
		t.Fatalf("err = %v", err)
	}
}

// An empty section is left out rather than printed with an empty bullet.
func TestRenderBriefOmitsEmptySections(t *testing.T) {
	got, err := RenderBrief(BriefInput{Key: "TASK-1", Title: "t", Name: "a", Role: RoleCoder,
		RootKey: "TASK-1", Objective: "do it", StopWhen: []string{"done"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"## Acceptance", "## Scope", "## Context", "## Verify", "worktrees:"} {
		if strings.Contains(got, h) {
			t.Errorf("empty section %q was printed:\n%s", h, got)
		}
	}
	if !strings.Contains(got, "## Stop when\n- done") {
		t.Errorf("missing stop-when section:\n%s", got)
	}
}

func TestIsDaemonPrompt(t *testing.T) {
	for _, p := range []string{
		IdleToken, " " + IdleToken + "\n",
		Kickoff("a", RoleOrchestrator, "EPIC-1", "T"), ResumeKickoff("a", RoleOrchestrator, "EPIC-1", "T"),
		PendingNotice(2, "a", "EPIC-1"), ControlNotice("a", "EPIC-1"), CompactionNotice(),
		"[swarm] Quota reset window passed. Resuming.", // wake.go quota notice: no preamble
		"typed by a human, merged with " + IdleToken + " " + ShortPreamble,
	} {
		if !IsDaemonPrompt(p) {
			t.Errorf("IsDaemonPrompt(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"Use zod", "yes", "  the second one  ", "swarm: what is inbox?", ""} {
		if IsDaemonPrompt(p) {
			t.Errorf("IsDaemonPrompt(%q) = true, want false", p)
		}
	}
}

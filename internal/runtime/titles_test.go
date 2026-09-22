package runtime

import "testing"

func TestSessionTitleStatusAndFallbackEmoji(t *testing.T) {
	running := sessionTitle(false, RoleCoder, "root-1", "login-form-coder")
	waiting := sessionTitle(true, RoleCoder, "root-1", "login-form-coder")
	if running == waiting {
		t.Fatalf("status emoji should differ: %q vs %q", running, waiting)
	}
	if got := sessionTitle(false, Role("made-up"), "root-1", "x"); got[0] == 0 {
		t.Fatalf("unknown role should still produce a title, got %q", got)
	}
}

// Terminals.swift matches a tab title that "ends with" a single space plus
// the exact tmux/agent name (the same boundary the old "swarm:" prefix gave
// it) -- so the name must always be the last token, right after one space.
func TestSessionTitleEndsWithASpaceThenTheExactName(t *testing.T) {
	title := sessionTitle(true, RoleOrchestrator, "root-1", "login-form-orchestrator")
	const suffix = " login-form-orchestrator"
	if len(title) < len(suffix) || title[len(title)-len(suffix):] != suffix {
		t.Fatalf("title %q does not end with %q", title, suffix)
	}
	// A sibling whose name is a suffix of another name must not false-match.
	sibling := sessionTitle(true, RoleOrchestrator, "root-1", "relogin-form-orchestrator")
	const target = " login-form-orchestrator"
	if len(sibling) >= len(target) && sibling[len(sibling)-len(target):] == target {
		t.Fatalf("sibling title %q must not end with %q", sibling, target)
	}
}

func TestGroupCircleIsDeterministicAndVaries(t *testing.T) {
	a1 := groupCircle("root-1")
	a2 := groupCircle("root-1")
	if a1 != a2 {
		t.Fatalf("groupCircle must be stable for the same id: %q vs %q", a1, a2)
	}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		seen[groupCircle(string(rune('a'+i)))] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected groupCircle to spread across the palette, got %v", seen)
	}
}

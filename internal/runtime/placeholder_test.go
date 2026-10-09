package runtime

import "testing"

func TestPlaceholderTitle(t *testing.T) {
	cases := []struct {
		name, request, want string
	}{
		{"cuts at a word boundary under 60 runes", "Fix the login redirect loop that happens after SSO sign-in on Safari",
			"Fix the login redirect loop that happens after SSO sign-in…"},
		{"first non-blank line, whitespace collapsed", "\n  \n  Fix   the   bug  \nmore detail", "Fix the bug"},
		{"short request is returned as-is", "Fix the bug", "Fix the bug"},
		{"single word over 60 runes hard-cuts at 59", strRepeat("a", 70), strRepeat("a", 59) + "…"},
		{"blank request", "   \n  ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := placeholderTitle(c.request)
			if got != c.want {
				t.Errorf("placeholderTitle(%q) = %q, want %q", c.request, got, c.want)
			}
		})
	}
}

func strRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestPlaceholderAgentName(t *testing.T) {
	cases := []struct {
		name, title, want string
	}{
		{"first four words kebabed plus role", "Fix the login redirect loop that happens after SSO sign-in…", "fix-the-login-redirect-orchestrator"},
		{"short title uses every word", "Fix bug", "fix-bug-orchestrator"},
		{"long words cap the slug at 24 like defaultName", "Internationalization reconfiguration decentralization incomprehensibilities",
			"internationalization-orchestrator"},
		{"empty title falls back to orchestrator", "", "orchestrator"},
		{"unkebabable title falls back to orchestrator", "!!! ???", "orchestrator"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := placeholderAgentName(c.title)
			if got != c.want {
				t.Errorf("placeholderAgentName(%q) = %q, want %q", c.title, got, c.want)
			}
		})
	}
}

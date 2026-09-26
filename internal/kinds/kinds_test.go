package kinds

import "testing"

func TestAgentKindDisplay(t *testing.T) {
	want := map[AgentKind]string{
		Claude: "Claude", Codex: "Codex", Agy: "agy", Cursor: "Cursor", Muse: "Muse", Fake: "Fake",
	}
	for k, w := range want {
		if got := k.Display(); got != w {
			t.Errorf("%s.Display() = %q, want %q", k, got, w)
		}
	}
}

// An empty kind is not the same as Fake: a caller that never resolved a kind
// (e.g. a spike with no default) must not have its error message claim "Fake"
// is what failed.
func TestAgentKindDisplayOfEmptyKindIsNotFake(t *testing.T) {
	if got := AgentKind("").Display(); got == "Fake" {
		t.Errorf(`AgentKind("").Display() = %q, must not be "Fake"`, got)
	}
}

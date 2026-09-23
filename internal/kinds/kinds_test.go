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

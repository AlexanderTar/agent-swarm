package advisor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func TestBuildContextHasEverySectionAndTheQuestionLast(t *testing.T) {
	out := BuildContext(ContextInput{
		Brief:       "# TASK-101 · Build the login form\n\n## Objective\nAdd the form.",
		Checkpoints: []runtime.Checkpoint{{Kind: "progress", Summary: "red test written"}},
		Requests:    []runtime.Request{{Kind: "question", Prompt: "Keep the email?"}},
		Question:    "Should the retry live in the client or the server?",
		Focus:       []string{"/w/web/src/views/Login.tsx", "art_01J9Z"},
		Transcript:  []Turn{{Role: "user", Text: "start"}, {Role: "assistant", Text: "on it"}},
	})
	for _, want := range []string{"## Brief", "## Recent checkpoints", "## Open requests",
		"## Files to read", "## Transcript", "## Question"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing section %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "/w/web/src/views/Login.tsx") || !strings.Contains(out, "art_01J9Z") {
		t.Error("focus paths are listed, not inlined")
	}
	qi := strings.Index(out, "## Question")
	if qi < strings.Index(out, "## Transcript") {
		t.Error("the question comes last: the prompt tells the advisor to answer the question at the end")
	}
}

// §11.6: at most 3 checkpoints, 30 turns, tool output cut to 300 characters.
func TestBuildContextTruncatesToolOutput(t *testing.T) {
	out := BuildContext(ContextInput{Question: "q",
		Transcript: []Turn{{Role: "tool", Tool: "Bash", Text: strings.Repeat("x", 900)}}})
	if strings.Contains(out, strings.Repeat("x", 400)) {
		t.Fatal("tool output must be cut to 300 characters")
	}
	if !strings.Contains(out, "Bash") {
		t.Fatal("the tool name stays")
	}
}

// §11.6: 40,000 characters, trimmed oldest first.
// Each turn is tagged with its index so the trim can be checked directly. The
// earlier version of this test joined the two halves with && ("oldest present AND
// newest absent"), which is the failure mode of a *reversed* trim and cannot fire
// for any correct or incorrect one-directional implementation (D62). The two
// conditions are separate assertions here, and either one alone is a real failure.
func TestBuildContextCapAndTrimOrder(t *testing.T) {
	var turns []Turn
	for i := range 400 {
		turns = append(turns, Turn{Role: "assistant",
			Text: fmt.Sprintf("<turn-%d>", i) + strings.Repeat("y", 300)})
	}
	out := BuildContext(ContextInput{Question: "the question", Transcript: turns,
		Brief: "the brief"})
	if len(out) > 40000 {
		t.Fatalf("context is %d characters, cap is 40000", len(out))
	}
	if !strings.Contains(out, "the question") || !strings.Contains(out, "the brief") {
		t.Fatal("the question and the brief always survive the trim")
	}
	// 400 turns of 300+ characters is well over the cap, so some must be gone.
	if strings.Contains(out, "<turn-0>") {
		t.Error("the oldest turn survived a trim that had to drop something")
	}
	if !strings.Contains(out, "<turn-399>") {
		t.Error("the newest turn was trimmed; the oldest go first")
	}
	// And the surviving range is contiguous from the end: no hole in the middle.
	first := -1
	for i := range 400 {
		if strings.Contains(out, fmt.Sprintf("<turn-%d>", i)) {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatal("no turn survived")
	}
	for i := first; i < 400; i++ {
		if !strings.Contains(out, fmt.Sprintf("<turn-%d>", i)) {
			t.Fatalf("turn %d is missing but %d survived; the trim must drop a prefix", i, first)
		}
	}
}

package workflow

import (
	"os"
	"strings"
	"testing"
)

func TestRenderBuildStepGolden(t *testing.T) {
	s := Spec{Steps: Templates["ui-tdd-reviewed"]}

	got := Render(s, "build", 2)

	want, err := os.ReadFile("testdata/render_build_r2.txt")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if got != strings.TrimRight(string(want), "\n") {
		t.Fatalf("Render() =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderReviewStepGolden(t *testing.T) {
	s := Spec{Steps: Templates["ui-tdd-reviewed"]}

	got := Render(s, "review", 2)

	want, err := os.ReadFile("testdata/render_review_r2.txt")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if got != strings.TrimRight(string(want), "\n") {
		t.Fatalf("Render() =\n%s\nwant\n%s", got, want)
	}
}

// Finding 5: the "red/green evidence" line only makes sense when the
// reviewed step actually has a tdd gate; design-reviewed's "design" step
// doesn't (its gate is artifact:design), so its review brief omits it.
func TestRenderReviewStepGoldenNoTDDGate(t *testing.T) {
	s := Spec{Steps: Templates["design-reviewed"]}

	got := Render(s, "review", 1)

	want, err := os.ReadFile("testdata/render_review_no_tdd_r1.txt")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if got != strings.TrimRight(string(want), "\n") {
		t.Fatalf("Render() =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "red/green") {
		t.Fatalf("Render() mentions red/green evidence for a step with no tdd gate:\n%s", got)
	}
}

// Decision 3 (minor): a resolved story spec has no Steps (Resolve leaves a
// non-task-shaped spec alone) - Render must operate on its single
// after_tasks review step instead of returning "" for it.
func TestRenderAfterTasksShape(t *testing.T) {
	storySpec, err := Resolve(Spec{AfterTasks: &Step{ID: "review", Review: []string{"reviewer"}}}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	got := Render(storySpec, "review", 1)
	want := "## Workflow\n" +
		"You are reviewing the story's merged work, round 1 of at most 3.\n" +
		"Give a verdict: pass, changes_requested or blocked. pass can't carry a critical or major finding. Each finding: {severity, file, line, unit (batched tasks), summary}. A blocked verdict escalates to the orchestrator."
	if got != want {
		t.Fatalf("Render() =\n%s\nwant\n%s", got, want)
	}
}

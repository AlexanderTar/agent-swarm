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

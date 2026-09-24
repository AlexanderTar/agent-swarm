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

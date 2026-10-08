package workflow

import (
	"reflect"
	"testing"
)

func TestClampLowToken(t *testing.T) {
	r1 := 1
	s, _ := Resolve(Spec{Template: "ui-tdd-reviewed"}, false)
	c := ClampLowToken(s)
	if got := c.Steps[1].Loop.MaxRounds; got != 2 {
		t.Fatalf("rounds=%d", got)
	}
	if c.Retries == nil || *c.Retries != 0 {
		t.Fatalf("retries=%v", c.Retries)
	}
	if !reflect.DeepEqual(c.Steps[1].Review, []string{"ui_reviewer"}) {
		t.Fatalf("review=%v", c.Steps[1].Review)
	}
	if s.Steps[1].Loop.MaxRounds != 3 || len(s.Steps[1].Review) != 2 {
		t.Fatal("input mutated")
	}
	one, _ := Resolve(Spec{Template: "tdd-reviewed", MaxRounds: 1, Retries: &r1}, false)
	if ClampLowToken(one).Steps[1].Loop.MaxRounds != 1 {
		t.Fatal("1 must stay 1")
	}
	story := Spec{AfterTasks: &Step{ID: "review", Review: []string{"reviewer", "ui_reviewer"}, Loop: &Loop{Fix: "x", MaxRounds: 3}}}
	cs := ClampLowToken(story)
	if cs.AfterTasks == nil || cs.AfterTasks.Loop.MaxRounds != 2 {
		t.Fatal("story review must stay, clamped")
	}
	if story.AfterTasks.Loop.MaxRounds != 3 {
		t.Fatal("story input mutated")
	}
	root := Spec{Integration: &Integration{FinalReview: []string{"reviewer", "ui_reviewer"}}}
	if got := ClampLowToken(root).Integration.FinalReview; !reflect.DeepEqual(got, []string{"ui_reviewer"}) {
		t.Fatalf("final=%v", got)
	}
	if len(root.Integration.FinalReview) != 2 {
		t.Fatal("root input mutated")
	}
	if got := ClampLowToken(Spec{Integration: &Integration{FinalReview: []string{"reviewer", "x"}}}).Integration.FinalReview; !reflect.DeepEqual(got, []string{"reviewer"}) {
		t.Fatalf("first role fallback=%v", got)
	}
}

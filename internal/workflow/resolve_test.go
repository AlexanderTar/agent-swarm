package workflow

import "testing"

func TestResolveDropsTDDWhenExempt(t *testing.T) {
	s := Spec{Template: "tdd-reviewed"}

	resolved, err := Resolve(s, true)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	build := resolved.Steps[0]
	for _, g := range build.Gates {
		if g == GateTDD {
			t.Fatalf("build step still has GateTDD when exempt: %v", build.Gates)
		}
	}
	want := []Gate{GateCommit, GateVerify}
	if len(build.Gates) != len(want) {
		t.Fatalf("build gates = %v, want %v", build.Gates, want)
	}
	for i, g := range want {
		if build.Gates[i] != g {
			t.Fatalf("build gates = %v, want %v", build.Gates, want)
		}
	}

	// The un-resolved template must not have been mutated.
	if len(Templates["tdd-reviewed"][0].Gates) != 3 {
		t.Fatalf("Templates[tdd-reviewed] mutated: %v", Templates["tdd-reviewed"][0].Gates)
	}

	// Non-exempt resolve keeps tdd.
	resolved2, err := Resolve(s, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	found := false
	for _, g := range resolved2.Steps[0].Gates {
		if g == GateTDD {
			found = true
		}
	}
	if !found {
		t.Fatalf("non-exempt resolve dropped GateTDD: %v", resolved2.Steps[0].Gates)
	}
}

func TestResolveMaxRoundsOverride(t *testing.T) {
	s := Spec{Template: "tdd-reviewed", MaxRounds: 5}

	resolved, err := Resolve(s, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	review := resolved.Steps[1]
	if review.Loop == nil || review.Loop.MaxRounds != 5 {
		t.Fatalf("review loop max_rounds = %+v, want 5", review.Loop)
	}

	// Un-resolved template stays at its own default.
	if Templates["tdd-reviewed"][1].Loop.MaxRounds != 3 {
		t.Fatalf("Templates[tdd-reviewed] mutated: %+v", Templates["tdd-reviewed"][1].Loop)
	}

	// Default retries is 1 when unset.
	if resolved.Retries == nil || *resolved.Retries != 1 {
		t.Fatalf("Retries = %v, want *1", resolved.Retries)
	}

	// Of is filled in for a custom spec that leaves it blank.
	custom := Spec{Steps: []Step{
		{ID: "design", Run: "designer"},
		{ID: "review", Review: []string{"ui_reviewer"}, Loop: &Loop{Fix: "design"}},
	}}
	resolvedCustom, err := Resolve(custom, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolvedCustom.Steps[1].Of != "design" {
		t.Fatalf("Of = %q, want %q", resolvedCustom.Steps[1].Of, "design")
	}
	if resolvedCustom.Steps[1].Loop.MaxRounds != 3 {
		t.Fatalf("default loop max_rounds = %d, want 3", resolvedCustom.Steps[1].Loop.MaxRounds)
	}

	// Explicit retries is preserved.
	two := 2
	withRetries := Spec{Template: "tdd-reviewed", Retries: &two}
	resolvedRetries, err := Resolve(withRetries, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolvedRetries.Retries == nil || *resolvedRetries.Retries != 2 {
		t.Fatalf("Retries = %v, want *2", resolvedRetries.Retries)
	}
}

func TestResolveRejectsTemplateAndSteps(t *testing.T) {
	_, err := Resolve(Spec{Template: "mechanical", Steps: []Step{{ID: "x", Run: "coder"}}}, false)
	if err == nil {
		t.Fatal("Resolve() = nil error, want an error")
	}
	if got, want := err.Error(), "set template or steps, not both"; got != want {
		t.Fatalf("Resolve() error = %q, want %q", got, want)
	}
}

// A story or root spec (no template, no steps) must resolve to something
// that still passes Validate for its level: Resolve must not default
// task-only fields (retries) on a spec that isn't task-shaped.
func TestResolveLeavesStoryFieldsAlone(t *testing.T) {
	s := Spec{AfterTasks: &Step{ID: "review", Review: []string{"reviewer"}}}

	resolved, err := Resolve(s, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Retries != nil {
		t.Fatalf("Retries = %v, want nil (story specs aren't task-shaped)", resolved.Retries)
	}
	if len(resolved.Steps) != 0 {
		t.Fatalf("Steps = %v, want none", resolved.Steps)
	}
	if err := Validate(LevelStory, resolved); err != nil {
		t.Fatalf("Validate(LevelStory, Resolve(storySpec)) = %v, want nil", err)
	}
}

// Resolve clears Template once it has expanded it into Steps, so a resolved
// task spec never has both fields set (which Validate would otherwise
// reject as "not both").
func TestResolveClearsTemplateAfterExpanding(t *testing.T) {
	resolved, err := Resolve(Spec{Template: "tdd-reviewed"}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Template != "" {
		t.Fatalf("Template = %q, want \"\" after resolving", resolved.Template)
	}
	if err := Validate(LevelTask, resolved); err != nil {
		t.Fatalf("Validate(LevelTask, Resolve(Spec{Template:...})) = %v, want nil", err)
	}
}

// A task-level review step with no loop, or a loop with no fix, gets one
// synthesized: Fix defaults to Of, MaxRounds to the usual default (or the
// spec-level override), OnExhausted to "escalate".
func TestResolveSynthesizesLoopForTaskReviewSteps(t *testing.T) {
	t.Run("no loop at all", func(t *testing.T) {
		s := Spec{Steps: []Step{
			{ID: "change", Run: "mechanical", Gates: []Gate{GateCommit}},
			{ID: "review", Review: []string{"reviewer"}},
		}}
		resolved, err := Resolve(s, false)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		review := resolved.Steps[1]
		if review.Loop == nil {
			t.Fatal("Loop = nil, want synthesized loop")
		}
		if review.Loop.Fix != "change" {
			t.Errorf("Loop.Fix = %q, want %q", review.Loop.Fix, "change")
		}
		if review.Loop.MaxRounds != 3 {
			t.Errorf("Loop.MaxRounds = %d, want 3", review.Loop.MaxRounds)
		}
		if review.Loop.OnExhausted != "escalate" {
			t.Errorf("Loop.OnExhausted = %q, want %q", review.Loop.OnExhausted, "escalate")
		}
		if err := Validate(LevelTask, resolved); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("loop with no fix", func(t *testing.T) {
		s := Spec{Steps: []Step{
			{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
			{ID: "review", Review: []string{"reviewer"}, Loop: &Loop{}},
		}}
		resolved, err := Resolve(s, false)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		review := resolved.Steps[1]
		if review.Loop.Fix != "build" {
			t.Errorf("Loop.Fix = %q, want %q", review.Loop.Fix, "build")
		}
		if review.Loop.MaxRounds != 3 {
			t.Errorf("Loop.MaxRounds = %d, want 3", review.Loop.MaxRounds)
		}
	})

	t.Run("spec-level max_rounds override applies to a synthesized loop too", func(t *testing.T) {
		s := Spec{
			MaxRounds: 5,
			Steps: []Step{
				{ID: "change", Run: "mechanical"},
				{ID: "review", Review: []string{"reviewer"}},
			},
		}
		resolved, err := Resolve(s, false)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if resolved.Steps[1].Loop.MaxRounds != 5 {
			t.Errorf("Loop.MaxRounds = %d, want 5", resolved.Steps[1].Loop.MaxRounds)
		}
	})
}

func TestRunRole(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want string
	}{
		{"tdd-reviewed", Spec{Steps: Templates["tdd-reviewed"]}, "coder"},
		{"design-reviewed", Spec{Steps: Templates["design-reviewed"]}, "designer"},
		{"mechanical", Spec{Steps: Templates["mechanical"]}, "mechanical"},
		{"empty", Spec{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RunRole(tt.spec); got != tt.want {
				t.Errorf("RunRole() = %q, want %q", got, tt.want)
			}
		})
	}
}

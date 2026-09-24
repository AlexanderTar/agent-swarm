package workflow

import (
	"reflect"
	"testing"
)

func TestTemplatesResolve(t *testing.T) {
	want := map[string][]Step{
		"tdd-reviewed": {
			{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
			{ID: "review", Review: []string{"reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3}},
		},
		"ui-tdd-reviewed": {
			{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
			{ID: "review", Review: []string{"reviewer", "ui_reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3}},
		},
		"design-reviewed": {
			{ID: "design", Run: "designer", Gates: []Gate{GateArtifactDesign}},
			{ID: "review", Review: []string{"ui_reviewer"}, Of: "design", Loop: &Loop{Fix: "design", MaxRounds: 2}},
		},
		"debug": {
			{ID: "fix", Run: "debugger", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
			{ID: "review", Review: []string{"reviewer"}, Of: "fix", Loop: &Loop{Fix: "fix", MaxRounds: 3}},
		},
		"mechanical": {
			{ID: "change", Run: "mechanical", Gates: []Gate{GateCommit, GateVerify}},
		},
		"research": {
			{ID: "research", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
		},
	}

	if len(Templates) != len(want) {
		t.Fatalf("Templates has %d entries, want %d", len(Templates), len(want))
	}

	for name, wantSteps := range want {
		gotSteps, ok := Templates[name]
		if !ok {
			t.Errorf("Templates missing %q", name)
			continue
		}
		if !reflect.DeepEqual(gotSteps, wantSteps) {
			t.Errorf("Templates[%q] = %+v, want %+v", name, gotSteps, wantSteps)
		}
	}
}

// TestTemplateResolves checks that Resolve(Spec{Template: name}, false)
// produces exactly the expected fully-resolved Spec: Template cleared,
// Steps from the table above, and Retries defaulted to 1.
func TestTemplateResolves(t *testing.T) {
	one := 1
	want := map[string]Spec{
		"tdd-reviewed": {
			Retries: &one,
			Steps: []Step{
				{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
				{ID: "review", Review: []string{"reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3}},
			},
		},
		"ui-tdd-reviewed": {
			Retries: &one,
			Steps: []Step{
				{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
				{ID: "review", Review: []string{"reviewer", "ui_reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3}},
			},
		},
		"design-reviewed": {
			Retries: &one,
			Steps: []Step{
				{ID: "design", Run: "designer", Gates: []Gate{GateArtifactDesign}},
				{ID: "review", Review: []string{"ui_reviewer"}, Of: "design", Loop: &Loop{Fix: "design", MaxRounds: 2}},
			},
		},
		"debug": {
			Retries: &one,
			Steps: []Step{
				{ID: "fix", Run: "debugger", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
				{ID: "review", Review: []string{"reviewer"}, Of: "fix", Loop: &Loop{Fix: "fix", MaxRounds: 3}},
			},
		},
		"mechanical": {
			Retries: &one,
			Steps: []Step{
				{ID: "change", Run: "mechanical", Gates: []Gate{GateCommit, GateVerify}},
			},
		},
		"research": {
			Retries: &one,
			Steps: []Step{
				{ID: "research", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
			},
		},
	}

	for name, want := range want {
		t.Run(name, func(t *testing.T) {
			got, err := Resolve(Spec{Template: name}, false)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", name, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Resolve(%q) = %+v, want %+v", name, got, want)
			}
			if err := Validate(LevelTask, got); err != nil {
				t.Errorf("Validate(LevelTask, Resolve(%q)) = %v, want nil", name, err)
			}
		})
	}
}

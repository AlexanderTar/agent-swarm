package workflow

import (
	"reflect"
	"testing"
)

// TestTemplateResolves checks that Resolve(Spec{Template: name}, false)
// produces exactly the expected fully-resolved Spec: Template cleared,
// Steps from spec B2's table (each loop's OnExhausted set to "escalate",
// matching a synthesized loop - see Resolve), and Retries defaulted to 1.
func TestTemplateResolves(t *testing.T) {
	one := 1
	want := map[string]Spec{
		"tdd-reviewed": {
			Retries: &one,
			Steps: []Step{
				{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
				{ID: "review", Review: []string{"reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3, OnExhausted: "escalate"}},
			},
		},
		"ui-tdd-reviewed": {
			Retries: &one,
			Steps: []Step{
				{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
				{ID: "review", Review: []string{"reviewer", "ui_reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3, OnExhausted: "escalate"}},
			},
		},
		"design-reviewed": {
			Retries: &one,
			Steps: []Step{
				{ID: "design", Run: "designer", Gates: []Gate{GateArtifactDesign}},
				{ID: "review", Review: []string{"ui_reviewer"}, Of: "design", Loop: &Loop{Fix: "design", MaxRounds: 2, OnExhausted: "escalate"}},
			},
		},
		"debug": {
			Retries: &one,
			Steps: []Step{
				{ID: "fix", Run: "debugger", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
				{ID: "review", Review: []string{"reviewer"}, Of: "fix", Loop: &Loop{Fix: "fix", MaxRounds: 3, OnExhausted: "escalate"}},
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

	if len(Templates) != len(want) {
		t.Fatalf("Templates has %d entries, want %d", len(Templates), len(want))
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

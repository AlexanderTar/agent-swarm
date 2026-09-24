package workflow

// reviewed builds the two-step {run -> review} shape shared by every
// reviewed template: a run step followed by a review step whose fix loop
// retries the run step.
func reviewed(id, role string, gates []Gate, reviewers []string, maxRounds int) []Step {
	return []Step{
		{ID: id, Run: role, Gates: gates},
		{ID: "review", Review: reviewers, Of: id, Loop: &Loop{Fix: id, MaxRounds: maxRounds, OnExhausted: "escalate"}},
	}
}

// Templates is the fixed set of named workflow shapes a task can reference
// by name (Spec.Template). Resolve expands the named template into
// Spec.Steps.
var Templates = map[string][]Step{
	"tdd-reviewed":    reviewed("build", "coder", []Gate{GateTDD, GateCommit, GateVerify}, []string{"reviewer"}, 3),
	"ui-tdd-reviewed": reviewed("build", "coder", []Gate{GateTDD, GateCommit, GateVerify}, []string{"reviewer", "ui_reviewer"}, 3),
	"design-reviewed": reviewed("design", "designer", []Gate{GateArtifactDesign}, []string{"ui_reviewer"}, 2),
	"debug":           reviewed("fix", "debugger", []Gate{GateTDD, GateCommit, GateVerify}, []string{"reviewer"}, 3),
	"mechanical": {
		{ID: "change", Run: "mechanical", Gates: []Gate{GateCommit, GateVerify}},
	},
	"research": {
		{ID: "research", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
	},
}

package workflow

import (
	"fmt"
	"strings"
)

// Render produces the "## Workflow" section of a step agent's brief (spec
// B6): what step and round the agent is running, its gates, who reviews it
// next (for a run step), or what it is reviewing and the verdict contract
// (for a review step). It returns "" if stepID isn't in s.Steps.
func Render(s Spec, stepID string, round int) string {
	step := findStep(s, stepID)
	if step == nil {
		return ""
	}

	lines := []string{"## Workflow"}

	if step.Run != "" {
		if rev, ok := findReviewOf(s, stepID); ok {
			max := defaultLoopMaxRounds
			if rev.Loop != nil && rev.Loop.MaxRounds != 0 {
				max = rev.Loop.MaxRounds
			}
			lines = append(lines, fmt.Sprintf("You are step %q (%s), round %d of at most %d.", step.ID, step.Run, round, max))
			if len(step.Gates) > 0 {
				lines = append(lines, fmt.Sprintf("Gates for your completed checkpoint: %s.", joinGates(step.Gates)))
			}
			lines = append(lines, fmt.Sprintf("After you complete: %s review your commit at its sha.", strings.Join(rev.Review, " + ")))
			lines = append(lines, "If they request changes you receive their findings as an assignment update in this same session.")
			lines = append(lines, "Do not spawn or message reviewers yourself.")
		} else {
			lines = append(lines, fmt.Sprintf("You are step %q (%s), round %d.", step.ID, step.Run, round))
			if len(step.Gates) > 0 {
				lines = append(lines, fmt.Sprintf("Gates for your completed checkpoint: %s.", joinGates(step.Gates)))
			}
		}
		return strings.Join(lines, "\n")
	}

	// Review step.
	max := defaultLoopMaxRounds
	if step.Loop != nil && step.Loop.MaxRounds != 0 {
		max = step.Loop.MaxRounds
	}
	lines = append(lines, fmt.Sprintf("You are reviewing step %q, round %d of at most %d.", step.Of, round, max))
	lines = append(lines, "Give a verdict: pass, changes_requested or blocked.")
	return strings.Join(lines, "\n")
}

func joinGates(gates []Gate) string {
	ss := make([]string, len(gates))
	for i, g := range gates {
		ss[i] = string(g)
	}
	return strings.Join(ss, ", ")
}

func findStep(s Spec, id string) *Step {
	for i := range s.Steps {
		if s.Steps[i].ID == id {
			return &s.Steps[i]
		}
	}
	return nil
}

// findReviewOf finds the review step whose Of is runStepID.
func findReviewOf(s Spec, runStepID string) (*Step, bool) {
	for i := range s.Steps {
		if len(s.Steps[i].Review) > 0 && s.Steps[i].Of == runStepID {
			return &s.Steps[i], true
		}
	}
	return nil, false
}

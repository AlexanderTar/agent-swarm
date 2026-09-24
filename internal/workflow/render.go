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
		if rev := findReviewOf(s, stepID); rev != nil {
			maxRounds := loopMaxRounds(rev.Loop)
			lines = append(lines, fmt.Sprintf("You are step %q (%s), round %d of at most %d.", step.ID, step.Run, round, maxRounds))
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

	// Review step: name what it reviews and the round, point at the
	// reviewed step's red/green evidence, and spell out the verdict
	// contract (spec B5/B6).
	ofRole := ""
	if of := findStep(s, step.Of); of != nil {
		ofRole = of.Run
	}
	lines = append(lines, fmt.Sprintf("You are reviewing step %q (%s), round %d of at most %d.", step.Of, ofRole, round, loopMaxRounds(step.Loop)))
	lines = append(lines, "The builder's red/green evidence is in its checkpoints; swarm_read the task's checkpoints to see it.")
	lines = append(lines, "Give a verdict: pass, changes_requested or blocked. pass can't carry a critical or major finding. Each finding: {severity, file, line, summary}. A blocked verdict escalates to the orchestrator.")
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

// findReviewOf finds the review step whose Of is runStepID, or nil.
func findReviewOf(s Spec, runStepID string) *Step {
	for i := range s.Steps {
		if len(s.Steps[i].Review) > 0 && s.Steps[i].Of == runStepID {
			return &s.Steps[i]
		}
	}
	return nil
}

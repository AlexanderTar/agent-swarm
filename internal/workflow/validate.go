package workflow

import (
	"fmt"
	"regexp"
)

var stepIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

var runRoles = map[string]bool{
	"coder":      true,
	"debugger":   true,
	"mechanical": true,
	"designer":   true,
	"researcher": true,
}

var reviewRoles = map[string]bool{
	"reviewer":    true,
	"ui_reviewer": true,
}

const maxSteps = 6

// Validate checks a Spec against the workflow DSL rules for its level.
func Validate(level Level, s Spec) error {
	taskFieldsSet := s.Template != "" || len(s.Steps) > 0 || s.MaxRounds != 0 || s.Retries != nil

	switch level {
	case LevelTask:
		if s.AfterTasks != nil {
			return fmt.Errorf("after_tasks is only for stories")
		}
		if s.Integration != nil {
			return fmt.Errorf("integration is only for epics and bugs")
		}
		hasTemplate := s.Template != ""
		hasSteps := len(s.Steps) > 0
		if hasTemplate && hasSteps {
			return fmt.Errorf("set template or steps, not both")
		}
		if !hasTemplate && !hasSteps {
			return fmt.Errorf("set template or steps")
		}
	case LevelStory:
		if taskFieldsSet {
			return fmt.Errorf("template, steps, max_rounds and retries are only for tasks")
		}
		if s.Integration != nil {
			return fmt.Errorf("integration is only for epics and bugs")
		}
	case LevelRoot:
		if taskFieldsSet {
			return fmt.Errorf("template, steps, max_rounds and retries are only for tasks")
		}
		if s.AfterTasks != nil {
			return fmt.Errorf("after_tasks is only for stories")
		}
	default:
		return fmt.Errorf("unknown level %q", level)
	}

	if s.Template != "" {
		if _, ok := Templates[s.Template]; !ok {
			return fmt.Errorf("unknown template %q", s.Template)
		}
	}

	if s.MaxRounds != 0 && (s.MaxRounds < 1 || s.MaxRounds > 5) {
		return fmt.Errorf("max_rounds must be 1–5")
	}

	if s.Retries != nil && (*s.Retries < 0 || *s.Retries > 2) {
		return fmt.Errorf("retries must be 0–2")
	}

	if err := validateSteps(s.Steps); err != nil {
		return err
	}

	if s.AfterTasks != nil {
		if err := validateAfterTasks(*s.AfterTasks); err != nil {
			return err
		}
	}

	if s.Integration != nil {
		for _, role := range s.Integration.FinalReview {
			if !reviewRoles[role] {
				return fmt.Errorf("integration final_review: %s can't review", role)
			}
		}
	}

	return nil
}

// validateStepShape checks the rules that apply to a single step in
// isolation: exactly one of run/review, a valid id, valid roles, no
// duplicate reviewers, and loop/of only on a review step. It doesn't check
// anything that needs the rest of the step list (uniqueness, ordering,
// earlier-run-step references).
func validateStepShape(st Step) error {
	hasRun := st.Run != ""
	hasReview := len(st.Review) > 0
	if hasRun == hasReview {
		return fmt.Errorf("step %s: set exactly one of run or review", st.ID)
	}
	if !stepIDPattern.MatchString(st.ID) {
		return fmt.Errorf("step %s: invalid id, must match [a-z][a-z0-9-]*", st.ID)
	}
	if hasRun {
		if !runRoles[st.Run] {
			return fmt.Errorf("step %s: %s can't run a step", st.ID, st.Run)
		}
		if st.Of != "" || st.Loop != nil {
			return fmt.Errorf("step %s: loop/of only apply to review steps", st.ID)
		}
		return nil
	}
	seenRoles := map[string]bool{}
	for _, r := range st.Review {
		if !reviewRoles[r] {
			return fmt.Errorf("step %s: %s can't review", st.ID, r)
		}
		if seenRoles[r] {
			return fmt.Errorf("step %s: duplicate reviewer %s", st.ID, r)
		}
		seenRoles[r] = true
	}
	return nil
}

func validateSteps(steps []Step) error {
	if len(steps) == 0 {
		return nil
	}
	if len(steps) > maxSteps {
		return fmt.Errorf("at most 6 steps")
	}
	if steps[0].Run == "" {
		return fmt.Errorf("the first step must be a run step")
	}

	seen := map[string]bool{}
	runSteps := map[string]bool{}

	for _, st := range steps {
		if err := validateStepShape(st); err != nil {
			return err
		}
		if seen[st.ID] {
			return fmt.Errorf("duplicate step id %q", st.ID)
		}
		seen[st.ID] = true

		if st.Run != "" {
			runSteps[st.ID] = true
			continue
		}

		if st.Of != "" && !runSteps[st.Of] {
			return fmt.Errorf("step %s: of/fix must name an earlier run step", st.ID)
		}
		if st.Loop != nil {
			if st.Loop.Fix != "" && !runSteps[st.Loop.Fix] {
				return fmt.Errorf("step %s: of/fix must name an earlier run step", st.ID)
			}
			if st.Loop.MaxRounds != 0 && (st.Loop.MaxRounds < 1 || st.Loop.MaxRounds > 5) {
				return fmt.Errorf("max_rounds must be 1–5")
			}
		}
	}
	return nil
}

func validateAfterTasks(st Step) error {
	if err := validateStepShape(st); err != nil {
		return err
	}
	if len(st.Review) == 0 {
		return fmt.Errorf("after_tasks must be a review step")
	}
	if st.Loop != nil {
		return fmt.Errorf("after_tasks can't have a loop")
	}
	return nil
}

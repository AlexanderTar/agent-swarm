package workflow

import "fmt"

const defaultLoopMaxRounds = 3
const defaultRetries = 1

// Resolve expands a template (if set) into Steps, applies the max_rounds
// override, defaults each loop's max_rounds and the spec's retries, fills in
// each review step's default Of (the nearest preceding run step) and, for a
// task-level review step with no loop (or a loop with no fix), synthesizes
// one (Fix: Of, MaxRounds: default or override, OnExhausted: "escalate");
// and drops the tdd gate everywhere when the task is tdd-exempt.
//
// A spec that is neither template- nor steps-shaped (a story or root spec)
// is returned as-is, aside from a defensive copy of AfterTasks: it has
// nothing to expand, and task-only fields like Retries must stay unset so
// Validate for its level still passes.
//
// Setting both Template and Steps is an error: pick one.
func Resolve(s Spec, tddExempt bool) (Spec, error) {
	out := s

	if out.AfterTasks != nil {
		at := *out.AfterTasks
		out.AfterTasks = &at
	}

	hasTemplate := s.Template != ""
	hasSteps := len(s.Steps) > 0

	if !hasTemplate && !hasSteps {
		return out, nil
	}
	if hasTemplate && hasSteps {
		return Spec{}, fmt.Errorf("set template or steps, not both")
	}

	if hasTemplate {
		steps, ok := Templates[s.Template]
		if !ok {
			return Spec{}, fmt.Errorf("unknown template %q", s.Template)
		}
		out.Steps = cloneSteps(steps)
		out.Template = "" // resolved: Steps is now the source of truth
	} else {
		out.Steps = cloneSteps(s.Steps)
	}

	lastRun := ""
	for i := range out.Steps {
		st := &out.Steps[i]
		if st.Run != "" {
			lastRun = st.ID
			continue
		}
		if st.Of == "" {
			st.Of = lastRun
		}
		if st.Loop == nil || st.Loop.Fix == "" {
			maxRounds := defaultLoopMaxRounds
			if st.Loop != nil && st.Loop.MaxRounds != 0 {
				maxRounds = st.Loop.MaxRounds
			}
			st.Loop = &Loop{Fix: st.Of, MaxRounds: maxRounds, OnExhausted: "escalate"}
		} else if st.Loop.MaxRounds == 0 {
			st.Loop.MaxRounds = defaultLoopMaxRounds
		}
	}

	if s.MaxRounds != 0 {
		for i := range out.Steps {
			if out.Steps[i].Loop != nil {
				out.Steps[i].Loop.MaxRounds = s.MaxRounds
			}
		}
	}

	if out.Retries == nil {
		n := defaultRetries
		out.Retries = &n
	}

	if tddExempt {
		for i := range out.Steps {
			out.Steps[i].Gates = dropGate(out.Steps[i].Gates, GateTDD)
		}
	}

	return out, nil
}

// RunRole returns the first run step's role, used for role_hint. It returns
// "" when the spec has no run step (e.g. it hasn't been resolved yet).
func RunRole(s Spec) string {
	for _, st := range s.Steps {
		if st.Run != "" {
			return st.Run
		}
	}
	return ""
}

func cloneSteps(steps []Step) []Step {
	out := make([]Step, len(steps))
	for i, st := range steps {
		ns := st
		if st.Gates != nil {
			ns.Gates = append([]Gate(nil), st.Gates...)
		}
		if st.Review != nil {
			ns.Review = append([]string(nil), st.Review...)
		}
		if st.Loop != nil {
			l := *st.Loop
			ns.Loop = &l
		}
		out[i] = ns
	}
	return out
}

func dropGate(gates []Gate, g Gate) []Gate {
	if gates == nil {
		return nil
	}
	out := make([]Gate, 0, len(gates))
	for _, x := range gates {
		if x != g {
			out = append(out, x)
		}
	}
	return out
}

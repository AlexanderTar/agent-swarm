package workflow

import "fmt"

const defaultLoopMaxRounds = 3
const defaultRetries = 1

// Resolve expands a template (if set) into Steps, applies the max_rounds
// override, defaults each loop's max_rounds and the spec's retries, fills in
// each review step's default Of (the nearest preceding run step), and drops
// the tdd gate everywhere when the task is tdd-exempt.
func Resolve(s Spec, tddExempt bool) (Spec, error) {
	out := s

	if s.Template != "" {
		steps, ok := Templates[s.Template]
		if !ok {
			return Spec{}, fmt.Errorf("unknown template %q", s.Template)
		}
		out.Steps = cloneSteps(steps)
	} else {
		out.Steps = cloneSteps(s.Steps)
	}

	if out.AfterTasks != nil {
		at := *out.AfterTasks
		out.AfterTasks = &at
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
		if st.Loop != nil && st.Loop.MaxRounds == 0 {
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

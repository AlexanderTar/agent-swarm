package workflow

// ClampLowToken returns a copy of s with the low-token limits applied: loop
// rounds at most 2, no automatic retries, and one reviewer per review step and
// for the final review. The input is never modified.
func ClampLowToken(s Spec) Spec {
	out := s
	if s.MaxRounds > 2 {
		out.MaxRounds = 2
	}
	if s.Steps != nil {
		out.Steps = make([]Step, len(s.Steps))
		for i, st := range s.Steps {
			out.Steps[i] = clampStep(st)
		}
	}
	if s.AfterTasks != nil {
		st := clampStep(*s.AfterTasks)
		out.AfterTasks = &st
	}
	if s.Integration != nil {
		in := *s.Integration
		in.MergeOrder = append([]string(nil), in.MergeOrder...)
		in.Verify = append([]string(nil), in.Verify...)
		in.FinalReview = pickOne(in.FinalReview)
		out.Integration = &in
	}
	zero := 0
	out.Retries = &zero
	return out
}

func clampStep(st Step) Step {
	st.Gates = append([]Gate(nil), st.Gates...)
	st.Review = pickOne(st.Review)
	if st.Loop != nil {
		l := *st.Loop
		if l.MaxRounds == 0 || l.MaxRounds > 2 {
			l.MaxRounds = 2
		}
		st.Loop = &l
	}
	return st
}

// pickOne keeps ui_reviewer if present, else the first role.
func pickOne(roles []string) []string {
	if len(roles) == 0 {
		return roles
	}
	for _, r := range roles {
		if r == "ui_reviewer" {
			return []string{r}
		}
	}
	return []string{roles[0]}
}

package workflow

import (
	"fmt"
	"sort"
)

// RunState is the lifecycle state of a step run.
type RunState string

const (
	RunStateWaiting   RunState = "waiting"
	RunStateActive    RunState = "active"
	RunStateCompleted RunState = "completed"
	RunStateFailed    RunState = "failed"
	RunStateCancelled RunState = "cancelled"
)

// Verdict is a reviewer's verdict on a run it reviewed.
type Verdict string

const (
	VerdictNone             Verdict = ""
	VerdictPass             Verdict = "pass"
	VerdictChangesRequested Verdict = "changes_requested"
	VerdictBlocked          Verdict = "blocked"
)

// Finding is one reviewer-reported issue.
type Finding struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Unit     int    `json:"unit,omitempty"`
	Summary  string `json:"summary"`
	Reviewer string `json:"reviewer,omitempty"` // the reviewer role that reported it
}

// Run is one agent's attempt at a workflow step.
type Run struct {
	StepID      string
	Round       int
	Role        string
	State       RunState // waiting|active|completed|failed|cancelled
	Verdict     Verdict  // ""|pass|changes_requested|blocked
	Findings    []Finding
	SHA         string
	AutoRetries int
}

// ActionKind is what the engine should do next.
type ActionKind string

const (
	ActionSpawn     ActionKind = "spawn"
	ActionRetryFix  ActionKind = "retry_fix"
	ActionAutoRetry ActionKind = "auto_retry"
	ActionWait      ActionKind = "wait"
	ActionSucceed   ActionKind = "succeed"
	ActionEscalate  ActionKind = "escalate"
)

// Action is the single next step the engine should take, as decided by Next.
type Action struct {
	Kind     ActionKind // spawn|retry_fix|auto_retry|wait|succeed|escalate
	StepID   string
	Roles    []string
	Round    int
	Findings []Finding
	Run      *Run
	SHA      string
	Reason   string
}

// Next is the pure workflow planner (spec B4). Given the resolved spec, every
// run recorded so far, the workflow's current round and any extra rounds
// granted by an orchestrator's resume, it returns the single next action.
// It is total: every reachable state produces exactly one Action, and it
// never panics.
//
// Next sorts its own copy of runs by (Round, StepID, Role) before looking at
// them, so callers may pass runs in any order (e.g. straight from a DB
// query with no ORDER BY) and get the same, deterministic result.
//
// A step that is a review step, or that is some review step's Loop.Fix, is
// "retryable": Next only considers its runs from the current round (an
// earlier round's runs for it are a superseded attempt). Every other run
// step is satisfied by its latest run at or before the current round, and
// its sha is carried forward - it is never re-spawned just because a later
// step's retry loop bumped the round.
func Next(s Spec, runs []Run, round, extraRounds int) Action {
	runs = sortedRuns(runs)
	retryable := retryableSteps(s)

	// Failures in the current round are handled first, regardless of which
	// step they belong to. A terminal run from an earlier, superseded round
	// (e.g. a stale reviewer left over from before a retry) is not a
	// current failure and is ignored here.
	for _, run := range runs {
		if run.Round != round {
			continue
		}
		if run.State == RunStateCancelled {
			return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s cancelled", run.Role)}
		}
		if run.State == RunStateFailed {
			retries := defaultRetries
			if s.Retries != nil {
				retries = *s.Retries
			}
			if run.AutoRetries < retries {
				rc := run
				return Action{Kind: ActionAutoRetry, StepID: run.StepID, Round: run.Round, Run: &rc}
			}
			return Action{Kind: ActionEscalate, Reason: failureReason(run, retries)}
		}
	}

	shas := map[string]string{}
	for i, step := range s.Steps {
		var stepRuns []Run
		if retryable[step.ID] {
			stepRuns = runsFor(runs, step.ID, round)
		} else {
			stepRuns = latestRunsFor(runs, step.ID, round)
		}
		last := i == len(s.Steps)-1

		if step.Run != "" {
			if len(stepRuns) == 0 {
				return Action{Kind: ActionSpawn, StepID: step.ID, Roles: []string{step.Run}, Round: round}
			}
			run := stepRuns[0]
			if run.State != RunStateCompleted {
				return Action{Kind: ActionWait, StepID: step.ID, Round: round}
			}
			if hasGate(step.Gates, GateCommit) && run.SHA == "" {
				return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s completed without a sha", step.ID)}
			}
			shas[step.ID] = run.SHA
			if last {
				return Action{Kind: ActionSucceed, SHA: run.SHA}
			}
			continue
		}

		// Review step.
		missing := missingRoles(step.Review, stepRuns)
		if len(missing) > 0 {
			return Action{Kind: ActionSpawn, StepID: step.ID, Roles: missing, Round: round, SHA: shas[step.Of]}
		}
		for _, run := range stepRuns {
			if run.State != RunStateCompleted {
				return Action{Kind: ActionWait, StepID: step.ID, Round: round}
			}
		}

		if blocked := findBlocked(stepRuns); blocked != nil {
			reason := blocked.Role + " blocked"
			if summary := firstSummary(blocked.Findings); summary != "" {
				reason += ": " + summary
			}
			return Action{Kind: ActionEscalate, Reason: reason}
		}

		if hasChangesRequested(stepRuns) {
			// Decision A: a review step with no loop (a story after_tasks
			// shape) has no fix step to retry - it can only escalate.
			if step.Loop == nil {
				return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s changes requested", step.ID)}
			}
			fix := step.Loop.Fix
			if fix == "" {
				fix = step.ID
			}
			effectiveMax := loopMaxRounds(step.Loop) + extraRounds
			if round >= effectiveMax {
				return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s rounds exhausted (%d/%d)", step.ID, round, effectiveMax)}
			}
			return Action{Kind: ActionRetryFix, StepID: fix, Round: round + 1, Findings: mergeFindings(step, stepRuns)}
		}

		// Every reviewer passed.
		if last {
			return Action{Kind: ActionSucceed, SHA: shas[step.Of]}
		}
	}

	// A spec with no steps at all: nothing to do but tell the caller so.
	return Action{Kind: ActionEscalate, Reason: "workflow has no steps"}
}

// retryableSteps returns the set of step ids that must be matched to the
// exact current round: every review step, plus every step named as some
// review step's Loop.Fix.
func retryableSteps(s Spec) map[string]bool {
	out := map[string]bool{}
	for _, st := range s.Steps {
		if len(st.Review) > 0 {
			out[st.ID] = true
		}
		if st.Loop != nil && st.Loop.Fix != "" {
			out[st.Loop.Fix] = true
		}
	}
	return out
}

func sortedRuns(in []Run) []Run {
	out := append([]Run(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Round != out[j].Round {
			return out[i].Round < out[j].Round
		}
		if out[i].StepID != out[j].StepID {
			return out[i].StepID < out[j].StepID
		}
		return out[i].Role < out[j].Role
	})
	return out
}

func runsFor(runs []Run, stepID string, round int) []Run {
	var out []Run
	for _, run := range runs {
		if run.StepID == stepID && run.Round == round {
			out = append(out, run)
		}
	}
	return out
}

// latestRunsFor returns the runs for stepID at the highest round <= upTo
// that has any, or nil if there are none. It's how a step that isn't being
// retried this round stays "satisfied" once it has ever completed.
func latestRunsFor(runs []Run, stepID string, upTo int) []Run {
	best := -1
	for _, run := range runs {
		if run.StepID == stepID && run.Round <= upTo && run.Round > best {
			best = run.Round
		}
	}
	if best == -1 {
		return nil
	}
	return runsFor(runs, stepID, best)
}

func missingRoles(want []string, have []Run) []string {
	present := map[string]bool{}
	for _, run := range have {
		present[run.Role] = true
	}
	var missing []string
	for _, role := range want {
		if !present[role] {
			missing = append(missing, role)
		}
	}
	return missing
}

func hasGate(gates []Gate, g Gate) bool {
	for _, x := range gates {
		if x == g {
			return true
		}
	}
	return false
}

func findBlocked(runs []Run) *Run {
	for i := range runs {
		if runs[i].Verdict == VerdictBlocked {
			return &runs[i]
		}
	}
	return nil
}

func hasChangesRequested(runs []Run) bool {
	for _, run := range runs {
		if run.Verdict == VerdictChangesRequested {
			return true
		}
	}
	return false
}

func firstSummary(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	return findings[0].Summary
}

func failureReason(run Run, retries int) string {
	if retries == 0 {
		return fmt.Sprintf("%s %s", run.Role, run.State)
	}
	return fmt.Sprintf("%s %s after %d auto-retries", run.Role, run.State, run.AutoRetries)
}

// loopMaxRounds returns l's max_rounds, or the default when l is nil or
// leaves it unset.
func loopMaxRounds(l *Loop) int {
	if l != nil && l.MaxRounds != 0 {
		return l.MaxRounds
	}
	return defaultLoopMaxRounds
}

// mergeFindings collects every reviewer's findings for the round (decision
// C: including a reviewer who passed but left minor/nit findings, not just
// the ones who requested changes), in the step's declared reviewer role
// order, each tagged with its reviewer and sorted by file then line.
func mergeFindings(step Step, stepRuns []Run) []Finding {
	var out []Finding
	for _, role := range step.Review {
		for _, run := range stepRuns {
			if run.Role != role {
				continue
			}
			fs := make([]Finding, len(run.Findings))
			copy(fs, run.Findings)
			for i := range fs {
				fs[i].Reviewer = role
			}
			sort.SliceStable(fs, func(i, j int) bool {
				if fs[i].File != fs[j].File {
					return fs[i].File < fs[j].File
				}
				return fs[i].Line < fs[j].Line
			})
			out = append(out, fs...)
		}
	}
	return out
}

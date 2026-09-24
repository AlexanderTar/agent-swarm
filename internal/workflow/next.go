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
	Severity string
	File     string
	Line     int
	Unit     int
	Summary  string
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
func Next(s Spec, runs []Run, round, extraRounds int) Action {
	// Failures are handled first, regardless of which step they belong to.
	for _, run := range runs {
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
			return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s %s twice", run.Role, run.State)}
		}
	}

	var lastSHA string
	for i, step := range s.Steps {
		stepRuns := runsFor(runs, step.ID, round)
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
			lastSHA = run.SHA
			if last {
				return Action{Kind: ActionSucceed, SHA: lastSHA}
			}
			continue
		}

		// Review step.
		if len(stepRuns) == 0 {
			return Action{Kind: ActionSpawn, StepID: step.ID, Roles: step.Review, Round: round, SHA: lastSHA}
		}
		for _, run := range stepRuns {
			if run.State != RunStateCompleted {
				return Action{Kind: ActionWait, StepID: step.ID, Round: round}
			}
		}

		if blocked := findBlocked(stepRuns); blocked != nil {
			return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s blocked: %s", blocked.Role, firstSummary(blocked.Findings))}
		}

		if hasChangesRequested(stepRuns) {
			max := defaultLoopMaxRounds
			fix := step.ID
			if step.Loop != nil {
				if step.Loop.MaxRounds != 0 {
					max = step.Loop.MaxRounds
				}
				if step.Loop.Fix != "" {
					fix = step.Loop.Fix
				}
			}
			effectiveMax := max + extraRounds
			if round >= effectiveMax {
				return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s rounds exhausted (%d/%d)", step.ID, round, effectiveMax)}
			}
			return Action{Kind: ActionRetryFix, StepID: fix, Round: round + 1, Findings: mergeFindings(step, stepRuns)}
		}

		// Every reviewer passed.
		if last {
			return Action{Kind: ActionSucceed, SHA: lastSHA}
		}
	}

	// A spec with no steps at all: nothing to do but tell the caller so.
	return Action{Kind: ActionEscalate, Reason: "workflow has no steps"}
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

// mergeFindings collects the findings of every reviewer that requested
// changes, in the step's declared reviewer role order, each reviewer's own
// findings sorted by file then line.
func mergeFindings(step Step, stepRuns []Run) []Finding {
	var out []Finding
	for _, role := range step.Review {
		for _, run := range stepRuns {
			if run.Role != role || run.Verdict != VerdictChangesRequested {
				continue
			}
			fs := append([]Finding(nil), run.Findings...)
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

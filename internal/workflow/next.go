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
// never panics. round < 1 is clamped to 1.
//
// Next sorts its own copy of runs by (Round, StepID, Role) before looking at
// them, so callers may pass runs in any order (e.g. straight from a DB
// query with no ORDER BY) and get the same, deterministic result.
//
// For round > 1, Next finds the review step in round-1 whose runs requested
// changes (the one that triggered this round), tie-broken to the lowest
// Loop.Fix index when more than one did, and pins every step from that fix
// step's index to the end of the step list: those must have a run in the
// exact current round. Steps before that index carry forward from their
// latest run at or before the current round, sha included - they are never
// re-spawned just because a later step's retry loop bumped the round. This
// still applies when the changes_requested/blocked row itself is stale (its
// sha doesn't match what it reviewed): the row's sha can't be trusted, but
// which step it belongs to can, and the only way such a row survives to the
// previous round at all is an orchestrator resume after the same-round
// stale-review escalation. A stale row with no such verdict (e.g. one that
// was itself the escalated row, recorded pass/active/failed before the sha
// it reviewed was superseded) pins the same way - it's the same resume
// shape, just without a verdict to key off. When neither explains the bump
// at all (e.g. the round was bumped by an orchestrator's resume after a
// crash escalation, not a normal fix loop), Next instead pins from the
// index of whichever step failed or was cancelled in round-1. round 1
// itself has no earlier round to consult, so everything is pinned.
//
// Whichever way a step's current run(s) are selected - pinned to this exact
// round, or carried forward from an earlier one - a failed or cancelled run
// among them is handled the same way (auto-retry while retries remain, else
// escalate): Next never returns Wait on a terminal run, carried forward or
// not.
func Next(s Spec, runs []Run, round, extraRounds int) Action {
	if round < 1 {
		round = 1
	}
	// A resolved story spec has no Steps (Resolve leaves a non-task-shaped
	// spec alone) - operate on its single after_tasks review step instead.
	if len(s.Steps) == 0 && s.AfterTasks != nil {
		s.Steps = []Step{*s.AfterTasks}
	}
	runs = sortedRuns(runs)
	pinnedFromIdx := pinnedFrom(s, runs, round)

	currentRuns := func(i int, stepID string) []Run {
		if i >= pinnedFromIdx {
			return runsFor(runs, stepID, round)
		}
		return latestRunsFor(runs, stepID, round)
	}

	// Failures are handled first, in step order, whether they're pinned to
	// this exact round or carried forward from an earlier one.
	for i, step := range s.Steps {
		for _, run := range currentRuns(i, step.ID) {
			// A stale review (one whose recorded sha no longer matches
			// what it reviewed) isn't a real failure signal at its old
			// sha - it's handled by the ordinary review-step evaluation
			// below instead (escalate if it's this round's own row, or
			// dropped if it's carried forward from an earlier one).
			if len(step.Review) > 0 {
				if ofIdx := stepIndex(s, step.Of); ofIdx != -1 {
					if want := completedSHA(currentRuns(ofIdx, step.Of)); want != "" && run.SHA != "" && run.SHA != want {
						continue
					}
				}
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
	}

	shas := map[string]string{}
	for i, step := range s.Steps {
		stepRuns := currentRuns(i, step.ID)
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

		// Review step. A reviewer's run is stamped with the sha it actually
		// reviewed; once that sha is superseded (the reviewed step re-ran),
		// the approval is stale. A stale row from an EARLIER round is
		// simply dropped - that role is treated as not having reported at
		// all, so it's re-spawned at the fresh sha in the current round (a
		// brand new (step, round, role) key). A stale row already sitting
		// in the CURRENT round can't be dropped-and-respawned the same
		// way: that key already exists, so a Spawn for it would be a
		// no-op against the UNIQUE(workflow_id, step_id, round, role)
		// constraint and hang the workflow. That case escalates instead.
		if want := shas[step.Of]; want != "" {
			fresh, escalate := staleReviews(step, stepRuns, want, round)
			if escalate != nil {
				return *escalate
			}
			stepRuns = fresh
		}

		// A blocked verdict escalates immediately - it never waits for, or
		// spawns, a still-missing reviewer role first.
		if blocked := findBlocked(stepRuns); blocked != nil {
			reason := blocked.Role + " blocked"
			if summary := firstSummary(blocked.Findings); summary != "" {
				reason += ": " + summary
			}
			return Action{Kind: ActionEscalate, Reason: reason}
		}

		missing := missingRoles(step.Review, stepRuns)
		if len(missing) > 0 {
			return Action{Kind: ActionSpawn, StepID: step.ID, Roles: missing, Round: round, SHA: shas[step.Of]}
		}
		for _, run := range stepRuns {
			if run.State != RunStateCompleted {
				return Action{Kind: ActionWait, StepID: step.ID, Round: round}
			}
		}

		if hasChangesRequested(stepRuns) {
			// Decision A: a review step with no loop (a story after_tasks
			// shape) has no fix step to retry - it can only escalate.
			if step.Loop == nil {
				return Action{Kind: ActionEscalate, Reason: fmt.Sprintf("%s changes requested", step.ID)}
			}
			fix := step.Loop.Fix
			if fix == "" {
				fix = step.Of
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

// pinnedFrom returns the step index from which steps must be matched to the
// exact current round (see Next's doc comment for the full rule). Steps
// before that index carry forward from their latest run at or before the
// current round.
func pinnedFrom(s Spec, runs []Run, round int) int {
	if round <= 1 {
		return 0
	}
	prevRound := round - 1

	fixIdx := func(step Step) int {
		fixID := step.Of
		if step.Loop != nil && step.Loop.Fix != "" {
			fixID = step.Loop.Fix
		}
		return stepIndex(s, fixID)
	}

	// Phase 1: the review step (if any) that requested changes - or was
	// blocked; both are "the reviewer wants this redone" - last round is
	// what drove this round's bump; pin from its fix step's index. This
	// counts even when the verdict-carrying row is itself stale (see
	// Next's doc comment).
	if idx := pinFromVerdict(s, runs, prevRound, fixIdx); idx != -1 {
		return idx
	}

	// Phase 2: no changes_requested/blocked verdict explains the bump, but
	// a stale row (any verdict or state - pass, still active, even a
	// crashed reviewer) at the previous round still means its step is the
	// fix loop that needs re-pinning; the row's sha just isn't trustworthy
	// evidence of anything else.
	if idx := pinFromStale(s, runs, prevRound, fixIdx); idx != -1 {
		return idx
	}

	// Phase 3: no round-1 review requested changes and none was stale
	// (e.g. an orchestrator resume after a crash escalation, not a normal
	// fix loop): pin from whichever step failed or was cancelled last
	// round.
	bestIdx := -1
	for i, step := range s.Steps {
		for _, run := range runsFor(runs, step.ID, prevRound) {
			if run.State == RunStateFailed || run.State == RunStateCancelled {
				if bestIdx == -1 || i < bestIdx {
					bestIdx = i
				}
			}
		}
	}
	if bestIdx != -1 {
		return bestIdx
	}

	// Nothing at the previous round explains the bump; pin everything.
	return 0
}

// pinFromVerdict returns the lowest fix-step index among review steps whose
// prevRound runs include a changes_requested or blocked verdict - regardless
// of whether that run's sha is stale - or -1 if none did.
func pinFromVerdict(s Spec, runs []Run, prevRound int, fixIdx func(Step) int) int {
	best := -1
	for _, step := range s.Steps {
		if len(step.Review) == 0 {
			continue
		}
		cr := false
		for _, run := range runsFor(runs, step.ID, prevRound) {
			if run.Verdict == VerdictChangesRequested || run.Verdict == VerdictBlocked {
				cr = true
				break
			}
		}
		if !cr {
			continue
		}
		if idx := fixIdx(step); idx != -1 && (best == -1 || idx < best) {
			best = idx
		}
	}
	return best
}

// pinFromStale returns the lowest fix-step index among review steps whose
// prevRound runs include one whose sha no longer matches what its reviewed
// step (Of) completed with by the end of that round - the shape left behind
// by a resume after a same-round stale-review escalation - or -1 if none
// did.
func pinFromStale(s Spec, runs []Run, prevRound int, fixIdx func(Step) int) int {
	best := -1
	for _, step := range s.Steps {
		if len(step.Review) == 0 {
			continue
		}
		ofSHA := completedSHA(runsFor(runs, step.Of, prevRound))
		if ofSHA == "" {
			continue
		}
		stale := false
		for _, run := range runsFor(runs, step.ID, prevRound) {
			if run.SHA != "" && run.SHA != ofSHA {
				stale = true
				break
			}
		}
		if !stale {
			continue
		}
		if idx := fixIdx(step); idx != -1 && (best == -1 || idx < best) {
			best = idx
		}
	}
	return best
}

// completedSHA returns the sha of the first completed run in runs, or "" if
// none completed.
func completedSHA(runs []Run) string {
	for _, run := range runs {
		if run.State == RunStateCompleted {
			return run.SHA
		}
	}
	return ""
}

func stepIndex(s Spec, id string) int {
	for i, st := range s.Steps {
		if st.ID == id {
			return i
		}
	}
	return -1
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

// staleReviews splits stepRuns into the ones that are still fresh (sha
// matches want, or carries no sha at all - never considered stale) and
// handles the stale ones: a stale row from an earlier round is dropped (nil
// alongside the fresh ones is fine - that role is then just "missing"), but
// a stale row already in the current round returns an escalate Action
// instead, since a Spawn for that same (step, round, role) key would be a
// no-op.
func staleReviews(step Step, runs []Run, want string, round int) ([]Run, *Action) {
	var fresh []Run
	for _, run := range runs {
		if run.SHA == "" || run.SHA == want {
			fresh = append(fresh, run)
			continue
		}
		if run.Round == round {
			reason := fmt.Sprintf("%s reviewed %s, but %s is now at %s", run.Role, sha7(run.SHA), step.Of, sha7(want))
			return nil, &Action{Kind: ActionEscalate, Reason: reason}
		}
		// Stale, but from an earlier round: drop it.
	}
	return fresh, nil
}

// sha7 is the short form of a sha: its first 7 characters, or the whole
// thing if it's shorter than that.
func sha7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
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
	unit := "auto-retries"
	if run.AutoRetries == 1 {
		unit = "auto-retry"
	}
	return fmt.Sprintf("%s %s after %d %s", run.Role, run.State, run.AutoRetries, unit)
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

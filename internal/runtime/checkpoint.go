package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

const verifyMissing = "Verification evidence missing: record what was run to verify this work before completing."
const tddMissingCopy = `TDD evidence missing: record the failing test run (phase: "red", ok: false) before the passing run (phase: "green", ok: true) in this round.`
const pausedTool = "paused: finish your handoff and stop."

var gatedRoles = []Role{RoleCoder, RoleDebugger, RoleMechanical}
var pauseAllowedKinds = []CheckpointKind{Handoff, BlockedCkp, FailedCkp}

// checkpointNotify is the §17.5 notification for a checkpoint kind: the wire
// kind and which of {name, KEY, title} its template needs. A kind absent from
// this map raises nothing — that includes Blocked, which §17.5's table has no
// row for at all.
//
// Found while wiring up the e2e harness (Batch 6b/P2 Task 37): every one of
// these calls used to go straight to Notify.Raise with only AgentName/ItemKey
// set and no Args, and notify.Render fails closed on a template placeholder
// with no argument (by design, so a missed key is a test failure and not a
// literal "{name}" in a banner) — so every accepted/failed checkpoint raised
// here rolled its whole transaction back with "notify: agent.X is missing
// name, KEY[, title]" the moment a real notify.Service was wired, and the
// Completed case used "agent.completed", a kind notify.Rules never defined at
// all ("notify: unknown kind"), so every completed checkpoint failed the same
// way. Nothing in internal/runtime's own test suite could have caught either
// bug: fakeNotifier.Raise (agents_test.go) just records the call, it never
// calls Render, so passing no Args was invisible until something exercised a
// real, wired notify.Service end to end — which no test did before the e2e
// harness. Originally believed not to need touching: internal/runtime's
// other Notify.Raise/notify() call sites already pass an Args map (grep -n
// "Args:" internal/runtime/*.go) — but "Args is present" turned out not to
// mean "Args is complete". A post-review pass (still Batch 6b) found more of
// the same class this grep couldn't see: OnWorktreeRetained's Args had
// {path, detail} but not the {ROOT-KEY} its template needs, and
// OnRequestOpened's had only {KEY}, not {name, prompt}. fakeNotifier.Raise
// now validates every raised kind's Args against the real §17.5 template
// (via internal/notifyrules, a leaf-package extraction of notify.Rules that
// runtime's own tests can import without the cycle notify itself would
// create), so this class of bug fails the first test that exercises the
// call site, not just an end-to-end run.
var checkpointNotify = map[CheckpointKind]struct {
	kind             string
	name, key, title bool
}{
	Accepted:     {"agent.accepted", true, true, true},
	CompletedCkp: {"item.completed", false, true, true},
	FailedCkp:    {"agent.failed", true, true, false},
}

// CheckpointInput is swarm_checkpoint's input (§8.1).
type CheckpointInput struct {
	Kind         CheckpointKind
	ItemKey      string
	Summary      string
	Resolution   string
	Next         []string
	Blockers     []string
	Git          []GitRef
	Verification []Verify
	Artifacts    []string
	Processed    []string
	// Verdict and Findings are spec B5: a reviewer/ui_reviewer agent with a
	// workflow run must set Verdict on a completed checkpoint; every other
	// role is refused if it sets one at all (isReviewerRole/validVerdict
	// below own the exact rules). Findings reuses workflow.Finding rather
	// than defining a second copy.
	Verdict  string
	Findings []workflow.Finding
	// RequestID is I11's idempotency key, scoped to the calling MCP session:
	// a repeated (session, RequestID) pair replays the first checkpoint's
	// result instead of writing a second one. Empty means "no idempotency,
	// just run once" (Store.Idempotent's own documented behavior).
	RequestID string
}

// CheckpointResult is swarm_checkpoint's result.
type CheckpointResult struct {
	CheckpointID string
	ItemStatus   items.Status
	// ItemRevision is the item's revision right after this checkpoint's own
	// transition attempt (a real bump, or none if it was a same-state no-op --
	// e.g. two agents both writing `completed` on one item, the second finding
	// it already in_review). Handing this back means a caller's very next
	// swarm_items update can use the correct value outright instead of
	// guessing whether this checkpoint bumped it, then re-reading to find out.
	ItemRevision int
}

// verifyOK is L24. Evidence is every verification entry of this attempt, earlier
// checkpoints included, so a pause and resume inside one attempt keeps it.
// It checks that at least one verification command was recorded, and returns
// "" when satisfied or the specific reason otherwise: a caller that sent
// entries with the wrong field names (so every Cmd came back empty) needs a
// different message than one that sent nothing at all, or it'll keep
// resending the same malformed shape under a new guessed key.
func verifyOK(prior, now []Verify) string {
	all := append(append([]Verify{}, prior...), now...)
	if len(all) == 0 {
		return verifyMissing
	}
	for _, v := range all {
		if strings.TrimSpace(v.Cmd) != "" {
			return ""
		}
	}
	plural := "entries"
	if len(all) == 1 {
		plural = "entry"
	}
	return fmt.Sprintf(`%s %d verification %s given, but none has a non-empty "cmd" -- each entry needs {"cmd": "...", "ok": true}.`,
		verifyMissing, len(all), plural)
}

// workflowRun is the slice of an agent's current workflow_runs row (spec B1)
// that checkpoint gating and sibling-closing need. P9's engine (not built
// yet) owns the row's full lifecycle (state, sha, ended_at, auto_retries);
// P8 only reads it and writes verdict/findings/sha onto it.
type workflowRun struct {
	ID, WorkflowID, StepID, Role, State, SHA string
	Round                                    int
	// CreatedAt is when this round's row was inserted -- before any
	// checkpoint of this round (spawn / RetryFix insert the row, then the
	// step agent starts working), so it's the tdd/verify gates' round-scope
	// boundary: an AutoRetry crash re-attempt reuses this same row, so
	// evidence from an earlier attempt of the same round is >= CreatedAt
	// too and still counts.
	CreatedAt time.Time
}

// workflowRunFor returns agentID's latest workflow_runs row (highest round,
// then most recent), or ok=false if it has none -- a legacy agent, or (until
// P9 wires the engine) a workflow agent no test has seeded a row for.
func (s *Store) workflowRunFor(ctx context.Context, tx *sql.Tx, agentID string) (workflowRun, bool, error) {
	var r workflowRun
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT id, workflow_id, step_id, round, role, state, COALESCE(sha, ''), created_at
		FROM workflow_runs WHERE agent_id = ? ORDER BY round DESC, created_at DESC LIMIT 1`, agentID).
		Scan(&r.ID, &r.WorkflowID, &r.StepID, &r.Round, &r.Role, &r.State, &r.SHA, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowRun{}, false, nil
	}
	if err != nil {
		return workflowRun{}, false, err
	}
	r.CreatedAt = db.FromMillis(created)
	return r, true, nil
}

// stepFor returns the step named stepID out of spec (nil-safe), or ok=false.
func stepFor(spec *workflow.Spec, stepID string) (workflow.Step, bool) {
	if spec == nil {
		return workflow.Step{}, false
	}
	// EffectiveSteps, not spec.Steps directly (fix round 2, finding 1): a
	// resolved story spec keeps its one review step in AfterTasks, with
	// Steps empty -- Next and Render both promote it the same way before
	// looking anything up by id.
	for _, st := range spec.EffectiveSteps() {
		if st.ID == stepID {
			return st, true
		}
	}
	return workflow.Step{}, false
}

// isReviewerRole reports whether r is one of the two roles that review a
// workflow step (spec B5: only these may ever set a verdict).
func isReviewerRole(r Role) bool { return r == RoleReviewer || r == RoleUIReviewer }

// validVerdict reports whether v is one of the three verdicts a reviewer may
// record.
func validVerdict(v workflow.Verdict) bool {
	return v == workflow.VerdictPass || v == workflow.VerdictChangesRequested || v == workflow.VerdictBlocked
}

// validSeverities are the only legal workflow.Finding.Severity values (fix
// round 1, finding 7).
var validSeverities = []string{"critical", "major", "minor", "nit"}

func validSeverity(sev string) bool { return slices.Contains(validSeverities, sev) }

// hasMajorOrCritical reports whether any finding is severity "major" or
// "critical" -- a pass verdict can't carry either (spec B5).
func hasMajorOrCritical(fs []workflow.Finding) bool {
	for _, f := range fs {
		if f.Severity == "major" || f.Severity == "critical" {
			return true
		}
	}
	return false
}

func shaMatches(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) >= 7 && len(b) >= 7 {
		return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
	}
	return false
}

func (s *Store) hasIntegrationVerifyPassed(ctx context.Context, tx *sql.Tx, itemID string, currentVerify []Verify, cmd string) (bool, error) {
	for _, v := range currentVerify {
		if v.Cmd == cmd && v.OK {
			return true, nil
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT verify_json FROM checkpoints WHERE item_id = ?`, itemID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var vs []Verify
		if err := json.Unmarshal([]byte(raw), &vs); err != nil {
			continue
		}
		for _, v := range vs {
			if v.Cmd == cmd && v.OK {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

func (s *Store) hasFinalReviewPassed(ctx context.Context, tx *sql.Tx, itemID string, integratedSHA string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.git_json FROM checkpoints c
		JOIN agents a ON a.id = c.agent_id
		WHERE c.item_id = ? AND c.verdict = 'pass' AND a.role IN ('reviewer', 'ui_reviewer')`, itemID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var refs []GitRef
		if err := json.Unmarshal([]byte(raw), &refs); err == nil {
			for _, ref := range refs {
				if shaMatches(ref.SHA, integratedSHA) {
					return true, nil
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	wfRows, err := tx.QueryContext(ctx, `SELECT r.sha FROM workflow_runs r
		JOIN workflows w ON w.id = r.workflow_id
		WHERE w.item_id = ? AND r.verdict = 'pass' AND r.role IN ('reviewer', 'ui_reviewer')`, itemID)
	if err != nil {
		return false, err
	}
	defer wfRows.Close()
	for wfRows.Next() {
		var runSHA sql.NullString
		if err := wfRows.Scan(&runSHA); err != nil {
			return false, err
		}
		if runSHA.Valid && shaMatches(runSHA.String, integratedSHA) {
			return true, nil
		}
	}
	return false, wfRows.Err()
}

// applyGates enforces the completed step's declared gates (spec B5) for an
// agent with a workflow run -- the replacement for verifyOK on such agents.
// Unit 8.1 only wires the dispatch (no gate does anything yet, so a step
// that only declares gates has nothing to check until a later unit fills
// its case in); 8.4 adds tdd/verify, 8.5 adds
// commit/artifact:design/artifact:notes.
func (s *Store) applyGates(ctx context.Context, tx *sql.Tx, it items.Item, run workflowRun, a Agent, in CheckpointInput) error {
	step, ok := stepFor(it.Workflow, run.StepID)
	if !ok {
		// A corrupted or stale run row (or an item whose workflow_json went
		// missing) must refuse, not silently enforce zero gates (fix round
		// 1, finding 4): stepFor's zero-value Step has no Gates, so the
		// loop below would just do nothing.
		return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
			`workflow step %q not found on %s; ask your orchestrator.`, run.StepID, it.Key)}
	}
	for _, g := range step.Gates {
		var err error
		switch g {
		case workflow.GateTDD:
			err = s.tddGate(ctx, tx, it, run, a, in)
		case workflow.GateVerify:
			err = s.verifyGate(ctx, tx, it, run, a, in)
		case workflow.GateCommit:
			err = s.commitGate(ctx, tx, a, in, run)
		case workflow.GateArtifactDesign:
			err = s.artifactGate(ctx, tx, it, a, in, "design", "designs")
		case workflow.GateArtifactNotes:
			err = s.artifactGate(ctx, tx, it, a, in, "research", "research")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// verifySince collects every verification entry agentID recorded at or
// after since, in order -- the tdd/verify gates' shared "this round" scope
// (workflowRun.CreatedAt is the boundary; see its doc comment).
func (s *Store) verifySince(ctx context.Context, tx *sql.Tx, agentID string, since time.Time) ([]Verify, error) {
	rows, err := tx.QueryContext(ctx, `SELECT verify_json FROM checkpoints
		WHERE agent_id = ? AND created_at >= ? ORDER BY created_at, rowid`, agentID, db.Millis(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verify
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var vs []Verify
		if err := json.Unmarshal([]byte(raw), &vs); err != nil {
			return nil, fmt.Errorf("checkpoints verify_json: %w", err)
		}
		out = append(out, vs...)
	}
	return out, rows.Err()
}

// findFixStepsFor returns every review step in spec whose loop retries
// buildStepID (`Loop.Fix`, falling back to `Of` when `Loop.Fix` is unset --
// spec B5, fix round 1's R3), or nil if none targets it at all (a step with
// no review, e.g. mechanical/research; or the review step for some OTHER
// build step in a multi-loop workflow). More than one review step can share
// a fix target (e.g. review-code and review-ui both reviewing "build",
// fix round 2's finding 2) -- every one of them is read, not just the
// first match.
func findFixStepsFor(spec *workflow.Spec, buildStepID string) []workflow.Step {
	if spec == nil {
		return nil
	}
	var out []workflow.Step
	for _, st := range spec.EffectiveSteps() {
		if len(st.Review) == 0 {
			continue
		}
		fix := st.Of
		if st.Loop != nil && st.Loop.Fix != "" {
			fix = st.Loop.Fix
		}
		if fix == buildStepID {
			out = append(out, st)
		}
	}
	return out
}

// fixRoundFindings collects reviewStepID's reviewer/ui_reviewer findings at
// round, and reports whether any of those rows actually requested changes
// or blocked (R3): a review step can have a row at that round with only a
// passing verdict (nothing to retry), which is NOT a fix round at all --
// distinct from a genuine fix round whose rows simply carry zero findings
// (e.g. a bare `blocked` verdict, no structured findings). hasBlocking is
// that distinction; findings is every row's findings, merged (a reviewer
// who passed but still left findings counts too, same as B4's
// mergeFindings).
func (s *Store) fixRoundFindings(ctx context.Context, tx *sql.Tx, workflowID, reviewStepID string, round int) (findings []workflow.Finding, hasBlocking bool, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(verdict, ''), COALESCE(findings_json, '[]') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = ? AND round = ? AND role IN ('reviewer', 'ui_reviewer')`,
		workflowID, reviewStepID, round)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var verdict, raw string
		if err := rows.Scan(&verdict, &raw); err != nil {
			return nil, false, err
		}
		if verdict == string(workflow.VerdictChangesRequested) || verdict == string(workflow.VerdictBlocked) {
			hasBlocking = true
		}
		var fs []workflow.Finding
		if err := json.Unmarshal([]byte(raw), &fs); err != nil {
			return nil, false, fmt.Errorf("workflow_runs %s findings_json: %w", reviewStepID, err)
		}
		findings = append(findings, fs...)
	}
	return findings, hasBlocking, rows.Err()
}

// hasRedBeforeGreen reports whether entries contains a {phase:"red",
// ok:false} entry for unit, followed later (in slice order) by a
// {phase:"green", ok:true} entry for the same unit. unit 0 means
// "untagged" (a non-batched task's own evidence).
func hasRedBeforeGreen(entries []Verify, unit int) bool {
	red := false
	for _, v := range entries {
		if v.Unit != unit {
			continue
		}
		switch {
		case v.Phase == "red" && !v.OK:
			red = true
		case v.Phase == "green" && v.OK && red:
			return true
		}
	}
	return false
}

// anyUnitHasRedBeforeGreen is hasRedBeforeGreen without pinning to one unit
// -- the fix-round "package-wide" requirement (spec B5): at least one unit
// (tagged or untagged) has its own red-before-green pair.
func anyUnitHasRedBeforeGreen(entries []Verify) bool {
	red := map[int]bool{}
	for _, v := range entries {
		switch {
		case v.Phase == "red" && !v.OK:
			red[v.Unit] = true
		case v.Phase == "green" && v.OK && red[v.Unit]:
			return true
		}
	}
	return false
}

// tddOK is the pure part of the tdd gate (spec B5): entries is the round's
// accumulated verification evidence. required is the set of unit numbers
// that each need their own red-before-green pair (nil for a non-batched
// task, or a fix round naming no specific unit); packageWide additionally
// accepts any single unit's pair when no specific unit is required. It
// returns ok, and (only when required is non-empty) which units are still
// missing evidence.
func tddOK(entries []Verify, required []int, packageWide bool) (ok bool, missing []int) {
	if len(required) > 0 {
		for _, u := range required {
			if !hasRedBeforeGreen(entries, u) {
				missing = append(missing, u)
			}
		}
		return len(missing) == 0, missing
	}
	if packageWide {
		return anyUnitHasRedBeforeGreen(entries), nil
	}
	return hasRedBeforeGreen(entries, 0), nil
}

func tddMissingUnitsError(missing []int) error {
	parts := make([]string, len(missing))
	for i, u := range missing {
		parts[i] = strconv.Itoa(u)
	}
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
		`TDD evidence missing for unit(s) %s: record red then green with "unit": <n>.`, strings.Join(parts, ","))}
}

// tddGate is the workflow tdd gate (spec B5, ruling-tdd-fix-rounds.md,
// ruling-tdd-followups.md). Skipped entirely when the item is tdd_exempt.
func (s *Store) tddGate(ctx context.Context, tx *sql.Tx, it items.Item, run workflowRun, a Agent, in CheckpointInput) error {
	if it.TddExempt != "" {
		return nil
	}
	prior, err := s.verifySince(ctx, tx, a.ID, run.CreatedAt)
	if err != nil {
		return err
	}
	entries := append(append([]Verify{}, prior...), in.Verification...)
	batched := len(it.Units) > 0

	everyUnit := func() []int {
		if !batched {
			return nil
		}
		req := make([]int, len(it.Units))
		for i := range it.Units {
			req[i] = i + 1
		}
		return req
	}

	var required []int
	packageWide := false
	if fixSteps := findFixStepsFor(it.Workflow, run.StepID); len(fixSteps) > 0 {
		// Fix round 2, finding 2: more than one review step can target the
		// same build step. Blocking if ANY of them requested changes or
		// blocked; findings merge across all of them (mirrors B4's
		// mergeFindings, which does the same across a single step's
		// several reviewer roles).
		var findings []workflow.Finding
		var hasBlocking bool
		for _, fixStep := range fixSteps {
			fnd, blocking, err := s.fixRoundFindings(ctx, tx, run.WorkflowID, fixStep.ID, run.Round-1)
			if err != nil {
				return err
			}
			findings = append(findings, fnd...)
			hasBlocking = hasBlocking || blocking
		}
		switch {
		case !hasBlocking:
			// This step's own first run: no changes_requested/blocked row
			// at round-1 explains a retry of it, however high the
			// workflow's round counter climbed for some OTHER step's fix
			// loop (a multi-loop spec) -- never mistake that for a fix
			// round of THIS step.
			required = everyUnit()
		case len(findings) == 0:
			// A genuine fix round (blocked/changes_requested), but no
			// structured findings at all (e.g. a bare blocked verdict,
			// resumed): still needs its one pair, never "nothing required".
			packageWide = true
		default:
			units := map[int]bool{}
			for _, f := range findings {
				u := f.Unit
				if !batched {
					u = 0 // unit tags don't apply to a non-batched task
				}
				if u == 0 {
					packageWide = true
					continue
				}
				units[u] = true
			}
			for u := range units {
				required = append(required, u)
			}
			sort.Ints(required)
		}
	} else {
		// No review step targets this step at all (no review, e.g.
		// mechanical/research; or this is round 1 with nothing to retry
		// yet): always a first run.
		required = everyUnit()
	}

	ok, missing := tddOK(entries, required, packageWide)
	if ok {
		return nil
	}
	if len(missing) > 0 {
		return tddMissingUnitsError(missing)
	}
	return errors.New(tddMissingCopy)
}

// verifyDeclaredOK reports whether entries contains an ok:true entry whose
// cmd, whitespace-normalized, equals or contains want (also
// whitespace-normalized).
func verifyDeclaredOK(entries []Verify, want string) bool {
	w := normalizeWhitespace(want)
	for _, v := range entries {
		if v.OK && strings.Contains(normalizeWhitespace(v.Cmd), w) {
			return true
		}
	}
	return false
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// verifyGate is the workflow verify gate (spec B5): every item.Verify command
// must be recorded ok:true, by containment, within this round's evidence.
func (s *Store) verifyGate(ctx context.Context, tx *sql.Tx, it items.Item, run workflowRun, a Agent, in CheckpointInput) error {
	prior, err := s.verifySince(ctx, tx, a.ID, run.CreatedAt)
	if err != nil {
		return err
	}
	entries := append(append([]Verify{}, prior...), in.Verification...)
	var missing []string
	for _, want := range it.Verify {
		if !verifyDeclaredOK(entries, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
		"Declared verify commands not recorded as passing: %s.", strings.Join(missing, "; "))}
}

// rwWorktree is one read-write worktree share the commit gate checks.
type rwWorktree struct{ Repo, Path string }

// rwWorktreesFor returns every currently-held 'rw' worktree reservation for
// agentID, repo name and worktree path, ordered by repo name for
// deterministic sha selection when a task shares more than one repo.
func (s *Store) rwWorktreesFor(ctx context.Context, tx *sql.Tx, agentID string) ([]rwWorktree, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.name, w.path FROM worktree_reservations wr
		JOIN worktrees w ON w.id = wr.worktree_id
		JOIN repos r ON r.id = w.repo_id
		WHERE wr.agent_id = ? AND wr.mode = 'rw' AND wr.released_at IS NULL
		ORDER BY r.name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rwWorktree
	for rows.Next() {
		var w rwWorktree
		if err := rows.Scan(&w.Repo, &w.Path); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// gitHead runs `git rev-parse HEAD` at path.
func (s *Store) gitHead(ctx context.Context, path string) (string, error) {
	runner := s.Exec
	if runner == nil {
		runner = execx.Run
	}
	out, err := runner(ctx, "git", "-C", path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func dirtyRepoError(repo string) error {
	return &items.Error{Code: items.CodeBadRequest,
		Message: fmt.Sprintf("Commit your work before completing: %s is dirty", repo)}
}

// commitGate is the workflow commit gate (spec B5): the checkpoint must
// declare git, every declared entry must claim clean, and for each rw
// worktree actually shared to this agent the real tree must be clean too
// (not just trust the caller's own dirty:false) with HEAD matching the
// entry recorded for that repo. The matched HEAD sha is stored on the run
// (its own lifecycle -- state, ended_at -- is P9's, not touched here).
func (s *Store) commitGate(ctx context.Context, tx *sql.Tx, a Agent, in CheckpointInput, run workflowRun) error {
	if len(in.Git) == 0 {
		return &items.Error{Code: items.CodeBadRequest,
			Message: "Completed needs git: [{repo, branch, sha, dirty:false}]."}
	}
	byRepo := map[string]GitRef{}
	for _, g := range in.Git {
		if g.Dirty {
			return dirtyRepoError(g.Repo)
		}
		byRepo[g.Repo] = g
	}
	wts, err := s.rwWorktreesFor(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	if len(wts) == 0 {
		return &items.Error{Code: items.CodeBadRequest,
			Message: "Commit your work before completing: no rw worktree shared with you"}
	}
	var sha string
	for _, wt := range wts {
		dirty, err := s.Worktree.DirtyStrict(ctx, wt.Path)
		if err != nil {
			return err
		}
		if dirty {
			return dirtyRepoError(wt.Repo)
		}
		g, ok := byRepo[wt.Repo]
		if !ok {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"Commit your work before completing: no git entry for %s", wt.Repo)}
		}
		head, err := s.gitHead(ctx, wt.Path)
		if err != nil {
			return err
		}
		if head != g.SHA {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"Commit your work before completing: %s HEAD is %s, checkpoint says %s",
				wt.Repo, workflow.SHA7(head), workflow.SHA7(g.SHA))}
		}
		if sha == "" {
			sha = head
		}
	}
	if sha != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET sha = ? WHERE id = ?`, sha, run.ID); err != nil {
			return err
		}
	}
	return nil
}

// registerArtifactAsDaemon registers a design/research artifact the
// artifact gate found (spec B5), as the daemon rather than through
// swarm_artifact's orchestrator-only RegisterArtifact -- a designer or
// researcher, not necessarily an orchestrator, writes these. It runs inside
// WriteCheckpoint's own transaction, on every completed checkpoint the
// artifact gate passes: a fix round's revised file gets a new revision
// (finding 8), deduped when the content is byte-identical to the current
// head revision (so an unchanged file across rounds doesn't pile up
// pointless revisions).
func (s *Store) registerArtifactAsDaemon(ctx context.Context, tx *sql.Tx, itemID, agentID, kind, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	newSHA := sha256Hex(string(body))
	var artifactID string
	var revision int
	var prevSHA, prevSectionsJSON string
	err = tx.QueryRowContext(ctx, `SELECT a.id, a.head_revision, r.sha256, r.sections_json FROM artifacts a
		JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.head_revision
		WHERE a.item_id = ? AND a.path = ?`, itemID, path).Scan(&artifactID, &revision, &prevSHA, &prevSectionsJSON)
	now := db.Millis(s.Now())
	bumped := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		artifactID, revision = ids.New("art"), 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
			(id, item_id, kind, path, head_revision, created_by, created_at)
			VALUES (?, ?, ?, ?, 1, ?, ?)`, artifactID, itemID, kind, path, agentID, now); err != nil {
			return err
		}
	case err != nil:
		return err
	case prevSHA == newSHA:
		return nil // dedupe: identical content already the head revision
	default:
		revision++
		bumped = true
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET head_revision = ? WHERE id = ?`,
			revision, artifactID); err != nil {
			return err
		}
	}
	sections := SplitSections(string(body))
	sectionsJSON, err := json.Marshal(sections)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_revisions
		(artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, artifactID, revision, newSHA, string(body), string(sectionsJSON), now); err != nil {
		return err
	}
	if !bumped {
		return nil // fresh registration: no open approvals could exist yet
	}
	// Fix round 2, finding 4: a revision bump must stale open approvals the
	// same way RegisterArtifact's own orchestrator-facing path does, so a
	// fix round's revised design/notes don't leave an approve_section
	// request pinned to superseded content.
	var prevSections []ArtifactSection
	if err := json.Unmarshal([]byte(prevSectionsJSON), &prevSections); err != nil {
		return fmt.Errorf("artifact_revisions sections_json: %w", err)
	}
	_, err = s.staleApprovals(ctx, tx, artifactID, sectionsChanged(prevSections, sections))
	return err
}

// artifactGate is the workflow artifact:design / artifact:notes gate (spec
// B5): artifacts must contain a readable file under
// ~/.swarm/<dir>/<ROOT-KEY>/, which is then registered on the task.
func (s *Store) artifactGate(ctx context.Context, tx *sql.Tx, it items.Item, a Agent, in CheckpointInput, kind, dir string) error {
	root := filepath.Join(s.Home, dir, it.RootKey)
	prefix := root + string(filepath.Separator)
	home, _ := os.UserHomeDir() // "" on failure: expandHome then leaves a leading ~ alone, matched by nothing
	for _, raw := range in.Artifacts {
		p := filepath.Clean(expandHome(raw, home))
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			continue
		}
		return s.registerArtifactAsDaemon(ctx, tx, it.ID, a.ID, kind, p)
	}
	noun := "design file"
	if kind == "research" {
		noun = "research notes"
	}
	// The real, resolved directory (finding 8) -- not a hardcoded
	// "~/.swarm/..." that would mislead when SWARM_HOME differs.
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
		"Completed needs your %s in artifacts (under %s/).", noun, root)}
}

// expandHome resolves a leading "~" (or "~/...") in p against home, leaving
// every other path (absolute, relative, or already resolved) untouched.
// Takes home as a parameter rather than calling os.UserHomeDir() itself, so
// it's a plain deterministic function to test (finding 8).
func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// requiredArtifactKind returns the artifact kind a root item's completed
// checkpoint must have on record before it's accepted, or "" if none is
// required (not a gated root type, or tdd_exempt). Only epic and bug roots
// are gated -- chore is the codebase's designated lightweight root type,
// and story/task are never roots.
func requiredArtifactKind(it items.Item) string {
	if it.TddExempt != "" {
		return ""
	}
	switch it.Type {
	case items.Epic:
		return "plan"
	case items.Bug:
		return "debug_report"
	default:
		return ""
	}
}

// hasArtifact reports whether an artifact of the given kind is registered
// against itemID, queried inside the caller's own transaction so it sees
// anything registered earlier in the same request and can't race a
// concurrent registration.
func (s *Store) hasArtifact(ctx context.Context, tx *sql.Tx, itemID, kind string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM artifacts WHERE item_id = ? AND kind = ? LIMIT 1`,
		itemID, kind).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func jsonArray[T any](v []T) string {
	if v == nil {
		v = []T{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// tryTransition applies a checkpoint's item-status effect. A denied transition
// (the item was not in the expected state — e.g. a reviewer completing a task
// still sitting at Ready) is a no-op: the checkpoint itself always records,
// and the derived status only moves when the state machine allows it. That is
// not silent, though — it leaves the item's displayed status disagreeing with
// what was just recorded, so it is logged (matching changedFiles' own
// lenient-but-logged pattern below). Any other error (a DB failure) still
// propagates.
func (s *Store) tryTransition(ctx context.Context, tx *sql.Tx, key string, to items.Status) error {
	_, err := s.Items.TransitionTx(ctx, tx, key, to, items.Daemon())
	if err == nil {
		return nil
	}
	var ie *items.Error
	if errors.As(err, &ie) && ie.Code == items.CodeTransitionDenied {
		s.logf("checkpoint: %s stays put, denied moving to %s: %v", key, to, err)
		return nil
	}
	return err
}

// isDescendant reports whether ancestorID is a strict ancestor of id.
func (s *Store) isDescendant(ctx context.Context, tx *sql.Tx, id, ancestorID string) (bool, error) {
	var found bool
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE up(id) AS (
			SELECT parent_id FROM items WHERE id = ? AND parent_id IS NOT NULL
			UNION SELECT i.parent_id FROM items i JOIN up ON i.id = up.id WHERE i.parent_id IS NOT NULL)
		SELECT EXISTS (SELECT 1 FROM up WHERE id = ?)`, id, ancestorID).Scan(&found)
	return found, err
}

// priorVerify collects every verification entry recorded so far this attempt.
func (s *Store) priorVerify(ctx context.Context, tx *sql.Tx, agentID string, attempt int) ([]Verify, error) {
	rows, err := tx.QueryContext(ctx, `SELECT verify_json FROM checkpoints
		WHERE agent_id = ? AND attempt = ? ORDER BY created_at, rowid`, agentID, attempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verify
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var vs []Verify
		json.Unmarshal([]byte(raw), &vs)
		out = append(out, vs...)
	}
	return out, rows.Err()
}

// parseShortstat reads the file count out of `git diff --shortstat`'s one line
// ("2 files changed, 10 insertions(+), 2 deletions(-)"); anything else is 0.
func parseShortstat(out string) int {
	fields := strings.Fields(out)
	for i, f := range fields {
		if i > 0 && strings.HasPrefix(f, "file") {
			n, _ := strconv.Atoi(fields[i-1])
			return n
		}
	}
	return 0
}

// changedFiles is the daemon-computed diff for an orchestrator's own completed
// checkpoint (L24). A git error, or a repo with no worktree row, counts as 0
// changed files and is logged: the lenient direction, since "agents don't report
// this number" and a git hiccup must not falsely gate an orchestrator.
func (s *Store) changedFiles(ctx context.Context, refs []GitRef) int {
	total := 0
	for _, g := range refs {
		var path, base string
		if err := s.DB.QueryRowContext(ctx, `SELECT w.path, w.base_sha FROM worktrees w
			JOIN repos r ON r.id = w.repo_id WHERE r.name = ? ORDER BY w.created_at DESC LIMIT 1`,
			g.Repo).Scan(&path, &base); err != nil {
			continue
		}
		runner := s.Exec
		if runner == nil {
			runner = execx.Run
		}
		out, err := runner(ctx, "git", "-C", path, "diff", "--shortstat", base+".."+g.SHA)
		if err != nil {
			s.logf("checkpoint: changedFiles %s: %v", g.Repo, err)
			continue
		}
		total += parseShortstat(string(out))
	}
	return total
}

// itemTypePlural pluralizes an item.Type for the completed-checkpoint gate's
// user-facing copy ("not storys" reads wrong; "not stories" doesn't).
func itemTypePlural(t items.Type) string {
	if t == items.Story {
		return "stories"
	}
	return string(t) + "s"
}

// siblingTeardown is one other live session on the item a completed
// checkpoint just landed on, closed out-of-band with WriteCheckpoint's own
// transaction: killing a tmux pane isn't a SQL operation, so it happens after
// the tx commits, mirroring Cancel's own tmux/DB split (agents.go's Cancel).
type siblingTeardown struct {
	TmuxName, ProviderSessionID string
	Kind                        AgentKind
}

// closeCompletedSiblings is the deterministic half of "an orchestrator closes
// children marked completed, regardless of the report": a completed
// checkpoint on item X ends every OTHER live session still assigned to X, in
// the same transaction the checkpoint itself writes in, so nothing needs an
// orchestrator to remember a follow-up swarm_control call (the failure mode
// that left s11-tool-stubs running indefinitely after its own item had
// already gone to done under a different agent's checkpoint). Session state
// goes to 'completed', not 'cancelled' -- this is the success path a session
// simply never got to close out for itself, not an abandon. The caller's own
// agent is excluded: if it owns the item too, it is still mid-turn and must
// not be torn down under itself.
//
// Narrowed by spec B5, amended by fix round 1's R1: the filter depends on
// the ITEM, not the caller. On a workflow task, every sibling is only torn
// down when it has the same role as the caller (and, when the caller has a
// workflow run, the same step too) -- a reviewer completing must not close
// a builder, and vice versa, with NO exemption: an orchestrator directly
// completing a workflow task on a builder's behalf is not a designed path
// (P9's engine owns that task's lifecycle), so it gets the same narrow
// filter as anyone else. On a legacy task, an orchestrator caller keeps the
// original exemption and still closes every live sibling regardless of
// role -- the s11-tool-stubs incident this function exists to fix, kept
// byte-for-byte for legacy tasks (Review Focus 1); any other legacy caller
// is filtered to same role (no step concept without a workflow run).
func (s *Store) closeCompletedSiblings(ctx context.Context, tx *sql.Tx, itemID, callerAgentID string,
	callerRole Role, callerRun workflowRun, callerHasRun, isWorkflowItem bool, now time.Time) ([]siblingTeardown, error) {
	args := []any{itemID, callerAgentID}
	placeholders := make([]string, len(LiveStates))
	for i, st := range LiveStates {
		placeholders[i] = "?"
		args = append(args, string(st))
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.tmux_name, COALESCE(s.provider_session_id, ''),
			a2.id, a2.name, a2.kind, a2.root_item_id, a2.role
		FROM agents a2 JOIN sessions s ON s.id = (
			SELECT id FROM sessions WHERE agent_id = a2.id ORDER BY generation DESC, attempt DESC LIMIT 1)
		WHERE a2.item_id = ? AND a2.id != ? AND s.state IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	type sibling struct {
		sessionID, tmux, provider, agentID, name, rootItemID, role string
		kind                                                       AgentKind
	}
	var found []sibling
	for rows.Next() {
		var r sibling
		var kind string
		if err := rows.Scan(&r.sessionID, &r.tmux, &r.provider, &r.agentID, &r.name, &kind, &r.rootItemID, &r.role); err != nil {
			rows.Close()
			return nil, err
		}
		r.kind = AgentKind(kind)
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var out []siblingTeardown
	for _, r := range found {
		// Legacy items keep the orchestrator override; workflow items never
		// do (R1). Everyone else (any role on a legacy item, or ANY caller
		// including an orchestrator on a workflow item) is filtered to the
		// same role, plus the same step when the caller has a run.
		if !(callerRole == RoleOrchestrator && !isWorkflowItem) {
			if Role(r.role) != callerRole {
				continue
			}
			if callerHasRun {
				sibRun, sibHasRun, err := s.workflowRunFor(ctx, tx, r.agentID)
				if err != nil {
					return nil, err
				}
				if !sibHasRun || sibRun.StepID != callerRun.StepID {
					continue
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'completed', ended_at = ? WHERE id = ?`,
			db.Millis(now), r.sessionID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`,
			db.Millis(now), r.agentID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ?
			WHERE agent_id = ? AND released_at IS NULL`, db.Millis(now), r.agentID); err != nil {
			return nil, err
		}
		if err := s.publishAgentChanged(ctx, tx, r.name, r.rootItemID); err != nil {
			return nil, err
		}
		s.logf("checkpoint: closing %s, its item completed under %s", r.name, callerAgentID)
		out = append(out, siblingTeardown{TmuxName: r.tmux, ProviderSessionID: r.provider, Kind: r.kind})
	}
	return out, nil
}

// WriteCheckpoint is swarm_checkpoint (§8.1, L24).
func (s *Store) WriteCheckpoint(ctx context.Context, sessionID string, in CheckpointInput) (CheckpointResult, error) {
	var out CheckpointResult
	var toClose []siblingTeardown
	// P9: hoisted out of the closure the same way toClose is, so the
	// post-commit advance() trigger below can see which workflow (if any)
	// this checkpoint's agent belongs to.
	var wfRun workflowRun
	var wfHasRun bool
	var failedWorkflowPane string
	// Batch 2 checkpoint binding: hoisted like wfRun, so the post-commit
	// manifest assembly below knows which agent's handoff to bind.
	var bindAgentID string
	ran, err := IdemTx(ctx, s, sessionID, in.RequestID, "swarm_checkpoint", &out, func(tx *sql.Tx) error {
		ses, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if !ses.State.Live() {
			return &items.Error{Code: items.CodeConflict, Message: "This session is not live."}
		}
		if ses.State.Pausing() && !slices.Contains(pauseAllowedKinds, in.Kind) {
			return errors.New(pausedTool)
		}
		if in.Kind == Handoff && ses.State.Pausing() {
			bindAgentID = a.ID
		}

		assignmentKey, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		itemKey := in.ItemKey
		if itemKey == "" {
			itemKey = assignmentKey
		}
		it, err := s.Items.GetTx(ctx, tx, itemKey)
		if err != nil {
			return err
		}
		if it.ID != a.ItemID {
			ok, err := s.isDescendant(ctx, tx, it.ID, a.ItemID)
			if err != nil {
				return err
			}
			if !ok {
				return &items.Error{Code: items.CodeBadRequest,
					Message: fmt.Sprintf("%s is outside your assignment.", itemKey)}
			}
		}

		if n := utf8.RuneCountInString(in.Summary); n < 1 || n > 500 {
			return &items.Error{Code: items.CodeBadRequest, Message: "Summary must be 1–500 characters."}
		}
		if in.Kind == Integrated {
			if a.Role != RoleOrchestrator {
				return &items.Error{Code: items.CodeBadRequest,
					Message: "Only an orchestrator can write an integrated checkpoint."}
			}
			if len(in.Git) == 0 || len(in.Verification) == 0 {
				return &items.Error{Code: items.CodeBadRequest,
					Message: "An integrated checkpoint needs git and verification."}
			}
			if it.Workflow != nil && it.Workflow.Integration != nil {
				for _, cmd := range it.Workflow.Integration.Verify {
					passed, err := s.hasIntegrationVerifyPassed(ctx, tx, it.ID, in.Verification, cmd)
					if err != nil {
						return err
					}
					if !passed {
						return &items.Error{Code: items.CodeBadRequest,
							Message: fmt.Sprintf("Integration verify not recorded as passing: %s.", cmd)}
					}
				}
				if len(it.Workflow.Integration.FinalReview) > 0 {
					var integratedSHA string
					for _, g := range in.Git {
						if g.SHA != "" {
							integratedSHA = g.SHA
							break
						}
					}
					sha7 := integratedSHA
					if len(sha7) > 7 {
						sha7 = sha7[:7]
					}
					passed, err := s.hasFinalReviewPassed(ctx, tx, it.ID, integratedSHA)
					if err != nil {
						return err
					}
					if !passed {
						return &items.Error{Code: items.CodeBadRequest,
							Message: fmt.Sprintf("Integration needs a passing final review of %s.", sha7)}
					}
				}
			}
		}
		if in.Resolution != "" {
			if in.Kind != CompletedCkp {
				return &items.Error{Code: items.CodeBadRequest,
					Message: "resolution is only valid on a completed checkpoint."}
			}
			if it.Type != items.Spike {
				return &items.Error{Code: items.CodeBadRequest,
					Message: "resolution is only valid on a spike."}
			}
			if in.Resolution != "no_change" && !strings.HasPrefix(in.Resolution, "duplicate_of:") {
				return &items.Error{Code: items.CodeBadRequest,
					Message: "resolution must be no_change or duplicate_of:<KEY>."}
			}
		}

		// Only a reviewer/ui_reviewer ever sets a verdict (spec B5), on any
		// checkpoint kind -- checked up front, independent of workflow-run
		// gating below, so a misuse is refused even for a legacy agent.
		verdict := workflow.Verdict(in.Verdict)
		if verdict != "" && !isReviewerRole(a.Role) {
			return &items.Error{Code: items.CodeBadRequest, Message: "Only reviewers set a verdict."}
		}
		// Finding 6: an invalid enum value is refused here, on any
		// checkpoint kind, rather than reaching the checkpoints.verdict
		// CHECK constraint (a raw, unclear SQL error) or only being caught
		// within the completed+hasRun+reviewer requiredness check below.
		if verdict != "" && !validVerdict(verdict) {
			return &items.Error{Code: items.CodeBadRequest,
				Message: "Reviewers must complete with verdict: pass, changes_requested or blocked."}
		}
		// Finding 7: server-side severity validation, not just the MCP
		// schema's advisory enum.
		for _, f := range in.Findings {
			if !validSeverity(f.Severity) {
				return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
					"finding severity %q must be critical, major, minor or nit.", f.Severity)}
			}
		}

		// completed is the universal session-terminal checkpoint (every role
		// ends its assignment with completed or failed -- terminalCheckpointKind
		// reads it to close the session cleanly), so it's valid on any item
		// type an agent can legitimately be assigned to: an orchestrator ends
		// its own Epic/Bug this way, a reviewer ends a story-wide review this
		// way. It only needs gating for gatedRoles (coder/debugger/mechanical):
		// runtime.Spawn now refuses those on anything but a Task, so this is
		// defense-in-depth for an agent already assigned before that gate
		// existed, not the primary fix.
		if in.Kind == CompletedCkp && slices.Contains(gatedRoles, a.Role) &&
			it.Type != items.Task && it.Type != items.Spike {
			return &items.Error{Code: items.CodeBadRequest,
				Message: fmt.Sprintf(
					"Completed checkpoints attach to tasks, not %s. Pass item: \"<TASK-KEY>\" for the task you finished.",
					itemTypePlural(it.Type))}
		}

		// P9: read unconditionally, not just for CompletedCkp -- relay
		// suppression (spec B4) needs hasRun for Accepted/Progress too, and
		// the run-state update below needs it for FailedCkp.
		run, hasRun, err := s.workflowRunFor(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		wfRun, wfHasRun = run, hasRun
		if in.Kind == CompletedCkp && hasRun {
			// Gates replace verifyOK for an agent with a workflow run (spec
			// B5): the step's own declared gates decide, not a blanket
			// "some verification was recorded". Reviewer/ui_reviewer steps
			// also require a verdict here, independent of any declared
			// gates (templates never put a Gate on a review step).
			if isReviewerRole(a.Role) {
				// Validity was already checked above; this is just
				// requiredness -- a reviewer with a run must set one.
				if verdict == "" {
					return &items.Error{Code: items.CodeBadRequest,
						Message: "Reviewers must complete with verdict: pass, changes_requested or blocked."}
				}
				if verdict == workflow.VerdictPass && hasMajorOrCritical(in.Findings) {
					return &items.Error{Code: items.CodeBadRequest,
						Message: "verdict pass can't carry critical or major findings."}
				}
			}
			if err := s.applyGates(ctx, tx, it, run, a, in); err != nil {
				return err
			}
		} else if in.Kind == CompletedCkp && it.TddExempt == "" {
			gated := slices.Contains(gatedRoles, a.Role)
			if !gated && a.Role == RoleOrchestrator && s.changedFiles(ctx, in.Git) > 0 {
				gated = true
			}
			if gated {
				prior, err := s.priorVerify(ctx, tx, a.ID, ses.Attempt)
				if err != nil {
					return err
				}
				if msg := verifyOK(prior, in.Verification); msg != "" {
					return errors.New(msg)
				}
			}
		}

		if in.Kind == CompletedCkp {
			if kind := requiredArtifactKind(it); kind != "" {
				ok, err := s.hasArtifact(ctx, tx, it.ID, kind)
				if err != nil {
					return err
				}
				if !ok {
					return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
						"completed requires a registered %s for this %s. Register one with "+
							"swarm_artifact register, or set tdd_exempt if this genuinely needs neither.",
						kind, it.Type)}
				}
			}
		}

		ckpID := ids.New("ckp")
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
			attempt, resolution, summary, next_json, blockers_json, git_json, verify_json, artifacts_json,
			processed_json, verdict, findings_json, daemon_written, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
			ckpID, sessionID, a.ID, it.ID, string(in.Kind), ses.Attempt, nullIf(in.Resolution), in.Summary,
			jsonArray(in.Next), jsonArray(in.Blockers), jsonArray(in.Git), jsonArray(in.Verification),
			jsonArray(in.Artifacts), jsonArray(in.Processed), nullIf(in.Verdict), jsonArray(in.Findings),
			db.Millis(now)); err != nil {
			return err
		}
		out.CheckpointID = ckpID
		if hasRun && verdict != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET verdict = ?, findings_json = ?
				WHERE id = ?`, string(verdict), jsonArray(in.Findings), run.ID); err != nil {
				return err
			}
		}
		// P9 (spec B4): the engine owns a run's state/ended_at from here on.
		// Completed/failed are this run's own terminal checkpoint (an
		// AutoRetry crash re-attempt reuses the same row via a fresh
		// advance, not a second insert).
		if hasRun && (in.Kind == CompletedCkp || in.Kind == FailedCkp) {
			if in.Kind == FailedCkp {
				failedWorkflowPane = a.Name
			}
			state := "completed"
			if in.Kind == FailedCkp {
				state = "failed"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET state = ?, ended_at = ? WHERE id = ?`,
				state, db.Millis(now), run.ID); err != nil {
				return err
			}
			if in.Kind == FailedCkp {
				// A voluntary FailedCkp is this session's own terminal
				// signal too (spec B4): normally reconcile's resolveDead
				// discovers a terminal checkpoint asynchronously and marks
				// the session 'failed' from there, which would leave the
				// engine's own AutoRetry (Store.Retry requires a
				// retryableStates session) waiting on that tick. A
				// workflow agent's own report doesn't need to wait for it.
				// The pane is closed after commit, before AutoRetry. When
				// retries are exhausted that close is still necessary because
				// reconcile no longer scans this terminal session.
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state = 'failed', ended_at = ? WHERE id = ?`,
					db.Millis(now), sessionID); err != nil {
					return err
				}
			}
		}
		if err := s.onPausingCheckpoint(ctx, tx, ses, in.Kind); err != nil {
			return err
		}

		for _, id := range in.Processed {
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'acked', acked_at = ?
				WHERE id = ? AND to_agent_id = ? AND state <> 'acked'`, db.Millis(s.Now()), id, a.ID); err != nil {
				return err
			}
		}

		switch in.Kind {
		case Accepted:
			to := items.InProgress
			if it.Status == items.Blocked {
				to = it.StatusBeforeBlock
			}
			if err := s.tryTransition(ctx, tx, itemKey, to); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET state = 'acked', acked_at = ?
				WHERE to_agent_id = ? AND kind = 'assignment' AND state = 'delivered'`,
				db.Millis(s.Now()), a.ID); err != nil {
				return err
			}
		case Progress:
			if it.Status == items.Blocked {
				if err := s.tryTransition(ctx, tx, itemKey, it.StatusBeforeBlock); err != nil {
					return err
				}
			}
		case BlockedCkp:
			if err := s.tryTransition(ctx, tx, itemKey, items.Blocked); err != nil {
				return err
			}
		case CompletedCkp:
			if it.Type == items.Task {
				if err := s.tryTransition(ctx, tx, itemKey, items.InReview); err != nil {
					return err
				}
			}
			tc, err := s.closeCompletedSiblings(ctx, tx, it.ID, a.ID, a.Role, run, hasRun, it.Workflow != nil, now)
			if err != nil {
				return err
			}
			toClose = tc
		}

		if in.Resolution != "" {
			reqID := ids.New("req")
			binding, _ := json.Marshal(map[string]string{"resolution": in.Resolution})
			prompt := fmt.Sprintf("%s found nothing to build (%s).", a.Name, in.Resolution)
			if len(prompt) > 1000 {
				prompt = prompt[:1000]
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, session_id, item_id,
				prompt, state, binding_json, created_at)
				VALUES (?, 'close_spike', ?, ?, ?, ?, 'open', ?, ?)`,
				reqID, a.ID, sessionID, it.ID, prompt, string(binding), db.Millis(now)); err != nil {
				return err
			}
			if _, err := s.Events.Append(ctx, tx, events.RequestOpened,
				map[string]string{"id": reqID, "kind": "close_spike", "item": itemKey, "state": "open"}); err != nil {
				return err
			}
			if s.Notify != nil {
				if err := s.Notify.Raise(ctx, tx, NotifyInput{Kind: "request.close_spike",
					AgentName: a.Name, ItemKey: itemKey, RequestID: reqID,
					Args: map[string]string{"KEY": itemKey, "name": a.Name, "resolution": in.Resolution}}); err != nil {
					return err
				}
			}
		}

		if in.Kind == BlockedCkp && len(in.Blockers) > 0 && a.ParentAgentID == "" {
			reqID := ids.New("req")
			prompt := in.Summary
			if prompt == "" {
				prompt = fmt.Sprintf("%s reported blockers on %s.", a.Name, itemKey)
			}
			if len(prompt) > 1000 {
				prompt = prompt[:1000]
			}
			optionsJSON := jsonArray(in.Blockers)
			if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
				prompt, options_json, state, created_at)
				VALUES (?, 'blocker', 1, ?, ?, ?, ?, ?, 'open', ?)`,
				reqID, a.ID, sessionID, it.ID, prompt, optionsJSON, db.Millis(now)); err != nil {
				return err
			}
			if _, err := s.Events.Append(ctx, tx, events.RequestOpened,
				map[string]string{"id": reqID, "kind": "blocker", "item": itemKey, "state": "open"}); err != nil {
				return err
			}
			if s.Notify != nil {
				if err := s.Notify.Raise(ctx, tx, NotifyInput{Kind: "request.blocker",
					AgentName: a.Name, ItemKey: itemKey, RequestID: reqID,
					Args: map[string]string{"KEY": itemKey, "name": a.Name, "prompt": prompt}}); err != nil {
					return err
				}
			}
		}

		// P9 (spec B4 Relays): for an agent with a workflow run, the engine
		// owns accepted/progress/completed -- WriteCheckpoint must not also
		// relay them to the parent (that would double the orchestrator's
		// own workflow_succeeded/escalated relay with a redundant raw
		// checkpoint one). blocked/failed/handoff and swarm_send questions
		// still reach the orchestrator exactly as today.
		suppressed := hasRun && (in.Kind == Accepted || in.Kind == Progress || in.Kind == CompletedCkp)
		// The enqueue itself lives below, after the final revision is
		// known (F11 carries it in the relay).

		if _, err := s.Events.Append(ctx, tx, events.CheckpointCreated,
			map[string]string{"item": itemKey, "agent": a.Name, "kind": string(in.Kind)}); err != nil {
			return err
		}
		if cn, ok := checkpointNotify[in.Kind]; ok && s.Notify != nil {
			args := map[string]string{}
			if cn.name {
				args["name"] = a.Name
			}
			if cn.key {
				args["KEY"] = itemKey
			}
			if cn.title {
				args["title"] = it.Title
			}
			if err := s.Notify.Raise(ctx, tx, NotifyInput{Kind: cn.kind,
				AgentName: a.Name, ItemKey: itemKey, Args: args}); err != nil {
				return err
			}
		}

		if err := s.Items.ReconcileTx(ctx, tx, itemKey); err != nil {
			return err
		}
		final, err := s.Items.GetTx(ctx, tx, itemKey)
		if err != nil {
			return err
		}
		out.ItemStatus = final.Status
		out.ItemRevision = final.Revision
		// F11: the parent relay carries the final item_revision observed
		// after cascading transitions (ReconcileTx above can bump it again
		// past this checkpoint's own transition), so the observing parent
		// sees current state. Recipients keep using that explicit revision
		// with optimistic concurrency; there is no revision:latest shortcut.
		if a.ParentAgentID != "" && !suppressed {
			body, err := json.Marshal(map[string]any{
				"event": string(in.Kind), "agent": a.Name, "item": itemKey,
				"item_revision": final.Revision,
				"checkpoint": map[string]any{"summary": in.Summary, "resolution": in.Resolution,
					"next": in.Next, "blockers": in.Blockers},
			})
			if err != nil {
				return err
			}
			if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon",
				ToAgentID: a.ParentAgentID, RootItemID: a.RootItemID, ItemID: it.ID, Payload: body}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !ran {
		// !ran is a genuine I11 replay: closeCompletedSiblings' DB effects were
		// never (re)computed, so tearing down tmux panes here too would double
		// -kill sessions a first, successful call already closed.
		return out, err
	}
	postCommitCtx := context.WithoutCancel(ctx)
	for _, t := range toClose {
		if ad := s.Adapters[t.Kind]; ad != nil {
			_ = s.Tmux.Keys(ctx, t.TmuxName, ad.InterruptKeys()...)
		}
		_ = s.Tmux.Kill(ctx, t.TmuxName)
	}
	if failedWorkflowPane != "" {
		// The failed session is terminal in the DB, so reconcile no longer
		// scans it. End its pane even when AutoRetry is exhausted.
		if err := s.Tmux.Kill(postCommitCtx, failedWorkflowPane); err != nil {
			s.logf("checkpoint: kill failed workflow pane %s: %v", failedWorkflowPane, err)
		}
	}
	// P9 (spec B4): a completed or failed checkpoint from a workflow agent
	// triggers advance after commit -- the engine reads the state this
	// checkpoint just wrote (workflow_runs.state, verdict/findings).
	if wfHasRun && (in.Kind == CompletedCkp || in.Kind == FailedCkp) {
		if err := s.advance(postCommitCtx, wfRun.WorkflowID); err != nil {
			s.logf("checkpoint: advance %s: %v", wfRun.WorkflowID, err)
		}
	}
	// Batch 2 checkpoint binding: a handoff checkpoint written while
	// pausing claims preservation is saved. With a pending handoff/recover
	// operation the daemon assembles the manifest and validates it before
	// ready can be claimed; any failure refuses the claim here (the
	// operation is already marked blocked) while the checkpoint itself
	// stands as evidence of the attempt.
	if bindAgentID != "" {
		if err := s.bindHandoffCheckpoint(postCommitCtx, bindAgentID, out.CheckpointID); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Checkpoints lists an item's checkpoints, most recent first.
func (s *Store) Checkpoints(ctx context.Context, itemKey string, limit int, before time.Time) ([]Checkpoint, error) {
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	cutoff := int64(1) << 62
	if !before.IsZero() {
		cutoff = db.Millis(before)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, session_id, agent_id, item_id, kind, attempt,
		COALESCE(resolution, ''), summary, next_json, blockers_json, git_json, verify_json, artifacts_json,
		processed_json, daemon_written, created_at
		FROM checkpoints WHERE item_id = ? AND created_at < ? ORDER BY created_at DESC, rowid DESC LIMIT ?`,
		it.ID, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var kind string
		var nextJSON, blockersJSON, gitJSON, verifyJSON, artifactsJSON, processedJSON string
		var daemonWritten int
		var created int64
		if err := rows.Scan(&c.ID, &c.SessionID, &c.AgentID, &c.ItemID, &kind, &c.Attempt,
			&c.Resolution, &c.Summary, &nextJSON, &blockersJSON, &gitJSON, &verifyJSON, &artifactsJSON,
			&processedJSON, &daemonWritten, &created); err != nil {
			return nil, err
		}
		c.Kind = CheckpointKind(kind)
		json.Unmarshal([]byte(nextJSON), &c.Next)
		json.Unmarshal([]byte(blockersJSON), &c.Blockers)
		json.Unmarshal([]byte(gitJSON), &c.Git)
		json.Unmarshal([]byte(verifyJSON), &c.Verification)
		json.Unmarshal([]byte(artifactsJSON), &c.Artifacts)
		json.Unmarshal([]byte(processedJSON), &c.Processed)
		c.DaemonWritten = daemonWritten != 0
		c.CreatedAt = db.FromMillis(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

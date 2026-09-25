// P9: the daemon-side workflow engine (spec B4). workflow.go owns a
// workflow task's whole lifecycle once an orchestrator starts it: start,
// advance (mapping DB rows -> internal/workflow.Next's pure Action and
// applying it), idempotent spawning, budget/FIFO waiting, relays,
// escalation, resume and cancel. internal/workflow (the leaf DSL package)
// makes every scheduling decision; this file only ever executes the single
// Action it returns.
package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

// WorkflowWorktree is one {worktree, mode} pair -- swarm_workflow start's own
// input (spec B7) and what workflows.worktrees_json stores.
type WorkflowWorktree struct {
	WorktreeID string `json:"worktree_id"`
	Mode       string `json:"mode"` // rw|ro
}

// StartWorkflowInput is Store.StartWorkflow's input (spec B4/B7).
type StartWorkflowInput struct {
	ItemKey              string
	Worktrees            []WorkflowWorktree
	Context              []string
	SessionID, RequestID string
}

// WorkflowRunView is one workflow_runs row the way swarm_workflow/swarm_read
// report it (spec B3/B7).
type WorkflowRunView struct {
	ID, StepID, Role, AgentID, AgentName, State, Verdict, SHA string
	Round, AutoRetries                                        int
	Findings                                                  []workflow.Finding
}

// WorkflowState is swarm_workflow's result shape (spec B7): the workflow's
// own row plus every run recorded so far.
type WorkflowState struct {
	ID, ItemKey, State, Escalation string
	Round, ExtraRounds             int
	Runs                           []WorkflowRunView
}

// wfRow is one workflows table row.
type wfRow struct {
	ID, ItemID, RootItemID, OwnerAgentID, State, Escalation, ContextJSON, WorktreesJSON string
	Round, ExtraRounds                                                                  int
	CreatedAt, UpdatedAt                                                                time.Time
}

// wfRunRow is one workflow_runs table row.
type wfRunRow struct {
	ID, WorkflowID, StepID, Role, AgentID, State, Verdict, SHA, ReviewWorktreeID string
	FindingsJSON                                                                string
	Round, AutoRetries                                                          int
	CreatedAt                                                                   time.Time
	EndedAt                                                                     *time.Time
}

func (r wfRunRow) findings() []workflow.Finding {
	var out []workflow.Finding
	_ = json.Unmarshal([]byte(r.FindingsJSON), &out)
	return out
}

func (r wfRunRow) toRun() workflow.Run {
	return workflow.Run{StepID: r.StepID, Round: r.Round, Role: r.Role, State: workflow.RunState(r.State),
		Verdict: workflow.Verdict(r.Verdict), Findings: r.findings(), SHA: r.SHA, AutoRetries: r.AutoRetries}
}

func (r wfRunRow) toView() WorkflowRunView {
	return WorkflowRunView{ID: r.ID, StepID: r.StepID, Role: r.Role, AgentID: r.AgentID, State: r.State,
		Verdict: r.Verdict, SHA: r.SHA, Round: r.Round, AutoRetries: r.AutoRetries, Findings: r.findings()}
}

// workflowLocks is the per-workflow mutex the engine holds across a whole
// advance call -- bookkeeping AND every side effect (Spawn/Retry/Worktree.*)
// it triggers -- so a second trigger firing while the first is still mid-way
// (e.g. a slot-release notification fired synchronously by the first call's
// own closeCompletedSiblings) can never read a half-applied state and
// double-spawn. Package-level, keyed by workflow id, mirrors
// internal/worktree's wtLocks for the identical reason: a lock scoped to one
// *Store only serializes callers sharing that exact value.
var workflowLocks sync.Map // workflow id -> *sync.Mutex

func lockForWorkflow(id string) *sync.Mutex {
	v, _ := workflowLocks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// dependenciesOpenErr is the existing §B7 "dependencies_open: <keys>" copy,
// reproduced here (internal/mcpserver's own copy is unreachable from this
// package) so StartWorkflow's validation error text matches swarm_items
// create's byte for byte.
func dependenciesOpenErr(keys []string) error {
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("dependencies_open: %s", strings.Join(keys, ", "))}
}

// callerOwnsRWWorktree reports whether wts names at least one 'rw' worktree
// that is currently active and owned by ownerAgentID (spec B4's Start
// validation).
func (s *Store) callerOwnsRWWorktree(ctx context.Context, wts []WorkflowWorktree, ownerAgentID string) (bool, error) {
	for _, w := range wts {
		if w.Mode != "rw" {
			continue
		}
		var owner string
		err := s.DB.QueryRowContext(ctx, `SELECT owner_agent_id FROM worktrees WHERE id = ? AND state = 'active'`,
			w.WorktreeID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if owner == ownerAgentID {
			return true, nil
		}
	}
	return false, nil
}

// StartWorkflow is swarm_workflow op:"start" (spec B4/B7): validates, inserts
// the workflows row (round 1, running) and calls advance.
func (s *Store) StartWorkflow(ctx context.Context, orch Agent, in StartWorkflowInput) (WorkflowState, error) {
	it, err := s.Items.Get(ctx, in.ItemKey)
	if err != nil {
		return WorkflowState{}, err
	}
	if it.Type != items.Task || it.Workflow == nil {
		return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s has no workflow.", it.Key)}
	}
	if it.RootID != orch.RootItemID {
		return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s is outside your assignment.", it.Key)}
	}
	var existing int
	err = s.DB.QueryRowContext(ctx, `SELECT 1 FROM workflows WHERE item_id = ? AND state IN ('running','escalated') LIMIT 1`,
		it.ID).Scan(&existing)
	if err == nil {
		return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s already has a running workflow.", it.Key)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkflowState{}, err
	}
	if len(it.BlockedBy) > 0 {
		return WorkflowState{}, dependenciesOpenErr(it.BlockedBy)
	}
	ok, err := s.callerOwnsRWWorktree(ctx, in.Worktrees, orch.ID)
	if err != nil {
		return WorkflowState{}, err
	}
	if !ok {
		return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: "Start needs a read-write worktree you own."}
	}

	wfID := ids.New("wf")
	now := s.now()
	wtJSON, err := json.Marshal(in.Worktrees)
	if err != nil {
		return WorkflowState{}, err
	}
	ctxJSON, err := json.Marshal(in.Context)
	if err != nil {
		return WorkflowState{}, err
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workflows
			(id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds, context_json, worktrees_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'running', 1, 0, ?, ?, ?, ?)`,
			wfID, it.ID, it.RootID, orch.ID, string(ctxJSON), string(wtJSON), db.Millis(now), db.Millis(now))
		return err
	}); err != nil {
		return WorkflowState{}, err
	}

	if err := s.advance(ctx, wfID); err != nil {
		return WorkflowState{}, err
	}
	st, _, err := s.workflowStateByID(ctx, wfID)
	return st, err
}

// WorkflowFor returns itemKey's latest workflow (any state) and its runs
// (spec B3/B7), or ok=false if it has never had one.
func (s *Store) WorkflowFor(ctx context.Context, itemKey string) (WorkflowState, bool, error) {
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		return WorkflowState{}, false, err
	}
	row, ok, err := s.latestWorkflowRow(ctx, it.ID)
	if err != nil || !ok {
		return WorkflowState{}, ok, err
	}
	st, err := s.workflowStateFromRow(ctx, row, it.Key)
	return st, true, err
}

func (s *Store) latestWorkflowRow(ctx context.Context, itemID string) (wfRow, bool, error) {
	var r wfRow
	var created, updated int64
	err := s.DB.QueryRowContext(ctx, `SELECT id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds,
		COALESCE(escalation, ''), COALESCE(context_json, '[]'), worktrees_json, created_at, updated_at
		FROM workflows WHERE item_id = ? ORDER BY created_at DESC LIMIT 1`, itemID).
		Scan(&r.ID, &r.ItemID, &r.RootItemID, &r.OwnerAgentID, &r.State, &r.Round, &r.ExtraRounds,
			&r.Escalation, &r.ContextJSON, &r.WorktreesJSON, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return wfRow{}, false, nil
	}
	if err != nil {
		return wfRow{}, false, err
	}
	r.CreatedAt, r.UpdatedAt = db.FromMillis(created), db.FromMillis(updated)
	return r, true, nil
}

func (s *Store) workflowRowByID(ctx context.Context, workflowID string) (wfRow, bool, error) {
	var r wfRow
	var created, updated int64
	err := s.DB.QueryRowContext(ctx, `SELECT id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds,
		COALESCE(escalation, ''), COALESCE(context_json, '[]'), worktrees_json, created_at, updated_at
		FROM workflows WHERE id = ?`, workflowID).
		Scan(&r.ID, &r.ItemID, &r.RootItemID, &r.OwnerAgentID, &r.State, &r.Round, &r.ExtraRounds,
			&r.Escalation, &r.ContextJSON, &r.WorktreesJSON, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return wfRow{}, false, nil
	}
	if err != nil {
		return wfRow{}, false, err
	}
	r.CreatedAt, r.UpdatedAt = db.FromMillis(created), db.FromMillis(updated)
	return r, true, nil
}

func (r wfRow) worktrees() []WorkflowWorktree {
	var out []WorkflowWorktree
	_ = json.Unmarshal([]byte(r.WorktreesJSON), &out)
	return out
}

func (r wfRow) contextLines() []string {
	var out []string
	_ = json.Unmarshal([]byte(r.ContextJSON), &out)
	return out
}

func (s *Store) loadWorkflowRuns(ctx context.Context, workflowID string) ([]wfRunRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, step_id, round, role, COALESCE(agent_id, ''), state,
		COALESCE(verdict, ''), COALESCE(findings_json, '[]'), COALESCE(review_worktree_id, ''), COALESCE(sha, ''),
		auto_retries, created_at, ended_at
		FROM workflow_runs WHERE workflow_id = ? ORDER BY round, step_id, role`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wfRunRow
	for rows.Next() {
		var r wfRunRow
		var created int64
		var ended sql.NullInt64
		if err := rows.Scan(&r.ID, &r.StepID, &r.Round, &r.Role, &r.AgentID, &r.State, &r.Verdict, &r.FindingsJSON,
			&r.ReviewWorktreeID, &r.SHA, &r.AutoRetries, &created, &ended); err != nil {
			return nil, err
		}
		r.WorkflowID = workflowID
		r.CreatedAt = db.FromMillis(created)
		if ended.Valid {
			t := db.FromMillis(ended.Int64)
			r.EndedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func toWorkflowRuns(rows []wfRunRow) []workflow.Run {
	out := make([]workflow.Run, len(rows))
	for i, r := range rows {
		out[i] = r.toRun()
	}
	return out
}

func (s *Store) workflowStateFromRow(ctx context.Context, row wfRow, itemKey string) (WorkflowState, error) {
	runs, err := s.loadWorkflowRuns(ctx, row.ID)
	if err != nil {
		return WorkflowState{}, err
	}
	views := make([]WorkflowRunView, len(runs))
	for i, r := range runs {
		v := r.toView()
		if v.AgentID != "" {
			if a, err := s.agentByID(ctx, v.AgentID); err == nil {
				v.AgentName = a.Name
			}
		}
		views[i] = v
	}
	return WorkflowState{ID: row.ID, ItemKey: itemKey, State: row.State, Escalation: row.Escalation,
		Round: row.Round, ExtraRounds: row.ExtraRounds, Runs: views}, nil
}

func (s *Store) workflowStateByID(ctx context.Context, workflowID string) (WorkflowState, bool, error) {
	row, ok, err := s.workflowRowByID(ctx, workflowID)
	if err != nil || !ok {
		return WorkflowState{}, ok, err
	}
	var itemKey string
	if err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, row.ItemID).Scan(&itemKey); err != nil {
		return WorkflowState{}, false, err
	}
	st, err := s.workflowStateFromRow(ctx, row, itemKey)
	return st, true, err
}

// stepFor is defined in checkpoint.go (P8) and reused here unchanged.

// advance is Store.advance (spec B4): under a per-workflow mutex, read
// runs, call workflow.Next and apply the single Action it returns, then try
// to fill any run still waiting on budget (leftover from an earlier
// budget-full advance, or one this call's own Spawn action just inserted).
// It is a no-op once the workflow is no longer 'running' (already resolved,
// or gone).
func (s *Store) advance(ctx context.Context, workflowID string) error {
	lock := lockForWorkflow(workflowID)
	lock.Lock()
	defer lock.Unlock()

	wf, ok, err := s.workflowRowByID(ctx, workflowID)
	if err != nil || !ok || wf.State != "running" {
		return err
	}
	it, err := s.itemByIDForEngine(ctx, wf.ItemID)
	if err != nil {
		return err
	}
	if it.Workflow == nil {
		return fmt.Errorf("advance: %s lost its workflow_json", it.Key)
	}
	runs, err := s.loadWorkflowRuns(ctx, wf.ID)
	if err != nil {
		return err
	}

	action := workflow.Next(*it.Workflow, toWorkflowRuns(runs), wf.Round, wf.ExtraRounds)
	switch action.Kind {
	case workflow.ActionSpawn:
		if err := s.applySpawn(ctx, wf, it, action); err != nil {
			return err
		}
	case workflow.ActionRetryFix:
		if err := s.applyRetryFix(ctx, wf, it, action); err != nil {
			return err
		}
	case workflow.ActionAutoRetry:
		if err := s.applyAutoRetry(ctx, wf, action, runs); err != nil {
			return err
		}
	case workflow.ActionSucceed:
		if err := s.applySucceed(ctx, wf, it, action, runs); err != nil {
			return err
		}
	case workflow.ActionEscalate:
		if err := s.applyEscalate(ctx, wf, it, action, runs); err != nil {
			return err
		}
	case workflow.ActionWait:
		// Nothing to do: some run for the current step is still
		// waiting/active. Fall through to the waiting-run fill pass below
		// anyway -- it may be exactly what Wait is waiting on.
	}
	return s.fillWaitingRuns(ctx, wf.ID)
}

// itemByIDForEngine is items.Store.GetTx's read, addressed by id instead of
// key (the engine only ever has the item id off a workflows row).
func (s *Store) itemByIDForEngine(ctx context.Context, itemID string) (items.Item, error) {
	var key string
	if err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, itemID).Scan(&key); err != nil {
		return items.Item{}, err
	}
	return s.Items.Get(ctx, key)
}

// insertWaitingRun inserts a fresh (workflow_id, step_id, round, role) row as
// 'waiting' (no agent yet), reporting whether it was actually inserted:
// ON CONFLICT DO NOTHING against the same UNIQUE key is what makes a
// duplicate advance's Spawn action a no-op (Review Focus 2).
func (s *Store) insertWaitingRun(ctx context.Context, workflowID, stepID string, round int, role, sha string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO workflow_runs
		(id, workflow_id, step_id, round, role, state, sha, created_at)
		VALUES (?, ?, ?, ?, ?, 'waiting', NULLIF(?, ''), ?)
		ON CONFLICT(workflow_id, step_id, round, role) DO NOTHING`,
		ids.New("wfr"), workflowID, stepID, round, role, sha, db.Millis(s.now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// markInProgress moves it from InReview back to InProgress as the daemon
// (spec B4: the task moves InReview <-> InProgress around every build step;
// Done only ever happens on Succeed). A no-op (silently denied, like every
// tryTransition call) when the item isn't currently InReview -- in
// particular the very first spawn, still Ready: that step is the builder's
// own future "accepted" checkpoint's job, not this one's.
func (s *Store) markInProgress(ctx context.Context, itemKey string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		return s.tryTransition(ctx, tx, itemKey, items.InProgress)
	})
}

// applySpawn applies a Spawn action (spec B4): inserts the new run row(s) as
// 'waiting' (idempotent), and for a review step also creates+shares the one
// review worktree the parallel reviewers share. Actually starting an agent
// is fillWaitingRuns' job (budget-gated), called once by advance after every
// action.
func (s *Store) applySpawn(ctx context.Context, wf wfRow, it items.Item, action workflow.Action) error {
	step, ok := stepFor(it.Workflow, action.StepID)
	if !ok {
		return fmt.Errorf("advance: workflow step %q not found on %s", action.StepID, it.Key)
	}
	if step.Run != "" {
		inserted, err := s.insertWaitingRun(ctx, wf.ID, action.StepID, action.Round, step.Run, "")
		if err != nil || !inserted {
			return err
		}
		return s.markInProgress(ctx, it.Key)
	}

	var newRoles []string
	for _, role := range action.Roles {
		inserted, err := s.insertWaitingRun(ctx, wf.ID, action.StepID, action.Round, role, action.SHA)
		if err != nil {
			return err
		}
		if inserted {
			newRoles = append(newRoles, role)
		}
	}
	if len(newRoles) == 0 {
		return nil // pure replay: every role's row already existed
	}
	wtID, err := s.reviewWorktreeFor(ctx, wf, action.StepID, action.Round, action.SHA)
	if err != nil {
		return err
	}
	for _, role := range newRoles {
		if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET review_worktree_id = ?
			WHERE workflow_id = ? AND step_id = ? AND round = ? AND role = ?`,
			wtID, wf.ID, action.StepID, action.Round, role); err != nil {
			return err
		}
	}
	return nil
}

// repoCandidate is one rw worktree's repo, for picking which repo a review
// worktree reviews when a task spans more than one (first by repo name,
// matching commitGate's own tie-break in checkpoint.go).
type repoCandidate struct{ id, path, name string }

func (s *Store) rwRepoCandidates(ctx context.Context, wts []WorkflowWorktree) ([]repoCandidate, error) {
	var out []repoCandidate
	for _, w := range wts {
		if w.Mode != "rw" {
			continue
		}
		var c repoCandidate
		err := s.DB.QueryRowContext(ctx, `SELECT r.id, r.path, r.name FROM worktrees wt
			JOIN repos r ON r.id = wt.repo_id WHERE wt.id = ?`, w.WorktreeID).Scan(&c.id, &c.path, &c.name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// reviewWorktreeFor returns the one review worktree id shared by every
// reviewer of (stepID, round): reused if a sibling reviewer's row already
// has one, else created fresh at sha, owned by the workflow's orchestrator.
func (s *Store) reviewWorktreeFor(ctx context.Context, wf wfRow, stepID string, round int, sha string) (string, error) {
	var existing string
	err := s.DB.QueryRowContext(ctx, `SELECT review_worktree_id FROM workflow_runs
		WHERE workflow_id = ? AND step_id = ? AND round = ? AND review_worktree_id IS NOT NULL LIMIT 1`,
		wf.ID, stepID, round).Scan(&existing)
	if err == nil && existing != "" {
		return existing, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	cands, err := s.rwRepoCandidates(ctx, wf.worktrees())
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("advance: no rw worktree to review for step %q", stepID)
	}
	repo := cands[0]
	wt, err := s.Worktree.Review(ctx, worktree.CreateInput{RepoID: repo.id, RepoPath: repo.path,
		OwnerAgentID: wf.OwnerAgentID, RootItemID: wf.RootItemID}, sha)
	if err != nil {
		return "", err
	}
	return wt.ID, nil
}

// oldestWaitingRun returns workflowID's oldest 'waiting' run, or ok=false if
// none.
func (s *Store) oldestWaitingRun(ctx context.Context, workflowID string) (wfRunRow, bool, error) {
	runs, err := s.loadWorkflowRuns(ctx, workflowID)
	if err != nil {
		return wfRunRow{}, false, err
	}
	var best *wfRunRow
	for i := range runs {
		if runs[i].State != string(workflow.RunStateWaiting) {
			continue
		}
		if best == nil || runs[i].CreatedAt.Before(best.CreatedAt) {
			best = &runs[i]
		}
	}
	if best == nil {
		return wfRunRow{}, false, nil
	}
	return *best, true, nil
}

// fillWaitingRuns spawns workflowID's waiting runs, oldest first, while
// SubagentSlots has room (spec B4 Budget: FIFO). Each spawn attempt claims
// its row first (state waiting -> active guarded by a WHERE ... AND
// state='waiting', so a concurrent fill can't double-spawn the same row);
// a Spawn error leaves the row waiting for the next trigger to retry, except
// when Spawn itself never got an agent id at all, in which case the row is
// left untouched (still waiting) rather than silently dropped.
func (s *Store) fillWaitingRuns(ctx context.Context, workflowID string) error {
	wf, ok, err := s.workflowRowByID(ctx, workflowID)
	if err != nil || !ok {
		return err
	}
	it, err := s.itemByIDForEngine(ctx, wf.ItemID)
	if err != nil {
		return err
	}
	for {
		run, ok, err := s.oldestWaitingRun(ctx, workflowID)
		if err != nil || !ok {
			return err
		}
		used, max, err := s.SubagentSlots(ctx, wf.OwnerAgentID)
		if err != nil {
			return err
		}
		if used >= max {
			return nil
		}
		spawned, err := s.spawnRunAgent(ctx, wf, it, run)
		if err != nil {
			return err
		}
		if !spawned {
			// The row was claimed by a concurrent call between the read and
			// here -- stop this pass; a fresh advance already owns it.
			return nil
		}
	}
}

// spawnRunAgent spawns run's step agent (build or review) and, on success,
// claims the row (agent_id + state -> active). It reports spawned=false,
// nil error when another caller already claimed the row first.
func (s *Store) spawnRunAgent(ctx context.Context, wf wfRow, it items.Item, run wfRunRow) (bool, error) {
	step, ok := stepFor(it.Workflow, run.StepID)
	if !ok {
		return false, fmt.Errorf("advance: workflow step %q not found on %s", run.StepID, it.Key)
	}
	brief := BriefForStep(it, *it.Workflow, run.StepID, run.Round, wf.contextLines())

	var shareRW []WorkflowWorktree
	if step.Run != "" {
		for _, w := range wf.worktrees() {
			if w.Mode == "rw" {
				shareRW = append(shareRW, w)
			}
		}
		wts, err := s.briefWorktrees(ctx, shareRW)
		if err != nil {
			return false, err
		}
		brief.Worktrees = wts
	} else if run.ReviewWorktreeID != "" {
		wts, err := s.briefWorktrees(ctx, []WorkflowWorktree{{WorktreeID: run.ReviewWorktreeID, Mode: "ro"}})
		if err != nil {
			return false, err
		}
		brief.Worktrees = wts
	}

	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: it.Key, Role: Role(run.Role), ParentAgentID: wf.OwnerAgentID,
		Brief: brief})
	if err != nil {
		return false, err
	}

	for _, w := range shareRW {
		if err := s.Worktree.Share(ctx, w.WorktreeID, a.ID, "rw"); err != nil {
			return false, err
		}
	}
	if step.Run == "" && run.ReviewWorktreeID != "" {
		if err := s.Worktree.Share(ctx, run.ReviewWorktreeID, a.ID, "ro"); err != nil {
			return false, err
		}
	}

	res, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET agent_id = ?, state = 'active'
		WHERE id = ? AND state = 'waiting'`, a.ID, run.ID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// applyRetryFix applies a RetryFix action (spec B4): bumps workflows.round
// (guarded by the pre-bump round, so a duplicate advance is a no-op),
// inserts the retried build step's new-round run row, moves the task back
// InReview -> InProgress, retries the SAME builder agent with the rendered
// findings as an assignment update, and releases+removes the finished
// round's review worktree(s).
func (s *Store) applyRetryFix(ctx context.Context, wf wfRow, it items.Item, action workflow.Action) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE workflows SET round = ?, updated_at = ? WHERE id = ? AND round = ?`,
		action.Round, db.Millis(s.now()), wf.ID, wf.Round)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already bumped by a concurrent/duplicate advance
	}
	step, ok := stepFor(it.Workflow, action.StepID)
	if !ok {
		return fmt.Errorf("advance: workflow step %q not found on %s", action.StepID, it.Key)
	}
	inserted, err := s.insertWaitingRun(ctx, wf.ID, action.StepID, action.Round, step.Run, "")
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	if err := s.markInProgress(ctx, it.Key); err != nil {
		return err
	}

	// wf.Round is still the pre-bump round here (the UPDATE above only
	// changed the DB row; this local copy is untouched) -- the exact round
	// the builder's still-current run sits at, before the new round-2 row
	// insertWaitingRun just added would otherwise outrank it in a "latest"
	// lookup.
	prevAgentID, err := s.runAgentIDAt(ctx, wf.ID, action.StepID, wf.Round)
	if err != nil {
		return err
	}
	if prevAgentID != "" {
		prev, err := s.agentByID(ctx, prevAgentID)
		if err != nil {
			return err
		}
		if _, err := s.Retry(ctx, prev.Name, renderFindings(action.Findings), "", ""); err != nil {
			var ie *items.Error
			if !(errors.As(err, &ie) && ie.Code == items.CodeConflict) {
				return err
			}
			// The builder's session isn't retryable yet (spec B4 names
			// Retry(builder, note), which needs the OLD session already
			// terminal -- normally true by the time a review comes back,
			// since reconcile's async pane-close has retired it, but not
			// guaranteed if that hasn't ticked yet). Leave the new round's
			// row 'waiting' rather than get the whole workflow stuck
			// retrying the same failing Retry() forever: fillWaitingRuns
			// spawns a fresh agent for it instead.
			s.logf("advance: %s not retryable yet (%v); spawning fresh for the fix round instead", prev.Name, err)
		} else if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET agent_id = ?, state = 'active'
			WHERE workflow_id = ? AND step_id = ? AND round = ? AND role = ?`,
			prevAgentID, wf.ID, action.StepID, action.Round, step.Run); err != nil {
			return err
		}
	}

	// wf.Round (the pre-bump round, captured before the UPDATE above) is the
	// finished round whose review(s) triggered this retry.
	for _, fixStep := range findFixStepsFor(it.Workflow, action.StepID) {
		if err := s.removeReviewWorktrees(ctx, wf, fixStep.ID, wf.Round); err != nil {
			s.logf("advance: remove review worktree for %s round %d: %v", fixStep.ID, wf.Round, err)
		}
	}
	return nil
}

// runAgentIDAt returns (workflowID, stepID)'s agent_id at exactly round, or
// "" if that (step, round) has no row -- a RetryFix retries the SAME
// builder agent at its just-finished round, it never spawns a new one.
func (s *Store) runAgentIDAt(ctx context.Context, workflowID, stepID string, round int) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = ? AND round = ?`, workflowID, stepID, round).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// removeReviewWorktrees releases every reviewer's reservation and removes
// the (deduplicated) review worktree(s) recorded on (workflowID, stepID,
// round)'s runs.
func (s *Store) removeReviewWorktrees(ctx context.Context, wf wfRow, stepID string, round int) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT review_worktree_id, COALESCE(agent_id, '') FROM workflow_runs
		WHERE workflow_id = ? AND step_id = ? AND round = ? AND review_worktree_id IS NOT NULL`, wf.ID, stepID, round)
	if err != nil {
		return err
	}
	var found []struct{ wt, agent string }
	for rows.Next() {
		var r struct{ wt, agent string }
		if err := rows.Scan(&r.wt, &r.agent); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	return s.releaseAndRemove(ctx, wf, found)
}

func (s *Store) releaseAndRemove(ctx context.Context, wf wfRow, found []struct{ wt, agent string }) error {
	seen := map[string]bool{}
	for _, r := range found {
		if r.agent != "" {
			if err := s.Worktree.Release(ctx, r.wt, r.agent); err != nil {
				return err
			}
		}
		if seen[r.wt] {
			continue
		}
		seen[r.wt] = true
		if _, err := s.Worktree.Remove(ctx, r.wt, wf.OwnerAgentID); err != nil {
			// A dirty/unmerged review worktree is retained by design
			// (worktree.Remove's own §12.2 policy); log, don't fail the
			// action that already committed.
			s.logf("advance: remove review worktree %s: %v", r.wt, err)
		}
	}
	return nil
}

// renderFindings is spec B4's Retry note: every reviewer's findings,
// grouped by reviewer, as "[severity] file:line summary" (action.Findings
// already arrives grouped and sorted -- internal/workflow's own
// mergeFindings).
func renderFindings(findings []workflow.Finding) string {
	if len(findings) == 0 {
		return "Changes requested; see the workflow's checkpoints for detail."
	}
	var b strings.Builder
	last := ""
	for _, f := range findings {
		if f.Reviewer != last {
			if last != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s:\n", f.Reviewer)
			last = f.Reviewer
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(&b, "[%s] %s %s\n", f.Severity, loc, f.Summary)
	}
	return strings.TrimRight(b.String(), "\n")
}

// applyAutoRetry applies an AutoRetry action (spec B4): retries the crashed
// run's SAME agent with a resume note, bumping auto_retries -- guarded by
// the run still being 'failed', so a duplicate advance is a no-op. If the
// run never got an agent id in the first place (a prior spawn attempt
// errored before Spawn returned one), it goes back to 'waiting' instead so
// the next fillWaitingRuns pass spawns it fresh.
func (s *Store) applyAutoRetry(ctx context.Context, wf wfRow, action workflow.Action, runs []wfRunRow) error {
	if action.Run == nil {
		return nil
	}
	var row *wfRunRow
	for i := range runs {
		r := runs[i]
		if r.StepID == action.Run.StepID && r.Round == action.Run.Round && r.Role == action.Run.Role {
			row = &runs[i]
			break
		}
	}
	if row == nil || row.State != string(workflow.RunStateFailed) {
		return nil
	}
	if row.AgentID == "" {
		res, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'waiting', auto_retries = auto_retries + 1
			WHERE id = ? AND state = 'failed'`, row.ID)
		if err != nil {
			return err
		}
		_, err = res.RowsAffected()
		return err
	}
	a, err := s.agentByID(ctx, row.AgentID)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'active', auto_retries = auto_retries + 1
		WHERE id = ? AND state = 'failed'`, row.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	sessionState := "failed"
	if ses, err := s.LatestSession(ctx, a.ID); err == nil {
		sessionState = string(ses.State)
	}
	note := fmt.Sprintf("Your previous session ended without finishing (%s). Resume from your last checkpoint.", sessionState)
	_, err = s.Retry(ctx, a.Name, note, "", "")
	return err
}

// runViews is one workflow_succeeded relay payload's "runs" field (spec B4).
func runViews(rows []wfRunRow) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = map[string]any{"step": r.StepID, "round": r.Round, "role": r.Role, "agent": r.AgentID, "state": r.State}
		if r.Verdict != "" {
			out[i]["verdict"] = r.Verdict
		}
	}
	return out
}

// lastFindings collects every finding recorded at the highest round among
// runs -- the "most recent findings" a workflow_escalated relay reports.
func lastFindings(runs []wfRunRow) []workflow.Finding {
	maxRound := 0
	for _, r := range runs {
		if r.Round > maxRound {
			maxRound = r.Round
		}
	}
	var out []workflow.Finding
	for _, r := range runs {
		if r.Round == maxRound {
			out = append(out, r.findings()...)
		}
	}
	return out
}

// applySucceed applies a Succeed action (spec B4): flips the workflow
// 'running' -> 'succeeded' (guarded, so a duplicate advance relays nothing
// a first call already sent -- Review Focus 5), moves the task to Done as
// the daemon, relays workflow_succeeded to the owner, and removes every
// review worktree.
func (s *Store) applySucceed(ctx context.Context, wf wfRow, it items.Item, action workflow.Action, runs []wfRunRow) error {
	var relayed bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE workflows SET state = 'succeeded', updated_at = ? WHERE id = ? AND state = 'running'`,
			db.Millis(s.now()), wf.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := s.Items.TransitionTx(ctx, tx, it.Key, items.Done, items.Daemon()); err != nil {
			var ie *items.Error
			if !(errors.As(err, &ie) && ie.Code == items.CodeTransitionDenied) {
				return err
			}
			s.logf("advance: %s stayed put moving to Done: %v", it.Key, err)
		}
		payload, err := json.Marshal(map[string]any{"event": "workflow_succeeded", "item": it.Key,
			"sha": action.SHA, "rounds": wf.Round, "runs": runViews(runs)})
		if err != nil {
			return err
		}
		if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: wf.OwnerAgentID,
			RootItemID: wf.RootItemID, ItemID: it.ID, Payload: payload}); err != nil {
			return err
		}
		relayed = true
		return nil
	})
	if err != nil || !relayed {
		return err
	}
	found := make([]struct{ wt, agent string }, 0, len(runs))
	for _, r := range runs {
		if r.ReviewWorktreeID != "" {
			found = append(found, struct{ wt, agent string }{r.ReviewWorktreeID, r.AgentID})
		}
	}
	return s.releaseAndRemove(ctx, wf, found)
}

// applyEscalate applies an Escalate action (spec B4): flips the workflow
// 'running' -> 'escalated' (guarded the same way applySucceed's is),
// relays workflow_escalated and raises the workflow.escalated notification.
func (s *Store) applyEscalate(ctx context.Context, wf wfRow, it items.Item, action workflow.Action, runs []wfRunRow) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE workflows SET state = 'escalated', escalation = ?, updated_at = ?
			WHERE id = ? AND state = 'running'`, action.Reason, db.Millis(s.now()), wf.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		payload, err := json.Marshal(map[string]any{"event": "workflow_escalated", "item": it.Key,
			"reason": action.Reason, "round": wf.Round, "last_findings": lastFindings(runs)})
		if err != nil {
			return err
		}
		if _, err := s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: wf.OwnerAgentID,
			RootItemID: wf.RootItemID, ItemID: it.ID, Payload: payload}); err != nil {
			return err
		}
		return s.notify(ctx, tx, NotifyInput{Kind: "workflow.escalated", ItemKey: it.Key,
			Args: map[string]string{"KEY": it.Key, "reason": action.Reason}})
	})
}

// briefWorktrees resolves wts (worktree id + mode) into the BriefWorktree
// header lines Spawn's brief renders (spec B6: "fixes today's never-
// populated brief worktree header").
func (s *Store) briefWorktrees(ctx context.Context, wts []WorkflowWorktree) ([]BriefWorktree, error) {
	out := make([]BriefWorktree, 0, len(wts))
	for _, w := range wts {
		var repo, path, branch, base string
		err := s.DB.QueryRowContext(ctx, `SELECT r.name, wt.path, COALESCE(wt.branch, ''), COALESCE(wt.base_sha, '')
			FROM worktrees wt JOIN repos r ON r.id = wt.repo_id WHERE wt.id = ?`, w.WorktreeID).Scan(&repo, &path, &branch, &base)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, BriefWorktree{Repo: repo, Path: path, Branch: branch, BaseSHA7: workflow.SHA7(base), Mode: w.Mode})
	}
	return out, nil
}

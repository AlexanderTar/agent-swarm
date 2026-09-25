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
	ID          string             `json:"id,omitempty"`
	StepID      string             `json:"step"`
	Role        string             `json:"role"`
	AgentID     string             `json:"agent_id,omitempty"`
	AgentName   string             `json:"agent"`
	State       string             `json:"state"`
	Verdict     string             `json:"verdict"`
	SHA         string             `json:"sha"`
	Round       int                `json:"round"`
	AutoRetries int                `json:"auto_retries,omitempty"`
	Findings    []workflow.Finding `json:"findings"`
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
	FindingsJSON                                                                 string
	Round, AutoRetries                                                           int
	CreatedAt                                                                    time.Time
	EndedAt                                                                      *time.Time
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
	f := r.findings()
	if f == nil {
		f = []workflow.Finding{}
	}
	return WorkflowRunView{ID: r.ID, StepID: r.StepID, Role: r.Role, AgentID: r.AgentID, State: r.State,
		Verdict: r.Verdict, SHA: r.SHA, Round: r.Round, AutoRetries: r.AutoRetries, Findings: f}
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

// validateWorktrees is spec B4/B7 Start validation (fix round 2, finding 2):
// every {worktree, mode} pair must name a real worktree and a real mode. A
// bogus id used to sail through Start silently -- briefWorktrees and
// rwRepoCandidates both just skip an unknown id (sql.ErrNoRows) -- and only
// surface much later as a blank brief worktree header, or an "advance: no
// rw worktree to review" error deep inside the engine, instead of a clear
// refusal at Start time.
func (s *Store) validateWorktrees(ctx context.Context, wts []WorkflowWorktree) error {
	for _, w := range wts {
		if w.Mode != "rw" && w.Mode != "ro" {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(`worktree %s: mode must be "rw" or "ro".`, w.WorktreeID)}
		}
		var exists int
		err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM worktrees WHERE id = ?`, w.WorktreeID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("worktree %s not found.", w.WorktreeID)}
		}
		if err != nil {
			return err
		}
	}
	return nil
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

// callerOwnsWorktree reports whether wts names at least one worktree
// that is currently active and owned by ownerAgentID (stories accept mode "ro" or "rw").
func (s *Store) callerOwnsWorktree(ctx context.Context, wts []WorkflowWorktree, ownerAgentID string) (bool, error) {
	for _, w := range wts {
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
	var st WorkflowState
	if hit, err := PeekIdempotent(ctx, s, in.SessionID, in.RequestID, &st); err != nil {
		return WorkflowState{}, err
	} else if hit {
		if st.ID != "" {
			if latest, ok, err := s.workflowStateByID(ctx, st.ID); err == nil && ok {
				return latest, nil
			}
		}
		return st, nil
	}

	it, err := s.Items.Get(ctx, in.ItemKey)
	if err != nil {
		return WorkflowState{}, err
	}
	if (it.Type != items.Task && it.Type != items.Story) || it.Workflow == nil {
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
	if err := s.validateWorktrees(ctx, in.Worktrees); err != nil {
		return WorkflowState{}, err
	}
	if it.Type == items.Story {
		ok, err := s.callerOwnsWorktree(ctx, in.Worktrees, orch.ID)
		if err != nil {
			return WorkflowState{}, err
		}
		if !ok {
			return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: "Start needs a worktree you own."}
		}
	} else {
		ok, err := s.callerOwnsRWWorktree(ctx, in.Worktrees, orch.ID)
		if err != nil {
			return WorkflowState{}, err
		}
		if !ok {
			return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: "Start needs a read-write worktree you own."}
		}
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
	ran, err := IdemTx(ctx, s, in.SessionID, in.RequestID, "swarm_workflow", &st, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workflows
			(id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds, context_json, worktrees_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'running', 1, 0, ?, ?, ?, ?)`,
			wfID, it.ID, it.RootID, orch.ID, string(ctxJSON), string(wtJSON), db.Millis(now), db.Millis(now))
		if err != nil {
			return err
		}
		st = WorkflowState{
			ID:          wfID,
			ItemKey:     it.Key,
			State:       "running",
			Round:       1,
			ExtraRounds: 0,
			Runs:        []WorkflowRunView{},
		}
		return nil
	})
	if err != nil {
		return WorkflowState{}, workflowsOneLiveErr(err, it.Key)
	}
	if !ran {
		if st.ID != "" {
			if latest, ok, err := s.workflowStateByID(ctx, st.ID); err == nil && ok {
				return latest, nil
			}
		}
		return st, nil
	}

	// The workflows row is already committed -- a caller ctx that gets
	// cancelled right as this runs (e.g. the MCP call's own deadline) must
	// not abort mid-spawn and leave the committed start with no advance
	// having ever run against it; context.WithoutCancel lets it finish
	// (fix round 2, finding 2's "run post-commit advances under
	// context.WithoutCancel"). An advance error here doesn't fail the
	// already-committed Start either (minor cleanup item): the workflow
	// exists and is 'running', so recoverWorkflows' stall scan will pick
	// it up within stallThreshold regardless.
	if err := s.advance(context.WithoutCancel(ctx), wfID); err != nil {
		s.logf("start workflow: advance %s: %v", wfID, err)
	}
	st, _, err = s.workflowStateByID(ctx, wfID)
	if err != nil {
		return WorkflowState{}, err
	}
	if in.RequestID != "" {
		if body, berr := json.Marshal(st); berr == nil {
			_, _ = s.DB.ExecContext(ctx, `UPDATE idempotency SET result_json = ? WHERE caller = ? AND request_id = ?`,
				string(body), in.SessionID, in.RequestID)
		}
	}
	return st, nil
}


// workflowsOneLiveErr maps a workflows_one_live unique-index violation to
// StartWorkflow's own friendly "already has a running workflow" refusal --
// the advisory pre-check (StartWorkflow's own `existing` SELECT) only
// prevents the common, non-concurrent case; two concurrent Starts for the
// same item can both pass it and race the INSERT, and only one wins against
// this actual DB-level guarantee (0011_workflows.sql's own UNIQUE partial
// index). Passed through unchanged when err is nil or doesn't match: sqlite
// reports this specific violation by column, not by index name --
// "UNIQUE constraint failed: workflows.item_id" -- confirmed empirically
// (fix round 2 self-review), not assumed from the index's own name.
func workflowsOneLiveErr(err error, itemKey string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: workflows.item_id") {
		return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s already has a running workflow.", itemKey)}
	}
	return err
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

// wfRowColumns is the column list latestWorkflowRow and workflowRowByID both
// select from workflows -- they differed only in their WHERE/ORDER clause,
// never the columns or scan logic (fix round 2 minor cleanup: deduped via
// scanWfRow below).
const wfRowColumns = `id, item_id, root_item_id, owner_agent_id, state, round, extra_rounds,
	COALESCE(escalation, ''), COALESCE(context_json, '[]'), worktrees_json, created_at, updated_at`

func scanWfRow(row *sql.Row) (wfRow, bool, error) {
	var r wfRow
	var created, updated int64
	err := row.Scan(&r.ID, &r.ItemID, &r.RootItemID, &r.OwnerAgentID, &r.State, &r.Round, &r.ExtraRounds,
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

func (s *Store) latestWorkflowRow(ctx context.Context, itemID string) (wfRow, bool, error) {
	return scanWfRow(s.DB.QueryRowContext(ctx,
		`SELECT `+wfRowColumns+` FROM workflows WHERE item_id = ? ORDER BY created_at DESC LIMIT 1`, itemID))
}

func (s *Store) workflowRowByID(ctx context.Context, workflowID string) (wfRow, bool, error) {
	return scanWfRow(s.DB.QueryRowContext(ctx, `SELECT `+wfRowColumns+` FROM workflows WHERE id = ?`, workflowID))
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
	runs, err = s.healStrandedActiveRuns(ctx, runs)
	if err != nil {
		return err
	}

	action := workflow.Next(*it.Workflow, toWorkflowRuns(runs), wf.Round, wf.ExtraRounds)
	if action.Kind != workflow.ActionWait {
		// Every applied action -- including a Spawn that only inserted a
		// 'waiting' row, still budget-blocked -- touches updated_at, so
		// recoverWorkflows' 30s stall scan (below) never mistakes real,
		// recent progress for a stuck workflow.
		if _, err := s.DB.ExecContext(ctx, `UPDATE workflows SET updated_at = ? WHERE id = ?`,
			db.Millis(s.now()), wf.ID); err != nil {
			return err
		}
	}
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

// healStrandedActiveRuns marks an 'active' run 'failed' or 'cancelled' when
// its own agent's latest session is no longer live (Opus review, fix round
// 2, finding 1): a startup failure via watchStartup->failSession, a direct
// swarm_control cancel, or a daemon crash between applyAutoRetry's own
// 'active' claim and its Retry() call all leave the run 'active' with a dead
// session and nothing left to ever advance it -- Next just Waits forever on
// it, and recoverWorkflows' stall scan used to skip any workflow with an
// active run at all, stranded or not. Only a session state that actually
// signals a crash heals it (Failed/Crashed -> failed matches AutoRetry's own
// budget; Cancelled -> cancelled matches Next's immediate escalate for a
// cancelled run); Interrupted (a deliberate pause past its deadline, finding
// 6) and Paused are left untouched, neither is a crash.
func (s *Store) healStrandedActiveRuns(ctx context.Context, runs []wfRunRow) ([]wfRunRow, error) {
	for i := range runs {
		r := &runs[i]
		if r.State != string(workflow.RunStateActive) || r.AgentID == "" {
			continue
		}
		ses, err := s.LatestSession(ctx, r.AgentID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // no session recorded yet -- a genuine race, not a stranding
			}
			return nil, err
		}
		if ses.State.Live() {
			continue
		}
		var newState string
		switch ses.State {
		case Cancelled:
			newState = string(workflow.RunStateCancelled)
		case Failed, Crashed:
			newState = string(workflow.RunStateFailed)
		case Completed:
			// RetryFix claims the next round before closing the previous
			// session. A crash in that gap leaves this older completed
			// session attached to an active run that never started.
			if !ses.StartedAt.Before(r.CreatedAt) {
				continue
			}
			newState = string(workflow.RunStateFailed)
		default:
			continue
		}
		res, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = ?, ended_at = ? WHERE id = ? AND state = 'active'`,
			newState, db.Millis(s.now()), r.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			r.State = newState
		}
	}
	return runs, nil
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
		return s.tx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `INSERT INTO workflow_runs
				(id, workflow_id, step_id, round, role, state, created_at)
				VALUES (?, ?, ?, ?, ?, 'waiting', ?)
				ON CONFLICT(workflow_id, step_id, round, role) DO NOTHING`,
				ids.New("wfr"), wf.ID, action.StepID, action.Round, step.Run, db.Millis(s.now()))
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				return s.tryTransition(ctx, tx, it.Key, items.InProgress)
			}
			return nil
		})
	}

	for _, role := range action.Roles {
		if _, err := s.insertWaitingRun(ctx, wf.ID, action.StepID, action.Round, role, action.SHA); err != nil {
			return err
		}
	}
	if action.SHA == "" {
		// The reviewed step declared no commit gate (e.g. design-reviewed's
		// designer step, gated on artifact:design instead) -- there is no
		// git sha to check out a review worktree at, and none is needed:
		// the reviewer reads the registered design/research artifact
		// (spec B6 Context), not a worktree. Leave review_worktree_id
		// unset; spawnRunAgent already treats that as "nothing to share".
		return nil
	}
	// Attach the shared review worktree to every row at (step, round) that
	// doesn't have one yet -- not just whichever roles THIS call happened
	// to insert (fix round 2, finding 3): a crash between insertWaitingRun
	// committing a role's row and this attach step used to leave every
	// PRE-EXISTING role's row permanently without one on replay, since
	// newRoles was empty (every row already existed) and this whole block
	// was skipped entirely -- the reviewer(s) would then spawn with no
	// worktree at all.
	pending, err := s.reviewRolesMissingWorktree(ctx, wf.ID, action.StepID, action.Round)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil // every role at (step, round) already has one
	}
	wtID, err := s.reviewWorktreeFor(ctx, wf, action.StepID, action.Round, action.SHA)
	if err != nil {
		return err
	}
	for _, role := range pending {
		if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET review_worktree_id = ?
			WHERE workflow_id = ? AND step_id = ? AND round = ? AND role = ?`,
			wtID, wf.ID, action.StepID, action.Round, role); err != nil {
			return err
		}
	}
	return nil
}

// reviewRolesMissingWorktree returns the roles among (workflowID, stepID,
// round)'s rows that don't have a review_worktree_id yet.
func (s *Store) reviewRolesMissingWorktree(ctx context.Context, workflowID, stepID string, round int) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT role FROM workflow_runs
		WHERE workflow_id = ? AND step_id = ? AND round = ? AND review_worktree_id IS NULL`,
		workflowID, stepID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
// SubagentSlots has room (spec B4 Budget: FIFO). spawnRunAgent claims its
// row (state waiting -> active) right after Spawn returns an agent, before
// any further side effect, so a concurrent fill can't double-spawn the same
// row and a later Share failure can't strand a live agent with no row
// pointing at it (fix round 2, finding 2). A Spawn error itself -- no
// agent id to claim with at all -- marks the row 'failed' instead of
// leaving it 'waiting' for the stall scan to retry (and orphan another
// agent) every 30s forever; see spawnRunAgent's own comment.
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
		older, err := s.olderWaitingRunElsewhere(ctx, wf.OwnerAgentID, workflowID, run.CreatedAt)
		if err != nil {
			return err
		}
		if older {
			// Fix round 2, finding 7: an older waiting run in a sibling
			// workflow the same owner runs goes first -- yield this slot to
			// it rather than spawning here out of turn.
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
// artifactContextLines is spec B6's Context addition: paths of design/
// research artifacts registered on itemID itself and on the items it
// depends on (registerArtifactAsDaemon, P8, is what writes these rows) --
// how a build step finds the design a designer step already produced.
func (s *Store) artifactContextLines(ctx context.Context, itemID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT path FROM artifacts
		WHERE kind IN ('design', 'research') AND (item_id = ?
			OR item_id IN (SELECT blocked_by_id FROM item_deps WHERE item_id = ?))
		ORDER BY path`, itemID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out = append(out, path)
	}
	return out, rows.Err()
}

// roundFindingLines renders a fresh (round > 1) build step spawn's most
// recent fix-round findings into brief Context lines (fix round 2, finding
// 4). A normal fix loop always retries the SAME builder agent
// (applyRetryFix's Retry() call, which delivers findings as an
// assignment_update message) -- a fresh spawn at round > 1 is only reached
// via ResumeWorkflow's own resumeBumpsRound bumping the round after a
// crashed/stale escalation, and that agent has no prior brief to update and
// no session to message: its first brief is the only chance these findings
// have to ever reach it. Reuses P8's own R3 lookup
// (findFixStepsFor/fixRoundFindings) rather than a third copy.
func (s *Store) roundFindingLines(ctx context.Context, workflowID string, spec *workflow.Spec, buildStepID string, round int) ([]string, error) {
	var findings []workflow.Finding
	for _, fixStep := range findFixStepsFor(spec, buildStepID) {
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			fnd, _, ferr := s.fixRoundFindings(ctx, tx, workflowID, fixStep.ID, round-1)
			findings = append(findings, fnd...)
			return ferr
		}); err != nil {
			return nil, err
		}
	}
	if len(findings) == 0 {
		return nil, nil
	}
	return []string{"Previous round's review findings:\n" + renderFindings(findings)}, nil
}

func (s *Store) spawnRunAgent(ctx context.Context, wf wfRow, it items.Item, run wfRunRow) (bool, error) {
	step, ok := stepFor(it.Workflow, run.StepID)
	if !ok {
		return false, fmt.Errorf("advance: workflow step %q not found on %s", run.StepID, it.Key)
	}
	if step.Run == "" && run.SHA != "" && run.ReviewWorktreeID == "" {
		wtID, err := s.reviewWorktreeFor(ctx, wf, run.StepID, run.Round, run.SHA)
		if err != nil {
			return false, err
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET review_worktree_id = ?
			WHERE workflow_id = ? AND step_id = ? AND round = ? AND review_worktree_id IS NULL`,
			wtID, wf.ID, run.StepID, run.Round); err != nil {
			return false, err
		}
		run.ReviewWorktreeID = wtID
	}
	artifactLines, err := s.artifactContextLines(ctx, it.ID)
	if err != nil {
		return false, err
	}
	ctxLines := append(append([]string{}, wf.contextLines()...), artifactLines...)
	if step.Run != "" && run.Round > 1 {
		findingLines, err := s.roundFindingLines(ctx, wf.ID, it.Workflow, run.StepID, run.Round)
		if err != nil {
			return false, err
		}
		ctxLines = append(ctxLines, findingLines...)
	}
	brief := BriefForStep(it, *it.Workflow, run.StepID, run.Round, ctxLines)

	var shareRW []WorkflowWorktree
	if step.Run != "" {
		for _, w := range wf.worktrees() {
			if w.Mode == "rw" {
				shareRW = append(shareRW, w)
			}
		}
		wts, err := s.BriefWorktrees(ctx, shareRW)
		if err != nil {
			return false, err
		}
		brief.Worktrees = wts
	} else if run.ReviewWorktreeID != "" {
		wts, err := s.BriefWorktrees(ctx, []WorkflowWorktree{{WorktreeID: run.ReviewWorktreeID, Mode: "ro"}})
		if err != nil {
			return false, err
		}
		brief.Worktrees = wts
	} else if it.Type == items.Story {
		wts, err := s.BriefWorktrees(ctx, wf.worktrees())
		if err != nil {
			return false, err
		}
		brief.Worktrees = wts
	}

	a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: it.Key, Role: Role(run.Role), ParentAgentID: wf.OwnerAgentID,
		Brief: brief})
	if err != nil {
		// Opus review, fix round 2, finding 2: Spawn can return an error
		// AFTER already committing the agent row and its assignment
		// message (agents.go's own startSession/watchStartup failure path
		// -- the agent row exists, but Spawn has no id left to hand back on
		// that path). There is no id here to record, so the row can't be
		// reunited with that orphaned agent; but leaving it 'waiting' would
		// make the stall scan retry Spawn on it every 30s forever, each
		// attempt creating one more orphan. Mark it 'failed' instead (no
		// agent_id): the next advance's Next() sees a failed run with no
		// agent and, via applyAutoRetry's existing AgentID=="" branch,
		// bumps auto_retries and puts it back to 'waiting' -- genuinely
		// counted this time, so repeated failures escalate rather than
		// spawning forever. Reported as spawned=true (not an error): this
		// row is handled, and a sibling waiting row (a parallel reviewer)
		// must still get its own spawn attempt.
		s.logf("advance: spawn %s/%s round %d: %v", run.StepID, run.Role, run.Round, err)
		res, uerr := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'failed', ended_at = ?
			WHERE id = ? AND state = 'waiting'`, db.Millis(s.now()), run.ID)
		if uerr != nil {
			return false, uerr
		}
		n, rerr := res.RowsAffected()
		return n > 0, rerr
	}

	// Claim the row right away -- before Share -- so a Share error, a
	// crash, or a cancelled ctx between here and the claim below can never
	// leave a live, running agent with no row pointing at it (finding 2):
	// the stall scan would otherwise see this row still 'waiting' and
	// spawn a second builder alongside the first, live one.
	res, err := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET agent_id = ?, state = 'active'
		WHERE id = ? AND state = 'waiting'`, a.ID, run.ID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		// The row was claimed by a concurrent call between the read and
		// here -- stop this pass; a fresh advance already owns it.
		return false, nil
	}

	for _, w := range shareRW {
		if err := s.Worktree.Share(ctx, w.WorktreeID, a.ID, "rw"); err != nil {
			// Logged, not failed (finding 2): the row is already claimed
			// and the agent is already running -- a worktree it can't
			// reach is a lesser failure than stranding or duplicating the
			// whole run over it.
			s.logf("advance: share %s with %s: %v", w.WorktreeID, a.Name, err)
		}
	}
	if step.Run == "" && run.ReviewWorktreeID != "" {
		if err := s.Worktree.Share(ctx, run.ReviewWorktreeID, a.ID, "ro"); err != nil {
			s.logf("advance: share %s with %s: %v", run.ReviewWorktreeID, a.Name, err)
		}
	}

	return true, nil


}

// applyRetryFix applies a RetryFix action (spec B4): bumps workflows.round
// (guarded by the pre-bump round, so a duplicate advance is a no-op),
// inserts the retried build step's new-round run row, moves the task back
// InReview -> InProgress, retries the SAME builder agent with the rendered
// findings as an assignment update, and releases+removes the finished
// round's review worktree(s).
func (s *Store) applyRetryFix(ctx context.Context, wf wfRow, it items.Item, action workflow.Action) error {
	step, ok := stepFor(it.Workflow, action.StepID)
	if !ok {
		return fmt.Errorf("advance: workflow step %q not found on %s", action.StepID, it.Key)
	}
	prevAgentID, err := s.runAgentIDAt(ctx, wf.ID, action.StepID, wf.Round)
	if err != nil {
		return err
	}
	var prev Agent
	if prevAgentID != "" {
		prev, err = s.agentByID(ctx, prevAgentID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			prevAgentID = ""
		}
	}
	// The round and its already-bound run are one durable transition. A
	// crash cannot expose a new round with a missing or unowned builder.
	inserted := false
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE workflows SET round = ?, updated_at = ? WHERE id = ? AND round = ?`,
			action.Round, db.Millis(s.now()), wf.ID, wf.Round)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		state := "active"
		if prevAgentID == "" {
			state = "waiting"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_runs
			(id, workflow_id, step_id, round, role, agent_id, state, created_at)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
			ids.New("wfr"), wf.ID, action.StepID, action.Round, step.Run, prevAgentID, state, db.Millis(s.now()))
		if err != nil {
			return err
		}
		inserted = true
		return s.tryTransition(ctx, tx, it.Key, items.InProgress)
	})
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}
	if prevAgentID != "" {
		// USER DIRECTIVE: never spawn a fresh builder while the old
		// one's session is still live. Close it out ourselves first --
		// the same way the daemon already ends a completed session
		// (resolveAlive's killCompletedAfter path: kill the pane, mark
		// the session terminal) -- so Retry can act on it immediately.
		// No fallback to a fresh spawn here: once closed, Retry must
		// succeed (an error now is a real failure, not a timing gap).
		if err := s.closeSessionForRetry(ctx, prev); err != nil {
			s.logf("applyRetryFix: closeSessionForRetry %s: %v", prev.Name, err)
			return err
		}
		if _, err := s.Retry(ctx, prev.Name, renderFindings(action.Findings), "", ""); err != nil {
			// The session is already closed (retryable); this is a
			// genuine Retry failure (a fallback preflight, or the
			// retried attempt's own startSession failing to start),
			// not the "still live" case above. Leaving the round's row
			// silently 'waiting' would violate the directive just the
			// same as the removed fallback did: fillWaitingRuns'
			// generic path doesn't know this row was ever meant for
			// prevAgentID and would spawn an unrelated fresh agent for
			// it on the very next advance. Mark it 'failed' instead --
			// same pattern applyAutoRetry's own Retry-failure fallback
			// uses -- so Next's ordinary crash handling (AutoRetry
			// while budget remains, then Escalate) owns it instead.
			if _, uerr := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'failed', ended_at = ?
					WHERE workflow_id = ? AND step_id = ? AND round = ? AND role = ? AND state = 'active'`,
				db.Millis(s.now()), wf.ID, action.StepID, action.Round, step.Run); uerr != nil {
				return uerr
			}
			s.logf("advance: retry fix %s: %v", prev.Name, err)
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

// closeSessionForRetry ends a's current session synchronously if it's still
// live -- the user directive: a fix round must never spawn a fresh builder
// while the old one's session is still live. Mirrors resolveAlive's own
// killCompletedAfter path (kill the pane, mark the session terminal) rather
// than waiting for reconcile's own tick to get there asynchronously, so
// Retry (which requires a retryableStates session) can act immediately. A
// no-op when the session is already terminal.
func (s *Store) closeSessionForRetry(ctx context.Context, a Agent) error {
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return err
	}
	if !ses.State.Live() {
		return nil
	}
	if err := s.Tmux.Kill(ctx, ses.TmuxName); err != nil {
		return err
	}
	return s.SetSessionState(ctx, ses.ID, Completed)
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
	if _, err := s.Retry(ctx, a.Name, note, "", ""); err != nil {
		// The row is already claimed 'active' above (the idempotency guard
		// against a concurrent duplicate advance); a Retry() failure here
		// (fallback preflight, missing adapter -- the tmux duplicate-session
		// cause is closed by WriteCheckpoint's own pane kill, but Retry()
		// can still fail for other reasons) must not leave it stuck there
		// with no live session and nothing to resolveDead/recoverWorkflows
		// it -- fall back to 'failed' (auto_retries already incremented) so
		// the next advance either retries again or, once the budget is
		// spent, escalates instead of hanging forever.
		if _, uerr := s.DB.ExecContext(ctx, `UPDATE workflow_runs SET state = 'failed' WHERE id = ?`, row.ID); uerr != nil {
			return uerr
		}
		s.logf("advance: auto-retry %s: %v", a.Name, err)
	}
	return nil
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
	return s.removeAllReviewWorktrees(ctx, wf, runs)
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
		// A failed retry can exhaust the budget while its agent row still
		// says active. Such a child blocks descendant checks and worktree
		// reclaim even though it has no running session.
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ?
			WHERE state = 'active' AND id IN
			(SELECT agent_id FROM workflow_runs WHERE workflow_id = ? AND state = 'failed' AND agent_id IS NOT NULL)
			AND NOT EXISTS (SELECT 1 FROM sessions se WHERE se.agent_id = agents.id
				AND se.state IN ('spawning','running','pause_requested','quiescing','stopping'))`,
			db.Millis(s.now()), wf.ID); err != nil {
			return err
		}
		// A retired child must give up every shared worktree reservation in
		// the same transaction as its agent state change. Reclaim excludes
		// another agent's unreleased reservation even after that agent ends.
		if _, err := tx.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ?
			WHERE released_at IS NULL AND agent_id IN (
				SELECT a.id FROM agents a JOIN workflow_runs r ON r.agent_id = a.id
				WHERE r.workflow_id = ? AND r.state = 'failed' AND a.state = 'finished'
				AND NOT EXISTS (SELECT 1 FROM sessions se WHERE se.agent_id = a.id
					AND se.state IN ('spawning','running','pause_requested','quiescing','stopping')))`,
			db.Millis(s.now()), wf.ID); err != nil {
			return err
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

// OnStoryReadyForReview is the items.Store.StoryReadyForReview hook: every task
// of a story is Done and the story has an after_tasks review workflow. The daemon relays
// story_ready_for_review to the orchestrator (spec B8).
func (s *Store) OnStoryReadyForReview(ctx context.Context, tx *sql.Tx, story items.Item) error {
	var runningOrSucceeded int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflows WHERE item_id = ? AND state IN ('running', 'succeeded') LIMIT 1`, story.ID).Scan(&runningOrSucceeded)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var alreadyRelayed int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE kind = 'relay' AND item_id = ?
		AND json_extract(payload_json, '$.event') = 'story_ready_for_review'
		AND created_at >= COALESCE((SELECT MAX(created_at) FROM workflows WHERE item_id = ?), 0) LIMIT 1`,
		story.ID, story.ID).Scan(&alreadyRelayed)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var orchID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM agents WHERE root_item_id = ? AND role = 'orchestrator' AND state = 'active'
		ORDER BY created_at DESC LIMIT 1`, story.RootID).Scan(&orchID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM agents WHERE root_item_id = ? AND role = 'orchestrator'
			ORDER BY created_at DESC LIMIT 1`, story.RootID).Scan(&orchID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"event": "story_ready_for_review",
		"story": story.Key,
		"item":  story.Key,
	})
	if err != nil {
		return err
	}
	_, err = s.enqueue(ctx, tx, Message{
		Kind:       "relay",
		Origin:     "daemon",
		ToAgentID:  orchID,
		RootItemID: story.RootID,
		ItemID:     story.ID,
		Payload:    payload,
	})
	return err
}

// advanceWaitingForOwner triggers advance for every 'running' workflow
// owned by ownerAgentID that has at least one 'waiting' run (spec B4
// Budget/Triggers: any child of the owner finishing frees a subagent slot
// that may let a sibling workflow's waiting run start now), oldest waiting
// run first (fix round 2, finding 7): the owner's whole budget is one shared
// pool across every workflow it owns (SubagentSlots counts every child of
// ownerAgentID, not per-workflow), so a slot freeing here must go to
// whichever sibling workflow has been waiting longest, not whichever one
// this DISTINCT happened to list first.
func (s *Store) advanceWaitingForOwner(ctx context.Context, ownerAgentID string) error {
	ids, err := s.queryIDs(ctx, `SELECT w.id FROM workflows w
		JOIN workflow_runs r ON r.workflow_id = w.id
		WHERE w.owner_agent_id = ? AND w.state = 'running' AND r.state = 'waiting'
		GROUP BY w.id ORDER BY MIN(r.created_at)`, ownerAgentID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.advance(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// olderWaitingRunElsewhere reports whether ownerAgentID has a 'waiting' run,
// created strictly before excludeCreatedAt, in some running workflow other
// than workflowID (fix round 2, finding 7). advanceWaitingForOwner's own
// FIFO ordering (above) only ever governs a slot-release trigger; this is
// the belt-and-suspenders half for every OTHER trigger (a checkpoint, the
// stall scan) that can call fillWaitingRuns on a newer sibling workflow
// directly, out of turn -- best-effort fairness (a TOCTOU race between two
// concurrent triggers on two different, unlocked workflows can still let
// both spawn or both yield), not a hard budget guarantee; SubagentSlots
// itself still caps total usage correctly either way.
func (s *Store) olderWaitingRunElsewhere(ctx context.Context, ownerAgentID, workflowID string, excludeCreatedAt time.Time) (bool, error) {
	var exists int
	err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM workflows w
		JOIN workflow_runs r ON r.workflow_id = w.id
		WHERE w.owner_agent_id = ? AND w.id != ? AND w.state = 'running' AND r.state = 'waiting'
			AND r.created_at < ? LIMIT 1`,
		ownerAgentID, workflowID, db.Millis(excludeCreatedAt)).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// stallThreshold is spec B4's crash-recovery window (see recoverWorkflows).
const stallThreshold = 30 * time.Second

// recoverWorkflows is the reconcile loop's stall-recovery scan (spec B4):
// every 'running' workflow that has at least one run recorded, none of
// which is 'waiting' or 'active' (nothing left for it to be doing, or
// waiting on budget for), and whose own updated_at is older than
// stallThreshold gets a fresh advance -- the daemon-restart gap between a
// checkpoint's commit and the advance() call that should have followed it.
func (s *Store) recoverWorkflows(ctx context.Context) error {
	// Only 'active' excludes -- not 'waiting' too (fix round 1, finding 6):
	// a workflow with an active run is genuinely in flight (its own
	// checkpoint/crash trigger will advance it), but a 'waiting' row with
	// nothing active is exactly the stranded case this scan exists to
	// catch -- e.g. the slot-release trigger that should have picked it up
	// never fired (its owner's last other child finished before this
	// workflow's own run went 'waiting', so no later slot ever freed).
	//
	// The exclusion itself narrowed further in fix round 2 (finding 1): an
	// 'active' run only means "genuinely in flight" while its own agent's
	// session is actually still live. A stranded active run (dead session,
	// nothing left to trigger it) used to hide its whole workflow from this
	// scan forever; now only a LIVE session's active run does. advance()'s
	// own healStrandedActiveRuns call (run under this same scan's advance,
	// right below) is what actually resolves a stranded run once this scan
	// stops excluding its workflow.
	ids, err := s.queryIDs(ctx, `SELECT w.id FROM workflows w
		WHERE w.state = 'running' AND w.updated_at < ?
			AND EXISTS (SELECT 1 FROM workflow_runs r WHERE r.workflow_id = w.id)
			AND NOT EXISTS (
				SELECT 1 FROM workflow_runs r
				JOIN sessions se ON se.agent_id = r.agent_id
				WHERE r.workflow_id = w.id AND r.state = 'active'
					AND se.id = (SELECT id FROM sessions WHERE agent_id = r.agent_id ORDER BY attempt DESC LIMIT 1)
					AND se.state IN ('spawning','running','pause_requested','quiescing','stopping'))`,
		db.Millis(s.now().Add(-stallThreshold)))
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.advance(ctx, id); err != nil {
			s.logf("reconcile: recover workflow %s: %v", id, err)
		}
	}
	return nil
}

// BriefWorktrees resolves wts (worktree id + mode) into the BriefWorktree
// header lines Spawn's brief renders (spec B6: "fixes today's never-
// populated brief worktree header").
func (s *Store) BriefWorktrees(ctx context.Context, wts []WorkflowWorktree) ([]BriefWorktree, error) {
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

// notWaitingOnYou is spec B7's resume-refusal copy.
func notWaitingOnYou(key, state string) error {
	return &items.Error{Code: items.CodeConflict, Message: fmt.Sprintf("%s's workflow isn't waiting on you (state: %s).", key, state)}
}

// resumeBumpsRound answers "does this resume need the round bumped, or is
// one more extraRounds enough?" by asking the planner itself, rather than
// reimplementing its pinnedFrom priority by hand (Opus review, fix round 2,
// finding 5: the hand-rolled version matched changes_requested before
// checking for a failed/cancelled run, so a mixed case -- one parallel
// reviewer requested changes while another crashed and exhausted its
// retries -- granted only extra_rounds and immediately re-escalated on the
// same crash forever; Next's own failure-handling loop runs before verdict
// evaluation, so extra_rounds alone can never un-escalate a crash). If
// granting one more extra round still escalates -- whatever the reason,
// verdict-exhausted is the only shape extra_rounds alone actually fixes --
// the round itself must bump too, so Next sees a clean round with no row
// yet and spawns the fix step fresh rather than retrying evidence that
// isn't trustworthy anymore (a crashed run, or a stale review row).
func resumeBumpsRound(spec workflow.Spec, runs []wfRunRow, round, extraRounds int) bool {
	return workflow.Next(spec, toWorkflowRuns(runs), round, extraRounds+1).Kind == workflow.ActionEscalate
}

// appendWorkflowContext appends line to workflowID's own workflows.
// context_json (fix round 2, finding 4): the same Context lines StartWorkflow
// populates from swarm_workflow start's own input, and spawnRunAgent already
// folds into every brief it renders -- reused here so a resume's note
// reaches whichever agent advance() spawns fresh right after, not just an
// existing one deliverNote can message.
func (s *Store) appendWorkflowContext(ctx context.Context, workflowID, line string) error {
	row, ok, err := s.workflowRowByID(ctx, workflowID)
	if err != nil || !ok {
		return err
	}
	raw, err := json.Marshal(append(row.contextLines(), line))
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE workflows SET context_json = ? WHERE id = ?`, string(raw), workflowID)
	return err
}

// deliverNote sends note to agentID as an assignment_update -- the same
// message shape Retry's own note delivery uses (agents.go), reused here so
// a resume's orchestrator note reaches whichever agent advance() just
// spawned or retried, on top of any findings note that spawn/retry already
// sent (spec B7: "note appended to the findings").
func (s *Store) deliverNote(ctx context.Context, agentID, note string) error {
	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"note": note})
	if err != nil {
		return err
	}
	// enqueue owns seq/wake_class/priority/state/created_at itself (fix
	// round 2, finding 9): this used to hand-roll the exact same INSERT
	// Retry's own note delivery did (agents.go), a second copy of the same
	// message shape.
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := s.enqueue(ctx, tx, Message{Kind: "assignment_update", Origin: "daemon",
			ToAgentID: a.ID, RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload})
		return err
	})
}

// ResumeWorkflow is swarm_workflow op:"resume" (spec B7). orch is the
// calling orchestrator (unused beyond validating the call reaches an
// escalated workflow -- ownership isn't re-checked here the way Start's is,
// since only the workflow's own owner can ever see it escalated to them in
// the first place via their inbox relay).
func (s *Store) ResumeWorkflow(ctx context.Context, orch Agent, itemKey, decision, note, sessionID, requestID string) (WorkflowState, error) {
	var st WorkflowState
	if hit, err := PeekIdempotent(ctx, s, sessionID, requestID, &st); err != nil {
		return WorkflowState{}, err
	} else if hit {
		if st.ID != "" {
			if latest, ok, err := s.workflowStateByID(ctx, st.ID); err == nil && ok {
				return latest, nil
			}
		}
		return st, nil
	}

	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		return WorkflowState{}, err
	}
	// Fix round 2, finding 8: only the workflow's own owner's root ever
	// sees it escalated to them in their inbox relay in the first place, so
	// this was never reachable in practice -- but nothing actually enforced
	// it, the same way StartWorkflow's own root check does. Same refusal
	// copy shape.
	if it.RootID != orch.RootItemID {
		return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s is outside your assignment.", it.Key)}
	}
	wf, ok, err := s.latestWorkflowRow(ctx, it.ID)
	if err != nil {
		return WorkflowState{}, err
	}
	if !ok || wf.State != "escalated" {
		state := "none"
		if ok {
			state = wf.State
		}
		return WorkflowState{}, notWaitingOnYou(it.Key, state)
	}

	var lostRace bool
	var bumped bool
	ran, err := IdemTx(ctx, s, sessionID, requestID, "swarm_workflow", &st, func(tx *sql.Tx) error {
		switch decision {
		case "retry":
			runs, err := s.loadWorkflowRuns(ctx, wf.ID)
			if err != nil {
				return err
			}
			newRound := wf.Round
			bumped = it.Workflow != nil && resumeBumpsRound(*it.Workflow, runs, wf.Round, wf.ExtraRounds)
			if bumped {
				newRound++
			}
			// Fix round 2, finding 8: guarded on state='escalated', the same as
			// StartWorkflow's own insert and applySucceed/applyEscalate's own
			// guarded flips -- a concurrent resume (or a fresh escalation
			// racing this one) must not double-apply.
			res, err := tx.ExecContext(ctx, `UPDATE workflows SET state = 'running', round = ?,
				extra_rounds = extra_rounds + 1, escalation = NULL, updated_at = ? WHERE id = ? AND state = 'escalated'`,
				newRound, db.Millis(s.now()), wf.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				lostRace = true
				st.ID = wf.ID
				st.ItemKey = it.Key
				return nil
			}
			// Persist the note into workflows.context_json BEFORE advance runs
			if note != "" {
				raw, err := json.Marshal(append(wf.contextLines(), note))
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE workflows SET context_json = ? WHERE id = ?`, string(raw), wf.ID); err != nil {
					return err
				}
			}
			st = WorkflowState{
				ID:          wf.ID,
				ItemKey:     it.Key,
				State:       "running",
				Round:       newRound,
				ExtraRounds: wf.ExtraRounds + 1,
			}
			return nil

		case "accept":
			res, err := tx.ExecContext(ctx, `UPDATE workflows SET state = 'succeeded', updated_at = ?
				WHERE id = ? AND state = 'escalated'`, db.Millis(s.now()), wf.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				lostRace = true
				st.ID = wf.ID
				st.ItemKey = it.Key
				return nil
			}
			if _, err := s.Items.TransitionTx(ctx, tx, it.Key, items.Done, items.Daemon()); err != nil {
				return err
			}
			st = WorkflowState{
				ID:          wf.ID,
				ItemKey:     it.Key,
				State:       "succeeded",
				Round:       wf.Round,
				ExtraRounds: wf.ExtraRounds,
			}
			return nil

		case "fail":
			res, err := tx.ExecContext(ctx, `UPDATE workflows SET state = 'failed', updated_at = ?
				WHERE id = ? AND state = 'escalated'`, db.Millis(s.now()), wf.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				lostRace = true
				st.ID = wf.ID
				st.ItemKey = it.Key
				return nil
			}
			if err := s.tryTransition(ctx, tx, it.Key, items.Ready); err != nil {
				return err
			}
			st = WorkflowState{
				ID:          wf.ID,
				ItemKey:     it.Key,
				State:       "failed",
				Round:       wf.Round,
				ExtraRounds: wf.ExtraRounds,
			}
			return nil

		default:
			return &items.Error{Code: items.CodeBadRequest,
				Message: `decision must be "retry", "accept" or "fail".`}
		}
	})
	if err != nil {
		return WorkflowState{}, err
	}
	if lostRace {
		st, _, err := s.workflowStateByID(ctx, wf.ID)
		if err == nil && ran && requestID != "" {
			if raw, err := json.Marshal(st); err == nil {
				_, _ = s.DB.ExecContext(ctx, `UPDATE idempotency SET result_json = ? WHERE session_id = ? AND request_id = ?`,
					string(raw), sessionID, requestID)
			}
		}
		return st, err
	}
	if !ran {
		if st.ID != "" {
			if latest, ok, err := s.workflowStateByID(ctx, st.ID); err == nil && ok {
				return latest, nil
			}
		}
		return st, nil
	}

	switch decision {
	case "retry":
		if err := s.advance(context.WithoutCancel(ctx), wf.ID); err != nil {
			return WorkflowState{}, err
		}
		if note != "" && !bumped {
			var activeAgentID string
			err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(agent_id, '') FROM workflow_runs
				WHERE workflow_id = ? AND state = 'active' ORDER BY round DESC, created_at DESC LIMIT 1`, wf.ID).
				Scan(&activeAgentID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return WorkflowState{}, err
			}
			if activeAgentID != "" {
				if err := s.deliverNote(ctx, activeAgentID, note); err != nil {
					return WorkflowState{}, err
				}
			}
		}
	case "accept", "fail":
		runs, err := s.loadWorkflowRuns(ctx, wf.ID)
		if err != nil {
			return WorkflowState{}, err
		}
		if err := s.removeAllReviewWorktrees(ctx, wf, runs); err != nil {
			return WorkflowState{}, err
		}
	}

	st, _, err = s.workflowStateByID(ctx, wf.ID)
	if err != nil {
		return WorkflowState{}, err
	}
	if requestID != "" {
		if body, berr := json.Marshal(st); berr == nil {
			_, _ = s.DB.ExecContext(ctx, `UPDATE idempotency SET result_json = ? WHERE caller = ? AND request_id = ?`,
				string(body), sessionID, requestID)
		}
	}
	return st, nil
}

// removeAllReviewWorktrees releases+removes every review worktree recorded
// on runs -- the same {wt, agent} collection applySucceed and CancelWorkflow
// each built inline (dedupe, fix round 2 minor cleanup); also now reused by
// ResumeWorkflow's accept/fail decisions, which never released a still-
// outstanding review worktree at all.
func (s *Store) removeAllReviewWorktrees(ctx context.Context, wf wfRow, runs []wfRunRow) error {
	found := make([]struct{ wt, agent string }, 0, len(runs))
	for _, r := range runs {
		if r.ReviewWorktreeID != "" {
			found = append(found, struct{ wt, agent string }{r.ReviewWorktreeID, r.AgentID})
		}
	}
	return s.releaseAndRemove(ctx, wf, found)
}

// CancelWorkflow is swarm_workflow op:"cancel" (spec B7): cancels every
// active run's agent, marks the workflow cancelled, moves the task back to
// Ready, and removes any review worktree.
func (s *Store) CancelWorkflow(ctx context.Context, orch Agent, itemKey, sessionID, requestID string) (WorkflowState, error) {
	var st WorkflowState
	if hit, err := PeekIdempotent(ctx, s, sessionID, requestID, &st); err != nil {
		return WorkflowState{}, err
	} else if hit {
		if st.ID != "" {
			if latest, ok, err := s.workflowStateByID(ctx, st.ID); err == nil && ok {
				return latest, nil
			}
		}
		return st, nil
	}

	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		return WorkflowState{}, err
	}
	// Fix round 2, finding 8: same root check as ResumeWorkflow/
	// StartWorkflow, same refusal copy shape.
	if it.RootID != orch.RootItemID {
		return WorkflowState{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("%s is outside your assignment.", it.Key)}
	}
	wf, ok, err := s.latestWorkflowRow(ctx, it.ID)
	if err != nil {
		return WorkflowState{}, err
	}
	if !ok || (wf.State != "running" && wf.State != "escalated") {
		state := "none"
		if ok {
			state = wf.State
		}
		return WorkflowState{}, notWaitingOnYou(it.Key, state)
	}

	// Fix round 2, finding 8: held across the whole cancel (state flip,
	// agent cancels, run cancels) -- the same lock advance()/fillWaitingRuns
	// hold, so a slot freed mid-cancel (cancelling one active run's agent
	// can itself free the owner's budget) can never let a LATER trigger
	// (advanceWaitingForOwner, the stall scan) spawn a fresh run for this
	// workflow while it's still being torn down; it just blocks until this
	// whole call finishes, by which point state is already 'cancelled' and
	// advance() no-ops immediately. s.Cancel (agents.go) never itself calls
	// s.advance, so holding this lock across it is not reentrant.
	lock := lockForWorkflow(wf.ID)
	lock.Lock()
	defer lock.Unlock()

	runs, err := s.loadWorkflowRuns(ctx, wf.ID)
	if err != nil {
		return WorkflowState{}, err
	}

	var lostRace bool
	ran, err := IdemTx(ctx, s, sessionID, requestID, "swarm_workflow", &st, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE workflows SET state = 'cancelled', updated_at = ?
			WHERE id = ? AND state IN ('running', 'escalated')`, db.Millis(s.now()), wf.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			lostRace = true
			st.ID = wf.ID
			st.ItemKey = it.Key
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET state = 'cancelled', ended_at = ?
			WHERE workflow_id = ? AND state IN ('active', 'waiting')`, db.Millis(s.now()), wf.ID); err != nil {
			return err
		}
		if err := s.tryTransition(ctx, tx, it.Key, items.Ready); err != nil {
			return err
		}
		st = WorkflowState{
			ID:          wf.ID,
			ItemKey:     it.Key,
			State:       "cancelled",
			Round:       wf.Round,
			ExtraRounds: wf.ExtraRounds,
		}
		return nil
	})
	if err != nil {
		return WorkflowState{}, err
	}
	if lostRace {
		st, _, err := s.workflowStateByID(ctx, wf.ID)
		if err == nil && ran && requestID != "" {
			if raw, err := json.Marshal(st); err == nil {
				_, _ = s.DB.ExecContext(ctx, `UPDATE idempotency SET result_json = ? WHERE session_id = ? AND request_id = ?`,
					string(raw), sessionID, requestID)
			}
		}
		return st, err
	}
	if !ran {
		if st.ID != "" {
			if latest, ok, err := s.workflowStateByID(ctx, st.ID); err == nil && ok {
				return latest, nil
			}
		}
		return st, nil
	}

	for _, r := range runs {
		if r.State != "active" || r.AgentID == "" {
			continue
		}
		a, err := s.agentByID(ctx, r.AgentID)
		if err != nil {
			continue
		}
		// "" not requestID: that's swarm_workflow cancel's own idempotency
		// key for this WHOLE call (P10's MCP wrapper). Reusing it per agent
		// would make Cancel's own PeekIdempotent replay the first agent's
		// result for every agent after it -- the second parallel reviewer
		// would never actually be cancelled.
		if _, err := s.Cancel(ctx, a.Name, "", ""); err != nil {
			s.logf("cancel workflow: cancel agent %s: %v", a.Name, err)
		}
	}

	if err := s.removeAllReviewWorktrees(ctx, wf, runs); err != nil {
		return WorkflowState{}, err
	}

	st, _, err = s.workflowStateByID(ctx, wf.ID)
	if err != nil {
		return WorkflowState{}, err
	}
	if requestID != "" {
		if body, berr := json.Marshal(st); berr == nil {
			_, _ = s.DB.ExecContext(ctx, `UPDATE idempotency SET result_json = ? WHERE caller = ? AND request_id = ?`,
				string(body), sessionID, requestID)
		}
	}
	return st, nil
}


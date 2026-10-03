package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

var allStatuses = []Status{Draft, Ready, InProgress, Blocked, InReview, AwaitingApproval, Done, Cancelled}

func deny(format string, args ...any) *Error { return errf(CodeTransitionDenied, format, args...) }

func exists(ctx context.Context, q querier, query string, args ...any) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, "SELECT EXISTS ("+query+")", args...).Scan(&ok)
	return ok, err
}

// isAcceptRoot reports whether t is a root type that finishes through
// accept_epic/accept_fix: epic, bug, chore (2026-09-27 chore spec).
func isAcceptRoot(t Type) bool { return t == Epic || t == Bug || t == Chore }

// acceptKind is the acceptance request kind for a root type.
func acceptKind(t Type) string {
	if t == Epic {
		return "accept_epic"
	}
	return "accept_fix" // bug and chore (chore spec decision 4)
}

// Transition applies one status change under the §10.1 rules.
func (s *Store) Transition(ctx context.Context, key string, to Status, by Actor) (Item, error) {
	var out Item
	err := s.write(ctx, func(tx *sql.Tx) (err error) {
		out, err = s.TransitionTx(ctx, tx, key, to, by)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, out.Key)
}

func (s *Store) TransitionTx(ctx context.Context, tx *sql.Tx, key string, to Status, by Actor) (Item, error) {
	it, err := s.getTx(ctx, tx, key)
	if err != nil {
		return Item{}, err
	}
	if err := s.orchestratorScope(ctx, tx, by, it); err != nil {
		return Item{}, err
	}
	if !slices.Contains(allStatuses, to) {
		return Item{}, errf(CodeBadRequest, "Unknown status %q.", to)
	}
	if it.Status == to {
		return it, nil
	}
	if err := s.check(ctx, tx, it, to, by); err != nil {
		return Item{}, err
	}
	from := it.Status
	if err := s.setStatus(ctx, tx, &it, to); err != nil {
		return Item{}, err
	}
	if to == Cancelled {
		if err := s.cancelDescendants(ctx, tx, it.ID); err != nil {
			return Item{}, err
		}
	}
	// reopen: old acceptances and close approvals no longer count; cancel: nothing is left to accept
	if (to == Ready && (from == Done || from == Cancelled)) || to == Cancelled {
		if err := s.staleAccepts(ctx, tx, it); err != nil {
			return Item{}, err
		}
	}
	// Chore spec decision 6: nothing ever moves back into Draft, so an
	// accepted checkpoint on a Draft root belongs to this lifecycle. It
	// counts on promotion, instead of parking the root at Ready forever
	// (acceptedSince only sees checkpoints newer than updated_at).
	if from == Draft && to == Ready && isAcceptRoot(it.Type) && it.ID == it.RootID {
		accepted, err := exists(ctx, tx, `SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'accepted'`, it.ID)
		if err != nil {
			return Item{}, err
		}
		if accepted {
			if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
				return Item{}, err
			}
		}
	}
	if err := s.ReconcileTx(ctx, tx, it.Key); err != nil {
		return Item{}, err
	}
	return s.getByID(ctx, tx, it.ID)
}

// staleAccepts stales every live acceptance or approval request on it (P1 carry:
// the spike approval kinds joined the list when their writers landed in P2).
func (s *Store) staleAccepts(ctx context.Context, tx *sql.Tx, it Item) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, kind FROM requests WHERE item_id = ?
		AND state IN ('open', 'approved') AND kind IN ('accept_epic', 'accept_fix', 'close_spike',
		'approve_section', 'approve_plan', 'approve_report')`, it.ID)
	if err != nil {
		return err
	}
	var live [][2]string
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			rows.Close()
			return err
		}
		live = append(live, [2]string{id, kind})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range live {
		if err := s.resolveStale(ctx, tx, r[0], r[1], it.Key); err != nil {
			return err
		}
	}
	return nil
}

// staleApprovedFinish stales the approved finish request of a root that isn't Done
// after a material edit (brief, acceptance, waivers…): the user approved the item as
// it was. Open requests need nothing here; reconcileRoot stales them on the revision.
func (s *Store) staleApprovedFinish(ctx context.Context, tx *sql.Tx, it Item) error {
	if it.ID != it.RootID || !isAcceptRoot(it.Type) || it.Status == Done {
		return nil
	}
	kind := acceptKind(it.Type)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM requests WHERE item_id = ? AND kind = ? AND state = 'approved'`, it.ID, kind)
	if err != nil {
		return err
	}
	var approved []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		approved = append(approved, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range approved {
		if err := s.resolveStale(ctx, tx, id, kind, it.Key); err != nil {
			return err
		}
	}
	return nil
}

// cancelDescendants cancels every descendant of parentID that is not Done or
// Cancelled yet (root-finish spec, locked decision 4). It runs wherever the
// parent's cancel was allowed, so there is no per-child check(). Archived
// children are included: archiving hides an item, it does not finish it.
func (s *Store) cancelDescendants(ctx context.Context, tx *sql.Tx, parentID string) error {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE d(id) AS (
			SELECT id FROM items WHERE parent_id = ?
			UNION ALL SELECT i.id FROM items i JOIN d ON i.parent_id = d.id)
		SELECT id FROM items WHERE id IN (SELECT id FROM d) AND status NOT IN ('done', 'cancelled')`, parentID)
	if err != nil {
		return err
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range pending {
		c, err := s.getByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.setStatus(ctx, tx, &c, Cancelled); err != nil {
			return err
		}
		if err := s.staleAccepts(ctx, tx, c); err != nil {
			return err
		}
	}
	return nil
}

// resolveStale stales one request and announces it; the board only invalidates
// its inbox on request.* (contracts §5). Payload per R5: {id, kind, item, state}.
func (s *Store) resolveStale(ctx context.Context, tx *sql.Tx, id, kind, itemKey string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'stale' WHERE id = ?`, id); err != nil {
		return err
	}
	payload, err := s.requestPayload(ctx, tx, id, kind, itemKey, "stale")
	if err != nil {
		return err
	}
	_, err = s.Events.Append(ctx, tx, events.RequestResolved, payload)
	return err
}

// requestPayload prefers the injected full-Request builder (R5) and falls back to
// the interim shape when it is unset.
func (s *Store) requestPayload(ctx context.Context, tx *sql.Tx, id, kind, itemKey, state string) (any, error) {
	if s.RequestPayload != nil {
		return s.RequestPayload(ctx, tx, id)
	}
	m := map[string]string{"id": id, "kind": kind, "item": itemKey}
	if state != "" {
		m["state"] = state
	}
	return m, nil
}

func (s *Store) check(ctx context.Context, tx *sql.Tx, it Item, to Status, by Actor) error {
	generic := deny("Couldn't update status. The item remains %s.", StatusLabel(it.Status))
	user, daemon, orch := by.Kind == ActorUser, by.Kind == ActorDaemon, by.isOrchestrator()
	if to == AwaitingApproval && it.Type != Spike {
		return deny("Only spikes can await approval.")
	}
	switch {
	case to == Cancelled:
		if it.Status != Done && (user || (orch && it.ParentID != "")) {
			return nil
		}
		return generic
	case to == Blocked:
		if it.Status != Done && it.Status != Cancelled && (user || orch || daemon) {
			return nil
		}
		return generic
	case it.Status == Blocked:
		if to == it.StatusBeforeBlock && (user || orch || daemon) {
			return nil
		}
		return generic
	case it.Status == Done || it.Status == Cancelled:
		if to == Ready && user {
			return nil
		}
		return generic
	case it.Status == Draft && to == Ready:
		if user || orch {
			return nil
		}
		return generic
	}
	switch it.Type {
	case Story:
		if to == Done {
			if daemon && it.Workflow != nil && it.Workflow.AfterTasks != nil {
				succeeded, err := s.workflowSucceeded(ctx, tx, it.ID)
				if err != nil {
					return err
				}
				if succeeded {
					return nil
				}
			}
			return deny("Couldn't move %s to Done. Complete all child tasks and their checkpoints first.", it.Key)
		}
		return generic // derived; only reconciliation moves stories
	case Spike:
		return s.checkSpike(ctx, tx, it, to, daemon, generic)
	case Epic, Bug, Chore:
		return s.checkRoot(ctx, tx, it, to, daemon, orch, generic)
	}
	return s.checkTask(ctx, tx, it, to, daemon, orch, generic)
}

// workflowDoneCopy is spec B5's Done-on-workflow-task copy, verbatim.
const workflowDoneCopy = "%s is finished by its workflow. It moves to Done when the workflow succeeds; use swarm_workflow resume to accept or fail it."

// workflowSucceeded reports whether itemID's latest workflows row (if any)
// is 'succeeded' -- spec B4/B5: the engine (P9) owns Done for a workflow
// task, checkTask never reaches it through completedCurrent's legacy path.
func (s *Store) workflowSucceeded(ctx context.Context, tx *sql.Tx, itemID string) (bool, error) {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM workflows WHERE item_id = ? ORDER BY created_at DESC LIMIT 1`,
		itemID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state == "succeeded", nil
}

// workflowFailedOrCancelled reports whether itemID's latest workflows row is
// 'failed' or 'cancelled' -- spec B7: a resume {decision:"fail"} or a
// cancel both move the task back to Ready as the daemon, which needs its
// own transition.go case (InProgress/InReview -> Ready has no other path).
func (s *Store) workflowFailedOrCancelled(ctx context.Context, tx *sql.Tx, itemID string) (bool, error) {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM workflows WHERE item_id = ? ORDER BY created_at DESC LIMIT 1`,
		itemID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state == "failed" || state == "cancelled", nil
}

func (s *Store) checkTask(ctx context.Context, tx *sql.Tx, it Item, to Status, daemon, orch bool, generic *Error) error {
	if it.Workflow != nil {
		switch to {
		case Done:
			// spec B5 "Done.": a workflow task's Done is entirely engine-
			// driven (P9's applySucceed calls TransitionTx as the daemon
			// actor once the workflow itself is already 'succeeded' --
			// including after an orchestrator's swarm_workflow resume
			// {decision:"accept"}, which marks the workflow succeeded
			// before this ever runs). Any other caller, or a daemon call
			// before the workflow actually succeeded, is refused with the
			// same copy swarm_items update surfaces.
			succeeded, err := s.workflowSucceeded(ctx, tx, it.ID)
			if err != nil {
				return err
			}
			if daemon && succeeded {
				return nil
			}
			return deny(workflowDoneCopy, it.Key)
		case Ready:
			if daemon && (it.Status == InProgress || it.Status == InReview) {
				terminal, err := s.workflowFailedOrCancelled(ctx, tx, it.ID)
				if err != nil {
					return err
				}
				if terminal {
					return nil
				}
			}
		}
	}
	switch {
	case to == Done:
		ok, err := s.completedCurrent(ctx, tx, it)
		if err != nil {
			return err
		}
		if !ok {
			return deny("Couldn't move %s to Done. No agent has reported it complete on this task.", it.Key)
		}
		if it.Status == InReview && (orch || daemon) {
			return nil
		}
	case it.Status == Ready && to == InProgress && daemon:
		return orGeneric(s.acceptedSince(ctx, tx, it))(generic)
	// it.Status == Ready is a self-heal (2026-09-22): an agent that skips its
	// mandatory first "accepted" checkpoint leaves acceptedSince permanently
	// false, so Ready never reaches InProgress on its own and every later
	// checkpoint.go tryTransition attempt is denied even though real,
	// completedCurrent-verified work landed. completedCurrent is strictly
	// stronger evidence than the accepted step would have been, so the
	// daemon's own checkpoint-driven attempt (never a direct orch/user call --
	// unchanged, still generic-denied) may take Ready straight to InReview.
	case (it.Status == InProgress || it.Status == Ready) && to == InReview && daemon:
		return orGeneric(s.completedCurrent(ctx, tx, it))(generic)
	case it.Status == InReview && to == InProgress && (orch || daemon):
		return nil
	}
	return generic
}

func (s *Store) checkRoot(ctx context.Context, tx *sql.Tx, it Item, to Status, daemon, orch bool, generic *Error) error {
	switch {
	case to == Done:
		if daemon && it.Status == InReview {
			ok, err := s.finishedCurrent(ctx, tx, it)
			if err != nil || ok {
				return err
			}
		}
		switch it.Type {
		case Epic:
			return deny("Finish this epic to mark it Done.")
		case Chore:
			return deny("Finish this chore to mark it Done.")
		}
		return deny("Finish this fix to mark it Done.")
	case it.Status == Ready && to == InProgress && daemon:
		return orGeneric(s.acceptedSince(ctx, tx, it))(generic)
	case it.Status == InProgress && to == InReview && daemon:
		st, err := s.rootState(ctx, tx, it)
		if err != nil {
			return err
		}
		if st.finished && st.ckpID != "" && st.ckpAt >= st.lastChild {
			return nil
		}
	case it.Status == InReview && to == InProgress && (orch || daemon):
		return nil
	}
	return generic
}

func (s *Store) checkSpike(ctx context.Context, tx *sql.Tx, it Item, to Status, daemon bool, generic *Error) error {
	switch {
	case to == Done:
		if daemon {
			done, err := exists(ctx, tx, `SELECT 1 FROM items WHERE origin_spike_id = ?
				UNION ALL SELECT 1 FROM requests WHERE item_id = ? AND kind = 'close_spike' AND state = 'approved'`, it.ID, it.ID)
			if err != nil || done {
				return err
			}
		}
		return deny("This spike reaches Done after materialization.")
	case (it.Status == Draft || it.Status == Ready) && to == InProgress && daemon:
		return orGeneric(s.acceptedSince(ctx, tx, it))(generic)
	case it.Status == InProgress && to == AwaitingApproval && daemon:
		return orGeneric(s.openApproval(ctx, tx, it))(generic)
	case it.Status == AwaitingApproval && to == InProgress && daemon:
		open, err := s.openApproval(ctx, tx, it)
		if err != nil || !open {
			return err
		}
	}
	return generic
}

// orGeneric turns (ok, err) into nil when ok, err when err, else the generic denial.
func orGeneric(ok bool, err error) func(*Error) error {
	return func(generic *Error) error {
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		return generic
	}
}

func (s *Store) acceptedSince(ctx context.Context, q querier, it Item) (bool, error) {
	return exists(ctx, q, `SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'accepted' AND created_at >= ?`,
		it.ID, db.Millis(it.UpdatedAt))
}

// completedCurrent reports whether the item has a "current" completed
// checkpoint. Legacy tasks (workflow_json IS NULL, it.Workflow == nil) use
// the same per-builder comparison as workflow tasks (F1 remainder: the old
// item-wide MAX(attempt) let a cancelled predecessor mask the successor).
// A workflow task instead fixes the cross-agent attempt bug (spec B5/Context):
// the original MAX(attempt) was taken across every checkpoint on the item,
// mixing each agent's own independent attempt/session counter -- a
// reviewer's own review-attempt sequence could mask or falsely validate a
// builder's completed checkpoint. The fix looks only at "build role"
// (fix round 1, R2: any role except reviewer/ui_reviewer/orchestrator)
// checkpoints, picks the temporally latest completed one among them, and
// checks it against THAT SAME agent's own
// latest attempt -- not the item-wide max -- so a later attempt from a
// DIFFERENT agent (e.g. a sibling reviewer) can never invalidate it, while a
// later attempt from the SAME agent (a fix-round retry) still does.
func (s *Store) completedCurrent(ctx context.Context, q querier, it Item) (bool, error) {
	if it.Workflow == nil {
		// F1/TASK-242 remainder: the legacy formula compared the completion
		// against the item-wide MAX(attempt), so a cancelled predecessor's
		// attempt-2 "accepted" masked the successor's completed at attempt
		// 1. Like the workflow branch, compare the latest relevant builder
		// completion against THAT builder's own latest attempt: a later
		// attempt from a different agent never invalidates it, while the
		// same agent's own retry (or a post-reopen retry) still does.
		// Reviewer-only completions are not builder completions. Tied
		// timestamps all satisfy the MAX (no rowid tie-break), so one tied
		// winner can never hide another builder's current completion.
		return exists(ctx, q, `SELECT 1 FROM checkpoints c JOIN agents ag ON ag.id = c.agent_id
			WHERE c.item_id = ? AND c.kind = 'completed' AND ag.role NOT IN ('reviewer','ui_reviewer','orchestrator')
			AND c.created_at = (SELECT MAX(c2.created_at) FROM checkpoints c2 JOIN agents ag2 ON ag2.id = c2.agent_id
				WHERE c2.item_id = ? AND c2.kind = 'completed' AND ag2.role NOT IN ('reviewer','ui_reviewer','orchestrator'))
			AND c.attempt = (SELECT MAX(attempt) FROM checkpoints WHERE item_id = ? AND agent_id = c.agent_id)`,
			it.ID, it.ID, it.ID)
	}
	// "Build roles" (fix round 1, R2): any role except reviewer/ui_reviewer/
	// orchestrator, not just coder/debugger/mechanical -- a design-reviewed
	// or research template's designer/researcher step must be able to
	// finish its own task too.
	return exists(ctx, q, `SELECT 1 FROM checkpoints c JOIN agents ag ON ag.id = c.agent_id
		WHERE c.item_id = ? AND c.kind = 'completed' AND ag.role NOT IN ('reviewer','ui_reviewer','orchestrator')
		AND c.created_at = (SELECT MAX(c2.created_at) FROM checkpoints c2 JOIN agents ag2 ON ag2.id = c2.agent_id
			WHERE c2.item_id = ? AND c2.kind = 'completed' AND ag2.role NOT IN ('reviewer','ui_reviewer','orchestrator'))
		AND c.attempt = (SELECT MAX(attempt) FROM checkpoints WHERE item_id = ? AND agent_id = c.agent_id)`,
		it.ID, it.ID, it.ID)
}

func (s *Store) openApproval(ctx context.Context, q querier, it Item) (bool, error) {
	return exists(ctx, q, `SELECT 1 FROM requests WHERE item_id = ? AND state = 'open'
		AND kind IN ('approve_section', 'approve_plan', 'approve_report', 'close_spike')`, it.ID)
}

type acceptBinding struct {
	ItemRevision         int             `json:"item_revision"`
	IntegratedCheckpoint string          `json:"integrated_checkpoint"`
	Git                  json.RawMessage `json:"git"`
	FinishOptions        json.RawMessage `json:"finish_options,omitempty"` // [{label, description}]
}

type rootState struct {
	finished  bool
	lastChild int64
	ckpID     string
	ckpGit    string
	ckpOpts   string // finish_options_json, "" when none
	ckpAt     int64
}

func (s *Store) rootState(ctx context.Context, q querier, it Item) (rootState, error) {
	var st rootState
	var n, fin int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(status IN ('done', 'cancelled')), 0), COALESCE(MAX(updated_at), 0)
		FROM items WHERE parent_id = ? AND archived_at IS NULL`, it.ID).Scan(&n, &fin, &st.lastChild)
	if err != nil {
		return st, err
	}
	st.finished = fin == n && (n > 0 || it.Type == Chore) // a chore may have no tasks
	err = q.QueryRowContext(ctx, `SELECT id, git_json, COALESCE(finish_options_json, ''), created_at FROM checkpoints
		WHERE item_id = ? AND kind = 'integrated' ORDER BY created_at DESC, rowid DESC LIMIT 1`, it.ID).
		Scan(&st.ckpID, &st.ckpGit, &st.ckpOpts, &st.ckpAt)
	if err == sql.ErrNoRows {
		err = nil
	}
	return st, err
}

// FinishApproval is the approved finish request bound to a root's newest integrated checkpoint.
type FinishApproval struct {
	RequestID, AgentID, Merge, Choice, CheckpointID string          // Merge "" for a pre-0022 approval
	Git                                             json.RawMessage // the checkpoint's git_json ([]GitRef shape; items can't import runtime)
}

// FinishApprovalTx is the exported accessor runtime uses (writeFinishing, MergeProgressFor,
// WatchMerges, resurfaceOpenRequests). ok is false when none.
func (s *Store) FinishApprovalTx(ctx context.Context, q querier, rootID string) (FinishApproval, bool, error) {
	it, err := s.getByID(ctx, q, rootID)
	if err != nil {
		return FinishApproval{}, false, err
	}
	st, err := s.rootState(ctx, q, it)
	if err != nil {
		return FinishApproval{}, false, err
	}
	return s.finishApproval(ctx, q, it, st)
}

// An approved row is bound to its integrated checkpoint only, not to the item revision:
// status moves (blocked and back) bump the revision but change nothing the user approved.
// Material edits stale the approval explicitly (staleApprovedFinish).
func (s *Store) finishApproval(ctx context.Context, q querier, it Item, st rootState) (fa FinishApproval, ok bool, err error) {
	if st.ckpID == "" {
		return fa, false, nil
	}
	err = q.QueryRowContext(ctx, `SELECT id, COALESCE(agent_id, ''), COALESCE(json_extract(binding_json, '$.merge'), ''),
		COALESCE(json_extract(binding_json, '$.choice'), '')
		FROM requests WHERE item_id = ? AND state = 'approved' AND kind IN ('accept_epic', 'accept_fix')
		AND json_extract(binding_json, '$.integrated_checkpoint') = ?
		ORDER BY responded_at DESC LIMIT 1`, it.ID, st.ckpID).Scan(&fa.RequestID, &fa.AgentID, &fa.Merge, &fa.Choice)
	if errors.Is(err, sql.ErrNoRows) {
		return FinishApproval{}, false, nil
	}
	if err != nil {
		return FinishApproval{}, false, err
	}
	fa.CheckpointID, fa.Git = st.ckpID, json.RawMessage(st.ckpGit)
	return fa, true, nil
}

// approvedCurrent reports an approved finish request for the current integration, and its $.merge ("" for a pre-0022 approval).
func (s *Store) approvedCurrent(ctx context.Context, q querier, it Item, st rootState) (bool, string, error) {
	fa, ok, err := s.finishApproval(ctx, q, it, st)
	return ok, fa.Merge, err
}

// ResolveRepoIDTx resolves a ref's repo spelling (a catalog id or name) to its catalog id, preferring
// the root's confirmed repos, then an id match, then the most recently used; "" when unknown.
func ResolveRepoIDTx(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, rootItemID, repo string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT id FROM repos WHERE (id = ? OR name = ?) AND id IN (SELECT value FROM json_each(
		(SELECT confirmed_repos_json FROM items WHERE id = ?))) ORDER BY id = ? DESC LIMIT 1`,
		repo, repo, rootItemID, repo).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = q.QueryRowContext(ctx, `SELECT id FROM repos WHERE id = ? OR name = ? ORDER BY id = ? DESC, last_used_at DESC LIMIT 1`,
			repo, repo, repo).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// IntegratedRepoIDsTx is the set of distinct repos in integrated git refs (git_json), keyed by
// resolved catalog id; an unknown repo keeps its spelling.
func IntegratedRepoIDsTx(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, rootItemID string, git []byte) (map[string]bool, error) {
	var refs []struct {
		Repo string `json:"repo"`
	}
	json.Unmarshal(git, &refs)
	repos := map[string]bool{}
	for _, r := range refs {
		id, err := ResolveRepoIDTx(ctx, q, rootItemID, r.Repo)
		if err != nil {
			return nil, err
		}
		if id == "" {
			id = r.Repo
		}
		repos[id] = true
	}
	return repos, nil
}

// mergeState counts the current integrated checkpoint's repos and their item_merges rows, by repo id.
func (s *Store) mergeState(ctx context.Context, q querier, it Item, st rootState) (merged, total int, closed bool, err error) {
	repos, err := IntegratedRepoIDsTx(ctx, q, it.ID, []byte(st.ckpGit))
	if err != nil {
		return 0, 0, false, err
	}
	total = len(repos)
	rows, err := q.QueryContext(ctx, `SELECT repo_id, state FROM item_merges WHERE item_id = ? AND integrated_checkpoint = ?`, it.ID, st.ckpID)
	if err != nil {
		return 0, 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var repoID, state string
		if err := rows.Scan(&repoID, &state); err != nil {
			return 0, 0, false, err
		}
		if state == "merged" && repos[repoID] {
			merged++
		}
		if state == "closed" {
			closed = true
		}
	}
	return merged, total, closed, rows.Err()
}

// finishedCurrent = approvedCurrent && (merge == "" || merged == total && total > 0 && !closed).
func (s *Store) finishedCurrent(ctx context.Context, q querier, it Item) (bool, error) {
	st, err := s.rootState(ctx, q, it)
	if err != nil {
		return false, err
	}
	approved, merge, err := s.approvedCurrent(ctx, q, it, st)
	if err != nil || !approved || merge == "" {
		return approved, err
	}
	merged, total, closed, err := s.mergeState(ctx, q, it, st)
	return merged == total && total > 0 && !closed, err
}

var overridable = []Status{Ready, InProgress, InReview, Done}

// forceStatusTx is TransitionTx for an orchestrator that gave a reason: a move
// the normal check allows stays a plain transition; one it denies is forced and
// recorded as an override. Only tasks and stories, never a root, and never out
// of Cancelled. Forcing Done also cancels the item's live workflow in the same
// tx (runtime's CancelWorkflow would move the task to Ready, which Done denies).
func (s *Store) forceStatusTx(ctx context.Context, tx *sql.Tx, key string, to Status, reason string, by Actor) error {
	it, err := s.getTx(ctx, tx, key)
	if err != nil {
		return err
	}
	if !by.isOrchestrator() {
		return errf(CodeBadRequest, errOrchestratorOnly)
	}
	reason = strings.TrimSpace(reason)
	switch {
	case it.ID == it.RootID && to == Done:
		return errf(CodeBadRequest, "A root reaches Done only through its finish question.")
	case it.ID == it.RootID || (it.Type != Task && it.Type != Story):
		return errf(CodeBadRequest, "Roots follow their own lifecycle; only a task or story can be overridden.")
	case !validReason(reason):
		return errf(CodeBadRequest, "Give a reason (1–300 characters).")
	case !slices.Contains(overridable, to):
		return errf(CodeBadRequest, "An override can set ready, in_progress, in_review or done.")
	case it.Status == to:
		return nil
	}
	denied := s.check(ctx, tx, it, to, by)
	if denied == nil {
		_, err := s.TransitionTx(ctx, tx, key, to, by)
		return err
	}
	var ie *Error
	if !errors.As(denied, &ie) || ie.Code != CodeTransitionDenied || it.Status == Cancelled {
		return denied
	}
	from, now := it.Status, s.Now()
	if to == Done {
		if _, err := tx.ExecContext(ctx, `UPDATE workflows SET state = 'cancelled', updated_at = ?
			WHERE item_id = ? AND state IN ('running', 'escalated')`, db.Millis(now), it.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET state = 'cancelled', ended_at = ?
			WHERE state IN ('active', 'waiting') AND workflow_id IN
			(SELECT id FROM workflows WHERE item_id = ? AND state = 'cancelled')`, db.Millis(now), it.ID); err != nil {
			return err
		}
	}
	if err := s.setStatus(ctx, tx, &it, to); err != nil {
		return err
	}
	if from == Done && to == Ready {
		if err := s.staleAccepts(ctx, tx, it); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(Override{Status: to, Reason: reason, Agent: by.AgentID, At: now})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET override_json = ? WHERE id = ?`, string(raw), it.ID); err != nil {
		return err
	}
	if _, err := s.Events.Append(ctx, tx, events.ItemOverridden, map[string]string{"key": it.Key,
		"root_key": it.RootKey, "from": string(from), "status": string(to), "reason": reason, "agent": by.AgentID}); err != nil {
		return err
	}
	return s.ReconcileTx(ctx, tx, it.Key)
}

func (s *Store) setStatus(ctx context.Context, tx *sql.Tx, it *Item, to Status) error {
	before := sql.NullString{}
	if to == Blocked {
		before = sql.NullString{String: string(it.Status), Valid: true}
	}
	// Any status change clears an override: it holds only while the status it forced stands.
	_, err := tx.ExecContext(ctx, `UPDATE items SET status = ?, status_before_block = ?, override_json = NULL,
		revision = revision + 1, updated_at = ? WHERE id = ?`, to, before, db.Millis(s.Now()), it.ID)
	if err != nil {
		return err
	}
	it.Status, it.Revision, it.Override = to, it.Revision+1, nil
	if to != Blocked {
		it.StatusBeforeBlock = ""
	}
	if err := s.changed(ctx, tx, *it); err != nil {
		return err
	}
	if to == Done && it.ID == it.RootID && s.RootDone != nil {
		if err := s.RootDone(ctx, tx, it.ID); err != nil {
			return err
		}
	}
	if (to == Done || to == Cancelled) && s.DepUnblocked != nil {
		return s.DepUnblocked(ctx, tx, it.ID)
	}
	return nil
}

// Reconcile recomputes daemon-owned state for key and every ancestor.
func (s *Store) Reconcile(ctx context.Context, key string) error {
	return s.write(ctx, func(tx *sql.Tx) error { return s.ReconcileTx(ctx, tx, key) })
}

func (s *Store) ReconcileTx(ctx context.Context, tx *sql.Tx, key string) error {
	it, err := s.getTx(ctx, tx, key)
	if err != nil {
		return err
	}
	for {
		switch it.Type {
		case Story:
			err = s.deriveStory(ctx, tx, it)
		case Epic, Bug, Chore:
			err = s.reconcileRoot(ctx, tx, it)
		case Spike:
			err = s.reconcileSpike(ctx, tx, it)
		}
		if err != nil || it.ParentID == "" {
			return err
		}
		if it, err = s.getByID(ctx, tx, it.ParentID); err != nil {
			return err
		}
	}
}

func (s *Store) deriveStory(ctx context.Context, tx *sql.Tx, it Item) error {
	if !slices.Contains([]Status{Ready, InProgress, InReview, Done}, it.Status) {
		return nil
	}
	if it.Override != nil && it.Override.Status == it.Status {
		return nil // an orchestrator forced this status; only a later transition clears it
	}
	var n, done, fin, review, moved int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(status = 'done'), 0),
		COALESCE(SUM(status IN ('done', 'cancelled')), 0), COALESCE(SUM(status = 'in_review'), 0),
		COALESCE(SUM(status NOT IN ('draft', 'ready')), 0)
		FROM items WHERE parent_id = ? AND archived_at IS NULL`, it.ID).Scan(&n, &done, &fin, &review, &moved)
	if err != nil || n == 0 {
		return err
	}
	var want Status
	switch {
	case fin == n && done > 0:
		if it.Workflow != nil && it.Workflow.AfterTasks != nil {
			succeeded, err := s.workflowSucceeded(ctx, tx, it.ID)
			if err != nil {
				return err
			}
			if succeeded {
				want = Done
			} else {
				want = InReview
				if s.StoryReadyForReview != nil {
					if err := s.StoryReadyForReview(ctx, tx, it); err != nil {
						return err
					}
				}
			}
		} else {
			want = Done
		}
	case fin == n:
		return nil // every child cancelled: leave the story as it is
	case fin+review == n:
		want = InReview
	case moved > 0:
		want = InProgress
	default:
		want = Ready
	}
	if want == it.Status {
		return nil
	}
	return s.setStatus(ctx, tx, &it, want)
}

func (s *Store) reconcileRoot(ctx context.Context, tx *sql.Tx, it Item) error {
	if it.Status == Ready {
		ok, err := s.acceptedSince(ctx, tx, it)
		if err != nil {
			return err
		}
		if ok {
			if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
				return err
			}
		}
	}
	if it.Status != InProgress && it.Status != InReview {
		return nil
	}
	st, err := s.rootState(ctx, tx, it)
	if err != nil {
		return err
	}
	kind := acceptKind(it.Type)

	// 1. open requests whose binding no longer matches go stale
	rows, err := tx.QueryContext(ctx, `SELECT id, binding_json FROM requests
		WHERE item_id = ? AND kind = ? AND state = 'open'`, it.ID, kind)
	if err != nil {
		return err
	}
	var staleIDs []string
	openCurrent := false
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var b acceptBinding
		json.Unmarshal([]byte(raw), &b)
		if st.finished && b.IntegratedCheckpoint == st.ckpID && b.ItemRevision == it.Revision {
			openCurrent = true
		} else {
			staleIDs = append(staleIDs, id)
		}
	}
	rows.Close()
	for _, id := range staleIDs {
		if err := s.resolveStale(ctx, tx, id, kind, it.Key); err != nil {
			return err
		}
	}

	if it.Status == InReview {
		// 2. an approval bound to the current integration finishes the item
		// once every integrated repo is merged (a pre-0022 approval has no merge: Done)
		approved, merge, err := s.approvedCurrent(ctx, tx, it, st)
		if err != nil {
			return err
		}
		if approved && st.finished {
			if merge == "" {
				return s.setStatus(ctx, tx, &it, Done)
			}
			merged, total, closed, err := s.mergeState(ctx, tx, it, st)
			if err != nil {
				return err
			}
			switch {
			case closed:
				// a PR closed without merging: back to work; step 4 sees this
				// checkpoint's request, so none opens until a new integration
				if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
					return err
				}
			case merged == total && total > 0:
				return s.setStatus(ctx, tx, &it, Done)
			default:
				return nil
			}
		} else if openCurrent {
			// 3. no live request (stale, changes requested, child reopened): back to work
			return nil
		} else if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
			return err
		}
	}

	// 4. finished children plus a fresh integration open exactly one request
	if !st.finished || st.ckpID == "" || st.ckpAt < st.lastChild {
		return nil
	}
	seen, err := exists(ctx, tx, `SELECT 1 FROM requests WHERE item_id = ? AND kind = ?
		AND json_extract(binding_json, '$.integrated_checkpoint') = ?`, it.ID, kind, st.ckpID)
	if err != nil || seen {
		return err
	}
	if err := s.setStatus(ctx, tx, &it, InReview); err != nil {
		return err
	}
	prompt := "Review completed work and accept the epic."
	switch it.Type {
	case Bug:
		prompt = "Review the fix and accept it."
	case Chore:
		prompt = "Review the chore and accept it."
	}
	ab := acceptBinding{ItemRevision: it.Revision, IntegratedCheckpoint: st.ckpID, Git: json.RawMessage(st.ckpGit)}
	if st.ckpOpts != "" {
		ab.FinishOptions = json.RawMessage(st.ckpOpts)
	}
	binding, _ := json.Marshal(ab)
	id := ids.New("req")
	if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at)
		VALUES (?, ?, ?, ?, 'open', ?, ?)`, id, kind, it.ID, prompt, string(binding), db.Millis(s.Now())); err != nil {
		return err
	}
	payload, err := s.requestPayload(ctx, tx, id, kind, it.Key, "open")
	if err != nil {
		return err
	}
	if _, err := s.Events.Append(ctx, tx, events.RequestOpened, payload); err != nil {
		return err
	}
	if s.RequestOpened != nil {
		return s.RequestOpened(ctx, tx, id)
	}
	return nil
}

func (s *Store) reconcileSpike(ctx context.Context, tx *sql.Tx, it Item) error {
	if it.Status == Draft || it.Status == Ready {
		ok, err := s.acceptedSince(ctx, tx, it)
		if err != nil || !ok {
			return err
		}
		if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
			return err
		}
	}
	if it.Status != InProgress && it.Status != AwaitingApproval {
		return nil
	}
	closed, err := exists(ctx, tx, `SELECT 1 FROM requests WHERE item_id = ? AND kind = 'close_spike' AND state = 'approved'`, it.ID)
	if err != nil {
		return err
	}
	if closed {
		return s.setStatus(ctx, tx, &it, Done)
	}
	open, err := s.openApproval(ctx, tx, it)
	if err != nil {
		return err
	}
	switch {
	case open && it.Status == InProgress:
		return s.setStatus(ctx, tx, &it, AwaitingApproval)
	case !open && it.Status == AwaitingApproval:
		return s.setStatus(ctx, tx, &it, InProgress)
	}
	return nil
}

// resolveTx closes a Draft/Ready root as Done because another, Done root resolved it. It is a
// deliberate, audited exception to "a root reaches Done only through the finish flow"
// (docs/specs/2026-10-03-resolved-by-close.md); the side effects are cancel's.
func (s *Store) resolveTx(ctx context.Context, tx *sql.Tx, it Item, p Patch, by Actor) (Item, error) {
	rest := p
	rest.ResolvedBy, rest.Status, rest.Revision = nil, nil, 0
	if !reflect.DeepEqual(rest, Patch{}) {
		return Item{}, errf(CodeBadRequest, "resolved_by can't be combined with other changes.")
	}
	if p.Status == nil || *p.Status != Done {
		return Item{}, errf(CodeBadRequest, "resolved_by needs status done.")
	}
	if !(by.Kind == ActorUser || by.isOrchestrator()) {
		return Item{}, deny("Only the user or an orchestrator can resolve %s.", it.Key)
	}
	if it.ID != it.RootID || !isAcceptRoot(it.Type) {
		return Item{}, deny("Only a top-level epic, bug or chore can be resolved by another item; %s isn't one.", it.Key)
	}
	if it.Status != Draft && it.Status != Ready {
		return Item{}, deny("Only a Draft or Ready item can be resolved by another item; %s is %s.", it.Key, StatusLabel(it.Status))
	}
	by0 := strings.TrimSpace(*p.ResolvedBy)
	target, err := s.getTx(ctx, tx, by0)
	if err != nil {
		return Item{}, deny("Can't resolve %s: no item %s.", it.Key, by0)
	}
	switch {
	case target.ID == it.ID:
		return Item{}, deny("%s can't be resolved by itself.", it.Key)
	case target.ID != target.RootID || !isAcceptRoot(target.Type):
		return Item{}, deny("%s must be resolved by a top-level epic, bug or chore; %s isn't one.", it.Key, target.Key)
	case target.Status != Done:
		return Item{}, deny("%s must be Done before it can resolve %s; it is %s.", target.Key, it.Key, StatusLabel(target.Status))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET resolved_by_id = ? WHERE id = ?`, target.ID, it.ID); err != nil {
		return Item{}, err
	}
	it.ResolvedBy = target.Key
	if err := s.setStatus(ctx, tx, &it, Done); err != nil {
		return Item{}, err
	}
	if err := s.cancelDescendants(ctx, tx, it.ID); err != nil {
		return Item{}, err
	}
	if err := s.staleAccepts(ctx, tx, it); err != nil {
		return Item{}, err
	}
	actor := by.Kind
	if by.AgentID != "" {
		actor = by.AgentID
	}
	if _, err := s.Events.Append(ctx, tx, events.ItemResolved, map[string]string{"key": it.Key,
		"root_key": it.RootKey, "resolved_by": target.Key, "actor": actor}); err != nil {
		return Item{}, err
	}
	if err := s.ReconcileTx(ctx, tx, it.Key); err != nil {
		return Item{}, err
	}
	return s.getByID(ctx, tx, it.ID)
}

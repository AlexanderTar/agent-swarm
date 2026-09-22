package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

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
	// reopen: old acceptances and close approvals no longer count; cancel: nothing is left to accept
	if (to == Ready && (from == Done || from == Cancelled)) || to == Cancelled {
		if err := s.staleAccepts(ctx, tx, it); err != nil {
			return Item{}, err
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
			return deny("Couldn't move %s to Done. Complete all child tasks and their checkpoints first.", it.Key)
		}
		return generic // derived; only reconciliation moves stories
	case Spike:
		return s.checkSpike(ctx, tx, it, to, daemon, generic)
	case Epic, Bug:
		return s.checkRoot(ctx, tx, it, to, daemon, orch, generic)
	}
	return s.checkTask(ctx, tx, it, to, daemon, orch, generic)
}

func (s *Store) checkTask(ctx context.Context, tx *sql.Tx, it Item, to Status, daemon, orch bool, generic *Error) error {
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
	case it.Status == InProgress && to == InReview && daemon:
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
			ok, err := s.approvedCurrent(ctx, tx, it)
			if err != nil || ok {
				return err
			}
		}
		if it.Type == Epic {
			return deny("Accept this epic to mark it Done.")
		}
		return deny("Accept this fix to mark it Done.")
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

func (s *Store) completedCurrent(ctx context.Context, q querier, it Item) (bool, error) {
	return exists(ctx, q, `SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'completed'
		AND attempt = (SELECT MAX(attempt) FROM checkpoints WHERE item_id = ?)`, it.ID, it.ID)
}

func (s *Store) openApproval(ctx context.Context, q querier, it Item) (bool, error) {
	return exists(ctx, q, `SELECT 1 FROM requests WHERE item_id = ? AND state = 'open'
		AND kind IN ('approve_section', 'approve_plan', 'approve_report', 'close_spike')`, it.ID)
}

type acceptBinding struct {
	ItemRevision         int             `json:"item_revision"`
	IntegratedCheckpoint string          `json:"integrated_checkpoint"`
	Git                  json.RawMessage `json:"git"`
}

type rootState struct {
	finished  bool
	lastChild int64
	ckpID     string
	ckpGit    string
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
	st.finished = n > 0 && fin == n
	err = q.QueryRowContext(ctx, `SELECT id, git_json, created_at FROM checkpoints
		WHERE item_id = ? AND kind = 'integrated' ORDER BY created_at DESC, rowid DESC LIMIT 1`, it.ID).
		Scan(&st.ckpID, &st.ckpGit, &st.ckpAt)
	if err == sql.ErrNoRows {
		err = nil
	}
	return st, err
}

func (s *Store) approvedCurrent(ctx context.Context, q querier, it Item) (bool, error) {
	st, err := s.rootState(ctx, q, it)
	if err != nil || st.ckpID == "" {
		return false, err
	}
	return exists(ctx, q, `SELECT 1 FROM requests WHERE item_id = ? AND state = 'approved'
		AND kind IN ('accept_epic', 'accept_fix')
		AND json_extract(binding_json, '$.integrated_checkpoint') = ?
		AND json_extract(binding_json, '$.item_revision') = ?`, it.ID, st.ckpID, it.Revision)
}

func (s *Store) setStatus(ctx context.Context, tx *sql.Tx, it *Item, to Status) error {
	before := sql.NullString{}
	if to == Blocked {
		before = sql.NullString{String: string(it.Status), Valid: true}
	}
	_, err := tx.ExecContext(ctx, `UPDATE items SET status = ?, status_before_block = ?, revision = revision + 1,
		updated_at = ? WHERE id = ?`, to, before, db.Millis(s.Now()), it.ID)
	if err != nil {
		return err
	}
	it.Status, it.Revision = to, it.Revision+1
	if to != Blocked {
		it.StatusBeforeBlock = ""
	}
	if err := s.changed(ctx, tx, *it); err != nil {
		return err
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
		case Epic, Bug:
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
		want = Done
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
	kind := "accept_epic"
	if it.Type == Bug {
		kind = "accept_fix"
	}

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
		// 2. an approval bound to the current integration and revision completes the item
		approved, err := s.approvedCurrent(ctx, tx, it)
		if err != nil {
			return err
		}
		if approved && st.finished {
			return s.setStatus(ctx, tx, &it, Done)
		}
		// 3. no live request (stale, changes requested, child reopened): back to work
		if openCurrent {
			return nil
		}
		if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
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
	if it.Type == Bug {
		prompt = "Review the fix and accept it."
	}
	binding, _ := json.Marshal(acceptBinding{ItemRevision: it.Revision, IntegratedCheckpoint: st.ckpID, Git: json.RawMessage(st.ckpGit)})
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

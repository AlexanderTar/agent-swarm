package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
)

// expansionClause is §17.5's optional clause on the confirm_repos notification.
func expansionClause(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", and suggests adding %d", n)
}

func (s *Store) repoNameTx(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT name FROM repos WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("Unknown repository %s.", id)}
	}
	return name, err
}

// repoRef is one entry of the repos_confirmed message payload (D49): agents
// read names and paths from their brief, not repo ids.
type repoRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Store) repoRefs(ctx context.Context, tx *sql.Tx, repoIDs []string) ([]repoRef, error) {
	out := make([]repoRef, 0, len(repoIDs))
	for _, id := range repoIDs {
		r := repoRef{ID: id}
		err := tx.QueryRowContext(ctx, `SELECT name, path FROM repos WHERE id = ?`, id).Scan(&r.Name, &r.Path)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf("Unknown repository %s.", id)}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// removed returns the ids in current that are absent from next.
func removed(current, next []string) []string {
	keep := map[string]bool{}
	for _, id := range next {
		keep[id] = true
	}
	var out []string
	for _, id := range current {
		if !keep[id] {
			out = append(out, id)
		}
	}
	return out
}

// repoHasLiveReservations reports whether repoID still has an unreleased
// worktree reservation under rootItemID (I13), plus the repo's name for the
// error copy.
func (s *Store) repoHasLiveReservations(ctx context.Context, tx *sql.Tx, rootItemID, repoID string) (bool, string, error) {
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM worktree_reservations wr JOIN worktrees w ON w.id = wr.worktree_id
		WHERE w.repo_id = ? AND w.root_item_id = ? AND wr.released_at IS NULL)`,
		repoID, rootItemID).Scan(&busy)
	if err != nil {
		return false, "", err
	}
	name, err := s.repoNameTx(ctx, tx, repoID)
	return busy, name, err
}

// askConfirmRepos is swarm_ask kind:"confirm_repos" (L25, I13, D42). Only an
// orchestrator may ask, every proposal needs a one-line reason, and every repo
// the user picked at spike creation must be accounted for — kept or explicitly
// dropped with its own reason.
func (s *Store) askConfirmRepos(ctx context.Context, sessionID string, in AskInput) (Request, error) {
	if n := utf8.RuneCountInString(in.Prompt); n < 1 || n > 1000 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Prompt must be 1–1000 characters."}
	}
	if len(in.Repos) == 0 || len(in.Repos) > 50 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Propose 1 to 50 repositories."}
	}
	for _, p := range in.Repos {
		if p.Reason == "" {
			return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Every proposed repository needs a one-line reason."}
		}
	}
	for _, p := range in.Expansion {
		if p.Reason == "" {
			return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Every suggested repository needs a one-line reason."}
		}
	}
	var out Request
	_, err := IdemTx(ctx, s, sessionID, in.RequestID, "swarm_ask", &out, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if a.Role != RoleOrchestrator {
			return &items.Error{Code: items.CodeBadRequest, Message: "Only an orchestrator can ask to confirm repositories."}
		}
		rootKey, err := s.itemKey(ctx, tx, a.RootItemID)
		if err != nil {
			return err
		}
		root, err := s.Items.GetTx(ctx, tx, rootKey)
		if err != nil {
			return err
		}
		// Every repo the user picked at creation must appear in the proposal —
		// either to keep, or with source "dropped" and a reason. Silence is
		// refused, because the New-orchestrator window showed the user that list
		// and the confirm sheet would otherwise just lose an entry unexplained.
		proposed := map[string]bool{}
		for _, p := range in.Repos {
			proposed[p.Repo] = true
		}
		for _, rid := range root.SuggestedRepos {
			if !proposed[rid] {
				name, err := s.repoNameTx(ctx, tx, rid)
				if err != nil {
					return err
				}
				return &items.Error{Code: items.CodeBadRequest,
					Message: fmt.Sprintf("Say why %s was dropped, or include it.", name)}
			}
		}
		for _, p := range in.Repos {
			if _, err := s.repoNameTx(ctx, tx, p.Repo); err != nil {
				return err
			}
		}
		for _, p := range in.Expansion {
			if _, err := s.repoNameTx(ctx, tx, p.Repo); err != nil {
				return err
			}
		}
		options, err := json.Marshal(map[string]any{"proposed": in.Repos, "expansion": in.Expansion})
		if err != nil {
			return err
		}
		binding, err := json.Marshal(map[string]int{"repos_version": root.ReposVersion})
		if err != nil {
			return err
		}
		id := ids.New("req")
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, agent_id, session_id, item_id,
			prompt, options_json, state, binding_json, created_at)
			VALUES (?, 'confirm_repos', ?, ?, ?, ?, ?, 'open', ?, ?)`,
			id, a.ID, sessionID, root.ID, in.Prompt, string(options), string(binding), db.Millis(s.Now())); err != nil {
			return err
		}
		out, err = s.finishOpen(ctx, tx, id, a.Name, rootKey, map[string]string{
			"N": strconv.Itoa(len(in.Repos)), "expansion": expansionClause(len(in.Expansion))})
		return err
	})
	return out, err
}

// validateRepoConfirmTx is the read half of a repo confirmation (I13): the
// repos_version check, that every id exists, and that no dropped repo still
// has a live worktree reservation. Shared by the request-bound ConfirmRepos
// and the item-level ValidateItemRepos (orchestrator spawn, L25).
func (s *Store) validateRepoConfirmTx(ctx context.Context, tx *sql.Tx, root items.Item, repoIDs []string, version int) ([]repoRef, error) {
	if len(repoIDs) == 0 {
		return nil, &items.Error{Code: items.CodeBadRequest, Message: "Choose at least one repository."}
	}
	if root.ReposVersion != version {
		return nil, &items.Error{Code: items.CodeConflict, Message: "This request changed. Review the latest version."}
	}
	refs, err := s.repoRefs(ctx, tx, repoIDs) // also proves every id exists
	if err != nil {
		return nil, err
	}
	for _, dropped := range removed(root.Repos, repoIDs) {
		busy, name, err := s.repoHasLiveReservations(ctx, tx, root.ID, dropped)
		if err != nil {
			return nil, err
		}
		if busy {
			return nil, &items.Error{Code: items.CodeConflict,
				Message: fmt.Sprintf("%s has active worktrees. Finish or release them first.", name)}
		}
	}
	return refs, nil
}

// commitRepoConfirmTx writes the confirmed set on root. The caller reconciles
// (ReconcileTx) at whatever point in its own flow that belongs.
func (s *Store) commitRepoConfirmTx(ctx context.Context, tx *sql.Tx, root items.Item, repoIDs []string) error {
	_, err := tx.ExecContext(ctx, `UPDATE items SET confirmed_repos_json = ?,
		repos_version = repos_version + 1, updated_at = ? WHERE id = ?`,
		jsonArray(repoIDs), db.Millis(s.Now()), root.ID)
	return err
}

// ValidateItemRepos is the read half of a repo confirmation for itemKey, with
// no associated request (L25: the orchestrator-spawn sheet's own repo
// picker). It resolves repoIDs to their filesystem paths, so the caller can
// feed them into a new agent's own Preflight (§11.4 steps 6-7), and returns
// before anything is written — the write happens only once the spawn that
// will use these repos has actually succeeded (see CommitItemRepos).
func (s *Store) ValidateItemRepos(ctx context.Context, itemKey string, repoIDs []string, version int) ([]string, error) {
	var paths []string
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		root, err := s.Items.GetTx(ctx, tx, itemKey)
		if err != nil {
			return err
		}
		refs, err := s.validateRepoConfirmTx(ctx, tx, root, repoIDs, version)
		if err != nil {
			return err
		}
		paths = make([]string, len(refs))
		for i, r := range refs {
			paths[i] = r.Path
		}
		return nil
	})
	return paths, err
}

// CommitItemRepos writes the confirmed set on itemKey and reconciles, once
// the orchestrator that will use these repos has actually spawned (L25).
func (s *Store) CommitItemRepos(ctx context.Context, itemKey string, repoIDs []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		root, err := s.Items.GetTx(ctx, tx, itemKey)
		if err != nil {
			return err
		}
		if err := s.commitRepoConfirmTx(ctx, tx, root, repoIDs); err != nil {
			return err
		}
		return s.Items.ReconcileTx(ctx, tx, itemKey)
	})
}

// ConfirmRepos records the user's repository choice (L25, I13). It is a UI or
// CLI action, so it writes the only repos_confirmed message the system ever
// produces.
func (s *Store) ConfirmRepos(ctx context.Context, id string, repoIDs []string, comment string, version int, via string) (Request, error) {
	if len(repoIDs) == 0 {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: "Choose at least one repository."}
	}
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		req, err := s.requestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if req.State != "open" {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		rootKey, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return err
		}
		root, err := s.Items.GetTx(ctx, tx, rootKey)
		if err != nil {
			return err
		}
		refs, err := s.validateRepoConfirmTx(ctx, tx, root, repoIDs, version)
		if err != nil {
			return err
		}
		now := db.Millis(s.Now())
		if err := s.commitRepoConfirmTx(ctx, tx, root, repoIDs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'approved',
			confirmed_json = ?, response_text = ?, responded_via = ?, responded_at = ?
			WHERE id = ?`, jsonArray(repoIDs), nullIf(comment), nullIf(via), now, id); err != nil {
			return err
		}
		// "user_action" appears here, in an allow-listed method, not in the shared
		// resolve helper (R5).
		payload, err := json.Marshal(map[string]any{"repos": refs, "comment": comment})
		if err != nil {
			return err
		}
		if _, err := s.enqueue(ctx, tx, Message{Kind: "repos_confirmed", Origin: "user_action",
			ToAgentID: req.AgentID, RootItemID: root.ID, RequestID: id, Payload: payload}); err != nil {
			return err
		}
		w, err := s.RequestWireTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
			return err
		}
		if err := s.Items.ReconcileTx(ctx, tx, rootKey); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, id)
		return err
	})
	return out, err
}

// ConfirmedRepos returns the top-level item's currently confirmed repositories.
func (s *Store) ConfirmedRepos(ctx context.Context, rootItemID string) ([]repos.Repo, error) {
	var raw string
	if err := s.DB.QueryRowContext(ctx, `SELECT confirmed_repos_json FROM items WHERE id = ?`,
		rootItemID).Scan(&raw); err != nil {
		return nil, err
	}
	var repoIDs []string
	if err := json.Unmarshal([]byte(raw), &repoIDs); err != nil {
		return nil, err
	}
	out := make([]repos.Repo, 0, len(repoIDs))
	for _, id := range repoIDs {
		var r repos.Repo
		var remoteURL, remoteOwner, defaultBranch sql.NullString
		var missing int
		if err := s.DB.QueryRowContext(ctx, `SELECT id, name, path, remote_url, remote_owner,
			default_branch, source, missing FROM repos WHERE id = ?`, id).Scan(
			&r.ID, &r.Name, &r.Path, &remoteURL, &remoteOwner, &defaultBranch, &r.Source, &missing); err != nil {
			return nil, err
		}
		r.RemoteURL, r.RemoteOwner, r.DefaultBranch = remoteURL.String, remoteOwner.String, defaultBranch.String
		r.Missing = missing != 0
		out = append(out, r)
	}
	return out, nil
}

// CloseSpike approves a close_spike request (I4); P1's reconcileSpike then
// finishes the spike with no materialized item.
func (s *Store) CloseSpike(ctx context.Context, id, via string) (Request, error) {
	check := func(req Request) error {
		if req.Kind != "close_spike" {
			return &items.Error{Code: items.CodeBadRequest, Message: "This request is not a close_spike."}
		}
		return nil
	}
	return s.resolve(ctx, id, "approved", "", via, "user_action", check,
		func(req Request) (MessageKind, any) {
			return "approval_result", map[string]any{"decision": "approved"}
		})
}

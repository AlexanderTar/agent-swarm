// Package worktree runs the git worktree operations the daemon performs for an
// orchestrator (spec §12.1, §12.2).
package worktree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

// Worktree is one row of the worktrees table.
type Worktree struct {
	ID, RepoID, Path, Branch, DetachedSHA, BaseRef, BaseSHA string
	OwnerAgentID, RootItemID, State, RetainedReason         string
	CreatedAt                                               time.Time
	RemovedAt                                               *time.Time
}

// Service runs the worktree operations against one database.
type Service struct {
	DB  *db.DB
	Run execx.Runner
	Now func() time.Time
	Log func(format string, args ...any)
	// OnRetained fires inside the same transaction that marks a worktree
	// retained, so a caller can e.g. raise a notification atomically.
	OnRetained func(ctx context.Context, tx *sql.Tx, wt Worktree) error
}

// CreateInput is Create and Review's input.
type CreateInput struct {
	RepoID, RepoPath, Branch, Base, OwnerAgentID, RootItemID string
}

func (s *Service) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

// BranchName is §10.4: "<type>/<key-lower>-<slug>" with the title cut to 24 chars.
func BranchName(rootType, rootKey, rootTitle string) string {
	slug, err := ids.KebabMax(rootTitle, 24)
	if err != nil {
		slug = ""
	}
	name := strings.ToLower(rootKey)
	if slug != "" {
		name += "-" + slug
	}
	return rootType + "/" + name
}

// PathFor is §12.1: "<repo parent>/<repo name>--<slug>", where the slug is the
// branch with its type prefix removed. A collision gets -2, -3, …
func PathFor(repoPath, branch string, exists func(string) bool) string {
	_, tail, ok := strings.Cut(branch, "/")
	if !ok {
		tail = branch
	}
	slug, err := ids.Kebab(tail)
	if err != nil {
		slug = "work"
	}
	base := filepath.Join(filepath.Dir(repoPath), filepath.Base(repoPath)+"--"+slug)
	p := base
	for i := 2; exists(p); i++ {
		p = fmt.Sprintf("%s-%d", base, i)
	}
	return p
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (s *Service) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return s.Run(ctx, "git", append([]string{"-C", dir}, args...)...)
}

// SigningOK is preflight step 7 (L23). An unset or false value fails.
func (s *Service) SigningOK(ctx context.Context, repoPath string) error {
	out, err := s.git(ctx, repoPath, "config", "commit.gpgsign")
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("Commit signing is off for %s. Enable it in git config.", filepath.Base(repoPath))
	}
	return nil
}

// DirtyStrict reports uncommitted changes. A command failure means dirty: we
// could not prove the tree is clean, and deleting it would lose work.
func (s *Service) DirtyStrict(ctx context.Context, path string) (bool, error) {
	out, err := s.git(ctx, path, "status", "--porcelain")
	if err != nil {
		return true, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// branchExists reports whether branch is a known local branch.
func (s *Service) branchExists(ctx context.Context, repoPath, branch string) bool {
	_, err := s.git(ctx, repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// defaultBase is "origin/<default_branch>" when a remote exists, otherwise the
// local default branch, read from the repos row and falling back to the repo's
// current HEAD.
func (s *Service) defaultBase(ctx context.Context, repoPath string) (string, error) {
	var branch sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT default_branch FROM repos WHERE path = ?`, repoPath).Scan(&branch); err != nil && err != sql.ErrNoRows {
		return "", err
	}
	name := branch.String
	if name == "" {
		out, err := s.git(ctx, repoPath, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return "", fmt.Errorf("worktree: could not determine the default branch of %s: %w", repoPath, err)
		}
		name = strings.TrimSpace(string(out))
	}
	if _, err := s.git(ctx, repoPath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name); err == nil {
		return "origin/" + name, nil
	}
	return name, nil
}

// insert writes the worktrees row and the owner's rw reservation in one transaction.
func (s *Service) insert(ctx context.Context, wt Worktree) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var detached, branch sql.NullString
		if wt.DetachedSHA != "" {
			detached = sql.NullString{String: wt.DetachedSHA, Valid: true}
		}
		if wt.Branch != "" {
			branch = sql.NullString{String: wt.Branch, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO worktrees
			(id, repo_id, path, branch, detached_sha, base_ref, base_sha, owner_agent_id, root_item_id, state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			wt.ID, wt.RepoID, wt.Path, branch, detached, wt.BaseRef, wt.BaseSHA,
			wt.OwnerAgentID, wt.RootItemID, wt.State, db.Millis(wt.CreatedAt)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO worktree_reservations
			(worktree_id, agent_id, mode, created_at) VALUES (?, ?, 'rw', ?)`,
			wt.ID, wt.OwnerAgentID, db.Millis(wt.CreatedAt))
		return err
	})
}

// retain sets state='retained', the reason, and calls OnRetained inside the
// same transaction.
func (s *Service) retain(ctx context.Context, wt Worktree, reason string) (Worktree, error) {
	wt.State, wt.RetainedReason = "retained", reason
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE worktrees SET state = 'retained', retained_reason = ? WHERE id = ?`, reason, wt.ID); err != nil {
			return err
		}
		if s.OnRetained != nil {
			return s.OnRetained(ctx, tx, wt)
		}
		return nil
	})
	return wt, err
}

func scanWorktree(row interface {
	Scan(dest ...any) error
}) (Worktree, error) {
	var wt Worktree
	var branch, detached, retainedReason sql.NullString
	var createdAt int64
	var removedAt sql.NullInt64
	if err := row.Scan(&wt.ID, &wt.RepoID, &wt.Path, &branch, &detached, &wt.BaseRef, &wt.BaseSHA,
		&wt.OwnerAgentID, &wt.RootItemID, &wt.State, &retainedReason, &createdAt, &removedAt); err != nil {
		return Worktree{}, err
	}
	wt.Branch = branch.String
	wt.DetachedSHA = detached.String
	wt.RetainedReason = retainedReason.String
	wt.CreatedAt = db.FromMillis(createdAt)
	if removedAt.Valid {
		t := db.FromMillis(removedAt.Int64)
		wt.RemovedAt = &t
	}
	return wt, nil
}

const worktreeCols = `id, repo_id, path, branch, detached_sha, base_ref, base_sha,
	owner_agent_id, root_item_id, state, retained_reason, created_at, removed_at`

// Get returns one worktree by id.
func (s *Service) Get(ctx context.Context, wtID string) (Worktree, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+worktreeCols+` FROM worktrees WHERE id = ?`, wtID)
	return scanWorktree(row)
}

// query returns worktrees matching a clause appended after "FROM worktrees "
// (a bare WHERE, or an alias like "w WHERE ..." for a correlated subquery)
// with its positional args.
func (s *Service) query(ctx context.Context, where string, args ...any) ([]Worktree, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+worktreeCols+` FROM worktrees `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Worktree
	for rows.Next() {
		wt, err := scanWorktree(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wt)
	}
	return out, rows.Err()
}

// ForAgent returns every worktree owned by, or shared with, agentID.
func (s *Service) ForAgent(ctx context.Context, agentID string) ([]Worktree, error) {
	return s.query(ctx, `w WHERE owner_agent_id = ? OR EXISTS (SELECT 1 FROM worktree_reservations r
		WHERE r.worktree_id = w.id AND r.agent_id = ? AND r.released_at IS NULL)`, agentID, agentID)
}

func (s *Service) repoPath(ctx context.Context, repoID string) (string, error) {
	var path string
	err := s.DB.QueryRowContext(ctx, `SELECT path FROM repos WHERE id = ?`, repoID).Scan(&path)
	return path, err
}

// Share records agentID's reservation on the worktree. It refuses once the
// worktree has left the active state (C4): Remove's claim and this check run
// as one-statement transactions on the same row, and SQLite's writer lock
// (db.Open's _txlock=immediate) serializes them, so a Share racing a Remove
// either lands before the claim (Remove then sees it and backs off) or after
// (Share then sees the claimed state and refuses) — never in between.
func (s *Service) Share(ctx context.Context, wtID, agentID, mode string) error {
	if mode != "rw" && mode != "ro" {
		return fmt.Errorf("worktree: unknown share mode %q", mode)
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM worktrees WHERE id = ?`, wtID).Scan(&state); err != nil {
			return err
		}
		if state != "active" {
			return fmt.Errorf("worktree: %s is not active", wtID)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO worktree_reservations
			(worktree_id, agent_id, mode, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(worktree_id, agent_id) DO UPDATE SET mode = excluded.mode, released_at = NULL`,
			wtID, agentID, mode, db.Millis(s.Now()))
		return err
	})
}

// Release clears agentID's reservation on the worktree.
func (s *Service) Release(ctx context.Context, wtID, agentID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ?
		WHERE worktree_id = ? AND agent_id = ? AND released_at IS NULL`, db.Millis(s.Now()), wtID, agentID)
	return err
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Worktree, error) {
	if in.Branch == "" {
		return Worktree{}, errors.New("worktree: a branch is required")
	}
	base := in.Base
	if base == "" {
		var err error
		if base, err = s.defaultBase(ctx, in.RepoPath); err != nil {
			return Worktree{}, err
		}
	}
	// a failed fetch is noted and ignored (§12.1)
	if _, err := s.git(ctx, in.RepoPath, "fetch", "--quiet", "origin"); err != nil {
		s.logf("worktree: fetch %s failed, using the local base: %v", in.RepoPath, err)
	}
	path := PathFor(in.RepoPath, in.Branch, fileExists)
	args := []string{"worktree", "add"}
	if !s.branchExists(ctx, in.RepoPath, in.Branch) {
		args = append(args, "-b", in.Branch, path, base)
	} else {
		args = append(args, path, in.Branch)
	}
	if out, err := s.git(ctx, in.RepoPath, args...); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add: %w: %s", err, out)
	}
	sha, err := s.git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return Worktree{}, err
	}
	wt := Worktree{ID: ids.New("wt"), RepoID: in.RepoID, Path: path, Branch: in.Branch,
		BaseRef: base, BaseSHA: strings.TrimSpace(string(sha)),
		OwnerAgentID: in.OwnerAgentID, RootItemID: in.RootItemID, State: "active", CreatedAt: s.Now()}
	return wt, s.insert(ctx, wt)
}

// shaPattern rejects anything that isn't a plausible git commit-ish before it
// reaches filepath.Join or an exec argv: sha is agent-controlled (the review
// {repo, sha} MCP operation), and length alone (len(sha) < 7) let arbitrary
// characters, including path separators, through.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Review creates the detached worktree a reviewer reads (I5).
func (s *Service) Review(ctx context.Context, in CreateInput, sha string) (Worktree, error) {
	if !shaPattern.MatchString(sha) {
		return Worktree{}, fmt.Errorf("worktree: %q is not a sha", sha)
	}
	path := filepath.Join(filepath.Dir(in.RepoPath), filepath.Base(in.RepoPath)+"--review-"+sha[:7])
	for i := 2; fileExists(path); i++ {
		path = fmt.Sprintf("%s-%d", path, i)
	}
	if out, err := s.git(ctx, in.RepoPath, "worktree", "add", "--detach", path, sha); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add --detach: %w: %s", err, out)
	}
	wt := Worktree{ID: ids.New("wt"), RepoID: in.RepoID, Path: path, DetachedSHA: sha,
		BaseRef: sha, BaseSHA: sha, OwnerAgentID: in.OwnerAgentID, RootItemID: in.RootItemID,
		State: "active", CreatedAt: s.Now()}
	return wt, s.insert(ctx, wt)
}

// Remove runs the §12.1 checks in order. Only the owner may call it, and only
// when nobody else holds an unreleased reservation (C4). The "no other
// reservation" check and the state change that keeps a new one from landing
// happen in one transaction (L12/§5): see claimForRemoval and Share.
func (s *Service) Remove(ctx context.Context, wtID, callerAgentID string) (Worktree, error) {
	wt, err := s.Get(ctx, wtID)
	if err != nil {
		return Worktree{}, err
	}
	if wt.OwnerAgentID != callerAgentID {
		return Worktree{}, fmt.Errorf("worktree: only the owner can remove %s", wt.Path)
	}
	claimed, err := s.claimForRemoval(ctx, wtID, callerAgentID)
	if err != nil {
		return Worktree{}, err
	}
	if !claimed {
		return Worktree{}, fmt.Errorf("worktree: %s is held by another agent, or a removal is already in progress", wt.Path)
	}
	wt.State, wt.RetainedReason = "retained", removingReason
	return s.remove(ctx, wt)
}

// removingReason is a private worktrees.retained_reason sentinel. worktrees.state
// only allows 'active'/'retained'/'removed' (no migration in P2), so
// claimForRemoval borrows 'retained' as the claimed/in-flight marker; remove
// always overwrites it with the real outcome (a genuine retained_reason, or
// 'removed' with retained_reason cleared) before returning.
const removingReason = "__removing__"

// claimForRemoval verifies, in one transaction, that wtID is active, owned by
// callerAgentID and has no other unreleased reservation, then marks it
// retained/removingReason so a concurrent Share is refused (C4). It reports
// whether the claim succeeded; false means someone else already holds or is
// removing it, not an error.
func (s *Service) claimForRemoval(ctx context.Context, wtID, callerAgentID string) (bool, error) {
	var claimed bool
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var owner, state string
		if err := tx.QueryRowContext(ctx, `SELECT owner_agent_id, state FROM worktrees WHERE id = ?`, wtID).
			Scan(&owner, &state); err != nil {
			return err
		}
		if owner != callerAgentID || state != "active" {
			return nil
		}
		var others int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktree_reservations
			WHERE worktree_id = ? AND agent_id <> ? AND released_at IS NULL`, wtID, callerAgentID).Scan(&others); err != nil {
			return err
		}
		if others > 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE worktrees SET state = 'retained', retained_reason = ?
			WHERE id = ? AND state = 'active'`, removingReason, wtID); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}

func (s *Service) remove(ctx context.Context, wt Worktree) (Worktree, error) {
	dirty, err := s.DirtyStrict(ctx, wt.Path)
	if dirty {
		if err != nil {
			s.logf("worktree: status failed for %s, keeping it: %v", wt.Path, err)
		}
		return s.retain(ctx, wt, "dirty")
	}
	if wt.Branch != "" && !s.mergedOrPushed(ctx, wt) {
		return s.retain(ctx, wt, "unmerged")
	}
	repoPath, err := s.repoPath(ctx, wt.RepoID)
	if err != nil {
		return Worktree{}, err
	}
	if out, rerr := s.git(ctx, repoPath, "worktree", "remove", wt.Path); rerr != nil {
		s.logf("worktree: remove %s failed: %v: %s", wt.Path, rerr, out)
		return s.retain(ctx, wt, "remove_failed")
	}
	wt.State, wt.RetainedReason = "removed", ""
	now := s.Now()
	wt.RemovedAt = &now
	_, err = s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'removed', retained_reason = NULL,
		removed_at = ? WHERE id = ?`, db.Millis(now), wt.ID)
	return wt, err
}

// mergedOrPushed is §12.1: merged into base_ref, or pushed so the upstream matches HEAD.
func (s *Service) mergedOrPushed(ctx context.Context, wt Worktree) bool {
	if _, err := s.git(ctx, wt.Path, "merge-base", "--is-ancestor", "HEAD", wt.BaseRef); err == nil {
		return true
	}
	up, err := s.git(ctx, wt.Path, "rev-parse", "@{u}")
	if err != nil {
		return false
	}
	head, err := s.git(ctx, wt.Path, "rev-parse", "HEAD")
	return err == nil && strings.TrimSpace(string(up)) == strings.TrimSpace(string(head))
}

// Sweep is §12.2: it runs Remove's rules on every active worktree of one root,
// ignoring reservations, because the caller has checked the whole tree is finished.
func (s *Service) Sweep(ctx context.Context, rootItemID string) ([]Worktree, error) {
	wts, err := s.query(ctx, `WHERE root_item_id = ? AND state = 'active'`, rootItemID)
	if err != nil {
		return nil, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ?
		WHERE released_at IS NULL AND worktree_id IN (SELECT id FROM worktrees WHERE root_item_id = ?)`,
		db.Millis(s.Now()), rootItemID); err != nil {
		return nil, err
	}
	var out []Worktree
	for _, wt := range wts {
		done, err := s.remove(ctx, wt)
		if err != nil {
			return out, err
		}
		out = append(out, done)
	}
	return out, nil
}

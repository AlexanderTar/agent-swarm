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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/events"
	"github.com/AlexanderTar/swarmrun/internal/execx"
	"github.com/AlexanderTar/swarmrun/internal/ids"
)

// Worktree is one row of the worktrees table.
type Worktree struct {
	ID, RepoID, Path, Branch, DetachedSHA, BaseRef, BaseSHA string
	OwnerAgentID, RootItemID, State, RetainedReason         string
	CreatedAt                                               time.Time
	RemovedAt                                               *time.Time
	// RemoveError is the git output of a failed remove, for the caller's report.
	// Not persisted: retained_reason stays the documented enum.
	RemoveError string
}

// Service runs the worktree operations against one database.
type Service struct {
	DB   *db.DB
	Run  execx.Runner
	Now  func() time.Time
	Log  func(format string, args ...any)
	Home string // $SWARM_HOME; worktrees live under Home/worktrees (§12.1)
	// OnRetained fires inside the same transaction that marks a worktree
	// retained, so a caller can e.g. raise a notification atomically.
	OnRetained func(ctx context.Context, tx *sql.Tx, wt Worktree) error
	// Evidence adds merged-elsewhere checks (merged PRs); nil = base rules only.
	Evidence MergeEvidence
	// Events, when set, records worktree.discarded; nil = no event.
	Events *events.Store
	pass   passState
}

// worktreesDir is where every worktree lives, regardless of where its repo
// sits on disk (§12.1) — see PathFor.
func (s *Service) worktreesDir() string { return filepath.Join(s.Home, "worktrees") }

// wtLocks holds the per-worktree mutex that serializes Remove/Sweep against
// Share (C4). It closes the race between the "no other reservation" guard and
// the eventual delete without any persisted claim state: L12 already
// guarantees a single daemon process is the only writer, so an in-process
// lock is a complete exclusion mechanism, and — unlike a persisted "claiming"
// state — a crash mid-removal leaves the row exactly as it was (no partial
// state to reconcile on restart).
//
// Package-level, not a Service field, on purpose: a lock scoped to one
// *Service only serializes callers that share that exact value. Two *Service
// values open on the same database (there is only one construction site
// today, but nothing stops a second) would each get their own empty lock map
// and the guard would silently stop working — a package-level map is keyed
// by worktree id across every *Service, however many exist.
var wtLocks sync.Map // worktree id -> *sync.Mutex

// lockFor returns the mutex for wtID, creating it on first use.
func lockFor(wtID string) *sync.Mutex {
	l, _ := wtLocks.LoadOrStore(wtID, &sync.Mutex{})
	return l.(*sync.Mutex)
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

// PathFor is §12.1: "<worktreesDir>/<repo name>--<slug>", where the slug is
// the branch with its type prefix removed. Every worktree lives under
// worktreesDir regardless of where repoPath sits on disk, so a user with
// repos scattered across many parent folders never gets any one of them
// congested with worktree siblings. A collision gets -2, -3, …
func PathFor(worktreesDir, repoPath, branch string, exists func(string) bool) string {
	_, tail, ok := strings.Cut(branch, "/")
	if !ok {
		tail = branch
	}
	slug, err := ids.Kebab(tail)
	if err != nil {
		slug = "work"
	}
	return pathWithSuffix(worktreesDir, repoPath, slug, exists)
}

// pathWithSuffix is PathFor and Service.Review's shared base+collision-loop
// logic: "<worktreesDir>/<repo name>--<suffix>", suffixed -2, -3, … on collision.
func pathWithSuffix(worktreesDir, repoPath, suffix string, exists func(string) bool) string {
	base := filepath.Join(worktreesDir, filepath.Base(repoPath)+"--"+suffix)
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

// pathTaken is true when path exists on disk or any worktrees row, in any
// state, holds it: a removed row keeps its path and worktrees.path is UNIQUE.
func (s *Service) pathTaken(ctx context.Context) func(string) bool {
	return func(path string) bool {
		if fileExists(path) {
			return true
		}
		var n int
		// on a query error, treat the path as free and let the insert surface it
		_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktrees WHERE path = ?`, path).Scan(&n)
		return n > 0
	}
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
	out, err := s.git(ctx, path, "--no-optional-locks", "status", "--porcelain")
	if err != nil {
		return true, err
	}
	return !onlyCookies(string(out)), nil
}

// cookiePrefix starts the files watchman drops into a watched folder to sync
// with the filesystem. They are never work, and they keep appearing while git
// deletes the folder.
const cookiePrefix = ".watchman-cookie-"

// onlyCookies is true when porcelain output lists nothing but watchman cookies.
func onlyCookies(porcelain string) bool {
	for _, line := range strings.Split(porcelain, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 4 || line[:3] != "?? " || !strings.HasPrefix(line[3:], cookiePrefix) {
			return false
		}
	}
	return true
}

// clearWatchman stops watchman watching path and deletes the cookies it left
// there. Both are best effort: watchman may not be installed or running.
func (s *Service) clearWatchman(ctx context.Context, path string) {
	_, _ = s.Run(ctx, "watchman", "--no-spawn", "watch-del", path)
	cookies, _ := filepath.Glob(filepath.Join(path, cookiePrefix+"*"))
	for _, c := range cookies {
		_ = os.Remove(c)
	}
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
	name, err := s.defaultName(ctx, repoPath)
	if err != nil {
		return "", err
	}
	if _, err := s.git(ctx, repoPath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name); err == nil {
		return "origin/" + name, nil
	}
	return name, nil
}

// defaultName is the repo's default branch name: the recorded one, else the
// branch HEAD points at.
func (s *Service) defaultName(ctx context.Context, repoPath string) (string, error) {
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
// same transaction -- but only on a genuine (state, reason) transition. At a
// 10-minute reclaim cadence the 30s notify dedup window (notify.go:33) never
// suppresses a repeat, so an unchanged dirty worktree would otherwise raise
// "Worktree kept" once per pass forever.
func (s *Service) retain(ctx context.Context, wt Worktree, reason string) (Worktree, error) {
	changed := wt.State != "retained" || wt.RetainedReason != reason
	wt.State, wt.RetainedReason = "retained", reason
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE worktrees SET state = 'retained', retained_reason = ? WHERE id = ?`, reason, wt.ID); err != nil {
			return err
		}
		if s.OnRetained != nil && changed {
			return s.OnRetained(ctx, tx, wt)
		}
		return nil
	})
	return wt, err
}

// markRemoved closes the row without touching git or the filesystem: the
// path is already gone, so there is nothing left to remove.
//
// ponytail: it does not run `git worktree prune`. Measured today,
// `git worktree list --porcelain | grep -c '^prunable'` is 0 in both repos,
// because the agents doing raw removals prune after themselves. Ceiling: a
// future `rm -rf` with no prune would leave `.git/worktrees/<name>`
// registered, and since PathFor checks the filesystem and not the registry,
// the next Create for the same slug would pick the same base path and
// `git worktree add` would fail with "missing but already registered".
// Upgrade path if that is ever observed: one `git worktree prune` in the repo
// here.
func (s *Service) markRemoved(ctx context.Context, wt Worktree) (Worktree, error) {
	wt.State, wt.RetainedReason = "removed", ""
	now := s.Now()
	wt.RemovedAt = &now
	_, err := s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'removed', retained_reason = NULL,
		removed_at = ? WHERE id = ?`, db.Millis(now), wt.ID)
	return wt, err
}

// atDetachedSHA reports whether a detached worktree's HEAD is still the sha
// it was created at. A command failure reads as "moved" -- the same
// cannot-prove-it-is-safe posture DirtyStrict takes.
//
// wt.DetachedSHA may be the abbreviated sha Review was called with
// (shaPattern allows 7-40 hex chars; a review worktree's real-world path,
// "review-0d2d79d", is exactly this shape) while `git rev-parse HEAD` always
// returns the full 40-char sha, so this is a prefix match, not ==: since
// DetachedSHA is guaranteed hex by shaPattern, HasPrefix here means "HEAD is
// the commit that abbreviation named" -- the same resolution git itself did
// at `worktree add --detach`.
//
// wt.DetachedSHA is never empty when this runs: remove()'s caller only takes
// this branch when wt.Branch == "", and Create requires a non-empty Branch
// while Review validates sha against shaPattern (non-empty) before
// constructing the row -- so "both empty" (which would make HasPrefix(head,
// "") trivially true and skip this check entirely) cannot occur by
// construction. Confirmed empirically too: zero live rows have both NULL.
func (s *Service) atDetachedSHA(ctx context.Context, wt Worktree) bool {
	head, err := s.git(ctx, wt.Path, "rev-parse", "HEAD")
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(head)), wt.DetachedSHA)
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
// worktree is removed (C4) and reactivates a retained one (BUG-60).
// lockFor(wtID) makes this mutually exclusive with Remove for the same
// worktree: a Share that starts first completes (insert, then Remove's guard
// sees it and backs off); a Share that starts after a Remove has finished
// sees the worktree's final state (still 'active' if Remove backed off,
// 'retained' -- reactivated -- or 'removed' -- refused). Never an in-between state, because Remove holds the
// lock for its whole guard-check-through-delete sequence.
//
// This takes lockFor(wtID) before touching the database at all, not inside a
// transaction opened around that wait: a transaction acquires SQLite's
// single writer lock immediately (_txlock=immediate) before its closure
// runs, so a closure that then blocks on lockFor(wtID) -- held by a
// concurrent Remove/Sweep still doing its own separate plain DB calls on
// this same connection pool -- starves those calls on the held writer lock
// and deadlocks into SQLITE_BUSY. Confirmed empirically: wrapping this body
// in s.DB.Tx broke TestShareRefusesAConcurrentClaimRace and
// TestSweepRefusesAConcurrentShareDuringRemoval, both of which exercise
// exactly this Share-during-Remove race. So Share (and the swarm_worktree
// "share" MCP op that calls it) is idempotency-guarded the same way
// create/review are: PeekIdempotent before this side effect runs, a trivial
// IdemTx to record the result after -- not folded into one transaction with
// the assignment_update message.
// LockWorktrees locks the in-memory mutexes for the given worktree IDs in sorted,
// deduplicated order. The returned func unlocks them in reverse order.
func (s *Service) LockWorktrees(wtIDs ...string) func() {
	if len(wtIDs) == 0 {
		return func() {}
	}
	sorted := slices.Clone(wtIDs)
	slices.Sort(sorted)
	var unique []string
	for i, id := range sorted {
		if id == "" {
			continue
		}
		if i == 0 || id != sorted[i-1] {
			unique = append(unique, id)
		}
	}
	for _, id := range unique {
		lockFor(id).Lock()
	}
	return func() {
		for i := len(unique) - 1; i >= 0; i-- {
			lockFor(unique[i]).Unlock()
		}
	}
}

// ShareTx records agentID's reservation on the worktree inside an active transaction.
// The caller is responsible for acquiring LockWorktrees before entering the transaction.
func (s *Service) ShareTx(ctx context.Context, tx *sql.Tx, wtID, agentID, mode string) error {
	if mode != "rw" && mode != "ro" {
		return fmt.Errorf("worktree: unknown share mode %q", mode)
	}
	if err := reactivate(ctx, tx, wtID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO worktree_reservations
		(worktree_id, agent_id, mode, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(worktree_id, agent_id) DO UPDATE SET mode = excluded.mode, released_at = NULL`,
		wtID, agentID, mode, db.Millis(s.Now()))
	return err
}

func (s *Service) Share(ctx context.Context, wtID, agentID, mode string) error {
	if mode != "rw" && mode != "ro" {
		return fmt.Errorf("worktree: unknown share mode %q", mode)
	}
	lock := lockFor(wtID)
	lock.Lock()
	defer lock.Unlock()
	if err := reactivate(ctx, s.DB, wtID); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO worktree_reservations
		(worktree_id, agent_id, mode, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(worktree_id, agent_id) DO UPDATE SET mode = excluded.mode, released_at = NULL`,
		wtID, agentID, mode, db.Millis(s.Now()))
	return err
}

// reactivate is Share's state guard: an active tree is shareable, and a
// retained one (Remove kept it, dirty or unmerged) is still on disk with its
// owner, so sharing it makes it active again (BUG-60). Anything else refuses.
func reactivate(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, wtID string) error {
	var state string
	if err := q.QueryRowContext(ctx, `SELECT state FROM worktrees WHERE id = ?`, wtID).Scan(&state); err != nil {
		return err
	}
	switch state {
	case "active":
		return nil
	case "retained":
		_, err := q.ExecContext(ctx, `UPDATE worktrees SET state = 'active', retained_reason = NULL WHERE id = ?`, wtID)
		return err
	}
	return fmt.Errorf("worktree: %s is not active", wtID)
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
	path := PathFor(s.worktreesDir(), in.RepoPath, in.Branch, s.pathTaken(ctx))
	args := []string{"worktree", "add"}
	newBranch := !s.branchExists(ctx, in.RepoPath, in.Branch)
	if newBranch {
		args = append(args, "-b", in.Branch, path, base)
	} else {
		args = append(args, path, in.Branch)
	}
	if out, err := s.git(ctx, in.RepoPath, args...); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add: %w: %s", err, out)
	}
	sha, err := s.git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		s.undoAdd(in.RepoPath, path, newBranch, in.Branch)
		return Worktree{}, err
	}
	wt := Worktree{ID: ids.New("wt"), RepoID: in.RepoID, Path: path, Branch: in.Branch,
		BaseRef: base, BaseSHA: strings.TrimSpace(string(sha)),
		OwnerAgentID: in.OwnerAgentID, RootItemID: in.RootItemID, State: "active", CreatedAt: s.Now()}
	if err := s.insert(ctx, wt); err != nil {
		s.undoAdd(in.RepoPath, path, newBranch, in.Branch)
		return Worktree{}, err
	}
	s.ignoreGraphifyOut(ctx, in.RepoPath)
	return wt, nil
}

// ignoreGraphifyOut appends "graphify-out/" to <common-dir>/info/exclude
// when absent, so a built code graph never dirties any worktree of the repo.
// A failure is logged and never fails the worktree operation.
func (s *Service) ignoreGraphifyOut(ctx context.Context, repoPath string) {
	out, err := s.git(ctx, repoPath, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		s.logf("worktree: graphify exclude %s: %v", repoPath, err)
		return
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoPath, p)
	}
	if err := appendLineOnce(p, "graphify-out/"); err != nil {
		s.logf("worktree: graphify exclude %s: %v", repoPath, err)
	}
}

// appendLineOnce creates the file (and its dir) when missing and appends
// line only when no existing line matches it by trimmed equality.
func appendLineOnce(path, line string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	} else {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(l) == line {
				return nil
			}
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(line + "\n")
	return err
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
	path := pathWithSuffix(s.worktreesDir(), in.RepoPath, "review-"+sha[:7], s.pathTaken(ctx))
	if out, err := s.git(ctx, in.RepoPath, "worktree", "add", "--detach", path, sha); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add --detach: %w: %s", err, out)
	}
	wt := Worktree{ID: ids.New("wt"), RepoID: in.RepoID, Path: path, DetachedSHA: sha,
		BaseRef: sha, BaseSHA: sha, OwnerAgentID: in.OwnerAgentID, RootItemID: in.RootItemID,
		State: "active", CreatedAt: s.Now()}
	if err := s.insert(ctx, wt); err != nil {
		s.undoAdd(in.RepoPath, path, false, "")
		return Worktree{}, err
	}
	s.ignoreGraphifyOut(ctx, in.RepoPath)
	s.cloneDeps(ctx, in.RepoID, in.RepoPath, sha, path)
	return wt, nil
}

// undoAdd reverses a `git worktree add` whose row could not be recorded, so no
// untracked checkout is left on disk (and no branch Create just made). It uses
// a fresh context: ctx may be the very thing that cancelled the insert.
func (s *Service) undoAdd(repoPath, path string, newBranch bool, branch string) {
	ctx := context.Background()
	// Go-side delete: the bulk `git worktree remove --force` runs under the
	// 30 s execx cap and a SIGKILL there strands the tree (BUG-137).
	if err := s.deleteTree(ctx, path); err != nil {
		s.logf("worktree: undo add %s failed: %v", path, err)
	} else {
		s.prune(ctx, repoPath)
	}
	if newBranch {
		if out, err := s.git(ctx, repoPath, "branch", "-D", branch); err != nil {
			s.logf("worktree: undo branch %s failed: %v: %s", branch, err, out)
		}
	}
}

// Remove runs the §12.1 checks in order. Only the owner may call it, and only
// when nobody else holds an unreleased reservation (C4). lockFor(wtID) holds
// for the whole guard-check-through-delete sequence, so a concurrent Share
// cannot land in the window between the "no other reservation" read and the
// final state write — see Share and lockFor.
func (s *Service) Remove(ctx context.Context, wtID, callerAgentID string) (Worktree, error) {
	lock := lockFor(wtID)
	lock.Lock()
	defer lock.Unlock()
	wt, err := s.Get(ctx, wtID)
	if err != nil {
		return Worktree{}, err
	}
	if wt.OwnerAgentID != callerAgentID {
		return Worktree{}, s.notOwnerError(ctx, wt)
	}
	var others int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktree_reservations
		WHERE worktree_id = ? AND agent_id <> ? AND released_at IS NULL`, wtID, callerAgentID).Scan(&others); err != nil {
		return Worktree{}, err
	}
	if others > 0 {
		return Worktree{}, fmt.Errorf("worktree: %d agents still hold %s", others, wt.Path)
	}
	return s.remove(ctx, wt)
}

// WouldRemove reports whether remove would delete wt now, without changing
// anything; otherwise the retained reason it would record.
func (s *Service) WouldRemove(ctx context.Context, wt Worktree) (bool, string) {
	if !fileExists(wt.Path) {
		return true, ""
	}
	reason := s.keepReason(ctx, wt)
	return reason == "", reason
}

// stray reports a folder that is no longer a worktree: its .git is gone, or
// the repo's `git worktree list` no longer names it (a leftover .git file then
// points at a pruned admin dir). Both follow a half finished `git worktree
// remove`. cookiesOnly says it holds nothing but watchman cookies (and a
// dangling .git file), so deleting it loses no work.
func (s *Service) stray(ctx context.Context, wt Worktree) (isStray, cookiesOnly bool) {
	if _, err := os.Lstat(filepath.Join(wt.Path, ".git")); err != nil {
		isStray = true
	} else if !s.registered(ctx, wt) {
		isStray = true
	}
	if !isStray {
		return false, false
	}
	entries, err := os.ReadDir(wt.Path)
	if err != nil {
		return false, false
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), cookiePrefix) && !(e.Name() == ".git" && e.Type().IsRegular()) {
			return true, false
		}
	}
	return true, true
}

// registered is false only when the repo's worktree list is readable and does
// not name wt.Path; an unreadable list says nothing.
func (s *Service) registered(ctx context.Context, wt Worktree) bool {
	repoPath, err := s.repoPath(ctx, wt.RepoID)
	if err != nil {
		return true
	}
	out, err := s.git(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return true
	}
	want := realPath(wt.Path)
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && realPath(p) == want {
			return true
		}
	}
	return false
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// removeStray deletes a cookies-only stray folder and prunes the repo's
// registration of it.
func (s *Service) removeStray(ctx context.Context, wt Worktree) (Worktree, error) {
	if err := os.RemoveAll(wt.Path); err != nil {
		s.logf("worktree: removing stray %s failed: %v", wt.Path, err)
		return s.retain(ctx, wt, "remove_failed")
	}
	if repoPath, err := s.repoPath(ctx, wt.RepoID); err == nil {
		if _, err := s.git(ctx, repoPath, "worktree", "prune"); err != nil {
			s.logf("worktree: prune after removing %s failed: %v", wt.Path, err)
		}
	}
	return s.markRemoved(ctx, wt)
}

// deleteTree removes path without a bulk `git worktree remove` subprocess:
// os.RemoveAll has no 30 s execx cap. It never prunes; every caller prunes
// the repo right after a successful delete.
func (s *Service) deleteTree(ctx context.Context, path string) error {
	s.clearWatchman(ctx, path)
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if fileExists(path) {
		// A watchman cookie landed mid-delete; clear and retry once, the way
		// the old "Directory not empty" retry did.
		s.clearWatchman(ctx, path)
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if fileExists(path) {
			return fmt.Errorf("worktree: %s still exists after delete", path)
		}
	}
	return nil
}

// prune drops the repo's admin entry for a just-deleted worktree. It runs on
// every delete, even inside a reclaim pass: skipping it while any pass was
// open left `git worktree list` naming the path, so a later add on the same
// path or branch failed with "already registered" (BUG-137 fix round). The
// pass loop's own once-per-repo prune stays as a second sweep.
func (s *Service) prune(ctx context.Context, repoPath string) {
	if _, err := s.git(ctx, repoPath, "worktree", "prune"); err != nil {
		s.logf("worktree: prune after removing failed: %v", err)
	}
}

// keepReason is remove's dirty and merged checks: "" means safe to delete.
func (s *Service) keepReason(ctx context.Context, wt Worktree) string {
	if isStray, cookiesOnly := s.stray(ctx, wt); isStray {
		if cookiesOnly && s.underWorktrees(wt.Path) {
			return ""
		}
		return "not_a_worktree"
	}
	if s.discardable(ctx, wt) {
		return ""
	}
	dirty, err := s.DirtyStrict(ctx, wt.Path)
	if dirty {
		if err != nil {
			s.logf("worktree: status failed for %s, keeping it: %v", wt.Path, err)
		}
		return "dirty"
	}
	// A detached worktree whose HEAD has moved off detached_sha holds commits
	// reachable from no ref anywhere else. Branch == "" alone would skip
	// mergedOrPushed entirely and delete those commits with the worktree.
	if wt.Branch == "" && !s.atDetachedSHA(ctx, wt) && !s.mergedOnlyElsewhere(ctx, wt) {
		return "unmerged"
	}
	if wt.Branch != "" && !s.mergedOrPushed(ctx, wt) {
		return "unmerged"
	}
	return ""
}

// discardable is true when the tree holds no commit of its own and the dirt
// is safe to throw away: a review tree still at its detached_sha, or build
// churn (only modifications of tracked files, owner already finished).
func (s *Service) discardable(ctx context.Context, wt Worktree) bool {
	if wt.Branch == "" && wt.DetachedSHA != "" && s.atDetachedSHA(ctx, wt) {
		return true
	}
	return s.buildChurn(ctx, wt)
}

// buildChurn is a branch tree whose owner has finished and whose root is
// done or cancelled, with no commit beyond the remote default branch (or
// the local one when there is no remote) and whose porcelain is only
// modifications of tracked files. An in-progress root stays retained: a
// finished owner can be retried, and those edits are still its work. An
// untracked path, a deletion, or a commit of the tree's own keeps it too.
func (s *Service) buildChurn(ctx context.Context, wt Worktree) bool {
	if wt.Branch == "" || !s.ownerFinished(ctx, wt.OwnerAgentID) || !s.rootFinished(ctx, wt.RootItemID) {
		return false
	}
	repoPath, err := s.repoPath(ctx, wt.RepoID)
	if err != nil {
		return false
	}
	name, err := s.defaultName(ctx, repoPath)
	if err != nil {
		return false
	}
	ref := "refs/heads/" + name
	if _, err := s.git(ctx, wt.Path, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name); err == nil {
		ref = "refs/remotes/origin/" + name
	}
	head, ok := s.headSHA(ctx, wt)
	if !ok || !s.isAncestor(ctx, wt.Path, head, ref) {
		return false
	}
	return s.onlyTrackedModifications(ctx, wt.Path)
}

func (s *Service) ownerFinished(ctx context.Context, agentID string) bool {
	var state string
	err := s.DB.QueryRowContext(ctx, `SELECT state FROM agents WHERE id = ?`, agentID).Scan(&state)
	return err == nil && (state == "finished" || state == "acknowledged")
}

func (s *Service) rootFinished(ctx context.Context, rootItemID string) bool {
	var status string
	err := s.DB.QueryRowContext(ctx, `SELECT status FROM items WHERE id = ?`, rootItemID).Scan(&status)
	return err == nil && (status == "done" || status == "cancelled")
}

// onlyTrackedModifications is true when status --porcelain lists at least one
// line and every line is a modification of a tracked file (M), not an add,
// deletion, rename, or untracked path.
func (s *Service) onlyTrackedModifications(ctx context.Context, path string) bool {
	out, err := s.git(ctx, path, "--no-optional-locks", "status", "--porcelain")
	if err != nil {
		return false
	}
	// Do not TrimSpace: a worktree modification is " M path", and the leading
	// space is the index column.
	saw := false
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "?? "+cookiePrefix) {
			continue
		}
		saw = true
		if !trackedModification(line) {
			return false
		}
	}
	return saw
}

func trackedModification(line string) bool {
	if len(line) < 4 || (line[2] != ' ' && line[2] != '\t') {
		return false
	}
	x, y := line[0], line[1]
	mod := func(c byte) bool { return c == ' ' || c == 'M' }
	return mod(x) && mod(y) && (x == 'M' || y == 'M')
}

func (s *Service) remove(ctx context.Context, wt Worktree) (Worktree, error) {
	// A path that is already gone is 'removed', not 'dirty': without this,
	// DirtyStrict's git call fails, dirty comes back true, and a vanished
	// worktree raises a spurious "Worktree kept" notification (§2.6). Runs
	// before any git call.
	if !fileExists(wt.Path) {
		return s.markRemoved(ctx, wt)
	}

	if reason := s.keepReason(ctx, wt); reason != "" {
		// A row stranded retained/remove_failed by a SIGKILLed bulk delete
		// reads as dirty (deletions), but the deletions ARE the delete: when
		// porcelain lists only tracked deletions plus watchman cookies and
		// the branch is otherwise merged, retry the delete instead of keeping
		// the tree forever. When the kill landed after the `.git` file was
		// already deleted, keepReason can only say not_a_worktree: a
		// remove_failed row under the worktrees dir whose branch is merged
		// from the repo side retries too. Real uncommitted work still retains.
		if !s.retryInterruptedDelete(ctx, wt, reason) {
			// A failed re-judge keeps the remove_failed marker while the
			// folder still looks like an interrupted delete, so a later pass
			// retries once the branch lands; otherwise the row would stick
			// as dirty/not_a_worktree and never be retried.
			if wt.RetainedReason == "remove_failed" && s.looksInterrupted(ctx, wt) {
				return s.retain(ctx, wt, "remove_failed")
			}
			return s.retain(ctx, wt, reason)
		}
	}
	if isStray, _ := s.stray(ctx, wt); isStray {
		return s.removeStray(ctx, wt)
	}
	repoPath, err := s.repoPath(ctx, wt.RepoID)
	if err != nil {
		return Worktree{}, err
	}
	if derr := s.deleteTree(ctx, wt.Path); derr != nil {
		s.logf("worktree: remove %s failed: %v", wt.Path, derr)
		wt, err := s.retain(ctx, wt, "remove_failed")
		wt.RemoveError = strings.TrimSpace(derr.Error())
		return wt, err
	}
	s.prune(ctx, repoPath)
	wt.State, wt.RetainedReason = "removed", ""
	now := s.Now()
	wt.RemovedAt = &now
	_, err = s.DB.ExecContext(ctx, `UPDATE worktrees SET state = 'removed', retained_reason = NULL,
		removed_at = ? WHERE id = ?`, db.Millis(now), wt.ID)
	return wt, err
}

// retryInterruptedDelete reports whether a retained/remove_failed row reads
// as a half-finished bulk delete worth retrying: porcelain shows only tracked
// deletions of a merged tree, or the `.git` file is already gone from a tree
// under the worktrees dir whose branch is merged from the repo side.
// Anything else is real work (or an unknown folder) and stays retained.
func (s *Service) retryInterruptedDelete(ctx context.Context, wt Worktree, reason string) bool {
	if wt.RetainedReason != "remove_failed" {
		return false
	}
	if reason == "dirty" {
		return s.isInterruptedDelete(ctx, wt)
	}
	if reason == "not_a_worktree" {
		return s.interruptedWithoutGit(ctx, wt)
	}
	return false
}

// looksInterrupted reports whether the folder still has the shape of a
// half-finished bulk delete: only tracked deletions with `.git` present, or
// no `.git` under the worktrees dir with the branch merged judged from the
// repo. Anything else (a modification, add, untracked path, or an unmerged
// branch with `.git` present) is real work and returns false, so the normal
// dirty path still retains under its own reason.
func (s *Service) looksInterrupted(ctx context.Context, wt Worktree) bool {
	if _, err := os.Lstat(filepath.Join(wt.Path, ".git")); err == nil {
		return s.onlyDeletions(ctx, wt.Path)
	}
	return s.underWorktrees(wt.Path) && s.branchMergedFromRepo(ctx, wt)
}

// interruptedWithoutGit is the no-`.git` half of an interrupted delete: the
// kill (or a partially failed delete) removed the `.git` file early, so git
// can no longer run inside the tree. Every delete prunes the repo's admin
// entry, so a real stranded tree is never registered anymore: the gate is the
// worktrees dir plus the branch merged judged from the repo.
func (s *Service) interruptedWithoutGit(ctx context.Context, wt Worktree) bool {
	if _, err := os.Lstat(filepath.Join(wt.Path, ".git")); err == nil {
		return false
	}
	if !s.underWorktrees(wt.Path) {
		return false
	}
	return s.branchMergedFromRepo(ctx, wt)
}

// branchMergedFromRepo judges wt's branch merged without running git inside
// the tree (its `.git` is gone): merged into the base, or vouched for by
// merged evidence. A squash-merged branch with no evidence row stays
// retained; see docs/worktree-cleanup.md.
func (s *Service) branchMergedFromRepo(ctx context.Context, wt Worktree) bool {
	repoPath, err := s.repoPath(ctx, wt.RepoID)
	if err != nil {
		return false
	}
	head := wt.Branch
	if head == "" {
		head = wt.DetachedSHA
	}
	if head == "" {
		return false
	}
	if _, err := s.git(ctx, repoPath, "merge-base", "--is-ancestor", head, wt.BaseRef); err == nil {
		return true
	}
	out, err := s.git(ctx, repoPath, "rev-parse", "--verify", "--quiet", head)
	if err != nil {
		return false
	}
	return s.Evidence != nil && s.Evidence.Merged(ctx, wt, repoPath, strings.TrimSpace(string(out)))
}

// isInterruptedDelete reports whether wt looks stranded halfway through a
// bulk delete: porcelain lists only tracked deletions (plus watchman
// cookies, which are never dirt) and the branch is otherwise merged. A
// modification, add, rename, untracked path, or unmerged branch reads as
// real work and returns false, so the normal dirty path still retains.
func (s *Service) isInterruptedDelete(ctx context.Context, wt Worktree) bool {
	if !s.onlyDeletions(ctx, wt.Path) {
		return false
	}
	return s.mergedForRemove(ctx, wt)
}

// onlyDeletions is true when status --porcelain lists at least one tracked
// deletion and nothing else except watchman cookies.
func (s *Service) onlyDeletions(ctx context.Context, path string) bool {
	out, err := s.git(ctx, path, "--no-optional-locks", "status", "--porcelain")
	if err != nil {
		return false
	}
	saw := false
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "?? "+cookiePrefix) {
			continue
		}
		saw = true
		if !isDeletion(line) {
			return false
		}
	}
	return saw
}

// isDeletion is a porcelain line deleting a tracked file with no other
// change staged or unstaged: " D path" or "D  path". Mixed (MD), both-deleted
// (DD), renames, and unmerged entries are not deletions.
func isDeletion(line string) bool {
	if len(line) < 4 || (line[2] != ' ' && line[2] != '\t') {
		return false
	}
	x, y := line[0], line[1]
	return (x == 'D' && y == ' ') || (x == ' ' && y == 'D')
}

// mergedForRemove mirrors keepReason's merged half: discardable review trees
// are already removable, so this covers branch trees (merged or pushed) and
// detached trees (still at sha or merged elsewhere).
func (s *Service) mergedForRemove(ctx context.Context, wt Worktree) bool {
	if wt.Branch == "" {
		return s.atDetachedSHA(ctx, wt) || s.mergedOnlyElsewhere(ctx, wt)
	}
	return s.mergedOrPushed(ctx, wt)
}

// mergedOnlyElsewhere is merged evidence 2-4 for the tree's current HEAD.
func (s *Service) mergedOnlyElsewhere(ctx context.Context, wt Worktree) bool {
	head, ok := s.headSHA(ctx, wt)
	return ok && s.mergedElsewhere(ctx, wt, head)
}

// mergedOrPushed is §12.1: merged into base_ref, or pushed so the upstream matches HEAD.
func (s *Service) mergedOrPushed(ctx context.Context, wt Worktree) bool {
	if _, err := s.git(ctx, wt.Path, "merge-base", "--is-ancestor", "HEAD", wt.BaseRef); err == nil {
		return true
	}
	if s.mergedOnlyElsewhere(ctx, wt) {
		return true
	}
	up, err := s.git(ctx, wt.Path, "rev-parse", "@{u}")
	if err != nil {
		return false
	}
	head, err := s.git(ctx, wt.Path, "rev-parse", "HEAD")
	return err == nil && strings.TrimSpace(string(up)) == strings.TrimSpace(string(head))
}

// ReclaimOne applies Remove's rules to one worktree the caller has already
// decided is eligible. It is Sweep's per-worktree body, exported: the same
// lockFor(wt.ID) window and the same call into remove, with no eligibility
// opinion of its own.
//
// It exists because Sweep's *own* eligibility rule — a whole root item
// finished — cannot fire during a multi-week epic, and runtime needs a way to
// act on one worktree at a time.
func (s *Service) ReclaimOne(ctx context.Context, wt Worktree) (Worktree, error) {
	lock := lockFor(wt.ID)
	lock.Lock()
	defer lock.Unlock()
	return s.remove(ctx, wt)
}

// Candidates returns the worktrees matching an eligibility clause the caller
// supplies, so runtime can express a gate over agents and sessions without
// this package taking a view on either. It is the existing unexported
// query(), exported.
func (s *Service) Candidates(ctx context.Context, where string, args ...any) ([]Worktree, error) {
	return s.query(ctx, where, args...)
}

// Sweep is §12.2: it runs Remove's rules on every active worktree of one
// root, ignoring reservations, because the caller has checked the whole
// tree is finished. Retained rows are not selected. Sweep runs from
// Reconcile every few seconds and outside BeginPass, so re-checking a
// still-unmerged retained tree would fetch on every tick. The reclaim pass
// (reclaimGateWhere) already selects retained rows, inside one BeginPass.
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
		done, err := s.ReclaimOne(ctx, wt)
		if err != nil {
			return out, err
		}
		out = append(out, done)
	}
	return out, nil
}

// Listed is a worktree row with the root item key and owner agent name
// joined in, for read-side listings.
type Listed struct {
	Worktree
	RootKey, OwnerName string
}

// List returns worktrees newest-first (id tie-break). An empty state means
// every live row (active and retained); removed rows are listed only when
// asked for by state. rootKey narrows to one root item.
func (s *Service) List(ctx context.Context, rootKey, state string) ([]Listed, error) {
	q := `SELECT w.id, w.repo_id, w.path, w.branch, w.detached_sha, w.base_ref, w.base_sha,
		w.owner_agent_id, w.root_item_id, w.state, w.retained_reason, w.created_at, w.removed_at,
		i.key, a.name
		FROM worktrees w JOIN items i ON i.id = w.root_item_id JOIN agents a ON a.id = w.owner_agent_id
		WHERE 1 = 1`
	var args []any
	if state == "" {
		q += ` AND w.state != 'removed'`
	} else {
		q += ` AND w.state = ?`
		args = append(args, state)
	}
	if rootKey != "" {
		q += ` AND i.key = ?`
		args = append(args, rootKey)
	}
	q += ` ORDER BY w.created_at DESC, w.id`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Listed
	for rows.Next() {
		var l Listed
		var branch, detached, reason sql.NullString
		var created int64
		var removed sql.NullInt64
		w := &l.Worktree
		if err := rows.Scan(&w.ID, &w.RepoID, &w.Path, &branch, &detached, &w.BaseRef, &w.BaseSHA,
			&w.OwnerAgentID, &w.RootItemID, &w.State, &reason, &created, &removed, &l.RootKey, &l.OwnerName); err != nil {
			return nil, err
		}
		w.Branch, w.DetachedSHA, w.RetainedReason = branch.String, detached.String, reason.String
		w.CreatedAt = db.FromMillis(created)
		if removed.Valid {
			t := db.FromMillis(removed.Int64)
			w.RemovedAt = &t
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Discard force-removes a worktree, by id or path, that the user has decided
// to drop: only when its owner is not live and no live agent holds a
// reservation. The root item's status does not matter. It never deletes the
// branch, so the commits stay recoverable, and returns how many commits the
// tree holds that are not on its base (-1 when that cannot be counted). A
// path under the worktrees directory with no row is removed the same way
// when some repo lists it as a worktree.
func (s *Service) Discard(ctx context.Context, ref string) (Worktree, int, error) {
	// A trailing slash, "//", or "/./" must still hit the row. Clean does not
	// resolve symlinks; discardUntracked refuses those so they cannot skip
	// the live-owner and reservation gates.
	ref = filepath.Clean(ref)
	wts, err := s.query(ctx, `WHERE id = ? OR path = ?`, ref, ref)
	if err != nil {
		return Worktree{}, 0, err
	}
	if len(wts) == 0 {
		return s.discardUntracked(ctx, ref)
	}
	lock := lockFor(wts[0].ID)
	lock.Lock()
	defer lock.Unlock()
	wt, err := s.Get(ctx, wts[0].ID)
	if err != nil {
		return Worktree{}, 0, err
	}
	if wt.State == "removed" {
		return wt, 0, fmt.Errorf("worktree %s is already removed", wt.Path)
	}
	var rootKey string
	if err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, wt.RootItemID).Scan(&rootKey); err != nil {
		return Worktree{}, 0, err
	}
	var ownerName, ownerState string
	if err := s.DB.QueryRowContext(ctx, `SELECT name, state FROM agents WHERE id = ?`, wt.OwnerAgentID).Scan(&ownerName, &ownerState); err != nil {
		return Worktree{}, 0, err
	}
	sessionLive, err := s.ownerSessionLive(ctx, wt.OwnerAgentID)
	if err != nil {
		return Worktree{}, 0, err
	}
	if ownerState == "queued" || ownerState == "active" || sessionLive {
		return Worktree{}, 0, fmt.Errorf("worktree %s is held by live agent %s", wt.Path, ownerName)
	}
	var holder string
	err = s.DB.QueryRowContext(ctx, `SELECT a.name FROM worktree_reservations r JOIN agents a ON a.id = r.agent_id
		WHERE r.worktree_id = ? AND r.released_at IS NULL AND a.state IN ('queued','active') LIMIT 1`, wt.ID).Scan(&holder)
	if err == nil {
		return Worktree{}, 0, fmt.Errorf("worktree %s is held by live agent %s", wt.Path, holder)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Worktree{}, 0, err
	}
	repoPath, err := s.repoPath(ctx, wt.RepoID)
	if err != nil {
		return Worktree{}, 0, err
	}
	head := wt.Branch
	if head == "" {
		head = wt.DetachedSHA
	}
	unlanded := -1
	if out, cerr := s.git(ctx, repoPath, "rev-list", "--count", wt.BaseRef+".."+head); cerr == nil {
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &unlanded)
	}
	if isStray, _ := s.stray(ctx, wt); isStray && s.underWorktrees(wt.Path) {
		// Not a worktree any more, so git has nothing to remove.
		if err := os.RemoveAll(wt.Path); err != nil {
			return Worktree{}, 0, err
		}
		_, _ = s.git(ctx, repoPath, "worktree", "prune")
	} else if fileExists(wt.Path) {
		// Go-side delete: the bulk `git worktree remove --force` runs under
		// the 30 s execx cap and a SIGKILL there strands the tree (BUG-137).
		if derr := s.deleteTree(ctx, wt.Path); derr != nil {
			return Worktree{}, 0, fmt.Errorf("worktree: remove %s failed: %w", wt.Path, derr)
		}
		s.prune(ctx, repoPath)
	}
	now := s.Now()
	wt.State, wt.RetainedReason, wt.RemovedAt = "removed", "", &now
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE worktrees SET state = 'removed', retained_reason = NULL,
			removed_at = ? WHERE id = ?`, db.Millis(now), wt.ID); err != nil {
			return err
		}
		if s.Events == nil {
			return nil
		}
		_, err := s.Events.Append(ctx, tx, events.WorktreeDiscarded, map[string]any{
			"worktree_id": wt.ID, "path": wt.Path, "branch": wt.Branch, "root": rootKey, "unlanded": unlanded})
		return err
	})
	if err == nil && s.Events != nil {
		s.Events.Notify()
	}
	return wt, unlanded, err
}

// discardUntracked removes a directory under the worktrees dir that has no
// row, with a Go-side delete in the repo whose worktree list names it (no
// capped `git worktree remove --force`).
// The branch is kept. Anything else is an unknown ref.
func (s *Service) discardUntracked(ctx context.Context, ref string) (Worktree, int, error) {
	unknown := fmt.Errorf("Unknown worktree %s. Pass the worktree id or path.", ref)
	if !s.underWorktrees(ref) || !fileExists(ref) {
		return Worktree{}, 0, unknown
	}
	if recorded, ok, err := s.nonRemovedPathResolvingTo(ctx, ref); err != nil {
		return Worktree{}, 0, err
	} else if ok {
		return Worktree{}, 0, fmt.Errorf("worktree %s is recorded as %s; pass that path or its id", ref, recorded)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT path FROM repos`)
	if err != nil {
		return Worktree{}, 0, err
	}
	defer rows.Close()
	var repoPath string
	for rows.Next() {
		var p string
		if rows.Scan(&p) != nil {
			continue
		}
		out, err := s.git(ctx, p, "worktree", "list", "--porcelain")
		if err != nil || !worktreeListed(string(out), ref) {
			continue
		}
		repoPath = p
		break
	}
	if err := rows.Err(); err != nil {
		return Worktree{}, 0, err
	}
	if repoPath == "" {
		return Worktree{}, 0, unknown
	}
	branch := ""
	if out, err := s.git(ctx, ref, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch = strings.TrimSpace(string(out))
		if branch == "HEAD" {
			branch = ""
		}
	}
	unlanded := -1
	if name, err := s.defaultName(ctx, repoPath); err == nil {
		base := name
		if _, err := s.git(ctx, ref, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+name); err == nil {
			base = "origin/" + name
		}
		if out, cerr := s.git(ctx, ref, "rev-list", "--count", base+"..HEAD"); cerr == nil {
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &unlanded)
		}
	}
	// Go-side delete: the bulk `git worktree remove --force` runs under the
	// 30 s execx cap and a SIGKILL there strands the tree (BUG-137).
	if derr := s.deleteTree(ctx, ref); derr != nil {
		return Worktree{}, 0, fmt.Errorf("worktree: remove %s failed: %w", ref, derr)
	}
	s.prune(ctx, repoPath)
	now := s.Now()
	return Worktree{Path: ref, Branch: branch, State: "removed", RemovedAt: &now}, unlanded, nil
}

// ownerSessionLive is the same session list reclaimGateWhere uses: a finished
// agent whose session is still spawning, running, pause_requested, quiescing,
// or stopping is a live owner.
func (s *Service) ownerSessionLive(ctx context.Context, agentID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent_id = ?
		AND state IN ('spawning','running','pause_requested','quiescing','stopping')`, agentID).Scan(&n)
	return n > 0, err
}

// nonRemovedPathResolvingTo reports a non-removed worktree whose directory is
// ref once symlinks are resolved. filepath.Clean does not do that, so a
// symlink spelling would otherwise miss the row and be force-removed.
func (s *Service) nonRemovedPathResolvingTo(ctx context.Context, ref string) (string, bool, error) {
	want, err := filepath.EvalSymlinks(ref)
	if err != nil {
		want = filepath.Clean(ref)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT path FROM worktrees WHERE state <> 'removed'`)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return "", false, err
		}
		got, err := filepath.EvalSymlinks(p)
		if err != nil {
			got = filepath.Clean(p)
		}
		if got == want {
			return p, true, nil
		}
	}
	return "", false, rows.Err()
}

func (s *Service) underWorktrees(path string) bool {
	rel, err := filepath.Rel(s.worktreesDir(), path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func worktreeListed(porcelain, path string) bool {
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		want = path
	}
	for _, line := range strings.Split(porcelain, "\n") {
		got, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if got == path {
			return true
		}
		if resolved, err := filepath.EvalSymlinks(got); err == nil && resolved == want {
			return true
		}
	}
	return false
}

// notOwnerError explains a non-owner remove: the tree's state and reason, and
// the user command that can drop it when no live agent holds it.
func (s *Service) notOwnerError(ctx context.Context, wt Worktree) error {
	state := wt.State
	if wt.RetainedReason != "" {
		state += ", " + wt.RetainedReason
	}
	msg := fmt.Sprintf("worktree: only the owner can remove %s (%s)", wt.Path, state)
	var rootKey, status string
	if s.DB.QueryRowContext(ctx, `SELECT key, status FROM items WHERE id = ?`, wt.RootItemID).Scan(&rootKey, &status) == nil {
		shown := status
		if status != "done" && status != "cancelled" {
			shown = strings.ReplaceAll(status, "_", " ")
		}
		msg += fmt.Sprintf("; its root %s is %s", rootKey, shown)
	}
	msg += fmt.Sprintf("; run `swarm cleanup --discard %s` to drop it when no live agent holds it (the branch is kept)", wt.Path)
	return errors.New(msg)
}

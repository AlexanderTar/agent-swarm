package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// FinishPR is one PR a finishing checkpoint reports.
type FinishPR struct {
	Repo string `json:"repo"` // repo name, as in the integrated checkpoint's git
	URL  string `json:"url"`  // https://github.com/<owner>/<name>/pull/<n>
}

// FinishMerged is one local merge a finishing checkpoint reports.
type FinishMerged struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"` // the default branch commit that contains the integrated sha
}

// ItemMerge is one item_merges row of a root's newest integrated checkpoint.
type ItemMerge struct {
	Repo      string `json:"repo"`
	Kind      string `json:"kind"` // "pr" | "local" | "kept"
	URL       string `json:"url,omitempty"`
	Number    int    `json:"number,omitempty"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	AutoMerge bool   `json:"auto_merge"`
	State     string `json:"state"`  // "open" | "merged" | "closed"
	Checks    string `json:"checks"` // "" | "pending" | "passing" | "failing"
	MergedSHA string `json:"merged_sha,omitempty"`
	Note      string `json:"note,omitempty"` // kept only: what the orchestrator did instead
}

type MergeProgress struct {
	Merged int `json:"merged"`
	Total  int `json:"total"` // distinct repos in the integrated checkpoint's git
}

// finishRepo is one integrated repo resolved against the catalog.
type finishRepo struct {
	Ref                                  GitRef
	RepoID, Path, RemoteURL, Owner, Base string
	GitHub                               bool
}

// ghPR is the subset of `gh pr view --json <prFields>` the daemon reads.
type ghPR struct {
	State            string    `json:"state"` // OPEN | CLOSED | MERGED
	HeadRefName      string    `json:"headRefName"`
	BaseRefName      string    `json:"baseRefName"`
	Number           int       `json:"number"`
	URL              string    `json:"url"`
	AutoMergeRequest *struct{} `json:"autoMergeRequest"`
	MergeCommit      *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	StatusCheckRollup []struct {
		Name       string `json:"name"`       // CheckRun
		Status     string `json:"status"`     // CheckRun: QUEUED | IN_PROGRESS | COMPLETED | ...
		Conclusion string `json:"conclusion"` // CheckRun: SUCCESS | FAILURE | ...
		Context    string `json:"context"`    // StatusContext
		State      string `json:"state"`      // StatusContext: SUCCESS | FAILURE | ERROR | PENDING | EXPECTED
	} `json:"statusCheckRollup"`
}

const prFields = "state,headRefName,baseRefName,number,url,autoMergeRequest,mergeCommit,statusCheckRollup"

var (
	prURLRe      = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/pull/(\d+)$`)
	exitStatusRe = regexp.MustCompile(`exit status \d+: `)
	hexSHA       = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)
)

func isGitHubRemote(url string) bool { return strings.Contains(url, "github.com") }

func (s *Store) runner() execx.Runner {
	if s.Exec == nil {
		return execx.Run
	}
	return s.Exec
}

// finishReposTx resolves each distinct ref repo, in first-seen order; an unknown repo has RepoID "".
func (s *Store) finishReposTx(ctx context.Context, q txQuerier, rootItemID string, refs []GitRef) ([]finishRepo, error) {
	var out []finishRepo
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.Repo] {
			continue
		}
		seen[ref.Repo] = true
		r := finishRepo{Ref: ref}
		cols := `SELECT id, path, COALESCE(remote_url,''), COALESCE(remote_owner,''), COALESCE(default_branch,'') FROM repos `
		err := q.QueryRowContext(ctx, cols+`WHERE name = ? AND id IN (SELECT value FROM json_each(
			(SELECT confirmed_repos_json FROM items WHERE id = ?)))`, ref.Repo, rootItemID).
			Scan(&r.RepoID, &r.Path, &r.RemoteURL, &r.Owner, &r.Base)
		if errors.Is(err, sql.ErrNoRows) {
			err = q.QueryRowContext(ctx, cols+`WHERE name = ? ORDER BY last_used_at DESC LIMIT 1`, ref.Repo).
				Scan(&r.RepoID, &r.Path, &r.RemoteURL, &r.Owner, &r.Base)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		r.GitHub = isGitHubRemote(r.RemoteURL)
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) ghPRView(ctx context.Context, url string) (ghPR, error) {
	var p ghPR
	out, err := s.runner()(ctx, "gh", "pr", "view", url, "--json", prFields)
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(out, &p)
}

// rollupChecks folds a PR's status rollup to "" | pending | passing | failing, plus the failing names.
func rollupChecks(p ghPR) (string, []string) {
	if len(p.StatusCheckRollup) == 0 {
		return "", nil
	}
	bad := map[string]bool{"FAILURE": true, "ERROR": true, "TIMED_OUT": true, "CANCELLED": true,
		"ACTION_REQUIRED": true, "STARTUP_FAILURE": true}
	var failing []string
	pending := false
	for _, c := range p.StatusCheckRollup {
		if bad[c.Conclusion] || bad[c.State] {
			name := c.Name
			if name == "" {
				name = c.Context
			}
			failing = append(failing, name)
			continue
		}
		if (c.Name != "" || c.Status != "") && c.Status != "COMPLETED" {
			pending = true
		}
		if c.Context != "" && (c.State == "PENDING" || c.State == "EXPECTED") {
			pending = true
		}
	}
	switch {
	case len(failing) > 0:
		return "failing", failing
	case pending:
		return "pending", nil
	}
	return "passing", nil
}

// ghErrLine is the first line of an execx error's stderr (or of the error itself).
func ghErrLine(err error) string {
	msg := err.Error()
	if loc := exitStatusRe.FindStringIndex(msg); loc != nil {
		msg = msg[loc[1]:]
	}
	line, _, _ := strings.Cut(msg, "\n")
	return line
}

func badRequest(format string, args ...any) error {
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(format, args...)}
}

// verified is one repo's checked finishing entry, ready to insert.
type verified struct {
	repo     finishRepo
	kind     string
	url      string
	pr       ghPR
	localSHA string
	note     string // kept only
}

// writeFinishing is swarm_checkpoint kind finishing (spec "writeFinishing, in order").
func (s *Store) writeFinishing(ctx context.Context, sessionID string, in CheckpointInput) (CheckpointResult, error) {
	var out CheckpointResult
	if n := utf8.RuneCountInString(in.Summary); n < 1 || n > 500 {
		return out, badRequest("Summary must be 1–500 characters.")
	}
	// 1. read pass
	var key string
	var fa items.FinishApproval
	var repos []finishRepo
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ses, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if !ses.State.Live() {
			return &items.Error{Code: items.CodeConflict, Message: "This session is not live."}
		}
		itemKey := in.ItemKey
		if itemKey == "" {
			if itemKey, err = s.itemKey(ctx, tx, a.ItemID); err != nil {
				return err
			}
		}
		it, err := s.Items.GetTx(ctx, tx, itemKey)
		if err != nil {
			return err
		}
		if a.Role != RoleOrchestrator || a.ParentAgentID != "" || it.ID != a.RootItemID {
			return badRequest("Only the top-level orchestrator can write finishing on its own item.")
		}
		key = it.Key
		fa, repos, err = s.finishStateTx(ctx, tx, it)
		return err
	})
	if err != nil {
		return out, err
	}

	// 2. coverage
	custom := fa.Merge == "custom"
	if len(in.Kept) > 0 && !custom {
		return out, badRequest("kept is only valid when the user chose one of your finish options.")
	}
	reported := map[string]string{} // repo -> "pr" | "local" | "kept"
	prURLs, localSHAs, keptNotes := map[string]string{}, map[string]string{}, map[string]string{}
	var dup []string // repos reported more than once
	report := func(repo, kind string) {
		if reported[repo] != "" {
			dup = append(dup, repo)
		}
		reported[repo] = kind
	}
	for _, p := range in.PRs {
		report(p.Repo, "pr")
		prURLs[p.Repo] = p.URL
	}
	for _, m := range in.Merged {
		if custom && !hexSHA.MatchString(m.SHA) {
			return out, badRequest("merged needs a hex commit sha for each repo.")
		}
		report(m.Repo, "local")
		localSHAs[m.Repo] = m.SHA
	}
	for _, k := range in.Kept {
		if n := utf8.RuneCountInString(k.Note); n < 1 || n > 300 {
			return out, badRequest("kept needs a note of 1–300 characters for each repo.")
		}
		report(k.Repo, "kept")
		keptNotes[k.Repo] = k.Note
	}
	integrated := map[string]bool{}
	for _, r := range repos {
		integrated[r.Ref.Repo] = true
	}
	for _, name := range append(append(repoNames(in.PRs), mergedNames(in.Merged)...), keptNames(in.Kept)...) {
		if !integrated[name] {
			return out, badRequest("%s is not in %s's integrated checkpoint.", name, key)
		}
	}
	if custom {
		// the user chose the orchestrator's own option: every repo reported once, however it was finished
		missing := slices.Clone(dup)
		for _, r := range repos {
			if reported[r.Ref.Repo] == "" {
				missing = append(missing, r.Ref.Repo)
			}
		}
		if len(missing) > 0 {
			return out, badRequest("Report every integrated repo once under prs, merged or kept: %s.", strings.Join(missing, ", "))
		}
	}
	for _, r := range repos {
		name := r.Ref.Repo
		switch {
		case custom && r.RepoID != "":
		case r.RepoID == "":
			return out, badRequest("Couldn't find %s in Swarm's repository catalog; register it with swarm_repo_register, then send finishing again.", name)
		case reported[name] == "":
			return out, badRequest("Missing %s: report a PR or a local merge for every integrated repo.", name)
		case r.GitHub && reported[name] != "pr":
			return out, badRequest("%s has a GitHub remote; report it under prs.", name)
		case !r.GitHub && reported[name] != "local":
			return out, badRequest("%s has no GitHub remote; merge it locally and report it under merged.", name)
		}
	}

	// 3. verification, outside any tx
	var checked []verified
	for _, r := range repos {
		v := verified{repo: r}
		switch {
		case reported[r.Ref.Repo] == "kept":
			v.kind, v.note = "kept", keptNotes[r.Ref.Repo]
		case custom && reported[r.Ref.Repo] == "local":
			// ponytail: a custom local merge is trusted, not ancestor-checked; the user chose the option.
			v.kind, v.localSHA = "local", localSHAs[r.Ref.Repo]
		case reported[r.Ref.Repo] == "pr" && custom:
			v.kind, v.url = "pr", prURLs[r.Ref.Repo]
			if v.pr, err = s.verifyPR(ctx, r, v.url, fa.Merge); err != nil {
				return out, err
			}
		case r.GitHub:
			v.kind, v.url = "pr", prURLs[r.Ref.Repo]
			if v.pr, err = s.verifyPR(ctx, r, v.url, fa.Merge); err != nil {
				return out, err
			}
		default:
			v.kind, v.localSHA = "local", localSHAs[r.Ref.Repo]
			if err := s.verifyLocal(ctx, r, v.localSHA); err != nil {
				return out, err
			}
		}
		checked = append(checked, v)
	}

	// 4. write tx
	_, err = IdemTx(ctx, s, sessionID, in.RequestID, "swarm_checkpoint", &out, func(tx *sql.Tx) error {
		ses, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		it, err := s.Items.GetTx(ctx, tx, key)
		if err != nil {
			return err
		}
		again, _, err := s.finishStateTx(ctx, tx, it)
		if err != nil {
			return err
		}
		if again.CheckpointID != fa.CheckpointID {
			return &items.Error{Code: items.CodeConflict, Message: fmt.Sprintf(
				"Nothing to finish: %s has no approved finish request for its latest integration.", key)}
		}
		now := db.Millis(s.Now())
		for _, v := range checked {
			state, checks, number, sha, url := "merged", "", any(nil), nullIf(v.localSHA), nullIf(v.url)
			if v.kind == "pr" {
				checks, _ = rollupChecks(v.pr)
				number = v.pr.Number
				state, sha = "open", nil
				if v.pr.State == "MERGED" {
					state = "merged"
					if v.pr.MergeCommit != nil {
						sha = nullIf(v.pr.MergeCommit.Oid)
					}
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO item_merges (id, item_id, integrated_checkpoint, repo, repo_id,
				kind, url, number, base, head, auto_merge, state, checks, merged_sha, checked_at, created_at, note)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				ids.New("mrg"), it.ID, fa.CheckpointID, v.repo.Ref.Repo, v.repo.RepoID, v.kind, url, number,
				v.repo.Base, v.repo.Ref.Branch, fa.Merge == "auto", state, checks, sha, now, now, nullIf(v.note)); err != nil {
				return err
			}
		}
		ckpID := ids.New("ckp")
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
			attempt, summary, next_json, blockers_json, git_json, verify_json, artifacts_json,
			processed_json, findings_json, daemon_written, created_at)
			VALUES (?,?,?,?,'progress',?,?,'[]','[]',?,'[]','[]','[]','[]',0,?)`,
			ckpID, sessionID, a.ID, it.ID, ses.Attempt, in.Summary, string(fa.Git), now); err != nil {
			return err
		}
		if _, err := s.Events.Append(ctx, tx, events.ItemChanged, map[string]string{"key": it.Key, "root_key": it.RootKey}); err != nil {
			return err
		}
		if err := s.Items.ReconcileTx(ctx, tx, key); err != nil {
			return err
		}
		if it, err = s.Items.GetTx(ctx, tx, key); err != nil {
			return err
		}
		if it.Status == items.Done {
			if err := s.notify(ctx, tx, NotifyInput{Kind: "item.merged", ItemKey: key, Args: map[string]string{"KEY": key}}); err != nil {
				return err
			}
		}
		out = CheckpointResult{CheckpointID: ckpID, ItemStatus: it.Status, ItemRevision: it.Revision}
		return nil
	})
	return out, err
}

// finishStateTx checks the root is in review with an approved finish and no finishing yet,
// and resolves its integrated repos.
func (s *Store) finishStateTx(ctx context.Context, tx *sql.Tx, it items.Item) (items.FinishApproval, []finishRepo, error) {
	fa, ok, err := s.Items.FinishApprovalTx(ctx, tx, it.ID)
	if err != nil {
		return fa, nil, err
	}
	if it.Status != items.InReview || !ok || fa.Merge == "" {
		return fa, nil, badRequest("Nothing to finish: %s has no approved finish request for its latest integration.", it.Key)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_merges WHERE item_id = ? AND integrated_checkpoint = ?`,
		it.ID, fa.CheckpointID).Scan(&n); err != nil {
		return fa, nil, err
	}
	if n > 0 {
		return fa, nil, &items.Error{Code: items.CodeConflict,
			Message: fmt.Sprintf("Finishing for %s is already recorded for this integration.", it.Key)}
	}
	var refs []GitRef
	if err := json.Unmarshal(fa.Git, &refs); err != nil {
		return fa, nil, err
	}
	repos, err := s.finishReposTx(ctx, tx, it.ID, refs)
	return fa, repos, err
}

func (s *Store) verifyPR(ctx context.Context, r finishRepo, url, merge string) (ghPR, error) {
	m := prURLRe.FindStringSubmatch(url)
	if m == nil || !strings.EqualFold(m[1], r.Owner) {
		return ghPR{}, badRequest("%s is not a PR in %s's %s repository.", url, r.Owner, r.Ref.Repo)
	}
	p, err := s.ghPRView(ctx, url)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return p, badRequest("gh isn't available to Swarm (%s). Install GitHub CLI and run gh auth login, then send finishing again.", err)
	case err != nil:
		return p, badRequest("Couldn't read %s with gh: %s. Check gh auth status, then send finishing again.", url, ghErrLine(err))
	case p.HeadRefName != r.Ref.Branch:
		return p, badRequest("%s merges %s, not the integrated branch %s.", url, p.HeadRefName, r.Ref.Branch)
	case p.BaseRefName != r.Base && merge != "custom": // a custom option may target another branch
		return p, badRequest("%s targets %s, not %s's default branch %s.", url, p.BaseRefName, r.Ref.Repo, r.Base)
	case p.State == "CLOSED":
		return p, badRequest("%s is closed without merging.", url)
	case merge == "auto" && p.AutoMergeRequest == nil && p.State != "MERGED":
		return p, badRequest("Auto-merge isn't on for %s. Wait for checks (gh pr checks %s --watch), fix failures, merge with gh pr merge %s --%s, then send finishing again.",
			url, url, url, s.mergeMethod(ctx, m[1], m[2]))
	}
	return p, nil
}

// mergeMethod is the repo's default merge method (locked decision 2); squash when gh can't say.
func (s *Store) mergeMethod(ctx context.Context, owner, name string) string {
	var allow struct {
		Squash bool `json:"allow_squash_merge"`
		Merge  bool `json:"allow_merge_commit"`
		Rebase bool `json:"allow_rebase_merge"`
	}
	out, err := s.runner()(ctx, "gh", "api", "repos/"+owner+"/"+name)
	if err != nil || json.Unmarshal(out, &allow) != nil {
		return "squash"
	}
	switch {
	case allow.Squash:
		return "squash"
	case allow.Merge:
		return "merge"
	case allow.Rebase:
		return "rebase"
	}
	return "squash"
}

func (s *Store) verifyLocal(ctx context.Context, r finishRepo, sha string) error {
	run := s.runner()
	for _, pair := range [][2]string{{r.Ref.SHA, sha}, {sha, r.Base}} {
		if _, err := run(ctx, "git", "-C", r.Path, "merge-base", "--is-ancestor", pair[0], pair[1]); err != nil {
			short := r.Ref.SHA
			if len(short) > 7 {
				short = short[:7]
			}
			return badRequest("%s is not on %s's %s; merge the integrated branch first.", short, r.Ref.Repo, r.Base)
		}
	}
	return nil
}

func repoNames(ps []FinishPR) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Repo)
	}
	return out
}

func keptNames(ks []KeptRepo) []string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Repo)
	}
	return out
}

func mergedNames(ms []FinishMerged) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Repo)
	}
	return out
}

// Merges is the newest integrated checkpoint's item_merges rows; nil when none.
func (s *Store) Merges(ctx context.Context, rootItemID string) ([]ItemMerge, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT repo, kind, COALESCE(url,''), COALESCE(number,0), base, head,
		auto_merge, state, checks, COALESCE(merged_sha,''), COALESCE(note,'') FROM item_merges
		WHERE item_id = ? AND integrated_checkpoint = (SELECT id FROM checkpoints
			WHERE item_id = ? AND kind = 'integrated' ORDER BY created_at DESC, rowid DESC LIMIT 1)
		ORDER BY created_at, repo`, rootItemID, rootItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemMerge
	for rows.Next() {
		var m ItemMerge
		if err := rows.Scan(&m.Repo, &m.Kind, &m.URL, &m.Number, &m.Base, &m.Head, &m.AutoMerge, &m.State,
			&m.Checks, &m.MergedSHA, &m.Note); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MergeProgressFor is nil unless the root is in_review with an approved finish.
func (s *Store) MergeProgressFor(ctx context.Context, rootItemID string) (*MergeProgress, error) {
	var status string
	if err := s.DB.QueryRowContext(ctx, `SELECT status FROM items WHERE id = ?`, rootItemID).Scan(&status); err != nil {
		return nil, err
	}
	if items.Status(status) != items.InReview {
		return nil, nil
	}
	fa, ok, err := s.Items.FinishApprovalTx(ctx, s.DB, rootItemID)
	if err != nil || !ok || fa.Merge == "" {
		return nil, err
	}
	var refs []GitRef
	if err := json.Unmarshal(fa.Git, &refs); err != nil {
		return nil, err
	}
	distinct := map[string]bool{}
	for _, r := range refs {
		distinct[r.Repo] = true
	}
	p := &MergeProgress{Total: len(distinct)}
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_merges WHERE item_id = ? AND integrated_checkpoint = ?
		AND state = 'merged'`, rootItemID, fa.CheckpointID).Scan(&p.Merged)
	return p, err
}

// WatchMerges polls each open PR row of an in_review root once (spec "WatchMerges, each tick").
func (s *Store) WatchMerges(ctx context.Context) error {
	type row struct {
		id, itemID, key, repo, url, checks string
		number                             int
	}
	rs, err := s.DB.QueryContext(ctx, `SELECT m.id, m.item_id, i.key, m.repo, m.url, m.number, m.checks
		FROM item_merges m JOIN items i ON i.id = m.item_id
		WHERE m.kind = 'pr' AND m.state = 'open' AND i.status = 'in_review' ORDER BY m.created_at, m.repo`)
	if err != nil {
		return err
	}
	var todo []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.itemID, &r.key, &r.repo, &r.url, &r.number, &r.checks); err != nil {
			rs.Close()
			return err
		}
		todo = append(todo, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	for _, r := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p, err := s.ghPRView(ctx, r.url)
		if err != nil {
			s.logf("merges: gh pr view %s: %v", r.url, err)
			continue
		}
		if err := s.tx(ctx, func(tx *sql.Tx) error { return s.applyPRTx(ctx, tx, r.id, r.itemID, r.key, r.repo, r.url, r.number, p) }); err != nil {
			s.logf("merges: %s: %v", r.url, err)
		}
	}
	return nil
}

// applyPRTx writes one polled PR state onto its item_merges row, re-checking it is still open.
func (s *Store) applyPRTx(ctx context.Context, tx *sql.Tx, id, itemID, key, repo, url string, number int, p ghPR) error {
	var state, old string
	if err := tx.QueryRowContext(ctx, `SELECT state, checks FROM item_merges WHERE id = ?`, id).Scan(&state, &old); err != nil {
		return err
	}
	if state != "open" {
		return nil
	}
	checks, failing := rollupChecks(p)
	now := db.Millis(s.Now())
	relay := func(fa items.FinishApproval, body any) error {
		if fa.AgentID == "" {
			return nil
		}
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		_, err = s.enqueueRaw(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: fa.AgentID,
			RootItemID: itemID, ItemID: itemID, Payload: b})
		return err
	}
	switch p.State {
	case "MERGED":
		sha := ""
		if p.MergeCommit != nil {
			sha = p.MergeCommit.Oid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE item_merges SET state = 'merged', merged_sha = ?, checks = ?, checked_at = ? WHERE id = ?`,
			nullIf(sha), checks, now, id); err != nil {
			return err
		}
	case "CLOSED":
		fa, _, err := s.Items.FinishApprovalTx(ctx, tx, itemID) // before ReconcileTx bumps the revision
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE item_merges SET state = 'closed', checks = ?, checked_at = ? WHERE id = ?`,
			checks, now, id); err != nil {
			return err
		}
		if err := relay(fa, struct {
			Event  string `json:"event"`
			Item   string `json:"item"`
			Repo   string `json:"repo"`
			URL    string `json:"url"`
			Number int    `json:"number"`
		}{"pr_closed", key, repo, url, number}); err != nil {
			return err
		}
	default: // OPEN
		if _, err := tx.ExecContext(ctx, `UPDATE item_merges SET checks = ?, checked_at = ? WHERE id = ?`, checks, now, id); err != nil {
			return err
		}
		if checks == old {
			return nil
		}
		if checks != "failing" {
			break
		}
		fa, _, err := s.Items.FinishApprovalTx(ctx, tx, itemID)
		if err != nil {
			return err
		}
		if err := relay(fa, struct {
			Event   string   `json:"event"`
			Item    string   `json:"item"`
			Repo    string   `json:"repo"`
			URL     string   `json:"url"`
			Number  int      `json:"number"`
			Failing []string `json:"failing"`
		}{"pr_checks_failed", key, repo, url, number, failing}); err != nil {
			return err
		}
		if err := s.notify(ctx, tx, NotifyInput{Kind: "pr.checks_failed", ItemKey: key, Args: map[string]string{
			"KEY": key, "repo": repo, "N": strconv.Itoa(number), "checks": strings.Join(failing, ", ")}}); err != nil {
			return err
		}
	}
	it, err := s.Items.GetTx(ctx, tx, key)
	if err != nil {
		return err
	}
	if _, err := s.Events.Append(ctx, tx, events.ItemChanged, map[string]string{"key": it.Key, "root_key": it.RootKey}); err != nil {
		return err
	}
	if p.State == "OPEN" {
		return nil
	}
	if err := s.Items.ReconcileTx(ctx, tx, key); err != nil {
		return err
	}
	if p.State == "MERGED" {
		if it, err = s.Items.GetTx(ctx, tx, key); err != nil {
			return err
		}
		if it.Status == items.Done {
			return s.notify(ctx, tx, NotifyInput{Kind: "item.merged", ItemKey: key, Args: map[string]string{"KEY": key}})
		}
	}
	return nil
}

// WatchMergesLoop runs WatchMerges every `every` until ctx is cancelled. Same shape as ReclaimWorktreesLoop.
func (s *Store) WatchMergesLoop(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.after(every):
		}
		if err := s.WatchMerges(ctx); err != nil && ctx.Err() == nil {
			s.logf("merges: %v", err)
		}
	}
}

package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

const tddMissing = "TDD evidence missing: record the failing test run (phase: red) before completing."
const pausedTool = "paused: finish your handoff and stop."

var gatedRoles = []Role{RoleCoder, RoleDebugger, RoleMechanical}
var pauseAllowedKinds = []CheckpointKind{Handoff, BlockedCkp, FailedCkp}

// checkpointNotifyKind is the §17.5 notification for a checkpoint kind; kinds
// absent from this map raise nothing.
var checkpointNotifyKind = map[CheckpointKind]string{
	Accepted: "accepted", CompletedCkp: "completed", FailedCkp: "failed", BlockedCkp: "blocked",
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
}

// CheckpointResult is swarm_checkpoint's result.
type CheckpointResult struct {
	CheckpointID string
	ItemStatus   items.Status
}

// tddOK is L24. Evidence is every verification entry of this attempt, earlier
// checkpoints included, so a pause and resume inside one attempt keeps it.
func tddOK(prior, now []Verify) bool {
	all := append(append([]Verify{}, prior...), now...)
	if len(all) == 0 {
		return false
	}
	redAt := map[string]int{}
	ok := false
	for i, v := range all {
		switch v.Phase {
		case "red":
			if _, seen := redAt[v.Cmd]; !seen {
				redAt[v.Cmd] = i
			}
		case "green":
			if r, seen := redAt[v.Cmd]; seen && r < i {
				ok = true
			}
		}
	}
	if !ok {
		return false
	}
	// the run must end green: the last entry per command is green and ok
	last := map[string]Verify{}
	for _, v := range all {
		last[v.Cmd] = v
	}
	for _, v := range last {
		if v.Phase != "green" || !v.OK {
			return false
		}
	}
	return true
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
// still sitting at Ready) is a silent no-op: the checkpoint itself always
// records, and the derived status only moves when the state machine allows it.
// Any other error (a DB failure) still propagates.
func (s *Store) tryTransition(ctx context.Context, tx *sql.Tx, key string, to items.Status) error {
	_, err := s.Items.TransitionTx(ctx, tx, key, to, items.Daemon())
	if err == nil {
		return nil
	}
	var ie *items.Error
	if errors.As(err, &ie) && ie.Code == items.CodeTransitionDenied {
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

// WriteCheckpoint is swarm_checkpoint (§8.1, L24).
func (s *Store) WriteCheckpoint(ctx context.Context, sessionID string, in CheckpointInput) (CheckpointResult, error) {
	var out CheckpointResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
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

		if in.Kind == CompletedCkp && it.TddExempt == "" {
			gated := slices.Contains(gatedRoles, a.Role)
			if !gated && a.Role == RoleOrchestrator && s.changedFiles(ctx, in.Git) > 0 {
				gated = true
			}
			if gated {
				prior, err := s.priorVerify(ctx, tx, a.ID, ses.Attempt)
				if err != nil {
					return err
				}
				if !tddOK(prior, in.Verification) {
					return errors.New(tddMissing)
				}
			}
		}

		ckpID := ids.New("ckp")
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind,
			attempt, resolution, summary, next_json, blockers_json, git_json, verify_json, artifacts_json,
			processed_json, daemon_written, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
			ckpID, sessionID, a.ID, it.ID, string(in.Kind), ses.Attempt, nullIf(in.Resolution), in.Summary,
			jsonArray(in.Next), jsonArray(in.Blockers), jsonArray(in.Git), jsonArray(in.Verification),
			jsonArray(in.Artifacts), jsonArray(in.Processed), db.Millis(now)); err != nil {
			return err
		}
		out.CheckpointID = ckpID

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
					AgentName: a.Name, ItemKey: itemKey, RequestID: reqID}); err != nil {
					return err
				}
			}
		}

		if a.ParentAgentID != "" {
			body, err := json.Marshal(map[string]any{
				"event": string(in.Kind), "agent": a.Name, "item": itemKey,
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

		if _, err := s.Events.Append(ctx, tx, events.CheckpointCreated,
			map[string]string{"item": itemKey, "agent": a.Name, "kind": string(in.Kind)}); err != nil {
			return err
		}
		if kindStr, ok := checkpointNotifyKind[in.Kind]; ok && s.Notify != nil {
			if err := s.Notify.Raise(ctx, tx, NotifyInput{Kind: "agent." + kindStr,
				AgentName: a.Name, ItemKey: itemKey}); err != nil {
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
		return nil
	})
	return out, err
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

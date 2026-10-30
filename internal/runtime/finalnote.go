package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
	"github.com/AlexanderTar/swarmrun/internal/items"
)

// Final-note nudge gaps: the first nudge goes out after 5 min idle on a Done
// root, the next 15 min after that, then 30 min, then hourly.
const (
	finalNoteFirstAfter  = 5 * time.Minute
	finalNoteSecondAfter = 15 * time.Minute
	finalNoteThirdAfter  = 30 * time.Minute
	finalNoteRepeatAfter = time.Hour
)

const finalNoteNudgeEvent = "final_note_nudge"

// finalNoteNote asks an idle orchestrator on a Done root for its final note.
// %s is the root key.
const finalNoteNote = "%s is Done. If your post-merge work (deploys, beta builds, cleanup) is " +
	"finished, write swarm_checkpoint kind completed with a one-line final summary. " +
	"Otherwise carry on and finish it."

// nudgeFinalNote asks an idle orchestrator on a Done root with no final note
// for its completed checkpoint, with 5m/15m/30m/hourly backoff, at most one
// pending nudge, and one attention notification on the first nudge. It never
// kills. idle is the same pane-idle value resolveAlive computed: a busy
// orchestrator is still working, not waiting, and gets nothing.
func (s *Store) nudgeFinalNote(ctx context.Context, r liveRow, idle bool) error {
	if !idle || r.Role != RoleOrchestrator || r.State != Running {
		return nil
	}
	if r.CompletedCheckpointAt != nil {
		return nil
	}
	var rootKey, rootStatus string
	if err := s.DB.QueryRowContext(ctx, `SELECT key, status FROM items WHERE id = ?`,
		r.RootItemID).Scan(&rootKey, &rootStatus); err != nil {
		return err
	}
	if rootStatus != string(items.Done) {
		return nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT state, created_at FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'
		AND origin = 'daemon' AND payload_json LIKE '%"event":"final_note_nudge"%'`, r.AgentID)
	if err != nil {
		return err
	}
	var pending bool
	var count int
	var last time.Time
	for rows.Next() {
		var state string
		var created int64
		if err := rows.Scan(&state, &created); err != nil {
			rows.Close()
			return err
		}
		if state == "pending" {
			pending = true
		}
		count++
		if t := db.FromMillis(created); t.After(last) {
			last = t
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Never more than one pending nudge: an orchestrator inside one long
	// turn cannot read it, so a second would only stack behind the first.
	if pending {
		return nil
	}
	baseline := r.StartedAt
	if r.LastSeenAt != nil && r.LastSeenAt.After(baseline) {
		baseline = *r.LastSeenAt
	}
	if r.LastCheckpointAt != nil && r.LastCheckpointAt.After(baseline) {
		baseline = *r.LastCheckpointAt
	}
	wait := finalNoteFirstAfter
	if count > 0 {
		baseline = last
		switch count {
		case 1:
			wait = finalNoteSecondAfter
		case 2:
			wait = finalNoteThirdAfter
		default:
			wait = finalNoteRepeatAfter
		}
	}
	if s.Now().Sub(baseline) < wait {
		return nil
	}
	payload, err := json.Marshal(map[string]string{"event": finalNoteNudgeEvent,
		"note": fmt.Sprintf(finalNoteNote, rootKey)})
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.enqueue(ctx, tx, Message{Kind: "assignment_update", Origin: "daemon",
			ToAgentID: r.AgentID, RootItemID: r.RootItemID, ItemID: r.ItemID, Payload: payload}); err != nil {
			return err
		}
		if count == 0 {
			return s.notify(ctx, tx, NotifyInput{Kind: "agent.final_note", AgentName: r.AgentName,
				ItemKey: r.ItemKey, Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey}})
		}
		return nil
	})
}

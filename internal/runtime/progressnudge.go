package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/db"
)

// progressNudgeAfter is how long a busy worker may go without a checkpoint before
// the daemon reminds it to write a progress one, and the gap between reminders.
const progressNudgeAfter = 15 * time.Minute

const progressNudgeNote = "You have been working for a while without a checkpoint. Write a progress checkpoint now " +
	"(one or two plain sentences on what landed and what is next), then carry on. " +
	"Write one at least every ~15 minutes of work and at each milestone."

// nudgeProgress asks a busy worker for a progress checkpoint once it has gone
// progressNudgeAfter without one (or since its start), at most once per window
// and never while an earlier reminder is still pending: a worker inside one long
// tool call cannot read it, so a second would only stack behind the first.
// The reminder is an ordinary assignment_update, so it rides the usual wake and
// hook-notice path and never interrupts a turn. Nothing goes to a worker that is
// pausing or whose kind is out of usage. The window is read back from the
// messages table, so a daemon restart does not repeat a reminder.
func (s *Store) nudgeProgress(ctx context.Context, r liveRow) error {
	if r.State != Running {
		return nil
	}
	last := r.StartedAt
	if r.LastCheckpointAt != nil && r.LastCheckpointAt.After(last) {
		last = *r.LastCheckpointAt
	}
	// The window starts at the current working stretch: an agent idle for
	// hours that wakes with its first tool call must not be nudged for the
	// silence before it woke.
	if ws, ok := s.workingSince(r.SessionID); ok && ws.After(last) {
		last = ws
	}
	if s.Now().Sub(last) < progressNudgeAfter || !s.busyWithinCeiling(r.SessionID) || s.kindExhausted(ctx, r.Kind) {
		return nil
	}
	var recent int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'
		AND origin = 'daemon' AND payload_json LIKE '%"event":"progress_nudge"%' AND (state = 'pending' OR created_at > ?)`,
		r.AgentID, db.Millis(s.Now().Add(-progressNudgeAfter))).Scan(&recent); err != nil || recent > 0 {
		return err
	}
	payload, err := json.Marshal(map[string]string{"event": "progress_nudge", "note": progressNudgeNote})
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := s.enqueue(ctx, tx, Message{Kind: "assignment_update", Origin: "daemon",
			ToAgentID: r.AgentID, RootItemID: r.RootItemID, ItemID: r.ItemID, Payload: payload})
		return err
	})
}

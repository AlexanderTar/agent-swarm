package items_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

const briefTooLongMsg = "Brief too long (max 6000 characters). Move detail into an artifact and reference it."

// TASK-1206 (BUG-106): the brief cap is enforced where a brief is written, not
// only when a kickoff is built, so a task can't grow a brief a later workflow
// step would refuse to spawn.
func TestCreateRefusesOverlongBrief(t *testing.T) {
	st := newStore(t)
	_, err := st.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Big", Brief: strings.Repeat("x", 6001)}, items.Daemon())
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != briefTooLongMsg {
		t.Fatalf("Create err = %v, want bad_request %q", err, briefTooLongMsg)
	}
	if _, err := st.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Fits", Brief: strings.Repeat("x", 6000)}, items.Daemon()); err != nil {
		t.Fatalf("Create at the cap: %v", err)
	}
}

func TestUpdateRefusesOverlongBrief(t *testing.T) {
	st := newStore(t)
	ep := mk(t, st, items.Epic, "", "Grow")
	long := strings.Repeat("x", 6001)
	_, err := st.Update(ctx, ep.Key, items.Patch{Brief: &long, Revision: ep.Revision}, items.Daemon())
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != briefTooLongMsg {
		t.Fatalf("Update err = %v, want bad_request %q", err, briefTooLongMsg)
	}
	if got := mustGet(t, st, ep.Key); got.Brief != "" || got.Revision != ep.Revision {
		t.Fatalf("refused update changed the item: brief %d chars, revision %d", len(got.Brief), got.Revision)
	}
}

// An item whose brief already exceeds the cap (written before the check) can
// still have its title or status edited; only writing a brief is refused.
func TestUpdateLeavesLegacyOverlongBriefAlone(t *testing.T) {
	st := newStore(t)
	ep := mk(t, st, items.Epic, "", "Legacy")
	if _, err := st.DB.ExecContext(ctx, `UPDATE items SET brief = ? WHERE key = ?`, strings.Repeat("x", 7000), ep.Key); err != nil {
		t.Fatal(err)
	}
	cur := mustGet(t, st, ep.Key)
	title := "Legacy renamed"
	if _, err := st.Update(ctx, ep.Key, items.Patch{Title: &title, Revision: cur.Revision}, items.Daemon()); err != nil {
		t.Fatalf("title-only update of a legacy long brief: %v", err)
	}
}

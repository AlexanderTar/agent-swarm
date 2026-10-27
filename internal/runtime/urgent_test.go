package runtime

import (
	"context"
	"encoding/json"
	"testing"
)

// Only the daemon's pause and handoff control messages are urgent. Nothing an
// agent can send (question, answer, finding) ever is, and a control message
// with another action is not either.
func TestOnlyPauseAndHandoffControlMessagesAreUrgent(t *testing.T) {
	cases := []struct {
		kind    MessageKind
		payload string
		urgent  bool
	}{
		{"control", `{"action":"pause","scope":"session"}`, true},
		{"control", `{"action":"handoff","scope":"session"}`, true},
		{"control", `{"action":"resume"}`, false},
		{"control", `{}`, false},
		{"finding", `{"body":"x","action":"pause"}`, false},
		{"answer", `{"body":"x","action":"handoff"}`, false},
		{"question", `{"body":"x"}`, false},
		{"assignment_update", `{"note":"x"}`, false},
		{"relay", `{"action":"pause"}`, false},
	}
	for _, c := range cases {
		if got := IsUrgent(c.kind, json.RawMessage(c.payload)); got != c.urgent {
			t.Errorf("IsUrgent(%s, %s) = %v, want %v", c.kind, c.payload, got, c.urgent)
		}
	}
}

// The stored priority follows urgency: 0 for urgent, 1 for everything else,
// whatever the caller asked for.
func TestEnqueueStoresPriorityZeroOnlyForUrgentMessages(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Urgent", Intent: "feature", Kind: Fake, Model: "fake-1"})
	for _, c := range []struct {
		kind    MessageKind
		payload string
		want    int
	}{
		{"control", `{"action":"pause","scope":"session"}`, 0},
		{"control", `{"action":"handoff","scope":"session"}`, 0},
		{"control", `{"action":"resume"}`, 1},
		{"finding", `{"body":"x"}`, 1},
		{"answer", `{"body":"x"}`, 1},
	} {
		m := enq(t, s, a.ID, a.RootItemID, c.kind, c.payload, 0)
		var got int
		if err := s.DB.QueryRowContext(ctx, `SELECT priority FROM messages WHERE id = ?`, m.ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s %s stored priority %d, want %d", c.kind, c.payload, got, c.want)
		}
	}
}

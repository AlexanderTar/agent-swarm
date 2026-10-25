package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// A kind a pane showed a usage-limit line for is exhausted until its reset,
// to every gate caller, with no usage poller at all (BUG-110: muse's weekly
// cap read 99% in the poller while the pane said "Usage limit reached").
func TestNotedUsageLimitDivertsSpawnsUntilReset(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
	ctx := context.Background()
	if s.kindExhausted(ctx, Claude) {
		t.Fatal("nothing noted yet")
	}
	reset := s.Now().Add(time.Hour)
	s.noteUsageLimit(Claude, reset)
	if !s.kindExhausted(ctx, Claude) || s.kindExhausted(ctx, Codex) {
		t.Fatal("only the noted kind reads exhausted")
	}
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Investigate crash", Intent: "debug",
		Kind: Claude, Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Codex || len(tm.started) != 1 {
		t.Fatalf("agent = %+v, started = %v, want substituted to codex", a, tm.started)
	}
	// Past the reset the kind is usable again.
	tm.clk.Advance(2 * time.Hour)
	if s.kindExhausted(ctx, Claude) {
		t.Fatal("exhausted after the reset time passed")
	}
}

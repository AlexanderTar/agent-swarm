package main

import (
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	usagesvc "github.com/AlexanderTar/agent-swarm/internal/usage"
)

func meterAt(id string, at time.Time) usagesvc.Meter {
	return usagesvc.Meter{ID: id, ResetsAt: &at}
}

// Root cause B: checkQuotaResets used to read only the latest snapshot's
// ResetsAt. Codex's 5h usage window restarts on first use after a reset
// (the user's own codex use shares it), so as soon as anyone uses codex
// past the reset instant, the next usage poll already reports the NEXT
// window's ResetsAt -- the old cutoff is never seen in the (cutoff+1m,
// cutoff+1h) detection window at all. dueResets must remember every
// ResetsAt ever observed per (kind, meter), not just the latest, so the
// old cutoff survives the live value moving on.
func TestDueResetsFiresOnAnOldCutoffAfterTheWindowRolledOver(t *testing.T) {
	seen := map[string]map[int64]bool{}
	windowStart := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	// First poll, 5 minutes before the reset: nothing due yet, but the
	// cutoff must be remembered.
	fires := dueResets(seen, []usagesvc.Snapshot{
		{Agent: runtime.Codex, Meters: []usagesvc.Meter{meterAt("codex_5h", windowStart)}},
	}, windowStart.Add(-5*time.Minute))
	if len(fires) != 0 {
		t.Fatalf("fires before the reset = %v, want none", fires)
	}

	// Next poll, 3 minutes after the reset: the user's own codex use has
	// already rolled the window forward, so the LIVE snapshot now reports
	// the next window's ResetsAt -- but the old cutoff is still due.
	nextWindow := windowStart.Add(5 * time.Hour)
	fires = dueResets(seen, []usagesvc.Snapshot{
		{Agent: runtime.Codex, Meters: []usagesvc.Meter{meterAt("codex_5h", nextWindow)}},
	}, windowStart.Add(3*time.Minute))
	if len(fires) != 1 || fires[0].Kind != runtime.Codex || !fires[0].Cutoff.Equal(windowStart) {
		t.Fatalf("fires = %+v, want one fire for %s at %s", fires, runtime.Codex, windowStart)
	}

	// 90 minutes after the old cutoff: outside the 1h detection window, so
	// it must no longer fire, and the entry must have been pruned (the
	// tracker cannot grow forever over the daemon's lifetime).
	fires = dueResets(seen, nil, windowStart.Add(90*time.Minute))
	if len(fires) != 0 {
		t.Fatalf("fires long after the cutoff = %v, want none (pruned)", fires)
	}
	if len(seen["codex|codex_5h"]) != 1 {
		t.Fatalf("seen[codex|codex_5h] = %v, want only the still-live next-window cutoff left", seen["codex|codex_5h"])
	}
}

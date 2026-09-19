// Package usagegate decides whether an agent kind is confirmed to be out of
// usage right now, for docs/specs/2026-09-19-usage-fallback-agent.md's
// usage-triggered fallback feature.
//
// internal/usage already imports internal/runtime (for AgentKind on
// Snapshot.Agent), so internal/runtime can never import internal/usage back
// without a cycle. This package sits above both and adapts a live
// *usage.Poller into runtime.UsageReader, the minimal interface
// internal/runtime consumes.
package usagegate

import (
	"context"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/usage"
)

// exhaustionPct is the UsedPct at/above which a meter counts as exhausted.
// This is 100, not a softer "close to 100" cutoff: Claude's real
// quota-exhausted response reports a genuine 100.0 UsedPct (confirmed by
// this session's other work on internal/usage/claude.go), so a lower
// threshold would be an unverified guess that risks diverting tasks off an
// agent that still has real, usable capacity left.
const exhaustionPct = 100.0

// Exhausted is the pure heuristic behind the fallback feature, independent
// of any I/O so it's directly unit-testable. snap is confirmed exhausted
// iff it is fresh (!Stale), carries no fetch error (a fetch error is not
// evidence of quota exhaustion), has at least one meter, and any one of its
// meters is at or above exhaustionPct with a reset time that hasn't already
// passed (a 100% reading whose window already rolled over is a stale
// display, not a live block).
//
// Every meter is checked, not just the headline (display) meter: each
// meter internal/usage's sources emit represents an independent hard cap,
// and hitting any one of them blocks further requests regardless of what
// the headline meter shows.
func Exhausted(snap usage.Snapshot, now time.Time) bool {
	if snap.Stale || snap.Error != "" || len(snap.Meters) == 0 {
		return false
	}
	for _, m := range snap.Meters {
		if m.UsedPct < exhaustionPct {
			continue
		}
		if m.ResetsAt != nil && !m.ResetsAt.After(now) {
			continue
		}
		return true
	}
	return false
}

// Gate implements runtime.UsageReader against a live *usage.Poller.
type Gate struct {
	Poller *usage.Poller
	Now    func() time.Time // nil means time.Now
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Exhausted reports whether kind is confirmed out of usage right now. A
// kind that was never polled (no stored snapshot at all) is "unknown", not
// "exhausted" -- it returns false, matching Exhausted's own fail-open
// default for missing data.
func (g *Gate) Exhausted(ctx context.Context, kind runtime.AgentKind) bool {
	snaps, err := g.Poller.Snapshots(ctx)
	if err != nil {
		return false
	}
	for _, s := range snaps {
		if s.Agent == kind {
			return Exhausted(s, g.now())
		}
	}
	return false
}

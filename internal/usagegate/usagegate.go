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
// evidence of quota exhaustion), has at least one meter, and its headline
// meter is at or above exhaustionPct with a reset time that hasn't already
// passed (a 100% reading whose window already rolled over is a stale
// display, not a live block).
//
// Only the headline meter is checked, not "any meter": reading
// internal/usage/claude.go, several of the meters a source emits are
// per-model or per-pool (Claude's seven_day_opus/seven_day_fable/
// seven_day_<model>, Cursor's cursor_auto vs cursor_api) -- hitting one of
// those does not block every other model or pool on that same agent kind.
// Checking any such meter would refuse a spawn that would actually have
// succeeded (e.g. the default config's Opus-heavy roles hitting their
// weekly cap must not block the Sonnet-based coder role) -- a regression
// this feature must never cause. The headline meter is each source's own
// choice of "the representative reading" (HeadlineID), so treating it as
// the availability signal is source-agnostic and requires no per-kind
// special-casing here. A genuine all-models cap that isn't reflected in the
// headline is a false negative (this kind reads as "available" when it
// isn't) -- that is the same stall this feature is meant to reduce, not a
// new one, and is preferred over the false-positive risk above.
func Exhausted(snap usage.Snapshot, now time.Time) bool {
	if snap.Stale || snap.Error != "" || len(snap.Meters) == 0 {
		return false
	}
	m := headlineMeter(snap)
	if m.UsedPct < exhaustionPct {
		return false
	}
	return m.ResetsAt == nil || m.ResetsAt.After(now)
}

// headlineMeter mirrors the menubar's own UsageSnapshot.headline convention
// (apps/menubar/Sources/SwarmBarKit/Wire.swift): the meter whose ID matches
// HeadlineID, or the first meter when HeadlineID is empty or matches none
// (every real source sets it, but a test fixture or a future source might
// not).
func headlineMeter(snap usage.Snapshot) usage.Meter {
	for _, m := range snap.Meters {
		if m.ID == snap.HeadlineID {
			return m
		}
	}
	return snap.Meters[0]
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

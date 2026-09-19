package runtime

import (
	"context"
	"fmt"
	"slices"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
)

// resolveUsageFallback is the fallback-resolution step
// (docs/specs/2026-09-19-usage-fallback-agent.md), called immediately
// before every place the daemon turns a resolved {kind, model} pair into a
// live session: StartSpike, StartOrchestrator, Spawn, Retry and
// DrainQueue's startQueued.
//
// It returns (kind, model) unchanged with a nil error when kind isn't
// confirmed exhausted (including whenever Store.Usage is nil -- the
// feature is off). When kind is exhausted and a usable fallback exists
// (configured, a different agent than kind, enabled, and not itself
// exhausted), it returns the substituted (fallback kind, fallback model)
// with substituted=true.
//
// When kind is exhausted and no usable fallback exists -- none configured,
// the fallback is the same agent as kind, the fallback isn't enabled, or
// the fallback is also confirmed exhausted -- it returns a non-nil error.
// The caller must treat that error exactly like a Preflight refusal (spec
// Locked Decision 6) and must NOT use the returned kind/model to spawn:
// proceeding would spawn against an agent already confirmed to have no
// quota left, guaranteeing the exact stall this feature exists to prevent.
func (s *Store) resolveUsageFallback(ctx context.Context, kind AgentKind, model string) (AgentKind, string, bool, error) {
	if s.Usage == nil || !s.Usage.Exhausted(ctx, kind) {
		return kind, model, false, nil
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		// Can't read Settings for a reason unrelated to usage: fail open
		// rather than block a spawn on an unrelated Settings error.
		return kind, model, false, nil
	}
	fb := cfg.FallbackDefault
	unusable := fb.Agent == "" || fb.Agent == kind ||
		!slices.Contains(cfg.EnabledAgents, fb.Agent) ||
		s.Usage.Exhausted(ctx, fb.Agent)
	if unusable {
		return kind, model, false, fmt.Errorf("%s is out of usage, and no other agent is available right now.", kind.Display())
	}
	fbModel := fb.Model
	if models, _, err := s.Catalog.ModelsFor(ctx, fb.Agent); err == nil {
		if _, ok := catalog.Find(models, fbModel); !ok && len(models) > 0 {
			fbModel = models[0].ID
		}
	}
	return fb.Agent, fbModel, true, nil
}

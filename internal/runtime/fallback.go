package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
)

// resolveUsageFallback is the fallback-resolution step
// (docs/specs/2026-09-19-usage-fallback-agent.md), called immediately
// before every place the daemon turns a resolved {kind, model, effort}
// triple into a live session: StartSpike, StartOrchestrator, Spawn, Retry
// and DrainQueue's startQueued.
//
// It returns (kind, model, effort) unchanged with a nil error when kind
// isn't confirmed exhausted (including whenever Store.Usage is nil -- the
// feature is off). When kind is exhausted and a usable fallback exists
// (configured, a different agent than kind, enabled, and not itself
// exhausted), it returns the substituted (fallback kind, fallback model,
// fallback effort) with substituted=true. effort must be substituted along
// with kind/model, not carried over from the original agent: an effort
// level valid for the original model (e.g. Claude's "xhigh") is frequently
// not a level the fallback's own model supports at all (Preflight would
// then refuse the very substitution meant to keep the spawn alive).
//
// When kind is exhausted and no usable fallback exists -- none configured,
// the fallback is the same agent as kind, the fallback isn't enabled, or
// the fallback is also confirmed exhausted -- it returns a non-nil error.
// The caller must treat that error exactly like a Preflight refusal (spec
// Locked Decision 6) and must NOT use the returned kind/model/effort to
// spawn: proceeding would spawn against an agent already confirmed to have
// no quota left, guaranteeing the exact stall this feature exists to
// prevent.
func (s *Store) resolveUsageFallback(ctx context.Context, kind AgentKind, model, effort string) (AgentKind, string, string, bool, error) {
	if s.Usage == nil || !s.Usage.Exhausted(ctx, kind) {
		return kind, model, effort, false, nil
	}
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		// Can't read Settings for a reason unrelated to usage: fail open
		// rather than block a spawn on an unrelated Settings error.
		return kind, model, effort, false, nil
	}
	fb := cfg.FallbackDefault
	unusable := fb.Agent == "" || fb.Agent == kind ||
		!slices.Contains(cfg.EnabledAgents, fb.Agent) ||
		s.Usage.Exhausted(ctx, fb.Agent)
	if unusable {
		return kind, model, effort, false, fmt.Errorf("%s is out of usage, and no other agent is available right now.", kind.Display())
	}
	fbModel, fbEffort := fb.Model, fb.Effort
	if models, _, err := s.Catalog.ModelsFor(ctx, fb.Agent); err == nil {
		if _, ok := catalog.Find(models, fbModel); !ok && len(models) > 0 {
			// The configured fallback model disappeared from the catalog:
			// fall back to the newest model for that agent, same as every
			// other "" -> models[0].ID default-picking call site. fb.Effort
			// was only ever validated (settings.validate) against the
			// *configured* model, not this substitute, so it doesn't carry
			// over either -- "" (the agent's own default) is always safe.
			fbModel, fbEffort = models[0].ID, ""
		}
	}
	return fb.Agent, fbModel, fbEffort, true, nil
}

// applyRetryFallback re-resolves a's kind/model/effort against usage
// exhaustion the way Retry has always done, whether the launch happens
// immediately or after waiting in the agent-limit queue (startSuccessor,
// reason "retry"): a freshly substituted kind has never been Preflighted,
// so it must be here, and the advisor kind/mode is re-resolved for it since
// the kind swap can flip whether "native" advisor mode still applies.
// Returns the (possibly updated) agent, the kind it had before any
// substitution (for the fallback_used notification), and whether one
// happened.
func (s *Store) applyRetryFallback(ctx context.Context, a Agent) (Agent, AgentKind, bool, error) {
	origKind := a.Kind
	fbKind, fbModel, fbEffort, substituted, err := s.resolveUsageFallback(ctx, a.Kind, a.Model, a.Effort)
	if err != nil {
		return a, origKind, false, err
	}
	if !substituted {
		return a, origKind, false, nil
	}
	if err := s.Preflight(ctx, PreflightInput{Kind: fbKind, Model: fbModel, Effort: fbEffort, Role: a.Role}); err != nil {
		return a, origKind, false, err
	}
	advKind, advModel, advEffort, advMode, advRequestedEffort := s.resolveAdvisor(ctx, fbKind,
		&AdvisorChoice{Kind: AgentKind(a.AdvisorKind), Model: a.AdvisorModel, Effort: a.AdvisorRequestedEffort})
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agents SET kind = ?, model = ?, effort = ?,
			advisor_kind = NULLIF(?, ''), advisor_model = NULLIF(?, ''), advisor_effort = NULLIF(?, ''), advisor_mode = NULLIF(?, ''), advisor_requested_effort = NULLIF(?, ''),
			kind_reason = ?
			WHERE id = ?`,
			string(fbKind), fbModel, fbEffort, string(advKind), advModel, advEffort, advMode, advRequestedEffort,
			joinReason(a.KindReason, fallbackReason(origKind)), a.ID)
		return err
	}); err != nil {
		return a, origKind, false, err
	}
	a.Kind, a.Model, a.Effort = fbKind, fbModel, fbEffort
	a.KindReason = joinReason(a.KindReason, fallbackReason(origKind))
	a.AdvisorKind, a.AdvisorModel, a.AdvisorEffort, a.AdvisorMode, a.AdvisorRequestedEffort = string(advKind), advModel, advEffort, advMode, advRequestedEffort
	return a, origKind, true, nil
}

// notifyRetryFallback raises agent.fallback_used after a retry substitution,
// immediate or queued.
func (s *Store) notifyRetryFallback(ctx context.Context, a Agent, origKind AgentKind) {
	if s.Notify == nil {
		return
	}
	var itemKey string
	_ = s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.ItemID).Scan(&itemKey)
	_ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.fallback_used", AgentName: a.Name, ItemKey: itemKey,
		Args: map[string]string{"name": a.Name, "agent": a.Kind.Display(), "from": origKind.Display()}})
}

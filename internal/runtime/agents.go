package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
)

type SpawnInput struct {
	ItemKey       string
	Role          Role
	Kind          AgentKind
	Model         string
	Effort        string
	Advisor       *AdvisorChoice
	ParentAgentID any
	Name          string
	Brief         BriefInput
	RepoPaths     []string
	Worktrees     []WorkflowWorktree
	// SessionID is the calling orchestrator's own MCP session, and RequestID
	// is I11's idempotency key scoped to it (empty means "no idempotency,
	// just run once"). Neither is the spawned agent's own session.
	SessionID string
	RequestID string
	// OverrideReason is what the user asked for when Kind/Model/Effort are
	// set explicitly (swarm_spawn requires it); recorded as kind_reason.
	OverrideReason string
}

type PreflightInput struct {
	Kind      AgentKind
	Model     string
	Effort    string
	Role      Role
	RepoPaths []string
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) after(d time.Duration) <-chan time.Time {
	if s.After != nil {
		return s.After(d)
	}
	return time.After(d)
}

func (s *Store) go_(f func()) {
	if s.Go != nil {
		s.Go(f)
		return
	}
	go f()
}

func (s *Store) baseEnv(osEnv func(string) string) map[string]string {
	if s.BaseEnv == nil {
		panic("runtime: BaseEnv is not wired")
	}
	return s.BaseEnv(osEnv)
}

// defaultName is §4: "<kebab(title) up to 24>-orchestrator" for a top-level
// orchestrator, "<kebab(item title) up to 24>-<role>" for a worker.
func defaultName(role Role, title string) (string, error) {
	slug, err := ids.KebabMax(title, 24)
	if err != nil {
		return "", err
	}
	if role == RoleOrchestrator {
		return slug + "-orchestrator", nil
	}
	// Kebab the whole name, not just the title: a role like ui_reviewer
	// must not leak its underscore into the agent name.
	return ids.Kebab(slug + "-" + string(role))
}

// joinReason joins two kind_reason parts with "; ", skipping empties.
func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// roleOverrideReason is the kind_reason part a parent's role override adds,
// with the user's reason when swarm_role_overrides stored one.
func roleOverrideReason(parentName string, rd settings.RoleDefault) string {
	r := "Role override set on " + parentName
	if rd.Reason != "" {
		r += ": " + rd.Reason
	}
	return r
}

// fallbackReason is the kind_reason part a usage fallback adds.
func fallbackReason(orig AgentKind) string { return orig.Display() + " is out of usage" }

// resolveName suffixes a daemon-generated name on a collision (§4) but refuses a
// user-typed one, because the UI previewed the exact kebab (P4 carry).
func (s *Store) resolveName(ctx context.Context, typed, generated string) (string, error) {
	taken := func(n string) bool {
		var one int
		return s.DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE name = ?`, n).Scan(&one) == nil
	}
	if typed != "" {
		n, err := ids.Kebab(typed)
		if err != nil {
			return "", &items.Error{Code: items.CodeBadRequest, Message: ids.ErrEmptyName.Error()}
		}
		if taken(n) {
			return "", &items.Error{Code: items.CodeConflict, Message: "This agent name is already in use."}
		}
		return n, nil
	}
	return ids.Unique(generated, taken), nil
}

// loginCommand is the §17.3 {cmd} for each agent.
func loginCommand(k AgentKind) string {
	switch k {
	case Claude:
		return "claude /login"
	case Codex:
		return "codex login"
	case Agy:
		return "agy login"
	case Cursor:
		return "cursor-agent login"
	}
	return string(k) + " login"
}

// Preflight is §11.4, in order. The copy is §17.3.
func (s *Store) Preflight(ctx context.Context, in PreflightInput) error {
	cfg, err := s.Settings.Get(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(cfg.EnabledAgents, in.Kind) {
		return fmt.Errorf("%s isn't installed on this Mac.", in.Kind.Display())
	}
	a, ok := s.Adapters[in.Kind]
	if !ok {
		return fmt.Errorf("%s isn't installed on this Mac.", in.Kind.Display())
	}
	if _, ok := a.Installed(ctx); !ok {
		return fmt.Errorf("%s isn't installed on this Mac.", in.Kind.Display())
	}
	// Fail safe (I5): AuthOK returns an error both for "signed out" and for output
	// it cannot parse, so a flag that changed under us blocks the spawn with a
	// message the user can act on, rather than launching an unauthenticated agent.
	if err := a.AuthOK(ctx); err != nil {
		return fmt.Errorf("%s isn't signed in. Run `%s` in a terminal.", in.Kind.Display(), loginCommand(in.Kind))
	}
	models, _, err := s.Catalog.ModelsFor(ctx, in.Kind)
	if err != nil {
		return err
	}
	m, found := catalog.Find(models, in.Model)
	if !found {
		return errors.New("Choose a model available for this agent.")
	}
	if in.Effort != "" && !m.SupportsEffort(in.Effort) {
		return fmt.Errorf("%s isn't available for %s; using the default.", in.Effort, in.Model)
	}
	if in.Role == RoleOrchestrator && !a.SuperpowersInstalled() {
		return fmt.Errorf("Install the superpowers plugin for %s to run orchestrators.", in.Kind.Display())
	}
	for _, p := range in.RepoPaths {
		if !repos.IsRepo(p) {
			return errors.New("Repository is unavailable. Choose another location.")
		}
		if err := s.Worktree.SigningOK(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// resolveRoleDefault fills in kind/model/effort for role from Settings, the
// same way StartOrchestrator always has: an explicit kind wins; else a kind
// implied by an explicit model; else the role's Settings default (if its
// agent is enabled); else the first enabled agent; then a model is picked
// from the resolved kind's catalog if still empty. ok is false only when
// none of that produced a kind at all (no role default, nothing enabled) --
// StartOrchestrator falls back to Fake in that case; StartSpike (which never
// had this resolution before, always sending kind "") treats it as a hard
// error instead of silently building an agent with a blank/wrong kind.
func (s *Store) resolveRoleDefault(ctx context.Context, role Role, kind AgentKind, model, effort string) (rKind AgentKind, rModel, rEffort string, ok bool) {
	cfg, _ := s.Settings.Get(ctx)
	if kind == "" && model != "" {
		if k, found := s.resolveAgentForModel(ctx, model); found {
			kind = k
		}
	}
	if kind == "" {
		if rd, has := cfg.Roles[role]; has && rd.Agent != "" && (len(cfg.EnabledAgents) == 0 || slices.Contains(cfg.EnabledAgents, rd.Agent)) {
			kind = rd.Agent
			if model == "" {
				if s.Catalog != nil {
					models, _, _ := s.Catalog.ModelsFor(ctx, kind)
					if _, found := catalog.Find(models, rd.Model); found {
						model = rd.Model
						if effort == "" {
							effort = rd.Effort
						}
					}
				} else {
					model = rd.Model
					if effort == "" {
						effort = rd.Effort
					}
				}
			}
		} else if len(cfg.EnabledAgents) > 0 {
			kind = cfg.EnabledAgents[0]
		}
	}
	if kind == "" {
		return "", model, effort, false
	}
	return kind, s.fillDefaultModel(ctx, kind, model), effort, true
}

// fillDefaultModel returns model unchanged if it's already set, otherwise the
// resolved kind's first catalog model. Shared by resolveRoleDefault's own
// ok=true path and by StartOrchestrator's further fallback to Fake when
// resolveRoleDefault can't resolve a kind at all -- that fallback picked a
// model the same way before the resolveRoleDefault extraction, and must
// keep doing so.
func (s *Store) fillDefaultModel(ctx context.Context, kind AgentKind, model string) string {
	if model != "" {
		return model
	}
	models, _, _ := s.Catalog.ModelsFor(ctx, kind)
	if len(models) > 0 {
		return models[0].ID
	}
	return model
}

func (s *Store) StartSpike(ctx context.Context, in SpikeInput) (string, Agent, bool, error) {
	name, err := s.resolveName(ctx, in.Name, in.Name)
	if err != nil {
		return "", Agent{}, false, err
	}

	if kind, model, effort, ok := s.resolveRoleDefault(ctx, RoleOrchestrator, in.Kind, in.Model, in.Effort); ok {
		in.Kind, in.Model, in.Effort = kind, model, effort
	} else {
		return "", Agent{}, false, errors.New("No agent kind given and no default is set for spikes; pass --agent.")
	}

	// ponytail: item creation and the agent-row INSERT below are separate
	// transactions, so an INSERT failure (or any error between here and
	// there) leaves this item as an orphaned draft with no agent, and a
	// retry creates a second item instead of reusing it. Upgrade path: wrap
	// both in one transaction.
	it, err := s.Items.Create(ctx, items.CreateInput{
		Type:           items.Spike,
		Title:          in.Name,
		SpikeIntent:    in.Intent,
		Brief:          in.Request,
		SuggestedRepos: in.Repos,
	}, items.User("board"))
	if err != nil {
		return "", Agent{}, false, err
	}

	// origKind is captured before resolveUsageFallback may substitute in.Kind,
	// so the agent.fallback_used notification below can report what the
	// caller actually asked for.
	origKind := in.Kind
	fbKind, fbModel, fbEffort, substituted, ferr := s.resolveUsageFallback(ctx, in.Kind, in.Model, in.Effort)
	in.Kind, in.Model, in.Effort = fbKind, fbModel, fbEffort

	advKind, advModel, advEffort, advMode := s.resolveAdvisor(ctx, in.Kind, in.Advisor)

	var preflightErr error
	if ferr != nil {
		// origKind is confirmed exhausted with no usable fallback: treat
		// this exactly like a Preflight refusal (spec Locked Decision 6)
		// rather than call the real Preflight, which would happily approve
		// origKind (it's installed and signed in -- just out of quota).
		preflightErr = ferr
	} else {
		preflightErr = s.Preflight(ctx, PreflightInput{
			Kind:      in.Kind,
			Model:     in.Model,
			Effort:    in.Effort,
			Role:      RoleOrchestrator,
			RepoPaths: in.RepoPaths,
		})
	}
	var rolesJSON any
	if len(in.Roles) > 0 {
		b, err := json.Marshal(in.Roles)
		if err == nil {
			rolesJSON = string(b)
		}
	}
	if preflightErr != nil {
		agentID := ids.New("agt")
		nowMs := s.now().UnixMilli()
		a := Agent{
			ID:             agentID,
			Name:           name,
			Kind:           in.Kind,
			Model:          in.Model,
			Effort:         in.Effort,
			Role:           RoleOrchestrator,
			ItemID:         it.ID,
			RootItemID:     it.ID,
			Brief:          in.Request,
			State:          AgentActive,
			PreflightError: preflightErr.Error(),
			RoleOverrides:  in.Roles,
			CreatedAt:      s.now(),
		}
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO agents
				(id, name, kind, model, effort, role, item_id, root_item_id, brief, state, preflight_error, created_at,
				 advisor_kind, advisor_model, advisor_effort, advisor_mode, role_overrides)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`,
				a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
				a.ItemID, a.RootItemID, a.Brief, string(a.State), a.PreflightError, nowMs,
				string(advKind), advModel, advEffort, advMode, rolesJSON)
			if err != nil {
				return err
			}
			return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
		}); err != nil {
			return "", Agent{}, false, err
		}
		if s.Notify != nil {
			_ = s.Notify.Raise(ctx, nil, NotifyInput{
				Kind:      "agent.preflight_failed",
				AgentName: a.Name,
				ItemKey:   it.Key,
				Args:      map[string]string{"reason": preflightErr.Error()},
			})
		}
		return it.Key, a, false, nil
	}

	briefText, err := RenderBrief(BriefInput{
		Key:       it.Key,
		Title:     it.Title,
		Name:      name,
		Role:      RoleOrchestrator,
		RootKey:   it.Key,
		Objective: in.Request,
	})
	if err != nil {
		return "", Agent{}, false, err
	}

	agentID := ids.New("agt")
	nowMs := s.now().UnixMilli()
	a := Agent{
		ID:            agentID,
		Name:          name,
		Kind:          in.Kind,
		Model:         in.Model,
		Effort:        in.Effort,
		Role:          RoleOrchestrator,
		ItemID:        it.ID,
		RootItemID:    it.ID,
		Brief:         briefText,
		State:         AgentActive,
		RoleOverrides: in.Roles,
		AdvisorKind:   string(advKind),
		AdvisorModel:  advModel,
		AdvisorEffort: advEffort,
		AdvisorMode:   advMode,
		CreatedAt:     s.now(),
	}

	payload, _ := json.Marshal(map[string]string{"brief": briefText, "item_key": it.Key})
	err = s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agents
			(id, name, kind, model, effort, role, item_id, root_item_id, brief, state, created_at,
			 advisor_kind, advisor_model, advisor_effort, advisor_mode, role_overrides)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`,
			a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
			a.ItemID, a.RootItemID, a.Brief, string(a.State), nowMs,
			string(advKind), advModel, advEffort, advMode, rolesJSON)
		if err != nil {
			return err
		}
		var seq int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages
			(id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, item_id, payload_json, state, created_at)
			VALUES (?, ?, 'assignment', 'immediate', 1, 'daemon', ?, ?, ?, ?, 'pending', ?)`,
			ids.New("msg"), seq, a.ID, it.ID, it.ID, string(payload), nowMs)
		if err != nil {
			return err
		}
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	})
	if err != nil {
		return "", Agent{}, false, err
	}

	ses, err := s.startSession(ctx, a, 1, 1, false, "", "")
	if err != nil {
		return "", Agent{}, false, err
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, ses, s.Adapters[a.Kind]); err != nil {
			s.logf("spawn: watchStartup %s: %v", a.Name, err)
		}
	})
	if substituted && s.Notify != nil {
		_ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.fallback_used", AgentName: a.Name, ItemKey: it.Key,
			Args: map[string]string{"name": a.Name, "agent": a.Kind.Display(), "from": origKind.Display()}})
	}

	return it.Key, a, false, nil
}

func (s *Store) StartOrchestrator(ctx context.Context, in OrchestratorInput) (Agent, bool, error) {
	it, err := s.Items.Get(ctx, in.ItemKey)
	if err != nil {
		return Agent{}, false, err
	}

	// Continuity: a stop, crash or session-cancel with an unfinished
	// assignment stays recoverable. When the exact same assignment already
	// has a logical orchestrator that is recoverable, restart it in place
	// (same canonical id, same name) instead of minting a suffixed second
	// agent. An active one still conflicts below; a terminal one (item
	// done/cancelled, user-cancelled, valid completed checkpoint) falls
	// through to the normal path. The match is the exact assignment
	// (item_id), never the root alone.
	if rec, ok, err := s.recoverableOrchestrator(ctx, it); err != nil {
		return Agent{}, false, err
	} else if ok {
		return s.restartOrchestratorInPlace(ctx, rec)
	}

	var existing int
	err = s.DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE root_item_id = ? AND role = 'orchestrator' AND state IN ('queued', 'active')`, it.RootID).Scan(&existing)
	if err == nil {
		return Agent{}, false, &items.Error{Code: items.CodeConflict, Message: "This item already has an orchestrator."}
	}

	if kind, model, effort, ok := s.resolveRoleDefault(ctx, RoleOrchestrator, in.Kind, in.Model, in.Effort); ok {
		in.Kind, in.Model, in.Effort = kind, model, effort
	} else {
		in.Kind = Fake
		in.Model = s.fillDefaultModel(ctx, in.Kind, in.Model)
	}

	origKind := in.Kind
	fbKind, fbModel, fbEffort, substituted, ferr := s.resolveUsageFallback(ctx, in.Kind, in.Model, in.Effort)
	if ferr != nil {
		return Agent{}, false, ferr
	}
	in.Kind, in.Model, in.Effort = fbKind, fbModel, fbEffort

	advKind, advModel, advEffort, advMode := s.resolveAdvisor(ctx, in.Kind, in.Advisor)

	if err := s.Preflight(ctx, PreflightInput{
		Kind:      in.Kind,
		Model:     in.Model,
		Effort:    in.Effort,
		Role:      RoleOrchestrator,
		RepoPaths: in.RepoPaths,
	}); err != nil {
		return Agent{}, false, err
	}

	defName, err := defaultName(RoleOrchestrator, it.Title)
	if err != nil {
		return Agent{}, false, err
	}
	name, err := s.resolveName(ctx, in.Name, defName)
	if err != nil {
		return Agent{}, false, err
	}

	briefText, err := RenderBrief(BriefInput{
		Key:       it.Key,
		Title:     it.Title,
		Name:      name,
		Role:      RoleOrchestrator,
		RootKey:   it.Key,
		Objective: it.Brief,
	})
	if err != nil {
		return Agent{}, false, err
	}

	var rolesJSON any
	if len(in.Roles) > 0 {
		b, err := json.Marshal(in.Roles)
		if err == nil {
			rolesJSON = string(b)
		}
	}

	agentID := ids.New("agt")
	nowMs := s.now().UnixMilli()
	a := Agent{
		ID:            agentID,
		Name:          name,
		Kind:          in.Kind,
		Model:         in.Model,
		Effort:        in.Effort,
		Role:          RoleOrchestrator,
		ItemID:        it.ID,
		RootItemID:    it.RootID,
		Brief:         briefText,
		RoleOverrides: in.Roles,
		AdvisorKind:   string(advKind),
		AdvisorModel:  advModel,
		AdvisorEffort: advEffort,
		AdvisorMode:   advMode,
		CreatedAt:     s.now(),
	}

	payload, _ := json.Marshal(map[string]string{"brief": briefText, "item_key": it.Key})
	var queued bool
	err = s.tx(ctx, func(tx *sql.Tx) error {
		admitted, err := s.Admit(ctx, tx, RoleOrchestrator, it.RootID)
		if err != nil {
			return err
		}
		if !admitted {
			queued = true
			a.State = AgentQueued
		} else {
			a.State = AgentActive
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agents
			(id, name, kind, model, effort, role, item_id, root_item_id, brief, state, created_at,
			 advisor_kind, advisor_model, advisor_effort, advisor_mode, role_overrides)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`,
			a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
			a.ItemID, a.RootItemID, a.Brief, string(a.State), nowMs,
			string(advKind), advModel, advEffort, advMode, rolesJSON)
		if err != nil {
			return err
		}
		var seq int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages
			(id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, item_id, payload_json, state, created_at)
			VALUES (?, ?, 'assignment', 'immediate', 1, 'daemon', ?, ?, ?, ?, 'pending', ?)`,
			ids.New("msg"), seq, a.ID, it.RootID, it.ID, string(payload), nowMs)
		if err != nil {
			return err
		}
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	})
	if err != nil {
		return Agent{}, false, err
	}
	if substituted && s.Notify != nil {
		_ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.fallback_used", AgentName: a.Name, ItemKey: it.Key,
			Args: map[string]string{"name": a.Name, "agent": a.Kind.Display(), "from": origKind.Display()}})
	}

	if queued {
		if s.Notify != nil {
			_ = s.Notify.Raise(ctx, nil, NotifyInput{
				Kind:      "agent.queued",
				AgentName: a.Name,
				ItemKey:   it.Key,
				Args:      map[string]string{"name": a.Name},
			})
		}
		return a, true, nil
	}

	ses, err := s.startSession(ctx, a, 1, 1, false, "", "")
	if err != nil {
		return Agent{}, false, err
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, ses, s.Adapters[a.Kind]); err != nil {
			s.logf("spawn: watchStartup %s: %v", a.Name, err)
		}
	})

	return a, false, nil
}

// recoverableOrchestrator finds the existing logical orchestrator for the
// exact assignment (item_id + orchestrator role) and reports whether it is
// recoverable: present but not live, with an unfinished assignment. An
// active orchestrator (queued, or active with a live latest session) is not
// recoverable -- the caller conflicts. Neither is a terminal one: an
// acknowledged agent is history, a done/cancelled item is terminal, and a
// valid completed checkpoint already finished the agent. A user-cancelled
// agent (auto_restart = 0) stays recoverable: Cancel keeps its identity, and
// auto_restart only gates restarts the daemon drives on its own, never an
// explicit Start.
func (s *Store) recoverableOrchestrator(ctx context.Context, it items.Item) (Agent, bool, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM agents WHERE item_id = ? AND role = 'orchestrator'
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, it.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	if err != nil {
		return Agent{}, false, err
	}
	a, err := s.agentByID(ctx, id)
	if err != nil {
		return Agent{}, false, err
	}
	if a.State == AgentQueued {
		return Agent{}, false, nil
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return Agent{}, false, err
	}
	if a.State == AgentActive && ses.State.Live() {
		return Agent{}, false, nil
	}
	if a.State == AgentAcknowledged {
		return Agent{}, false, nil
	}
	if it.Status == items.Done || it.Status == items.Cancelled {
		return Agent{}, false, nil
	}
	if kind, has, err := s.terminalCheckpointKind(ctx, a.ID, a.ItemID, ses.Attempt); err != nil {
		return Agent{}, false, err
	} else if has && kind == CompletedCkp {
		return Agent{}, false, nil
	}
	return a, true, nil
}

// restartOrchestratorInPlace restarts a recoverable orchestrator on its own
// row through the replacement coordinator (mode recover): same id, same
// name (resolveName is never consulted, so no suffix), a durable operation,
// the recovery kickoff and bundle, admission for a finished row, and the
// in-flight guard. The request re-enables auto_restart, so a parked
// operation is resumed by the reconciler. queued reports an operation
// still waiting (for the old pane to die, or for an admission slot).
func (s *Store) restartOrchestratorInPlace(ctx context.Context, a Agent) (Agent, bool, error) {
	if err := s.refuseIfOperationInFlight(ctx, a.ID); err != nil {
		return Agent{}, false, err
	}
	op, err := s.RequestReplacement(ctx, a.ID, ModeRecover, "", "")
	if err != nil {
		return Agent{}, false, err
	}
	if op.Phase == PhaseBlocked {
		return Agent{}, false, &items.Error{Code: items.CodeConflict,
			Message: fmt.Sprintf("Recovery %s is blocked: %s", op.ID, op.Error)}
	}
	out, err := s.agentByID(ctx, a.ID)
	if err != nil {
		return Agent{}, false, err
	}
	return out, op.Phase != PhaseSucceeded, nil
}

// roleDefaultKindModel fills an empty Kind/Model from the role's configured
// default (cfg.Roles[role]) rather than an arbitrary catalog entry. It falls
// back to the first enabled agent / first cataloged model only when the role
// has no configured default, or that default names a kind or model that
// isn't currently available (e.g. a retired or renamed model) -- the same
// safety net the old unconditional fallback provided.
func (s *Store) roleDefaultKindModel(ctx context.Context, role Role, kind AgentKind, model string) (AgentKind, string) {
	cfg, _ := s.Settings.Get(ctx)
	rd := cfg.Roles[role]
	if kind == "" && rd.Agent != "" && slices.Contains(cfg.EnabledAgents, rd.Agent) {
		kind = rd.Agent
	}
	if kind == "" {
		if len(cfg.EnabledAgents) > 0 {
			kind = cfg.EnabledAgents[0]
		} else {
			kind = Fake
		}
	}
	models, catalogDef, _ := s.Catalog.ModelsFor(ctx, kind)
	if model == "" && kind == rd.Agent && rd.Model != "" {
		if _, ok := catalog.Find(models, rd.Model); ok {
			model = rd.Model
		}
	}
	// catalogDef is each fetcher's own stated default model (Codex/Agy/Cursor;
	// Claude's fetcher never sets one), preferred over models[0] because Claude's
	// list orders aliases (fable, opus, sonnet, haiku) first -- models[0] there is
	// never "the default", just the alias-first entry.
	if model == "" && catalogDef != "" {
		model = catalogDef
	}
	if model == "" && len(models) > 0 {
		model = models[0].ID
	}
	return kind, model
}

// resolveAdvisor is Task 12's spawn-order step 1, advisor half (P2 plan line
// 6122): picks the advisor kind/model/effort/mode for a newly spawned agent
// of kind sessionKind. choice is the caller's swarm_spawn/API "advisor"
// field: nil means "use Settings" (the common case today, since no caller
// sets this yet), choice.None means the caller explicitly asked for no
// advisor. Returns four empty strings when there is no advisor.
func (s *Store) resolveAdvisor(ctx context.Context, sessionKind AgentKind, choice *AdvisorChoice) (kind AgentKind, model, effort, mode string) {
	if choice != nil && choice.None {
		return "", "", "", ""
	}
	if choice != nil {
		kind, model, effort = choice.Kind, choice.Model, choice.Effort
	} else {
		cfg, err := s.Settings.Get(ctx)
		if err != nil {
			return "", "", "", ""
		}
		rd := cfg.Roles[RoleAdvisor]
		if rd.Model == "" || rd.Model == settings.NoAdvisor {
			return "", "", "", ""
		}
		kind, model, effort = rd.Agent, rd.Model, rd.Effort
	}
	if model == "" {
		return "", "", "", ""
	}
	capable := false
	if models, _, err := s.Catalog.ModelsFor(ctx, kind); err == nil {
		if m, ok := catalog.Find(models, model); ok {
			capable = m.AdvisorCapable
		}
	}
	if s.Advisor != nil {
		mode = s.Advisor.Mode(sessionKind, kind, model, capable)
	}
	return kind, model, effort, mode
}

// resolveAgentForModel resolves which AgentKind supports modelID, checking
// enabled agents first, then falling back to all agent kinds.
func (s *Store) resolveAgentForModel(ctx context.Context, modelID string) (AgentKind, bool) {
	if s.Catalog == nil || modelID == "" {
		return "", false
	}
	var enabled []AgentKind
	if s.Settings != nil {
		if cfg, err := s.Settings.Get(ctx); err == nil {
			enabled = cfg.EnabledAgents
		}
	}
	for _, kind := range enabled {
		models, _, err := s.Catalog.ModelsFor(ctx, kind)
		if err != nil {
			continue
		}
		if _, ok := catalog.Find(models, modelID); ok {
			return kind, true
		}
	}
	all := append(slices.Clone(AgentKinds), Fake)
	for _, kind := range all {
		if slices.Contains(enabled, kind) {
			continue
		}
		models, _, err := s.Catalog.ModelsFor(ctx, kind)
		if err != nil {
			continue
		}
		if _, ok := catalog.Find(models, modelID); ok {
			return kind, true
		}
	}
	return "", false
}

func (s *Store) Spawn(ctx context.Context, in SpawnInput) (Agent, bool, error) {
	it, err := s.Items.Get(ctx, in.ItemKey)
	if err != nil {
		return Agent{}, false, err
	}

	// A genuine replay must not re-evaluate any of the checks below: by the
	// time it replays, the first call's own agent row already exists, so the
	// orchestrator-uniqueness check and resolveName's own-name conflict would
	// both spuriously fire against that row. This also skips a redundant
	// Preflight, which could itself fail on a replay for reasons that have
	// nothing to do with whether the spawn already succeeded (e.g. the CLI's
	// installed state changed since).
	var result spawnResult
	if hit, err := PeekIdempotent(ctx, s, in.SessionID, in.RequestID, &result); err != nil {
		return Agent{}, false, err
	} else if hit {
		return result.Agent, result.Queued, nil
	}

	if in.Role == "implementer" {
		in.Role = RoleCoder
	}

	if in.Role == RoleOrchestrator {
		var existing int
		err = s.DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE root_item_id = ? AND role = 'orchestrator' AND state IN ('queued', 'active')`, it.RootID).Scan(&existing)
		if err == nil {
			return Agent{}, false, &items.Error{Code: items.CodeConflict, Message: "This item already has an orchestrator."}
		}
	}

	// Gated roles (coder/debugger/mechanical) write completed checkpoints, and
	// only a Task consumes one (checkpoint.go's CompletedCkp switch moves it
	// to InReview; WriteCheckpoint now refuses one on anything else). Spawning
	// one on a Story/Epic/Bug/Spike leaves it with no item it can ever move,
	// and isDescendant refuses to let it redirect a later checkpoint to a
	// sibling task. Refuse at spawn time instead -- deterministically, before
	// any work starts -- and name the task keys to use.
	if slices.Contains(gatedRoles, in.Role) && it.Type != items.Task {
		children, cerr := s.Items.Children(ctx, it.Key)
		if cerr != nil {
			return Agent{}, false, cerr
		}
		var keys []string
		for _, c := range children {
			if c.Type == items.Task {
				keys = append(keys, c.Key)
			}
		}
		msg := fmt.Sprintf("Spawn %s on a task, not %s.", in.Role, it.Key)
		if len(keys) > 0 {
			msg = fmt.Sprintf("%s Its tasks: %s.", msg, strings.Join(keys, ", "))
		}
		return Agent{}, false, &items.Error{Code: items.CodeBadRequest, Message: msg}
	}

	var parentID string
	switch p := in.ParentAgentID.(type) {
	case string:
		parentID = p
	case items.Item:
		parentID = p.ID
	}
	var parentRoleOverrides map[Role]settings.RoleDefault
	var parentParam *string
	var parentName string
	if parentID != "" {
		if parent, err := s.agentByID(ctx, parentID); err == nil {
			parentRoleOverrides = parent.RoleOverrides
			parentParam = &parentID
			parentName = parent.Name
		}
	}

	cfg, _ := s.Settings.Get(ctx)
	// kindReason explains a kind/model that isn't the user's role default
	// (spec 2026-09-26 L1/L2); "" means it came from Settings.
	var kindReason string
	if in.Kind != "" || in.Model != "" || in.Effort != "" {
		kindReason = "User override"
		if in.OverrideReason != "" {
			kindReason += ": " + in.OverrideReason
		}
	}
	if in.Kind == "" && in.Model != "" {
		if k, ok := s.resolveAgentForModel(ctx, in.Model); ok {
			in.Kind = k
		}
	}
	// missingModel is a role default's model the catalog no longer lists;
	// the model then falls back to the kind's first one, visibly.
	var missingModel string
	applyRoleDefault := func(rd settings.RoleDefault) bool {
		if rd.Agent == "" || (len(cfg.EnabledAgents) > 0 && !slices.Contains(cfg.EnabledAgents, rd.Agent)) {
			return false
		}
		in.Kind = rd.Agent
		if in.Model == "" {
			if s.Catalog != nil {
				models, _, _ := s.Catalog.ModelsFor(ctx, in.Kind)
				if _, found := catalog.Find(models, rd.Model); found {
					in.Model = rd.Model
					if in.Effort == "" {
						in.Effort = rd.Effort
					}
				} else {
					missingModel = rd.Model
				}
			} else {
				in.Model = rd.Model
				if in.Effort == "" {
					in.Effort = rd.Effort
				}
			}
		}
		return true
	}
	if in.Kind == "" {
		var matched bool
		if rd, ok := parentRoleOverrides[in.Role]; ok {
			matched = applyRoleDefault(rd)
			if matched {
				kindReason = joinReason(kindReason, roleOverrideReason(parentName, rd))
			}
		}
		if !matched {
			if rd, ok := cfg.Roles[in.Role]; ok {
				matched = applyRoleDefault(rd)
			}
		}
		if !matched {
			if len(cfg.EnabledAgents) > 0 {
				in.Kind = cfg.EnabledAgents[0]
			} else {
				in.Kind = Fake
			}
			kindReason = joinReason(kindReason, fmt.Sprintf("Role default for %s unavailable; using %s", in.Role, in.Kind.Display()))
		}
	}
	if in.Model == "" {
		models, _, _ := s.Catalog.ModelsFor(ctx, in.Kind)
		if len(models) > 0 {
			in.Model = models[0].ID
			if missingModel != "" {
				kindReason = joinReason(kindReason, fmt.Sprintf("Role default model %s for %s unavailable; using %s", missingModel, in.Role, in.Model))
			}
		}
	}

	// A role default's Effort only ever reached in.Effort above when this
	// call resolved Kind/Model FROM that default (in.Kind/in.Model both
	// started empty). A caller that names its own explicit kind/model -- a
	// user-requested override (swarm_spawn's override_reason) -- skipped
	// applyRoleDefault entirely, so a role override's Effort
	// silently never applied even when the caller's kind/model happened to
	// match it and Effort was left blank (2026-09-24: the actual gap behind
	// "we need to be able to override effort" -- the override always stored
	// and read back Effort correctly; it just never got applied here).
	// Matching on the FINAL (Kind, Model) rather than reusing
	// applyRoleDefault keeps this safe: a caller whose explicit model
	// doesn't match the stored default's model must not inherit an effort
	// tuned for a different model.
	if in.Effort == "" {
		matchEffort := func(rd settings.RoleDefault, ok bool) bool {
			if !ok || rd.Agent != in.Kind || rd.Model != in.Model || rd.Effort == "" {
				return false
			}
			in.Effort = rd.Effort
			return true
		}
		rd, ok := parentRoleOverrides[in.Role]
		if matchEffort(rd, ok) {
			kindReason = joinReason(kindReason, roleOverrideReason(parentName, rd))
		} else {
			rd, ok = cfg.Roles[in.Role]
			matchEffort(rd, ok)
		}
	}

	origKind := in.Kind
	fbKind, fbModel, fbEffort, substituted, ferr := s.resolveUsageFallback(ctx, in.Kind, in.Model, in.Effort)
	if ferr != nil {
		return Agent{}, false, ferr
	}
	in.Kind, in.Model, in.Effort = fbKind, fbModel, fbEffort
	if substituted {
		kindReason = joinReason(kindReason, fallbackReason(origKind))
	}

	advKind, advModel, advEffort, advMode := s.resolveAdvisor(ctx, in.Kind, in.Advisor)

	if err := s.Preflight(ctx, PreflightInput{
		Kind:      in.Kind,
		Model:     in.Model,
		Effort:    in.Effort,
		Role:      in.Role,
		RepoPaths: in.RepoPaths,
	}); err != nil {
		return Agent{}, false, err
	}

	defName, err := defaultName(in.Role, it.Title)
	if err != nil {
		return Agent{}, false, err
	}
	name, err := s.resolveName(ctx, in.Name, defName)
	if err != nil {
		return Agent{}, false, err
	}

	in.Brief.Key = it.Key
	in.Brief.Title = it.Title
	in.Brief.Name = name
	in.Brief.Role = in.Role
	in.Brief.RootKey = it.RootKey
	in.Brief.ParentName = parentName
	briefText, err := RenderBrief(in.Brief)
	if err != nil {
		return Agent{}, false, err
	}

	agentID := ids.New("agt")
	nowMs := s.now().UnixMilli()
	a := Agent{
		ID:            agentID,
		Name:          name,
		Kind:          in.Kind,
		Model:         in.Model,
		Effort:        in.Effort,
		Role:          in.Role,
		ItemID:        it.ID,
		RootItemID:    it.RootID,
		ParentAgentID: parentID,
		Brief:         briefText,
		AdvisorKind:   string(advKind),
		AdvisorModel:  advModel,
		AdvisorEffort: advEffort,
		AdvisorMode:   advMode,
		KindReason:    kindReason,
		CreatedAt:     s.now(),
	}

	payload, _ := json.Marshal(map[string]string{"brief": briefText, "item_key": it.Key})
	if len(in.Worktrees) > 0 && s.Worktree != nil {
		wtIDs := make([]string, len(in.Worktrees))
		for i, wt := range in.Worktrees {
			wtIDs[i] = wt.WorktreeID
		}
		unlock := s.Worktree.LockWorktrees(wtIDs...)
		defer unlock()
	}
	ran, err := IdemTx(ctx, s, in.SessionID, in.RequestID, "swarm_spawn", &result, func(tx *sql.Tx) error {
		admitted, err := s.Admit(ctx, tx, in.Role, it.RootID)
		if err != nil {
			return err
		}
		if !admitted {
			a.State = AgentQueued
		} else {
			a.State = AgentActive
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agents
			(id, name, kind, model, effort, role, item_id, root_item_id, parent_agent_id, brief, state, created_at,
			 advisor_kind, advisor_model, advisor_effort, advisor_mode, role_overrides, kind_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''))`,
			a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
			a.ItemID, a.RootItemID, parentParam, a.Brief, string(a.State), nowMs,
			string(advKind), advModel, advEffort, advMode, nil, a.KindReason)
		if err != nil {
			return err
		}
		for _, wt := range in.Worktrees {
			if s.Worktree != nil {
				if err := s.Worktree.ShareTx(ctx, tx, wt.WorktreeID, a.ID, wt.Mode); err != nil {
					return err
				}
			} else {
				if wt.Mode != "rw" && wt.Mode != "ro" {
					return fmt.Errorf("worktree: unknown share mode %q", wt.Mode)
				}
				var state string
				if err := tx.QueryRowContext(ctx, `SELECT state FROM worktrees WHERE id = ?`, wt.WorktreeID).Scan(&state); err != nil {
					return err
				}
				if state != "active" {
					return fmt.Errorf("worktree: %s is not active", wt.WorktreeID)
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO worktree_reservations
					(worktree_id, agent_id, mode, created_at) VALUES (?, ?, ?, ?)
					ON CONFLICT(worktree_id, agent_id) DO UPDATE SET mode = excluded.mode, released_at = NULL`,
					wt.WorktreeID, a.ID, wt.Mode, nowMs)
				if err != nil {
					return err
				}
			}
		}

		var seq int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages
			(id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, item_id, payload_json, state, created_at)
			VALUES (?, ?, 'assignment', 'immediate', 1, 'daemon', ?, ?, ?, ?, 'pending', ?)`,
			ids.New("msg"), seq, a.ID, it.RootID, it.ID, string(payload), nowMs)
		if err != nil {
			return err
		}
		if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
			return err
		}
		result.Agent, result.Queued = a, !admitted
		return nil
	})
	if err != nil {
		return Agent{}, false, err
	}
	if ran && result.Agent.KindReason != "" {
		s.logf("spawn: %s on %s/%s: %s", result.Agent.Name, result.Agent.Kind, result.Agent.Model, result.Agent.KindReason)
	}
	// A replay must not re-raise agent.fallback_used: it already went out
	// on the genuine first call (same rule as agent.queued just below).
	if ran && substituted && s.Notify != nil {
		_ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.fallback_used", AgentName: result.Agent.Name, ItemKey: it.Key,
			Args: map[string]string{"name": result.Agent.Name, "agent": result.Agent.Kind.Display(), "from": origKind.Display()}})
	}

	if result.Queued {
		// A replay of an already-queued spawn must not re-raise agent.queued:
		// the notification already went out on the genuine first call.
		if ran && s.Notify != nil {
			_ = s.Notify.Raise(ctx, nil, NotifyInput{
				Kind:      "agent.queued",
				AgentName: result.Agent.Name,
				ItemKey:   it.Key,
				Args:      map[string]string{"name": result.Agent.Name},
			})
		}
		return result.Agent, true, nil
	}
	if !ran {
		// A replay of an already-started (non-queued) spawn: the session and
		// its tmux process already exist from the genuine first call, so they
		// must not be started a second time.
		return result.Agent, false, nil
	}

	ses, err := s.startSession(ctx, result.Agent, 1, 1, false, "", "")
	if err != nil {
		return Agent{}, false, err
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), result.Agent, ses, s.Adapters[result.Agent.Kind]); err != nil {
			s.logf("spawn: watchStartup %s: %v", result.Agent.Name, err)
		}
	})

	return result.Agent, false, nil
}

// spawnResult is Spawn's own IdemTx payload: the typed pair a cache hit
// unmarshals back into, so a replay can tell (without re-running Admit)
// whether the original call queued the agent or started it immediately.
type spawnResult struct {
	Agent  Agent
	Queued bool
}

// resolveLaunchModel converts a catalog base model id into the exact id an
// agent's CLI expects on --model, via CatalogModel.LaunchModel (P0 fix,
// docs/specs/2026-09-26-agy-launch-model.md): agy only accepts
// effort-suffixed ids (e.g. "gemini-3.8-flash-high"), never the base slug.
// It is a no-op for kinds whose catalog entries aren't slug-encoded
// (Claude, Codex, Fake, and cursor's per-effort entries today), and it
// passes model through unchanged whenever it isn't found in the catalog --
// a user-configured raw suffixed id must keep working. Every place that
// turns an Agent into a live launch (startSession, covering spawn, resume,
// retry and usage-fallback) and the wake call sites in wake.go must resolve
// through this one function.
func (s *Store) resolveLaunchModel(ctx context.Context, kind AgentKind, model, effort string) string {
	if s.Catalog == nil || model == "" {
		return model
	}
	models, _, err := s.Catalog.ModelsFor(ctx, kind)
	if err != nil {
		return model
	}
	// catalog.Find matches on Aliases too (e.g. Claude's "opus"/"sonnet"/
	// "fable"), and LaunchModel's contract for a flag-encoded hit is m.ID
	// (the exact --model value for a bare/family id), not the alias the
	// caller passed in -- rewriting it would pin a default Claude agent to
	// the cached catalog's dated snapshot instead of the rolling alias.
	// Only rewrite what actually needs it: a slug-encoded hit.
	m, ok := catalog.Find(models, model)
	if !ok || m.EffortEncoding != "slug" {
		return model
	}
	return m.LaunchModel(effort)
}

// succMode is "" for a brand-new assignment, or one of "handoff", "recovery"
// or "resume" when this session continues the same agent's prior work: the
// kickoff is then the section-4 SuccessorKickoff template (with its normative
// additions) instead of the fresh-assignment Kickoff.
func (s *Store) startSession(ctx context.Context, a Agent, attempt, generation int, resume bool, providerID, succMode string) (result Session, retErr error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM sessions WHERE agent_id = ?`, a.ID)
	if err == nil {
		for rows.Next() {
			var oldID string
			if err := rows.Scan(&oldID); err == nil {
				_ = os.Remove(filepath.Join(s.Home, "run", "tokens", oldID))
			}
		}
		rows.Close()
	}

	tokBytes := make([]byte, 32)
	if _, err := rand.Read(tokBytes); err != nil {
		return Session{}, err
	}
	token := hex.EncodeToString(tokBytes)
	tokenHashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(tokenHashBytes[:])

	sesID := ids.New("ses")
	// adapter/muse.go's museMCPEnv recomputes this exact formula (it needs
	// the token path before this function returns it in Spec/env): keep the
	// two in sync if this ever moves.
	tokPath := filepath.Join(s.Home, "run", "tokens", sesID)
	if err := os.MkdirAll(filepath.Dir(tokPath), 0o700); err != nil {
		return Session{}, err
	}
	if err := os.WriteFile(tokPath, []byte(token), 0o600); err != nil {
		return Session{}, err
	}

	cwd := filepath.Join(s.Home, "work", a.Name)
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return Session{}, err
	}

	ad, ok := s.Adapters[a.Kind]
	if !ok {
		return Session{}, fmt.Errorf("no adapter for %s", a.Kind)
	}
	_ = ad.TrustFolder(ctx, cwd)

	nowMs := s.now().UnixMilli()
	ses := Session{
		ID:         sesID,
		AgentID:    a.ID,
		Attempt:    attempt,
		Generation: generation,
		TokenHash:  tokenHash,
		TmuxName:   a.Name,
		Cwd:        cwd,
		CwdKind:    "neutral",
		State:      Spawning,
		StartedAt:  s.now(),
	}

	err = s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions
			(id, agent_id, attempt, generation, provider_session_id, token_hash, tmux_name, cwd, cwd_kind, state, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ses.ID, ses.AgentID, ses.Attempt, ses.Generation, providerID, ses.TokenHash,
			ses.TmuxName, ses.Cwd, ses.CwdKind, string(ses.State), nowMs)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	// Every error after the insert must retire this session. Launch, pre-run,
	// and stale-pane cleanup can fail before Tmux.Start; leaving their rows
	// spawning would make a later exhausted retry look live forever.
	defer func() {
		if retErr != nil {
			if err := s.failSession(context.WithoutCancel(ctx), a, ses, retErr.Error()); err != nil {
				s.logf("start session %s: record failure: %v", a.Name, err)
			}
		}
	}()

	// Epic-approval-lane decision 2: every start path (fresh, retry, drain,
	// resume, recovery, handoff successor) funnels through here, so this is
	// where open requests come back to the agent. Fail open: a relay error
	// never blocks a spawn.
	reminder := ""
	if n, err := s.resurfaceOpenRequests(ctx, a, ses.ID, true, time.Time{}); err != nil {
		s.logf("start session %s: resurface open requests: %v", a.Name, err)
	} else if n > 0 {
		reminder = " " + OpenRequestsReminder(n)
	}

	var itemKey, itemTitle, itemTypeStr string
	_ = s.DB.QueryRowContext(ctx, `SELECT key, title, type FROM items WHERE id = ?`, a.ItemID).Scan(&itemKey, &itemTitle, &itemTypeStr)
	itemType := items.Type(itemTypeStr)

	var kickoff string
	switch {
	case resume:
		kickoff = ResumeKickoff(a.Name, a.Role, itemType, itemKey, itemTitle)
	case succMode != "":
		kickoff = s.successorKickoff(ctx, a, itemType, itemKey, itemTitle, succMode)
	default:
		kickoff = Kickoff(a.Name, a.Role, itemType, itemKey, itemTitle)
	}
	kickoff += reminder

	// Settings read failure is unrelated to the spawn itself: fail open (same
	// philosophy as resolveUsageFallback in fallback.go) rather than block a
	// spawn on it. cfg's zero value already gives Instructions == "".
	cfg, _ := s.Settings.Get(ctx)

	spec := adapter.Spec{
		AgentName:         a.Name,
		AgentID:           a.ID,
		SessionID:         ses.ID,
		Token:             token,
		TokenFile:         tokPath,
		DaemonURL:         s.DaemonURL,
		Model:             s.resolveLaunchModel(ctx, a.Kind, a.Model, a.Effort),
		Effort:            a.Effort,
		Cwd:               cwd,
		ProviderSessionID: providerID,
		Kickoff:           kickoff,
		SettingsDir:       filepath.Join(s.Home, "run", "launch", ses.ID),
		Bin:               s.Bin,
		Instructions:      cfg.Instructions,
	}
	if a.AdvisorMode == "native" {
		spec.AdvisorModel = a.AdvisorModel
	}

	var l adapter.Launch
	if resume {
		l, err = ad.Resume(spec)
	} else {
		l, err = ad.Launch(spec)
	}
	if err != nil {
		return Session{}, err
	}

	var lastStdout string
	for _, cmd := range l.PreRun {
		if len(cmd) == 0 {
			continue
		}
		runner := s.Exec
		if runner == nil {
			runner = execx.Run
		}
		out, err := runner(ctx, cmd[0], cmd[1:]...)
		if err != nil {
			return Session{}, fmt.Errorf("prerun %v: %w", cmd, err)
		}
		lastStdout = strings.TrimSpace(string(out))
	}
	for i, arg := range l.Argv {
		if strings.Contains(arg, adapter.PreRunOutput) {
			l.Argv[i] = strings.ReplaceAll(arg, adapter.PreRunOutput, lastStdout)
		}
	}

	osEnv := s.OSEnv
	if osEnv == nil {
		osEnv = os.Getenv
	}
	env := s.baseEnv(osEnv)
	env["SWARM_URL"] = s.DaemonURL
	env["SWARM_SESSION"] = ses.ID
	env["SWARM_TOKEN_FILE"] = tokPath
	env["SWARM_AGENT_KIND"] = string(a.Kind)
	for k, v := range l.Env {
		env[k] = v
	}

	// P0-crash-1 (2026-09-19): a previous attempt's pane can still be alive
	// under this same tmux name (e.g. reconcile marked it crashed while it
	// was really just stuck at an unanswered prompt, or a retry raced a slow
	// spawn). Without this, tmux new-session fails with "duplicate session"
	// and this attempt is stillborn while the old, orphaned pane runs on
	// forever. Kill is idempotent, so this is a no-op on the common path
	// where nothing is there yet.
	if err := s.Tmux.Kill(ctx, a.Name); err != nil {
		return Session{}, err
	}
	if err := s.Tmux.Start(ctx, a.Name, cwd, env, l.Argv); err != nil {
		return Session{}, err
	}

	return ses, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// firstReadableLine returns the first line of pane (after stripping ANSI and
// trimming whitespace) that contains at least one letter or digit, so a
// notification's reason never becomes an ANSI-colored box-drawing rule
// (2026-09-26 incident: the notification body was an unreadable "────" line).
// It is capped at 120 runes plus "…". Returns "" if no such line exists.
func firstReadableLine(pane string) string {
	for _, l := range strings.Split(stripANSI(pane), "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		readable := false
		for _, r := range t {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				readable = true
				break
			}
		}
		if !readable {
			continue
		}
		if utf8.RuneCountInString(t) > 120 {
			runes := []rune(t)
			t = string(runes[:120]) + "…"
		}
		return t
	}
	return ""
}

func (s *Store) failSession(ctx context.Context, a Agent, ses Session, paneText string) error {
	if err := s.SetSessionState(ctx, ses.ID, Failed); err != nil {
		return err
	}
	firstLine := firstReadableLine(paneText)
	reason := "Couldn't start agent."
	if firstLine != "" {
		reason = firstLine + " " + reason
	}
	// The orchestrator otherwise never learns a child failed to start: it just
	// sees no ack and has no event to act on (unlike interrupted/crashed,
	// which already relay).
	txErr := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET failure_text = ? WHERE id = ?`, paneText, ses.ID); err != nil {
			return err
		}
		if err := s.notify(ctx, tx, NotifyInput{
			Kind:      "agent.preflight_failed",
			AgentName: a.Name,
			Args:      map[string]string{"reason": reason, "output": paneText},
		}); err != nil {
			return err
		}
		if a.ParentAgentID == "" {
			return nil
		}
		itemKey, err := s.itemKey(ctx, tx, a.ItemID)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"event": "failed", "agent": a.Name, "item": itemKey})
		if err != nil {
			return err
		}
		_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: a.ParentAgentID,
			RootItemID: a.RootItemID, ItemID: a.ItemID, Payload: payload})
		return err
	})
	if txErr != nil {
		return txErr
	}
	// A failed session's pane is otherwise left running forever (2026-09-26
	// incident: three Claude panes sat stuck on an unanswered trust dialog for
	// 2-5 days, because nothing ever killed them). The reaper (Task 7) also
	// catches this pane within one tick if this kill is somehow missed.
	if err := s.Tmux.Kill(ctx, ses.TmuxName); err != nil {
		s.logf("failSession: kill %s: %v", ses.TmuxName, err)
	}
	return nil
}

// stripANSI strips terminal escape sequences before a StartupDialogs regex
// sees the capture. P0-crash-1 (2026-09-19): Claude 2.1.278 renders a
// highlighted menu option (e.g. the dev-channels warning's "1. I am using
// this for local development") with a separate color escape around every
// word, so a plain multi-word Dialog.Match like claudeDev never matched the
// raw -e capture and the prompt sat unanswered forever. failSession's saved
// pane text still sees the raw capture (the first line shown to the user
// keeps its real shape); only dialog matching needs words contiguous.
// adapter.Idle's own Busy check strips ANSI too now (2026-09-19, separate
// live incident), via the same adapter.StripANSI this wraps.
func stripANSI(s string) string { return adapter.StripANSI(s) }

// startupStallTimeout and startupCeiling (2026-09-19, live incident): a fixed
// 30s deadline from spawn used to fail a session outright the moment real
// startup work (bun install, an advisor call, reading files) ran past 30s
// without the adapter ever reporting Idle -- even though the pane was never
// stuck, just busy. Confirmed live: a subtask doing real work got marked
// failed at exactly spawn+30s while its tmux pane stayed alive and kept
// producing new output for 10+ more minutes, and every retry this triggered
// killed and restarted the pane, discarding real progress (advisor
// consultation, a pending swarm_ask question) each time. startupStallTimeout
// now only measures INACTIVITY: it resets on every poll where the capture
// text actually changed, so a busy pane (its spinner alone changes the
// capture every tick) never trips it. startupCeiling is an independent,
// non-resetting outer bound so a pane that keeps producing distinct output
// forever without ever going idle still ends this background goroutine
// eventually instead of polling forever.
const startupStallTimeout = 30 * time.Second
const startupCeiling = 10 * time.Minute

// dialogRetryEvery, dialogMaxSends and dialogEscalateAfter govern how long a
// startup or live-session dialog is retried before it is handed to the user
// as a Needs-you row (P0-crash-1 follow-up, 2026-09-26 incident: a dialog's
// keys were sent once and never again, so a swallowed or too-early key press
// left the pane stuck for days with nothing surfaced).
const dialogRetryEvery = 5 * time.Second
const dialogMaxSends = 3
const dialogEscalateAfter = 15 * time.Second

// dialogState tracks one (session, dialog) pair's retry/escalation progress.
// In-memory like lastAliveAt: it is rebuilt from scratch on daemon restart,
// which just means one extra key press or a slightly later escalation, never
// a wrong one.
type dialogState struct {
	firstSeen time.Time
	lastSent  time.Time
	sends     int
	reqID     string
}

func (s *Store) watchStartup(ctx context.Context, a Agent, ses Session, ad adapter.Adapter) error {
	// setWatchStartupActive tells reconcile it can safely defer this
	// session's dialogs to this goroutine (covers every return path below);
	// once it isn't set any more -- this goroutine exited, or a daemon
	// restart dropped it without ever running this defer -- reconcile takes
	// the session's dialogs over itself instead of leaving it stuck (see
	// resolveAlive's ownedByWatchStartup and hasEscalatedPrompt's DB check,
	// which covers an escalation surviving the restart via its open row).
	s.setWatchStartupActive(ses.ID, true)
	defer s.setWatchStartupActive(ses.ID, false)
	st := map[int]*dialogState{}
	ceiling := s.Now().Add(startupCeiling)
	stallDeadline := s.Now().Add(startupStallTimeout)
	lastCapture := ""
	for s.Now().Before(ceiling) {
		if st, err := s.SessionState(ctx, ses.ID); err == nil && st != Spawning {
			return nil // a hook call already moved it to running
		}
		capture, err := s.Tmux.Capture(ctx, ses.TmuxName, 60)
		if err != nil {
			return err
		}
		if capture != lastCapture {
			lastCapture = capture
			stallDeadline = s.Now().Add(startupStallTimeout)
		}
		if !s.Now().Before(stallDeadline) {
			return s.failSession(ctx, a, ses, lastLines(capture, 40))
		}
		plain := stripANSI(capture)
		now := s.Now()
		anyEscalated := false
		for i, d := range ad.StartupDialogs() {
			if !d.Match.MatchString(plain) {
				if dst := st[i]; dst != nil {
					if dst.reqID != "" {
						if err := s.ResolveDialogPrompt(ctx, ses.ID, d.Title); err != nil {
							s.logf("startup: %s: resolve dialog %q: %v", a.Name, d.Title, err)
						}
					}
					delete(st, i)
				}
				continue
			}
			if d.Require != nil && !d.Require.MatchString(plain) {
				// The dialog is still on screen, just mid-render (the option
				// line hasn't drawn yet): keep any existing retry/escalation
				// state instead of dropping it, or a one-tick redraw would
				// reset the send budget and, worse, resolve an already
				// escalated row "via terminal" while the pane is still stuck.
				if dst := st[i]; dst != nil && dst.reqID != "" {
					anyEscalated = true
				}
				continue
			}
			if d.Fail {
				return s.failSession(ctx, a, ses, lastLines(capture, 40))
			}
			dst := st[i]
			if dst == nil {
				dst = &dialogState{firstSeen: now}
				st[i] = dst
			}
			if len(d.Keys) > 0 && dst.sends < dialogMaxSends &&
				(dst.sends == 0 || now.Sub(dst.lastSent) >= dialogRetryEvery) {
				if err := s.Tmux.Keys(ctx, ses.TmuxName, d.Keys...); err != nil {
					return err
				}
				dst.sends++
				dst.lastSent = now
				s.logf("startup: %s: sent %v for dialog %q (send %d of %d)", a.Name, d.Keys, d.Title, dst.sends, dialogMaxSends)
			}
			if dst.reqID == "" && now.Sub(dst.firstSeen) >= dialogEscalateAfter {
				req, _, err := s.OpenDialogPrompt(ctx, ses.ID, d.Title)
				if err != nil {
					s.logf("startup: %s: open dialog prompt %q: %v", a.Name, d.Title, err)
				} else {
					dst.reqID = req.ID
					// hasEscalatedPrompt's DB check (reconcile.go) picks this
					// open row up directly by title, so a Spawning child stuck
					// here is still suppressed from a no-ack relay to its
					// parent without watchStartup mirroring anything into
					// reconcile's own in-memory state.
					s.logf("startup: %s: dialog %q still visible after %s, opened %s", a.Name, d.Title, dialogEscalateAfter, req.ID)
				}
			}
			if dst.reqID != "" {
				anyEscalated = true
			}
		}
		if anyEscalated {
			// A human-blocked pane never fails or times out on its own: it waits
			// for the user to clear the dialog in the terminal.
			ceiling = now.Add(startupCeiling)
			stallDeadline = now.Add(startupStallTimeout)
		}
		// A session that clears its startup dialogs and launches straight into
		// continuous, genuinely busy work (2026-09-20, live incident: "s1-review-2"
		// showed "✻ Twisting… (6m 40s · ↓ 15.3k tokens)" for its whole run) never
		// once matches IdlePrompt, and its ever-changing spinner/token-count text
		// also defeats the stall check above -- so without this it would run the
		// full startupCeiling and then get false-failed despite doing real work.
		// ad.Busy() matching is just as much proof the startup phase is over as
		// ad.Idle() becoming true: it is the adapter's own signal for "actively
		// doing agent work", already trusted everywhere idle() uses it to rule out
		// a false idle read, so reusing it here to rule in "done starting" is the
		// same signal, not a new one.
		if ad.Idle(capture) || (ad.Busy() != nil && ad.Busy().MatchString(stripANSI(capture))) {
			for i, d := range ad.StartupDialogs() {
				if dst := st[i]; dst != nil && dst.reqID != "" {
					if err := s.ResolveDialogPrompt(ctx, ses.ID, d.Title); err != nil {
						s.logf("startup: %s: resolve dialog %q: %v", a.Name, d.Title, err)
					}
				}
			}
			s.discoverProviderSession(ctx, a, ses, ad)
			return s.SetSessionState(ctx, ses.ID, Running)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.after(500 * time.Millisecond):
		}
	}
	capture, _ := s.Tmux.Capture(ctx, ses.TmuxName, 60)
	return s.failSession(ctx, a, ses, lastLines(capture, 40))
}

// discoverProviderSession is the muse-shaped fallback for kinds with no hook
// surface to populate ProviderSessionID via ParseHook (spec: DiscoverSession
// on the Adapter interface). It is strictly best-effort: startup correctness
// must never depend on it, so every failure (no matching pane, no match in
// the adapter's own registry, a write error) is logged and swallowed rather
// than returned, and it never runs at all once a hook path has already
// populated ProviderSessionID.
func (s *Store) discoverProviderSession(ctx context.Context, a Agent, ses Session, ad adapter.Adapter) {
	if ses.ProviderSessionID != "" {
		return
	}
	panes, err := s.Tmux.Panes(ctx)
	if err != nil {
		s.logf("discoverProviderSession: Panes: %v", err)
		return
	}
	var pid int
	found := false
	for _, p := range panes {
		if p.Session == ses.TmuxName {
			pid, found = p.Pid, true
			break
		}
	}
	if !found {
		return
	}
	providerID, ok := ad.DiscoverSession(ctx, pid, ses.Cwd)
	if !ok {
		s.logf("discoverProviderSession: %s: pid %d matched no registry entry", a.Name, pid)
		return
	}
	if err := s.setProviderSessionID(ctx, ses.ID, providerID); err != nil {
		s.logf("discoverProviderSession: setProviderSessionID(%s): %v", a.Name, err)
	}
}

// setProviderSessionID is discoverProviderSession's write. It never
// overwrites an existing value: this path only ever fires for a session that
// started with an empty one (see discoverProviderSession's own guard), and
// the WHERE clause is a second, defensive belt against a race clobbering a
// value the hook path wrote concurrently.
func (s *Store) setProviderSessionID(ctx context.Context, sessionID, providerID string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE sessions SET provider_session_id = ? WHERE id = ? AND (provider_session_id IS NULL OR provider_session_id = '')`,
		providerID, sessionID)
	return err
}

// Cancel is swarm_control's cancel action. sessionID/requestID are I11's
// idempotency key, scoped to the calling orchestrator's own MCP session (not
// the cancelled agent's); requestID empty means "no idempotency, just run
// once."
func (s *Store) Cancel(ctx context.Context, name, sessionID, requestID string) (Agent, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Agent{}, err
	}
	// A genuine replay must not send a second interrupt/kill: check before
	// the side effect, not after (see PeekIdempotent's own doc comment).
	var out Agent
	if hit, err := PeekIdempotent(ctx, s, sessionID, requestID, &out); err != nil {
		return Agent{}, err
	} else if hit {
		return out, nil
	}
	// Continuity: user Cancel stops execution, disables auto-restart and
	// retains identity -- and wins over any pending replacement launch, so
	// in-flight operations are cancelled before anything is killed. The
	// operation driver lock makes that atomic against a driver mid-launch:
	// either the launch finished (and is killed below) or it never starts.
	defer lockAgentOperations(a.ID)()
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET auto_restart = 0 WHERE id = ?`, a.ID); err != nil {
			return err
		}
		return s.cancelAgentOperationsTx(ctx, tx, a.ID)
	}); err != nil {
		return Agent{}, err
	}
	// Root-finish spec decision 2: closing an agent whose root is already
	// Done records the work as completed, not cancelled. A paused or
	// interrupted session on a Done root is closed too; on any other root it
	// stays as it is, so a cancelled agent remains resumable.
	rootDone, err := s.rootIsDone(ctx, s.DB, a.RootItemID)
	if err != nil {
		return Agent{}, err
	}
	end := Cancelled
	if rootDone {
		end = Completed
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err == nil && ses.State.Live() {
		ad := s.Adapters[a.Kind]
		if ad != nil {
			_ = s.Tmux.Keys(ctx, ses.TmuxName, ad.InterruptKeys()...)
		}
		_ = s.Tmux.Kill(ctx, ses.TmuxName)
		_ = s.SetSessionState(ctx, ses.ID, end)
	} else if err == nil && rootDone && (ses.State == Paused || ses.State == Interrupted) {
		_ = s.SetSessionState(ctx, ses.ID, Completed)
	}

	nowMs := s.now().UnixMilli()
	nowTime := s.now()
	if _, err := IdemTx(ctx, s, sessionID, requestID, "swarm_control", &out, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`, nowMs, a.ID); err != nil {
			return err
		}
		_, _ = tx.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ? WHERE agent_id = ? AND released_at IS NULL`, nowMs, a.ID)
		if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
			return err
		}
		a.State = AgentFinished
		a.FinishedAt = &nowTime
		out = a
		return nil
	}); err != nil {
		return Agent{}, err
	}
	return out, nil
}

// OverridableRoles is kinds.SettingsRoles minus RoleAdvisor. RoleAdvisor is
// "Settings only; never an agent row" (kinds.go's own comment), and
// resolveAdvisor (above) reads cfg.Roles[RoleAdvisor] straight off the live
// global Settings -- it never consults a parent's RoleOverrides the way
// Spawn's own role-default resolution does at line 748. A RoleAdvisor entry
// in role_overrides would therefore be silently dead, so it's refused here
// rather than accepted and ignored (docs/specs/2026-09-23-orchestrator-role-overrides.md).
var OverridableRoles = []Role{RoleOrchestrator, RoleCoder, RoleReviewer, RoleUIReviewer, RoleResearcher, RoleDebugger, RoleMechanical, RoleDesigner}

func joinRoles(roles []Role) string {
	ss := make([]string, len(roles))
	for i, r := range roles {
		ss[i] = string(r)
	}
	return strings.Join(ss, ", ")
}

// SetRoleOverride is swarm_role_overrides' set/clear op. name is always
// resolved from the caller's own MCP session by the mcpserver handler, never
// a target parameter -- there is no way to reach another agent's row through
// this method, which is the whole "self only" scope proof (grepping
// RoleOverrides across internal/runtime shows only line 711/748, the owning
// orchestrator's own spawn resolution, ever reads the map this writes).
//
// rd == nil clears role back to falling through to the live global default
// (line 748's own lookup already treats a missing map key that way); rd !=
// nil sets/replaces it after validation against enabled agents and the real
// catalog, via the same settings.Store.ValidateDefault the user's own
// Settings page goes through -- so an orchestrator can't set a role to a
// disabled agent or an unlisted model any more than a Settings PUT could.
func (s *Store) SetRoleOverride(ctx context.Context, name string, role Role, rd *settings.RoleDefault, sessionID, requestID string) (Agent, error) {
	if !slices.Contains(OverridableRoles, role) {
		return Agent{}, fmt.Errorf("bad_request: role must be one of %s", joinRoles(OverridableRoles))
	}
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Agent{}, err
	}
	if rd != nil {
		cfg, err := s.Settings.Get(ctx)
		if err != nil {
			return Agent{}, err
		}
		old := a.RoleOverrides[role]
		if err := s.Settings.ValidateDefault(ctx, old, *rd, cfg.EnabledAgents, nil); err != nil {
			return Agent{}, fmt.Errorf("bad_request: %s", err.Error())
		}
	}

	var out Agent
	if _, err := IdemTx(ctx, s, sessionID, requestID, "swarm_role_overrides", &out, func(tx *sql.Tx) error {
		// Read-modify-write inside the transaction, not before it: every
		// connection opens with _txlock=immediate (internal/db/db.go:33-35),
		// so this always holds SQLite's write lock from BEGIN. Two concurrent
		// set/clear calls on different roles
		// from the same orchestrator must not clobber each other's map
		// entry the way a read taken outside the tx could.
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(role_overrides, '') FROM agents WHERE id = ?`, a.ID).Scan(&raw); err != nil {
			return err
		}
		overrides := map[Role]settings.RoleDefault{}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
				return err
			}
		}
		if rd == nil {
			delete(overrides, role)
		} else {
			overrides[role] = *rd
		}
		var next any
		if len(overrides) > 0 {
			b, err := json.Marshal(overrides)
			if err != nil {
				return err
			}
			next = string(b)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET role_overrides = ? WHERE id = ?`, next, a.ID); err != nil {
			return err
		}
		if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
			return err
		}
		a.RoleOverrides = overrides
		out = a
		return nil
	}); err != nil {
		return Agent{}, err
	}
	return out, nil
}

// retryableStates is §8.1's own swarm_control description: "retry starts a
// new attempt of a completed, failed, crashed or interrupted agent." Every
// other live or pausing state (queued, spawning, running, pause_requested,
// quiescing, stopping, paused) refuses, matching how Pause guards on
// !role.live() and Resume guards on ses.State being Paused or Interrupted.
var retryableStates = []SessionState{Completed, Failed, Crashed, Interrupted}

const notRetryable = "This agent isn't in a state that can be retried."

// Retry is swarm_control's retry action. sessionID/requestID are I11's
// idempotency key, scoped to the calling orchestrator's own MCP session (not
// the retried agent's); requestID empty means "no idempotency, just run
// once."
func (s *Store) Retry(ctx context.Context, name, note, sessionID, requestID string) (Agent, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Agent{}, err
	}
	// A genuine replay must not re-evaluate the state guard below: by the
	// time it replays, the retried session has moved on (that's the whole
	// point), so it would spuriously fail here. Must not send a second note,
	// flip the agent active a second time, or start a second session either
	// (see PeekIdempotent's own doc comment).
	var out Agent
	if hit, err := PeekIdempotent(ctx, s, sessionID, requestID, &out); err != nil {
		return Agent{}, err
	} else if hit {
		return out, nil
	}
	// Batch 3: Retry consults the replacement coordinator before the state
	// guard, so a refused retry names the operation it would race.
	if err := s.refuseIfOperationInFlight(ctx, a.ID); err != nil {
		return Agent{}, err
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return Agent{}, err
	}
	if ses.State == Stopping {
		// A stop is still in flight: persist the retry as a durable queued
		// recover intent instead of failing or doubling the launch. The
		// next operation resume executes it once the session settles.
		return s.queueRetryIntent(ctx, a, ses, note, sessionID, requestID)
	}
	if !slices.Contains(retryableStates, ses.State) {
		return Agent{}, &items.Error{Code: items.CodeConflict, Message: notRetryable}
	}

	// origKind is captured before resolveUsageFallback may substitute a.Kind,
	// so the agent.fallback_used notification below can report what was
	// actually configured.
	origKind := a.Kind
	fbKind, fbModel, fbEffort, substituted, ferr := s.resolveUsageFallback(ctx, a.Kind, a.Model, a.Effort)
	if ferr != nil {
		return Agent{}, ferr
	}
	if substituted {
		// Retry never re-validates the *original* kind -- it trusts the
		// agent row was already vetted at spawn time -- but a freshly
		// substituted kind has never been Preflighted, so it must be here,
		// or a broken substitute (not installed, not signed in) would spawn
		// a session doomed to fail instead of surfacing a clear refusal.
		if err := s.Preflight(ctx, PreflightInput{Kind: fbKind, Model: fbModel, Effort: fbEffort, Role: a.Role}); err != nil {
			return Agent{}, err
		}
		// The kind swap above can flip whether "native" advisor mode still
		// applies (it's Claude-only): re-resolve with the agent's existing
		// advisor kind/model/effort as an explicit choice, so mode gets
		// recomputed for fbKind instead of surviving stale from spawn time.
		advKind, advModel, advEffort, advMode := s.resolveAdvisor(ctx, fbKind,
			&AdvisorChoice{Kind: AgentKind(a.AdvisorKind), Model: a.AdvisorModel, Effort: a.AdvisorEffort})
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE agents SET kind = ?, model = ?, effort = ?,
				advisor_kind = NULLIF(?, ''), advisor_model = NULLIF(?, ''), advisor_effort = NULLIF(?, ''), advisor_mode = NULLIF(?, ''),
				kind_reason = ?
				WHERE id = ?`,
				string(fbKind), fbModel, fbEffort, string(advKind), advModel, advEffort, advMode,
				joinReason(a.KindReason, fallbackReason(origKind)), a.ID)
			return err
		}); err != nil {
			return Agent{}, err
		}
		a.Kind, a.Model, a.Effort = fbKind, fbModel, fbEffort
		a.KindReason = joinReason(a.KindReason, fallbackReason(origKind))
		a.AdvisorKind, a.AdvisorModel, a.AdvisorEffort, a.AdvisorMode = string(advKind), advModel, advEffort, advMode
	}

	if note != "" {
		// deliverNote (workflow.go, P9 fix round 2 finding 9: deduped with
		// what used to be a second copy of this exact INSERT here).
		if err := s.deliverNote(ctx, a.ID, note); err != nil {
			s.logf("retry %s: deliver note: %v", a.Name, err)
		}
	}

	if a.State != AgentActive {
		_ = s.tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'active' WHERE id = ?`, a.ID)
			return err
		})
		a.State = AgentActive
	}

	nextAttempt := ses.Attempt + 1
	nextGeneration := ses.Generation + 1
	newSes, err := s.startSession(ctx, a, nextAttempt, nextGeneration, false, "", "")
	if err != nil {
		return Agent{}, err
	}
	if _, err := IdemTx(ctx, s, sessionID, requestID, "swarm_control", &out, func(tx *sql.Tx) error {
		if err := s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID); err != nil {
			return err
		}
		out = a
		return nil
	}); err != nil {
		return Agent{}, err
	}
	if substituted && s.Notify != nil {
		var itemKey string
		_ = s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, out.ItemID).Scan(&itemKey)
		_ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.fallback_used", AgentName: out.Name, ItemKey: itemKey,
			Args: map[string]string{"name": out.Name, "agent": out.Kind.Display(), "from": origKind.Display()}})
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), out, newSes, s.Adapters[out.Kind]); err != nil {
			s.logf("spawn: watchStartup %s: %v", out.Name, err)
		}
	})

	return out, nil
}

func (s *Store) Ack(ctx context.Context, name string) error {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'acknowledged' WHERE name = ?`, name); err != nil {
			return err
		}
		return s.publishAgentChanged(ctx, tx, a.Name, a.RootItemID)
	})
}

func (s *Store) Terminal(ctx context.Context, name string) (string, string, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return "", "", err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		_, err := s.Events.Append(ctx, tx, events.TerminalOpen, map[string]string{"name": a.Name, "tmux": a.Name})
		return err
	})
	if err != nil {
		return "", "", err
	}
	return a.Name, "menubar", nil
}

func (s *Store) TerminalOpened(ctx context.Context, name, by string) error {
	return nil
}

func (s *Store) SessionState(ctx context.Context, sessionID string) (SessionState, error) {
	var st string
	err := s.DB.QueryRowContext(ctx, `SELECT state FROM sessions WHERE id = ?`, sessionID).Scan(&st)
	return SessionState(st), err
}

func (s *Store) SetSessionState(ctx context.Context, sessionID string, to SessionState) error {
	var endedAt any
	if to == Completed || to == Failed || to == Crashed || to == Cancelled || to == Interrupted {
		endedAt = s.now().UnixMilli()
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if endedAt != nil {
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET state = ?, ended_at = ? WHERE id = ?`, string(to), endedAt, sessionID)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET state = ? WHERE id = ?`, string(to), sessionID)
		return err
	})
}

func (s *Store) LatestSession(ctx context.Context, agentID string) (Session, error) {
	var ses Session
	var st string
	var waiting int
	var pauseRoot int
	var needsCompaction int
	var lastSeen, lastWake, started, ended, pauseDeadline sql.NullInt64
	var exitCode sql.NullInt64
	var failureText sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT
		id, agent_id, attempt, generation, COALESCE(provider_session_id, ''), token_hash, tmux_name,
		cwd, cwd_kind, state, waiting, COALESCE(pause_scope, ''), pause_root, pause_deadline_at, stop_blocks,
		needs_compaction_notice, last_seen_at, last_wake_at, exit_code, failure_text, started_at, ended_at
		FROM sessions WHERE agent_id = ? ORDER BY generation DESC, attempt DESC LIMIT 1`, agentID).Scan(
		&ses.ID, &ses.AgentID, &ses.Attempt, &ses.Generation, &ses.ProviderSessionID, &ses.TokenHash,
		&ses.TmuxName, &ses.Cwd, &ses.CwdKind, &st, &waiting, &ses.PauseScope, &pauseRoot, &pauseDeadline,
		&ses.StopBlocks, &needsCompaction, &lastSeen, &lastWake, &exitCode, &failureText, &started, &ended,
	)
	if err != nil {
		return ses, err
	}
	ses.State = SessionState(st)
	ses.PauseRoot = pauseRoot != 0
	ses.Waiting = (waiting != 0)
	ses.NeedsCompactionNotice = (needsCompaction != 0)
	if pauseDeadline.Valid {
		t := db.FromMillis(pauseDeadline.Int64)
		ses.PauseDeadlineAt = &t
	}
	if lastSeen.Valid {
		t := db.FromMillis(lastSeen.Int64)
		ses.LastSeenAt = &t
	}
	if lastWake.Valid {
		t := db.FromMillis(lastWake.Int64)
		ses.LastWakeAt = &t
	}
	if exitCode.Valid {
		c := int(exitCode.Int64)
		ses.ExitCode = &c
	}
	if failureText.Valid {
		ses.FailureText = &failureText.String
	}
	if started.Valid {
		ses.StartedAt = db.FromMillis(started.Int64)
	}
	if ended.Valid {
		t := db.FromMillis(ended.Int64)
		ses.EndedAt = &t
	}
	return ses, nil
}

func (s *Store) SessionByToken(ctx context.Context, token string) (Session, error) {
	h := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(h[:])

	var ses Session
	var st string
	var waiting int
	var pauseRoot int
	var needsCompaction int
	var lastSeen, lastWake, started, ended, pauseDeadline sql.NullInt64
	var exitCode sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT
		id, agent_id, attempt, generation, COALESCE(provider_session_id, ''), token_hash, tmux_name,
		cwd, cwd_kind, state, waiting, COALESCE(pause_scope, ''), pause_root, pause_deadline_at, stop_blocks,
		needs_compaction_notice, last_seen_at, last_wake_at, exit_code, started_at, ended_at
		FROM sessions WHERE token_hash = ?`, hash).Scan(
		&ses.ID, &ses.AgentID, &ses.Attempt, &ses.Generation, &ses.ProviderSessionID, &ses.TokenHash,
		&ses.TmuxName, &ses.Cwd, &ses.CwdKind, &st, &waiting, &ses.PauseScope, &pauseRoot, &pauseDeadline,
		&ses.StopBlocks, &needsCompaction, &lastSeen, &lastWake, &exitCode, &started, &ended,
	)
	if err != nil {
		return ses, err
	}
	ses.State = SessionState(st)
	ses.PauseRoot = pauseRoot != 0
	ses.Waiting = (waiting != 0)
	ses.NeedsCompactionNotice = (needsCompaction != 0)
	if pauseDeadline.Valid {
		t := db.FromMillis(pauseDeadline.Int64)
		ses.PauseDeadlineAt = &t
	}
	if lastSeen.Valid {
		t := db.FromMillis(lastSeen.Int64)
		ses.LastSeenAt = &t
	}
	if lastWake.Valid {
		t := db.FromMillis(lastWake.Int64)
		ses.LastWakeAt = &t
	}
	if exitCode.Valid {
		c := int(exitCode.Int64)
		ses.ExitCode = &c
	}
	if started.Valid {
		ses.StartedAt = db.FromMillis(started.Int64)
	}
	if ended.Valid {
		t := db.FromMillis(ended.Int64)
		ses.EndedAt = &t
	}
	return ses, nil
}

func scanAgent(row *sql.Row) (Agent, error) {
	var a Agent
	var kind, role, state, roleOverrides string
	var created int64
	var finished sql.NullInt64
	err := row.Scan(
		&a.ID, &a.Name, &kind, &a.Model, &a.Effort, &role,
		&a.ItemID, &a.RootItemID, &a.ParentAgentID,
		&a.AdvisorKind, &a.AdvisorModel, &a.AdvisorEffort, &a.AdvisorMode,
		&a.Brief, &state, &a.PreflightError, &created, &finished,
		&roleOverrides, &a.KindReason,
	)
	if err != nil {
		return a, err
	}
	a.Kind = AgentKind(kind)
	a.Role = Role(role)
	a.State = AgentState(state)
	a.CreatedAt = db.FromMillis(created)
	if finished.Valid {
		t := db.FromMillis(finished.Int64)
		a.FinishedAt = &t
	}
	if roleOverrides != "" {
		_ = json.Unmarshal([]byte(roleOverrides), &a.RoleOverrides)
	}
	return a, nil
}

func (s *Store) Agent(ctx context.Context, name string) (Agent, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT
		id, name, kind, model, COALESCE(effort, ''), role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), COALESCE(advisor_kind, ''), COALESCE(advisor_model, ''),
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state,
		COALESCE(preflight_error, ''), created_at, finished_at,
		COALESCE(role_overrides, ''), COALESCE(kind_reason, '')
		FROM agents WHERE name = ?`, name)
	return scanAgent(row)
}

func (s *Store) agentByID(ctx context.Context, id string) (Agent, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT
		id, name, kind, model, COALESCE(effort, ''), role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), COALESCE(advisor_kind, ''), COALESCE(advisor_model, ''),
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state,
		COALESCE(preflight_error, ''), created_at, finished_at,
		COALESCE(role_overrides, ''), COALESCE(kind_reason, '')
		FROM agents WHERE id = ?`, id)
	return scanAgent(row)
}

func (s *Store) AgentByID(ctx context.Context, id string) (Agent, error) {
	return s.agentByID(ctx, id)
}

func (s *Store) AgentTree(ctx context.Context, rootItemKey string) ([]Agent, error) {
	it, err := s.Items.Get(ctx, rootItemKey)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT
		id, name, kind, model, COALESCE(effort, ''), role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), COALESCE(advisor_kind, ''), COALESCE(advisor_model, ''),
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state,
		COALESCE(preflight_error, ''), created_at, finished_at,
		COALESCE(role_overrides, ''), COALESCE(kind_reason, '')
		FROM agents WHERE root_item_id = ? ORDER BY created_at`, it.RootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		var kind, role, state, roleOverrides string
		var created int64
		var finished sql.NullInt64
		if err := rows.Scan(
			&a.ID, &a.Name, &kind, &a.Model, &a.Effort, &role,
			&a.ItemID, &a.RootItemID, &a.ParentAgentID,
			&a.AdvisorKind, &a.AdvisorModel, &a.AdvisorEffort, &a.AdvisorMode,
			&a.Brief, &state, &a.PreflightError, &created, &finished,
			&roleOverrides, &a.KindReason,
		); err != nil {
			return nil, err
		}
		a.Kind = AgentKind(kind)
		a.Role = Role(role)
		a.State = AgentState(state)
		a.CreatedAt = db.FromMillis(created)
		if finished.Valid {
			t := db.FromMillis(finished.Int64)
			a.FinishedAt = &t
		}
		if roleOverrides != "" {
			_ = json.Unmarshal([]byte(roleOverrides), &a.RoleOverrides)
		}
		out = append(out, a)
	}
	return out, nil
}

// DeliverAdvice is advisor.Service.Deliver: an answered advice request becomes
// an `advice` message in the agent's inbox, so it arrives through swarm_sync
// like everything else rather than through the terminal (L6, P2 T35's P32).
func (s *Store) DeliverAdvice(ctx context.Context, sessionID string, adv Advice) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		// Message.Payload is json.RawMessage (model.go); the brief's snippet
		// assigns Advice directly, which doesn't type-check. advice_id is the
		// correlation key the inline swarm_advise result carries too (spec
		// line 1023); no consumer reads it yet, but it belongs on the wire.
		payload, err := json.Marshal(map[string]any{"advice_id": adv.ID, "question": adv.Question,
			"answer": adv.Answer, "error": adv.Error, "state": adv.State})
		if err != nil {
			return err
		}
		_, err = s.enqueue(ctx, tx, Message{Kind: "advice", Origin: "daemon",
			ToAgentID: a.ID, RootItemID: a.RootItemID, Payload: payload})
		return err
	})
}

// publishAgentChanged appends agent.changed (contracts §5: {name, root_key})
// inside tx, matching the events.Append-inside-the-mutation's-own-transaction
// pattern used elsewhere (e.g. ConfirmRepos). P3's board invalidates its
// agent list on this event, so every route that spawns, pauses, resumes,
// cancels, retries or acknowledges an agent needs to raise it.
func (s *Store) publishAgentChanged(ctx context.Context, tx *sql.Tx, agentName, rootItemID string) error {
	rootKey, err := s.itemKey(ctx, tx, rootItemID)
	if err != nil {
		return err
	}
	_, err = s.Events.Append(ctx, tx, events.AgentChanged, map[string]string{"name": agentName, "root_key": rootKey})
	return err
}

// OnWorktreeRetained is worktree.Service.OnRetained: §17.5's "Worktree kept"
// (P2 T35's P32). It runs inside the worktree service's own transaction, so it
// takes the tx — and that transaction is retain's own (internal/worktree/
// worktree.go), the one that sets state='retained': a Render failure here
// (a missing required placeholder) rolls the whole thing back, so the
// worktree never actually gets marked retained in the DB. The template
// (internal/notify/notify.go) needs {ROOT-KEY} and {detail}, not {path} —
// resolved via itemKey, the same helper publishAgentChanged uses above.
// ItemKey is set too: without it the dedup key degenerates to the bare
// string "worktree.retained::", collapsing two different worktrees retained
// within the same 30s window into one notification.
func (s *Store) OnWorktreeRetained(ctx context.Context, tx *sql.Tx, wt worktree.Worktree) error {
	detail := "uncommitted changes"
	if wt.RetainedReason == "unmerged" {
		detail = "unmerged commits"
	}
	rootKey, err := s.itemKey(ctx, tx, wt.RootItemID)
	if err != nil {
		return err
	}
	return s.notify(ctx, tx, NotifyInput{Kind: "worktree.retained", ItemKey: rootKey,
		Args: map[string]string{"ROOT-KEY": rootKey, "detail": detail}})
}

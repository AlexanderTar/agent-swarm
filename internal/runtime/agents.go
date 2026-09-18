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

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// AdvisorChoice is the caller-supplied "advisor" field on swarm_spawn,
// POST /api/spikes and POST /api/items/{key}/orchestrator (spec §7, §8.1).
// A nil *AdvisorChoice on the input struct it's embedded in means "use
// Settings"; None means the caller explicitly asked for no advisor.
type AdvisorChoice struct {
	None   bool
	Kind   AgentKind
	Model  string
	Effort string
}

type SpikeInput struct {
	Name      string
	Intent    string
	Kind      AgentKind
	Model     string
	Effort    string
	Advisor   *AdvisorChoice
	Request   string
	RepoPaths []string
	Repos     []string // suggested repo ids, shown back on the confirm_repos ask (D42)
}

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
}

type OrchestratorInput struct {
	ItemKey   string
	Kind      AgentKind
	Model     string
	Effort    string
	Advisor   *AdvisorChoice
	Name      string
	RepoPaths []string
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
	return slug + "-" + string(role), nil
}

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

func (s *Store) StartSpike(ctx context.Context, in SpikeInput) (string, Agent, bool, error) {
	name, err := s.resolveName(ctx, in.Name, in.Name)
	if err != nil {
		return "", Agent{}, false, err
	}

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

	advKind, advModel, advEffort, advMode := s.resolveAdvisor(ctx, in.Kind, in.Advisor)

	preflightErr := s.Preflight(ctx, PreflightInput{
		Kind:      in.Kind,
		Model:     in.Model,
		Effort:    in.Effort,
		Role:      RoleOrchestrator,
		RepoPaths: in.RepoPaths,
	})
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
			CreatedAt:      s.now(),
		}
		_ = s.tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO agents
				(id, name, kind, model, effort, role, item_id, root_item_id, brief, state, preflight_error, created_at,
				 advisor_kind, advisor_model, advisor_effort, advisor_mode)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`,
				a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
				a.ItemID, a.RootItemID, a.Brief, string(a.State), a.PreflightError, nowMs,
				string(advKind), advModel, advEffort, advMode)
			return err
		})
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
		ID:         agentID,
		Name:       name,
		Kind:       in.Kind,
		Model:      in.Model,
		Effort:     in.Effort,
		Role:       RoleOrchestrator,
		ItemID:     it.ID,
		RootItemID: it.ID,
		Brief:      briefText,
		State:      AgentActive,
		CreatedAt:  s.now(),
	}

	payload, _ := json.Marshal(map[string]string{"brief": briefText, "item_key": it.Key})
	err = s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agents
			(id, name, kind, model, effort, role, item_id, root_item_id, brief, state, created_at,
			 advisor_kind, advisor_model, advisor_effort, advisor_mode)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`,
			a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
			a.ItemID, a.RootItemID, a.Brief, string(a.State), nowMs,
			string(advKind), advModel, advEffort, advMode)
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
		return err
	})
	if err != nil {
		return "", Agent{}, false, err
	}

	ses, err := s.startSession(ctx, a, 1, 1, false, "")
	if err != nil {
		return "", Agent{}, false, err
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, ses, s.Adapters[a.Kind]); err != nil {
			s.logf("spawn: watchStartup %s: %v", a.Name, err)
		}
	})

	return it.Key, a, false, nil
}

func (s *Store) StartOrchestrator(ctx context.Context, in OrchestratorInput) (Agent, bool, error) {
	it, err := s.Items.Get(ctx, in.ItemKey)
	if err != nil {
		return Agent{}, false, err
	}

	var existing int
	err = s.DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE root_item_id = ? AND role = 'orchestrator' AND state IN ('queued', 'active')`, it.RootID).Scan(&existing)
	if err == nil {
		return Agent{}, false, &items.Error{Code: items.CodeConflict, Message: "This item already has an orchestrator."}
	}

	if in.Kind == "" {
		cfg, _ := s.Settings.Get(ctx)
		if len(cfg.EnabledAgents) > 0 {
			in.Kind = cfg.EnabledAgents[0]
		} else {
			in.Kind = Fake
		}
	}
	if in.Model == "" {
		models, _, _ := s.Catalog.ModelsFor(ctx, in.Kind)
		if len(models) > 0 {
			in.Model = models[0].ID
		}
	}

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

	agentID := ids.New("agt")
	nowMs := s.now().UnixMilli()
	a := Agent{
		ID:         agentID,
		Name:       name,
		Kind:       in.Kind,
		Model:      in.Model,
		Effort:     in.Effort,
		Role:       RoleOrchestrator,
		ItemID:     it.ID,
		RootItemID: it.RootID,
		Brief:      briefText,
		CreatedAt:  s.now(),
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
			 advisor_kind, advisor_model, advisor_effort, advisor_mode)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`,
			a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
			a.ItemID, a.RootItemID, a.Brief, string(a.State), nowMs,
			string(advKind), advModel, advEffort, advMode)
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
		return err
	})
	if err != nil {
		return Agent{}, false, err
	}

	if queued {
		if s.Notify != nil {
			_ = s.Notify.Raise(ctx, nil, NotifyInput{
				Kind:      "agent.queued",
				AgentName: a.Name,
				ItemKey:   it.Key,
			})
		}
		return a, true, nil
	}

	ses, err := s.startSession(ctx, a, 1, 1, false, "")
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

func (s *Store) Spawn(ctx context.Context, in SpawnInput) (Agent, bool, error) {
	it, err := s.Items.Get(ctx, in.ItemKey)
	if err != nil {
		return Agent{}, false, err
	}

	if in.Role == RoleOrchestrator {
		var existing int
		err = s.DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE root_item_id = ? AND role = 'orchestrator' AND state IN ('queued', 'active')`, it.RootID).Scan(&existing)
		if err == nil {
			return Agent{}, false, &items.Error{Code: items.CodeConflict, Message: "This item already has an orchestrator."}
		}
	}

	if in.Kind == "" {
		cfg, _ := s.Settings.Get(ctx)
		if len(cfg.EnabledAgents) > 0 {
			in.Kind = cfg.EnabledAgents[0]
		} else {
			in.Kind = Fake
		}
	}
	if in.Model == "" {
		models, _, _ := s.Catalog.ModelsFor(ctx, in.Kind)
		if len(models) > 0 {
			in.Model = models[0].ID
		}
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

	var parentID string
	switch p := in.ParentAgentID.(type) {
	case string:
		parentID = p
	case items.Item:
		parentID = p.ID
	}
	var parentParam *string
	if parentID != "" {
		var one int
		if s.DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, parentID).Scan(&one) == nil {
			parentParam = &parentID
		}
	}

	in.Brief.Key = it.Key
	in.Brief.Title = it.Title
	in.Brief.Name = name
	in.Brief.Role = in.Role
	in.Brief.RootKey = it.RootKey
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
		CreatedAt:     s.now(),
	}

	payload, _ := json.Marshal(map[string]string{"brief": briefText, "item_key": it.Key})
	var queued bool
	err = s.tx(ctx, func(tx *sql.Tx) error {
		admitted, err := s.Admit(ctx, tx, in.Role, it.RootID)
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
			(id, name, kind, model, effort, role, item_id, root_item_id, parent_agent_id, brief, state, created_at,
			 advisor_kind, advisor_model, advisor_effort, advisor_mode)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`,
			a.ID, a.Name, string(a.Kind), a.Model, a.Effort, string(a.Role),
			a.ItemID, a.RootItemID, parentParam, a.Brief, string(a.State), nowMs,
			string(advKind), advModel, advEffort, advMode)
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
		return err
	})
	if err != nil {
		return Agent{}, false, err
	}

	if queued {
		if s.Notify != nil {
			_ = s.Notify.Raise(ctx, nil, NotifyInput{
				Kind:      "agent.queued",
				AgentName: a.Name,
				ItemKey:   it.Key,
			})
		}
		return a, true, nil
	}

	ses, err := s.startSession(ctx, a, 1, 1, false, "")
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

func (s *Store) startSession(ctx context.Context, a Agent, attempt, generation int, resume bool, providerID string) (Session, error) {
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

	var itemKey, itemTitle string
	_ = s.DB.QueryRowContext(ctx, `SELECT key, title FROM items WHERE id = ?`, a.ItemID).Scan(&itemKey, &itemTitle)

	var kickoff string
	if resume {
		kickoff = ResumeKickoff(a.Name, a.Role, itemKey, itemTitle)
	} else {
		kickoff = Kickoff(a.Name, a.Role, itemKey, itemTitle)
	}

	spec := adapter.Spec{
		AgentName:         a.Name,
		SessionID:         ses.ID,
		Token:             token,
		DaemonURL:         s.DaemonURL,
		Model:             a.Model,
		Effort:            a.Effort,
		Cwd:               cwd,
		ProviderSessionID: providerID,
		Kickoff:           kickoff,
		SettingsDir:       filepath.Join(s.Home, "run", "launch", ses.ID),
		Bin:               s.Bin,
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

func (s *Store) failSession(ctx context.Context, a Agent, ses Session, paneText string) error {
	if err := s.SetSessionState(ctx, ses.ID, Failed); err != nil {
		return err
	}
	firstLine := ""
	for _, l := range strings.Split(paneText, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			firstLine = t
			break
		}
	}
	reason := "Couldn't start agent."
	if firstLine != "" {
		reason = firstLine + " " + reason
	}
	if s.Notify != nil {
		_ = s.Notify.Raise(ctx, nil, NotifyInput{
			Kind:      "agent.preflight_failed",
			AgentName: a.Name,
			Args:      map[string]string{"reason": reason, "output": paneText},
		})
	}
	return nil
}

func (s *Store) watchStartup(ctx context.Context, a Agent, ses Session, ad adapter.Adapter) error {
	answered := map[int]bool{}
	deadline := s.Now().Add(30 * time.Second)
	for s.Now().Before(deadline) {
		if st, err := s.SessionState(ctx, ses.ID); err == nil && st != Spawning {
			return nil // a hook call already moved it to running
		}
		capture, err := s.Tmux.Capture(ctx, ses.TmuxName, 60)
		if err != nil {
			return err
		}
		for i, d := range ad.StartupDialogs() {
			if answered[i] || !d.Match.MatchString(capture) {
				continue
			}
			if d.Require != nil && !d.Require.MatchString(capture) {
				continue // the option line is not drawn yet
			}
			if d.Fail {
				return s.failSession(ctx, a, ses, lastLines(capture, 40))
			}
			if err := s.Tmux.Keys(ctx, ses.TmuxName, d.Keys...); err != nil {
				return err
			}
			answered[i] = true
		}
		if ad.Idle(capture) {
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

func (s *Store) Cancel(ctx context.Context, name string) (Agent, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Agent{}, err
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err == nil && ses.State.Live() {
		ad := s.Adapters[a.Kind]
		if ad != nil {
			_ = s.Tmux.Keys(ctx, ses.TmuxName, ad.InterruptKeys()...)
		}
		_ = s.Tmux.Kill(ctx, ses.TmuxName)
		_ = s.SetSessionState(ctx, ses.ID, Cancelled)
	}

	nowMs := s.now().UnixMilli()
	nowTime := s.now()
	err = s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'finished', finished_at = ? WHERE id = ?`, nowMs, a.ID)
		if err != nil {
			return err
		}
		_, _ = tx.ExecContext(ctx, `UPDATE worktree_reservations SET released_at = ? WHERE agent_id = ? AND released_at IS NULL`, nowMs, a.ID)
		return nil
	})
	if err != nil {
		return Agent{}, err
	}
	a.State = AgentFinished
	a.FinishedAt = &nowTime
	return a, nil
}

func (s *Store) Retry(ctx context.Context, name, note string) (Agent, error) {
	a, err := s.Agent(ctx, name)
	if err != nil {
		return Agent{}, err
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		return Agent{}, err
	}

	if note != "" {
		payload, _ := json.Marshal(map[string]string{"note": note})
		nowMs := s.now().UnixMilli()
		_ = s.tx(ctx, func(tx *sql.Tx) error {
			var seq int64
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages`).Scan(&seq)
			_, err := tx.ExecContext(ctx, `INSERT INTO messages
				(id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id, item_id, payload_json, state, created_at)
				VALUES (?, ?, 'assignment_update', 'immediate', 1, 'daemon', ?, ?, ?, ?, 'pending', ?)`,
				ids.New("msg"), seq, a.ID, a.RootItemID, a.ItemID, string(payload), nowMs)
			return err
		})
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
	newSes, err := s.startSession(ctx, a, nextAttempt, nextGeneration, false, "")
	if err != nil {
		return Agent{}, err
	}
	s.go_(func() {
		if err := s.watchStartup(context.WithoutCancel(ctx), a, newSes, s.Adapters[a.Kind]); err != nil {
			s.logf("spawn: watchStartup %s: %v", a.Name, err)
		}
	})

	return a, nil
}

func (s *Store) Ack(ctx context.Context, name string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'acknowledged' WHERE name = ?`, name)
		return err
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
	var needsCompaction int
	var lastSeen, lastWake, started, ended, pauseDeadline sql.NullInt64
	var exitCode sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT
		id, agent_id, attempt, generation, COALESCE(provider_session_id, ''), token_hash, tmux_name,
		cwd, cwd_kind, state, waiting, COALESCE(pause_scope, ''), pause_deadline_at, stop_blocks,
		needs_compaction_notice, last_seen_at, last_wake_at, exit_code, started_at, ended_at
		FROM sessions WHERE agent_id = ? ORDER BY generation DESC, attempt DESC LIMIT 1`, agentID).Scan(
		&ses.ID, &ses.AgentID, &ses.Attempt, &ses.Generation, &ses.ProviderSessionID, &ses.TokenHash,
		&ses.TmuxName, &ses.Cwd, &ses.CwdKind, &st, &waiting, &ses.PauseScope, &pauseDeadline,
		&ses.StopBlocks, &needsCompaction, &lastSeen, &lastWake, &exitCode, &started, &ended,
	)
	if err != nil {
		return ses, err
	}
	ses.State = SessionState(st)
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

func (s *Store) SessionByToken(ctx context.Context, token string) (Session, error) {
	h := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(h[:])

	var ses Session
	var st string
	var waiting int
	var needsCompaction int
	var lastSeen, lastWake, started, ended, pauseDeadline sql.NullInt64
	var exitCode sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT
		id, agent_id, attempt, generation, COALESCE(provider_session_id, ''), token_hash, tmux_name,
		cwd, cwd_kind, state, waiting, COALESCE(pause_scope, ''), pause_deadline_at, stop_blocks,
		needs_compaction_notice, last_seen_at, last_wake_at, exit_code, started_at, ended_at
		FROM sessions WHERE token_hash = ?`, hash).Scan(
		&ses.ID, &ses.AgentID, &ses.Attempt, &ses.Generation, &ses.ProviderSessionID, &ses.TokenHash,
		&ses.TmuxName, &ses.Cwd, &ses.CwdKind, &st, &waiting, &ses.PauseScope, &pauseDeadline,
		&ses.StopBlocks, &needsCompaction, &lastSeen, &lastWake, &exitCode, &started, &ended,
	)
	if err != nil {
		return ses, err
	}
	ses.State = SessionState(st)
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
	var kind, role, state string
	var created int64
	var finished sql.NullInt64
	err := row.Scan(
		&a.ID, &a.Name, &kind, &a.Model, &a.Effort, &role,
		&a.ItemID, &a.RootItemID, &a.ParentAgentID,
		&a.AdvisorKind, &a.AdvisorModel, &a.AdvisorEffort, &a.AdvisorMode,
		&a.Brief, &state, &a.PreflightError, &created, &finished,
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
	return a, nil
}

func (s *Store) Agent(ctx context.Context, name string) (Agent, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT
		id, name, kind, model, COALESCE(effort, ''), role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), COALESCE(advisor_kind, ''), COALESCE(advisor_model, ''),
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state,
		COALESCE(preflight_error, ''), created_at, finished_at
		FROM agents WHERE name = ?`, name)
	return scanAgent(row)
}

func (s *Store) agentByID(ctx context.Context, id string) (Agent, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT
		id, name, kind, model, COALESCE(effort, ''), role, item_id, root_item_id,
		COALESCE(parent_agent_id, ''), COALESCE(advisor_kind, ''), COALESCE(advisor_model, ''),
		COALESCE(advisor_effort, ''), COALESCE(advisor_mode, ''), brief, state,
		COALESCE(preflight_error, ''), created_at, finished_at
		FROM agents WHERE id = ?`, id)
	return scanAgent(row)
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
		COALESCE(preflight_error, ''), created_at, finished_at
		FROM agents WHERE root_item_id = ? ORDER BY created_at`, it.RootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		var kind, role, state string
		var created int64
		var finished sql.NullInt64
		if err := rows.Scan(
			&a.ID, &a.Name, &kind, &a.Model, &a.Effort, &role,
			&a.ItemID, &a.RootItemID, &a.ParentAgentID,
			&a.AdvisorKind, &a.AdvisorModel, &a.AdvisorEffort, &a.AdvisorMode,
			&a.Brief, &state, &a.PreflightError, &created, &finished,
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
		out = append(out, a)
	}
	return out, nil
}

package items

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

type Store struct {
	DB     *db.DB
	Events *events.Store
	Now    func() time.Time

	// RequestPayload builds the request.* SSE payload (contracts R5). nil keeps the
	// interim {id, kind, item, state} form.
	RequestPayload func(ctx context.Context, tx *sql.Tx, id string) (any, error)
	// RequestOpened raises the §17.5 notification for a request the daemon opened.
	RequestOpened func(ctx context.Context, tx *sql.Tx, id string) error
	// DepUnblocked fires whenever an item reaches Done or Cancelled -- the two
	// statuses that stop it from blocking anything else (deriveStory and
	// reconcileRoot already treat this same pair as "finished"). doneItemID is
	// that item's id; the hook is responsible for waking whatever was waiting
	// on it (item_deps, blocked_by_id = doneItemID). nil does nothing, same as
	// the other hooks.
	DepUnblocked func(ctx context.Context, tx *sql.Tx, doneItemID string) error
	// StoryReadyForReview fires when every child task of a story is Done and the
	// story has an after_tasks workflow. It relays story_ready_for_review to the
	// orchestrator.
	StoryReadyForReview func(ctx context.Context, tx *sql.Tx, story Item) error
}

type CreateInput struct {
	Type           Type
	ParentKey      string
	Title          string
	Brief          string
	Acceptance     []string
	Status         Status // "" means Draft; only Draft or Ready
	Priority       *int   // nil means 2
	RoleHint       string
	TddExempt      string
	Workflow       *workflow.Spec // orchestrator/daemon only (B3); resolved and stored
	Steps          []string       // orchestrator/daemon only; either Steps or Units, never both
	Units          []Unit         // orchestrator/daemon only; at most 8
	Solo           string         // orchestrator/daemon only
	Verify         []string       // orchestrator/daemon only
	Repos          []string       // top-level: confirmed repo ids; children: hints (subset of the root's)
	SuggestedRepos []string
	SpikeIntent    string
	OriginSpikeID  string
	LegacyKey      string
	SortOrder      int
}

type Patch struct {
	Title      *string
	Brief      *string
	Acceptance *[]string
	Priority   *int
	TddExempt  *string
	Workflow   *workflow.Spec // orchestrator/daemon only; provided means "set to this"
	Steps      *[]string      // orchestrator/daemon only
	Units      *[]Unit        // orchestrator/daemon only
	Solo       *string        // orchestrator/daemon only
	Verify     *[]string      // orchestrator/daemon only
	Status     *Status
	Revision   int
}

var allowedParents = map[Type][]Type{Story: {Epic}, Task: {Story, Bug, Spike, Chore}}
var parentHint = map[Type]string{Story: "A story needs a parent epic.", Task: "A task needs a parent story, bug, spike or chore."}
var tddValues = []string{"docs", "config", "mechanical-rename", "spike-research"}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const itemCols = `i.id, i.key, i.type, COALESCE(i.parent_id, ''), COALESCE(p.key, ''), i.root_id, r.key,
 i.title, i.brief, i.acceptance_json, i.status, COALESCE(i.status_before_block, ''), i.priority,
 COALESCE(i.role_hint, ''), COALESCE(i.tdd_exempt, ''), i.confirmed_repos_json, i.repos_version,
 i.repo_hints_json, i.suggested_repos_json, COALESCE(i.spike_intent, ''), COALESCE(i.origin_spike_id, ''),
 COALESCE(i.legacy_key, ''), i.sort_order, i.revision, i.archived_at, i.created_at, i.updated_at,
 i.workflow_json, COALESCE(i.steps_json, '[]'), COALESCE(i.units_json, '[]'), COALESCE(i.solo, ''), COALESCE(i.verify_json, '[]')
 FROM items i LEFT JOIN items p ON p.id = i.parent_id JOIN items r ON r.id = i.root_id`

type scanner interface{ Scan(dest ...any) error }

func scanItem(sc scanner) (Item, error) {
	var it Item
	var acc, confirmed, hints, suggested string
	var archived sql.NullInt64
	var created, updated int64
	var workflowJSON sql.NullString
	var stepsRaw, unitsRaw, verifyRaw string
	err := sc.Scan(&it.ID, &it.Key, &it.Type, &it.ParentID, &it.ParentKey, &it.RootID, &it.RootKey,
		&it.Title, &it.Brief, &acc, &it.Status, &it.StatusBeforeBlock, &it.Priority,
		&it.RoleHint, &it.TddExempt, &confirmed, &it.ReposVersion,
		&hints, &suggested, &it.SpikeIntent, &it.OriginSpikeID,
		&it.LegacyKey, &it.SortOrder, &it.Revision, &archived, &created, &updated,
		&workflowJSON, &stepsRaw, &unitsRaw, &it.Solo, &verifyRaw)
	if err != nil {
		return it, err
	}
	if workflowJSON.Valid && workflowJSON.String != "" {
		var spec workflow.Spec
		if err := json.Unmarshal([]byte(workflowJSON.String), &spec); err != nil {
			return it, fmt.Errorf("items: %s workflow_json: %w", it.Key, err)
		}
		it.Workflow = &spec
	}
	if err := json.Unmarshal([]byte(stepsRaw), &it.Steps); err != nil {
		return it, fmt.Errorf("items: %s steps_json: %w", it.Key, err)
	}
	if err := json.Unmarshal([]byte(unitsRaw), &it.Units); err != nil {
		return it, fmt.Errorf("items: %s units_json: %w", it.Key, err)
	}
	if err := json.Unmarshal([]byte(verifyRaw), &it.Verify); err != nil {
		return it, fmt.Errorf("items: %s verify_json: %w", it.Key, err)
	}
	it.Steps, it.Verify = nonNil(it.Steps), nonNil(it.Verify)
	if it.Units == nil {
		it.Units = []Unit{}
	}
	reposCol, repos := "repo_hints_json", hints
	if it.ParentID == "" {
		reposCol, repos = "confirmed_repos_json", confirmed
	}
	for _, c := range []struct {
		name, raw string
		dst       *[]string
	}{{"acceptance_json", acc, &it.Acceptance}, {"suggested_repos_json", suggested, &it.SuggestedRepos}, {reposCol, repos, &it.Repos}} {
		if err := json.Unmarshal([]byte(c.raw), c.dst); err != nil {
			return it, fmt.Errorf("items: %s %s: %w", it.Key, c.name, err)
		}
	}
	it.Acceptance = nonNil(it.Acceptance)
	it.Repos = nonNil(it.Repos)
	it.SuggestedRepos = nonNil(it.SuggestedRepos)
	it.BlockedBy = []string{}
	if archived.Valid {
		at := db.FromMillis(archived.Int64)
		it.ArchivedAt = &at
	}
	it.CreatedAt, it.UpdatedAt = db.FromMillis(created), db.FromMillis(updated)
	return it, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func jsonList(s []string) string {
	b, _ := json.Marshal(nonNil(s))
	return string(b)
}

// deref and derefStr read a Patch pointer-to-slice/string field as its zero
// value when unset, for callers (like workflowFieldsPermitted) that only
// care about the value, not whether it was provided.
func deref[T any](p *[]T) []T {
	if p == nil {
		return nil
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func jsonUnits(u []Unit) string {
	if u == nil {
		u = []Unit{}
	}
	b, _ := json.Marshal(u)
	return string(b)
}

func workflowJSONString(w *workflow.Spec) string {
	if w == nil {
		return ""
	}
	b, _ := json.Marshal(w)
	return string(b)
}

// workflowLevel maps an item's Type to the workflow DSL level that decides
// which Spec fields are legal (spec B2's Level): stories get after_tasks,
// epics/bugs get integration, everything else (task, plus spike/chore --
// unspecified by the spec, since neither carries integration/after_tasks
// semantics -- default to the task shape) resolves template/steps.
func workflowLevel(t Type) workflow.Level {
	switch t {
	case Story:
		return workflow.LevelStory
	case Epic, Bug:
		return workflow.LevelRoot
	default:
		return workflow.LevelTask
	}
}

// resolveWorkflow validates spec against t's level and resolves it (template
// expansion, defaults, dropping the tdd gate when tddExempt), returning the
// resolved spec and its RunRole (for role_hint; "" if spec is nil or has no
// run step). nil in, nil out: an item with no workflow is untouched.
func resolveWorkflow(t Type, spec *workflow.Spec, tddExempt bool) (*workflow.Spec, string, error) {
	if spec == nil {
		return nil, "", nil
	}
	if err := workflow.Validate(workflowLevel(t), *spec); err != nil {
		return nil, "", errf(CodeBadRequest, "%s", err)
	}
	resolved, err := workflow.Resolve(*spec, tddExempt)
	if err != nil {
		return nil, "", errf(CodeBadRequest, "%s", err)
	}
	return &resolved, workflow.RunRole(resolved), nil
}

// validateStepsUnits is spec B3/C4: a task has either steps or units, never
// both, at most 8 units, and every unit has a title and at least one
// non-empty step (P7 fix round 1 finding #2).
func validateStepsUnits(identifier string, steps []string, units []Unit) error {
	if len(steps) > 0 && len(units) > 0 {
		return errf(CodeBadRequest, "Task %s has both steps and units; use one.", identifier)
	}
	if len(units) > 8 {
		return errf(CodeBadRequest, "Task %s has %d units (max 8).", identifier, len(units))
	}
	for i, u := range units {
		malformed := strings.TrimSpace(u.Title) == "" || len(u.Steps) == 0
		for _, st := range u.Steps {
			if strings.TrimSpace(st) == "" {
				malformed = true
			}
		}
		if malformed {
			return errf(CodeBadRequest, "Unit %d needs a title and at least one step.", i+1)
		}
	}
	return nil
}

// onlyTasksSet is P7 fix round 1 finding #1: steps/units/solo/verify are
// task-only fields (spec C1). A story or root may still carry its own level
// of Workflow (after_tasks / integration, validated by workflowLevel above)
// -- this only refuses the task-execution-script fields on a non-task item.
func onlyTasksSet(t Type, steps []string, units []Unit, solo string, verify []string) error {
	if t == Task {
		return nil
	}
	if len(steps) > 0 || len(units) > 0 || solo != "" || len(verify) > 0 {
		return errf(CodeBadRequest, "Only tasks can set steps, units, solo or verify.")
	}
	return nil
}

// workflowFieldsPermitted is this package's judgment call (spec copy for it
// wasn't given, unlike tdd_exempt's): only an orchestrator or the daemon
// (a plan materializing) may set workflow/steps/units/solo/verify, mirroring
// tdd_exempt's own permission rule exactly.
func workflowFieldsPermitted(by Actor, hasWorkflow bool, steps []string, units []Unit, verify []string, solo string) error {
	if !hasWorkflow && len(steps) == 0 && len(units) == 0 && solo == "" && len(verify) == 0 {
		return nil
	}
	if !by.isOrchestrator() && by.Kind != ActorDaemon {
		return errf(CodeBadRequest, "Only an orchestrator or a plan can set workflow, steps, units, solo or verify.")
	}
	return nil
}

func (s *Store) getTx(ctx context.Context, q querier, key string) (Item, error) {
	it, err := scanItem(q.QueryRowContext(ctx, `SELECT `+itemCols+` WHERE i.key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return it, errf(CodeNotFound, "No item %s.", key)
	}
	return it, err
}

// GetTx reads an item inside the caller's own write transaction (D48).
func (s *Store) GetTx(ctx context.Context, tx *sql.Tx, key string) (Item, error) {
	return s.getTx(ctx, tx, key)
}

func (s *Store) getByID(ctx context.Context, q querier, id string) (Item, error) {
	it, err := scanItem(q.QueryRowContext(ctx, `SELECT `+itemCols+` WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return it, errf(CodeNotFound, "No item %s.", id)
	}
	return it, err
}

// write runs fn in a transaction and wakes event subscribers after commit.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	err := s.DB.Tx(ctx, fn)
	if err == nil {
		s.Events.Notify()
	}
	return err
}

func (s *Store) changed(ctx context.Context, tx *sql.Tx, it Item) error {
	_, err := s.Events.Append(ctx, tx, events.ItemChanged, map[string]string{"key": it.Key, "root_key": it.RootKey})
	return err
}

func validateText(title, brief string) error {
	if n := utf8.RuneCountInString(title); n < 1 || n > 200 {
		return errf(CodeBadRequest, "Title must be 1–200 characters.")
	}
	return nil
}

func validPriority(p int) error {
	if p < 0 || p > 3 {
		return errf(CodeBadRequest, "Priority must be between 0 and 3.")
	}
	return nil
}

func (s *Store) Create(ctx context.Context, in CreateInput, by Actor) (Item, error) {
	var out Item
	err := s.write(ctx, func(tx *sql.Tx) (err error) {
		out, err = s.CreateTx(ctx, tx, in, by)
		return err
	})
	return out, err
}

func (s *Store) CreateTx(ctx context.Context, tx *sql.Tx, in CreateInput, by Actor) (Item, error) {
	in.Title = strings.TrimSpace(in.Title)
	switch in.Type {
	case Epic, Story, Task, Bug, Spike, Chore:
	default:
		return Item{}, errf(CodeBadRequest, "Unknown item type %q.", in.Type)
	}
	if err := validateText(in.Title, in.Brief); err != nil {
		return Item{}, err
	}
	prio := 2
	if in.Priority != nil {
		prio = *in.Priority
	}
	if err := validPriority(prio); err != nil {
		return Item{}, err
	}
	if in.Status == "" {
		in.Status = Draft
	}
	if in.Status != Draft && in.Status != Ready {
		return Item{}, errf(CodeBadRequest, "New items start as Draft or Ready.")
	}
	if in.Type == Spike && in.SpikeIntent != "feature" && in.SpikeIntent != "debug" && in.SpikeIntent != "chore" {
		return Item{}, errf(CodeBadRequest, "Spikes start with an intent. Use New spike.")
	}
	if in.Type != Spike && in.SpikeIntent != "" {
		return Item{}, errf(CodeBadRequest, "Only spikes have an intent.")
	}
	if in.TddExempt != "" {
		if !slices.Contains(tddValues, in.TddExempt) {
			return Item{}, errf(CodeBadRequest, "tdd_exempt must be one of docs, config, mechanical-rename, spike-research.")
		}
		if in.Type != Task {
			return Item{}, errf(CodeBadRequest, "Only tasks can be TDD-exempt.")
		}
		if !by.isOrchestrator() && by.Kind != ActorDaemon {
			return Item{}, errf(CodeBadRequest, "Only an orchestrator or a plan can set tdd_exempt.")
		}
	}
	if err := workflowFieldsPermitted(by, in.Workflow != nil, in.Steps, in.Units, in.Verify, in.Solo); err != nil {
		return Item{}, err
	}
	if err := onlyTasksSet(in.Type, in.Steps, in.Units, in.Solo, in.Verify); err != nil {
		return Item{}, err
	}
	if err := validateStepsUnits(in.Title, in.Steps, in.Units); err != nil {
		return Item{}, err
	}
	rw, runRole, werr := resolveWorkflow(in.Type, in.Workflow, in.TddExempt != "")
	if werr != nil {
		return Item{}, werr
	}
	if rw != nil {
		in.Workflow, in.RoleHint = rw, runRole
	}

	id := ids.New("itm")
	rootID, parentID := id, sql.NullString{}
	if in.ParentKey == "" {
		if hint, ok := parentHint[in.Type]; ok {
			return Item{}, errf(CodeBadRequest, "%s", hint)
		}
		// An orchestrator proposing a new root item (2026-09-26 top-level
		// items spec): always lands Draft (decision 2, refused rather than
		// silently downgraded), any repos passed are suggestions only, and
		// origin_spike_id records the caller's own root so the board's
		// existing "Started from KEY" rendering resolves it. The
		// top-level-only restriction (a child orchestrator may not propose)
		// is enforced by the caller in internal/mcpserver, which knows
		// ParentAgentID; this package only knows it's an orchestrator.
		if by.isOrchestrator() {
			if in.Status == Ready {
				return Item{}, errf(CodeBadRequest, "A proposed top-level item starts as Draft. The user starts it.")
			}
			in.Status = Draft
			if len(in.Repos) > 0 {
				in.SuggestedRepos = append(append([]string{}, in.SuggestedRepos...), in.Repos...)
				in.Repos = nil
			}
			if in.OriginSpikeID == "" {
				in.OriginSpikeID = by.RootID
			}
		}
	} else {
		parent, err := s.getTx(ctx, tx, in.ParentKey)
		if err != nil {
			return Item{}, err
		}
		if !slices.Contains(allowedParents[in.Type], parent.Type) {
			return Item{}, errf(CodeBadRequest, "%s can't be a child of %s.",
				capitalize(article(string(in.Type))), article(string(parent.Type)))
		}
		if err := s.orchestratorScope(ctx, tx, by, parent); err != nil {
			return Item{}, err
		}
		rootID, parentID = parent.RootID, sql.NullString{String: parent.ID, Valid: true}
		if len(in.Repos) > 0 {
			root, err := s.getByID(ctx, tx, rootID)
			if err != nil {
				return Item{}, err
			}
			for _, r := range in.Repos {
				if !slices.Contains(root.Repos, r) {
					return Item{}, errf(CodeBadRequest, "%s isn't confirmed for %s.", r, root.Key)
				}
			}
		}
	}

	// Locked decision 15: a task an orchestrator creates must carry a
	// workflow (no role_hint -> workflow inference anywhere). Only a task
	// the user creates on the board, or one the daemon materializes from a
	// plan, may omit it (legacy flow / a future package's concern).
	if in.Type == Task && in.Workflow == nil && by.isOrchestrator() {
		return Item{}, errf(CodeBadRequest, "Task %s has no workflow. Plans assign every role: pick a template or write steps.", in.Title)
	}

	key, err := ids.NextKey(ctx, tx, string(in.Type))
	if err != nil {
		return Item{}, err
	}
	confirmed, hints, reposVersion := "[]", jsonList(in.Repos), 0
	if !parentID.Valid {
		confirmed, hints = jsonList(in.Repos), "[]"
		if len(in.Repos) > 0 {
			reposVersion = 1
		}
	}
	now := db.Millis(s.Now())
	_, err = tx.ExecContext(ctx, `INSERT INTO items (id, key, type, parent_id, root_id, title, brief, acceptance_json,
		status, priority, role_hint, tdd_exempt, confirmed_repos_json, repos_version, repo_hints_json, spike_intent,
		suggested_repos_json, origin_spike_id, legacy_key, sort_order, created_at, updated_at,
		workflow_json, steps_json, units_json, solo, verify_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?,
		NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?)`,
		id, key, in.Type, parentID, rootID, in.Title, in.Brief, jsonList(in.Acceptance),
		in.Status, prio, in.RoleHint, in.TddExempt, confirmed, reposVersion, hints, in.SpikeIntent,
		jsonList(in.SuggestedRepos), in.OriginSpikeID, in.LegacyKey, in.SortOrder, now, now,
		workflowJSONString(in.Workflow), jsonList(in.Steps), jsonUnits(in.Units), in.Solo, jsonList(in.Verify))
	if err != nil {
		return Item{}, err
	}
	it, err := s.getByID(ctx, tx, id)
	if err != nil {
		return Item{}, err
	}
	if err := s.changed(ctx, tx, it); err != nil {
		return Item{}, err
	}
	if parentID.Valid {
		if err := s.ReconcileTx(ctx, tx, in.ParentKey); err != nil {
			return Item{}, err
		}
	}
	return it, nil
}

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// orchestratorScope refuses an orchestrator touching another top-level item's tree.
func (s *Store) orchestratorScope(ctx context.Context, q querier, by Actor, it Item) error {
	if !by.isOrchestrator() || by.RootID == it.RootID {
		return nil
	}
	own, err := s.getByID(ctx, q, by.RootID)
	if err != nil {
		return err
	}
	return errf(CodeBadRequest, "%s is outside %s.", it.Key, own.Key)
}

func (s *Store) Get(ctx context.Context, key string) (Item, error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return it, err
	}
	list := []Item{it}
	err = s.enrich(ctx, s.DB, list)
	return list[0], err
}

func (s *Store) Update(ctx context.Context, key string, p Patch, by Actor) (Item, error) {
	var out Item
	err := s.write(ctx, func(tx *sql.Tx) (err error) {
		out, err = s.UpdateTx(ctx, tx, key, p, by)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, out.Key)
}

// UpdateTx is Update on a transaction the caller owns (mirrors CreateTx and
// AddDepTx). Like AddDepTx, it does not wake SSE subscribers itself: the
// caller commits its own tx and is responsible for calling Events.Notify()
// after that commit.
func (s *Store) UpdateTx(ctx context.Context, tx *sql.Tx, key string, p Patch, by Actor) (Item, error) {
	it, err := s.getTx(ctx, tx, key)
	if err != nil {
		return Item{}, err
	}
	if err := s.orchestratorScope(ctx, tx, by, it); err != nil {
		return Item{}, err
	}
	if p.Revision != it.Revision {
		return Item{}, errf(CodeConflict, StaleRevision)
	}
	if p.Title != nil || p.Brief != nil || p.Acceptance != nil || p.Priority != nil || p.TddExempt != nil ||
		p.Workflow != nil || p.Steps != nil || p.Units != nil || p.Solo != nil || p.Verify != nil {
		if p.Title != nil {
			it.Title = strings.TrimSpace(*p.Title)
		}
		if p.Brief != nil {
			it.Brief = *p.Brief
		}
		if p.Acceptance != nil {
			it.Acceptance = *p.Acceptance
		}
		if p.Priority != nil {
			it.Priority = *p.Priority
		}
		if p.TddExempt != nil {
			it.TddExempt = *p.TddExempt
		}
		if p.Steps != nil {
			it.Steps = *p.Steps
		}
		if p.Units != nil {
			it.Units = *p.Units
		}
		if p.Solo != nil {
			it.Solo = *p.Solo
		}
		if p.Verify != nil {
			it.Verify = *p.Verify
		}
		if err := validateText(it.Title, it.Brief); err != nil {
			return Item{}, err
		}
		if err := validPriority(it.Priority); err != nil {
			return Item{}, err
		}
		if it.TddExempt != "" {
			if !slices.Contains(tddValues, it.TddExempt) {
				return Item{}, errf(CodeBadRequest, "tdd_exempt must be one of docs, config, mechanical-rename, spike-research.")
			}
			if it.Type != Task {
				return Item{}, errf(CodeBadRequest, "Only tasks can be TDD-exempt.")
			}
			if !by.isOrchestrator() && by.Kind != ActorDaemon {
				return Item{}, errf(CodeBadRequest, "Only an orchestrator or a plan can set tdd_exempt.")
			}
		}
		// Permission is checked against what THIS patch asks to set, not the
		// item's already-stored state (unlike tdd_exempt above): the only
		// caller that can ever reach here with these fields is swarm_items,
		// already orchestrator-gated at the MCP layer, but a plain board
		// edit of an already-workflowed task's title must not trip on the
		// task's own pre-existing workflow.
		if err := workflowFieldsPermitted(by, p.Workflow != nil, deref(p.Steps), deref(p.Units), deref(p.Verify), derefStr(p.Solo)); err != nil {
			return Item{}, err
		}
		if err := onlyTasksSet(it.Type, it.Steps, it.Units, it.Solo, it.Verify); err != nil {
			return Item{}, err
		}
		if p.Workflow != nil {
			rw, runRole, werr := resolveWorkflow(it.Type, p.Workflow, it.TddExempt != "")
			if werr != nil {
				return Item{}, werr
			}
			it.Workflow, it.RoleHint = rw, runRole
		}
		if err := validateStepsUnits(it.Key, it.Steps, it.Units); err != nil {
			return Item{}, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE items SET title = ?, brief = ?, acceptance_json = ?, priority = ?,
			tdd_exempt = NULLIF(?, ''), role_hint = NULLIF(?, ''), workflow_json = NULLIF(?, ''), steps_json = ?,
			units_json = ?, solo = NULLIF(?, ''), verify_json = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ?`,
			it.Title, it.Brief, jsonList(it.Acceptance), it.Priority, it.TddExempt, it.RoleHint,
			workflowJSONString(it.Workflow), jsonList(it.Steps), jsonUnits(it.Units), it.Solo, jsonList(it.Verify),
			db.Millis(s.Now()), it.ID, p.Revision)
		if err != nil {
			return Item{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return Item{}, errf(CodeConflict, StaleRevision)
		}
		if err := s.changed(ctx, tx, it); err != nil {
			return Item{}, err
		}
	}
	if p.Status != nil {
		if _, err := s.TransitionTx(ctx, tx, it.Key, *p.Status, by); err != nil {
			return Item{}, err
		}
	} else if err := s.ReconcileTx(ctx, tx, it.Key); err != nil {
		return Item{}, err
	}
	return s.getByID(ctx, tx, it.ID)
}

// Ancestors returns the chain from the top-level item down to the parent.
func (s *Store) Ancestors(ctx context.Context, key string) ([]Item, error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return nil, err
	}
	var out []Item
	for id := it.ParentID; id != ""; {
		p, err := s.getByID(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		out = append([]Item{p}, out...)
		id = p.ParentID
	}
	return out, nil
}

func (s *Store) Children(ctx context.Context, key string) ([]Item, error) {
	it, err := s.getTx(ctx, s.DB, key)
	if err != nil {
		return nil, err
	}
	return s.queryItems(ctx, s.DB, `SELECT `+itemCols+` WHERE i.parent_id = ? AND i.archived_at IS NULL
		ORDER BY i.sort_order, i.created_at`, it.ID)
}

func (s *Store) queryItems(ctx context.Context, q querier, query string, args ...any) ([]Item, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.enrich(ctx, q, out)
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// enrich fills BlockedBy, Progress, ActiveAgents and OpenRequests in place.
func (s *Store) enrich(ctx context.Context, q querier, its []Item) error {
	if len(its) == 0 {
		return nil
	}
	idx := map[string]*Item{}
	args := make([]any, len(its))
	for i := range its {
		idx[its[i].ID] = &its[i]
		args[i] = its[i].ID
	}
	in := placeholders(len(its))
	each := func(query string, fn func(rows *sql.Rows) error) error {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := fn(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	err := each(`SELECT d.item_id, b.key FROM item_deps d JOIN items b ON b.id = d.blocked_by_id
		WHERE b.status NOT IN ('done', 'cancelled') AND d.item_id IN (`+in+`) ORDER BY b.created_at`,
		func(rows *sql.Rows) error {
			var id, key string
			if err := rows.Scan(&id, &key); err != nil {
				return err
			}
			idx[id].BlockedBy = append(idx[id].BlockedBy, key)
			return nil
		})
	if err != nil {
		return err
	}
	err = each(`SELECT parent_id, SUM(status = 'done'), SUM(status <> 'cancelled') FROM items
		WHERE archived_at IS NULL AND parent_id IN (`+in+`) GROUP BY parent_id`,
		func(rows *sql.Rows) error {
			var id string
			var p Progress
			if err := rows.Scan(&id, &p.Done, &p.Total); err != nil {
				return err
			}
			p.Unit = "tasks"
			if idx[id].Type == Epic {
				p.Unit = "stories"
			}
			idx[id].Progress = &p
			return nil
		})
	if err != nil {
		return err
	}
	err = each(`SELECT item_id, COUNT(*) FROM requests WHERE state = 'open' AND item_id IN (`+in+`) GROUP BY item_id`,
		func(rows *sql.Rows) error {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			idx[id].OpenRequests = n
			return nil
		})
	if err != nil {
		return err
	}
	return each(`WITH RECURSIVE sub(top, id) AS (
			SELECT id, id FROM items WHERE id IN (`+in+`)
			UNION ALL SELECT sub.top, c.id FROM items c JOIN sub ON c.parent_id = sub.id)
		SELECT sub.top, COUNT(s.id) FROM sub
		JOIN agents a ON a.item_id = sub.id
		JOIN sessions s ON s.agent_id = a.id
		 AND s.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		GROUP BY sub.top`,
		func(rows *sql.Rows) error {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			idx[id].ActiveAgents = n
			return nil
		})
}

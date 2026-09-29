# Menubar board handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From the menubar, start an orchestrator on a Ready board item or hand a live orchestrator off to a different agent/model/effort/advisor; an in-place restart of a cancelled orchestrator honours the picks too.

**Architecture:** The daemon stores a requested `AgentSwitch` as `agent_operations.switch_json` (migration 0023) and applies it in `startSuccessor` right before `startSession`, for handoff operations (`RequestHandoffTo`) and in-place recover operations (`restartOrchestratorInPlace` with picks). The menubar extracts the Agent/Advisor picker into `AgentPickerModel` + `AgentPickerGrid`, adds a `BoardHandoffForm` that joins `GET /api/items?view=flat` with live agents, and a new "Orchestrate board item" window.

**Tech Stack:** Go 1.x + SQLite (daemon), Swift 5.10 / SwiftUI + AppKit (menubar, SwiftPM package `apps/menubar`).

**Spec:** `docs/specs/2026-09-29-menubar-board-handoff.md` — executors read it alongside this plan. Sections are cited by heading (e.g. spec §"All user-facing copy").

## Global Constraints

- Worktree: `/Users/alexandertar/GitHub/agent-swarm-board-handoff`, branch `feat/menubar-board-handoff`. All paths below are relative to it.
- Task 1 (Go) and Task 2 (Swift Kit) touch disjoint files and MAY run concurrently in this one worktree. Task 3 starts only after Task 2's commit lands.
- Stage explicit paths only: `git add <path> …`. NEVER `git add -A`, NEVER `git commit --amend`. Every commit ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV
  ```
- Go checks: `gofmt -l internal/` prints nothing; `go vet ./...` clean; `go test -race ./internal/...` green. `web/embed.go` embeds `web/dist`; it exists in this worktree — if a Go build complains `pattern all:dist: no matching files`, run `make web-build` once.
- Swift checks: `cd apps/menubar && swift test` green (render tests need a logged-in GUI session, as today).
- Never delete a test. Tests whose API moved are ported (`f.choice` → `f.picker.choice`), not removed.
- Copy strings, error strings and wire shapes come verbatim from spec §"Model / API types", §"All user-facing copy" and the errors table. Strings tests assert are repeated here.
- Migration number 0023: re-check `ls internal/db/schema` before committing; if main added 0023 meanwhile, renumber to the next free number (file, test name, fixture versions).

---

### Task 1: Daemon — switch_json, RequestHandoffTo, switch at successor start, in-place picks, HTTP body

**Files:**
- Create: `internal/db/schema/0023_handoff_switch.sql`
- Create: `internal/db/schema_0023_handoff_switch_test.go`
- Modify: `internal/runtime/model.go` (add `AgentSwitch` after `AdvisorChoice`, ~line 125)
- Modify: `internal/runtime/replacement.go` (`RequestReplacement` 88-171 → wrapper + `requestReplacement`; new `RequestHandoffTo`, `applyAgentSwitch`, error consts; `startSuccessor` ~916-1011)
- Modify: `internal/runtime/agents.go` (`StartOrchestrator` recover branch ~571-578; `restartOrchestratorInPlace` ~786-806)
- Create: `internal/runtime/handoff_switch_test.go`
- Modify: `internal/httpapi/handoff.go` (`handoffBody`, `handoffAgent`)
- Modify: `internal/httpapi/handoff_test.go` (append tests)

**Interfaces:**
- Consumes: existing `Preflight(ctx, PreflightInput)`, `resolveAdvisor(ctx, AgentKind, *AdvisorChoice) (AgentKind, string, string, string, string)`, `operationByKey`, `advanceOperation`, `setPhase`, `advisorChoiceFromBody(*advisorChoiceBody) *runtime.AdvisorChoice`, `wrapPreflightErr(error) error`.
- Produces (HTTP wire, consumed by Task 2): `POST /api/agents/{name}/handoff` body `{"request_id","note"?,"agent"?,"model"?,"effort"?,"advisor"? (object|"none")}` → 202 `replacementWire`; `POST /api/items/{key}/orchestrator` unchanged wire, now honours picks on an in-place restart.
- Produces (Go):
  ```go
  type AgentSwitch struct { Kind AgentKind `json:"kind"`; Model string `json:"model"`; Effort string `json:"effort,omitempty"`; Advisor *AdvisorChoice `json:"advisor,omitempty"` }
  func (s *Store) RequestHandoffTo(ctx context.Context, agentID, requestKey string, sw AgentSwitch) (Operation, error)
  func (s *Store) requestReplacement(ctx context.Context, agentID string, mode ReplacementMode, requestKey, note, switchJSON string) (Operation, error)
  func (s *Store) applyAgentSwitch(ctx context.Context, opID string, a Agent) (Agent, error)
  func (s *Store) restartOrchestratorInPlace(ctx context.Context, a Agent, sw *AgentSwitch, repoPaths []string) (Agent, bool, error)
  ```

- [ ] **Step 1: Migration test (fails: column missing)**

`internal/db/schema_0023_handoff_switch_test.go`:
```go
package db

import "testing"

func TestHandoffSwitchMigrationAddsColumnDefaultEmpty(t *testing.T) {
	raw := openFixtureAtVersion(t, 22)
	if _, err := raw.Exec(`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
		VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_progress', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO agents (id, name, kind, model, role, item_id, root_item_id, brief, state, created_at)
		VALUES ('agt_1', 'o', 'fake', 'fake-1', 'orchestrator', 'itm_1', 'itm_1', 'b', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, generation, created_at, updated_at)
		VALUES ('op_1', 'agt_1', 'handoff', 'succeeded', 'k', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	continueMigratingTo(t, raw, 22, 23)
	var sw string
	if err := raw.QueryRow(`SELECT switch_json FROM agent_operations WHERE id = 'op_1'`).Scan(&sw); err != nil {
		t.Fatal(err)
	}
	if sw != "" {
		t.Fatalf("existing row switch_json = %q, want ''", sw)
	}
}
```
If the `agents`/`agent_operations` INSERT fails on a NOT NULL column at version 22, add that column with a literal value (read `internal/db/schema/*.sql` for the column list); do not drop the row inserts.

- [ ] **Step 2: Run it** — `go test ./internal/db/ -run HandoffSwitchMigration` → FAIL (`no such column: switch_json` or migrate-to-23 error).

- [ ] **Step 3: Migration** — `internal/db/schema/0023_handoff_switch.sql`, exactly the SQL in spec §"DB models" (comment + `ALTER TABLE agent_operations ADD COLUMN switch_json TEXT NOT NULL DEFAULT '';`).

- [ ] **Step 4: Run** — `go test ./internal/db/` → PASS.

- [ ] **Step 5: Runtime tests (fail: undefined RequestHandoffTo/AgentSwitch)**

`internal/runtime/handoff_switch_test.go`:
```go
package runtime

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// claudeOrch starts a Claude orchestrator on EPIC-1 with role overrides set and
// a running, pane-backed session, ready for a handoff walk.
func claudeOrch(t *testing.T, s *Store, tm *fakeTmux) (Agent, Session) {
	t.Helper()
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Claude, Model: "claude-sonnet-5",
		Roles: map[Role]settings.RoleDefault{RoleCoder: {Agent: Claude, Model: "claude-sonnet-5"}}})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses.ID, Running); err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: ses.TmuxName})
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}
	tm.killed = nil
	return orch, ses
}

// driveHandoff walks a preserving handoff to its end: checkpoint, stop, start.
func driveHandoff(t *testing.T, s *Store, tm *fakeTmux, ses Session) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Handoff, Summary: "saved"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	panes(tm)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
}

func agentRow(t *testing.T, s *Store, id string) (kind, model, effort, advKind, advMode, advEffort, kindReason string, overrides sql.NullString) {
	t.Helper()
	if err := s.DB.QueryRow(`SELECT kind, model, COALESCE(effort,''), COALESCE(advisor_kind,''), COALESCE(advisor_mode,''),
		COALESCE(advisor_effort,''), COALESCE(kind_reason,''), role_overrides FROM agents WHERE id = ?`, id).
		Scan(&kind, &model, &effort, &advKind, &advMode, &advEffort, &kindReason, &overrides); err != nil {
		t.Fatal(err)
	}
	return
}

func TestHandoffSwitchAppliesAtSuccessor(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, ses := claudeOrch(t, s, tm)
	op, err := s.RequestHandoffTo(ctx, orch.ID, "sw1", AgentSwitch{Kind: Codex, Model: "gpt-6-astra", Effort: "high",
		Advisor: &AdvisorChoice{None: true}})
	if err != nil {
		t.Fatal(err)
	}
	if k, _, _, _, _, _, _, _ := agentRow(t, s, orch.ID); k != "claude" {
		t.Fatalf("kind switched at request time (%s); the switch applies at PhaseStarting", k)
	}
	driveHandoff(t, s, tm, ses)
	got, _ := s.getOperation(ctx, op.ID)
	if got.Phase != PhaseSucceeded {
		t.Fatalf("phase = %s (%s), want succeeded", got.Phase, got.Error)
	}
	kind, model, effort, advKind, _, _, reason, overrides := agentRow(t, s, orch.ID)
	if kind != "codex" || model != "gpt-6-astra" || effort != "high" || advKind != "" || reason != "" || overrides.Valid {
		t.Fatalf("row = %s/%s/%s adv=%q reason=%q overrides=%v", kind, model, effort, advKind, reason, overrides)
	}
	fa := s.Adapters[Codex].(*adapter.Fake)
	if fa.LastSpec.ProviderSessionID != "" {
		t.Fatalf("successor resumed provider session %q, want a fresh one", fa.LastSpec.ProviderSessionID)
	}
	if !strings.Contains(fa.LastSpec.Kickoff, "continuing in a fresh session after handoff") {
		t.Fatalf("kickoff = %q, want the handoff successor kickoff", fa.LastSpec.Kickoff)
	}
}

func TestHandoffSwitchReplayReturnsSameOp(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, _ := claudeOrch(t, s, tm)
	sw := AgentSwitch{Kind: Codex, Model: "gpt-6-astra"}
	a, err := s.RequestHandoffTo(ctx, orch.ID, "same", sw)
	if err != nil {
		t.Fatal(err)
	}
	sw.Model = "not-a-model" // replay skips validation
	b, err := s.RequestHandoffTo(ctx, orch.ID, "same", sw)
	if err != nil || a.ID != b.ID {
		t.Fatalf("replay = %s/%v, want %s", b.ID, err, a.ID)
	}
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("operations = %d, want 1", n)
	}
}

func TestHandoffSwitchRejects(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, s *Store, orch Agent) string // returns agent id to hand off
		sw   AgentSwitch
		want string
	}{
		{"non-orchestrator", func(t *testing.T, s *Store, orch Agent) string {
			w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude, Model: "claude-sonnet-5",
				ParentAgentID: orch.ID, Brief: BriefInput{Objective: "x"}})
			if err != nil {
				t.Fatal(err)
			}
			return w.ID
		}, AgentSwitch{Kind: Codex, Model: "gpt-6-astra"}, errSwitchNotOrchestrator},
		{"finished", func(t *testing.T, s *Store, orch Agent) string {
			s.DB.Exec(`UPDATE agents SET state = 'finished' WHERE id = ?`, orch.ID)
			return orch.ID
		}, AgentSwitch{Kind: Codex, Model: "gpt-6-astra"}, errSwitchNotLive},
		{"bad model", func(t *testing.T, s *Store, orch Agent) string { return orch.ID },
			AgentSwitch{Kind: Codex, Model: "nope"}, "model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, tm := newStoreWithFallback(t)
			orch, _ := claudeOrch(t, s, tm)
			id := tc.prep(t, s, orch)
			_, err := s.RequestHandoffTo(ctx, id, "k", tc.sw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			var n int
			s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, id).Scan(&n)
			if n != 0 {
				t.Fatalf("operations = %d, want none recorded", n)
			}
		})
	}
}

func TestHandoffSwitchPreflightFailsAtStartBlocks(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, ses := claudeOrch(t, s, tm)
	op, err := s.RequestHandoffTo(ctx, orch.ID, "k", AgentSwitch{Kind: Codex, Model: "gpt-6-astra"})
	if err != nil {
		t.Fatal(err)
	}
	setEnabled(t, s, Claude) // Codex disabled after the request was accepted
	driveHandoff(t, s, tm, ses)
	got, _ := s.getOperation(ctx, op.ID)
	if got.Phase != PhaseBlocked || got.Error == "" {
		t.Fatalf("phase = %s (%q), want blocked with the Preflight text", got.Phase, got.Error)
	}
	if k, _, _, _, _, _, _, ov := agentRow(t, s, orch.ID); k != "claude" || !ov.Valid {
		t.Fatalf("row changed on a blocked switch: kind=%s overrides=%v", k, ov)
	}
	if got := s.Notify.(*fakeNotifier).kinds(); !slices.Contains(got, "agent.preflight_failed") {
		t.Fatalf("raised %v, want agent.preflight_failed", got)
	}
}

func TestHandoffSwitchNativeAdvisorReResolved(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Codex, Model: "gpt-6-astra"})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, orch.ID)
	s.SetSessionState(ctx, ses.ID, Running)
	panes(tm, Pane{Session: ses.TmuxName})
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}
	if _, err := s.RequestHandoffTo(ctx, orch.ID, "k", AgentSwitch{Kind: Claude, Model: "claude-sonnet-5",
		Advisor: &AdvisorChoice{Kind: Claude, Model: "claude-fable-5-1", Effort: "high"}}); err != nil {
		t.Fatal(err)
	}
	driveHandoff(t, s, tm, ses)
	_, _, _, advKind, advMode, advEffort, _, _ := agentRow(t, s, orch.ID)
	if advKind != "claude" || advMode != "native" || advEffort != "" {
		t.Fatalf("advisor = %s/%s/%q, want claude/native/''", advKind, advMode, advEffort)
	}
}

func TestPlainHandoffLeavesSwitchEmpty(t *testing.T) {
	s, tm := newStoreWithFallback(t)
	ctx := context.Background()
	orch, ses := claudeOrch(t, s, tm)
	op, err := s.RequestReplacement(ctx, orch.ID, ModeHandoff, "plain", "")
	if err != nil {
		t.Fatal(err)
	}
	driveHandoff(t, s, tm, ses)
	var sw string
	s.DB.QueryRow(`SELECT switch_json FROM agent_operations WHERE id = ?`, op.ID).Scan(&sw)
	if k, _, _, _, _, _, _, ov := agentRow(t, s, orch.ID); sw != "" || k != "claude" || !ov.Valid {
		t.Fatalf("plain handoff: switch_json=%q kind=%s overrides=%v", sw, k, ov)
	}
}

func cancelledClaudeOrch(t *testing.T, s *Store) Agent {
	t.Helper()
	ctx := context.Background()
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Claude, Model: "claude-sonnet-5",
		Roles: map[Role]settings.RoleDefault{RoleCoder: {Agent: Claude, Model: "claude-sonnet-5"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(ctx, orch.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	return orch
}

func TestStartAfterCancelAppliesPicks(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	orch := cancelledClaudeOrch(t, s)
	again, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Codex, Model: "gpt-6-astra", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != orch.ID || again.Name != orch.Name {
		t.Fatalf("restart = %s/%s, want same %s/%s", again.ID, again.Name, orch.ID, orch.Name)
	}
	kind, model, effort, _, _, _, reason, ov := agentRow(t, s, orch.ID)
	if kind != "codex" || model != "gpt-6-astra" || effort != "high" || reason != "" || ov.Valid {
		t.Fatalf("row = %s/%s/%s reason=%q overrides=%v", kind, model, effort, reason, ov)
	}
	var mode, phase string
	s.DB.QueryRow(`SELECT mode, phase FROM agent_operations WHERE agent_id = ? ORDER BY created_at DESC LIMIT 1`, orch.ID).Scan(&mode, &phase)
	if mode != "recover" || phase != "succeeded" {
		t.Fatalf("op = %s/%s, want recover/succeeded", mode, phase)
	}
}

func TestStartAfterCancelWithoutPicksKeepsKind(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	orch := cancelledClaudeOrch(t, s)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1"}); err != nil {
		t.Fatal(err)
	}
	var sw string
	s.DB.QueryRow(`SELECT switch_json FROM agent_operations WHERE agent_id = ? ORDER BY created_at DESC LIMIT 1`, orch.ID).Scan(&sw)
	if k, _, _, _, _, _, _, ov := agentRow(t, s, orch.ID); k != "claude" || !ov.Valid || sw != "" {
		t.Fatalf("kind=%s overrides=%v switch_json=%q, want unchanged", k, ov, sw)
	}
}

func TestStartAfterCancelBadPicksIsPreflightError(t *testing.T) {
	s, _ := newStoreWithFallback(t)
	ctx := context.Background()
	orch := cancelledClaudeOrch(t, s)
	var before int
	s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&before)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Codex, Model: "nope"}); err == nil {
		t.Fatal("want a Preflight error for an unknown model")
	}
	var after int
	s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, orch.ID).Scan(&after)
	if k, _, _, _, _, _, _, _ := agentRow(t, s, orch.ID); after != before || k != "claude" {
		t.Fatalf("ops %d→%d kind=%s, want nothing recorded, row unchanged", before, after, k)
	}
}
```
Add `"slices"` to the imports. If `fa.LastSpec.Kickoff`'s handoff wording differs, copy the exact phrase from `SuccessorKickoff` in `internal/runtime/text.go` (`"…continuing in a fresh session after %s…"` with `after = "handoff"`). If a Preflight message for an unknown model does not contain `model`, assert on the exact sentence from `agents.go:167-210` instead.

- [ ] **Step 6: Run** — `go test ./internal/runtime/ -run 'HandoffSwitch|PlainHandoff|StartAfterCancel'` → FAIL to compile (`undefined: AgentSwitch`, `RequestHandoffTo`, `errSwitchNotOrchestrator`).

- [ ] **Step 7: Implement runtime**

`model.go` — add `AgentSwitch` exactly as spec §"Go — runtime".

`replacement.go`:
```go
const (
	errSwitchNotOrchestrator = "Only an orchestrator can switch agents on handoff."
	errSwitchNotLive         = "This orchestrator is no longer running. Reopen the list."
)

func (s *Store) RequestReplacement(ctx context.Context, agentID string, mode ReplacementMode, requestKey, note string) (Operation, error) {
	return s.requestReplacement(ctx, agentID, mode, requestKey, note, "")
}

// requestReplacement is the former RequestReplacement body; only the INSERT changes:
//   INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, note, switch_json, created_at, updated_at)
//   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)   -- extra arg: switchJSON

// RequestHandoffTo: see spec §"Go — runtime" for the ordered checks.
func (s *Store) RequestHandoffTo(ctx context.Context, agentID, requestKey string, sw AgentSwitch) (Operation, error) {
	if requestKey != "" {
		op, err := s.operationByKey(ctx, agentID, requestKey)
		if err == nil {
			return op, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Operation{}, err
		}
	}
	a, err := s.agentByID(ctx, agentID)
	if err != nil {
		return Operation{}, err
	}
	if a.Role != RoleOrchestrator {
		return Operation{}, &items.Error{Code: items.CodeBadRequest, Message: errSwitchNotOrchestrator}
	}
	if a.State != AgentQueued && a.State != AgentActive {
		return Operation{}, &items.Error{Code: items.CodeConflict, Message: errSwitchNotLive}
	}
	if err := s.Preflight(ctx, PreflightInput{Kind: sw.Kind, Model: sw.Model, Effort: sw.Effort, Role: RoleOrchestrator}); err != nil {
		return Operation{}, err
	}
	b, err := json.Marshal(sw)
	if err != nil {
		return Operation{}, err
	}
	return s.requestReplacement(ctx, agentID, ModeHandoff, requestKey, "", string(b))
}

// applyAgentSwitch applies an operation's pending switch to the agent row
// right before its successor starts. No switch → a unchanged.
func (s *Store) applyAgentSwitch(ctx context.Context, opID string, a Agent) (Agent, error) {
	var raw string
	if err := s.DB.QueryRowContext(ctx, `SELECT switch_json FROM agent_operations WHERE id = ?`, opID).Scan(&raw); err != nil {
		return a, err
	}
	if raw == "" {
		return a, nil
	}
	var sw AgentSwitch
	if err := json.Unmarshal([]byte(raw), &sw); err != nil {
		return a, err
	}
	if err := s.Preflight(ctx, PreflightInput{Kind: sw.Kind, Model: sw.Model, Effort: sw.Effort, Role: a.Role}); err != nil {
		return a, err
	}
	advKind, advModel, advEffort, advMode, advReq := s.resolveAdvisor(ctx, sw.Kind, sw.Advisor)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = ?, model = ?, effort = ?,
		advisor_kind = NULLIF(?, ''), advisor_model = NULLIF(?, ''), advisor_effort = NULLIF(?, ''),
		advisor_mode = NULLIF(?, ''), advisor_requested_effort = NULLIF(?, ''),
		role_overrides = NULL, kind_reason = '' WHERE id = ?`,
		string(sw.Kind), sw.Model, sw.Effort, string(advKind), advModel, advEffort, advMode, advReq, a.ID); err != nil {
		return a, err
	}
	a.Kind, a.Model, a.Effort = sw.Kind, sw.Model, sw.Effort
	a.AdvisorKind, a.AdvisorModel, a.AdvisorEffort, a.AdvisorMode, a.AdvisorRequestedEffort = string(advKind), advModel, advEffort, advMode, advReq
	a.RoleOverrides, a.KindReason = nil, ""
	return a, nil
}
```
Match the `effort` column's NULL convention to `applyRetryFallback` (`fallback.go:99`, which writes the raw string); keep it identical.

`startSuccessor`: extract the retry branch's notification into
```go
func (s *Store) raisePreflightFailed(ctx context.Context, a Agent, err error) {
	if s.Notify == nil {
		return
	}
	var itemKey string
	_ = s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, a.ItemID).Scan(&itemKey)
	_ = s.Notify.Raise(ctx, nil, NotifyInput{Kind: "agent.preflight_failed", AgentName: a.Name,
		ItemKey: itemKey, Args: map[string]string{"reason": err.Error()}})
}
```
use it in the retry branch (keep its comment), and after the `switch { … }` block, before `startSession`:
```go
	var err error
	if a, err = s.applyAgentSwitch(ctx, op.ID, a); err != nil {
		s.raisePreflightFailed(ctx, a, err)
		return s.setPhase(ctx, op.ID, PhaseStarting, PhaseBlocked, err.Error())
	}
```
(reuse the existing `err` if already declared in scope; `succ, err := …` below becomes `succ, err = …` if needed.)

`agents.go` — `StartOrchestrator` recover branch and `restartOrchestratorInPlace` exactly as spec §"Picks on an in-place restart":
```go
	} else if ok {
		var sw *AgentSwitch
		if in.Kind != "" {
			sw = &AgentSwitch{Kind: in.Kind, Model: in.Model, Effort: in.Effort, Advisor: in.Advisor}
		}
		return s.restartOrchestratorInPlace(ctx, rec, sw, in.RepoPaths)
	}
```
```go
func (s *Store) restartOrchestratorInPlace(ctx context.Context, a Agent, sw *AgentSwitch, repoPaths []string) (Agent, bool, error) {
	if err := s.refuseIfOperationInFlight(ctx, a.ID); err != nil {
		return Agent{}, false, err
	}
	switchJSON := ""
	if sw != nil {
		if err := s.Preflight(ctx, PreflightInput{Kind: sw.Kind, Model: sw.Model, Effort: sw.Effort,
			Role: RoleOrchestrator, RepoPaths: repoPaths}); err != nil {
			return Agent{}, false, err
		}
		b, err := json.Marshal(sw)
		if err != nil {
			return Agent{}, false, err
		}
		switchJSON = string(b)
	}
	op, err := s.requestReplacement(ctx, a.ID, ModeRecover, "", "", switchJSON)
	// … rest unchanged (blocked → conflict, agentByID, queued flag)
}
```
Update the doc comment above `restartOrchestratorInPlace` with one line: picks, when sent, ride the operation as `switch_json` and apply at start.

- [ ] **Step 8: Run** — `go test -race ./internal/runtime/ -run 'HandoffSwitch|PlainHandoff|StartAfterCancel|CancelThenStart|Replacement|Handoff'` → PASS (includes the unchanged `TestCancelThenStartRecoversSameOrchestrator`, which now takes the switch path with `Kind: Fake`).

- [ ] **Step 9: httpapi tests (fail)** — append to `internal/httpapi/handoff_test.go`:
```go
func TestHandoffWithSwitchIs202(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff",
		`{"request_id":"s1","agent":"fake","model":"fake-1","advisor":"none"}`)
	if rec.Code != 202 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var sw string
	if err := s.DB.QueryRowContext(bg, `SELECT switch_json FROM agent_operations WHERE request_key = 's1'`).Scan(&sw); err != nil || sw == "" {
		t.Fatalf("switch_json = %q (%v), want the stored switch", sw, err)
	}
}

func TestHandoffSwitchFieldsWithoutAgentIs400(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"s2","model":"x"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 400, "bad_request", "Choose an agent.")
}

func TestHandoffSwitchOnWorkerIs400(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/task-worker/handoff", `{"request_id":"s3","agent":"fake","model":"fake-1"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 400, "bad_request", "Only an orchestrator can switch agents on handoff.")
}

func TestHandoffSwitchBadModelIs422(t *testing.T) {
	s, _ := newRuntimeServer(t)
	rec := s.post(t, "/api/agents/root-orchestrator/handoff", `{"request_id":"s4","agent":"fake","model":"nope"}`)
	wantErr(t, rec.Code, rec.Body.Bytes(), 422, "preflight_failed", "")
}
```
If the seeded `root-orchestrator` session is not replaceable via the plain path either, check `TestHandoffReturns202WithReplacementResult` (it uses `task-worker`) and seed per its pattern; the assertions stay.

- [ ] **Step 10: Run** — `go test ./internal/httpapi/ -run Handoff` → the 4 new tests FAIL (switch fields ignored → 202 everywhere).

- [ ] **Step 11: Implement httpapi** — `handoff.go`: `handoffBody` as spec §"Go — httpapi". In `handoffAgent`, after resolving `a`:
```go
	var op runtime.Operation
	switch {
	case body.Agent == "" && (body.Model != "" || body.Effort != "" || body.Advisor != nil):
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "Choose an agent."))
		return
	case body.Agent != "":
		op, err = s.RT.RequestHandoffTo(r.Context(), a.ID, body.RequestID, runtime.AgentSwitch{
			Kind: runtime.AgentKind(body.Agent), Model: body.Model, Effort: body.Effort,
			Advisor: advisorChoiceFromBody(body.Advisor)})
		err = wrapPreflightErr(err)
	default:
		op, err = s.RT.RequestReplacement(r.Context(), a.ID, runtime.ModeHandoff, body.RequestID, body.Note)
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
```
(`body.Note` is ignored on the switch path — spec.)

- [ ] **Step 12: Run everything** — `gofmt -l internal/` (empty), `go vet ./...`, `go test -race ./internal/...` → all PASS.

- [ ] **Step 13: Commit**
```bash
git add internal/db/schema/0023_handoff_switch.sql internal/db/schema_0023_handoff_switch_test.go \
  internal/runtime/model.go internal/runtime/replacement.go internal/runtime/agents.go \
  internal/runtime/handoff_switch_test.go internal/httpapi/handoff.go internal/httpapi/handoff_test.go
git commit -m "feat(runtime): handoff agent switch and picks on in-place orchestrator restart

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 2: Menubar Kit — AgentPickerModel extraction, board-item rows, BoardHandoffForm, client methods

**Files (all under `apps/menubar/`):**
- Create: `Sources/SwarmBarKit/AgentPickerModel.swift` (code MOVED from `NewOrchestratorForm.swift` lines 31-33, 39-41, 136-159, 178-232, 351-356 + the catalog part of `load()` 83-97)
- Modify: `Sources/SwarmBarKit/NewOrchestratorForm.swift` (holds `public let picker: AgentPickerModel`)
- Create: `Sources/SwarmBarKit/BoardHandoff.swift`
- Modify: `Sources/SwarmBarKit/Wire.swift` (`BoardItem`, `BoardItemList`, `StartOrchestratorBody`, `HandoffRequest`)
- Modify: `Sources/SwarmBarKit/DaemonClient.swift` (protocol + `MockDaemonClient`)
- Modify: `Sources/SwarmBarKit/HTTPDaemonClient.swift` (3 methods; delete private `HandoffBody`)
- Modify: `Sources/SwarmBarKit/AppModel.swift` (`boardHandoffPreselect`, `makeBoardHandoffForm()`)
- Modify: `Sources/SwarmBarKit/Copy.swift` (spec §"All user-facing copy")
- Create: `Tests/Fixtures/items.json`
- Create: `Tests/SwarmBarTests/AgentPickerModelTests.swift`, `Tests/SwarmBarTests/BoardHandoffTests.swift`
- Modify (port, never delete): `Tests/SwarmBarTests/NewOrchestratorFormTests.swift`, `Tests/SwarmBarTests/NewOrchestratorRenderTests.swift`, `Tests/SwarmBarTests/HTTPDaemonClientTests.swift`
- Modify (compile only): `Sources/SwarmBarUI/NewOrchestratorView.swift` — replace `form.X` with `form.picker.X` for the moved members so the package builds. No layout change here (Task 3 does the view extraction).

**Interfaces:**
- Consumes: Task 1's wire (spec §"Go — httpapi"); existing `CatalogRules`, `AgentTree.actions/handoffStatus/flatten`, `DisplayState`, `NewOrchestratorForm.wouldQueue`.
- Produces (Task 3 relies on these exact names):
  ```swift
  @MainActor @Observable public final class AgentPickerModel   // members exactly as spec §"Swift — SwarmBarKit"
  public struct BoardItem, BoardItemList, StartOrchestratorBody, HandoffRequest   // spec shapes
  public struct BoardItemRow { item: BoardItem; orchestrator: AgentNode?; id }
  public enum BoardHandoffRules {
      static func rows(_ items: [BoardItem], agents: [AgentNode]) -> [BoardItemRow]
      static func title(_ r: BoardItemRow) -> String
      static func detail(_ r: BoardItemRow, catalog: [AgentCatalogEntry]) -> String
      static func canHandOff(_ a: AgentNode, connected: Bool) -> Bool
      static func offersHandOffTo(_ a: AgentNode, actions: [AgentAction]) -> Bool   // row "Hand off to…" gate
  }
  @MainActor @Observable public final class BoardHandoffForm   // members exactly as spec
  AppModel.boardHandoffPreselect: String?; AppModel.makeBoardHandoffForm() -> BoardHandoffForm
  DaemonClient.boardItems() / startOrchestrator(itemKey:_:) / handoff(_:_:)
  Copy.orchestrateBoardItem, orchestrateBoardItemMenu, moreStartOptions, handOffTo, handOff, item, noBoardItems,
       boardItemsLoadFailed, handoffFailed, handoffUnavailable, ready, itemType(_:), boardItemTitle(_:_:)
  ```

- [ ] **Step 1: Extract AgentPickerModel (move, then port tests)**

Create `AgentPickerModel.swift` containing, moved verbatim from `NewOrchestratorForm`: `choice`, `advisor`, `advisorEffort`, `catalog`, `agentChangeErrors`, `effortNote`, `settings`, `errors`, `agentOptions`, `modelOptions`, `effortOptions`, `advisorAgentOptions`, `advisorModelOptions`, `advisorEffortOptions`, `setAgent/setModel/setEffort/setAdvisorEffort/setAdvisorAgent/setAdvisorModel`, private `normalizeAdvisorEffort`. New members:
```swift
    public init(settings: Settings) {
        self.settings = settings
        (choice, advisor) = CatalogRules.prefill(settings)
        advisorEffort = settings[.advisor]?.effort ?? ""
    }

    /// The catalog arrived: the former NewOrchestratorForm.load() normalisation.
    public func apply(catalog: [AgentCatalogEntry]) {
        self.catalog = catalog
        advisor = CatalogRules.normalizedAdvisor(advisor, settings: settings, catalog: catalog)
        normalizeAdvisorEffort()
        choice.effort = CatalogRules.normalizeEffort(choice.agent,
            CatalogRules.resolve(CatalogRules.entry(catalog, choice.agent), choice.model), choice.effort)
    }

    public var effortPayload: String? {
        guard let agent = choice.agent else { return nil }
        let e = CatalogRules.normalizeEffort(agent, CatalogRules.resolve(CatalogRules.entry(catalog, agent), choice.model), choice.effort)
        return e.isEmpty ? nil : e
    }

    public var advisorPayload: AdvisorPayload { /* body of the former private advisorBody(), unchanged */ }
```
In `NewOrchestratorForm`: delete the moved members, add `public let picker: AgentPickerModel` (init: `picker = AgentPickerModel(settings: settings)`), keep `public let settings` (read `picker.settings` is also fine). `load()` becomes `async let c = …; picker.apply(catalog: await c ?? [])` + the repos part. `canStart` uses `picker.errors.isValid`. `body()` uses `picker.choice.agent`, `picker.choice.model`, `picker.effortPayload`, `picker.advisorPayload`.

Port tests mechanically (no test deleted):
```bash
cd apps/menubar
perl -pi -e 's/\b(f|form)\.(choice|advisorEffortOptions|advisorEffort|advisorAgentOptions|advisorModelOptions|advisor|catalog|agentChangeErrors|effortNote|errors|agentOptions|modelOptions|effortOptions|setAgent|setModel|setEffort|setAdvisorAgent|setAdvisorModel|setAdvisorEffort)\b/$1.picker.$2/g' \
  Tests/SwarmBarTests/NewOrchestratorFormTests.swift Tests/SwarmBarTests/NewOrchestratorRenderTests.swift \
  Sources/SwarmBarUI/NewOrchestratorView.swift
grep -c 'picker\.' Tests/SwarmBarTests/NewOrchestratorFormTests.swift   # expect ~72
```
Fix any leftover compile errors by hand (a differently named local, `form.errors` in the view's Start gate). `form.body()`/`canStart`/`startLabel` stay on the form.

New `Tests/SwarmBarTests/AgentPickerModelTests.swift` (standalone coverage of the moved type):
```swift
import XCTest
@testable import SwarmBarKit

@MainActor
final class AgentPickerModelTests: XCTestCase {
    func testPrefillApplyAndPayloads() throws {
        let state: StateResponse = try Fixture.decode("state.json")
        let catalog: [AgentCatalogEntry] = try Fixture.decode("catalog.json")
        let p = AgentPickerModel(settings: state.settings)
        XCTAssertEqual(p.choice, AgentChoice(agent: .claude, model: "opus"))
        p.apply(catalog: catalog)
        XCTAssertTrue(p.errors.isValid)
        XCTAssertNil(p.advisorEffortOptions, "native Claude-on-Claude advisor has no separate effort")
        p.setAdvisorAgent("none")
        XCTAssertEqual(p.advisorPayload, .none)
        p.setAgent("codex")
        XCTAssertEqual(p.choice.agent, .codex)
    }
}
```

Run: `cd apps/menubar && swift test --filter 'AgentPicker|NewOrchestrator'` → PASS (same count of NewOrchestrator tests as before + 1).

- [ ] **Step 2: Wire types + client — failing tests**

`Tests/Fixtures/items.json` (daemon order = updated_at DESC):
```json
{"items": [
  {"key": "BUG-7", "type": "bug", "status": "ready", "title": "Login crash", "parent_key": null},
  {"key": "SPIKE-4", "type": "spike", "status": "in_progress", "title": "Billing options", "parent_key": null},
  {"key": "EPIC-20", "type": "epic", "status": "in_progress", "title": "Orphan epic", "parent_key": null},
  {"key": "STORY-4", "type": "story", "status": "ready", "title": "A story", "parent_key": null},
  {"key": "EPIC-12", "type": "epic", "status": "in_progress", "title": "Authentication", "parent_key": null},
  {"key": "CHORE-3", "type": "chore", "status": "ready", "title": "Bump deps", "parent_key": null},
  {"key": "TASK-101", "type": "task", "status": "ready", "title": "Login form", "parent_key": "EPIC-12"},
  {"key": "EPIC-40", "type": "epic", "status": "done", "title": "Shipped", "parent_key": null},
  {"key": "WIDGET-1", "type": "widget", "status": "ready", "title": "Unknown type", "parent_key": null}
], "matches": 9}
```
(state.json live orchestrators: `auth-epic-orchestrator`→EPIC-12 running, `billing-spike-orchestrator`→SPIKE-4 queued.)

Append to `HTTPDaemonClientTests.swift`:
```swift
    func testHandoffWithoutSwitchSendsOnlyRequestID() async throws {
        let session = StubURLProtocol.install { _ in (202, Data("{}".utf8)) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        try await client.handoff("auth-epic-orchestrator", HandoffRequest(requestId: "k1"))
        let seen = try XCTUnwrap(StubURLProtocol.seen.last)
        XCTAssertEqual(seen.method, "POST")
        XCTAssertEqual(seen.path, "/api/agents/auth-epic-orchestrator/handoff")
        XCTAssertEqual(try Fixture.json(Data(seen.body.utf8)), try Fixture.json(Data(#"{"request_id":"k1"}"#.utf8)))
    }

    func testHandoffWithSwitchSendsPicksAndNoneAdvisor() async throws {
        let session = StubURLProtocol.install { _ in (202, Data("{}".utf8)) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        try await client.handoff("o", HandoffRequest(requestId: "k2", agent: .codex, model: "gpt-6-astra", effort: "high", advisor: AdvisorPayload.none))
        let body = try XCTUnwrap(StubURLProtocol.seen.last).body
        XCTAssertEqual(try Fixture.json(Data(body.utf8)),
                       try Fixture.json(Data(#"{"request_id":"k2","agent":"codex","model":"gpt-6-astra","effort":"high","advisor":"none"}"#.utf8)))
    }

    func testStartOrchestratorBodyHasNoRolesOrRepos() async throws {
        let session = StubURLProtocol.install { _ in (200, try Fixture.data("agent.json")) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        _ = try await client.startOrchestrator(itemKey: "BUG-7", StartOrchestratorBody(requestId: "r", agent: .claude, model: "opus", effort: nil, advisor: .none))
        let seen = try XCTUnwrap(StubURLProtocol.seen.last)
        XCTAssertEqual(seen.path, "/api/items/BUG-7/orchestrator")
        XCTAssertFalse(seen.body.contains("roles") || seen.body.contains("repos"), seen.body)
    }

    func testBoardItemsDecodesFlatListIncludingUnknownTypes() async throws {
        let session = StubURLProtocol.install { _ in (200, try Fixture.data("items.json")) }
        let client = HTTPDaemonClient(endpoint: try tempEndpoint(), session: session)
        let items = try await client.boardItems()
        XCTAssertEqual(StubURLProtocol.seen.last?.path, "/api/items?view=flat")
        XCTAssertEqual(items.count, 9)
        XCTAssertEqual(items.last?.type, "widget")
    }
```
`testHandoffSendsTheCallersRequestKey` stays unchanged (it now exercises `HandoffRequest(requestId:)` through `agent(_:.handoff,…)`). If `agent.json` is not an `AgentNode`, use the fixture that is (check `FixtureTests.swift`).

Run: `swift test --filter HTTPDaemonClient` → compile FAIL (`HandoffRequest`, `handoff`, …).

- [ ] **Step 3: Implement wire + client** — types in `Wire.swift` exactly as spec §"Swift — SwarmBarKit", each with a memberwise `public init` (for `HandoffRequest`: `init(requestId: String, agent: AgentKind? = nil, model: String? = nil, effort: String? = nil, advisor: AdvisorPayload? = nil)`). Protocol additions per spec. HTTP:
```swift
    public func boardItems() async throws -> [BoardItem] {
        try await call("GET", "/api/items?view=flat", as: BoardItemList.self).items
    }
    public func startOrchestrator(itemKey: String, _ body: StartOrchestratorBody) async throws -> AgentNode {
        try await call("POST", "/api/items/\(Self.segment(itemKey))/orchestrator", body: body, timeout: 60)
    }
    public func handoff(_ name: String, _ body: HandoffRequest) async throws {
        _ = try await call("POST", "/api/agents/\(Self.segment(name))/handoff", body: body, timeout: 60, as: Empty.self)
    }
```
`agent(_:.handoff,…)` sends `HandoffRequest(requestId: requestID ?? UUID().uuidString)`; delete `HandoffBody`. Mock:
```swift
    public var boardItemList: [BoardItem] = []
    public var startResult: Result<AgentNode, DaemonError>?
    public private(set) var handoffRequests: [HandoffRequest] = []
    public private(set) var startBodies: [StartOrchestratorBody] = []

    public func boardItems() async throws -> [BoardItem] {
        try record("items")
        return boardItemList
    }
    public func startOrchestrator(itemKey: String, _ body: StartOrchestratorBody) async throws -> AgentNode {
        startBodies.append(body)
        try record("start \(itemKey) \(body.agent.rawValue) \(body.model)")
        return try (startResult ?? .success(AgentNode(name: "new-orchestrator", model: body.model, role: .orchestrator,
                                                      itemKey: itemKey, rootKey: itemKey))).get()
    }
    public func handoff(_ name: String, _ body: HandoffRequest) async throws {
        handoffRequests.append(body)
        try record("handoff \(name) \(body.agent?.rawValue ?? "-") \(body.model ?? "-")")
    }
```
In `init(fixtures:)` after `self.init(...)`: `boardItemList = (try? load("items.json", BoardItemList.self))?.items ?? []`.

Run: `swift test --filter 'HTTPDaemonClient|MockDaemonClient|Fixture'` → PASS.

- [ ] **Step 4: BoardHandoff rules + form — failing tests**

`Tests/SwarmBarTests/BoardHandoffTests.swift`:
```swift
import XCTest
@testable import SwarmBarKit

@MainActor
final class BoardHandoffTests: XCTestCase {
    var client: MockDaemonClient!
    var state = StateResponse()
    var catalog: [AgentCatalogEntry] = []

    override func setUp() async throws {
        client = try MockDaemonClient(fixtures: Fixture.dir)
        state = try Fixture.decode("state.json")
        catalog = try Fixture.decode("catalog.json")
    }

    private func form(preselect: String? = nil, connected: Bool = true, max: Int? = nil) async -> BoardHandoffForm {
        var settings = state.settings
        if let max { settings.maxConcurrentAgents = max }
        let f = BoardHandoffForm(client: client, settings: settings, agents: state.agents, connected: connected, preselectAgent: preselect)
        await f.load()
        return f
    }

    func testRowsEligibilityAndOrdering() {
        let rows = BoardHandoffRules.rows(client.boardItemList, agents: state.agents)
        XCTAssertEqual(rows.map(\.id), ["SPIKE-4", "EPIC-12", "BUG-7", "CHORE-3"],
                       "live-orchestrator rows first, then Ready; daemon order within; no story/task/child/done/in-progress-without-orchestrator/unknown type")
        XCTAssertEqual(rows[1].orchestrator?.name, "auth-epic-orchestrator")
        XCTAssertNil(rows[2].orchestrator)
    }

    func testFinishedOrchestratorDoesNotCount() {
        var agents = state.agents
        agents[0].state = .finished // auth-epic-orchestrator
        XCTAssertFalse(BoardHandoffRules.rows(client.boardItemList, agents: agents).contains { $0.id == "EPIC-12" })
    }

    func testTitleAndDetail() {
        let rows = BoardHandoffRules.rows(client.boardItemList, agents: state.agents)
        XCTAssertEqual(BoardHandoffRules.title(rows[1]), "EPIC-12 · Authentication")
        let o = rows[1].orchestrator!
        let entry = CatalogRules.entry(catalog, o.kind)
        let parts = [o.name, CatalogRules.modelLabel(entry, o.model), CatalogRules.previewEffortLabel(entry, o.model, o.effort),
                     AgentTree.handoffStatus(o) ?? DisplayState(o).label ?? Copy.runningLabel].compactMap { $0 }
        XCTAssertEqual(BoardHandoffRules.detail(rows[1], catalog: catalog), parts.joined(separator: " · "))
        XCTAssertEqual(BoardHandoffRules.detail(rows[2], catalog: catalog), "Bug · Ready")
        XCTAssertEqual(BoardHandoffRules.detail(rows[3], catalog: catalog), "Chore · Ready")
    }

    func testDetailOmitsUnknownEffort() {
        var o = AgentNode(name: "x-orch", kind: .claude, model: "unknown-model", role: .orchestrator, itemKey: "BUG-7")
        o.effort = nil
        let row = BoardItemRow(item: client.boardItemList[0], orchestrator: o)
        XCTAssertEqual(BoardHandoffRules.detail(row, catalog: catalog).components(separatedBy: " · ").count, 3)
    }

    func testCanHandOff() {
        let flat = AgentTree.flatten(state.agents)
        let running = flat.first { $0.name == "auth-epic-orchestrator" }!
        let queued = flat.first { $0.name == "billing-spike-orchestrator" }!
        XCTAssertTrue(BoardHandoffRules.canHandOff(running, connected: true))
        XCTAssertFalse(BoardHandoffRules.canHandOff(running, connected: false))
        XCTAssertFalse(BoardHandoffRules.canHandOff(queued, connected: true))
        var replacing = running
        replacing.replacement = AgentReplacement(operationId: "op", mode: "handoff", phase: "stopping")
        XCTAssertFalse(BoardHandoffRules.canHandOff(replacing, connected: true))
    }

    func testOffersHandOffToOnlyOnTopLevelOrchestratorsWithEnabledHandoff() {
        let flat = AgentTree.flatten(state.agents)
        let orch = flat.first { $0.name == "auth-epic-orchestrator" }!
        let coder = flat.first { $0.name == "login-form-coder" }!
        XCTAssertTrue(BoardHandoffRules.offersHandOffTo(orch, actions: AgentTree.actions(orch, tmuxAlive: true, connected: true)))
        XCTAssertFalse(BoardHandoffRules.offersHandOffTo(coder, actions: AgentTree.actions(coder, tmuxAlive: true, connected: true)))
        XCTAssertFalse(BoardHandoffRules.offersHandOffTo(orch, actions: []))
    }

    func testDefaultSelectionAndLabels() async {
        let f = await form()
        XCTAssertEqual(f.selectedKey, "SPIKE-4", "first row when nothing is preselected")
        XCTAssertTrue(f.isHandoff)
        XCTAssertEqual(f.primaryLabel, Copy.handOff)
        XCTAssertEqual(f.caption, Copy.handoffUnavailable, "queued orchestrator can't hand off")
        XCTAssertFalse(f.canSubmit)
        f.selectedKey = "EPIC-12"
        XCTAssertNil(f.caption)
        XCTAssertTrue(f.canSubmit)
        f.selectedKey = "BUG-7"
        XCTAssertEqual(f.primaryLabel, Copy.startOrchestrator)
    }

    func testQueueLabelAtCapacity() async {
        let f = await form(max: 1)
        f.selectedKey = "BUG-7"
        XCTAssertEqual(f.primaryLabel, Copy.queueOrchestrator)
        XCTAssertEqual(f.caption, Copy.queuedCaption)
    }

    func testPreselectPicksTheOrchestratorsItem() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        XCTAssertEqual(f.selectedKey, "EPIC-12")
    }

    func testHandoffSubmitSendsPicksAndReusesRequestIDOnRetry() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        f.picker.setAgent("codex")
        client.failNext = .timedOut
        let first = await f.primary()
        XCTAssertFalse(first)
        XCTAssertEqual(f.primaryLabel, Copy.tryAgain)
        XCTAssertTrue(f.failure?.hasPrefix(Copy.handoffFailed) == true)
        let ok = await f.primary()
        XCTAssertTrue(ok)
        XCTAssertEqual(client.handoffRequests.count, 2)
        XCTAssertEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId, "same entries → same id")
        XCTAssertEqual(client.handoffRequests[1].agent, .codex)
        XCTAssertEqual(client.calls.last, "handoff auth-epic-orchestrator codex \(f.picker.choice.model)")
    }

    func testApiErrorMintsNewRequestID() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        client.failNext = .api(status: 409, code: "conflict", message: "A replacement is already in progress: op_1.")
        _ = await f.primary()
        XCTAssertEqual(f.failure, Copy.handoffFailed + " A replacement is already in progress: op_1.")
        _ = await f.primary()
        XCTAssertNotEqual(client.handoffRequests[0].requestId, client.handoffRequests[1].requestId)
    }

    func testReadyStartUsesStartEndpoint() async {
        let f = await form()
        f.selectedKey = "CHORE-3"
        let ok = await f.primary()
        XCTAssertTrue(ok)
        XCTAssertEqual(client.startBodies.count, 1)
        XCTAssertTrue(client.calls.contains { $0.hasPrefix("start CHORE-3 claude") })
    }

    func testLoadFailureShowsTryAgainAndReloads() async {
        let f = BoardHandoffForm(client: client, settings: state.settings, agents: state.agents, connected: true, preselectAgent: nil)
        client.boardItemsError = .unreachable
        await f.load()
        XCTAssertEqual(f.loadError, Copy.boardItemsLoadFailed)
        XCTAssertEqual(f.primaryLabel, Copy.tryAgain)
        XCTAssertTrue(f.canSubmit, "Try again reruns load()")
        client.boardItemsError = nil
        let closed = await f.primary()
        XCTAssertFalse(closed)
        XCTAssertNil(f.loadError)
        XCTAssertEqual(f.rows.count, 4)
    }

    func testEmptyListDisablesPrimary() async {
        client.boardItemList = []
        let f = await form()
        XCTAssertNil(f.selected)
        XCTAssertFalse(f.canSubmit)
    }

    func testUpdateKeepsSelectionWhileEligible() async {
        let f = await form(preselect: "auth-epic-orchestrator")
        var agents = state.agents
        f.update(agents: agents)
        XCTAssertEqual(f.selectedKey, "EPIC-12")
        agents[0].state = .finished
        f.update(agents: agents)
        XCTAssertNotEqual(f.selectedKey, "EPIC-12", "EPIC-12 is in progress without a live orchestrator → gone")
    }

    func testAppModelPreselectIsConsumedOnce() async {
        let model = AppModel(client: client) // use the same constructor the existing AppModelTests use
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        _ = model.makeBoardHandoffForm()
        XCTAssertNil(model.boardHandoffPreselect)
    }
}
```
Add to `MockDaemonClient`: `public var boardItemsError: DaemonError?` checked in `boardItems()` (`if let e = boardItemsError { throw e }` after `record`) — `failNext` would be consumed by the parallel `catalog()` call, so load-failure tests need this dedicated switch. Adjust `AgentReplacement(...)` and `AppModel(...)` initialisers to the real signatures (see `Wire.swift` and `AppModelTests.swift`); the assertions stay.

Run: `swift test --filter BoardHandoff` → compile FAIL (`BoardHandoffRules` undefined).

- [ ] **Step 5: Implement `BoardHandoff.swift`, Copy, AppModel**

```swift
import Foundation
import Observation

public struct BoardItemRow: Equatable, Sendable, Identifiable {
    public var item: BoardItem
    public var orchestrator: AgentNode?
    public var id: String { item.key }
    public init(item: BoardItem, orchestrator: AgentNode?) { self.item = item; self.orchestrator = orchestrator }
}

public enum BoardHandoffRules {
    static let eligibleTypes: Set<String> = ["epic", "bug", "chore", "spike"]

    public static func rows(_ items: [BoardItem], agents: [AgentNode]) -> [BoardItemRow] {
        let live = AgentTree.flatten(agents).filter { $0.role == .orchestrator && ($0.state == .queued || $0.state == .active) }
        var orchestrated: [BoardItemRow] = [], ready: [BoardItemRow] = []
        for it in items where it.parentKey == nil && eligibleTypes.contains(it.type) {
            if let o = live.first(where: { $0.itemKey == it.key }) {
                orchestrated.append(BoardItemRow(item: it, orchestrator: o))
            } else if it.status == "ready" {
                ready.append(BoardItemRow(item: it, orchestrator: nil))
            }
        }
        return orchestrated + ready
    }

    public static func title(_ r: BoardItemRow) -> String { Copy.boardItemTitle(r.item.key, r.item.title) }

    public static func detail(_ r: BoardItemRow, catalog: [AgentCatalogEntry]) -> String {
        guard let o = r.orchestrator else { return "\(Copy.itemType(r.item.type)) · \(Copy.ready)" }
        let entry = CatalogRules.entry(catalog, o.kind)
        let state = AgentTree.handoffStatus(o) ?? DisplayState(o).label ?? Copy.runningLabel
        return [o.name, CatalogRules.modelLabel(entry, o.model), CatalogRules.previewEffortLabel(entry, o.model, o.effort), state]
            .compactMap { $0 }.joined(separator: " · ")
    }

    public static func canHandOff(_ a: AgentNode, connected: Bool) -> Bool {
        AgentTree.actions(a, tmuxAlive: true, connected: connected).contains { $0.endpoint == .handoff && !$0.disabled }
    }

    public static func offersHandOffTo(_ a: AgentNode, actions: [AgentAction]) -> Bool {
        a.role == .orchestrator && a.parentName == nil && actions.contains { $0.endpoint == .handoff && !$0.disabled }
    }
}

@MainActor
@Observable
public final class BoardHandoffForm {
    public let picker: AgentPickerModel
    public private(set) var rows: [BoardItemRow] = []
    public var selectedKey: String?
    public private(set) var loading = true
    public private(set) var loadError: String?
    public private(set) var failure: String?
    public private(set) var submitting = false
    public var connected: Bool

    private let client: DaemonClient
    private var agents: [AgentNode]
    private var items: [BoardItem] = []
    private var preselectAgent: String?
    private var requestID = UUID().uuidString
    private var lastAttempt: Attempt?

    private struct Attempt: Equatable {
        var key: String, agent: AgentKind, model: String, effort: String?, advisor: AdvisorPayload
    }

    public init(client: DaemonClient, settings: Settings, agents: [AgentNode], connected: Bool, preselectAgent: String?) {
        self.client = client
        self.agents = agents
        self.connected = connected
        self.preselectAgent = preselectAgent
        picker = AgentPickerModel(settings: settings)
    }

    public func load() async {
        loading = true
        loadError = nil
        async let c = try? client.catalog()
        async let i = client.boardItems()
        picker.apply(catalog: await c ?? [])
        do { items = try await i } catch { items = []; loadError = Copy.boardItemsLoadFailed }
        rows = BoardHandoffRules.rows(items, agents: agents)
        if let p = preselectAgent, let row = rows.first(where: { $0.orchestrator?.name == p }) { selectedKey = row.id }
        preselectAgent = nil
        if selected == nil { selectedKey = rows.first?.id }
        loading = false
    }

    public func update(agents: [AgentNode]) {
        self.agents = agents
        rows = BoardHandoffRules.rows(items, agents: agents)
        if selected == nil { selectedKey = rows.first?.id }
    }

    public var selected: BoardItemRow? { rows.first { $0.id == selectedKey } }
    public var isHandoff: Bool { selected?.orchestrator != nil }
    private var queued: Bool { NewOrchestratorForm.wouldQueue(agents, max: picker.settings.maxConcurrentAgents) }
    private var handoffPossible: Bool { selected?.orchestrator.map { BoardHandoffRules.canHandOff($0, connected: connected) } ?? false }

    public var primaryLabel: String {
        if failure != nil || loadError != nil { return Copy.tryAgain }
        if isHandoff { return Copy.handOff }
        return queued ? Copy.queueOrchestrator : Copy.startOrchestrator
    }

    public var caption: String? {
        guard let row = selected else { return nil }
        if row.orchestrator != nil { return handoffPossible ? nil : Copy.handoffUnavailable }
        return queued ? Copy.queuedCaption : nil
    }

    public var canSubmit: Bool {
        if loadError != nil { return connected && !loading }
        return connected && !submitting && !loading && selected != nil && picker.errors.isValid && (!isHandoff || handoffPossible)
    }

    /// true = close the window.
    public func primary() async -> Bool {
        if loadError != nil { await load(); return false }
        guard canSubmit, let row = selected, let agent = picker.choice.agent else { return false }
        let attempt = Attempt(key: row.id, agent: agent, model: picker.choice.model, effort: picker.effortPayload, advisor: picker.advisorPayload)
        if let last = lastAttempt, last != attempt { requestID = UUID().uuidString }
        lastAttempt = attempt
        submitting = true
        defer { submitting = false }
        let base = row.orchestrator != nil ? Copy.handoffFailed : Copy.launchFailed
        do {
            if let orch = row.orchestrator {
                try await client.handoff(orch.name, HandoffRequest(requestId: requestID, agent: agent, model: attempt.model,
                                                                   effort: attempt.effort, advisor: attempt.advisor))
            } else {
                _ = try await client.startOrchestrator(itemKey: row.id, StartOrchestratorBody(requestId: requestID, agent: agent,
                    model: attempt.model, effort: attempt.effort, advisor: attempt.advisor))
            }
            failure = nil
            return true
        } catch let e as DaemonError {
            if case .api = e { requestID = UUID().uuidString; lastAttempt = nil }
            failure = e == .unreachable ? base : base + " " + e.message
            return false
        } catch {
            failure = base
            return false
        }
    }
}
```
`Copy.swift`: add the keys from spec §"All user-facing copy" with the exact texts; `itemType(_:)` maps epic/bug/chore/spike → Epic/Bug/Chore/Spike, others `slug.prefix(1).uppercased() + slug.dropFirst()`; `boardItemTitle(_ key: String, _ title: String) -> String { "\(key) · \(title)" }`.
`AppModel.swift` (beside `makeNewOrchestratorForm`):
```swift
    /// Agent name whose item the next Orchestrate board item window preselects; consumed by makeBoardHandoffForm().
    public var boardHandoffPreselect: String?

    public func makeBoardHandoffForm() -> BoardHandoffForm {
        defer { boardHandoffPreselect = nil }
        return BoardHandoffForm(client: client, settings: state.settings, agents: state.agents, connected: connected,
                                preselectAgent: boardHandoffPreselect)
    }
```

- [ ] **Step 6: Run** — `cd apps/menubar && swift test` → all PASS (BoardHandoff, AgentPicker, NewOrchestrator ported, HTTPDaemonClient, Popover unchanged).

- [ ] **Step 7: Commit**
```bash
cd /Users/alexandertar/GitHub/agent-swarm-board-handoff
git add apps/menubar/Sources/SwarmBarKit/AgentPickerModel.swift apps/menubar/Sources/SwarmBarKit/BoardHandoff.swift \
  apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift apps/menubar/Sources/SwarmBarKit/Wire.swift \
  apps/menubar/Sources/SwarmBarKit/DaemonClient.swift apps/menubar/Sources/SwarmBarKit/HTTPDaemonClient.swift \
  apps/menubar/Sources/SwarmBarKit/AppModel.swift apps/menubar/Sources/SwarmBarKit/Copy.swift \
  apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift apps/menubar/Tests/Fixtures/items.json \
  apps/menubar/Tests/SwarmBarTests/AgentPickerModelTests.swift apps/menubar/Tests/SwarmBarTests/BoardHandoffTests.swift \
  apps/menubar/Tests/SwarmBarTests/NewOrchestratorFormTests.swift apps/menubar/Tests/SwarmBarTests/NewOrchestratorRenderTests.swift \
  apps/menubar/Tests/SwarmBarTests/HTTPDaemonClientTests.swift
git commit -m "feat(menubar): agent picker model, board-item handoff form and client routes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

### Task 3: Menubar UI — AgentPickerGrid, split button, "Hand off to…", board-handoff window

Starts after Task 2's commit.

**Files (under `apps/menubar/`):**
- Create: `Sources/SwarmBarUI/AgentPickerGrid.swift` (`AgentPickerGrid` + `WideOptionPicker` moved from `NewOrchestratorView.swift:158-210, 479-534`)
- Modify: `Sources/SwarmBarUI/NewOrchestratorView.swift` (use `AgentPickerGrid(picker: form.picker)`; delete moved code)
- Create: `Sources/SwarmBarUI/BoardHandoffView.swift`
- Modify: `Sources/SwarmBarUI/Popover/PopoverView.swift` (init param + split footer)
- Modify: `Sources/SwarmBarUI/Popover/AgentsSection.swift` (thread `openBoardHandoff`; context-menu entry)
- Modify: `Sources/SwarmBar/App.swift` (`Window("board-handoff")`, `BoardHandoffHost`, `PopoverHost.openCentered`)
- Create: `Tests/SwarmBarTests/BoardHandoffRenderTests.swift`

**Interfaces:**
- Consumes (Task 2): `AgentPickerModel`, `BoardHandoffForm`, `BoardHandoffRules.title/detail/offersHandOffTo`, `AppModel.boardHandoffPreselect/makeBoardHandoffForm()`, Copy keys.
- Produces: `struct AgentPickerGrid: View { init(picker: AgentPickerModel) }`; `struct BoardHandoffView: View { init(form: BoardHandoffForm, onDone: @escaping () -> Void, onCancel: @escaping () -> Void) }`; `PopoverView.init(model:openNewOrchestrator:openBoardHandoff:openSettings:)` with `openBoardHandoff: @escaping (String?) -> Void = { _ in }`; `WideOptionPicker(_:options:value:icon:detail:onChange:)` (internal).

- [ ] **Step 1: Render test (fails: BoardHandoffView undefined)**

`Tests/SwarmBarTests/BoardHandoffRenderTests.swift`:
```swift
import AppKit
import SwiftUI
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class BoardHandoffRenderTests: XCTestCase {
    private func render(_ form: BoardHandoffForm) -> NSHostingView<BoardHandoffView> {
        let host = NSHostingView(rootView: BoardHandoffView(form: form, onDone: {}, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 300)
        host.layoutSubtreeIfNeeded()
        return host
    }
    private func popups(_ v: NSView) -> [NSPopUpButton] { ((v as? NSPopUpButton).map { [$0] } ?? []) + v.subviews.flatMap(popups) }

    func testItemPopupIsOneLineWithTwoLineMenuRows() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        model.boardHandoffPreselect = "auth-epic-orchestrator"
        let form = model.makeBoardHandoffForm()
        await form.load()
        let host = render(form)
        let item = try XCTUnwrap(popups(host).first { $0.accessibilityLabel() == Copy.item })
        XCTAssertLessThan(item.frame.height, 30, "closed Item popup shows one line")
        XCTAssertEqual(item.titleOfSelectedItem, "EPIC-12 · Authentication")
        let rowTitle = try XCTUnwrap(item.itemArray.first?.attributedTitle?.string)
        XCTAssertTrue(rowTitle.contains("\n"), "menu rows carry a second detail line")
        XCTAssertNotNil(popups(host).first { $0.accessibilityLabel() == Copy.agent }, "shared AgentPickerGrid present")
    }

    func testEmptyStateShowsNoItemsCopy() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        client.boardItemList = []
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        let host = render(form)
        XCTAssertNil(popups(host).first { $0.accessibilityLabel() == Copy.item })
        XCTAssertFalse(form.canSubmit)
    }
}
```
Run: `cd apps/menubar && swift test --filter BoardHandoffRender` → compile FAIL.

- [ ] **Step 2: Extract `AgentPickerGrid` + `WideOptionPicker`**

Move `agentFields`, `advisorAgentValue`, `advisorModelValue` from `NewOrchestratorView` into:
```swift
import SwarmBarKit
import SwiftUI

struct AgentPickerGrid: View {
    let picker: AgentPickerModel
    var body: some View { /* former agentFields body with `form.picker.` → `picker.` */ }
    private var advisorAgentValue: String { /* moved */ }
    private var advisorModelValue: String { /* moved */ }
}
```
`NewOrchestratorView` shows `AgentPickerGrid(picker: form.picker)` where `agentFields` was. Move `WideOptionPicker` (drop `private`) and add:
```swift
    let detail: ((PickerOption) -> NSAttributedString?)?
    // init gains `detail: ((PickerOption) -> NSAttributedString?)? = nil` before onChange
```
In `updateNSView`, when building items: if `let d = detail?(option)`, set
```swift
let title = NSMutableAttributedString(string: option.label, attributes: [.font: NSFont.systemFont(ofSize: NSFont.systemFontSize)])
title.append(NSAttributedString(string: "\n"))
title.append(d)
item.attributedTitle = title
```
and after selection, when `detail != nil`: `(popup.cell as? NSPopUpButtonCell)?.usesItemFromMenu = false; (popup.cell as? NSPopUpButtonCell)?.menuItem = NSMenuItem(title: popup.selectedItem.flatMap { $0.representedObject as? String }.flatMap { v in displayed.first { $0.value == v }?.label } ?? "", action: nil, keyEquivalent: "")`. Do the same reset in `Coordinator.changed(_:)` (give the coordinator a `var lineOne: (String) -> String` set in `updateNSView`). Line 2 is built by the caller (BoardHandoffView) per spec §"Swift — SwarmBarUI / app": `.smallSystemFontSize`, `NSColor.secondaryLabelColor`, agent icon `NSTextAttachment(image: Icons.image(IconName(kind)))` prefix for orchestrator rows.

Run: `swift test --filter NewOrchestratorRender` → PASS (unchanged layout).

- [ ] **Step 3: `BoardHandoffView`** — layout per spec §"Screens" (padding 22 h / 12 v, footer divider, same structure as `NewOrchestratorView`'s footer):
```swift
import AppKit
import SwarmBarKit
import SwiftUI

public struct BoardHandoffView: View {
    let form: BoardHandoffForm
    let onDone: () -> Void
    let onCancel: () -> Void

    public init(form: BoardHandoffForm, onDone: @escaping () -> Void, onCancel: @escaping () -> Void) {
        self.form = form; self.onDone = onDone; self.onCancel = onCancel
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                if let failure = form.failure {
                    Text("⚠ \(failure)").font(.callout).foregroundStyle(.red)
                }
                HStack {
                    Text(Copy.item).frame(width: 70, alignment: .leading)
                    if form.loadError != nil {
                        Text(Copy.boardItemsLoadFailed).foregroundStyle(.red)
                    } else if !form.loading && form.rows.isEmpty {
                        Text(Copy.noBoardItems).foregroundStyle(.secondary)
                    } else {
                        WideOptionPicker(Copy.item,
                                         options: form.rows.map { PickerOption($0.id, BoardHandoffRules.title($0)) },
                                         value: form.selectedKey ?? "",
                                         detail: { opt in form.rows.first { $0.id == opt.value }.map(detailLine) }) { form.selectedKey = $0 }
                            .frame(maxWidth: .infinity)
                            .disabled(form.loading)
                    }
                }
                AgentPickerGrid(picker: form.picker)
            }
            .padding(.horizontal, 22).padding(.vertical, 12)
            Divider()
            HStack {
                if let caption = form.caption { Text(caption).font(.caption).foregroundStyle(.secondary) }
                Spacer()
                Button(Copy.cancel, action: onCancel).keyboardShortcut(.cancelAction)
                Button(form.primaryLabel) { Task { if await form.primary() { onDone() } } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!form.canSubmit)
            }
            .padding(.horizontal, 22).padding(.vertical, 12)
        }
    }

    private func detailLine(_ row: BoardItemRow) -> NSAttributedString {
        let attrs: [NSAttributedString.Key: Any] = [.font: NSFont.systemFont(ofSize: NSFont.smallSystemFontSize),
                                                    .foregroundColor: NSColor.secondaryLabelColor]
        let out = NSMutableAttributedString()
        if let kind = row.orchestrator?.kind, let image = Icons.image(IconName(kind)) {
            let a = NSTextAttachment(); a.image = image
            a.bounds = CGRect(x: 0, y: -2, width: 12, height: 12)
            out.append(NSAttributedString(attachment: a)); out.append(NSAttributedString(string: " ", attributes: attrs))
        }
        out.append(NSAttributedString(string: BoardHandoffRules.detail(row, catalog: form.picker.catalog), attributes: attrs))
        return out
    }
}
```
Match `Icons.image`'s real signature (optional or not) and `IconName(kind)` as used in `NewOrchestratorView`.

Run: `swift test --filter BoardHandoffRender` → PASS.

- [ ] **Step 4: Popover split button + row menu entry**

`PopoverView`: add `let openBoardHandoff: (String?) -> Void` with the init default `{ _ in }` (so `PopoverRenderTests` compiles unchanged); footer:
```swift
            HStack(spacing: 0) {
                Button { openNewOrchestrator() } label: { Label(Copy.newOrchestrator, systemImage: "plus") }
                Menu {
                    Button(Copy.orchestrateBoardItemMenu) { openBoardHandoff(nil) }
                } label: { Image(systemName: "chevron.down") }
                    .menuStyle(.borderlessButton)
                    .menuIndicator(.hidden)
                    .frame(width: 18)
                    .help(Copy.moreStartOptions)
                    .accessibilityLabel(Copy.moreStartOptions)
            }
            .disabled(!model.connected)
```
Pass `openBoardHandoff` into `AgentsSection` → `AgentRowView` (new stored `let openBoardHandoff: (String?) -> Void`). In `AgentRowView.contextMenu`:
```swift
            ForEach(actions) { a in
                Button(a.label) { run(a) }.disabled(a.disabled)
                if a.endpoint == .handoff, BoardHandoffRules.offersHandOffTo(agent, actions: actions) {
                    Button(Copy.handOffTo) { openBoardHandoff(agent.name) }
                }
            }
```

- [ ] **Step 5: Window scene + host (`App.swift`)**

Extract the centering block of `PopoverHost.openNewOrchestrator` into
```swift
    private func openCentered(_ id: String) {
        StatusItemWatcher.dismissPopover()
        NSApp.activate(ignoringOtherApps: true)
        openWindow(id: id)
        // macOS restores the last frame on reopen; always center it on the active screen.
        DispatchQueue.main.async {
            guard let w = NSApp.windows.first(where: { $0.identifier?.rawValue.contains(id) == true }),
                  let screen = NSScreen.main?.visibleFrame else { return }
            w.setFrameOrigin(NSPoint(x: screen.midX - w.frame.width / 2, y: screen.midY - w.frame.height / 2))
        }
    }
```
`openNewOrchestrator: { openCentered("new-orchestrator") }`, `openBoardHandoff: { name in model.boardHandoffPreselect = name; openCentered("board-handoff") }`. Scene:
```swift
        Window(Copy.orchestrateBoardItem, id: "board-handoff") {
            BoardHandoffHost(model: delegate.model)
        }
        .windowResizability(.contentSize)
        .defaultSize(width: 820, height: 300)
        .defaultPosition(.center)
```
Host (mirrors `NewOrchestratorHost`):
```swift
struct BoardHandoffHost: View {
    let model: AppModel
    @State private var form: BoardHandoffForm?
    @Environment(\.dismissWindow) private var dismissWindow

    var body: some View {
        Group {
            if let form {
                BoardHandoffView(form: form,
                                 onDone: { dismissWindow(id: "board-handoff"); Task { await model.refresh() } },
                                 onCancel: { dismissWindow(id: "board-handoff") })
            } else {
                ProgressView().frame(width: 480, height: 200)
            }
        }
        .onAppear { reopen() }
        .onDisappear { form = nil }
        // "Hand off to…" while the window is already open: rebuild with the new preselection.
        .onChange(of: model.boardHandoffPreselect) { _, name in if name != nil { reopen() } }
        .onChange(of: model.state.agents) { _, a in form?.update(agents: a) }
        .onChange(of: model.connected) { _, up in form?.connected = up }
    }

    private func reopen() {
        let f = model.makeBoardHandoffForm()
        form = f
        Task { await f.load() }
    }
}
```
If `AgentNode`/`[AgentNode]` is not `Equatable` for `onChange`, it is (`AgentNode: Equatable`); keep as written.

- [ ] **Step 6: Run** — `cd apps/menubar && swift build && swift test` → all PASS. Manual smoke (spec §"Verification" 6, scenarios a, c, d, h, i) on `make dev` + `SWARM_URL=http://127.0.0.1:17777 swift run SwarmBar`.

- [ ] **Step 7: Commit**
```bash
cd /Users/alexandertar/GitHub/agent-swarm-board-handoff
git add apps/menubar/Sources/SwarmBarUI/AgentPickerGrid.swift apps/menubar/Sources/SwarmBarUI/BoardHandoffView.swift \
  apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift apps/menubar/Sources/SwarmBarUI/Popover/PopoverView.swift \
  apps/menubar/Sources/SwarmBarUI/Popover/AgentsSection.swift apps/menubar/Sources/SwarmBar/App.swift \
  apps/menubar/Tests/SwarmBarTests/BoardHandoffRenderTests.swift
git commit -m "feat(menubar): orchestrate board item window, split button and Hand off to…

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

## Final verification (after Task 3)

1. `go vet ./... && gofmt -l internal/ && go test -race ./internal/...`
2. `make test-go`
3. `cd apps/menubar && swift test`, then `make test-menubar`
4. Manual scenarios (a)-(l) from spec §"Verification".

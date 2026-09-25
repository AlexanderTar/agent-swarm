# Spike Native Approvals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden `swarm_artifact`/`swarm_ask` MCP schemas and document the per-section native-question approval loop.

**Architecture:** Reuse the existing `objSchemaRequired` helper for both schemas (same pattern `swarm_checkpoint` already uses); document the orchestrator-side native loop in the spike skill section and mirror it via `skills-sync`. No DB, wire-result, hook, or web changes.

**Tech Stack:** Go (MCP server), Markdown skills, existing `go test` suites.

**Spec:** `docs/specs/2026-09-25-spike-native-approvals.md`

## Global Constraints

- No required-field or description change may alter a result shape — schemas only.
- `kind` enum for artifacts is exactly `[spec, plan, debug_report, note, design, research]`.
- Native copy is verbatim from the spec (header `Spike approval`, options `Approve` / `Request changes`).
- Only top-level orchestrators raise native questions; skill text must say so.
- Work in the `claude/spike-native-approvals` worktree; leave `main` untouched until review.

## TDD Contract (binds every task below)

- No production edit before its RED test is written and observed failing.
- RED must fail for the specified reason (missing `required`, stored `relay`),
  not a compile error or typo — read the failure lines before proceeding.
- GREEN is the minimal `Schema:`-line or default-block change; no refactors,
  no adjacent cleanups.
- Every GREEN run re-runs the neighboring tests named in its step (revise,
  idempotency, same-root sends) — a green file is not a green package.
- Refactor only after GREEN, keeping tests green; no behavior may change.

## Review Focus

- `swarm_artifact` called with no `item` — must error naming the item, not proceed. (Task 1 test.)
- `swarm_artifact` called with `kind: "spec"` vs `"banana"` — banana must fail enum validation client-side and server-side accepts only known kinds. (Task 1 test asserts enum contents.)
- `swarm_ask` approval with no `artifact` — backend already rejects (`TestAskApprovalValidatesTheArtifact`); schema now marks the requirement. (Task 2 test asserts required contains `kind`.)
- `swarm_artifact revise` on unchanged file — still succeeds, revision bumps (existing `TestArtifactToolSchemaAllowsRevise` guards; re-run).
- Skill reader on a legacy task — text must scope the native loop to spikes without breaking legacy instructions. (Task 3: read-back check, no code.)
- Agent omits `kind` on `swarm_send` — must store `finding`, never the daemon-only `relay`. (Task 5C test sends both omitted and explicit-`relay`.)
- Explicit `kind: "banana"` on `swarm_send` — passes schema enum today and stores verbatim; out of scope (no handler validation), documented as known gap, not silently fixed.
- `swarm_materialize` without `spike` — backend already errors (`spike is required`); schema now declares it. (Task 5B table test.)
- Required-fields added to a schema whose callers omit them — safe because the server never enforces `required` (decode-only); only validating clients see it. (No test; stated invariant.)

## File Structure

- `internal/mcpserver/orchestrator.go` (~line 253): `artifactTool` schema only.
- `internal/mcpserver/tools.go` (~line 151): `askTool` schema only.
- `internal/mcpserver/orchestrator_test.go` / `tools_test.go`: schema-shape tests only.
- `skills/swarm-orchestrator/SKILL.md` spike section + `internal/install/skills/` mirror via `make skills-sync`.
- Task 5 reuses the same four Go files: `tools.go` gains blocker/send/advise/instructions/kb `required` plus the send default change; `orchestrator.go` gains items/worktree/control/role_overrides/materialize `required`; tests gain `TestSharedToolSchemasDeclareRequired`, `TestOrchestratorToolSchemasDeclareRequired`, `TestSendDefaultsOmittedKindToFinding`. No new files.

---

### Task 1: `swarm_artifact` schema requires fields and describes values

**Files:**
- Modify: `internal/mcpserver/orchestrator.go` (artifactTool Schema)
- Test: `internal/mcpserver/orchestrator_test.go` (new `TestArtifactToolSchemaRequiresFields`)

**Interfaces:**
- Consumes: `objSchemaRequired(props, required)` from `internal/mcpserver/tools.go:38`
- Produces: unchanged result shape `{artifact_id, revision, sections, stale_requests, warnings}`

- [x] **Step 1: Write the failing test**

```go
// swarm_artifact's guessing loop (muse calling repeatedly) comes from a
// schema with no required fields and no value hints. Pin both.
func TestArtifactToolSchemaRequiresFields(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Op struct {
				Enum []string `json:"enum"`
			} `json:"op"`
			Kind struct {
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"kind"`
			Path struct {
				Description string `json:"description"`
			} `json:"path"`
			Item struct {
				Description string `json:"description"`
			} `json:"item"`
		} `json:"properties"`
	}
	for _, d := range s.ToolsFor(seed.Caller) {
		if d.Name == "swarm_artifact" {
			if err := json.Unmarshal(d.Schema, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, want := range []string{"op", "item", "kind", "path"} {
		if !slices.Contains(schema.Required, want) {
			t.Errorf("swarm_artifact required = %v, want %q", schema.Required, want)
		}
	}
	for _, want := range []string{"spec", "plan", "debug_report", "note", "design", "research"} {
		if !slices.Contains(schema.Properties.Kind.Enum, want) {
			t.Errorf("swarm_artifact kind enum = %v, want %q", schema.Properties.Kind.Enum, want)
		}
	}
	if schema.Properties.Path.Description == "" || schema.Properties.Item.Description == "" {
		t.Errorf("swarm_artifact path/item need descriptions, got %q / %q",
			schema.Properties.Path.Description, schema.Properties.Item.Description)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcpserver/ -run 'TestArtifactToolSchemaRequiresFields' -v`
Expected: FAIL — `required = []`, empty descriptions.

- [x] **Step 3: Write minimal implementation**

```go
Schema: objSchemaRequired(`"op":{"type":"string","enum":["register","revise"]},"item":{"type":"string","description":"Top-level item KEY (not id) the artifact belongs to"},
	"kind":{"type":"string","enum":["spec","plan","debug_report","note","design","research"],"description":"Artifact kind, not a filename"},
	"path":{"type":"string","description":"Existing file under ~/.swarm/specs or ~/.swarm/plans with '## '-headed sections; plans end with a `+"```swarm-tree`"+` block"},
	"request_id":{"type":"string"}`,
	[]string{"op", "item", "kind", "path"}),
```

(Keep the existing §8.1 comment above it; only the `Schema:` line changes.)

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcpserver/ -run 'TestArtifactToolSchemaRequiresFields|TestArtifactToolSchemaAllowsRevise' -v`
Expected: PASS, plus existing revise test still green.

### Task 2: `swarm_ask` schema requires `kind` and documents approval fields

**Files:**
- Modify: `internal/mcpserver/tools.go` (askTool Schema)
- Test: `internal/mcpserver/tools_test.go` (new `TestAskToolSchemaRequiresKind`)

**Interfaces:**
- Consumes: `objSchemaRequired` (same helper)
- Produces: unchanged result shape `{request_id, state}`

- [x] **Step 1: Write the failing test**

```go
func TestAskToolSchemaRequiresKind(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Artifact struct {
				Description string `json:"description"`
			} `json:"artifact"`
			Section struct {
				Description string `json:"description"`
			} `json:"section"`
		} `json:"properties"`
	}
	for _, d := range s.ToolsFor(seed.Caller) {
		if d.Name == "swarm_ask" {
			if err := json.Unmarshal(d.Schema, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !slices.Contains(schema.Required, "kind") {
		t.Errorf("swarm_ask required = %v, want kind", schema.Required)
	}
	if schema.Properties.Artifact.Description == "" || schema.Properties.Section.Description == "" {
		t.Errorf("swarm_ask artifact/section need descriptions")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcpserver/ -run 'TestAskToolSchemaRequiresKind' -v`
Expected: FAIL — no `required`, empty descriptions.

- [x] **Step 3: Write minimal implementation**

Change only the `Schema:` line of `askTool` to `objSchemaRequired` with
`[]string{"kind"}`, adding `"description":"Kind: question, approval, confirm_repos, or withdraw"` to `kind`,
`"description":"Artifact id for approval kinds"` to `artifact`, and
`"description":"Section id for per-section approval"` to `section`.

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcpserver/ -run 'TestAskToolSchemaRequiresKind' -v`
Expected: PASS.

### Task 3: Document the per-section native-question loop in the spike skill

**Files:**
- Modify: `skills/swarm-orchestrator/SKILL.md` (spike section, after the `swarm_artifact revise` bullet)
- Mirror: `internal/install/skills/swarm-orchestrator/SKILL.md` via `make skills-sync` (config/docs change — no TDD test)

**Interfaces:**
- Consumes: spec copy (header `Spike approval`, options `Approve` / `Request changes`)
- Produces: skill text the orchestrator follows; mirror identical via sync

- [x] **Step 1: Add the subsection** (exact text, no placeholders)

```markdown
- Approving through native question tools: in addition to each `swarm_ask`
  approval above, present it through your own native question tool — one
  prompt per spec section (never batch sections), one for the plan (append
  any plan warnings), one for a debug report. Header `Spike approval`;
  question `Approve <Kind> section "<Title>" (rev <N>)?` with single-select
  options `Approve` / `Request changes` (free text allowed). Forward each
  native answer into the matching `swarm_ask` outcome (approve /
  request-changes / withdraw): the native answer alone records context, only
  the daemon `approval_result` counts. Only a top-level (parentless)
  orchestrator may do this — parented agents keep reporting via `swarm_send`
  — and only in an interactive TUI session (headless runs have no dialog).
  Per-agent tool: claude `AskUserQuestion`, codex `request_user_input`,
  cursor/agy `ask_question`, muse `request_user_input`.
```

- [x] **Step 2: Sync the installed mirror**

Run: `make skills-sync`
Verify: `diff -q skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md` prints IDENTICAL (it already does today; keep it so).

### Task 4: Full verification (no new code)

- [x] **Step 1: Run affected suites**

Run: `go test ./internal/mcpserver/ ./internal/runtime/ ./internal/hook/`
Expected: all PASS.

- [x] **Step 2: Gates**

Run: `go vet ./internal/mcpserver/ && gofmt -l internal/mcpserver/ skills 2>/dev/null; git status --short`
Expected: vet clean, no gofmt output on touched `.go` files, only intended files dirty.

- [x] **Step 3: Existing e2e coverage check (no change)**

`./scripts/e2e.sh -run 'TestScenario01HappyFeatureSpike'` PASS (0.77s,
subagent-run, full tail quoted) — per-section approval flow holds after the
schema hardening. No code change needed.

---

### Batch 1: All schema `required` (shared + orchestrator) — one RED, one GREEN

Batching rationale: schema edits change metadata only (the server never
enforces `required`; decode-only), so ten one-line edits carry no
behavioral risk to isolate. One RED build for both table tests, one GREEN
build for all ten edits — two cycles instead of four. The behavior change
(relay default) stays in its own batch.

**Files:**
- Modify: `internal/mcpserver/tools.go` (blocker/send/advise/instructions/kb/read/sync schemas only)
- Modify: `internal/mcpserver/orchestrator.go` (items/worktree/control/role_overrides/materialize schemas only)
- Test: `internal/mcpserver/tools_test.go` (new `TestSharedToolSchemasDeclareRequired`)
- Test: `internal/mcpserver/orchestrator_test.go` (new `TestOrchestratorToolSchemasDeclareRequired`)

**Interfaces:**
- Consumes: `objSchemaRequired` (`tools.go:38`)
- Produces: unchanged result shapes; `required` metadata only (plus
  `swarm_read`/`swarm_sync` descriptions)

- [x] **Step 1: Write both failing tests**

```go
// Every shared tool must declare what it actually needs: clients that
// validate arguments before sending can only see the schema, so an omitted
// `required` turns every missing field into a runtime round-trip. sync/read
// stay all-optional by design (any filter combo is valid) — pinned here too.
func TestSharedToolSchemasDeclareRequired(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	required := map[string][]string{}
	for _, d := range s.ToolsFor(seed.Caller) {
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(d.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		required[d.Name] = schema.Required
	}
	for tool, want := range map[string][]string{
		"swarm_blocker":      {"reason"},
		"swarm_send":         {"to", "body"},
		"swarm_advise":       {"question"},
		"swarm_instructions": {"op"},
		"swarm_kb":           {"op"},
	} {
		got, ok := required[tool]
		if !ok {
			t.Errorf("%s not visible to orchestrator caller", tool)
			continue
		}
		for _, w := range want {
			if !slices.Contains(got, w) {
				t.Errorf("%s required = %v, want %q", tool, got, w)
			}
		}
	}
	for _, tool := range []string{"swarm_sync", "swarm_read"} {
		if got := required[tool]; len(got) != 0 {
			t.Errorf("%s required = %v, want empty (all-optional by design)", tool, got)
		}
	}
}
```

plus the orchestrator table test in `orchestrator_test.go`:

```go
// Same contract as the shared tools: every orchestrator tool declares the
// fields its handler actually rejects when empty, so validating clients fail
// fast instead of guessing through runtime round-trips.
func TestOrchestratorToolSchemasDeclareRequired(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	required := map[string][]string{}
	for _, d := range s.ToolsFor(seed.Caller) {
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(d.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		required[d.Name] = schema.Required
	}
	for tool, want := range map[string][]string{
		"swarm_items":          {"op"},
		"swarm_worktree":       {"op"},
		"swarm_control":        {"target", "action"},
		"swarm_role_overrides": {"op"},
		"swarm_materialize":    {"spike"},
	} {
		got, ok := required[tool]
		if !ok {
			t.Errorf("%s not visible to orchestrator caller", tool)
			continue
		}
		for _, w := range want {
			if !slices.Contains(got, w) {
				t.Errorf("%s required = %v, want %q", tool, got, w)
			}
		}
	}
}
```

- [x] **Step 2: Run once — verify both fail**

Run: `go test ./internal/mcpserver/ -run 'TestSharedToolSchemasDeclareRequired|TestOrchestratorToolSchemasDeclareRequired' -v`
Expected: FAIL on all ten tools, each reporting `required = []`. Read the
lines: a missing tool name (not visible) or a compile error is the wrong
failure — fix the test, not the schema.

- [x] **Step 3: Write minimal implementation (all ten edits)**

One `Schema:` line each, nothing else.
`tools.go`: blocker → `["reason"]`; send → `["to", "body"]` + `to`
description `Recipient agent name, or "parent" for your orchestrator`;
advise → `["question"]`; instructions → `["op"]`; both `swarm_kb`
variants → `["op"]`; read/sync → descriptions only
(`refs`/`filter`/`repos`/`since_seq`; `ack`/`limit`), no `required`.
`orchestrator.go`: items → `["op"]`, worktree → `["op"]`,
control → `["target", "action"]`, role_overrides → `["op"]`,
materialize → `["spike"]`. (Conditional set/clear and per-op fields stay
optional — the schema cannot express conditionals.)

- [x] **Step 4: Run once — verify both pass plus neighbors**

Run: `go test ./internal/mcpserver/ -run 'TestSharedToolSchemasDeclareRequired|TestOrchestratorToolSchemasDeclareRequired|TestArtifactToolSchemaAllowsRevise' -v`
Expected: PASS.

### Batch 2: `swarm_send` omitted/explicit-relay stores `finding` (behavior change, isolated)

**Files:**
- Modify: `internal/mcpserver/tools.go` (sendTool default only)
- Test: `internal/mcpserver/tools_test.go` (new `TestSendDefaultsOmittedKindToFinding`)

**Interfaces:**
- Consumes: `s.call` + direct DB read of `messages.kind`
- Produces: agent-origin sends never store `relay`

Background (researched, not guessed): `relay` is a daemon-origin kind — all
12 non-test producers enqueue with `Origin:"daemon"`. An omitted `kind`
currently stores `Kind:relay` + `Origin:agent`, which `summarizeFor` renders
as `" from  ()"` garbage. Requiring `kind` is rejected: 4 existing tests
plus `server_test.go:247` and live agents omit it and expect success.

- [x] **Step 1: Write the failing test**

```go
// Omitted kind (and explicit relay, which no agent can ever mean — it is a
// daemon-origin kind) must store finding, the generic agent message kind.
func TestSendDefaultsOmittedKindToFinding(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	for _, body := range []string{
		`{"to":"` + seed.Caller.AgentName + `","body":"no kind"}`,
		`{"to":"` + seed.Caller.AgentName + `","kind":"relay","body":"explicit relay"}`,
	} {
		if _, err := s.call(ctx, seed.Caller, "swarm_send", body); err != nil {
			t.Fatal(err)
		}
	}
	var kinds []string
	rows, err := s.RT.DB.QueryContext(ctx, `SELECT kind FROM messages WHERE to_agent_id = ? ORDER BY seq`, seed.Caller.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 {
		t.Fatalf("stored kinds = %v, want exactly 2 messages", kinds)
	}
	for _, k := range kinds {
		if k != "finding" {
			t.Errorf("stored kind = %q, want finding", k)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcpserver/ -run 'TestSendDefaultsOmittedKindToFinding' -v`
Expected: FAIL — stored `relay`.

- [x] **Step 3: Write minimal implementation**

In `sendTool` handler, replace the default block:

```go
kind := in.Kind
if kind == "" || kind == "relay" {
	kind = "finding"
}
```

plus a `kind` description noting the default. Nothing else changes.

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcpserver/ -run 'TestSendDefaultsOmittedKindToFinding|TestSendSucceedsToAnAgentInTheSameRoot|TestSendRequestIDReplaysInsteadOfSendingTwice' -v`
Expected: PASS — existing kind-less callers keep working, now storing `finding`.

### Batch 3: Full gate for Task 5 (no new code)

- [x] **Step 1: Run affected suites**

Run: `go test ./internal/mcpserver/ ./internal/runtime/ ./internal/hook/`
Expected: all PASS (relay suites inbox/text/workflow included via runtime).

- [x] **Step 2: Gates**

Run: `go vet ./internal/mcpserver/ && gofmt -l internal/mcpserver/; git status --short`
Expected: vet clean, no gofmt output, only the 8 intended files dirty
(`tools.go`, `orchestrator.go`, both test files, both skill files, plan, spec).

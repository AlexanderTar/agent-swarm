# Implementation Plan: Advisor Timeout, Model-to-Agent Resolution, Claude MCP Inheritance, and UI Role Overrides

**Date:** 2026-09-22  
**Spec:** `docs/specs/2026-09-22-advisor-timeout-role-overrides-and-mcp-inheritance.md`  

---

### Task 1: Fix `execx.Run` Deadline Truncation & Wire Advisor 240s Timeout

**Goal:** Ensure `execx.Run` does not truncate an existing caller context deadline, and wire `cmd/swarm/daemon.go` to provide the full 240s timeout to `advisor.Service`.

**Files:**
- Modify: `internal/execx/execx.go`
- Modify: `internal/execx/execx_test.go`
- Modify: `cmd/swarm/daemon.go`

**Step 1.1: Write failing test in `internal/execx/execx_test.go`**
Add test asserting that when a context with a 500ms deadline is passed, `Run` respects it and does not replace it with an arbitrary timeout.
```go
func TestRunWithLongerCallerDeadlineDoesNotTruncate(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
    defer cancel()
    start := time.Now()
    _, err := Run(ctx, "sleep", "1")
    if err == nil {
        t.Fatal("expected timeout error")
    }
    dur := time.Since(start)
    if dur > 500*time.Millisecond {
        t.Fatalf("expected command to abort near caller deadline ~200ms, took %v", dur)
    }
}
```

**Step 1.2: Run test to watch it pass/fail**
`go test ./internal/execx -run TestRunWithLongerCallerDeadlineDoesNotTruncate`

**Step 1.3: Update `internal/execx/execx.go`**
```go
func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
```

**Step 1.4: Update `cmd/swarm/daemon.go:244`**
Change `Run: execx.Run` to `Run: execx.RunFor(240 * time.Second)` in the `advisor.Service` struct literal.

**Step 1.5: Verify and commit**
`go test ./internal/execx/... ./internal/advisor/...`
Commit: `git commit -m "fix(advisor): respect caller deadline and give advisor 240s timeout"`

---

### Task 2: Codex Advisor MCP Isolation

**Goal:** Pass `-c mcp_servers={}` to `codex exec` in `AdvisorCommand()` so Codex does not inherit external MCP servers in read-only advisor mode.

**Files:**
- Modify: `internal/advisor/run.go`
- Modify: `internal/advisor/run_test.go`

**Step 2.1: Write failing test in `internal/advisor/run_test.go`**
Assert that `AdvisorCommand(runtime.Codex, "gpt-6-astra", "medium", dir, prompt)` contains `-c` and `mcp_servers={}`.

**Step 2.2: Implement in `internal/advisor/run.go`**
In `AdvisorCommand`:
```go
	case runtime.Codex:
		a := []string{"codex", "exec", "-s", "read-only", "--skip-git-repo-check", "-c", "mcp_servers={}", "-m", model}
		if effort != "" {
			a = append(a, "-c", `model_reasoning_effort="`+effort+`"`)
		}
		return append(a, "-o", filepath.Join(dir, "answer.md"), prompt), nil
```

**Step 2.3: Verify and commit**
`go test ./internal/advisor/...`
Commit: `git commit -m "fix(advisor): isolate codex advisor from user mcp servers"`

---

### Task 3: Claude Worker MCP Server Inheritance

**Goal:** In `claude.go`, read `mcpServers` from `~/.claude.json` (if present) and merge with `swarm` so Claude worker sessions inherit user MCP servers.

**Files:**
- Modify: `internal/adapter/claude.go`
- Modify: `internal/adapter/claude_test.go`

**Step 3.1: Write failing test in `internal/adapter/claude_test.go`**
Create a test where `d.UserHome` has a `.claude.json` containing `{"mcpServers":{"neon":{"type":"http","url":"https://mcp.neon.tech"}}}`. Assert that the generated `claude-mcp.json` contains both `neon` and `swarm`.

**Step 3.2: Implement in `internal/adapter/claude.go`**
In `flags(s Spec)`:
- Read `filepath.Join(c.d.UserHome, ".claude.json")`.
- If valid JSON with `.mcpServers` (as a `map[string]any`), copy the map.
- Set `servers["swarm"] = map[string]any{"type": "stdio", "command": s.Bin, "args": []string{"mcp"}}`.
- Marshal and write `claude-mcp.json`.

**Step 3.3: Verify and commit**
`go test ./internal/adapter/... -run TestClaude`
Commit: `git commit -m "feat(claude): inherit user mcp servers in claude sessions"`

---

### Task 4: Worker Model-to-Agent Resolution & Role Normalization

**Goal:** When `swarm_spawn` provides `model` without `agent`, resolve the agent kind from the model across catalog models. Normalize `role: "implementer"` to `RoleCoder`.

**Files:**
- Modify: `internal/runtime/agents.go`
- Modify: `internal/runtime/agents_test.go`
- Modify: `internal/mcpserver/orchestrator.go`
- Modify: `internal/install/skills/swarm-orchestrator/SKILL.md`

**Step 4.1: Write failing test in `internal/runtime/agents_test.go`**
Spawn a worker with `Model: "gpt-6-astra"` and `Kind: ""` and `Role: "implementer"`. Verify that `Kind` resolves to `Codex` and `Role` resolves to `RoleCoder`.

**Step 4.2: Implement `resolveAgentForModel` in `internal/runtime/agents.go`**
```go
func (s *Store) resolveAgentForModel(ctx context.Context, modelID string) (AgentKind, bool) {
    cfg, err := s.Settings.Get(ctx)
    if err != nil {
        return "", false
    }
    for _, kind := range cfg.EnabledAgents {
        models, _, err := s.Catalog.ModelsFor(ctx, kind)
        if err != nil {
            continue
        }
        if _, found := catalog.Find(models, modelID); found {
            return kind, true
        }
    }
    return "", false
}
```
In `Spawn()`:
```go
    if in.Role == "implementer" {
        in.Role = RoleCoder
    }
    if in.Kind == "" && in.Model != "" {
        if resolved, ok := s.resolveAgentForModel(ctx, in.Model); ok {
            in.Kind = resolved
        }
    }
```

**Step 4.3: Update MCP tool description in `internal/mcpserver/orchestrator.go` & `SKILL.md`**
Clarify that `agent` is an optional override and that `model` automatically resolves `agent`.

**Step 4.4: Verify and commit**
`go test ./internal/runtime/... ./internal/mcpserver/...`
Commit: `git commit -m "feat(runtime): resolve agent from model and normalize implementer role"`

---

### Task 5: Database Schema & Runtime Persistence for Role Overrides

**Goal:** Add `role_overrides TEXT` to `agents` table, save role overrides in `StartOrchestrator` and `StartSpike`, and make `Spawn()` inherit them for child workers.

**Files:**
- Create: `internal/db/schema/0007_add_agent_role_overrides.sql`
- Modify: `internal/runtime/model.go`
- Modify: `internal/runtime/agents.go`
- Modify: `internal/runtime/agents_test.go`

**Step 5.1: Create migration `0007_add_agent_role_overrides.sql`**
```sql
ALTER TABLE agents ADD COLUMN role_overrides TEXT;
```

**Step 5.2: Update `internal/runtime/model.go`**
Add `Roles map[Role]settings.RoleDefault` to `OrchestratorInput` and `SpikeInput`. Add `RoleOverrides map[Role]settings.RoleDefault` to `Agent`.

**Step 5.3: Update `internal/runtime/agents.go`**
- In `StartOrchestrator` and `StartSpike`: serialize `in.Roles` to JSON and save in `role_overrides`.
- In `Spawn()`: if `in.Kind == ""` and `in.Model == ""`, check parent agent's `role_overrides[in.Role]` before checking `cfg.Roles[in.Role]`.

**Step 5.4: Write tests in `internal/runtime/agents_test.go`**
Start an orchestrator with role overrides (e.g. `RoleCoder: {Agent: Codex, Model: "gpt-6-astra"}`). Spawn a coder under that orchestrator without specifying agent/model. Verify child inherits `Codex` and `gpt-6-astra`.

**Step 5.5: Verify and commit**
`go test ./internal/db/... ./internal/runtime/...`
Commit: `git commit -m "feat(runtime): persist and inherit orchestrator role overrides"`

---

### Task 6: HTTP API Support for Role Overrides

**Goal:** Accept `roles` in `POST /api/items/{key}/orchestrator` and `POST /api/spikes`.

**Files:**
- Modify: `internal/httpapi/spawn.go`
- Modify: `internal/httpapi/spawn_test.go`

**Step 6.1: Write failing test in `internal/httpapi/spawn_test.go`**
Post `POST /api/items/{key}/orchestrator` with `"roles": {"coder": {"agent": "codex", "model": "gpt-6-astra"}}`. Assert that the orchestrator starts and stores the role overrides.

**Step 6.2: Implement in `internal/httpapi/spawn.go`**
Add `Roles map[string]roleDefaultBody `json:"roles"`` to `orchestratorRequestBody` and `spikeRequestBody`. Convert to `map[runtime.Role]settings.RoleDefault` and pass to `s.RT.StartOrchestrator` and `s.RT.StartSpike`.

**Step 6.3: Verify and commit**
`go test ./internal/httpapi/...`
Commit: `git commit -m "feat(httpapi): support role overrides in orchestrator and spike routes"`

---

### Task 7: Web UI Role Overrides in `SpawnSheet` & `NewSpikeSheet`

**Goal:** Allow users in the UI to override agent, model, and effort for all worker roles when starting an orchestrator or spike.

**Files:**
- Modify: `web/src/types.ts`
- Modify: `web/src/components/AgentFields.tsx`
- Modify: `web/src/panels/SpawnSheet.tsx`
- Modify: `web/src/panels/NewSpikeSheet.tsx`
- Modify: `web/src/panels/SpawnSheet.test.tsx`

**Step 7.1: Update `web/src/types.ts`**
Add `roles?: Partial<Record<SettingsRole, RoleDefault>>` to `StartOrchestratorBody` and `CreateSpikeBody`.

**Step 7.2: Update `web/src/components/AgentFields.tsx`**
Add collapsible "Worker Roles" section with role-specific Agent/Model/Effort pickers for `coder`, `reviewer`, `ui_reviewer`, `researcher`, `debugger`, `mechanical`.

**Step 7.3: Update `web/src/panels/SpawnSheet.tsx` & `NewSpikeSheet.tsx`**
Forward `fields.roles` in `orchestratorPayload` and `spikePayload`.

**Step 7.4: Write/update tests in `web/src/panels/SpawnSheet.test.tsx`**
Test that role overrides are editable and submitted with the payload.

**Step 7.5: Verify and commit**
`pnpm test`
Commit: `git commit -m "feat(web): add worker role override controls to orchestrator spawn sheets"`

---

### Task 8: Full End-to-End Verification

**Goal:** Run full verification across the Go daemon and Web frontend.

1. `go test -v ./...`
2. `pnpm --prefix web test`
3. Verify git clean diff and proper branch state.

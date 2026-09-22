# Isolated MCP and Durable Custom Instructions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure all four Swarm agents (`claude`, `codex`, `cursor`, `agy`) run with strictly isolated `swarm` MCP tooling and receive durable custom instructions defined via the Menubar app and editable via a `swarm_instructions` MCP tool.

**Architecture:** Add `Instructions` to daemon `Settings` (SQLite-backed). Add `swarm_instructions` MCP tool (`get`/`set`) to `mcpserver`. Update all 4 adapters to isolate MCP configs (only `swarm`) and inject `Instructions` while suppressing global user instructions. Add an `Instructions` tab in Menubar Settings with rendered Markdown and Edit mode.

**Tech Stack:** Go 1.24, SQLite, Swift 6 / SwiftUI (macOS AppKit), Model Context Protocol (MCP).

**Spec:** `docs/specs/2026-09-22-isolated-mcp-and-custom-instructions.md`

## Global Constraints

- Never mutate primary checkout branches directly; work exclusively in `/Users/alexandertar/GitHub/agent-swarm--instructions-and-isolated-mcp`.
- NEVER delete or drop existing tests; update assertions to match the new isolated contracts.
- Use atomic file writes (`writeFileAtomic`) when generating config/instruction files.
- Never use unquoted shell globs in tests or scripts.

## Review Focus

1. Ensure Claude `--strict-mcp-config` does not break `--dangerously-load-development-channels server:swarm`.
2. Ensure Codex `CODEX_HOME` isolation preserves `auth.json` so authentication succeeds without prompting.
3. Ensure Antigravity `$ISOLATED_HOME` preserves `.gemini/antigravity-cli/{antigravity-oauth-token, settings.json}` symlinks so auth succeeds.
4. Ensure `swarm_instructions` `set` operation persists to SQLite and notifies SSE subscribers.
5. Ensure empty instructions string is handled gracefully (no stray empty flag or invalid file passed to CLIs).

---

### Task 1: Settings Model & Persistence for Instructions

**Files:**
- Modify: `internal/settings/settings.go:32-50,64-87`
- Test: `internal/settings/settings_test.go`

**Interfaces:**
- Consumes: Existing SQLite `settings` table key-value mechanism
- Produces: `Settings.Instructions string` with default `""` and persistence

- [ ] **Step 1: Write the failing test**

In `internal/settings/settings_test.go`:
```go
func TestSettingsInstructionsRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	s, err := st.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Instructions != "" {
		t.Fatalf("expected empty default instructions, got %q", s.Instructions)
	}

	s.Instructions = "# Custom Swarm Rules\n1. Standard library first."
	saved, err := st.Put(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Instructions != s.Instructions {
		t.Fatalf("Put returned instructions %q, want %q", saved.Instructions, s.Instructions)
	}

	reloaded, err := st.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Instructions != s.Instructions {
		t.Fatalf("Get returned instructions %q, want %q", reloaded.Instructions, s.Instructions)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSettingsInstructionsRoundTrip ./internal/settings/...`  
Expected: FAIL (field `Instructions` undefined)

- [ ] **Step 3: Implement minimal code**

In `internal/settings/settings.go`:
Add `Instructions string `json:"instructions"` ` to `type Settings struct`.
In `Defaults()`: `Instructions: ""` is initialized automatically.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestSettingsInstructionsRoundTrip ./internal/settings/...`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/settings/settings.go internal/settings/settings_test.go
git commit -m "feat(settings): add Instructions field with SQLite persistence"
```

---

### Task 2: Swarm Instructions MCP Tool

**Files:**
- Modify: `internal/mcpserver/tools.go`
- Modify: `internal/mcpserver/server.go`
- Test: `internal/mcpserver/tools_test.go`

**Interfaces:**
- Consumes: `s.Settings.Get` and `s.Settings.Put`
- Produces: `swarm_instructions` tool with `get` and `set` operations

- [ ] **Step 1: Write the failing test**

In `internal/mcpserver/tools_test.go`:
```go
func TestInstructionsToolGetAndSet(t *testing.T) {
	srv, cleanup := newTestServerWithSettings(t)
	defer cleanup()
	ctx := context.Background()
	c := Caller{SessionID: "s1", AgentName: "coder-1"}

	// 1. Get initial empty instructions
	res, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"get"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["instructions"] != "" {
		t.Fatalf("expected empty instructions, got %v", m["instructions"])
	}

	// 2. Set new instructions
	newInstr := "# Team Guidelines\nAlways write tests."
	setRes, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"set","instructions":"# Team Guidelines\nAlways write tests."}`))
	if err != nil {
		t.Fatal(err)
	}
	sm := setRes.(map[string]any)
	if sm["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", sm)
	}

	// 3. Get updated instructions
	res2, err := srv.CallTool(ctx, c, "swarm_instructions", json.RawMessage(`{"op":"get"}`))
	if err != nil {
		t.Fatal(err)
	}
	m2 := res2.(map[string]any)
	if m2["instructions"] != newInstr {
		t.Fatalf("expected updated instructions %q, got %q", newInstr, m2["instructions"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestInstructionsToolGetAndSet ./internal/mcpserver/...`  
Expected: FAIL ("unknown tool swarm_instructions")

- [ ] **Step 3: Implement minimal code**

In `internal/mcpserver/tools.go`:
Add `instructionsTool(s *Server) ToolDef`:
```go
func instructionsTool(s *Server) ToolDef {
	return ToolDef{
		Name:        "swarm_instructions",
		Description: "Read or update durable Swarm instructions injected into agents.",
		Schema: objSchema(`"op":{"type":"string","enum":["get","set"]},"instructions":{"type":"string"}`),
		Handler: func(ctx context.Context, c Caller, args json.RawMessage) (any, error) {
			var in struct {
				Op           string `json:"op"`
				Instructions string `json:"instructions"`
			}
			if err := decode(args, &in); err != nil {
				return nil, err
			}
			switch in.Op {
			case "get":
				cfg, err := s.Settings.Get(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"instructions": cfg.Instructions}, nil
			case "set":
				cfg, err := s.Settings.Get(ctx)
				if err != nil {
					return nil, err
				}
				cfg.Instructions = in.Instructions
				if _, err := s.Settings.Put(ctx, cfg); err != nil {
					return nil, err
				}
				return map[string]any{"status": "ok"}, nil
			default:
				return nil, fmt.Errorf("op must be get or set, got %q", in.Op)
			}
		},
	}
}
```
Register `instructionsTool(s)` in `Server.ToolsFor()`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestInstructionsToolGetAndSet ./internal/mcpserver/...`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver/tools.go internal/mcpserver/server.go internal/mcpserver/tools_test.go
git commit -m "feat(mcpserver): add swarm_instructions tool with get and set ops"
```

---

### Task 3: Claude Adapter Isolated MCP & Instructions

**Files:**
- Modify: `internal/adapter/adapter.go:21-32`
- Modify: `internal/adapter/claude.go:48-82`
- Test: `internal/adapter/claude_test.go`

**Interfaces:**
- Consumes: `Spec.Instructions string`
- Produces: Claude flags with `--strict-mcp-config`, strictly `swarm` in `claude-mcp.json`, `--setting-sources "project,local"`, and `--append-system-prompt-file`

- [ ] **Step 1: Write the failing test**

In `internal/adapter/claude_test.go`:
Update `TestClaudeInheritsUserMCPServers` to assert `TestClaudeMCPConfigIsIsolated` (only `swarm`, no `neon`), and add `TestClaudeCustomInstructionsFlag`:
```go
func TestClaudeMCPConfigIsIsolated(t *testing.T) {
	d := testDeps(t)
	// Even if user has .claude.json with other servers, claude-mcp.json must only contain swarm
	userJSON := []byte(`{"mcpServers":{"neon":{"type":"http","url":"https://mcp.neon.tech"}}}`)
	if err := os.WriteFile(filepath.Join(d.UserHome, ".claude.json"), userJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := claudeSpec(t, d)
	spec.Instructions = "# Custom Instructions\nFollow TDD strictly."
	l, err := newClaude(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}

	var mcpPath, promptFilePath string
	var hasStrictMCP, hasSettingSources bool
	for i, v := range l.Argv {
		if v == "--mcp-config" {
			mcpPath = l.Argv[i+1]
		}
		if v == "--append-system-prompt-file" {
			promptFilePath = l.Argv[i+1]
		}
		if v == "--strict-mcp-config" {
			hasStrictMCP = true
		}
		if v == "--setting-sources" && l.Argv[i+1] == "project,local" {
			hasSettingSources = true
		}
	}

	if !hasStrictMCP {
		t.Errorf("expected --strict-mcp-config flag in Claude argv")
	}
	if !hasSettingSources {
		t.Errorf("expected --setting-sources project,local in Claude argv")
	}
	if promptFilePath == "" {
		t.Fatalf("expected --append-system-prompt-file in Claude argv")
	}
	content, err := os.ReadFile(promptFilePath)
	if err != nil || string(content) != spec.Instructions {
		t.Fatalf("prompt file content = %q, want %q", string(content), spec.Instructions)
	}

	b, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers["neon"] != nil {
		t.Errorf("expected neon to be excluded from isolated mcp config")
	}
	if cfg.Servers["swarm"] == nil {
		t.Errorf("expected swarm to be present in mcpServers")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestClaudeMCPConfigIsIsolated ./internal/adapter/...`  
Expected: FAIL

- [ ] **Step 3: Implement minimal code**

In `internal/adapter/adapter.go`:
Add `Instructions string` to `Spec`.

In `internal/adapter/claude.go`:
- In `flags(s Spec)`:
  - Remove user `~/.claude.json` reading loop.
  - Set `mcp, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"swarm": map[string]any{"type": "stdio", "command": s.Bin, "args": []string{"mcp"}}}})`
  - Add `--strict-mcp-config`, `--setting-sources`, `"project,local"` to flags.
  - If `s.Instructions != ""`:
    - Write launch file `claude-instructions.md` with body `[]byte(s.Instructions)`.
    - Append `--append-system-prompt-file`, `instrPath` to flags.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestClaudeMCPConfigIsIsolated ./internal/adapter/...`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/adapter.go internal/adapter/claude.go internal/adapter/claude_test.go
git commit -m "feat(adapter/claude): isolate mcp to swarm and inject custom instructions"
```

---

### Task 4: Codex Adapter Isolated MCP & Instructions

**Files:**
- Modify: `internal/adapter/codex.go:29-53`
- Test: `internal/adapter/codex_test.go`

**Interfaces:**
- Consumes: `Spec.Instructions string`
- Produces: Codex launch with isolated `CODEX_HOME`, symlinked `auth.json`, `-c model_instructions_file`, `-c mcp_servers.swarm...`

- [ ] **Step 1: Write the failing test**

In `internal/adapter/codex_test.go`:
```go
func TestCodexIsolatedMCPAndInstructions(t *testing.T) {
	d := testDeps(t)
	authPath := filepath.Join(d.UserHome, ".codex", "auth.json")
	_ = os.MkdirAll(filepath.Dir(authPath), 0o755)
	_ = os.WriteFile(authPath, []byte(`{"tokens":"secret"}`), 0o600)

	spec := codexSpec(t, d)
	spec.Instructions = "# Codex Custom Instructions"
	l, err := newCodex(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}

	codexHome := l.Env["CODEX_HOME"]
	if codexHome == "" {
		t.Fatalf("expected CODEX_HOME in Launch.Env")
	}
	symAuth := filepath.Join(codexHome, "auth.json")
	if _, err := os.Stat(symAuth); err != nil {
		t.Fatalf("expected symlinked auth.json at %s", symAuth)
	}

	var hasInstructionsFlag bool
	for i, v := range l.Argv {
		if v == "-c" && strings.HasPrefix(l.Argv[i+1], "model_instructions_file=") {
			hasInstructionsFlag = true
		}
	}
	if !hasInstructionsFlag {
		t.Errorf("expected -c model_instructions_file=... in codex argv")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCodexIsolatedMCPAndInstructions ./internal/adapter/...`  
Expected: FAIL

- [ ] **Step 3: Implement minimal code**

In `internal/adapter/codex.go`:
In `Launch(s Spec)` and `Resume(s Spec)`:
- Create isolated dir: `codexHome := filepath.Join(c.d.launchDir(s.SessionID), "codex-home")`
- Ensure `os.MkdirAll(codexHome, 0o700)`
- Symlink user `~/.codex/auth.json` to `filepath.Join(codexHome, "auth.json")` if user auth exists.
- Set `env["CODEX_HOME"] = codexHome`.
- If `s.Instructions != ""`:
  - Write `filepath.Join(c.d.launchDir(s.SessionID), "codex-instructions.md")`.
  - Pass `-c`, fmt.Sprintf(`model_instructions_file="%s"`, instrPath).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestCodexIsolatedMCPAndInstructions ./internal/adapter/...`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/codex.go internal/adapter/codex_test.go
git commit -m "feat(adapter/codex): isolate CODEX_HOME and inject custom instructions"
```

---

### Task 5: Antigravity (`agy`) & Cursor Adapters Isolated MCP & Instructions

**Files:**
- Modify: `internal/adapter/agy.go`
- Test: `internal/adapter/agy_test.go`
- Modify: `internal/adapter/cursor.go`
- Test: `internal/adapter/cursor_test.go`

**Interfaces:**
- Consumes: `Spec.Instructions string`
- Produces:
  - Agy: isolated `HOME`, `.gemini/config/mcp_config.json` with only `swarm`, `.gemini/AGENTS.md`
  - Cursor: isolated `CURSOR_DATA_DIR/mcp.json` with only `swarm`, workspace `AGENTS.md`

- [ ] **Step 1: Write failing tests**

In `internal/adapter/agy_test.go`:
```go
func TestAgyIsolatedMCPAndInstructions(t *testing.T) {
	d := testDeps(t)
	spec := agySpec(t, d)
	spec.Instructions = "# Agy Rules"
	l, err := newAgy(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	agyHome := l.Env["HOME"]
	if agyHome == "" {
		t.Fatalf("expected HOME in Launch.Env")
	}
	mcpFile := filepath.Join(agyHome, ".gemini", "config", "mcp_config.json")
	if _, err := os.Stat(mcpFile); err != nil {
		t.Fatalf("expected mcp_config.json at %s", mcpFile)
	}
	rulesFile := filepath.Join(agyHome, ".gemini", "AGENTS.md")
	content, err := os.ReadFile(rulesFile)
	if err != nil || string(content) != spec.Instructions {
		t.Fatalf("expected rules file with content %q", spec.Instructions)
	}
}
```

In `internal/adapter/cursor_test.go`:
```go
func TestCursorIsolatedMCPAndInstructions(t *testing.T) {
	d := testDeps(t)
	spec := cursorSpec(t, d)
	spec.Instructions = "# Cursor Rules"
	l, err := newCursor(d).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	cursorDir := l.Env["CURSOR_DATA_DIR"]
	if cursorDir == "" {
		t.Fatalf("expected CURSOR_DATA_DIR in Launch.Env")
	}
	mcpFile := filepath.Join(cursorDir, "mcp.json")
	if _, err := os.Stat(mcpFile); err != nil {
		t.Fatalf("expected mcp.json at %s", mcpFile)
	}
	wsRules := filepath.Join(spec.Cwd, "AGENTS.md")
	content, err := os.ReadFile(wsRules)
	if err != nil || string(content) != spec.Instructions {
		t.Fatalf("expected workspace AGENTS.md with content %q", spec.Instructions)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run "TestAgyIsolatedMCPAndInstructions|TestCursorIsolatedMCPAndInstructions" ./internal/adapter/...`  
Expected: FAIL

- [ ] **Step 3: Implement minimal code**

In `internal/adapter/agy.go`:
- Prepare `<launchDir>/agy-home`
- Symlink `antigravity-oauth-token` and `settings.json` from `d.UserHome/.gemini/antigravity-cli/`
- Write `.gemini/config/mcp_config.json` with only `swarm`
- If `s.Instructions != ""`: write `.gemini/AGENTS.md`
- Set `Env["HOME"] = agyHome`

In `internal/adapter/cursor.go`:
- Prepare `<launchDir>/cursor-home`
- Write `<launchDir>/cursor-home/mcp.json` with only `swarm`
- If `s.Instructions != ""`: write `filepath.Join(s.Cwd, "AGENTS.md")` (if writable)
- Set `Env["CURSOR_DATA_DIR"] = cursorHome`

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run "TestAgyIsolatedMCPAndInstructions|TestCursorIsolatedMCPAndInstructions" ./internal/adapter/...`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/agy.go internal/adapter/agy_test.go internal/adapter/cursor.go internal/adapter/cursor_test.go
git commit -m "feat(adapter/agy,cursor): isolate mcp to swarm and inject custom instructions"
```

---

### Task 6: Runtime Materialization & Spawn Wire-Up

**Files:**
- Modify: `internal/runtime/agents.go`
- Test: `internal/runtime/agents_test.go`

**Interfaces:**
- Consumes: `s.Settings.Get(ctx)`
- Produces: `Spec.Instructions` populated on `StartOrchestrator`, `StartSpike`, and `Spawn`

- [ ] **Step 1: Write the failing test**

In `internal/runtime/agents_test.go`:
Assert that when `Settings.Instructions` is non-empty, the adapter `Spec` receives the instructions on agent spawn.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSpawnPassesInstructionsToSpec ./internal/runtime/...`  
Expected: FAIL

- [ ] **Step 3: Implement minimal code**

In `internal/runtime/agents.go`:
In `specFor(ctx, ...)`:
Fetch current settings: `cfg, _ := s.Settings.Get(ctx)`
Set `spec.Instructions = cfg.Instructions`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestSpawnPassesInstructionsToSpec ./internal/runtime/...`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/agents.go internal/runtime/agents_test.go
git commit -m "feat(runtime): wire daemon settings instructions into adapter spec"
```

---

### Task 7: Menubar App Settings UI & Kit Integration

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarKit/Copy.swift`
- Modify: `apps/menubar/Sources/SwarmBarKit/SettingsModel.swift`
- Modify: `apps/menubar/Sources/SwarmBarUI/SettingsView.swift`
- Test: `apps/menubar/Tests/SwarmBarTests/SettingsModelTests.swift`

**Interfaces:**
- Consumes: `GET /api/settings` and `PUT /api/settings`
- Produces: `InstructionsTab` in menubar settings with rendered Markdown and Edit mode

- [ ] **Step 1: Write the failing test**

In `apps/menubar/Tests/SwarmBarTests/SettingsModelTests.swift`:
Test decoding `instructions` from JSON and updating `model.setInstructions(...)`.

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter SettingsModelTests` in `apps/menubar`  
Expected: FAIL

- [ ] **Step 3: Implement minimal code**

In `Copy.swift`: Add copy strings (`tabInstructions`, `agentInstructions`, `editInstructions`, etc.).
In `SettingsModel.swift`: Add `public var instructions: String` to `SettingsPayload` and `SettingsModel`. Add `setInstructions(_ text: String)` method calling `PUT /api/settings`.
In `SettingsView.swift`: Add `InstructionsTab(model: model).tabItem { Label(Copy.tabInstructions, systemImage: "doc.text") }` with `isEditing` toggle, rendered Markdown `ScrollView` and monospace `TextEditor`.

- [ ] **Step 4: Run test to verify it passes**

Run: `swift test` in `apps/menubar`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add apps/menubar/Sources/SwarmBarKit/Copy.swift apps/menubar/Sources/SwarmBarKit/SettingsModel.swift apps/menubar/Sources/SwarmBarUI/SettingsView.swift apps/menubar/Tests/SwarmBarTests/SettingsModelTests.swift
git commit -m "feat(menubar): add Instructions tab to settings with markdown preview and edit mode"
```

---

### Task 8: Full End-to-End Verification

**Files:**
- None (verification only)

- [ ] **Step 1: Run all backend tests**

```bash
go test ./internal/...
```
Expected: PASS across all packages.

- [ ] **Step 2: Run all Menubar tests**

```bash
cd apps/menubar && swift test
```
Expected: PASS.

- [ ] **Step 3: Run web tests**

```bash
cd web && pnpm test
```
Expected: PASS.

- [ ] **Step 4: Final commit and worktree clean check**

```bash
git status
```
Expected: clean worktree.

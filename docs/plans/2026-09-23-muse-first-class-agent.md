# Muse first-class agent + superpowers remediation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Muse (`muse` CLI, Meta provider) works as a fifth swarm agent kind end-to-end, and the three superpowers remediation fronts land with it.

**Architecture:** Mirror the agy vertical slice: registry → catalog → adapter → install → UI, each with TDD tests; live behavior (wake, idle, auth, hooks) nailed down by gated probes (`MUSE_LIVE_PROBE=1`) following `internal/adapter/agy_wake_probe_test.go`.

**Tech Stack:** Go (daemon), TypeScript/React (web), `muse` CLI v1.3.0 probes.

**Spec:** `docs/specs/2026-09-23-muse-first-class-agent.md`

## Global Constraints

- Phase-0 rule: every CLI flag in `Launch` must pass a probe first; never invent a flag.
- Yolo mode always: `--yolo --trust-workspace` on every muse launch.
- Muse is opt-in: role defaults stay claude; `EnabledAgents` gains muse only when installed.
- No DB migration. No `muse exec` headless path. No echo provider. Cost table out of scope.
- User-facing copy: label `Muse`, login hint `muse login`, catalog source `muse model-catalog`.
- Never `git add -A` in shared worktrees; stage explicit paths. Never amend.

## Review Focus

- Unknown `--model` ids are accepted silently by muse: `validateDefault` must reject models outside the fetched catalog, or a typo spawns a broken session that looks healthy.
- `event/usage` vs `record/quantity` double-count in export parsing: sums must come from exactly one path.
- `model-catalog/*.json` missing (fresh machine, never ran muse): fetcher must return the settings.json union, never an error that disables the kind.
- Muse `settings.json` merge must preserve unknown user keys (MCP servers, capabilities) byte-for-byte.
- Spawned-session skill check must run as the spawned user/HOME, not the operator shell, or isolation gaps stay hidden.

---

### Task 1: kinds registry

**Files:**
- Modify: `internal/kinds/kinds.go`
- Test: `internal/kinds/kinds_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `kinds.Muse AgentKind = "muse"`, `Display() "Muse"`, `AgentKinds` with Muse last (drives `settings.Defaults` enablement).

- [ ] **Step 1: Write the failing test**

```go
func TestMuseKind(t *testing.T) {
    if kinds.Muse.Display() != "Muse" { t.Fatalf("Display = %q", kinds.Muse.Display()) }
    if !slices.Contains(kinds.AgentKinds, kinds.Muse) { t.Fatal("AgentKinds lacks Muse") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kinds/ -run TestMuseKind -v`
Expected: FAIL (undefined: kinds.Muse)

- [ ] **Step 3: Write minimal implementation**

```go
Muse AgentKind = "muse" // in const block
var AgentKinds = []AgentKind{Claude, Codex, Agy, Cursor, Muse}
case Muse: return "Muse" // in Display()
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/kinds/ -v`
Expected: PASS (whole package)

- [ ] **Step 5: Commit**

```bash
git add internal/kinds/kinds.go internal/kinds/kinds_test.go
git commit -m "feat(kinds): add Muse agent kind"
```

### Task 2: install Kind + SkillsDir + binaries

**Files:**
- Modify: `internal/install/config.go`
- Test: `internal/install/agents_test.go` (kinds-agree assertion), `internal/install/config_test.go` (if SkillsDir table lives there)

**Interfaces:**
- Consumes: `kinds.Muse` (Task 1).
- Produces: `install.KindMuse`, `Kinds` with muse last, `agentBinaries[KindMuse]="muse"`, `Config.Muse()` path root + `SkillsDir` case.

- [ ] **Step 1: Write the failing test**

```go
func TestKindsAgreeWithKinsPackage(t *testing.T) {
    // extend the existing two-agree assertion: every kinds.AgentKind except Fake
    // must have an install.Kind, including muse.
}
```

(Read the existing assertion first; extend its table, do not rewrite it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/install/ -run TestKinds -v`
Expected: FAIL (muse missing)

- [ ] **Step 3: Write minimal implementation**

```go
KindMuse Kind = "muse"
var Kinds = []Kind{KindClaude, KindCodex, KindAgy, KindCursor, KindMuse}
case KindMuse: return "Muse"
func (c Config) Muse(rest ...string) string { return filepath.Join(c.UserHome, ".local", "share", "muse", filepath.Join(rest...)) }
```

`SkillsDir` gains `case KindMuse: return c.Muse("skills")`. Probe before locking the
path: `muse skills install <testdata-skill> --scope user` then
`muse skills list --json | grep <name>`, then `muse skills uninstall <name>`. If the
install lands elsewhere, use the probed path instead and note it in the commit message.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/install/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/install/config.go internal/install/agents.go internal/install/agents_test.go internal/install/config_test.go
git commit -m "feat(install): muse kind, binary, skills dir"
```

### Task 3: catalog MuseFetcher + parser

**Files:**
- Create: `internal/catalog/muse.go` (fetcher), `internal/catalog/parse_muse.go` (parser) — or extend `parse.go`/`fetch.go` if the file stays focused; follow the Agy precedent (fetcher in fetch.go, parser in parse.go).
- Test: `internal/catalog/parse_test.go` (add `TestParseMuseModels`), fetcher test with temp dirs.
- Testdata: `internal/catalog/testdata/muse-catalog.json` (structure mirrors the real `6d657461__p746268.json` with 2 rows), `muse-settings.json` (`{"model":"muse-spark-1.3-contributor"}`).

**Interfaces:**
- Consumes: `kinds.Muse`.
- Produces: `ParseMuseModels(catalog, settings []byte) ([]CatalogModel, string, error)`; `MuseFetcher{DataDir, SettingsFile}` with `Kind()/Version()/Fetch()`; registered in `DefaultFetchers`.

- [ ] **Step 1: Write the failing test**

```go
func TestParseMuseModels(t *testing.T) {
    cat := mustRead(t, "testdata/muse-catalog.json")
    set := []byte(`{"model":"muse-spark-1.3-contributor","reasoning_effort":"high"}`)
    ms, def, err := ParseMuseModels(cat, set)
    if err != nil || def != "muse-spark-1.3-contributor" { t.Fatalf("def=%q err=%v", def, err) }
    // efforts of the contributor row must equal its reasoning_effort_variants tiers
    // IsDefault true only on that row; missing settings model falls back to is_default row.
}
```

Plus a missing-dir case: fetcher with empty DataDir + settings model returns 1 model, no error (Review Focus #3).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/catalog/ -run TestParseMuseModels -v`
Expected: FAIL (undefined: ParseMuseModels)

- [ ] **Step 3: Write minimal implementation**

```go
type museRow struct {
    ModelID string `json:"model_id"`
    IsDefault bool `json:"is_default"`
    Variants []struct{ Tier string `json:"tier"` } `json:"reasoning_effort_variants"`
}
func ParseMuseModels(catalog, settings []byte) ([]CatalogModel, string, error) {
    // unmarshal {rows:[...]}; each visible row -> CatalogModel{ID, Label: model_id,
    //   Efforts: tiers, DefaultEffort: "high"}; default = settings.model if it matches
    //   a row else the is_default row; empty rows -> errNoModels.
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/catalog/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/catalog/
git commit -m "feat(catalog): dynamic muse model fetcher"
```

### Task 4: adapter Muse (launch/resume/installed/auth)

**Files:**
- Create: `internal/adapter/muse.go`, `internal/adapter/muse_test.go`
- Test: fake Deps (follow `claude_test.go` harness); assert argv exact.

**Interfaces:**
- Consumes: `kinds.Muse`, `catalog` launch values (model slug + effort tier).
- Produces: `Muse` adapter registered via `init()`; `Launch`/`Resume` argv.

- [ ] **Step 1: Write the failing test**

```go
func TestMuseLaunch(t *testing.T) {
    // Launch(Spec{AgentName:"n", Model:"muse-spark-1.3-contributor", Effort:"high", Kickoff:"hi"})
    // want Argv ["muse","-i","hi","--model","muse-spark-1.3-contributor","--reasoning-effort","high","--yolo","--trust-workspace"]
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adapter/ -run TestMuseLaunch -v`
Expected: FAIL (no Muse adapter)

- [ ] **Step 3: Write minimal implementation**

```go
type Muse struct{ base }
func newMuse(d Deps) *Muse { return &Muse{base{d: d, kind: kinds.Muse}} }
func init() { register(kinds.Muse, func(d Deps) Adapter { return newMuse(d) }) }
func (m *Muse) Launch(s Spec) (Launch, error) {
    return Launch{Argv: []string{"muse","-i",s.Kickoff,"--model",s.Model,
        "--reasoning-effort",effortOr(s.Effort,"high"),"--yolo","--trust-workspace"}}, nil
}
func (m *Muse) Installed(ctx context.Context) (string, bool) // muse --version via Run
func (m *Muse) AuthOK(ctx context.Context) error // mirror codex AuthOK; probe auth.json providers shape first
```

Idle/Busy regexps, `Models()` (errNotImplemented, like others), `Wake`, hooks: stub with
explicit `TODO(probe)` ONLY where a live probe decides the value, and the probe task
(Task 5) must resolve every stub — no stub survives the branch.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/adapter/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/muse.go internal/adapter/muse_test.go
git commit -m "feat(adapter): muse launch, installed, auth"
```

### Task 5: live probes — wake, idle, hooks (gated, never CI)

**Files:**
- Create: `internal/adapter/muse_wake_probe_test.go`

Probe plan (nod already given): `MUSE_LIVE_PROBE=1` only. (a) `session-message send`
to a live TUI session, assert the turn appears; repeat-send ordering check (agy pattern).
(b) Capture idle vs busy TUI screens, pin `IdlePrompt`/`Busy` regexps. (c) Fire each
hook surface muse exposes (plugin hook test fixture) and record which ones reach
`swarm hook`. Tmux-paste fallback stays if (a) fails — record the decision in the test
comment, do not invent a native wake.

- [ ] Steps follow the file's own structure (SchemaConfirmation → ordering → decision);
      commit as `test(adapter): muse live wake/idle/hook probes`.

### Task 6: install wiring — MCP merge, skills, plugins

**Files:**
- Modify: `internal/install/muse.go` (new; follow `agy.go` writer pattern),
  `internal/install/plugins.go` (add KindMuse to superpowers + elements-of-style maps)
- Test: `internal/install/muse_test.go` (merge preserves unknown keys — Review Focus #4)

MCP merge rule: read existing `settings.json`, set `mcpServers.swarm = {mode optional,
command <bin>, args [mcp]}`, write back atomically; assert a fixture with extra servers
round-trips byte-identical except the swarm key. Skills: `WriteSkills` covers muse via
Task 2's `SkillsDir` — add a case to the existing skills test, not a new harness.
Plugins: superpowers 6.4.1 already installs natively in muse; the map entry records
intent and the Task 9 probe verifies visibility.

### Task 7: usage via export

**Files:**
- Probe first: find the existing per-agent usage seam (`internal/usage/`, `usagegate`).
- Modify: add muse usage reader (parse `muse export --session <id> --redacted` JSON,
  sum `events[].envelope.payload.event.usage`, ignore `record/quantity`).
- Test: checked-in redacted export fixture (2 usage events + 1 quantity dup → expected sum).

### Task 8: web UI

**Files:**
- Modify: `web/src/types.ts` (AgentKind), `web/src/copy.ts` (label + login),
  `web/src/components/icons.tsx` (+ `assets/agents/muse.svg`), `web/src/logic/catalog.ts`
  (effort labels; muse uses tier ladder, no claude special-case).
- Test: extend `catalog.test.ts`, `AgentFields` tests, `contract.test.ts` if kind-unioned.

### Task 9: superpowers remediation (R1 delivery probes, R2 instruction fix, R3 kickoff MUST)

- R2 first (no probe needed): rewrite `skills/swarm-orchestrator/SKILL.md:45` paths to
  `docs/specs/<YYYY-MM-DD>-<slug>.md` / `docs/plans/<YYYY-MM-DD>-<slug>.md`, keep the
  "never `.superpowers/`" rule (repo CLAUDE.md). Grep `skills/` for sibling copies of
  the contradiction and fix each once.
- R1: per-kind spawned-session probe asserting `swarm` + `superpowers:*` visibility
  (muse via `skills list --json`; others via their CLIs); fix what fails, muse first.
- R3: locate kickoff builder (`internal/runtime/agents.go`, `text.go`), add MUST-level
  superpowers invocation line; daemon-side gate stays out of scope per spec §2.

### Task 10: full verification + merge

- [ ] Run: `go test ./...` then `pnpm test` in `web/` (exact repo gates).
- [ ] Run: `MUSE_LIVE_PROBE=1 go test ./internal/adapter/ -run TestMuse` (paid; local only).
- [ ] Merge branch to main (`git merge --no-ff`), prune worktree with
      `git worktree remove`, report verification output verbatim.

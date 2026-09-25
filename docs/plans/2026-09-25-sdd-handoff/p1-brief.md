## Global Constraints

- Everything ships together in one release; no compatibility shims between daemon, web and menubar.
- Legacy behaviour is preserved exactly for tasks whose `workflow_json` is NULL (existing epics, board-created tasks). Every change to checkpoint, transition or spawn code keeps today's legacy-task tests green.
- Never delete or weaken an existing test to make a new one pass. Tests whose expectations change intentionally are named in the unit that changes them.
- After any edit under `skills/`, run `make skills-sync`; `go test ./internal/install/...` enforces the mirror.
- **One commit per unit** (conventional message, e.g. `feat(install): …`, signed, repo style). Review fix rounds add commits; never amend.
- Every Go unit keeps `go build ./... && go vet ./...` green. Web units also run `cd web && pnpm typecheck && pnpm test`. Menubar units also run `cd apps/menubar && swift test`.
- Hot files: `internal/runtime/checkpoint.go`, `internal/runtime/agents.go`, `internal/items/transition.go`. Pull or rebase before starting P7–P10.
- **Package review loop** (every package, once, after all its units are committed):
  1. Run the package Verify set.
  2. Request review with `superpowers:requesting-code-review` against the package Acceptance, walking the diff unit by unit.
  3. Handle findings with `superpowers:receiving-code-review`. Fix, re-run Verify, and commit.
  4. Repeat until the review passes. Stop after the workflow's `max_rounds` and escalate to the human.
  5. If one unit collects all the findings two rounds in a row, split it into a follow-up package instead of re-running the whole package (`swarm-batching` split trigger).

## Review Focus

Spec inputs most likely to be under-tested. Reviewers check these explicitly:

1. Legacy tasks (no workflow) behave byte-for-byte as before across checkpoint, transition and spawn paths.
2. Engine idempotency: a repeated `advance` (daemon restart, duplicate trigger) never double-spawns (`UNIQUE (workflow_id, step_id, round, role)`).
3. `closeCompletedSiblings` no longer tears down a builder when a reviewer completes on the same task, and vice versa.
4. Symlinked skills are actually discovered by each agent CLI. The P1 empirical check decides where the copy fallback is used.
5. Relay suppression: the orchestrator receives exactly one `workflow_succeeded` per successful package. It still receives `blocked`/`failed` checkpoints and questions from step agents.
6. No role is ever inferred. A task without a workflow is rejected at plan registration and on orchestrator `swarm_items create`.


### P1: Skill distribution
**Workflow:** `tdd-reviewed` (coder → reviewer) · **Units:** 4

**Files:**
- `internal/install/{skills.go,skills_test.go,claude.go,codex.go,agy.go,cursor.go,muse.go,doctor.go,uninstall.go}`
- `internal/adapter/claude.go` and its test
- `internal/migrate/{integrations.go,recover_test.go}`
- `Makefile`
- `cmd/swarm/daemon.go`

**Interfaces (produces):**
- `type Skill struct{ Name, Dir string; Vendored bool }`
- `func Skills() ([]Skill, error)`
- `func SkillFS() fs.FS`
- `func SkillNames() []string`
- `func SyncSkills(home string) ([]string, error)` (`home` is the swarm home, e.g. `~/.swarm`; writes `<home>/skills`)
- `const ManagedMarker = ".swarm-managed"`
- `func LinkSkills(root, skillsHome string, mode LinkMode) (skipped []string, err error)`
- `func WriteSkills(c Config, k Kind) (changed, skipped []string, err error)`
- `func CheckSkills(c Config, k Kind) Check`

**Acceptance:**
- The whole `skills/` tree is embedded (nested dirs, LICENSE, scripts, data) and extracted to `~/.swarm/skills/<name>/`, with vendored skills flattened by name.
- Stale files are pruned, and each extracted dir gets a `.swarm-managed` marker.
- The daemon syncs skills at startup.
- `make skills-sync` mirrors the tree with deletes, and the drift test compares every file.
- Every kind's skills root links to `~/.swarm/skills/<name>`, or holds a managed copy where the CLI can't follow symlinks.
- A user-owned skill with the same name is left untouched and reported.
- Claude spawns get links in `<cwd>/.claude/skills/`.
- Uninstall removes only swarm entries.
- Doctor checks skills for every kind, and warns when `python3` is missing.

**Verify:**
- `make skills-sync`
- `go test ./internal/install/... ./internal/adapter/... ./internal/migrate/...`
- `go build ./... && go vet ./...`

#### Unit 1.1: Embed, registry, sync
- [ ] Write the failing tests in `internal/install/skills_test.go`:

```go
func TestSkillsRegistryMatchesTree(t *testing.T) {
	sk, err := Skills()
	if err != nil { t.Fatal(err) }
	names := map[string]bool{}
	for _, s := range sk {
		if names[s.Name] { t.Fatalf("duplicate skill name %q", s.Name) }
		names[s.Name] = true
		body, err := fs.ReadFile(SkillFS(), path.Join(s.Dir, "SKILL.md"))
		if err != nil { t.Fatalf("%s: %v", s.Name, err) }
		fm := parseFrontmatter(t, body)
		if fm["name"] != s.Name || path.Base(s.Dir) != s.Name { t.Errorf("%s: frontmatter/dir mismatch", s.Dir) }
		if strings.TrimSpace(fm["description"]) == "" { t.Errorf("%s: empty description", s.Name) }
	}
	for _, want := range []string{"swarm", "swarm-orchestrator", "swarm-batching"} {
		if !names[want] { t.Errorf("missing %s", want) }
	}
}

func TestSyncSkillsWritesNestedFilesAndPrunes(t *testing.T) {
	home := t.TempDir()
	if _, err := SyncSkills(home); err != nil { t.Fatal(err) }
	marker := filepath.Join(home, "skills", "swarm", ManagedMarker)
	if _, err := os.Stat(marker); err != nil { t.Fatalf("marker: %v", err) }
	stale := filepath.Join(home, "skills", "swarm", "stale.md")
	os.WriteFile(stale, []byte("x"), 0o644)
	if _, err := SyncSkills(home); err != nil { t.Fatal(err) }
	if _, err := os.Stat(stale); !os.IsNotExist(err) { t.Fatalf("stale file survived sync") }
}

func TestEmbeddedMirrorMatchesCanonicalTree(t *testing.T) { /* walk ../../skills vs SkillFS(); same file set, same bytes */ }
```

- [ ] Run `go test ./internal/install/ -run 'TestSkillsRegistry|TestSyncSkills|TestEmbeddedMirror'`. Expect a compile failure and record it as red (unit 1).
- [ ] Implement the embed and sync:
  - Replace the current embed with `//go:embed all:skills`. `SkillFS()` returns `fs.Sub(..., "skills")`.
  - `Skills()` treats every dir holding a `SKILL.md` as a skill; `Vendored` is set when the dir is under `vendor/`.
  - `SyncSkills` writes each file with `WriteIfChanged`, keeps the exec bit under `scripts/`, writes the marker, and prunes files that are no longer embedded.
  - `SkillNames()` becomes a function. Update its callers in `WriteSkills`, `adapter/claude.go`, `uninstall.go` and `migrate/integrations.go`.
  - Update the count assertions in `skills_test.go:66` and `migrate/recover_test.go:479-517` to derive from `len(SkillNames())`. This is an intentional change.
- [ ] Change `Makefile` `skills-sync` to mirror with deletes (`rm -rf internal/install/skills && cp -R skills internal/install/skills`; rsync isn't always installed) and run it.
- [ ] In `cmd/swarm/daemon.go`, call `install.SyncSkills(cfg.Home)` (the swarm home) before the reconcile loop, then relink kinds that already hold swarm-owned skills. Log failures; they are not fatal.
- [ ] Run green and record it (unit 1). Commit: `feat(install): embed and sync the full skills tree`.

#### Unit 1.2: Per-kind links, user-owned safety, uninstall
- [ ] **Empirical check first.** For each installed CLI (claude, codex, agy, cursor-agent, muse), symlink a throwaway skill into its skills root and confirm whether the CLI lists or uses it. Set `skillLinkMode[kind]` from the result. A CLI that isn't installed defaults to `Copy`. Put the findings in the commit message.
- [ ] Write the failing tests and record red:
  - `TestWriteSkillsSymlinksEveryManagedSkill`
  - `TestWriteSkillsSkipsUserOwnedSameName`
  - `TestWriteSkillsReplacesStaleSwarmCopy`
  - `TestUninstallRemovesOnlySwarmSkills`
- [ ] Implement `LinkSkills` and `WriteSkills`. An entry counts as swarm-owned if it is a symlink into `skillsHome`, or a dir containing the marker.
- [ ] Run green. Commit: `feat(install): link swarm skills into every agent kind`.

#### Unit 1.3: Claude per-spawn links
- [ ] Write the failing test `TestClaudeProjectConfigLinksSkills` in `internal/adapter/claude_test.go`. Red.
- [ ] Make `writeProjectSwarmConfig` call `LinkSkills(<cwd>/.claude/skills, ~/.swarm/skills, skillLinkMode[Claude])`.
- [ ] Run green. Commit: `feat(adapter): link skills into claude spawn dirs`.

#### Unit 1.4: Doctor
- [ ] Write the failing tests `TestDoctorChecksSkillsForEveryKind` and `TestDoctorWarnsWithoutPython3`. Red.
- [ ] Add `CheckSkills` to every kind's `Check*`, and add a warn-level `python3` base check. Refresh the README's `swarm doctor` line (folded docs).
- [ ] Run green. Commit: `feat(doctor): skills check per kind and python3 warning`.


# Package PA report: agy skills root

Worktree: `/Users/alexandertar/GitHub/agent-swarm--pa`, branch `pkg/pa`.
Status: **DONE**. 4 commits, one per unit, all TDD (red then green).

## Summary

`Config.SkillsDir(KindAgy)` pointed at `~/.gemini/antigravity-cli/skills`,
but agy 1.2.10/1.2.11 actually reads skills from
`$HOME/.gemini/config/skills` and migrates the former away from on first
run in a fresh `HOME`. Every swarm-spawned agy session got its own fresh
`HOME` whose `.gemini/antigravity-cli` was symlinked whole to the *real*
`~/.gemini/antigravity-cli` — so each new session's first run chained the
real `~/.gemini/antigravity-cli/skills` one hop deeper into that session's
own `run/launch/<session>` folder, with no permanent, session-independent
home for the shared `swarm`/`swarm-orchestrator` skills. Confirmed still
present on this machine (read-only, unmodified throughout this task):
`~/.gemini/antigravity-cli/skills` is a two-hop symlink chain resolving to
`~/.swarm/run/launch/ses_01M36FPV68G9S2S3SCDAJB1AF6/agy-home/.gemini/config/skills`.

Fixed by: pointing the install path at the location agy actually reads,
making `setupEnv` link that real path directly (never touching
`antigravity-cli` for skills again), adding an install-time repair for
existing broken installs plus a doctor warning, and re-verifying agy's
symlink-following behavior at the corrected path in a fully sealed
scratch environment.

## Units

### PA.1 — `SkillsDir(KindAgy)` → `~/.gemini/config/skills`

RED: `go test ./internal/install/... -run TestSkillsDirPerAgent -v`
```
config_test.go:69: SkillsDir(agy) = "/fake/home/.gemini/antigravity-cli/skills", want "/fake/home/.gemini/config/skills"
--- FAIL: TestSkillsDirPerAgent (0.00s)
```
GREEN: same command, `--- PASS: TestSkillsDirPerAgent (0.00s)`.

Change: `internal/install/config.go`, `SkillsDir(KindAgy)` case now returns
`c.Gemini("config", "skills")`. `CheckSkills`, `WriteSkills`, `Uninstall`
all derive the path from this method, so they follow automatically — no
other code change needed for those.

Commit: `854541e fix(install): SkillsDir(KindAgy) points at ~/.gemini/config/skills`

### PA.2 — `setupEnv` links `config/skills` + `.migrated`

RED: `go test ./internal/adapter/... -run 'TestAgySetupEnvLinksConfigSkillsAndMigratedMarker|TestAgySetupEnvCopiesRealMigratedMarkerContent' -v`
```
agy_test.go:578: expected .../agy-home/.gemini/config/skills to be a symlink: ... no such file or directory
--- FAIL: TestAgySetupEnvLinksConfigSkillsAndMigratedMarker
agy_test.go:621: expected a plain (non-symlink) .migrated marker: ... no such file or directory
--- FAIL: TestAgySetupEnvCopiesRealMigratedMarkerContent
```
GREEN: same command, both PASS.

Change: `internal/adapter/agy.go` `setupEnv` now:
- `os.MkdirAll`s the real `~/.gemini/config/skills` if missing (`0o755`),
  then symlinks `<agy-home>/.gemini/config/skills` straight at it.
- Reads the real `~/.gemini/config/.migrated`'s bytes (empty slice if it
  doesn't exist) and writes them as a **plain file** (never a symlink) at
  `<agy-home>/.gemini/config/.migrated` — a symlink there would hand a
  spawned agy a write path back into the real `~/.gemini/config` tree,
  exactly the class of bug this package removes (judgment call, confirmed
  with the advisor: the brief's "symlink (or create)" phrasing is
  satisfied by copying the marker's *content*, not its link).
- Leaves the existing `antigravity-cli` / `hooks.json` / `plugins`
  symlinks unchanged.

Updated `TestAgyIsolatedHomeCarriesSkillsAndHooks` (stale test, not
deleted): it used to plant a skill at the old
`~/.gemini/antigravity-cli/skills` path and assert it reachable via the
`antigravity-cli` symlink; it now plants at `~/.gemini/config/skills` and
asserts the new symlink and its target directly, matching PA.1/PA.2's
actual contract.

Commit: `eb7da61 fix(adapter): agy setupEnv links config/skills, stops rewiring real antigravity-cli`

### PA.3 — Install repair + doctor warn

RED: `go test ./internal/install/... -run 'TestWriteAgyRepairsALegacySkillsChainIntoRunLaunch|TestWriteAgyLeavesAHealthySkillsRootAlone|TestCheckAgyWarnsWhenSkillsRootIsInsideARunLaunchSession' -v`
```
agy_test.go:359: antigravity-cli/skills points at ".../.swarm/run/launch/ses_x/agy-home/.gemini/config/skills", <nil>, want ".../.gemini/config/skills"
--- FAIL: TestWriteAgyRepairsALegacySkillsChainIntoRunLaunch
agy_test.go:407: no check named "agy skills root" in [...]
--- FAIL: TestCheckAgyWarnsWhenSkillsRootIsInsideARunLaunchSession
```
(`TestWriteAgyLeavesAHealthySkillsRootAlone` was green from the start —
kept as a negative-case pin.)

GREEN: same command, all three PASS.

Change: `internal/install/agy.go`:
- `legacyAgySkillsChain(c Config) (resolved string, ok bool)` — shared
  detector: is `~/.gemini/antigravity-cli/skills` a symlink chain
  resolving under `<swarm home>/run/launch/`? (`filepath.EvalSymlinks` on
  both the target and the launch root, then `filepath.Rel`, per the
  advisor's fix for the `/var` vs `/private/var` macOS tmpdir symlink
  mismatch that a naive `strings.HasPrefix` would have hit.)
- `repairAgySkillsRoot(c Config) error` — called from `WriteAgy`, before
  `WriteSkills`. When the chain is detected: for each entry under the
  resolved dir, check `isSwarmOwned(dst, skillsHome, true)` against the
  same-named entry at the new root; copy over it (via the existing
  `copyTree` helper from `plugins.go`) unless that entry is user-owned,
  in which case it's left alone. Then repoints
  `~/.gemini/antigravity-cli/skills` → `~/.gemini/config/skills`. Never
  touches anything under `run/launch` itself (read-only `os.ReadDir`).
  `WriteAgy` is only ever called from `Agents()`'s writer step (`internal/install/agents.go:107`),
  which is only reached by `cmdInstall` (`cmd/swarm/commands.go`) — never
  the daemon, confirmed by grep (`WriteAgy` has exactly one call site).
- `agySkillsRootLegacyCheck(c Config) Check` — added to `CheckAgy`'s
  return slice. Warn-level (`OK: true`, following the existing `python3`
  check's precedent), detail `"agy skills live inside a swarm session
  folder; run swarm install to move them"` when the chain is detected.

Commit: `a5abf34 fix(install): repair a broken agy skills chain on swarm install, doctor warn`
(also adds spec A7 and the "PA: agy skills root" plan package in the same commit)

### PA.4 — Sealed re-probe + link mode + probe doc

Full method, exact commands and output are in
`docs/plans/2026-09-24-skill-symlink-probe.md`, new section "agy —
2026-09-25 re-probe (package PA)". Summary:

- Two scratch `HOME`s built entirely under this task's scratchpad
  (`home-symlink`, `home-control`), **no symlink to any real directory**
  anywhere inside either (verified with `find -type l` before running).
- Only the four files agy needs (`antigravity-oauth-token`,
  `antigravity_state.pbtxt`, `installation_id`, `settings.json`) were
  **copied**, never linked, from the real `~/.gemini/antigravity-cli/`;
  `settings.json`'s `trustedWorkspaces` was rewritten in the copy to name
  only the scratch cwd. `env | grep -iE 'gemini|antigravity|xdg'` was
  empty (no stray env var could redirect HOME resolution); `HOME` was
  always set explicitly on the command line, never inherited.
- Probe skill (`zz-swarm-symlink-probe`, codeword `NARWHAL-4482`, a fresh
  codeword distinct from the 2026-09-24 probe's) symlinked at
  `home-symlink/.gemini/config/skills/zz-swarm-symlink-probe` to a source
  dir outside both scratch homes; `home-control` got a real copy at the
  same path instead.
- Before the first run and after each run, the real home was snapshotted
  (`ls -la ~/.gemini/antigravity-cli/skills ~/.gemini/config
  ~/.gemini/antigravity-cli`) and diffed against the previous snapshot.
  **All three diffs were empty — zero drift on the real home across the
  whole probe.** (Verified again just now, after all commits: the real
  chain is still exactly what it was before this task started.)
- Result: agy 1.2.11 read the probe skill straight through the symlink —
  the cited file path in its own output was the symlink's path itself —
  with no migration side effect (`find -newer <stamp>` showed nothing
  written under `.gemini/config/skills`; the probe source file was still
  present, unmoved, unchanged afterward). Control run (real copy) gave
  the identical result, as expected.
- **Verdict: Symlink.** `skillLinkMode[KindAgy]` flipped from `Copy` to
  `Symlink` in `internal/install/skills.go`; `TestSkillLinkModePerKind`
  updated (RED confirmed: `SkillLinkMode(agy) = 1, want 0`; GREEN after).
- Sensitive scratch copies (the oauth token file) were deleted
  immediately after both runs completed; the kept evidence files under
  the scratchpad are `ls -la` metadata and agy's own text responses only
  — no token bytes in this report or in any kept file.

Commit: `537e8bd fix(install): re-verify agy follows a symlink at the corrected skills root`

## Verify (brief's set, run after PA.4)

```
$ go test ./internal/install/... ./internal/adapter/... ./cmd/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	3.877s
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter	0.810s
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm	3.385s
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm-fake-agent	1.855s

$ go build ./... && go vet ./...
(clean, exit 0)

$ go test ./... (once)
... all ok, except the one pre-existing baseline failure named in the
implementer contract:
--- FAIL: TestBoardServedAtRoot (internal/httpapi) — GET /kanban = 503, web bundle not built
```

## Files changed

- `internal/install/config.go`, `config_test.go`
- `internal/install/agy.go`, `agy_test.go`
- `internal/install/skills.go`, `skills_test.go`
- `internal/adapter/agy.go`, `agy_test.go`
- `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md` (new
  "A7. agy skills root" section, plus one "All user-facing copy" line)
- `docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md` (new
  "PA: agy skills root" package, all 4 units ticked)
- `docs/plans/2026-09-24-skill-symlink-probe.md` (Verdicts row updated,
  new dated section appended — the pre-existing "agy — 1.2.10: unverified"
  section is untouched, per instructions not to rewrite existing probe
  history)

No changes under `skills/`, so no `make skills-sync` was needed.

## Self-review / judgment calls

1. **`.migrated` marker: content copy, not symlink.** The brief said
   "symlink (or create)"; I copied bytes into a plain file instead,
   because a symlink there defeats the purpose of the package (never
   giving a spawned agy a write path back to the real `~/.gemini/config`
   tree). Confirmed with the advisor before implementing. Read semantics
   for agy are identical either way (agy only ever reads this marker to
   decide whether to migrate).
2. **Repair ordering.** `repairAgySkillsRoot` runs before `WriteSkills`
   inside `WriteAgy`, so `WriteSkills`'s own sync/adoption pass runs
   immediately after and re-writes every registered (swarm-owned) skill
   name at the new root regardless of what the repair copied — the repair
   only needs to rescue *unregistered* / user-owned content that would
   otherwise be silently orphaned under `run/launch`.
3. **`legacyAgySkillsChain` shared between repair and doctor.** Avoids
   duplicating the `run/launch` chain-resolution logic (and the
   `EvalSymlinks`-on-both-sides fix for the macOS `/var` vs `/private/var`
   tmpdir symlink) in two places.
4. **PA.4 used a fresh probe codeword** (`NARWHAL-4482` vs. the
   2026-09-24 probe's `PELICAN-7731`) so this run's evidence in the doc is
   unambiguously distinct from the earlier one.
5. **Real home was never touched.** Confirmed by diff at three points
   during PA.4, and again just now after all 4 commits: `readlink -f
   ~/.gemini/antigravity-cli/skills` still resolves to the same
   `ses_01M36FPV68.../agy-home/.gemini/config/skills` it did before this
   task started. The actual repair of that live chain will happen the
   next time the user runs a real `swarm install` (out of scope here per
   the hard safety rules — this package only builds and tests the fix).

## Concerns

**Action required after merge/deploy: run `swarm install` once.** Until it
runs, every new agy spawn gets an empty real `~/.gemini/config/skills`
(`setupEnv` creates it; the daemon's own `RefreshSkillLinks` skips it
because `anyAlreadyInstalled` is false for a kind that was never
installed there) — briefly worse than the pre-PA interim state (zero
swarm skills visible to a freshly spawned agy, vs. the old broken-chain
copy that at least had content). `swarm install` populates
`~/.gemini/config/skills` and repairs the existing broken chain
(PA.3) in the same run. Not run as part of this task per the hard safety
rules (never against the real home).

Other notes, none blocking:
- The real `~/.gemini/antigravity-cli/skills` on this machine is still the
  broken chain from the prior incident (confirmed unchanged at the end of
  this task, both by diff during PA.4 and by a final manual check after
  all commits). Real `~/.gemini/config/skills` does not yet exist. Both
  are fixed by the `swarm install` above.
- Checked `README.md` and `docs/` for stale `antigravity-cli/skills`
  references from the P0 (2026-09-23) fix that first pointed
  `SkillsDir(KindAgy)` there: none outside two historical, dated
  plan/spec snapshots (`docs/plans/2026-09-23-superpowers-remediation.md`,
  `docs/specs/2026-09-23-superpowers-remediation.md`), left untouched as
  point-in-time records, same treatment as the probe doc's own
  2026-09-24 section.

## Pre-report self-review (advisor, before this report was first sent)

The advisor caught one blocker and three non-blocking items before I
reported done. All four addressed, one new commit:

**Blocker — `setupEnv`'s `os.RemoveAll(symConfigSkills)` could delete live
content on Resume.** `setupEnv` runs on both `Launch` and `Resume`, and
`agyHome` is keyed by session ID (`agy.go:42`), so a `Resume` of an
already-spawned session reuses the same `agy-home` — which can hold real,
already-migrated content (the live broken chain's terminal directory is
exactly a past session's `agy-home/.gemini/config/skills`). The
unconditional `RemoveAll` before symlinking would have deleted that
content on the first `Resume` after this fix shipped, or dangled the
chain's middle hop — the exact incident class PA exists to end.

Fix: never `RemoveAll`. `Lstat` first; create the symlink only if nothing
is there; no-op if it's already the correct symlink; otherwise log and
leave it alone (`internal/adapter/agy.go`).

TDD: wrote `TestAgySetupEnvNeverDeletesExistingConfigSkillsContent`,
confirmed it fails against the pre-fix code (`git stash` the fix,
re-run):
```
agy_test.go:663: setupEnv deleted pre-existing agy-home content on Resume: ... no such file or directory
--- FAIL: TestAgySetupEnvNeverDeletesExistingConfigSkillsContent
```
Restored the fix (`git stash pop`), all `TestAgy*` green including the
new test.

**Non-blocking items, all addressed:**
1. Added the "run `swarm install`" line to Concerns above (was present
   but buried; now first and explicit).
2. Token-copy check: run 1's `find -newer` output (captured, see PA.4
   evidence above) did not list `antigravity-oauth-token` — confirmed not
   rewritten. Run 2 (control) was not checked with `find -newer`. Not
   re-verified against the real HOME per the advisor's own instruction
   not to. If `agy` auth ever fails after a real `swarm install`, `agy
   login` re-establishes it — unrelated to anything this package touches
   (setupEnv only ever symlinks the real token file into a session's
   isolated home; PA never copies or reads it in production code, only
   the PA.4 probe's throwaway scratch homes did, and those are deleted).
3. Probe scope caveat closed empirically: ran a third sealed probe (same
   protocol, real home diffed clean before/after) with a real, non-empty
   legacy `antigravity-cli/skills/dummy-old` present alongside `.migrated`
   and a symlinked `config/skills` probe. agy 1.2.11 never surfaced,
   read, or touched `dummy-old`; `config/skills` and `antigravity-cli/skills`
   were both byte-identical before and after. Confirms decision 2's core
   assumption directly rather than by inference. Full transcript appended
   to `docs/plans/2026-09-24-skill-symlink-probe.md`.
4. Grepped `README.md`/`docs/` for stale old-path references: none live
   (see Concerns above).

Commit: `1b78a27 fix(adapter): agy setupEnv never deletes existing agy-home config/skills`

## Verify after the pre-report self-review

```
$ go build ./... && go vet ./...
(clean)
$ go test ./internal/install/... ./internal/adapter/... ./cmd/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	(cached)
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter	0.902s
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm	2.708s
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm-fake-agent	(cached)
$ go test ./...
... all ok except the same pre-existing baseline failure
(internal/httpapi TestBoardServedAtRoot, web bundle not built)
```

Real home re-checked after the 5th commit: `readlink -f
~/.gemini/antigravity-cli/skills` still resolved to the same
`ses_01M36FPV68.../agy-home/.gemini/config/skills` it did before this
task started.

This report was then sent to the coordinator, who ran an independent
(Opus) review. Its findings and the resulting changes are in "Fix round
1" below.

## Fix round 1 (Opus review, via coordinator)

Findings, from `pa-fix1-findings.md`: decisions D1-D5 confirmed correctly
implemented, the sealed probe method confirmed sound, and a real-home agy
run observed around 08:33 confirmed as NOT originating from this task's
session. One Important finding plus six Minor ones. All addressed, three
new commits.

### Finding 1 (Important) — a symlinked legacy entry aborted the whole repair

`repairAgySkillsRoot`'s `copyTree` call (`plugins.go:394`) walks a
directory tree with `filepath.WalkDir`, which follows a symlinked entry
and then tries `os.ReadFile` on what turns out to be a directory —
erroring out and aborting the entire `swarm install` for agy before
hooks, MCP registration, or `WriteSkills` ever ran, whenever the legacy
dir held even one symlinked entry (e.g. a vendored skill some earlier
install itself had linked in).

RED: `go test ./internal/install/... -run TestWriteAgyRepairSalvagesASymlinkedLegacyEntry -v`
```
agy_test.go:419: WriteAgy must not abort on a symlinked legacy entry: read .../skills/linked: is a directory
--- FAIL: TestWriteAgyRepairSalvagesASymlinkedLegacyEntry
```
GREEN: same command, PASS.

Fix: in the repair loop, `e.Type()&os.ModeSymlink != 0` now recreates the
link itself at the destination (`os.Readlink` + `os.Symlink`) instead of
walking through it with `copyTree`.

### Finding 4 (Minor) — legacy detection only checked the fully-resolved target

`legacyAgySkillsChain` used to `filepath.EvalSymlinks` the *whole* chain
before checking it against `run/launch`, which missed two shapes: a
dangling first hop (a later hop deleted, e.g. a reaped session — there's
still a legacy link that needs repointing even with nothing left to
salvage), and a first hop into `run/launch` whose chain resolves all the
way back to the healthy new root itself (possible once a spawn's own
`agy-home/.gemini/config/skills` is itself a symlink to the real
`config/skills`, per PA.2's `setupEnv` — copying that root into itself
would be both pointless and unsafe).

RED: `go test ./internal/install/... -run 'TestWriteAgyRepairsADanglingLegacyChain|TestWriteAgyRepairsAChainThatResolvesBackToTheNewRoot' -v`
```
agy_test.go:527: a dangling legacy chain must still be repointed at the new root: ".../run/launch/ses_reaped/agy-home/.gemini/config/skills", <nil>
--- FAIL: TestWriteAgyRepairsADanglingLegacyChain
agy_test.go:570: must be repointed straight at the new root: ".../run/launch/ses_x/agy-home/.gemini/config/skills", <nil>
--- FAIL: TestWriteAgyRepairsAChainThatResolvesBackToTheNewRoot
```
GREEN: same command, both PASS.

Fix: `legacyAgySkillsChain` now checks the FIRST hop's (unresolved)
target against `<Home>/run/launch`, and separately attempts full
resolution only to compute what (if anything) to salvage. Also extracted
the shared "is this path under that directory" check into skills.go's
new `underDir` helper, reused by `isSwarmOwned`'s existing symlink-
ownership check (a pure refactor there — same behavior, one definition).
`repairAgySkillsRoot` skips the copy loop entirely (straight to
repointing) when `resolved` is empty (dangling) or equals the new root
itself.

### Finding 3 (Minor) — doctor's message didn't distinguish dangling / missing / healthy

RED: `go test ./internal/install/... -run 'TestCheckAgyWarnsOnADanglingLegacyChainIntoRunLaunch|TestCheckAgyReportsNothingWhenTheOldPathDoesNotExist' -v`
```
agy_test.go:631: dangling chain into run/launch = {... Detail:.../antigravity-cli/skills is not inside a session folder}, want the same warning as a live chain
--- FAIL: TestCheckAgyWarnsOnADanglingLegacyChainIntoRunLaunch
agy_test.go:643: missing old path = {... Detail:.../antigravity-cli/skills is not inside a session folder}, want a neutral OK, not the session-folder phrasing
--- FAIL: TestCheckAgyReportsNothingWhenTheOldPathDoesNotExist
```
GREEN: same command, both PASS. (The dangling case was actually fixed by
finding 4's `legacyAgySkillsChain` change — a dangling first hop into
`run/launch` now reports `ok=true`, so it already gets the warn text.
This test pins that.) `agySkillsRootLegacyCheck` now also special-cases
"nothing at the old path at all" (`os.IsNotExist`) with a distinct,
neutral "nothing at ... yet" message, leaving "is not inside a session
folder" for the case that's actually accurate for it: something present
there that genuinely isn't the legacy shape.

### Finding 5 (Minor) — test coverage gaps

- Extended `TestWriteAgyRepairsALegacySkillsChainIntoRunLaunch`: added a
  genuinely legacy-only entry (`legacy-only-skill`, no pre-existing
  conflict at the new root) and asserted its content actually lands at
  the new root — the original test only proved a same-named,
  already-conflicting entry survived at the *source*, never that salvage
  actually writes to the *destination*.
- Added `TestWriteAgyLeavesARealAntigravityCliSkillsDirAlone` (old path =
  a real, non-symlink directory) and `TestWriteAgyLeavesAnAlreadyHealthySymlinkAlone`
  (old path already points straight at the new root) as explicit
  no-op pins alongside the two new dangling/resolves-back-to-root shapes
  from finding 4.
- One test-construction bug found and fixed along the way: the symlinked-
  legacy-entry test (finding 1) originally pointed its symlink at a path
  *inside* `~/.swarm/skills` (`skillsHome`) by accident, which made the
  entry look swarm-owned and get pruned by `WriteSkills`'s own cleanup
  pass right after the repair ran — not a production bug, a test picking
  a colliding path. Fixed by pointing the test's symlink at an unrelated
  `t.TempDir()` instead.

### Finding 6 (Minor) — probe the actual two-hop production shape

The first sealed probe (PA.4) tested a single symlink hop
(`agy-home/.gemini/config/skills` → a flat probe directory). Production
stacks two: `setupEnv` makes `agy-home/.gemini/config/skills` itself a
symlink to the real `~/.gemini/config/skills`, and — now that
`skillLinkMode[KindAgy]` is `Symlink` — `WriteSkills` makes every entry
*inside* that real directory its own symlink to `~/.swarm/skills/<name>`.

Ran one more sealed probe (same discipline as PA.4: scratch `HOME` under
the scratchpad, no symlink to any real directory, only the four auth
files copied in, real home diffed immediately before and immediately
after the one `agy` invocation):
```
cd $SCRATCH/cwd-prodshape && HOME=$SCRATCH/home-prodshape agy --print-timeout 150s \
  --dangerously-skip-permissions \
  -p "List your available skills by name. If a skill named zz-swarm-symlink-probe exists, use it and tell me the probe codeword."
```
```
### Available Skills
- **agy-customizations**
- **antigravity-guide**
- **zz-swarm-symlink-probe**

The skill [zz-swarm-symlink-probe](file:///$SCRATCH/home-prodshape/.gemini/config/skills/zz-swarm-symlink-probe/SKILL.md) exists.
The probe codeword is: **WALRUS-7726**
```
Nothing written anywhere in the chain (`find ... -newer <stamp>` empty),
probe source unchanged. Real home diffed identical immediately before →
immediately after this run. **No change to the Verdict or
`skillLinkMode[KindAgy]`** — this closes the gap between what the first
probe actually tested and what production actually does. Full transcript
in `docs/plans/2026-09-24-skill-symlink-probe.md`, "Follow-up run 2".

One observation surfaced while re-snapshotting the real home just before
this run: the real `antigravity-oauth-token`'s mtime had moved (08:06 →
09:01) since the last checkpoint, with everything else (the skills
symlink target, `config/` listing, `antigravity-cli/` listing otherwise)
byte-identical. No `agy` invocation of this task's ran in that gap — this
is ambient, real `agy` usage on this machine (consistent with the
reviewer's independent note about the unrelated 08:33 real-home run),
not something this session did. Logged here for transparency; the actual
before/after diff bracketing this task's one probe invocation was clean.

### Finding 7 (Minor) — report/doc gaps

- **Transient over-copy, corrected before any run:** the very first
  scratch-home build (PA.4) briefly used a blanket `find ... -type f`
  copy from the real `~/.gemini/antigravity-cli/`, which pulled in more
  than the four files actually needed (`conversation_summaries.db*`,
  `history.jsonl`, `jetbox_summaries_proto.pb`, etc. — private session
  data, not auth/onboarding state). Noticed before any `agy` invocation
  and corrected to the explicit four-file allowlist
  (`antigravity-oauth-token`, `antigravity_state.pbtxt`,
  `installation_id`, `settings.json`) actually used in every run. No
  `agy` run happened against the over-copied version.
- **Unsealed `agy --version`:** before building any scratch home, this
  task ran `which agy` and `agy --version` against the ambient
  environment (real `$HOME`) to confirm the binary and check its version
  (1.2.11). This is a read-only, side-effect-free version query — not a
  session-creating invocation — but it was outside the seal, unlike every
  other `agy` invocation in this task, which all had `HOME` set
  explicitly to a scratch directory.
- Spec A7 gained an "Accepted trade-offs" note (`setupEnv`'s per-spawn
  `MkdirAll`, a resumed legacy agy-home's stale private copy) and
  decision 3 now says repair also runs from `swarm migrate` step 9 (same
  `install.Agents` → `WriteAgy` path as `swarm install`, confirmed by
  reading `cmd/swarm/migrate.go:70-87`'s `DoInstall`, not a separate code
  path). The plan's PA Files list now includes `skills.go`/`skills_test.go`
  and the new `underDir` helper.

### Commits (fix round 1)

- `2443621 fix(install): agy repair handles symlinked entries and dangling chains` (findings 1, 3, 4, 5)
- `51aebec docs(A7): re-probe production shape, migrate step 9, accepted trade-offs` (findings 2, 6, 7)

(Finding 2's "no code change needed" claim is itself a finding worth
stating plainly: `swarm migrate`'s `DoInstall` already calls
`installAgents` → `install.Agents` → `WriteAgy` → `repairAgySkillsRoot`,
the identical path `swarm install` uses. This was true before fix round
1 too; only the documentation was missing.)

## Final verify (after fix round 1)

```
$ go build ./... && go vet ./...
(clean)
$ go test ./internal/install/... ./internal/adapter/... ./cmd/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.085s (or cached)
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter	(cached)
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm	(cached)
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm-fake-agent	(cached)
$ go test ./...
... all ok except the same pre-existing baseline failure
(internal/httpapi TestBoardServedAtRoot, web bundle not built)
```

Real home re-checked one final time after all 7 commits:
`readlink -f ~/.gemini/antigravity-cli/skills` still resolves to the same
`ses_01M36FPV68.../agy-home/.gemini/config/skills` it did at the start of
this task — untouched throughout, as required. (The unrelated, ambient
oauth-token mtime drift noted under finding 6 is the only real-home
change observed anywhere in this task, and it falls entirely outside
every before/after bracket around this session's own `agy` invocations.)

## Fix round 2 (Opus re-review, via coordinator)

`pa-fix2-findings.md`: fix round 1's findings 1, 3, 4, 5, 6, 7 confirmed
addressed (finding 2 had one remaining doc line, below). One new
Important finding plus two Minors. No probes needed this round. Two new
commits.

### Finding 1 (Important, new) — string comparison instead of same-directory check

`repairAgySkillsRoot` compared `resolved` (the fully
`filepath.EvalSymlinks`'d chain target) against `newRoot` (built from
unresolved `c.Home`) with a plain `!=`. On a home path that itself has a
symlink component — macOS's `/var` → `/private/var`, which is exactly
what `t.TempDir()` gives every test in this package, and can occur on a
real machine's home directory too — the two strings differ even though
they name the identical directory. Fix round 1's own "skip when they're
the same" guard therefore never fired for this case: the code went ahead
and treated `newRoot` as if it were "a distinct directory to copy from",
read its own entries, `RemoveAll`'d each one (the very `dst` it was about
to copy from, since `src` and `dst` were actually the same path on disk),
then failed the subsequent `copyTree` call on the now-missing source —
**deleting real, swarm-owned skill content and aborting the entire
`swarm install` for agy** before hooks, MCP registration, or `WriteSkills`
ever ran.

The reviewer's repro (`pa-fix1-findings.md` → `scratchpad/rr-pa1/head/internal/install/zz_review_test.go`,
`TestReviewResolvesBackWithSwarmLink`) confirmed this against the round-1
code:
```
zz_review_test.go:35: WriteAgy: readlink .../private/var/.../config/skills/swarm: no such file or directory
--- FAIL: TestReviewResolvesBackWithSwarmLink
```

RED (folded into this repo's own test, per the instruction "Extend
TestWriteAgyRepairsAChainThatResolvesBackToTheNewRoot with a swarm-owned
symlink entry"): added a Symlink-mode `swarm` entry at `newRoot` — the
actual production shape once `skillLinkMode[KindAgy]=Symlink` — since the
test's original plain-file entry (`already-healthy.txt`) never exercised
the bug at all: `isSwarmOwned`'s catch-all treats a non-symlink,
non-managed-marker-dir entry as foreign and skips it regardless of the
comparison bug.
```
$ git stash push -- internal/install/agy.go internal/install/plugins.go
$ go test ./internal/install/... -run TestWriteAgyRepairsAChainThatResolvesBackToTheNewRoot -v
agy_test.go:602: readlink .../private/var/.../config/skills/swarm: no such file or directory
--- FAIL: TestWriteAgyRepairsAChainThatResolvesBackToTheNewRoot
$ git stash pop
```
GREEN: same command, PASS.

Fix: added `sameDir(a, b string) (bool, error)`, which compares via
`os.Stat` + `os.SameFile` (device+inode, not string equality), and used
it in place of the `!=` check.

### Finding 3 (Minor) — first-hop check only matched the unresolved launch root

`legacyAgySkillsChain`'s first-hop check (`underDir(target, filepath.Join(c.Home, "run", "launch"))`,
from fix round 1) only matched the unresolved form. A first hop whose
stored text had been spelled using the home path's own resolved form
(again, the `/var` → `/private/var` class of alias) went completely
undetected — not just mis-salvaged, but invisible to both the doctor
warning and the repair.

RED: `TestWriteAgyRepairsALegacyChainSpelledViaTheHomesOwnSymlink` (new),
against the pre-round-2 code:
```
agy_test.go:676: a chain spelled via the home's own symlink must still be detected and repointed: "/private/var/.../run/launch/ses_x/agy-home/.gemini/config/skills", <nil>
--- FAIL
```
GREEN: same command, PASS.

Fix: `legacyAgySkillsChain` now also tries `underDir(target, filepath.EvalSymlinks(launchRoot))`
when the unresolved match fails, matching either spelling.

### Finding 4 (Minor) — symlink handling belonged in `copyTree`, not a special case

Fix round 1's fix for finding 1 (the symlinked-legacy-entry abort) only
special-cased a legacy entry's own top level, inside
`repairAgySkillsRoot`'s loop. A symlink nested *inside* a salvaged real
directory (not the loop's own top-level entry) still hit the original
crash, since `copyTree`'s `filepath.WalkDir` doesn't know about the
special case at all. Also, that special case copied the symlink's target
text verbatim — correct only for an absolute target; a relative one would
resolve from the wrong directory once read back from the destination.

RED, two new tests against the pre-round-2 code:
```
$ go test ./internal/install/... -run 'TestWriteAgyRepairSalvagesANestedSymlinkInsideARealDir|TestWriteAgyRepairResolvesARelativeSymlinkTargetToAbsolute' -v
agy_test.go:478: WriteAgy must not abort on a nested symlink inside a salvaged dir: read .../sk/refs: is a directory
--- FAIL: TestWriteAgyRepairSalvagesANestedSymlinkInsideARealDir
agy_test.go:538: relative link did not resolve from the new location: stat .../config/skills/rel/SKILL.md: no such file or directory
--- FAIL: TestWriteAgyRepairResolvesARelativeSymlinkTargetToAbsolute
```
GREEN: same command, both PASS.

Fix: moved the symlink handling into `copyTree` itself (`internal/install/plugins.go`),
checked before the `d.IsDir()` branch (a symlink-to-directory's own
`DirEntry.Type()` is `ModeSymlink`, not `ModeDir`, so it would otherwise
fall through to the file-copy branch and crash the same way as before —
this is what the original bug actually was). A relative link target is
now resolved against the *source* directory to an absolute path before
being written at the destination. `repairAgySkillsRoot`'s special case
was removed; the loop now just calls `copyTree` unconditionally, same as
before finding 1 existed, and `copyTree` itself handles every depth.
Checked the other `copyTree` caller (cursor's vendored-plugin copy,
`plugins.go:389`) isn't affected: its source tree never contains
symlinks, and `TestSyncVendorsCursorPluginsAsRealFoldersNotSymlinks`
still passes unchanged.

### Finding 2 remainder (Minor)

The plan doc's PA Acceptance bullet said only "`swarm install` repairs
..." — spec A7 decision 3 already named `swarm migrate` step 9 (added in
fix round 1), but the plan's own Acceptance line hadn't been updated to
match. Fixed: `docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md:796`.

### Commits (fix round 2)

- `624f8e0 fix(install): agy repair uses same-file comparison, copyTree handles symlinks generically` (findings 1, 3, 4)
- `ca4b360 docs(plan): PA Acceptance names swarm migrate step 9 too` (finding 2 remainder)

## Final verify (after fix round 2)

```
$ go build ./... && go vet ./...
(clean)
$ go test ./internal/install/... ./internal/adapter/... ./cmd/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.281s
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter	0.711s
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm	2.933s
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm-fake-agent	(cached)
$ go test ./...
... all ok except the same pre-existing baseline failure
(internal/httpapi TestBoardServedAtRoot, web bundle not built)
```

Real home re-checked one final time after all 9 commits:
`readlink -f ~/.gemini/antigravity-cli/skills` still resolves to the same
`ses_01M36FPV68.../agy-home/.gemini/config/skills` it did at the start of
this task. No `agy` invocation ran in this round (per the coordinator's
"no probes needed"); nothing in this round touched the real home at all.

## Fix round 3 (Opus re-review, via coordinator)

`pa-fix3-findings.md`: fix round 2's findings 1-3 confirmed addressed.
Fix round 2's own fix for finding 4 (moving symlink handling into
`copyTree` by *recreating* each symlink at the destination) itself broke
two things — a controller ruling replaced that approach entirely. No
probes needed this round (no real `agy` invocation). Two new commits.

### The problem with fix round 2's "recreate the symlink" approach

1. `copyTree` is also called by `vendorForCursor` (`plugins.go:389`) to
   copy a cloned plugin into `~/.cursor/plugins/local/<name>`, documented
   as producing "the real folder cursor needs (P0-8)" — cursor ignores a
   symlink there entirely. A vendored plugin can legitimately contain an
   intra-tree relative symlink (e.g. `AGENTS.md -> SKILL.md`); fix round
   2's `copyTree` recreated that as a symlink at the destination too,
   breaking cursor's own contract for an entirely unrelated caller.
2. Content `repairAgySkillsRoot` salvages out of a swarm session's
   `run/launch` folder must survive that session later being reaped
   (ordinary swarm session lifecycle — cleaning up a finished session's
   launch directory is not a repair-specific action, it's routine). A
   recreated symlink still pointed back into `run/launch`, so the
   salvaged copy dangled the moment the session was cleaned up — exactly
   the class of fragility this whole package exists to eliminate.

Reviewer repros (`pa-fix3-findings.md` →
`scratchpad/rr-pa2-base/internal/install/zz_review2_test.go`), run
against the round-2 code:
```
$ go test ./internal/install/... -run TestReview2 -v
zz_review2_test.go:41: nested file symlink now a symlink (was a regular file copy): -> .../vendor/plugins/superpowers/skills/b/SKILL.md
--- FAIL: TestReview2CursorNestedFileLink
zz_review2_test.go:61: salvaged intra-tree link dangles after reap: stat .../config/skills/sk/alias.md: no such file or directory
--- FAIL: TestReview2SalvageSurvivesReap
```

### Controller ruling and fix

`copyTree` now **dereferences** every symlink it finds instead of
recreating it: a link to a file becomes a real file holding the target's
content; a link to a directory becomes a real directory holding a
recursive copy of the target's own contents. Guarded against a symlink
cycle (a link back to one of its own ancestors) by a `visited` set of
each dereferenced directory's real (`filepath.EvalSymlinks`'d) path, plus
an unconditional `maxCopyTreeDepth` (32) backstop — either returns a
clear error rather than recursing forever.

`repairAgySkillsRoot`'s salvage loop keeps exactly one exception, per the
ruling: a **top-level** legacy entry that is itself a symlink whose
resolved target lies *outside* `<Home>/run/launch` (the user's own
skill, linked in from somewhere else entirely — not wreckage from the
migration chain this repair exists to clean up) is preserved as a
symlink to that absolute resolved target; everything else (a real file
or directory, or a symlink whose target is itself still inside
`run/launch`) goes through the now-dereferencing `copyTree`.

Reviewer repros GREEN after the fix:
```
$ go test ./internal/install/... -run TestReview2 -v
--- PASS: TestReview2CursorNestedFileLink
--- PASS: TestReview2SalvageSurvivesReap
```

Two of fix round 1/2's own tests asserted the now-superseded
"recreate as a symlink" behavior for cases the ruling changed; updated
(not deleted) to match the new, intentional contract:
- `TestWriteAgyRepairSalvagesASymlinkedLegacyEntry` (top-level entry
  outside `run/launch`, still preserved as a symlink — but now to the
  target's *resolved* form, per the ruling's exact wording): only the
  expected value changed (`filepath.EvalSymlinks(linkTarget)` instead of
  the raw, unresolved `linkTarget`).
- The nested-symlink test, renamed
  `TestWriteAgyRepairSalvagesANestedSymlinkInsideARealDirAndItSurvivesAReap`:
  now asserts the nested entry became a **real directory** (deep-copied,
  since it's nested inside a top-level real dir, not itself the
  top-level entry) holding a copy of the target's contents, and — new —
  asserts that content is still readable after `os.RemoveAll`ing the
  source session's directory, directly proving the "survives a reap"
  property the controller ruling exists for.

RED evidence for both updated tests comes from the reviewer's own repros
above, which exercise the identical code paths (a top-level symlink
outside `run/launch`; a nested symlink-to-directory inside a salvaged
real dir) against the pre-fix round-2 code and failed exactly as shown.
The two tests in this repo were rewritten to match the new contract
directly rather than re-derived RED separately, since round 2's own
version of each test was itself asserting the behavior this round
overturns (a separate red/green cycle against the same bug the
reviewer's repro already pinned would only reconfirm the same fact).
GREEN, run after the fix: full `internal/install` suite passes, including
`TestSyncVendorsCursorPluginsAsRealFoldersNotSymlinks` (the other
`copyTree` caller's own test, confirming cursor's contract holds again)
and a new white-box `TestCopyTreeDetectsASymlinkCycle`
(`skills_internal_test.go`) for the cycle guard.

### Minor — aliased-launch-root fallback resolved the wrong path first

`legacyAgySkillsChain`'s fix-round-2 fallback (`filepath.EvalSymlinks(launchRoot)`,
where `launchRoot = filepath.Join(c.Home, "run", "launch")`) resolves the
*already-joined* path, which requires that specific `run/launch`
subdirectory to already exist. Fixed to resolve `c.Home` first (far more
likely to already exist — it's the swarm home itself) and join
`"run", "launch"` onto the *resolved* home, so a merely-missing
`run/launch` directory can no longer silently hide an aliased dangling
chain. Applied to both places this pattern appeared (`legacyAgySkillsChain`
itself and the salvage loop's own top-level-entry classification, which
needed the same resolved-launch-root form to decide "outside run/launch"
correctly).

### Commits (fix round 3)

- `c0d210b fix(install): copyTree dereferences symlinks into real content` (controller ruling + the aliased-launch-root minor)
- `2519214 docs(A7): document the copyTree dereferencing ruling (fix round 3)`

## Final verify (after fix round 3)

```
$ go build ./... && go vet ./...
(clean)
$ go test ./internal/install/... ./internal/adapter/... ./cmd/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	(cached)
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter	(cached)
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm	(cached)
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm-fake-agent	(cached)
$ go test ./...
... all ok except the same pre-existing baseline failure
(internal/httpapi TestBoardServedAtRoot, web bundle not built)
```

Real home re-checked one final time after all 11 commits:
`readlink -f ~/.gemini/antigravity-cli/skills` still resolves to the same
`ses_01M36FPV68.../agy-home/.gemini/config/skills` it did at the start of
this task. No `agy` invocation ran in this round; nothing touched the
real home.

## Fix round 4 — paused

Paused by coordinator before any file edit was made. Working tree is clean
(no commit needed); head is still `2519214` (fix round 3's last commit).

### What's done
- Read `pa-fix4-findings.md`, the implementer contract, `pa-brief.md`, the
  full fix-round history in this report, current `internal/install/agy.go`,
  `internal/install/plugins.go`, `internal/install/skills_internal_test.go`,
  and the relevant slice of `internal/install/agy_test.go`.
- Read the reviewer repro fixtures named in the findings:
  `scratchpad/rr-pa2-base/internal/install/zz_review2_test.go`
  (`TestReview2CursorNestedFileLink`, `TestReview2SalvageSurvivesReap`) and
  `scratchpad/rr-pa3/internal/install/zz_review3_test.go`
  (`TestReview3TopLevelDangling`, `TestReview3NestedDangling`, plus
  `TestReview3Diamond`/`TestReview3CycleInSalvage` for context — the latter
  expects an error, which this round's ruling supersedes: cycle now skips
  instead of erroring).
- Confirmed `underDir` already exists (`internal/install/skills.go:223`) and
  is the primitive `underLaunchRoot` should wrap.
- Confirmed no existing logger reaches `copyTree` (it's a package-level
  func called from both `Plugins.vendorForCursor`, which has a `p.Log
  io.Writer`, and `agy.go`'s `repairAgySkillsRoot`, which only has a
  `Config` and no logger at all) — planned to use stdlib `log.Printf`
  directly inside `copyTreeGuarded` rather than threading a logger
  parameter through, per the "smallest thing that works" call.
- Worked out the exact diff (not yet applied) — see "Planned but not
  applied" below.

### Planned but not applied (nothing written to disk)
1. **`internal/install/plugins.go`, `copyTreeGuarded`**: on a symlink whose
   `EvalSymlinks`/`Stat` fails (broken target), `log.Printf` one line naming
   the path and `return nil` (skip, don't abort) instead of returning the
   error. On `visited[resolved]` (cycle), same: log-and-skip instead of
   `return fmt.Errorf(...)`. `maxCopyTreeDepth` stays a hard backstop
   (still an error) for anything not literally caught by `visited`.
   `copyTree` (the outer, non-guarded entry point) seeds `visited` with
   `filepath.EvalSymlinks(src)` before the first call, so a link back to
   the tree's own root is caught on first sight rather than after one
   wasted recursive copy. Add a `ponytail:` comment: following a link
   copies the whole target with no size cap; add one if a vendored/legacy
   tree ever links to something huge. Rename the local `real` variable
   (shadows the `real()` builtin) to `resolved`.
2. **`internal/install/agy.go`**: extract `underLaunchRoot(c Config, p
   string) bool` (checks `p` against both `c.Home`'s unresolved and, if
   resolvable, `EvalSymlinks`'d `run/launch` — the pattern currently
   duplicated at `legacyAgySkillsChain` ~213-219 and
   `repairAgySkillsRoot` ~279-284/312-313). Use it in both places, dropping
   the now-redundant local `launchRoot`/`resolvedLaunchRoot`/`evalErr`
   variables. Rename the `real` variable at ~310 (shadows the builtin) to
   `resolvedSrc` (exact name not yet finalized).
3. **`internal/install/skills_internal_test.go`**:
   `TestCopyTreeDetectsASymlinkCycle` (lines 16-34) rewritten to the new
   contract: add a sibling file next to the cyclic `loop` entry, assert
   `copyTree` returns **no error**, the `loop` entry is **absent** from
   `dst`, and the sibling **is** copied.
4. **`internal/install/plugins_test.go`**:
   `TestSyncVendorsCursorPluginsAsRealFoldersNotSymlinks`'s `seedClone`
   gains a nested relative link (`skills/brainstorming/AGENTS.md ->
   SKILL.md`); assert the destination copy has it as a real file holding
   `"# b"`, not a symlink (reviewer repro `TestReview2CursorNestedFileLink`).
5. **`internal/install/agy_test.go`**: three new tests, adapted from the
   reviewer repros to this file's existing style (`fakeHome`,
   `execx.Fake`, `install.WriteAgy`):
   - salvaged file link (`alias.md -> SKILL.md`) survives
     `os.RemoveAll` of the source session dir (`TestReview2SalvageSurvivesReap`).
   - top-level dangling legacy link + a real sibling entry → `WriteAgy`
     succeeds, sibling salvaged, dangling entry simply absent
     (`TestReview3TopLevelDangling`).
   - nested dangling link inside a salvaged real dir → `WriteAgy`
     succeeds, rest of the dir salvaged (`TestReview3NestedDangling`).
6. **Minor comment fixes** (not yet applied): `agy_test.go:563`'s "Read
   back THROUGH the recreated link" comment and the header comment on
   `TestWriteAgyRepairResolvesARelativeSymlinkTargetToAbsolute` (~524-528)
   both still describe fix round 2's "recreate the symlink" behavior;
   round 3 already changed the actual behavior to deep-copy (the test
   itself still passes, only the prose is stale). `agy_test.go:410`'s
   comment reviewed and judged NOT stale as written (it correctly
   describes the one case that IS still recreated as a symlink — an
   entry outside `run/launch`); planned to tighten its wording only to
   make the contrast with the deep-copy path explicit, not to change its
   claim.

### Not started
- No test has been run yet (neither RED nor GREEN) — no code was edited.
- `go build` / `go vet` / the package Verify set have not been re-run this
  round.
- No commit made this round; head is unchanged at `2519214`.

### To resume
Everything above is a fully worked-out plan, not a guess — next step on
resume is: write test changes 3-5 first, run them RED against the
unmodified code (all four should fail the way the reviewer repros already
demonstrated), then apply the `copyTreeGuarded`/`underLaunchRoot`
implementation, run GREEN, apply the comment fixes, run the full package
Verify set from `pa-fix4-findings.md`, then commit (new commits, one per
logical unit, never amend) and append a proper "Fix round 4" section
(replacing this paused one, or added after it) with full RED/GREEN
evidence.

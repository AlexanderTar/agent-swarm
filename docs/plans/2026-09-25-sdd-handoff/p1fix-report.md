# P1 fix report — Skill distribution review findings (rounds 1–2)

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p1fix`, branch `pkg/p1fix`.
Commit: `0168186 fix(install,daemon): stop the daemon's startup skills refresh from reaching the real user's home`

All four findings (C1, I1, I2, M2) landed in one commit: their fixes share
the same functions (`isSwarmOwned`'s new `adopt` parameter serves both C1 §3
and I2; the marker-content write touches both C1 §3 and I1's new helper), so
splitting them into separate commits would have meant staging partial hunks
of the same edit rather than reflecting real, independent units of work. The
contract explicitly allows this ("C1, I1, I2+M2 can share one commit if
small").

## Safety record (required by the dispatch)

Baseline, before any cmd/swarm test ran, and again after every subsequent
full `go test ./cmd/...` and `go test ./...` run in this session:

```
lrwxr-xr-x  1 alexandertar  staff  99 23 Sep 08:02 /Users/alexandertar/.gemini/antigravity-cli/skills -> /Users/alexandertar/.swarm/run/launch/ses_01M36H52AFXG0V5GQ4157FSVB7/agy-home/.gemini/config/skills
drwxr-xr-x    3 alexandertar  staff    96 24 Sep 19:45 swarm
drwxr-xr-x    3 alexandertar  staff    96 24 Sep 19:45 swarm-orchestrator
drwxr-xr-x   3 alexandertar  staff    96 24 Sep 19:45 swarm
drwxr-xr-x   3 alexandertar  staff    96 24 Sep 19:45 swarm-orchestrator
drwxr-xr-x  3 alexandertar  staff   96 24 Sep 19:45 swarm
drwxr-xr-x  3 alexandertar  staff   96 24 Sep 19:45 swarm-orchestrator
drwxr-xr-x   3 alexandertar  staff    96 24 Sep 19:45 swarm
drwxr-xr-x   3 alexandertar  staff    96 24 Sep 19:45 swarm-orchestrator
```

Identical every time: `swarm` and `swarm-orchestrator` as plain directories in
every real kind's skills root, no `swarm-batching`, no dangling symlinks. The
`agy` line is unrelated to this work — `~/.gemini/antigravity-cli/skills`
itself is a symlink into a *live agy session's* home (pre-existing on this
machine), not something this daemon's skills refresh created.

Order followed exactly per the dispatch: C1 step 1 (`daemonConfig.UserHome`
field + `openDaemon` honoring it) and step 5 (`TestMain` HOME override) landed
*before* the first cmd/swarm test of any kind ran. Every `daemonConfig{...}`
literal in `cmd/swarm/{main,daemon_p2,seed}_test.go` was given an explicit
`UserHome: t.TempDir()` on top of that.

## C1 (critical) — daemon startup refresh reaching the real user's skill roots

**Root cause confirmed**: `cmd/swarm/daemon.go`'s `openDaemon` always called
`os.UserHomeDir()` for `userHome`, ignoring `cfg.Home`, and passed it straight
into `install.Config{UserHome: userHome, Home: cfg.Home}` for
`SyncAndRefreshSkills`. `RefreshSkillLinks` (inside `SyncAndRefreshSkills`)
computes each kind's per-kind root from `UserHome` (`~/.claude/skills`, etc.)
— always the *real* home, regardless of `cfg.Home`. No test ever set a
distinct UserHome, so this was live in every daemon test on this machine.

A second, independent hole in the same code path: `ManagedMarker` was written
with empty content (`[]byte{}`). `isSwarmOwned`'s directory branch only
checked the marker's *existence*, never which swarm home wrote it — so a
Copy-mode kind's marker (which gets faithfully copied into every kind's own
root by `copyTreeSynced`, since it's a real file in the source tree) read as
"owned" by *any* skillsHome that asked, including a throwaway test's. That's
the mechanism that actually overwrote content silently in Copy mode
(demonstrated by the RED run below).

**Fix**:
1. `daemonConfig` gets `UserHome string` (default `os.UserHomeDir()` when
   empty); every place in `openDaemon` that used to call
   `os.UserHomeDir()` directly now resolves `userHome` this way once, and
   everything downstream (catalog, ScanRoot default, adapter/advisor deps,
   usage sources, the skills config) uses that one variable.
2. `install.SyncAndRefreshSkills` (not `daemon.go`) now owns the gate: it
   always calls `SyncSkills(c.Home)` (safe for any Home — it only ever writes
   under `c.Home`), but only calls `RefreshSkillLinks` when
   `filepath.Clean(c.Home) == filepath.Join(c.UserHome, ".swarm")` — i.e. only
   when this daemon's own swarm home *is* the canonical default home for this
   real user. Any other Home (a temp dir, `--home ~/.swarm-dev`, every test)
   means "leave the user's real per-kind roots alone." This was placed inside
   the `install` package function itself (not just in `daemon.go`'s call
   site) per advisor review, so the invariant holds for any future caller,
   and the existing `TestSyncAndRefreshSkillsRepairsAnAlreadyInstalledKindsBrokenLink`
   (which already used exactly `Home: filepath.Join(home, ".swarm")`,
   `UserHome: home`) stayed green without modification.
3. `ManagedMarker`'s content is now the skillsHome path that wrote it
   (`syncSkills` writes `dstRoot` into the marker). `isSwarmOwned`'s directory
   branch now reads the marker and compares: content equals this call's
   skillsHome → owned; empty (a pre-this-fix marker) → owned only if `adopt`
   is true; anything else (a different home's path) → never owned. A new
   `adopt bool` parameter threads through `isSwarmOwned` → `linkSkills` /
   `pruneUnregistered` → `LinkSkills`/`WriteSkills` (true: explicit
   `swarm install`/per-spawn action) vs. `RefreshSkillLinks`/
   `anyAlreadyInstalled` (false: the daemon's own automatic startup refresh
   never adopts a legacy/ambiguous entry).
4. Regression test `TestOpenDaemonWithACustomHomeLeavesTheRealUserHomeSkillsUntouched`
   (`cmd/swarm/main_test.go`): seeds a real `WriteSkills` install under a fake
   `userHome` for both Claude (Symlink mode) and Codex (Copy mode), then
   deliberately drifts Codex's real `SKILL.md` content and points Claude's
   real symlink at a now-deleted temp dir (the literal shape of the incident),
   snapshots both trees, opens a daemon with a *different* temp `Home` but the
   *same* `UserHome`, and asserts both trees are byte-for-byte unchanged
   afterward. A companion positive test,
   `TestOpenDaemonWithTheCanonicalHomeStillRefreshesUserHomeSkillLinks`,
   confirms the gate isn't overly strict: when `Home` really is
   `UserHome/.swarm`, a broken link still gets repaired.
5. `cmd/swarm/main_test.go` gets a package-wide `TestMain` that points `$HOME`
   at a throwaway `os.MkdirTemp` dir for the whole test binary — the
   last-resort net for any test (existing or future) that forgets its own
   `UserHome`/`HOME` override.

**TDD evidence**

RED (`go test ./cmd/swarm/ -run TestOpenDaemonWithACustomHomeLeavesTheRealUserHomeSkillsUntouched -v`,
captured *after* C1 steps 1 and 5 were already in place, so this run was safe
— it only ever touched fake temp dirs):
```
--- FAIL: TestOpenDaemonWithACustomHomeLeavesTheRealUserHomeSkillsUntouched (0.05s)
FAIL
FAIL	github.com/AlexanderTar/agent-swarm/cmd/swarm	0.756s
```
(full transcript: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/c1-red.txt`)

GREEN (same command, after the fix):
```
=== RUN   TestOpenDaemonWithACustomHomeLeavesTheRealUserHomeSkillsUntouched
--- PASS: TestOpenDaemonWithACustomHomeLeavesTheRealUserHomeSkillsUntouched (0.05s)
PASS
```
Plus `TestOpenDaemonWithTheCanonicalHomeStillRefreshesUserHomeSkillLinks` PASS,
and the pre-existing `TestOpenDaemonSyncsSkills` / `TestSyncAndRefreshSkillsRepairsAnAlreadyInstalledKindsBrokenLink` still PASS unmodified.

## I1 (important) — WriteIfChanged mode fix applied to every caller

Confirmed: `EditJSON`, and the codex/agy/muse/cursor writers all call
`WriteIfChanged` with a fixed mode argument even on a genuinely-unchanged
config file, so the round-1 mode self-heal reset an operator's deliberately
tightened file (e.g. 0600) back to the caller's mode on every no-op install.

**Fix**: `WriteIfChanged` (`internal/install/files.go`) is a pure
content-diff no-op again (no mode correction). A new
`writeSkillFileSynced` wrapper in `internal/install/skills.go` adds the mode
self-heal back, and only `syncSkills`'s per-file/marker writes and
`copyTreeSynced` (the skills-sync paths) call it.

`TestWriteIfChangedFixesTheModeWhenOnlyThatDrifted` (files_test.go) no longer
describes `WriteIfChanged`'s real contract, so per the finding's own
instruction it was replaced, not silently weakened:
- `TestWriteIfChangedLeavesAContentEqualFileAtItsOwnModeEvenWhenTheModeArgDiffers`
  (files_test.go) — new, asserts a 0600 file with equal content stays 0600
  and `wrote=false`, exactly what the finding asked for.
- `TestSyncSkillsFixesADriftedFileMode` (skills_test.go) — new, moves the
  original mode-self-heal intent onto the skills-sync path
  (`install.SyncSkills`), where it still belongs.

**TDD evidence**: running the pre-fix suite showed
`TestWriteIfChangedFixesTheModeWhenOnlyThatDrifted` as the only failure
(`--- FAIL`, `want wrote=true when only the mode needed fixing`) once
`WriteIfChanged` was reverted — that *was* the RED for this finding, since the
whole point is that this test's old expectation must stop holding. After
replacing it with the two tests above:
```
go test ./internal/install/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	1.178s
```
all green, including both new tests individually verified.

## I2 (important) — pre-A1 adoption widened beyond spec

**Fix**: `internal/install/pre_a1_hashes.go` (new file) holds
`preA1SkillBodyHashes`, the 35 sha256 hex digests of every body
`skills/swarm/SKILL.md` and `skills/swarm-orchestrator/SKILL.md` have ever had
on `main`, generated with:
```
for f in skills/swarm/SKILL.md skills/swarm-orchestrator/SKILL.md; do
  for c in $(git log --format=%H main -- "$f"); do
    git show "$c:$f" | shasum -a 256 | cut -d' ' -f1
  done
done | sort -u
```
(`git log --format=%H --all -- <path>` gave byte-identical results to `main`
for both files in this repo — verified with a diff before committing to
`main`-only, matching the finding's prose even though its parenthetical
example command used `--all`.)

`isPreA1CoreSkillDir` now requires the single `SKILL.md`'s sha256 to be in
that set, not just a frontmatter `name:` match. Combined with the `adopt`
threading from C1 §3, the daemon's own `RefreshSkillLinks` never adopts a
pre-A1 directory at all — only an explicit `swarm install`
(`WriteSkills`/`LinkSkills`) does.

**TDD evidence**: `TestWriteSkillsLeavesAMarkerlessSameNameDirAloneWhenItsBodyWasNeverShipped`
(skills_test.go, new) — a marker-less `swarm` directory with valid
frontmatter (`name: swarm`) but a body that was never shipped is reported
`skipped`, not `changed`, its content is untouched, and no marker is written.
This is RED against the pre-fix name-only check (would have adopted it) and
GREEN after the hash check. The existing
`TestWriteSkillsAdoptsAPreA1RealSkillDirectoryInCopyMode` and
`TestWriteClaudeAdoptsAPreA1RealSkillDirectory` (byte-matching current
content, which is in the hash set since HEAD's blob is the tip of the
collected history) both still pass unmodified — confirming legitimate
adoption still works.

```
go test ./internal/install/... -run 'PreA1|Leaves.*Never'
ok
```
(covered by the full `go test ./internal/install/...` green run above.)

## M2 (minor) — inaccurate "created fresh" comment

Confirmed: `internal/runtime/agents.go:975-976` does
`cwd := filepath.Join(s.Home, "work", a.Name); os.MkdirAll(cwd, 0o700)` —
`MkdirAll` is a no-op on an existing directory, so the cwd is kept across
attempts, not recreated. Both comments in `internal/adapter/claude.go` making
the "created fresh" claim (lines ~105-107 and ~133-134 pre-fix, wrapped across
lines so a single-line grep for "fresh" caught them but not the exact wording)
were corrected to describe the real behavior (MkdirAll'd once,
kept/reused across attempts).

## Verify (full set, run at the end)

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills
(git status: no diff — mirror already matched)

$ go test ./internal/install/... ./internal/adapter/... ./internal/migrate/... ./cmd/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter
ok  	github.com/AlexanderTar/agent-swarm/internal/migrate
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm
ok  	github.com/AlexanderTar/agent-swarm/cmd/swarm-fake-agent

$ go build ./... && go vet ./...
(clean)

$ go test ./...
... every package ok, except the documented pre-existing baseline failure:
--- FAIL: TestBoardServedAtRoot (internal/httpapi, web bundle not built)
```

Real-home skills listing recorded before and after every cmd/swarm and full
`go test ./...` run in this session: identical every time (see Safety record
above).

## Files changed

- `cmd/swarm/daemon.go` — `daemonConfig.UserHome`, `openDaemon` honors it.
- `cmd/swarm/main_test.go` — `TestMain` HOME safety net, `UserHome` added to
  every `daemonConfig{...}` literal, `snapshotTree` helper, two new C1
  regression tests.
- `cmd/swarm/daemon_p2_test.go`, `cmd/swarm/seed_test.go` — `UserHome` added
  to their `daemonConfig{...}` literals.
- `internal/install/skills.go` — `isSwarmOwned`/`isPreA1CoreSkillDir` rewrite
  (marker-home tie, hash-based adoption), `adopt` threaded through
  `linkSkills`/`pruneUnregistered`/`LinkSkills`/`WriteSkills`/
  `RefreshSkillLinks`/`anyAlreadyInstalled`/`CheckSkills`, `SyncAndRefreshSkills`'s
  new gate, `writeSkillFileSynced` helper, marker content write.
- `internal/install/pre_a1_hashes.go` — new, the 35-hash set.
- `internal/install/files.go` — `WriteIfChanged` reverted to pure content-diff.
- `internal/install/claude.go`, `internal/install/legacy.go`,
  `internal/install/uninstall.go` — updated `isSwarmOwned` call sites for the
  new `adopt` parameter (all `true`: explicit actions or symlink-only checks
  where it's moot).
- `internal/install/files_test.go`, `internal/install/skills_test.go` — new
  and replaced tests as described above.
- `internal/adapter/claude.go` — M2 comment fixes.

## Self-review notes / judgment calls

1. **`CheckSkills` uses `adopt=false`.** Not explicitly ruled by the findings
   (only "the daemon refresh never adopts" is stated). Chose `false` for
   consistency with the daemon's own view of ownership — `doctor` reports a
   pre-this-fix legacy install as "user-owned" until an explicit
   `swarm install` upgrades its marker, rather than doctor and the daemon
   silently disagreeing about the same directory. Verified this doesn't
   regress `TestDoctorChecksSkillsForEveryKind` (calls `WriteSkills` first, so
   markers are always fresh-format) or
   `TestCheckSkillsReportsTheExactUserOwnedDetail` (its fixture has no marker
   and invalid frontmatter either way).
2. **`--all` vs `main` for the pre-A1 hash history.** The finding's prose says
   "on main" but its parenthetical example command uses `--all`. Verified
   `git log --format=%H --all` and `--format=%H main` give byte-identical
   commit lists for both `SKILL.md` files in this repo, so this didn't matter
   in practice; documented "main" in the generating comment to match the
   stated intent.
3. **The pre-A1 hash set will need regenerating whenever `skills/swarm/SKILL.md`
   or `skills/swarm-orchestrator/SKILL.md` next changes**, if a future
   marker-less-directory-adoption test wants to use the *new* current body as
   its fixture (today's adoption tests work only because HEAD's blob is the
   tip of the collected history). Not a defect — just noting it so a future
   skill edit doesn't get surprised by an adoption test failing until the
   hash list is regenerated. Left unhandled per ponytail (no speculative
   automation added for a one-command regen).
4. **Where the gate lives.** Placed the Home-vs-UserHome equality check inside
   `install.SyncAndRefreshSkills` rather than in `cmd/swarm/daemon.go`'s call
   site (my first draft had it in `daemon.go`) — advisor recommended this so
   the invariant protects any future caller of `SyncAndRefreshSkills`, not
   just this one call site, and it kept the existing
   `TestSyncAndRefreshSkillsRepairsAnAlreadyInstalledKindsBrokenLink` green
   without modification.

## Advisor self-review (before reporting done)

Called the advisor after the first commit and before declaring done, as the
contract requires. It found three real gaps:

**1. C1 §3 (marker names another home) had no direct test.** The daemon
regression test proved the `SyncAndRefreshSkills` Home-vs-UserHome gate only
— it never let `RefreshSkillLinks` run, so `isSwarmOwned`'s marker-content
branch was never reached by any test, and no install-package test seeded a
foreign or empty marker outside the `adopt=true` path. Added two tests
(commit `3ed946a`):
- `TestWriteSkillsLeavesADirAloneWhenItsMarkerNamesADifferentSwarmHome` — a
  foreign marker is skipped even by an explicit `WriteSkills` (adopt=true).
- `TestSyncAndRefreshSkillsLeavesAnEmptyMarkerDirAloneButWriteSkillsAdoptsIt`
  — the adopt=false/adopt=true asymmetry for an empty (pre-this-fix) marker,
  exercised through the real `SyncAndRefreshSkills` entry point.

RED captured for both by temporarily restoring the old content-blind marker
check (`if _, err := os.Stat(marker); err == nil { return true, nil }`) in
`isSwarmOwned`, then reverting (`git checkout -- internal/install/skills.go`):
```
--- FAIL: TestWriteSkillsLeavesADirAloneWhenItsMarkerNamesADifferentSwarmHome (0.01s)
    skills_test.go:587: a dir with a foreign marker was not reported skipped: []
    skills_test.go:591: changed reports the foreign-marker dir .../swarm
    skills_test.go:596: the foreign-marker dir's content was touched: "---\nname: swarm\n..." (the real swarm body)
--- FAIL: TestSyncAndRefreshSkillsLeavesAnEmptyMarkerDirAloneButWriteSkillsAdoptsIt (0.01s)
    skills_test.go:628: SyncAndRefreshSkills touched an empty-marker dir: "---\nname: swarm\n..." (the real swarm body)
FAIL	github.com/AlexanderTar/agent-swarm/internal/install	0.468s
```
GREEN after reverting: `go test ./internal/install/... ` → `ok`.

**2. I1 and I2's new tests had only ever been run GREEN**, i.e. written after
the fix, so the failing-old-test-in-the-suite evidence proved the revert
happened but not that the *new* test would have caught the *original* bug on
its own. Captured properly, post hoc, no code changes resulted:
- I1: temporarily restored the chmod-on-equal-content branch in
  `WriteIfChanged`, ran
  `TestWriteIfChangedLeavesAContentEqualFileAtItsOwnModeEvenWhenTheModeArgDiffers`:
  ```
  --- FAIL: TestWriteIfChangedLeavesAContentEqualFileAtItsOwnModeEvenWhenTheModeArgDiffers (0.00s)
      files_test.go:81: want wrote=false: content-equal must be a true no-op
      files_test.go:88: mode = -rw-r--r--, want the operator's own 0600 left alone
  ```
  Reverted (`git checkout -- internal/install/files.go`); GREEN after.
- I2: temporarily restored `frontmatterField(body, "name") == name` as
  `isPreA1CoreSkillDir`'s only check, ran
  `TestWriteSkillsLeavesAMarkerlessSameNameDirAloneWhenItsBodyWasNeverShipped`:
  ```
  --- FAIL: TestWriteSkillsLeavesAMarkerlessSameNameDirAloneWhenItsBodyWasNeverShipped (0.01s)
      skills_test.go:712: the user's own same-name dir was not reported skipped: []
      skills_test.go:716: changed reports the user-owned dir .../swarm
      skills_test.go:721: the user's own SKILL.md was touched: "---\nname: swarm\n..." (the real swarm body)
      skills_test.go:724: a marker was written into the user's own dir: <nil>
  ```
  Reverted (`git checkout -- internal/install/skills.go`); GREEN after.

**3. Deployment consequence of the fix, checked read-only.** The real
machine's roots (per the Safety record above) are marker-less, single-file,
pre-A1-shaped directories — the controller's own repair of the live-machine
incident. Ran (read-only, no writes):
```
$ shasum -a 256 ~/.claude/skills/{swarm,swarm-orchestrator}/SKILL.md \
    ~/.codex/skills/{swarm,swarm-orchestrator}/SKILL.md \
    ~/.cursor/skills/{swarm,swarm-orchestrator}/SKILL.md \
    ~/.config/muse/skills/{swarm,swarm-orchestrator}/SKILL.md
```
All 8 files hash to exactly the two values already in
`pre_a1_hashes.go` (`3f6d34e2...` for `swarm`, `77267c0f...` for
`swarm-orchestrator` — both are HEAD's own current bodies). **Conclusion:
this machine's real roots will be adopted correctly by a future explicit
`swarm install`** (byte-match → `isPreA1CoreSkillDir` → adopted); until then,
`doctor` will report them "user-owned" (CheckSkills, adopt=false) and the
daemon's own automatic refresh will leave them alone (RefreshSkillLinks,
adopt=false) rather than silently repairing them — both correct per the
findings' rulings, but worth the controller knowing this is the concrete,
expected post-merge state on this machine until the next `swarm install`.

Full verify set re-run clean after this round (`make skills-sync`; `go test
./internal/install/... ./internal/adapter/... ./internal/migrate/... ./cmd/...`;
`go build ./... && go vet ./...`); real-home listing re-checked, still
byte-identical to the original baseline.

## Fix round 2 (Opus re-review: C1, I1, I2, M2 all ADDRESSED; four follow-ups)

Findings: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p1-fix2-findings.md`.
Commits: `bfb6f78 test(install): freeze pre-A1 fixtures to historical bodies; isolate the refresh gate`,
`c7a458d fix(install): CheckSkills recognizes a pre-A1 install as swarm-owned`,
`39febe7 test(cmd/swarm): TestMain also scrubs SWARM_HOME and SWARM_URL`.

### Item 1 (important) — pre-A1 fixtures coupled to the current skill body

`TestWriteSkillsAdoptsAPreA1RealSkillDirectoryInCopyMode` and
`TestWriteClaudeAdoptsAPreA1RealSkillDirectory` built their fixture from
`install.SkillBody(name)` (the current embedded body), which only worked
because HEAD's own blob happens to be in `preA1SkillBodyHashes` — P3 changing
`skills/swarm/SKILL.md` next would have broken both for a reason unrelated to
the adoption logic under test.

**Fix**: added `internal/install/testdata/pre_a1_swarm_skill.md` and
`pre_a1_swarm_orchestrator_skill.md`, frozen bodies captured with
`git show 90918bd:skills/swarm/SKILL.md` / `...swarm-orchestrator/SKILL.md`
(commit `90918bd`, "ship the swarm and swarm-orchestrator skills for every
agent" — the first commit that shipped either file, i.e. genuinely pre-A1).
Both hashes (`745b65a3...`, `8745780f...`) are permanent entries in
`pre_a1_hashes.go`. Added a `readTestdata(t, name)` helper and switched both
tests to it. Corrected the file-level comment in `pre_a1_hashes.go` (removed
"including the current one"; the list is a frozen 2026-09-24 snapshot).

**Decoupling verified directly** (not just by inspection): temporarily
appended a line to the real `skills/swarm/SKILL.md` (simulating a P3-style
edit), ran `cp -R skills internal/install/skills` to refresh the embed, and
re-ran both tests:
```
=== RUN   TestWriteSkillsAdoptsAPreA1RealSkillDirectoryInCopyMode
--- PASS: TestWriteSkillsAdoptsAPreA1RealSkillDirectoryInCopyMode (0.01s)
=== RUN   TestWriteClaudeAdoptsAPreA1RealSkillDirectory
--- PASS: TestWriteClaudeAdoptsAPreA1RealSkillDirectory (0.01s)
```
(`TestEmbeddedMirrorMatchesCanonicalTree` failed in that same run, as
expected — an artifact of my manual `cp -R` into an already-populated target
directory nesting `skills/skills/...`, not a real drift; irrelevant to the
point being proven.) Reverted the real file
(`git checkout -- skills/swarm/SKILL.md` equivalent — restored from a
pre-edit backup copy) and re-ran `make skills-sync` before committing;
`git status` showed no diff.

### Item 3 (fold in) — the Home-vs-UserHome gate was never isolated by a test

Every round-1 regression test passed even without the gate at
`skills.go:565`, because their fixtures' markers never matched the test's own
computed `skillsHome` — the marker-content check alone (C1 §3) was doing all
the protecting, and the gate itself (C1 §2) had no test that would fail if
it were deleted.

**Fix**: added
`TestSyncAndRefreshSkillsGateBlocksRefreshEvenWhenTheMarkerMatchesThisCallsOwnSkillsHome`
— seeds a `UserHome` entry whose `.swarm-managed` marker names exactly the
`skillsHome` this call's own (non-canonical) `Home` computes, so the
marker-content check alone would call it owned; only the gate can still
block it.

RED, gate temporarily removed (`skills.go:565`'s `if filepath.Clean(c.Home)
!= filepath.Join(c.UserHome, ".swarm") { return nil, nil }` deleted, leaving
`RefreshSkillLinks` unconditional), then reverted:
```
--- FAIL: TestSyncAndRefreshSkillsGateBlocksRefreshEvenWhenTheMarkerMatchesThisCallsOwnSkillsHome (0.01s)
    skills_test.go:679: the gate did not block the refresh: "---\nname: swarm\n..." (the real swarm body, recopied)
```
GREEN after `git checkout -- internal/install/skills.go` (which also
required re-applying item 2's not-yet-committed `CheckSkills` change on top,
since that revert rolled back to the last commit — redone and re-verified).

### Item 2 (minor, fold in) — doctor calls a pre-A1 install "user-owned"

`CheckSkills` used `adopt=false` (matching the daemon's own automatic
refresh), so a marker-less pre-A1 install — this machine's actual real shape,
confirmed in round 1's Concerns — is reported "user-owned" even though
`swarm install` would recognize it as swarm's own. Doctor never writes
anything, so it carries none of the daemon-refresh risk that justified
`adopt=false` there.

**Fix**: `CheckSkills`'s one `isSwarmOwned` call now passes `adopt=true`.

RED: `TestCheckSkillsRecognizesAPreA1InstallAsSwarmOwned` (seeds a real
`WriteSkills` install, then replaces the "swarm" entry with the frozen
pre-A1 testdata fixture, marker-less) failed against `adopt=false`:
```
--- FAIL: TestCheckSkillsRecognizesAPreA1InstallAsSwarmOwned (0.01s)
    doctor_p5_test.go:221: a pre-A1 install was reported user-owned: {Name:Codex skills OK:true Detail:skill swarm for Codex is user-owned; swarm's copy is not installed there}
```
GREEN after flipping to `adopt=true`. `TestDoctorChecksSkillsForEveryKind`
and `TestCheckSkillsReportsTheExactUserOwnedDetail` (genuinely non-shipped
content) stay green unmodified — this closes the Concern flagged at the end
of round 1's report (this machine's real roots will now report OK via
`doctor`, not "user-owned").

### Item 4 (fold in) — TestMain should also scrub SWARM_HOME/SWARM_URL

`defaultHome()`/`defaultURL()` (`cmd/swarm/main.go`) check `SWARM_HOME` /
`SWARM_URL` before falling back to the real home / `localhost` — a runner
with either exported could point an under-specified test's default at
something real, same class of leak the `HOME` override exists to prevent.

**Fix**: `runCmdSwarmTests` (`TestMain`) now also calls
`os.Unsetenv("SWARM_HOME")` / `os.Unsetenv("SWARM_URL")`.

RED, run with a simulated leaking runner shell:
```
$ SWARM_HOME=/tmp/should-not-leak SWARM_URL=http://example.invalid go test ./cmd/swarm/ -run TestMainScrubsSwarmHomeAndSwarmURLFromTheEnvironment -v
    main_test.go:64: SWARM_HOME leaked into a test: "/tmp/should-not-leak"
    main_test.go:67: SWARM_URL leaked into a test: "http://example.invalid"
--- FAIL
```
GREEN after the fix, same command:
```
--- PASS: TestMainScrubsSwarmHomeAndSwarmURLFromTheEnvironment (0.00s)
```
Re-ran the *entire* `./cmd/...` suite with both vars exported too — all
green, and the real-home skills listing stayed byte-identical before/after
(recorded below).

### Verify (round 2, full set)

```
$ make skills-sync            # no diff
$ go test ./internal/install/... ./internal/adapter/... ./internal/migrate/... ./cmd/...
ok  	.../internal/install
ok  	.../internal/adapter
ok  	.../internal/migrate
ok  	.../cmd/swarm
ok  	.../cmd/swarm-fake-agent
$ go build ./... && go vet ./...
(clean)
$ go test ./...
... all ok except the documented baseline TestBoardServedAtRoot (web bundle not built)
```

Real-home skills listing, checked before and after every cmd/swarm run this
round (including the SWARM_HOME/SWARM_URL-leaking simulated run): identical
to round 1's baseline every time.

### Files changed (round 2)

- `internal/install/pre_a1_hashes.go` — comment correction only.
- `internal/install/testdata/pre_a1_swarm_skill.md`,
  `pre_a1_swarm_orchestrator_skill.md` — new, frozen historical fixtures.
- `internal/install/skills_test.go` — `readTestdata` helper, both pre-A1
  adoption tests repointed at testdata, new gate-isolation test.
- `internal/install/skills.go` — `CheckSkills`'s `isSwarmOwned` call flipped
  to `adopt=true`; comment updated.
- `internal/install/doctor_p5_test.go` — new
  `TestCheckSkillsRecognizesAPreA1InstallAsSwarmOwned`.
- `cmd/swarm/main_test.go` — `TestMain` also unsets `SWARM_HOME`/`SWARM_URL`;
  new `TestMainScrubsSwarmHomeAndSwarmURLFromTheEnvironment`.

## Concerns

- **Resolved by round 2, item 2** (was open after round 1): the real
  `~/.claude/skills/swarm` etc. are pre-A1-shaped. Round 1 left them reading
  as "user-owned" to `doctor` until an explicit `swarm install`; round 2's
  `CheckSkills` `adopt=true` fix means `doctor` now reports them OK directly
  (confirmed: their hashes match the shipped set — see round 1's read-only
  `shasum` check). The daemon's own automatic refresh still leaves them alone
  (`RefreshSkillLinks`, `adopt=false`) until an explicit `swarm install`
  writes fresh markers — that's intentional per both rounds' rulings, not a
  gap.
- Deferred per the dispatch: M1 (python3 warn level), M3, M4, M5 — not
  touched.

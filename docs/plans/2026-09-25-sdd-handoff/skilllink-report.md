# Skill-link symlink check — empirical results (2026-09-24)

Worktree: `/Users/alexandertar/GitHub/agent-swarm--skilllink` (branch `pkg/skilllink`)

## Versions (all at `~/.local/bin`)

| CLI | `--version` |
|---|---|
| claude | 2.1.282 (Claude Code) |
| codex | codex-cli 0.155.1 |
| agy | 1.2.10 |
| cursor-agent | 2026.09.23-86fc751 |
| muse | Muse Code 1.3.0 (1.3.0-R3401.1) |

## Method

For each CLI: created a probe skill outside every skills root at
`scratchpad/skillprobe/zz-swarm-symlink-probe/SKILL.md` (frontmatter `name:
zz-swarm-symlink-probe`, `description: "Probe skill; when asked for the probe
codeword, answer PELICAN-7731"`, body restating the codeword). Symlinked it
into the kind's `Config.SkillsDir(kind)` root as `zz-swarm-symlink-probe`, ran
the CLI headless, and looked for `PELICAN-7731` or the skill name in the
output. Removed the probe after every run (`rm` at a captured, guarded path)
and verified removal with `ls`/`find`. `swarm`/`swarm-orchestrator` entries in
every root were left untouched (confirmed unmodified before and after).

## Result: all five discover a symlinked skill directory

| Kind | Symlink discovered? | Command | Evidence |
|---|---|---|---|
| claude | Yes (pre-existing, unchanged) | — | Already `Symlink` in the map; not re-tested this session. |
| codex | **Yes** | `codex exec -s read-only "<prompt>"` (cwd = worktree, git repo required) | Listed `zz-swarm-symlink-probe` among its skills, ran `cat ~/.codex/skills/zz-swarm-symlink-probe/SKILL.md` (followed the symlink transparently) and reported `PELICAN-7731`. |
| agy | **Yes** — but only with a fresh, never-used isolated `HOME` (see below) | `HOME=<fresh dir> agy --print-timeout 150s --dangerously-skip-permissions -p "<prompt>"` | Listed `zz-swarm-symlink-probe` alongside `swarm`/`swarm-orchestrator` and reported `PELICAN-7731`. |
| cursor-agent | **Yes** | `cursor-agent -p --mode ask --trust "<prompt>"` | Listed `zz-swarm-symlink-probe` and reported `PELICAN-7731` on the very first (bare) run. |
| muse | **Yes** | `muse skills list --json --source user` / `muse skills inspect zz-swarm-symlink-probe --source user --json`, confirmed live with `muse exec "<prompt>"` | Listing's `description` field for the probe entry contains the codeword text (read through the symlink); `inspect` succeeds; live `muse exec` output includes `zz-swarm-symlink-probe` in its skill list and `Probe codeword: PELICAN-7731`. |

## agy detail — a real dead end, then the actual answer

A bare `agy --mode plan --print-timeout 150s --dangerously-skip-permissions -p
"<prompt>"` run under the operator's normal `$HOME` did **not** list the
probe — nor did it list the pre-existing `swarm`/`swarm-orchestrator` skills
that are definitely installed there (both real directories with valid
frontmatter). A control test (replacing the symlink with a real copy of the
same probe, same root) also failed to be discovered. That ruled out "symlink
not followed" as the explanation — neither format was visible.

Isolated the `--mode plan` variable specifically (all three failing runs had
used it; both succeeding isolated-HOME runs hadn't): re-ran bare `$HOME`
*without* `--mode plan` (`agy --print-timeout 150s
--dangerously-skip-permissions -p "<prompt>"`) against the same symlinked
probe. Still no discovery, and `swarm`/`swarm-orchestrator` were still
missing from the list too — so `--mode plan` was not the confound. The
precise root cause of the bare-`$HOME` negative was not isolated further
(plausibly a stale/cached catalog on that particular long-lived `$HOME`, but
that's unconfirmed); what's confirmed is that it's not a symlink-following
problem, since a real (non-symlink) copy failed identically at the same
root.

`internal/adapter/agy.go`'s `setupEnv` shows how swarm actually launches agy
in production: every spawn gets a **brand-new** per-session `HOME`
(`<launchDir>/agy-home`) whose `.gemini/antigravity-cli` is a fresh symlink to
the real `~/.gemini/antigravity-cli` (which is where `Config.SkillsDir(KindAgy)`
= `~/.gemini/antigravity-cli/skills` lives). Reproducing exactly that — a
fresh temp `HOME` with only `.gemini/antigravity-cli` symlinked to the real
one (skills reachable through it) and `.gemini/config/plugins` symlinked (for
parity with setupEnv) — the same prompt against the same symlinked probe
correctly listed `swarm`, `swarm-orchestrator`, and `zz-swarm-symlink-probe`,
and reported `PELICAN-7731`. This matches production behavior (every real
swarm spawn is a fresh per-session HOME), so **agy → Symlink** is the correct
verified conclusion; the bare/no-override run is a false negative from an
unrelated stale-catalog issue on this dev machine's default `$HOME`, not
representative of how swarm invokes agy.

## Config.SkillsDir(KindAgy) oddity (as flagged in the dispatch)

On this machine, the *real* `~/.gemini/antigravity-cli/skills` is itself
already a multi-hop symlink chain:
```
~/.gemini/antigravity-cli/skills
  -> /Users/alexandertar/.swarm/run/launch/ses_01M36H52AFXG0V5GQ4157FSVB7/agy-home/.gemini/config/skills
  -> /Users/alexandertar/.swarm/run/launch/ses_01M36FPV68G9S2S3SCDAJB1AF6/agy-home/.gemini/config/skills   (terminal, real dir; holds `swarm/` and `swarm-orchestrator/`)
```
This is leftover state from some earlier session/experiment on this dev
machine, not something `adapter/agy.go`'s `setupEnv` itself produces (that
code only ever symlinks *inside* a session's isolated home, pointing outward
at the real `~/.gemini/antigravity-cli`; it never touches the real path
itself). It did not block this check — the probe was planted at the terminal
(`readlink -f`) target either way — but it's worth someone eventually
resolving, since a stray reversed symlink at the real path is surprising
state to carry on a dev machine. Out of scope for this task; not touched.

## Codeword capture (representative)

- codex: `Probe codeword: **PELICAN-7731**` (read via `cat` of the symlinked path).
- agy (fresh isolated HOME): `**Probe Codeword:** \`PELICAN-7731\`` with file link
  `.../agy-test-home2/.gemini/config/skills/zz-swarm-symlink-probe/SKILL.md`.
- cursor-agent: `**Probe codeword:** \`PELICAN-7731\`` plus a full skill list
  including `zz-swarm-symlink-probe`.
- muse: `skills list --json` entry `"description": "Probe skill; when asked
  for the probe codeword, answer PELICAN-7731"`; live `muse exec` output line
  `Probe codeword: PELICAN-7731`.

## Cleanup verification

After every probe was removed:
```
find ~/.swarm/run/launch -maxdepth 6 -iname zz-swarm-symlink-probe   -> (nothing)
find ~/.gemini -maxdepth 6 -iname zz-swarm-symlink-probe             -> (nothing)
find ~/.codex ~/.cursor ~/.config/muse ~/.claude -maxdepth 4 -iname zz-swarm-symlink-probe -> (nothing)
```
`swarm`/`swarm-orchestrator` `SKILL.md` bodies in the agy terminal directory
were re-checked and are unchanged. Scratchpad-local isolated-HOME test dirs
(`agy-test-home`, `agy-test-home2`) were removed too (session scratchpad, not
user config).

## Code change

`internal/install/skills.go`: `skillLinkMode` flipped from
`{claude: Symlink, codex/agy/cursor/muse: Copy}` to all five `Symlink`, with
the doc comment rewritten to summarize this check (superseding the stale
"only claude is installed" note).

**Intentional test change**: `internal/install/skills_test.go`,
`TestWriteSkillsRemovesASymlinkBeforeCopyingRatherThanFollowingIt`. This test
used `install.WriteSkills(c, install.KindCodex)` purely as a stand-in for "a
Copy-mode kind" to exercise the Copy-mode anti-symlink-follow safety net —
it was never actually testing anything codex-specific. Now that every real
`Kind` is `Symlink`, there is no Copy-mode kind left to stand in. Rewrote it
to call the mode-explicit exported API, `install.LinkSkills(root, skillsHome,
install.Copy)`, directly against a throwaway root — same assertions, same
behavior under test, no longer coupled to any particular `Kind`'s mode. No
test was deleted or weakened; the assertions are byte-for-byte the same.

No other test in the repo pinned a Copy-mode expectation for
codex/agy/cursor/muse specifically (checked via `grep -rn
"SkillLinkMode\|ModeSymlink\|Lstat" internal/adapter/
internal/install/*_test.go`); the other `KindCodex`-using tests in
`skills_test.go` don't assert symlink-vs-copy and passed unchanged.

## Verify

```
$ go build ./... && go vet ./...
(clean)

$ go test ./internal/install/... ./internal/adapter/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	3.123s
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter	(cached)

$ go test ./...
... all ok except:
--- FAIL: TestBoardServedAtRoot (internal/httpapi) — pre-existing baseline
    failure per the implementer contract (web bundle not built). Unrelated
    to this change.
```

## Files changed

- `internal/install/skills.go` — `skillLinkMode` map + doc comment.
- `internal/install/skills_test.go` — one test's Copy-mode exercise
  decoupled from `KindCodex`.

## Self-review / judgment calls

- Used a fresh isolated `HOME` for agy's test run (mirroring
  `adapter/agy.go`'s `setupEnv`) rather than accepting the bare-`$HOME`
  negative at face value, because the bare run's control (real copy) also
  failed identically for `swarm`/`swarm-orchestrator`, which are definitely
  real, long-installed skills — strong evidence the bare run wasn't
  exercising the discovery mechanism at all, not evidence against symlink
  support specifically. Documented this reasoning in the code comment so a
  future reader isn't misled by a superficial "bare agy run found nothing"
  retest.
- Did not touch the pre-existing reversed `~/.gemini/antigravity-cli/skills`
  symlink chain on this dev machine; flagged it as an oddity per the
  dispatch but left it alone (out of scope, and it's shared live state on a
  machine running other agents).
- Chose the throwaway-root fix for the one broken test over inventing a new
  synthetic `Kind` or exporting `skillLinkMode` for testing — `LinkSkills`
  already existed as the mode-explicit API this test needed.
- `TestWriteSkillsRemovesASymlinkBeforeCopyingRatherThanFollowingIt` now
  calls `LinkSkills`, not `WriteSkills` — its name says `WriteSkills`. Left
  the name as-is (both route to the same `linkSkills(..., adopt=true)`
  underneath, and the test's actual behavior under test — Copy mode never
  follows a leftover symlink — is unchanged); flagging the name/body
  mismatch here so a reviewer doesn't read it as an unrelated oversight.
- Checked for agy side effects from the repeated runs:
  `~/.gemini/antigravity-cli/settings.json`'s `trustedWorkspaces` does not
  contain this worktree's path — agy did not persist anything from these
  probe runs beyond its own normal session state.

## Fix round 1 (Opus review)

Findings doc: `scratchpad/sdd/skilllink-fix1-findings.md`.

**Safety note acknowledged**: the isolated-HOME agy probe runs in the
original pass caused agy 1.2.10 to migrate real, shared state
(`swarm`/`swarm-orchestrator` under `~/.gemini/antigravity-cli/skills`) into
a scratch `HOME` that was then deleted during cleanup, leaving the live
`~/.gemini/antigravity-cli/skills` symlink dangling. The controller restored
it. Verified read-only (no writes) before starting this round:
```
$ readlink ~/.gemini/antigravity-cli/skills
/Users/alexandertar/.swarm/run/launch/ses_01M36H52AFXG0V5GQ4157FSVB7/agy-home/.gemini/config/skills
```
Confirmed intact. **No CLI probes were run this round and nothing under `~`
was touched**, per the review's explicit instruction.

### What changed

1. **agy reverted to Copy.** The apparent Symlink positive from the original
   pass was not proof of symlink-following: agy reads skills from
   `$HOME/.gemini/config/skills` and migrates
   `.gemini/antigravity-cli/skills` (`Config.SkillsDir(KindAgy)`) there on
   first run in a fresh `HOME`. The isolated-HOME run that "found" the probe
   was reading that migrated copy (its own reported file path was under
   `.gemini/config/skills`, not `.gemini/antigravity-cli/skills`) — not
   following a symlink placed at the location swarm actually installs to.
   The two earlier bare-`HOME` runs, which used that exact location without
   triggering a fresh-`HOME` migration, found nothing at all (symlink or
   real copy alike). `skillLinkMode[KindAgy]` is back to `Copy` — the
   pre-existing safe default, now `unverified` rather than `disproven`.
2. **`internal/install/skills.go` comment rewritten**: no more "stale
   catalog" claim (removed per the review), no claim that claude was
   re-run (it wasn't; its Symlink predates this check). The comment is now
   a verdict-per-kind summary pointing at the new durable-evidence doc.
3. **New durable evidence doc**: `docs/plans/2026-09-24-skill-symlink-probe.md`
   — per CLI: version, exact command, exact prompt, relevant output lines,
   verdict; the full agy migration finding (what was run, the smoking-gun
   file path from run 4's own output, why it doesn't prove Symlink, and the
   safety incident); a recorded, not-implemented follow-up (move
   `Config.SkillsDir(KindAgy)` to `~/.gemini/config/skills`, update
   `adapter/agy.go`'s `setupEnv` to link straight there, then re-probe in a
   HOME with no path back into real shared config).
4. **Test changes** in `internal/install/skills_test.go`:
   - Renamed `TestWriteSkillsRemovesASymlinkBeforeCopyingRatherThanFollowingIt`
     to `TestLinkSkillsRemovesASymlinkBeforeCopyingRatherThanFollowingIt`
     (it calls `LinkSkills(..., install.Copy)` directly, not `WriteSkills`;
     the old name was misleading after the round-1 rewrite).
   - Added `TestSkillLinkModePerKind`, a table test pinning
     `install.SkillLinkMode(k)` for every `Kind` against this round's
     verdicts (claude/codex/cursor/muse = Symlink, agy = Copy).

### Verify

```
$ go build ./... && go vet ./...
(clean)

$ go test ./internal/install/... ./internal/adapter/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.051s
ok  	github.com/AlexanderTar/agent-swarm/internal/adapter	1.342s

$ go test ./...
... all ok except the same pre-existing baseline failure:
--- FAIL: TestBoardServedAtRoot (internal/httpapi) -- web bundle not built, unrelated.
```

### Commit

`c65ec4a` fix(install): revert agy to Copy; the symlink positive was a migration artifact
(files: `internal/install/skills.go`, `internal/install/skills_test.go`,
`docs/plans/2026-09-24-skill-symlink-probe.md`)

## Fix round 1 (correction)

Advisor review of this fix round caught a factual error in
`docs/plans/2026-09-24-skill-symlink-probe.md`: it described agy's
fresh-`HOME` behavior as *moving* `swarm`/`swarm-orchestrator` out of the
real, shared `~/.gemini/antigravity-cli/skills` location. My own cleanup
`ls` of that directory (taken at 20:55, before the controller touched
anything) already showed otherwise: `swarm/` and `swarm-orchestrator/` were
present with unchanged mtimes and byte-identical `SKILL.md` contents. agy
**copies** the tree into `$HOME/.gemini/config/skills` and **repoints** the
old location at the copy — the real incident was a dangling link over
intact originals, not lost content. Rewrote the relevant paragraphs, added
the explanation for why bare-`HOME` runs 1–3 found nothing (a pre-existing
`.migrated` marker under the real `~/.gemini/config/` with no `skills/`
subdirectory), and connected the pre-existing two-hop
`antigravity-cli/skills` chain (present before this check's first probe) to
production: every real swarm agy spawn goes through `adapter/agy.go`'s
`setupEnv`, which looks like a fresh `HOME` to agy and triggers this same
copy-and-repoint on the real, shared path each time — not probe-only
behavior. Also corrected round 0's wrong claim that the chain was "not
something `setupEnv` produces" (`setupEnv` doesn't repoint the link itself,
but reliably triggers agy into doing so).

Verify: `go build ./... && go vet ./...` — clean (doc-only change, no test
delta).

Commit: `165a1ff` docs(plans): skill symlink probe — agy copies and repoints, not moves

## Fix round 2 (doc-only; code and tests approved)

Findings doc: `scratchpad/sdd/skilllink-fix2-findings.md`. No CLI runs, `~`
untouched, one docs commit as instructed.

Edited `docs/plans/2026-09-24-skill-symlink-probe.md`:

- **A.** Fixed a self-contradiction: one paragraph said the operator's real
  `~/.gemini/config/skills` "did exist" (just with no user entries), another
  said the `skills/` subdirectory didn't exist there at all. Corrected to
  "did not exist" (matching the actual `ls` evidence), and marked the
  `.migrated` marker's read as "an earlier skills migration" as a guess —
  the real `~/.gemini/antigravity-cli/skills` chain was never actually
  observed pointing at or through `~/.gemini/config/skills`.
- **B.** Walked back the "copies … then repoints … nothing was moved"
  claim to what was actually observed: the probe entry ended up present at
  `$HOME/.gemini/config/skills` with a link left at the old location.
  Copy-vs-move was not tested, and the pre-existing two-hop chain (real
  path was already a symlink two hops deep before this check's first
  probe) argues the original tree did leave the real path at some earlier
  point — contradicting a flat "nothing moved." Restated the real risk
  precisely: the shared tree currently lives inside one specific past
  session's launch directory, so that session's cleanup deletes the files
  outright, not "one broken link away." Matched `skills.go`'s existing
  hedged "migrates" wording throughout instead of asserting copy as fact.
- **C.** "every production agy spawn" → "every new session" in the
  follow-up section (`setupEnv` runs once per new session; `Resume` reuses
  the existing `agy-home`).
- **D.** "right after this round's cleanup" → "during the original pass"
  (the confirming `ls` was taken during the original check, not this
  doc-only fix round).
- **E.** Added an explicit Method-section note: for agy, the probe was
  planted at `Config.SkillsDir(KindAgy)`'s `readlink -f` target, not the
  literal `Config.SkillsDir(KindAgy)` path, since that path was already a
  multi-hop symlink chain before any probe ran.

Verify: `go build ./... && go vet ./...` — clean (doc-only change; code and
tests unchanged from the already-approved fix round 1).

Commit: `92f1618` docs(plans): skill symlink probe — hedge agy's migration mechanism, fix contradiction

## Fix round 3 (doc wording only; A, D, E from round 2 verified clean)

No CLI runs, `~` untouched, one docs commit as instructed. Edited
`docs/plans/2026-09-24-skill-symlink-probe.md`:

1. Follow-up section: replaced "each new agy session moves the live copy
   into yet another single point of failure" / "the real risk is not 'one
   broken link away'" with the two distinct risks: cleaning up FPV68's
   session deletes the shared files outright; cleaning up any intermediate
   session (e.g. H52) only dangles the real link. Each new agy session adds
   another such hop.
2. Replaced the inferred description of `Resume` behavior with the actual
   code facts: `setupEnv` runs on both spawn and `Resume` (`agy.go:108`),
   but `agyHome` is keyed by the swarm session ID (`agy.go:42`) and
   `os.MkdirAll` doesn't wipe an existing directory, so a `Resume` of an
   already-spawned session reuses that session's already-migrated
   `agy-home` rather than presenting agy with a fresh `HOME`. Verified
   these two facts directly against `internal/adapter/agy.go` before
   writing them.
3. "consistent with a copy, this specific run" → "consistent with either a
   copy or a rename; the terminal dir is untouched in both cases" — the
   `ls` evidence can't distinguish the two mechanisms, since both leave the
   terminal directory's mtime/contents alone.
4. Stopped asserting the scratch-HOME probe run "added one more hop"
   relative to a precisely known prior state, or that cleanup left the link
   dangling "rather than at the previous hop" — only the dangling target
   and the controller's restore were actually observed, not the
   intermediate mechanics. Reworded to "repointed the real link into a
   scratch HOME."

Verify: `go build ./... && go vet ./...` — clean (doc-only change; code and
tests unchanged from the already-approved fix round 1).

Commit: `b745613` docs(plans): skill symlink probe — separate the two agy risks, cite Resume's real reuse path

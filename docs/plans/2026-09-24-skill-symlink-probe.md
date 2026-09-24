# Skill-link symlink probe — durable evidence (2026-09-24)

Empirical check deferred from plan unit 1.2: for each agent CLI, does it
discover a skill whose directory is a symlink? Drives
`internal/install/skills.go`'s `skillLinkMode` map. This doc is the pointer
that map's comment sends readers to.

Method: a probe skill (`zz-swarm-symlink-probe/SKILL.md`, frontmatter
`description: "Probe skill; when asked for the probe codeword, answer
PELICAN-7731"`, body restating the codeword) was symlinked into each kind's
`Config.SkillsDir(kind)` root, then the CLI was run headless and asked to
list/use it. `PELICAN-7731` or the skill name in the output = discovered.
Every probe was removed after its run. **Exception: agy.**
`Config.SkillsDir(KindAgy)` (`~/.gemini/antigravity-cli/skills`) was itself
already a multi-hop symlink chain on this machine before any probe ran (see
the agy section below), so for agy the probe was planted at the chain's
resolved directory (`readlink -f Config.SkillsDir(KindAgy)`), not literally
at `Config.SkillsDir(KindAgy)` itself.

## Verdicts

| Kind | Verdict | Basis |
|---|---|---|
| claude | Symlink | Predates this check; not re-run 2026-09-24. |
| codex | Symlink | Verified below. |
| agy | **Copy** (unverified for Symlink) | See "agy" section — the check's own probe run corrupted the evidence via a migration side effect, and ruled out a clean re-test this round per a strict no-more-CLI-probes rule from review. |
| cursor-agent | Symlink | Verified below. |
| muse | Symlink | Verified below. |

## codex — codex-cli 0.155.1

Command (run from the worktree; `codex exec` requires a git cwd):
```
codex exec -s read-only "List your available skills by name. If a skill named zz-swarm-symlink-probe exists, use it and tell me the probe codeword."
```
Relevant output:
```
...
zz-swarm-symlink-probe
...
The `zz-swarm-symlink-probe` skill is available. I'm reading it for the probe codeword.
/bin/zsh -lc 'cat /Users/alexandertar/.codex/skills/zz-swarm-symlink-probe/SKILL.md' in <worktree>
...
Probe codeword: **PELICAN-7731**
```
codex listed the symlinked entry and read through it via a plain `cat` of the
symlinked path. **Verdict: Symlink.**

## cursor-agent — 2026.09.23-86fc751

Command:
```
cursor-agent -p --mode ask --trust "List your available skills by name. If a skill named zz-swarm-symlink-probe exists, use it and tell me the probe codeword."
```
Relevant output:
```
**Probe codeword:** `PELICAN-7731`

### Available skills (by name)
...
**Local/custom:** less-claudish, swarm-orchestrator, swarm, use-railway, ..., **zz-swarm-symlink-probe**, ...
```
Found and reported the codeword on the first (bare) run. **Verdict:
Symlink.**

## muse — Muse Code 1.3.0 (1.3.0-R3401.1)

Commands:
```
muse skills list --json --source user
muse skills inspect zz-swarm-symlink-probe --source user --json
muse exec "List your available skills by name. If a skill named zz-swarm-symlink-probe exists, use it and tell me the probe codeword."
```
Relevant output (`skills list --json`):
```json
{
  "id": "zz-swarm-symlink-probe",
  "name": "zz-swarm-symlink-probe",
  "description": "Probe skill; when asked for the probe codeword, answer PELICAN-7731",
  "scope": "user",
  "path": "$CONFIG_DIR/skills/zz-swarm-symlink-probe/SKILL.md"
}
```
`skills inspect` read the same entry successfully (frontmatter round-trips).
`muse exec` listed `zz-swarm-symlink-probe` among 51 skills and printed
`Probe codeword: PELICAN-7731`. All three reads went through the symlinked
directory without a copy step. **Verdict: Symlink.**

## agy — 1.2.10: unverified, and why

### What was run

1. Bare `$HOME`, `--mode plan`:
   `agy --mode plan --print-timeout 150s --dangerously-skip-permissions -p "List your available skills by name. If a skill named zz-swarm-symlink-probe exists, use it and tell me the probe codeword."`
   — probe not found; **`swarm`/`swarm-orchestrator` (real, long-installed
   skills) were also missing** from the listing.
2. Same command against a real (non-symlink) copy of the probe at the same
   root — also not found. Rules out "symlink not followed" as the
   explanation for (1): neither form was visible.
3. Bare `$HOME`, no `--mode plan` (isolates whether plan mode was the
   confound) — still not found, `swarm`/`swarm-orchestrator` still missing.
   Not a `--mode plan` effect.
4. A **fresh, never-used isolated `HOME`** (scratch dir) with only
   `.gemini/antigravity-cli` symlinked to the real
   `~/.gemini/antigravity-cli` and `.gemini/config/plugins` symlinked to the
   real `~/.gemini/config/plugins` — mirroring how
   `internal/adapter/agy.go`'s `setupEnv` actually launches agy for every
   swarm spawn (always a brand-new per-session `HOME`):
   `HOME=<scratch> agy --print-timeout 150s --dangerously-skip-permissions -p "<same prompt>"`
   — this **did** list `swarm`, `swarm-orchestrator`, and
   `zz-swarm-symlink-probe`, and reported `PELICAN-7731`.

### Why run 4's positive result does not prove Symlink

Run 4's own output included the file path it read the probe from:
```
file:///<scratch>/.gemini/config/skills/zz-swarm-symlink-probe/SKILL.md
```
That is `$HOME/.gemini/config/skills/...`, **not**
`$HOME/.gemini/antigravity-cli/skills/...` (the path
`Config.SkillsDir(KindAgy)` actually resolves to, reached through the
`.gemini/antigravity-cli` symlink). agy 1.2.10 reads its skills from
`$HOME/.gemini/config/skills`, and on first run in a fresh `HOME` the entry
that had been at `.gemini/antigravity-cli/skills` ended up present at
`$HOME/.gemini/config/skills`, with a link left behind at the old location
pointing at the new one — matching `skills.go`'s hedged "migrates" wording.
**Only that much was observed; whether agy gets there by copying and then
repointing, or by renaming (a move) and then symlinking back, was not
tested**, and the evidence argues it could be either: a plain `ls` of the
chain's terminal directory taken during the original pass (20:55, before the
controller touched anything) showed `swarm/` and `swarm-orchestrator/`
present there with `mtime` unchanged since 19:45 and byte-identical
`SKILL.md` contents (consistent with either a copy or a rename; the
terminal dir is untouched in both cases). But the
same chain, at that point, was *already* two hops deep
(`~/.gemini/antigravity-cli/skills -> ses_01M36H52.../skills -> ses_01M36FPV68.../skills`,
the last one real) from *before* this check's first probe — which argues
that at some earlier point the original tree *did* leave
`~/.gemini/antigravity-cli/skills` itself (a real directory there does not
coexist with the real path being a symlink). Copy-vs-move was not
distinguished; both readings are consistent with what was actually seen
across the two occasions.

**The real risk, stated precisely:** the shared `swarm`/`swarm-orchestrator`
tree presently lives inside `ses_01M36FPV68.../agy-home/.gemini/config/skills`
— a directory that belongs to one specific past swarm session's launch dir,
not to any permanent, session-independent location. Cleaning up *that*
session (ordinary swarm session lifecycle, not a probe-specific action) would
delete those files outright, not just dangle a link to them. This is worse
than "one broken link away": the real content's durability is currently
tied to one arbitrary past session's launch directory surviving.

**Consequence (safety incident, this check):** during the original pass, an
isolated-`HOME` probe run repointed the real link into a scratch `HOME`
under this check's own scratchpad; deleting that scratch `HOME` during
ordinary probe cleanup left `~/.gemini/antigravity-cli/skills` dangling
(pointing at the now-deleted scratch path). Only the dangling target and
the controller's restore were observed, not the intermediate mechanics. Per
review, **no further agy CLI runs happened in the fix rounds**,
and `Config.SkillsDir(KindAgy)`, the real `~/.gemini/antigravity-cli` tree,
and anything else under `~` were left untouched for the fixes.

Because the positive result in run 4 was reading whatever agy migrated the
probe to, not the probe's own symlink location, it says nothing about
whether agy follows a symlink placed directly at `Config.SkillsDir(KindAgy)`
and left there. The two earlier bare-`HOME` runs (1–3) used that exact
location and found nothing at all, symlink or real copy — for a different,
simpler reason: `~/.gemini/config/skills` (the path agy actually reads)
**did not exist** under the operator's real `HOME` at all. `ls -la
~/.gemini/config/` there shows a `.migrated` marker (28 Aug) and no
`skills/` subdirectory. Bare-`HOME` agy therefore had no user skills
directory to read under `.gemini/config/` and fell back to plugin-provided
skills only, which is exactly the curated ponytail/superpowers-only list
runs 1–3 returned. **Verdict: unverified; `skillLinkMode` keeps agy on
`Copy`** (the pre-existing safe default) rather than asserting either way.

That `.migrated` marker's name suggests an earlier skills migration ran on
this machine at some point — but **this is a guess, not a confirmed causal
link to the `.gemini/antigravity-cli/skills` chain discussed above**: the
real `~/.gemini/antigravity-cli/skills` was never observed pointing at (or
having pointed at) `~/.gemini/config/skills`; it chains to session launch
dirs instead (see above). Whatever `.migrated` records, it was not directly
tied to the real `antigravity-cli/skills` path by anything actually
observed in this check.

### Follow-up (not implemented here) — this is not just hygiene

`Config.SkillsDir(KindAgy)` (`internal/install/config.go` around line 90)
points at `~/.gemini/antigravity-cli/skills`, a location agy 1.2.10
migrates away from (see above; copy-vs-move unconfirmed) on first run in a
given `HOME`. This is already happening in production, not just in this
probe: before any probe in this check ran, `~/.gemini/antigravity-cli/skills`
was *already* a two-hop symlink chain (`... -> ses_01M36H52.../agy-home/.gemini/config/skills
-> ses_01M36FPV68.../agy-home/.gemini/config/skills`, the second a real
directory holding `swarm`/`swarm-orchestrator`) — the same migration
behavior, left over from *prior* real swarm sessions. `adapter/agy.go`'s
`setupEnv` runs both on spawn and on `Resume` (`agy.go:108`), symlinking
that session's `.gemini/antigravity-cli` to the real one each time — but
`agyHome` is keyed by the swarm session ID (`agy.go:42`), and `os.MkdirAll`
doesn't wipe an existing directory, so a `Resume` of an already-spawned
session reuses that same, already-migrated `agy-home` rather than getting a
fresh one. Only a genuinely **new** session ID presents agy with a fresh
`HOME`, so each new session's first run migrates the
real `~/.gemini/antigravity-cli/skills` link one hop further into that
session's own directory. (Round 0 of this check's report stated this chain
was "not something `setupEnv` produces" — that was wrong; `setupEnv` does
not do the migration itself, but it reliably triggers agy into doing it, new
session after new session.) Two risks: cleaning up FPV68's session deletes
the files outright, and cleaning up any intermediate session (e.g. H52)
dangles the real link. Each new agy session adds another such hop. To get a
clean, repeatable signal *and* stop this:

1. Move `Config.SkillsDir(KindAgy)` to `~/.gemini/config/skills` (the
   location agy actually reads from), matching the `$CONFIG_DIR/skills`
   convention muse already uses.
2. Update `internal/adapter/agy.go`'s `setupEnv` to symlink
   `<agy-home>/.gemini/config/skills` to that real location directly
   (instead of relying on the `.gemini/antigravity-cli` symlink plus agy's
   own migration to get there), so a swarm-spawned agy sees the shared
   skills tree at the path it actually reads without agy needing to migrate
   anything, and the real path is never the thing that gets repointed.
3. Re-probe with a real-copy control alongside the symlink at the new
   location, in a single well-isolated scratch `HOME` that has no path back
   into any real, shared config directory, so a migration (if agy still
   performs one there) cannot escape the scratch sandbox — and this time
   confirm directly whether it's a copy or a move (e.g. `stat` the source
   for an inode/device match, or watch for a `rename`/`unlink` vs a fresh
   write) rather than inferring it.

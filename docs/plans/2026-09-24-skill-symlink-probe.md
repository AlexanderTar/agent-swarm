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
Every probe was removed after its run.

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
`$HOME/.gemini/config/skills`, and on first run in a fresh `HOME` it
**copies** whatever it finds under `.gemini/antigravity-cli/skills` into
`$HOME/.gemini/config/skills`, then **repoints**
`.gemini/antigravity-cli/skills` itself at that copy (a reverse symlink) —
confirmed by a plain `ls` of the chain's terminal directory right after this
round's cleanup (20:55, before the controller touched anything): `swarm/`
and `swarm-orchestrator/` were present, `mtime` unchanged since 19:45, and
`SKILL.md` contents byte-identical to before. Nothing was moved or deleted;
the *link* was repointed, the originals stayed put. Since in run 4
`.gemini/antigravity-cli` was itself a symlink to the **real**
`~/.gemini/antigravity-cli`, that repoint reached all the way to the real,
shared `~/.gemini/antigravity-cli/skills` path — after run 4, that real path
no longer pointed at the terminal directory holding the real
`swarm`/`swarm-orchestrator` dirs; it pointed into the scratch `HOME`
instead.

**Consequence (safety incident):** deleting that scratch `HOME` afterwards
(ordinary probe cleanup) removed the copy agy had repointed the real link
at, leaving `~/.gemini/antigravity-cli/skills` **dangling over otherwise
intact originals** — no content was lost, but a real, shared, in-use path
(this machine runs other live swarm sessions) was left broken by a probe
run. The controller restored the link. Per that review, **no further agy
CLI runs happened in this round**, and `Config.SkillsDir(KindAgy)`, the real
`~/.gemini/antigravity-cli` tree, and anything else under `~` were left
untouched for the fix.

Because the positive result in run 4 was reading agy's own copy-then-repoint
target, it says nothing about whether agy follows a symlink placed directly
at `Config.SkillsDir(KindAgy)` and left there. The two earlier bare-`HOME`
runs (1–3) used that exact location without triggering a fresh-`HOME`
copy/repoint and found nothing at all, symlink or real copy — because on
this operator's real `HOME`, the migration had already run once before (see
below), so `~/.gemini/config/skills` did exist but had no user-installed
entries in it, and agy never looked at `.gemini/antigravity-cli/skills`
again. **Verdict: unverified; `skillLinkMode` keeps agy on `Copy`** (the
pre-existing safe default) rather than asserting either way.

Runs 1–3's "nothing at all" is explained the same way: `ls -la
~/.gemini/config/` shows a `.migrated` marker (28 Aug) and no `skills/`
subdirectory at all under the real `$HOME` — the one-time copy/repoint had
already happened on this machine, long before this check, against whatever
`~/.gemini/antigravity-cli/skills` held at the time. Bare-`HOME` agy has no
user skills directory under `.gemini/config/` to read at all; it falls back
to plugin-provided skills only, which is exactly the curated
ponytail/superpowers-only list runs 1–3 returned.

### Follow-up (not implemented here) — this is not just hygiene

`Config.SkillsDir(KindAgy)` (`internal/install/config.go` around line 90)
points at `~/.gemini/antigravity-cli/skills`, a location agy 1.2.10
copies away from and repoints on first run in a given `HOME`. This is
already happening in production, not just in this probe: before any probe
in this check ran, `~/.gemini/antigravity-cli/skills` was *already* a
two-hop symlink chain (`... -> ses_01M36H52.../agy-home/.gemini/config/skills
-> ses_01M36FPV68.../agy-home/.gemini/config/skills`, the second a real
directory holding `swarm`/`swarm-orchestrator`). That is exactly this same
copy-and-repoint behavior, left over from *prior* real swarm spawns — every
production agy spawn goes through `adapter/agy.go`'s `setupEnv`, which
symlinks a brand-new session's `.gemini/antigravity-cli` to the real one, so
every spawn is a "fresh `HOME`" from agy's point of view and each one
copies-and-repoints the real `~/.gemini/antigravity-cli/skills` link one hop
further into that session's own directory. (Round 0 of this check's report
stated this chain was "not something `setupEnv` produces" — that was wrong;
`setupEnv` does not do the repointing itself, but it reliably triggers agy
into doing it, spawn after spawn.) Each hop lands inside a session directory
that is later cleaned up, so the chain is one broken link away from going
dangling on any ordinary swarm session cleanup — this already happened once
by accident during this check and will keep happening on its own. To get a
clean, repeatable signal *and* stop this:

1. Move `Config.SkillsDir(KindAgy)` to `~/.gemini/config/skills` (the
   location agy actually reads from), matching the `$CONFIG_DIR/skills`
   convention muse already uses.
2. Update `internal/adapter/agy.go`'s `setupEnv` to symlink
   `<agy-home>/.gemini/config/skills` to that real location directly
   (instead of relying on the `.gemini/antigravity-cli` symlink plus agy's
   own copy-and-repoint to get there), so a swarm-spawned agy sees the
   shared skills tree at the path it actually reads without agy needing to
   copy or repoint anything, and the real path is never the thing that gets
   repointed.
3. Re-probe with a real-copy control alongside the symlink at the new
   location, in a single well-isolated scratch `HOME` that has no path back
   into any real, shared config directory, so a copy/repoint (if agy still
   performs one there) cannot escape the scratch sandbox.

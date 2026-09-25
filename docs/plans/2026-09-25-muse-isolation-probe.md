# Muse spawn isolation — probe evidence (PM.1)

Companion to spec `A8. muse spawn isolation` and plan package `### PM: muse spawn isolation`
in `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md` /
`docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md`.

## Phase 1 findings summary (full detail: original PM Phase 1 report, superseded here for
the record — key numbers repeated so this doc stands alone)

- `internal/adapter/muse.go`'s `setupEnv` isolates only `XDG_CONFIG_HOME`; it never sets
  `HOME`. A spawned muse therefore inherits the operator's real `$HOME` and independently
  scans `$HOME/.claude/skills`, `$HOME/.codex/skills`, `$HOME/.agents/skills` (13 foreign
  "user"-scope skills in this operator's real environment, only 2 of which — `swarm`,
  `swarm-orchestrator` — are swarm's own) and loads `~/.claude/CLAUDE.md` ("Including your
  Claude Code personal rules and 1 skill — manage with /settings.").
- Isolating `XDG_CONFIG_HOME` alone (without isolating `HOME`) removes **zero** of the 13
  foreign skills — proven directly (real `$HOME`, isolated swarm-only `XDG_CONFIG_HOME`
  still returns all 13).
- `settings.json` is cloned from the real file unmutated; every operator MCP server
  (context7, neon, notion, railway, revenuecat, vercel), including live bearer tokens,
  reaches every spawned muse.
- `settings.json`'s `context.foreign_personal_skills`/`context.foreign_personal_rules`
  (booleans) suppress the `.claude`/`.codex` leak and the CLAUDE.md notice, but **not**
  `$HOME/.agents/skills` — muse treats `.agents` as one of its own native personal-skill
  roots, not "foreign". Closing that gap requires `HOME` isolation, not just the settings
  flag.
- muse's plugin store (`XDG_DATA_HOME/muse/plugins`) enforces an integrity/ownership check
  that rejects every partial reconstruction tried (symlinked root: "must be a regular
  non-symlink directory"; symlinked `installed.json`: "installed pointer must be a regular
  non-symlink file"; copied `installed.json` + symlinked `cache`/`marketplaces` + copied
  `.installed.lock`: `plugin_package_retention_refused`, "could not acquire verified
  lifetime ownership"). The only proven-working option is pointing `XDG_DATA_HOME` at the
  real, untouched `~/.local/share` directly.
- Combined design (isolated `HOME` denylisting other agents' personal roots + isolated
  `XDG_CONFIG_HOME` with swarm-only skills/MCP + `XDG_DATA_HOME`/state/cache pinned to the
  real paths) was validated end-to-end: `skills list --json` returns exactly `swarm`,
  `swarm-orchestrator` at user scope, superpowers' 13 skills + `elements-of-style` +
  `threejs` at plugin scope (all real, untouched), zero foreign-tool skills.

## PM.1 — live TUI probe (this unit)

**Question:** `Launch`/`Resume` invoke muse's interactive TUI (not `exec`), which
Phase 1 could not probe headlessly. Does a fresh isolated `HOME` + `XDG_CONFIG_HOME`
(cloned-then-mutated settings.json, swarm-only skills) + real-pinned `XDG_DATA_HOME` show
any blocking first-run or foreign-context dialog on first launch, the way `codexTrust`/
`agyTrust` block codex/agy on an untrusted directory? `muse.go`'s `StartupDialogs()`
currently returns `nil` with `// TODO(probe): confirm no first-run onboarding wizard
blocks a fresh HOME` — this unit resolves that TODO for the proposed env shape.

### Setup

Scratch dir: `/private/tmp/claude-501/.../scratchpad/museprobe/tui1/`

- `tui1/home/`: fresh dir. Every real `~/*` and `~/.* ` entry symlinked in **except**
  `.claude`, `.codex`, `.cursor`, `.agents`, `.gemini`, `.config`, `.muse` (the denylist —
  design bullet 1).
- `tui1/xdg-config/muse/`:
  - `auth.json` copied from the real file.
  - `settings.json`: the real file, loaded and mutated in Python to match the proposed
    `setupEnv` output — `mcpServers` replaced with a single `swarm` entry (fake
    command/env, since no real daemon/session backs this probe), `context.foreign_personal_skills`
    and `context.foreign_personal_rules` set to `false`. Every other real key
    (`runtime_capabilities["plugin:superpowers:hook:session-start"].trusted_definition_hash`,
    `tui.foreign_context_notice_shown: true`, `model`, `reasoning_effort`) preserved.
  - `skills/swarm`, `skills/swarm-orchestrator`: symlinked from the real
    `~/.config/muse/skills/`.
- `XDG_DATA_HOME=$HOME/.local/share`, `XDG_STATE_HOME=$HOME/.local/state`,
  `XDG_CACHE_HOME=$HOME/.cache` — pinned to the **real** paths (design bullet 4/6), passed
  explicitly rather than left to fall through the now-isolated `HOME`.

### Command

```
tmux new-session -d -s museprobe1 -x 220 -y 50 -c "<scratch>/tui1/workspace" \
  "env HOME='<scratch>/tui1/home' \
       XDG_CONFIG_HOME='<scratch>/tui1/xdg-config' \
       XDG_DATA_HOME='$HOME/.local/share' \
       XDG_STATE_HOME='$HOME/.local/state' \
       XDG_CACHE_HOME='$HOME/.cache' \
       PATH='$PATH' \
   muse --model muse-spark-1.3-contributor --reasoning-effort high --yolo --trust-workspace \
        'reply with exactly: probe ok'"
```
This mirrors `Muse.argv` exactly (`{"muse", "--model", s.Model, "--reasoning-effort",
museEffort(s.Effort), "--yolo", "--trust-workspace", s.Kickoff}`), launched with `-c` set
to a scratch workspace dir (the adapter sets the pane's cwd, not a `--workspace` flag).

### Pane captures (`tmux capture-pane -p`)

**5s:**
```
  Muse Code 1.4.0

  Model set to muse-spark-1.3-contributor
  ⎿  Your content, including inter-session messages, may be used for product improvement.

  1 optional MCP server unavailable (ctrl+o to expand)

❯ reply with exactly: probe ok

◆ probe ok

◇ Double checking (6s · esc to interrupt)
...
```

**15s:** turn already complete, back at the idle `❯` prompt (model answered "probe ok" and
finished before 15s).

**30s:** byte-identical to the 15s capture (`diff` empty) — pane stable at idle, nothing
further happened.

### Verdict

No blocking dialog at any capture point. No trust prompt, no onboarding wizard, no
foreign-context notice requiring a keypress. "1 optional MCP server unavailable" is
expected and non-blocking (the probe's fake swarm command doesn't exist; `mode: "optional"`
is exactly what prevents this from blocking, matching `WriteMuse`'s existing choice). The
model responded to the prompt and the pane returned to idle on its own. `StartupDialogs()`
needs **no new entry** for this env shape — cloning-then-mutating `settings.json` (keeping
`tui.foreign_context_notice_shown: true`) is sufficient; the TODO in `muse.go` is resolved
for the isolation change (still open for any *other*, unrelated first-run surface muse
might add later — not this unit's concern).

### Safety

Snapshot of `~/.config/muse`, `~/.config/muse/skills`, `~/.local/share/muse` taken before
and after this probe. `~/.config/muse/{settings.json,auth.json,skills/}` byte-identical
(unchanged mtimes) — the probe only ever read from or symlinked out of them. Because this
was a real interactive session (unlike Phase 1's read-only `skills list` probes),
`~/.local/share/muse/session-index.db` and `tui-history.jsonl` grew (this probe's own
session got logged there, since `XDG_DATA_HOME` was pinned to the real path by design) —
this is the "session/log files excepted" carve-out in the PM brief's snapshot rule, not a
violation: it's the real, intended consequence of Q2 (shared real data dir). No other file
under either tree changed; `plugins/installed.json` diffed identical.

**Fix round 1 correction:** the probe's session actually registered as **two** entries in
the real session store, not one — `01a0d78b-9da4-7bf3-93e7-fc959c5bca99` (session.jsonl
mtime 08:50:56) and `01a0d78b-b80a-78d2-a9e1-464013cc0eac` (mtime 08:51:03), 7 seconds
apart. Cause: the probe ended with `tmux kill-session -t museprobe1`, which SIGKILLs the
pane's process group without giving muse a chance to exit cleanly; muse appears to
register a fresh session entry on that kind of abrupt disconnect rather than reusing the
one already open. Both are ordinary muse session directories under
`~/.local/share/muse/sessions/2026/09/25/` (transcripts, `session.peer-history.sqlite3`,
etc. — no isolation-relevant content, this probe's own single "reply with exactly: probe
ok" exchange) and the controller has decided to leave them for the user to clean up or
keep, rather than have this probe write to the real home a second time to remove them.
**Future probes should end the muse TUI with its own `/exit` command (typed into the
pane, then a short wait for the prompt to return) instead of killing the tmux session
out from under it**, so exactly one session entry is registered per probe run.

# Package PM: muse spawn isolation — Phase 1 probe report

Worktree: /Users/alexandertar/GitHub/agent-swarm--pm (no code changes made; Phase 1 is probes only)
muse binary: `Muse Code 1.4.0 (1.4.0-R4161.1)` at `/Users/alexandertar/.local/bin/muse` (launcher script `exec`s `/Users/alexandertar/.local/bin/muse-bin-1.4.0-R4161.1`)
Scratch dir used: `/private/tmp/claude-501/.../scratchpad/museprobe/{sealed1,sealed2,sealed3}`

## Safety
Pre/post snapshot of `~/.config/muse`, `~/.config/muse/skills`, `~/.local/share/muse` taken before and after **all** probes, including `sealed5` (which pointed `XDG_DATA_HOME` at the real `~/.local/share` for a read-only `skills list`) — **re-verified after every probe in this report, not just the first batch**: `settings.json` (1506 bytes, unchanged mtime), `auth.json` (302 bytes, unchanged mtime), `skills/` (unchanged mtimes on `swarm`/`swarm-orchestrator`), `session-index.db` (229376 bytes) and `tui-history.jsonl` (34747 bytes) all byte-identical to the pre-probe baseline — no drift at all, not even mtime, because every real-`XDG_DATA_HOME` probe used `skills list` (read-only), never `exec`/interactive (which would create a session). `plugins/installed.json` diffed explicitly against the untouched real file — identical. All probes ran under `env -i HOME=<scratch> XDG_CONFIG_HOME=<scratch> XDG_DATA_HOME=<scratch> ...`, i.e. real dirs were only ever read from or symlinked/copied *out of*, never written into. No probe used `--yolo`/interactive `exec` against the real `meta` provider for a real conversation turn (avoided API spend and avoided writing real session state); one `--provider echo --json` exec ran fully sealed (no real dirs referenced) to inspect the personal-rules-notice line, at zero API cost. `muse skills list --json` was otherwise sufficient because skill/plugin/MCP-config resolution happens during context assembly regardless of provider.

## Finding 1 — current code leaks all operator MCP credentials into every spawned muse

Real `~/.config/muse/settings.json` (`cat ~/.config/muse/settings.json`):
```json
{
  "mcpServers": {
    "context7": {"headers": {"Authorization": "Bearer ctx7sk-..."}, "mode": "optional", "url": "https://mcp.context7.com/mcp"},
    "neon":     {"headers": {"Authorization": "Bearer napi_..."}, "mode": "optional", "url": "https://mcp.neon.tech/mcp"},
    "notion":   {"mode": "optional", "url": "https://mcp.notion.com/mcp"},
    "railway":  {"args": ["mcp"], "command": "railway", "mode": "optional"},
    "revenuecat": {"mode": "optional", "url": "https://mcp.revenuecat.ai/mcp"},
    "swarm":    {"args": ["mcp"], "command": ".../bin/swarm", "mode": "optional"},
    "vercel":   {"headers": {"Authorization": "Bearer vcp_..."}, "mode": "optional", "url": "https://mcp.vercel.com"}
  },
  "model": "muse-spark-1.3-contributor", "provider": "meta", "reasoning_effort": "xhigh", "schema_version": 1
}
```
`internal/adapter/muse.go` `setupEnv` (lines ~104-114) does:
```go
realSettings := filepath.Join(m.d.UserHome, ".config", "muse", "settings.json")
if raw, err := os.ReadFile(realSettings); err == nil { json.Unmarshal(raw, &settings) }
...
servers["swarm"] = map[string]any{...}  // only swarm is added, nothing else removed
```
i.e. it clones the whole file, live bearer tokens included, and only *adds* the swarm entry. Every swarm-spawned muse today gets working, credentialed access to context7, neon, notion, railway and vercel — tools the operator configured for their own interactive use, not for the agent.

Compare `internal/adapter/codex.go` `setupEnv`: it never reads the user's `~/.codex/config.toml`, so the user's other MCP servers are **not** carried into a spawned codex. Muse should match this.

## Finding 2 — REVISED: the 13 "user"-scope skills are not from `~/.config/muse/skills` at all — muse reads `$HOME/.claude/skills`, `$HOME/.codex/skills`, `$HOME/.agents/skills` directly, and today's code never isolates `HOME`

Original hypothesis (whole-dir symlink of `~/.config/muse/skills`) was wrong — checked and ruled out:
```
$ ls -la ~/.config/muse/skills
swarm/  swarm-orchestrator/   # only these two, nothing else
```
Yet `muse skills list --source all --json` (real env) shows 13 "user"-scope skills. Their `path` field gives the real source:
```
clickhouse-io               | $HOME/.claude/skills/clickhouse-io/SKILL.md
code-review-skill           | $HOME/.agents/skills/code-review-skill/SKILL.md
less-claudish                | $HOME/.claude/skills/less-claudish/SKILL.md
react-native-best-practices  | $HOME/.agents/skills/react-native-best-practices/SKILL.md
... (react-navigation, typesafe-ai, use-railway, validate-skills, vercel-* — all $HOME/.agents/skills)
swarm, swarm-orchestrator    | $CONFIG_DIR/skills/...             (the only two actually from muse's own dir)
```
muse independently scans `$HOME/.claude/skills`, `$HOME/.codex/skills` and `$HOME/.agents/skills` — other coding agents' personal skill roots — and pulls them in as "foreign personal skills" (confirmed by decoy-file probes below). **`internal/adapter/muse.go`'s `setupEnv` never sets `HOME` in the returned env map at all** — it only returns `{"XDG_CONFIG_HOME": ...}` — so every spawned muse inherits the operator's real `$HOME` and therefore all of this, regardless of the `XDG_CONFIG_HOME` isolation. Proven directly: re-running `skills list` with the *real* `$HOME` but the fully-isolated, swarm-only `XDG_CONFIG_HOME` from Finding 4 still returns all 13 "user" skills unchanged — isolating `XDG_CONFIG_HOME` alone (my original bullet-3-only fix) removes **zero** of them. The whole-`skills/`-dir symlink in `setupEnv` is real but is not where this leak comes from; it's still worth replacing with `install.LinkSkills`/`install.SkillsHome` as hygiene (a user could someday have real personal skills directly under `~/.config/muse/skills`), it just isn't the fix for the 13-skill leak.

Decoy-file probe (sealed `HOME=<scratch>/home`, isolated `XDG_CONFIG_HOME` with only `swarm`/`swarm-orchestrator` symlinked in): planting `$HOME/.claude/skills/probe-skill/SKILL.md`, `$HOME/.agents/skills/probe-agents-skill/SKILL.md`, `$HOME/.codex/skills/probe-codex-skill/SKILL.md` all three show up in `skills list --json` with `scope: "user"` and the corresponding `$HOME/...` path — confirming the mechanism is generic (any of `.claude`, `.codex`, `.agents` under `$HOME`), not specific to one tool.

## Finding 2b — CLAUDE.md ("personal rules") also leaks via real `$HOME`, and a settings.json toggle exists that suppresses the `.claude`/`.codex` leak (but not `.agents`)

`muse exec --provider echo --json` (no network cost — echo provider still assembles real context) against the sealed `HOME` with a decoy `$HOME/.claude/CLAUDE.md` and `$HOME/.claude/skills/probe-skill`:
```
muse: Including your Claude Code personal rules and 1 skill — manage with /settings.
```
i.e. muse actually loads and includes the operator's `~/.claude/CLAUDE.md` content into the session by default — this is very likely the "full context" half of the user's complaint, not just skills.

The binary's settings.json schema has a `context` object with two booleans, confirmed live by trial (a string value is rejected: `invalid type: string "off", expected a boolean`):
```json
{ "context": { "foreign_personal_skills": false, "foreign_personal_rules": false } }
```
With this set, re-running the same exec: the "Including your Claude Code personal rules..." stderr line **disappears entirely**, and `skills list --json` no longer shows `probe-skill` ($HOME/.claude) or `probe-codex-skill` ($HOME/.codex). **However `probe-agents-skill` ($HOME/.agents/skills) is unaffected by this flag** — it still appears with the flag set to `false`. Conclusion: muse treats `.claude`/`.codex` (other named tools) as "foreign" personal context governed by this toggle, but treats `$HOME/.agents/skills` as one of *its own* native personal-skill roots (alongside `$CONFIG_DIR/muse/skills`), not as "foreign" — so the settings flag alone cannot close that gap; only not letting `$HOME/.agents` resolve to the real directory (i.e. isolating `HOME`) does. This also means `$HOME/.agents/AGENTS.md` (this user's own global rules file — `~/.claude/CLAUDE.md` names it as the canonical cross-tool standards doc, per the user's real `CLAUDE.md`) was not tested directly, but given `.agents/skills` demonstrably survives the settings flag, `.agents`'s rules almost certainly would too. **`HOME` isolation with `.agents` excluded is therefore required to close this leak, not optional defense-in-depth on top of the settings flag** — the settings flag alone provably leaves at least one personal-skills root (and likely a rules file) open.

`--no-foreign-personal-context` (the CLI flag mentioned in the brief) exists only on `muse exec --help`, not on the interactive TUI (`muse --help`) that `Launch`/`Resume` actually invoke, and not on `skills list`. The settings.json `context.*` keys are therefore the only mechanism that reaches Launch and Resume both (a CLI flag would need argv changes wired into `argv()`/`Resume()` too, for no additional effect once the settings key is set).

## Finding 3 — a fully sealed HOME/XDG_* still boots muse fine (isolation is honoured)

Sealed env (`env -i HOME=<scratch>/home XDG_CONFIG_HOME=<scratch>/xdg-config XDG_DATA_HOME=<scratch>/xdg-data XDG_STATE_HOME=<scratch>/xdg-state XDG_CACHE_HOME=<scratch>/xdg-cache`), with only a copied `auth.json` and a hand-written minimal `settings.json` (`schema_version:1`, empty `mcpServers`) under `xdg-config/muse/`:
```
muse skills list --source all --workspace <scratch>/workspace --trust-workspace --json
→ total: 20   Counter({'bundled': 19, 'plugin': 1})   plugin: ['plugin:threejs:threejs']
```
No crash, no onboarding wizard, no complaint about missing real dirs. `threejs` is a *built-in* plugin baked into the binary (`plugins/cache/builtin/threejs`), unrelated to the user's installed plugins — it shows up with zero plugin store present at all. This confirms `XDG_CONFIG_HOME` and `XDG_DATA_HOME` are both fully honoured and a from-scratch config tree is a supported, working state (strings in the binary confirm no `MUSE_HOME`/`MUSE_CONFIG` override exists; only the XDG vars and `HOME`).

## Finding 4 — swarm-managed-skills-only isolation works cleanly

Same sealed setup, but symlinking in **only** the two swarm-managed skill directories (`~/.config/muse/skills/swarm`, `.../skills/swarm-orchestrator` — exactly what `install.WriteSkills`/`install.SkillsHome` manage today for other kinds):
```
muse skills list --source all --json
→ total: 22   Counter({'bundled': 19, 'user': 2, 'plugin': 1})
   user: ['swarm', 'swarm-orchestrator']
```
Exactly the two swarm skills, nothing else personal. `install.LinkSkills(root, install.SkillsHome(swarmHome), install.SkillLinkMode(KindMuse))` — the same call `claude.go`'s `writeProjectSwarmConfig` already makes — is a drop-in replacement for the current whole-dir symlink.

## Finding 5 — the plugin store (superpowers) enforces an integrity/ownership check that defeats partial isolation

Real `~/.local/share/muse/plugins/installed.json` lists two user-installed plugins: `elements-of-style` (marketplace-cached) and `superpowers` (`"provenance": "native-local"`, `"source.path": "/Users/alexandertar/GitHub/superpowers"` — a live dev checkout, not copied into the data dir). `threejs` above is separately baked into the binary and does NOT depend on this store.

Attempts to reconstruct a working plugin store under an isolated `XDG_DATA_HOME`:
- Symlinking the whole `plugins/` directory itself: **rejected** — `"plugin store root must be a regular non-symlink directory"`.
- Real `plugins/` dir + symlinked `installed.json`: **rejected** — `"installed pointer must be a regular non-symlink file"`.
- Real `plugins/` dir + **copied** `installed.json` + symlinked `marketplaces/`, `cache/`: loads with zero errors but **zero plugin skills** (not even `threejs`).
- Same + copied `.installed.lock` too: still refused —
  ```
  muse skills list --source plugin ...
  → No skills found.
    Diagnostics:
    - plugin_package_retention_refused: the selected package could not acquire verified
      lifetime ownership; affected installed paths are withheld (plugin://elements-of-style)
    - plugin_package_retention_refused: ... (plugin://superpowers)
  ```
This is a real internal integrity mechanism (store identity/lock/ownership), not a missing-file problem — reconstructing it piecemeal is not supported and is fragile against muse version changes. **Today's adapter code never isolates `XDG_DATA_HOME` at all** (`setupEnv` returns only `{"XDG_CONFIG_HOME": ...}`), and `Muse.DiscoverSession` already reads the *real* `~/.local/share/muse/runtime/muse/sessions` directly via `m.d.UserHome` (not XDG_DATA_HOME-relative) to recover the provider session id after launch — so leaving `XDG_DATA_HOME` unset/real is both the only proven-working option and already what resume/session-discovery depends on.

`XDG_DATA_HOME` contents found: `plugins/` (cache + marketplaces, no secrets), `sessions/` + `session-index.db` + `tui-history.jsonl` (session/transcript logs), `model-catalog/`, `feature-config/`, `runtime/` (0700, session registry `DiscoverSession` reads), `local-tracing/` (0700). No credentials live here — those are entirely in `~/.config/muse/{auth.json,settings.json}`.

## Finding 6 — REVISED: the global "personal rules" file is real and is `~/.claude/CLAUDE.md` (Finding 2b), not something under `~/.config/muse`

`~/.config/muse` itself contains only `auth.json`, `settings.json`, `skills/`, `trust.json` (workspace-trust store) — no muse-specific `RULES.md`/`MUSE.md`/global-memory file, correct as far as it goes. But muse's "personal rules" concept (the same `context.foreign_personal_rules` toggle from Finding 2b) reads **other tools'** global rules files off the real `$HOME` — confirmed live: `~/.claude/CLAUDE.md` gets included ("Including your Claude Code personal rules..."). This is the literal "context" the user's bug report named. `--trust-workspace` loading `<cwd>/AGENTS.md` (cursor's pattern, already used) is unaffected and stays muse's per-run *workspace* instructions surface — that part was already correct and needs no change.

## Finding 7 — combined design validated end-to-end in one probe

`sealed5`: isolated empty `HOME`; isolated `XDG_CONFIG_HOME` with only `swarm`/`swarm-orchestrator` symlinked into `skills/`, and a `settings.json` built by **cloning the real one and mutating it in place** (`mcpServers` replaced with swarm-only, `context.foreign_personal_skills`/`foreign_personal_rules` set to `false`, every other real key — `runtime_capabilities`, `tui.foreign_context_notice_shown`, `model`, `reasoning_effort` — left untouched); `XDG_DATA_HOME` **explicitly set to the real `~/.local/share`** (not left to fall through the now-isolated `HOME`, which defaults to `$HOME/.local/share` when `XDG_DATA_HOME` is unset — confirmed in the binary's own embedded docs strings). Result:
```
muse skills list --source all --json
→ total: 39   Counter({'bundled': 20, 'plugin': 17, 'user': 2})
   user:   ['swarm', 'swarm-orchestrator']
   plugin: superpowers (13 skills) + elements-of-style + threejs — all real, untouched
```
Exactly the target shape: only swarm's 2 skills at user scope, zero foreign-tool skills, superpowers (and every other real plugin) fully intact via the real, untouched plugin store. This is the first probe that gets the whole picture right at once, so it is the reference config for Phase 2's `setupEnv`.

**Important interaction to keep**: `XDG_DATA_HOME` must be set explicitly to the real `~/.local/share` path once `HOME` is isolated. Today's code isolates only `XDG_CONFIG_HOME` and leaves `HOME` untouched, so `XDG_DATA_HOME`'s absence is currently harmless (it falls through the real `$HOME`). The moment `HOME` is isolated for Finding 2/2b, an unset `XDG_DATA_HOME` would silently move muse's plugin/session data under the isolated `HOME` instead — reintroducing Finding 5's "plugin store not found" failure and breaking `DiscoverSession`/resume. `XDG_STATE_HOME`/`XDG_CACHE_HOME` were not separately probed for content but should get the same explicit real-path treatment defensively, for the same fallback-through-`HOME` reason.

## Findings table: today vs. proposed

| Surface | Today (real code) | Proposed |
|---|---|---|
| `HOME` | **not set — real, unisolated.** This is the actual root cause: `$HOME/.claude/skills`, `$HOME/.codex/skills`, `$HOME/.agents/skills`, and `$HOME/.claude/CLAUDE.md` all leak in regardless of `XDG_CONFIG_HOME` (Finding 2, 2b — proven: isolating `XDG_CONFIG_HOME` alone removes 0 of 13 foreign skills) | isolate per-launch (mirrors `agy.go`'s pattern), symlinking back only what a shell tool needs (`.gitconfig`, `.ssh`, etc. — Phase 2 detail) and explicitly never symlinking other agents' personal roots (`.claude`, `.codex`, `.cursor`, `.agents`, `.gemini`) back in |
| `XDG_CONFIG_HOME` | isolated per-launch dir (kept) | isolated per-launch dir (kept) |
| `settings.json` | **full clone of real file, only swarm added** — all operator MCP servers + live credentials (context7/neon/notion/railway/revenuecat/vercel) carried through | clone real file (keeps `runtime_capabilities` trust hash, `tui` flags), then mutate: `mcpServers` → swarm-only, add `context.foreign_personal_skills: false`, `context.foreign_personal_rules: false` |
| `~/.config` siblings (git, gh, ...) | symlinked through (kept) | unchanged, keep |
| `auth.json` | symlinked (kept) | unchanged, keep (read-only from muse's view) |
| `~/.config/muse/skills/` | whole real dir symlinked (in this install it only has `swarm`+`swarm-orchestrator`, so no observed leak here — Finding 2's original hypothesis was wrong) | swap to `install.LinkSkills`+`install.SkillsHome` as hygiene, matching claude.go, in case a user ever has real personal skills directly under this dir |
| `XDG_DATA_HOME` (plugins, sessions, model-catalog) | not set — falls through the real, unisolated `HOME` | **explicitly set to the real `~/.local/share`** once `HOME` is isolated (Finding 7) — plugin-store integrity check (Finding 5) rules out reconstructing it any other way |
| superpowers / other plugins | available (real data dir) | still available, unchanged — real plugin store referenced directly, satisfies "superpowers stays a plugin install"; note other real plugins (e.g. `elements-of-style`) stay visible too since the whole store is real, not filtered |
| Instructions (AGENTS.md) | written to `s.Cwd/AGENTS.md`, loaded via `--trust-workspace` | unchanged, keep |
| Resume / `DiscoverSession` | reads real `~/.local/share/muse/runtime/muse/sessions` via `m.d.UserHome` | unchanged, keep — works as long as `XDG_DATA_HOME` is pinned to the real path (see above), not left to an isolated `HOME`'s fallback |

## Proposed design (Phase 2 scope)

1. **Isolate `HOME` per-launch, using a denylist, not an allowlist**: a fresh per-launch directory, symlinking in every real `~` entry *except* `.claude`, `.codex`, `.cursor`, `.agents`, `.gemini` (other coding agents' personal roots — the actual leak) and `.config` (already handled separately by the existing `XDG_CONFIG_HOME` isolation/sibling-symlink loop, unchanged). This mirrors the pattern `setupEnv` already uses one level down for `~/.config`'s siblings ("symlink everything except `muse/`") and avoids guessing at an allowlist of exactly which dotfiles (`.gitconfig`, `.ssh`, `.npmrc`, `.cargo`, `go`, `.nvm`, `.pyenv`, ...) a shell-tool call might need — swarm muse agents commit and push per unit, so an unprobed allowlist is a strong risk of production breakage. (Note: `agy.go` does return an isolated `HOME` in its env map, but its own `setupEnv` only symlinks back `.gemini`-specific paths — it is not itself a working precedent for a broad denylist; this is a new pattern for the codebase, borrowed from `muse.go`'s own `~/.config`-siblings loop shape.)
2. Keep `setupEnv`'s per-launch `XDG_CONFIG_HOME` isolation and the "symlink every `~/.config` sibling except `muse/`" loop exactly as today — it already reads from `m.d.UserHome/.config` (the daemon's own stored real path), not from the muse subprocess's `HOME` env var, so isolating `HOME` (step 1) does not move or affect it. No change needed here beyond step 3 below.
3. Stop cloning `settings.json` unmutated. Keep cloning it (preserves `runtime_capabilities["plugin:superpowers:hook:session-start"].trusted_definition_hash` and `tui.foreign_context_notice_shown` — dropping either was flagged as a risk and not needed once we mutate instead of rebuild), then: replace `mcpServers` with swarm-only (unchanged from today's intent, codex precedent), and add `context.foreign_personal_skills: false` + `context.foreign_personal_rules: false` (Finding 2b/7 — required, not optional: this is what actually suppresses the `.claude`/`.codex` skill+rules leak; `HOME` isolation alone handles `.agents` and is belt-and-braces for the rest).
4. Replace the whole-dir `symlinkIfExists(realSkillsDir, museDir/skills)` with `install.LinkSkills(filepath.Join(museDir, "skills"), skillsHome, install.SkillLinkMode(install.KindMuse))` where `skillsHome, _ := install.SkillsHome(m.d.Home)` — same call `claude.go`'s `writeProjectSwarmConfig` already makes; `internal/install/skills.go` already declares `KindMuse: Symlink`. Hygiene only (Finding 2) — the real leak is fixed by steps 1 and 3.
5. Keep `auth.json` symlinked exactly as today.
6. **Explicitly set `XDG_DATA_HOME` to the real `~/.local/share`** (`filepath.Join(m.d.UserHome, ".local", "share")`) in the returned env map once `HOME` is isolated (step 1) — required so muse's plugin store and session registry keep resolving to the real, untouched directory (Finding 5, 7) instead of falling through the new isolated `HOME`. Set `XDG_STATE_HOME`/`XDG_CACHE_HOME` to their real paths too, defensively, for the same reason (contents not separately probed).
7. No change to instructions injection (`AGENTS.md` + `--trust-workspace`) or to `Resume`/`DiscoverSession` logic itself — both stay correct once step 6 is in place.
8. Net result: MCP credentials (context7/neon/notion/railway/revenuecat/vercel), the operator's 11 non-swarm personal skills, and `~/.claude/CLAUDE.md`'s content stop reaching spawned muse agents; swarm's own 2 skills, auth, superpowers (and other real plugins), and resume all keep working — validated together in Finding 7.
9. This ends up closer to `agy.go` (full `HOME` isolation with a symlink-back allowlist) than to `codex.go`, because unlike codex the leak here is `HOME`-rooted, not just `XDG_CONFIG_HOME`-rooted — but the data dir (`XDG_DATA_HOME`) still gets pinned to the real path rather than isolated, because of Finding 5's plugin-store integrity constraint (a difference from agy.go, which has no equivalent plugin-store lock to worry about).
10. **Unresolved from Phase 1, needs a Phase-2 probe before/while implementing**: `muse.go`'s `StartupDialogs()` currently returns `nil` with `// TODO(probe): confirm no first-run onboarding wizard blocks a fresh HOME`. Finding 2b's stderr notice ("Including your Claude Code personal rules...") is non-blocking under `exec`, but `Launch`/`Resume` invoke the **interactive TUI**, not `exec` — Phase 1 did not confirm whether the TUI shows a *blocking* first-run/foreign-context dialog requiring a keypress the first time `tui.foreign_context_notice_shown` is `false`/absent for a given isolated `HOME`+`XDG_CONFIG_HOME` pair. Cloning the real settings.json (step 3) carries `tui.foreign_context_notice_shown: true` through, which should prevent this, but it needs a live TUI probe (tmux pane, not `exec --json`) to confirm before Phase 2 implementation is trusted.
11. Phase 2 tests (temp `UserHome` + temp swarm-skills home, per contract's TDD-per-unit): (a) returned env map's `HOME` is a fresh per-launch dir, not `m.d.UserHome`; (b) that dir contains no `.claude`/`.codex`/`.agents`/`.cursor`/`.gemini` entries even when the fake `UserHome` has them; (c) settings.json written by `setupEnv` has exactly one `mcpServers` key (`swarm`), `context.foreign_personal_skills: false`, `context.foreign_personal_rules: false`, and *retains* an arbitrary extra key planted in a fake real settings.json (proves clone-then-mutate, not rebuild); (d) `museDir/skills` contains only symlinks resolving under the fake swarm-skills home; (e) returned env map's `XDG_DATA_HOME` equals `<fake UserHome>/.local/share`, not the isolated `HOME`; (f) `auth.json` symlink still present.

## Open questions for the controller

1. **Drop operator MCP servers entirely (as proposed) or keep some allowlisted?** The user's report just says "isolated environment... like every other agent." Proposing full drop (mcpServers → swarm-only), matching codex's precedent. Confirm.
2. ~~`HOME` isolation's symlink-back allowlist~~ — resolved in this revision: denylist (symlink every real `~` entry except `.claude`/`.codex`/`.cursor`/`.agents`/`.gemini`/`.config`), not an allowlist, per design bullet 1. No open question remains here unless the controller wants a narrower allowlist instead.
3. **`XDG_DATA_HOME`/state/cache stay real/shared** (plugins + session logs + model catalog — no secrets found there) once explicitly pinned past the isolated `HOME`, per Finding 5/7's constraint that the plugin store's integrity check rejects any partial reconstruction. Confirm this trade-off (session transcripts/logs from spawned agents land in the operator's real `~/.local/share/muse`, same as today) is acceptable, and that leaving *all* real plugins visible (not just superpowers — `elements-of-style` too, per Finding 7) is fine, rather than trying to filter the plugin store to an allowlist (which Finding 5 shows is not straightforward).
4. **TUI blocking-dialog risk (design bullet 10)** — should Phase 2 begin with a live tmux-pane probe of `Launch`'s first run under a fresh isolated `HOME` (matching the existing `StartupDialogs()` TODO) before writing the `setupEnv` change, given this is the one part of the design Phase 1 could not fully de-risk with headless (`exec`/`skills list`) probing alone?

---

# Phase 2 — implementation

Controller ruling: `docs/plans/2026-09-25-muse-isolation-probe.md`'s companion ruling —
proceed with the proposed design; Q1 drop all operator MCP servers; Q2 accept the shared
real `XDG_DATA_HOME`/state/cache, pinned explicitly; Q3 live TUI probe as PM.1.

Docs added/updated: spec `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md`
section **A8. muse spawn isolation**; plan
`docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md` package
**### PM: muse spawn isolation** (units PM.1-PM.4, all ticked); probe evidence doc
`docs/plans/2026-09-25-muse-isolation-probe.md`.

## PM.1 — live TUI probe

Reproduced the proposed env (denylist-isolated `HOME`, isolated `XDG_CONFIG_HOME` with
cloned-then-mutated `settings.json` + swarm-only skills, `XDG_DATA_HOME`/state/cache
pinned to the real paths) by hand in a scratch tmux session and launched muse
interactively exactly as `Muse.argv` would. Captured the pane at 5s/15s/30s: no blocking
dialog at any point — no trust prompt, no onboarding wizard, no foreign-context notice
requiring a keypress; the model answered the probe prompt and the pane returned to idle
on its own by 15s. Full commands, env and pane captures:
`docs/plans/2026-09-25-muse-isolation-probe.md`. Resolves `muse.go`'s
`// TODO(probe): confirm no first-run onboarding wizard blocks a fresh HOME` for this
change. `StartupDialogs()` needed no new entry.

Commit: `867bca7 docs(plan): live TUI probe for muse spawn isolation`.

## PM.2 — `HOME` denylist isolation + pinned data/state/cache

TDD evidence:
- RED: `TestMuseSetupEnvIsolatesHOMEExceptOtherAgentsPersonalRoots`,
  `TestMuseSetupEnvPinsDataStateCacheToRealHome`, `TestMuseResumeUsesSameIsolation` written
  first; `go test ./internal/adapter/... -run '...'` failed as expected
  (`Launch must set HOME to an isolated per-launch dir`, empty `XDG_DATA_HOME`/state/cache,
  `Resume must also isolate HOME, got ""`).
- GREEN: implemented `museHomeDenylist` (`.claude`, `.codex`, `.cursor`, `.agents`,
  `.gemini`, `.config`, `.muse`) and a per-launch `homeDir` that symlinks every other real
  `~` entry through; added `XDG_DATA_HOME`/`XDG_STATE_HOME`/`XDG_CACHE_HOME` to the
  returned env map, pinned to `m.d.UserHome`-relative real paths. All three tests pass;
  full `TestMuse*` suite still green.

Commit: `79204ba feat(adapter): isolate muse's HOME, pin its real data/state/cache dirs`
(+ `f36dfea docs(plan): tick PM.2`).

## PM.3 — settings clone-then-mutate

TDD evidence:
- RED: new test `TestMuseSetupEnvDropsOperatorMCPServersAndForeignContext` (a fake real
  settings.json with `notion`/`vercel` MCP servers, a `runtime_capabilities` trust hash and
  a `tui.foreign_context_notice_shown` flag) failed as expected (`mcpServers` still had
  `notion`/`vercel`, `context.foreign_personal_skills`/`rules` were `<nil>`). Also flipped
  the now-stale assertion in `TestMuseLaunchIsolatesXDGConfigHomeWithLiteralSwarmEnv` (it
  previously asserted an unrelated operator MCP server *survives* the clone — that
  expectation changed intentionally per spec A8/ruling Q1) and confirmed it failed first
  with the old code.
- GREEN: `mcpServers` is now replaced wholesale with swarm-only (no longer merged);
  `context.foreign_personal_skills`/`context.foreign_personal_rules` set to `false`,
  merged into any existing `context` map. All targeted tests plus the full `TestMuse*`
  suite pass; `runtime_capabilities`/`tui` keys confirmed preserved (clone-then-mutate,
  not rebuild).

Commit:
`2390793 feat(adapter): drop operator MCP servers and foreign personal context from muse spawns`.

## PM.4 — swarm-managed skills only

TDD evidence:
- RED: new test `TestMuseSetupEnvLinksOnlySwarmManagedSkills` written first; captured
  failing against the *old* whole-dir-symlink implementation via `git stash` (confirmed
  `TestMuseLaunchIsolatesXDGConfigHomeWithLiteralSwarmEnv` still passed post-edit, since
  its `skills` whole-dir assertion was already removed; the new test failed with
  `.../muse/skills/building-components: no such file or directory`, i.e. no per-skill
  symlinks existed yet).
- GREEN: replaced the whole-dir `symlinkIfExists(realSkillsDir, museDir/skills)` with
  `install.LinkSkills(filepath.Join(museDir, "skills"), skillsHome,
  install.SkillLinkMode(install.KindMuse))` (`skillsHome, _ := install.SkillsHome(m.d.Home)`)
  — the same call `claude.go`'s `writeProjectSwarmConfig` makes. All targeted tests plus
  the full `TestMuse*` suite pass; a personal skill planted directly under the fake real
  `~/.config/muse/skills` is confirmed absent from the isolated link set.

Commit: `67aaf0b feat(adapter): link only swarm-managed skills into muse spawns`
(+ `16d6634 docs(plan): tick PM.3-PM.4`).

## Verify (per contract + ruling)

```
$ go test ./internal/adapter/... ./internal/install/...
ok      github.com/AlexanderTar/agent-swarm/internal/adapter    0.489s
ok      github.com/AlexanderTar/agent-swarm/internal/install    3.989s

$ go build ./... && go vet ./...
(clean, no output)

$ go test ./... -count=1
... all ok, except:
--- FAIL: TestBoardServedAtRoot (internal/httpapi)
    server_test.go:257: GET /kanban = 503
```
`TestBoardServedAtRoot` is the documented pre-existing baseline failure (web bundle not
built) — not touched by this package, not new.

## Safety (final)

`~/.config/muse/{settings.json,auth.json,skills/}` — byte-identical mtimes/sizes to the
very first Phase-1 snapshot; all Phase 2 code/test work used Go's own temp dirs
(`t.TempDir()`), never the real `UserHome`. `~/.local/share/muse/plugins/installed.json`
diffed identical. `~/.local/share/muse/{session-index.db,tui-history.jsonl}` grew only
from PM.1's own real interactive tmux session (the ruling's "session/log files excepted"
carve-out) — nothing changed since that snapshot during PM.2-PM.4.

## Files changed

- `internal/adapter/muse.go` — `setupEnv`: `HOME` isolation (denylist), pinned
  `XDG_DATA_HOME`/state/cache, settings clone-then-mutate (swarm-only `mcpServers` +
  `context.foreign_personal_*`), `install.LinkSkills` for swarm-managed skills only.
- `internal/adapter/muse_test.go` — 5 new tests (PM.2 x3, PM.3 x1, PM.4 x1); updated one
  existing test's now-intentionally-changed assertion (PM.3).
- `docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md` — new `A8. muse spawn
  isolation` section.
- `docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md` — new
  `### PM: muse spawn isolation` package, units PM.1-PM.4, all ticked.
- `docs/plans/2026-09-25-muse-isolation-probe.md` — new, PM.1's probe evidence + Phase 1
  findings summary.

## Self-review / concerns

- Design bullet 2 from Phase 1 (verify `~/.config` siblings still resolve correctly once
  `HOME` moves) turned out to be a non-issue, confirmed by the advisor and by
  `TestMuseLaunchIsolatesXDGConfigHomeWithLiteralSwarmEnv`'s still-passing `gh` sibling
  assertion: that loop reads `m.d.UserHome/.config` directly, never the muse subprocess's
  `HOME` env var.
- The `museHomeDenylist` excludes exactly `.claude`/`.codex`/`.cursor`/`.agents`/`.gemini`/
  `.config`/`.muse`. If the operator has some *other* coding agent whose personal root
  isn't in this list (a newer/less common tool), it would still leak through the denylist
  the same way `.agents` did originally. Not testable against every possible tool in
  Phase 2's scope; flagged here rather than silently assumed complete.
- No judgment calls beyond what the ruling already resolved (Q1-Q3).
- **`m.d.Home` (the swarm home, `~/.swarm` in production) is not on the `museHomeDenylist`.**
  Since `~/.swarm` lives inside `UserHome`, the denylist loop symlinks
  `<isolated HOME>/.swarm → ~/.swarm`, and `<isolated HOME>` itself lives at
  `~/.swarm/run/launch/<sid>/muse-home/` — a symlink cycle for any `find -L`/recursive
  walk a spawned agent's shell tool might run from `$HOME`, and it hands the spawned
  agent read access to `~/.swarm/run/tokens/*` (every currently-live session's swarm
  auth token, not just this one). Neither is a regression — today's real-`HOME` spawn
  already had unrestricted access to both — and the ruling's denylist (item 5) named only
  `.claude`/`.codex`/`.cursor`/`.agents`/`.gemini`/`.config`/`.muse`, not `.swarm`, so no
  code change was made here without a ruling. `agy.go`'s isolated `HOME` does not carry
  `~/.gemini`'s sibling `.swarm` through either way (it has no denylist loop at all, only
  named symlinks), which is a precedent for excluding it if the controller wants this
  closed in a fix round.
- `MUSE_LIVE_PROBE=1 go test ./internal/adapter/ -run TestMuseSetupEnvReachesRealMCPSubprocess -v`
  (uses `Deps{Home: t.TempDir(), UserHome: t.TempDir()}`, never real dirs; `muse exec
  --provider echo`, no paid model call) run against the real binary post-PM.3/PM.4: PASS
  in 35.75s, confirming the shipped `setupEnv`'s swarm-MCP env delivery (the pre-existing
  P0-1 mechanism) still reaches a real spawned stdio MCP subprocess after this package's
  settings.json mutation changes. Real `~/.config/muse/{settings.json,auth.json}`
  confirmed unchanged afterward.

---

# Fix round 1

Opus review (`docs/plans/2026-09-25-muse-isolation-probe.md`'s companion
`pm-fix1-findings.md`): all A8 design items present, leak channels closed. Two Important
fixes + four Minors. No live muse runs required; real home not touched (verified after
every commit — `~/.config/muse/{settings.json,auth.json}` byte-identical throughout).

## Important 1 — exclude the swarm home by path, not name

`m.d.Home` (`~/.swarm` in production) sits inside `UserHome`, so the old name-based
denylist symlinked it into the isolated `HOME` — a `find -L`/`grep -R` cycle, since the
isolated `HOME` itself lives under `<swarm home>/run/launch/<sid>/muse-home/`. Fixed with
a path comparison (`filepath.Join(m.d.UserHome, e.Name()) == filepath.Clean(m.d.Home)`)
alongside the name-based denylist, `ponytail:`-flagged as only a single-level check (a
swarm home nested deeper than directly-under-`UserHome` still cycles via its own parent).
Documented explicitly that this closes the symlink-cycle problem but does **not** close
absolute-path token exposure — a shell tool that already knows/guesses the real swarm
home's path can still `cat ~/.swarm/run/tokens/*` directly; `HOME` isolation only controls
`$HOME`-rooted lookups.

RED: `TestMuseSetupEnvExcludesSwarmHomeByPath` (new; `d.Home = UserHome/.swarm`) failed
first — `isolated HOME must not carry the swarm home (.swarm) through, got err=<nil>`.
GREEN after the path-based exclusion.

## Important 2 — `HOME/.config` symlinked to the isolated `XDG_CONFIG_HOME`

Previously `.config` was simply excluded from the `HOME` denylist loop (absent from the
isolated `HOME` entirely), so a tool that hardcodes `~/.config/<name>` instead of honouring
`$XDG_CONFIG_HOME` (gcloud, solana) would find nothing there. Fixed: `HOME/.config` is now
its own symlink to `xdgConfigHome`, made idempotent (removed first if present) since
`Launch`/`Resume` share one `launchDir` per session and both call `setupEnv`.

RED: extended `TestMuseSetupEnvIsolatesHOMEExceptOtherAgentsPersonalRoots` with a fake
`UserHome/.config/gcloud` and an assertion that `HOME/.config` is a symlink to
`XDG_CONFIG_HOME` resolving through to it — failed first (`.config: ... no such file or
directory`, since `.config` was simply absent). GREEN after adding the symlink (first pass
also caught an `os.Symlink` "file exists" regression from `Launch`+`Resume` sharing a
`launchDir` in other existing tests — fixed by making the link idempotent, matching
`symlinkIfExists`/`applyLink`'s own pattern).

## Minor 3 — `.claude.json` added to `museHomeDenylist`

Claude Code's other top-level config file (`~/.claude.json`, distinct from the `~/.claude`
directory) is now denylisted too. Folded into the same RED/GREEN cycle as Important 2's
test.

## Minor 4 — doc cleanup

- Separated `museHomeDenylist`'s doc comment from `setupEnv`'s own (previously one
  continuous block).
- Removed the stale "[settings.json] cloned first so every other MCP server ... keeps
  working" line from `setupEnv`'s doc — contradicted Q1 (every operator MCP server is
  dropped, not kept).
- `Launch`'s doc comment now says both `HOME` and `XDG_CONFIG_HOME` are isolated, not only
  `XDG_CONFIG_HOME`.
- Removed the now-resolved `// TODO(probe): confirm no first-run onboarding wizard blocks
  a fresh HOME` on `StartupDialogs()` — PM.1's live TUI probe answered it; replaced with a
  pointer to that probe doc.

## Minor 5 — `TestMuseResumeUsesSameIsolation` compares full env maps

Previously spot-checked a handful of keys. Now calls both `Launch` and `Resume` with the
same `Spec`/`SessionID` and asserts the two returned env maps have the same key count and
every value matches, in addition to the existing `HOME`-isolation and pinned-path checks.

## Minor 6 — probe doc: record the two session entries; spec A8 wording

PM.1's `tmux kill-session` (rather than a graceful `/exit`) caused muse to register two
session entries in the real store 7 seconds apart — confirmed by inspecting (read-only)
`~/.local/share/muse/sessions/2026/09/25/`: `01a0d78b-9da4-7bf3-93e7-fc959c5bca99`
(`session.jsonl` mtime 08:50:56) and `01a0d78b-b80a-78d2-a9e1-464013cc0eac` (mtime
08:51:03). Recorded in the probe doc with cause and a note that future probes should
`/exit` the muse TUI gracefully instead of killing the tmux session. Per the controller,
the leftover registry entries are left for the user to decide — not cleaned up by this
package. Spec A8's Q2 trade-off text now names `tui-history.jsonl` prompt history
explicitly among what lands in the real, shared data dir.

## Verify

```
$ go test ./internal/adapter/... ./internal/install/...
ok      github.com/AlexanderTar/agent-swarm/internal/adapter
ok      github.com/AlexanderTar/agent-swarm/internal/install

$ go build ./... && go vet ./...
(clean)

$ go test ./... -count=1
... all ok, except the same documented baseline:
--- FAIL: TestBoardServedAtRoot (internal/httpapi)
```

## Commits

- `5fc5914` fix(adapter): fix round 1 -- exclude swarm home by path, link HOME/.config
- `516d4e4` docs(plan): fix round 1 minor 6 -- record the probe's two session entries

## Safety

`~/.config/muse/{settings.json,auth.json}` confirmed byte-identical (same size/mtime)
after every commit in this round; `git status --short` clean after the final commit; no
live muse runs performed this round (findings explicitly said none were needed).

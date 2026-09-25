# Package PM: muse spawn isolation (user request, PR #20, 2026-09-25)

## User's report
"muse … the agent is spawned with full home folder and context along with skills and MCPs. Muse should follow the same pattern as every other agent: isolated environment with injected swarm MCP, skills and user instructions. Run some probes to validate."

## Current code (internal/adapter/muse.go setupEnv)
- Isolates only XDG_CONFIG_HOME → <launchDir>/muse-config. Every real ~/.config sibling is symlinked in.
- Clones the REAL ~/.config/muse/settings.json (so every MCP server the operator configured is present), adds mcpServers.swarm with literal env.
- Symlinks real auth.json and the real ~/.config/muse/skills (all user skills, not just swarm's).
- Writes instructions to <cwd>/AGENTS.md. HOME is the real home; the muse data dir (~/.local/share/muse: plugins cache, sessions, catalog) is shared.
Compare codex.go setupEnv (isolated CODEX_HOME: auth + hooks + skills + plugins linked; swarm MCP injected via -c flags; the user's config.toml, and so its MCP servers, NOT carried), agy.go, cursor.go, claude.go (per-spawn project dir with swarm skills links).

## Phase 1 — probes (do this first, then STOP and report)
Goal: establish exactly what a swarm-spawned muse sees today, and what each isolation knob does. For each finding record the exact command, env, prompt and output lines.
1. Reproduce a swarm-style launch in a scratch dir: build the env exactly as setupEnv does today (call it from a tiny Go test harness gated by an env var, or replicate by hand), run muse headless (find the non-interactive mode: `muse exec`/`--print`/similar via `muse --help`), and ask it to list: its MCP servers/tools, its skills, any instructions/memory/context files it loaded (AGENTS.md, global rules, memories), and its plugins.
2. Enumerate everything muse reads from HOME / XDG_CONFIG_HOME / XDG_DATA_HOME / XDG_STATE_HOME / XDG_CACHE_HOME: `strings` the binary for paths, check `muse --help` and subcommands (`muse skills list --json`, `muse mcp list` if present), and look at the real ~/.config/muse and ~/.local/share/muse trees (read-only).
3. Test isolation knobs in a sealed scratch HOME (no symlink to any real dir except what a test explicitly needs; COPY auth.json rather than link): does muse honour HOME, XDG_DATA_HOME, XDG_STATE_HOME? Which is the minimum set of copied/linked files for auth + the superpowers plugin to work? Does a global user instructions/memory file exist and where?
4. Confirm resume still works (the provider session store location) under the isolation you'd propose.
Report (to the report file) a table: what muse sees today vs. what it should see, and a concrete proposed design: which env vars to set, which files are copied/linked/generated per launch, how swarm skills get in (the swarm-managed skills root for muse, i.e. entries install.WriteSkills manages, or ~/.swarm/skills links — NOT the user's whole skills dir), how the superpowers plugin stays available (spec Locked decision 7: superpowers stays a plugin install), how instructions are injected, and what about resume. Then reply NEEDS_CONTEXT with the design summary so the controller can rule before you implement.

## Phase 2 — implement (only after the controller replies)
Spec section "A8. muse isolation" + a "### PM" package in the plan; test-first units; tests use temp homes.

## Safety
- Never write/link/move/delete anything under the real home except inside your scratchpad dir. Reading and COPYING (auth) is fine.
- Prefer probes with sealed HOME/XDG_* so muse writes its session/state into scratch, not the real ~/.local/share/muse. If a probe unavoidably needs the real data dir, say so in the report and use it read-only where possible.
- Snapshot `ls -la ~/.config/muse ~/.local/share/muse ~/.config/muse/skills` before and after all probes; any difference beyond muse's own session/log files → stop and report BLOCKED with the diff.
- Stay out of internal/runtime (another agent is working there) and out of internal/adapter/agy.go and internal/install/config.go's agy lines (another agent).

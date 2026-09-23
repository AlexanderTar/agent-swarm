# Claude "Unknown skill: swarm" / "no MCP server configured" at spawn

## Context

**Reported symptom.** A freshly spawned claude session (`3-tiny-lows-from-task-coder`)
showed this in its pane right after spawn:

```
▎ Channels (experimental) messages from server:swarm inject directly in this session
▎ server:swarm · no MCP server configured with that name

⏺ Skill(swarm)
  ⎿  Error: Unknown skill: swarm. Did you mean share?
```

The agent then fell back to calling `swarm_sync` directly as an MCP tool a few
seconds later and got on with real work, so this wasn't permanently fatal.

**Correction after a targeted delivery probe (see Phase 1):** the
"no MCP server configured with that name" line is cosmetic. It does **not**
mean native wake is broken — a `notifications/claude/channel` frame sent by
the MCP server after that banner prints is still delivered into the pane
(`← swarm: <content>`) with the banner unchanged, both with and without this
fix. Only `Skill(swarm)` is actually, functionally broken by
`--setting-sources project,local`; the banner line is a confusing but inert
side effect of the same flag. This spec and the fix still address both
(the banner is legitimately confusing — the original report reasonably read
it as "native wake is dead"), but the channel half of the fix is a clarity
improvement, not a restoration of broken functionality.

**Original hypothesis (this investigation's starting point): a client-side
startup race.** The theory was that `claude` starts executing the kickoff
prompt (baked into argv, delivered as the first turn with no gate) before it
finishes connecting the configured MCP server and registering whatever the
server exposes. This spec supersedes that hypothesis — see Root cause below.

**Investigation method (systematic-debugging).** Phase 1 evidence:
- Daemon log (`~/.swarm/logs/daemon.err.log`) shows the same two-hop
  `UserPromptSubmit` pattern (immediate no-op at spawn, a `decision`-bearing
  one 2–5 s later) on every single fresh claude spawn sampled across the last
  ~5 hours of production activity (12+ sessions checked) — this symptom class
  is universal, not a rare one-off.
- Direct reproduction (`claude -p "list every skill..." --setting-sources
  project,local`) shows the `swarm` skill is **never** in the list, 100% of
  three repeated runs, with **zero** delay — proving this is not timing
  dependent for the skill symptom.
- Direct reproduction with a fake stdio MCP server that answers `initialize`
  with **zero artificial delay** (`DELAY_MS=0`) still produces
  `server:swarm · no MCP server configured with that name` under
  `--setting-sources project,local`. A slow-vs-fast MCP response makes no
  observed difference to this symptom either.
- The same fake-server harness with `--setting-sources user,project,local`
  (the only variable changed) makes **both** symptoms disappear: the channels
  banner is clean and `Skill(swarm)` reports "Successfully loaded skill".
- **Delivery probe (added after the advisor's second review, before declaring
  done):** does the "no MCP server configured with that name" banner mean
  native wake is actually broken, or just that the banner text is wrong?
  Extended the fake MCP server to emit one `notifications/claude/channel`
  frame (`PROBE-WAKE-MARKER`) 6 s after `initialize`, then ran two sessions —
  one with `--setting-sources project,local` and no project `.mcp.json`
  (pre-fix shape), one with the same flags plus a project `.mcp.json` mirroring
  `--mcp-config` (post-fix shape). **`← swarm: PROBE-WAKE-MARKER` was
  delivered into the pane in both**, banner unchanged either way. Native wake
  delivery was never actually broken by this bug; the banner line is
  cosmetic. Also checked process count during the post-fix run:
  `ps -ef | grep fake_mcp.py` showed exactly one process per session (not
  two) — `--strict-mcp-config` does not let the project `.mcp.json` open a
  second, duplicate connection to the same server name, so the fix carries no
  double-delivery risk.

## Root cause

`(*Claude) flags()` in `internal/adapter/claude.go` passes
`--setting-sources project,local` on every claude launch (added 2026-09-22 in
commit `43d5de0c`, `feat(adapter/claude): isolate mcp to swarm and inject
custom instructions`; see `docs/specs/2026-09-22-isolated-mcp-and-custom-instructions.md`
line 39). That flag's *intended* effect, stated in that spec, was narrow:
stop the agent's own global `~/.claude/CLAUDE.md` from leaking into the swarm
agent's system prompt. It does that.

Its *undocumented side effect*, confirmed by direct reproduction (Phase 1
above), is that Claude Code gates two more things on the same "user" setting
source, neither of which is CLAUDE.md:

1. **Skill discovery.** `~/.claude/skills/*` (where `swarm install` writes the
   `swarm` and `swarm-orchestrator` skills — `internal/install/skills.go`) is
   scoped to "user". Excluding "user" makes those skills invisible to the
   `Skill` tool, even though the files are present, correctly named, and
   correctly formed. This is why `Skill(swarm)` always fails with "Unknown
   skill: swarm" — not a race, a deterministic exclusion.
2. **Channel name resolution (banner only, not delivery — see the delivery
   probe above).** `--dangerously-load-development-channels server:swarm`
   resolves the name `swarm` against whatever registry Claude Code consults
   to print the "server:swarm · ..." status line — a registry that is
   populated only when the "user" source loads, and that is a *different*
   registry from the one `--strict-mcp-config --mcp-config <path>` actually
   connects the tool-calling (and, per the delivery probe, channel-emitting)
   server from. The `swarm` MCP server works fine the whole time, for both
   tool calls and `notifications/claude/channel` delivery; only the *banner
   text* is wrong, printing "no MCP server configured with that name" even
   though one is configured and working. This is still worth fixing — the
   banner is exactly what led the original report to (reasonably, but
   incorrectly) conclude native wake was down — but it is a cosmetic bug, not
   a functional one.

Both failures are **100% deterministic given `--setting-sources
project,local`** — they do not depend on timing, process load, prompt length,
or how fast the `swarm mcp` shim's `initialize` round-trip is. The original
"startup race inside the Claude CLI" hypothesis is refuted for this bug: it
is a straightforward, always-on misconfiguration introduced by yesterday's
`--setting-sources` change, not a race Claude Code loses only sometimes.

(The daemon log's universal 2–5 s-later `UserPromptSubmit` pattern noted in
Phase 1 is consistent with, but not proof of, this: it is the model abandoning
`Skill(swarm)` and calling `swarm_sync` directly, which succeeds once that
tool round-trip completes — normally a couple of seconds after spawn. This
report does not depend on that interpretation; the skill/channel
determinism above was established with a controlled, zero-delay probe.)

## Locked decisions

- **Do not remove `--setting-sources project,local`.** Its stated purpose
  (suppress the user's own global `~/.claude/CLAUDE.md`) is a deliberate,
  documented design decision from 2026-09-22 and stays.
- **Do not restore the "user" source.** That would re-admit
  `~/.claude/CLAUDE.md` and any other user-global settings/MCP servers the
  2026-09-22 change was written to keep out — reopening the very isolation
  hole that change closed.
- **Fix by making both resources visible under "project" scope instead,**
  confirmed by direct reproduction to work:
  - A `.mcp.json` at the session's cwd (not just the launch-dir file already
    passed via `--mcp-config`) — verified live: this alone silences "no MCP
    server configured with that name".
  - `.claude/skills/swarm/SKILL.md` and `.claude/skills/swarm-orchestrator/SKILL.md`
    at the session's cwd — verified live: this alone makes `Skill(swarm)`
    succeed.
- **The session's cwd (`~/.swarm/work/<agent-name>`) is a safe place to write
  both.** Confirmed live across 5 running production sessions (some active
  for 24+ hours): this directory is always empty at spawn
  (`internal/runtime/agents.go:947-948`, fresh `MkdirAll` right before
  `Launch`/`Resume`) and stays empty for the life of the session — agents
  `cd` elsewhere for real repo work. It is never a git worktree
  (`git status` inside it: "not a git repository"). Writing two small config
  files there cannot pollute any tracked diff and is never visible to `git
  status` in the agent's real working tree.
- **Reuse the embedded skill bodies**, not a copy: `internal/install.SkillBody(name)`
  and `internal/install.SkillNames` (already embedded via `go:embed` in
  `internal/install/skills.go`) are the single source of truth for skill
  content; `internal/adapter` does not duplicate the markdown.
- **Reuse the already-marshaled MCP JSON**, not a second construction: the
  `mcp` byte slice `flags()` already builds for `claude-mcp.json` is written
  a second time, verbatim, to `<cwd>/.mcp.json`.
- **Applies to both `Launch()` and `Resume()`**, since both call `flags()`.
- **No fix for the Claude CLI itself.** This is closed-source; the fix here is
  entirely on agent-swarm's side (making both resources reachable via a scope
  Claude Code does load), not a patch to Claude Code.

## API / behavior changes

`(*Claude) flags(s Spec) ([]string, error)` in `internal/adapter/claude.go`
gains two side effects before it returns (no argv/signature change):

```go
// after mcp is marshaled and written to claude-mcp.json (launch dir):
if err := writeProjectSwarmConfig(s.Cwd, mcp); err != nil {
    return nil, err
}
```

New unexported helper in `internal/adapter/claude.go`:

```go
// writeProjectSwarmConfig makes the swarm skill and the "swarm" channel name
// resolvable under --setting-sources project,local, which excludes the
// "user" scope where `swarm install` writes ~/.claude/skills and where
// Claude Code's own channel-name registry apparently lives too (see
// docs/specs/2026-09-23-claude-mcp-startup-race.md). It writes project-scope
// copies into the session's own scratch cwd -- always empty at spawn, never
// a real git worktree (internal/runtime/agents.go creates it fresh right
// before Launch) -- so both become visible without re-admitting the
// excluded user scope (and with it ~/.claude/CLAUDE.md, which
// --setting-sources project,local exists to keep out).
func writeProjectSwarmConfig(cwd string, mcp []byte) error {
	if cwd == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(cwd, ".mcp.json"), mcp, 0o600); err != nil {
		return err
	}
	for _, name := range install.SkillNames {
		dir := filepath.Join(cwd, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), install.SkillBody(name), 0o600); err != nil {
			return err
		}
	}
	return nil
}
```

New import: `github.com/AlexanderTar/agent-swarm/internal/install` in
`internal/adapter/claude.go`. Verified no import cycle: neither package
currently imports the other.

## File list

- `internal/adapter/claude.go` — add `writeProjectSwarmConfig`, call it from
  `flags()`, add the `internal/install` import.
- `internal/adapter/claude_test.go` — new tests (see plan) asserting
  `.mcp.json` and both `.claude/skills/*/SKILL.md` files land in `s.Cwd` with
  the right content, for both `Launch()` and `Resume()`.
- No other files change. No changes to `internal/install`, `internal/spawn`,
  `internal/runtime`, or any other adapter.

## Verification

- `go test ./internal/adapter/...` — new tests pass, all existing
  `TestClaudeLaunchArgv` / `TestClaudeMCPConfigJSON` / `TestClaudeMCPConfigIsIsolated`
  /`TestClaudeResumeUsesTheProviderID` tests still pass unchanged (argv is
  untouched by this fix).
- `go build ./...` and `go vet ./...` clean.
- Manual end-to-end re-run of the exact probe that found the bug (documented
  in the plan's manual-verification step): mirror `flags()`'s real argv
  against a throwaway `cwd` with the fix applied, on an isolated tmux test
  socket (never `swarm`), and confirm both the channels banner and
  `Skill(swarm)` are clean.

## Explicitly out of scope

- Any change to the Claude Code CLI itself (closed source, not ours to fix).
- Any change to `--strict-mcp-config`, `--setting-sources`'s value, or any
  other existing flag in `flags()`.
- The agy skills-path bug (`fix/agy-skills-isolation`,
  `fix/agy-skills-correct-path`) — a different, already-fixed, unrelated
  mechanism (agy's isolated `HOME` symlink strategy), noted here only as
  precedent that this class of "isolation flag hides a resource Claude/agy
  needs" bug has recurred and gets fixed the same way (make the resource
  reachable at a scope that survives isolation).
- Deeper investigation into exactly which internal Claude Code registry the
  channel-name lookup consults, or why skill discovery is tied to
  `--setting-sources` at all — both are closed-source implementation details;
  this spec documents the observed, reproduced behavior, not Claude Code's
  internals.

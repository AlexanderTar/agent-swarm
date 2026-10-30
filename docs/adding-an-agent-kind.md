# Adding an agent kind

Follow this when you add a CLI (a new "kind", like `muse`, `cursor` or `grok`) to Swarm, or change how an existing kind is launched, hooked or measured. A kind touches about 45 production files in 12 areas plus a DB migration, and most of the seam is not enforced by the compiler. Muse shipped with a dozen same-day fix rounds because of that; section 9 lists them.

Worked examples: `internal/adapter/muse.go` (hooked via settings-level hooks in a per-launch `settings.json`, isolated HOME), `internal/adapter/cursor.go` (hooked via `hooks.json`), `internal/adapter/claude.go` (fully hooked). Muse's delivery spec and plan are in `docs/specs/2026-09-23-muse-first-class-agent.md` and the menubar follow-up in `docs/specs/2026-09-23-menubar-muse-support.md`.

## 1. Probe first, no code

Write nothing until each question below has a dated live probe recorded in the spec (exact command, trimmed output, date, CLI version). Never invent a flag: muse shipped a bogus `-i` that was only found by a live failure. Run probes in a scratch directory that is not a repo, with a throwaway CLI session, and delete debug logs afterwards.

Questions to answer:

1. Binary, `--version` format, and the process name tmux sees (`pane_current_command`, cut to 15 characters; a `sh -c` wrapper hides it).
2. Auth: files, the login command, and an offline "signed in" check. Check for keychain prompts.
3. Model list source and the effort/reasoning dialect (names, defaults, what an unknown model returns).
4. Every launch flag: yolo/approve, trust, model, effort, session id, kickoff prompt, system-prompt addendum, subagent switches. Probe each one; also probe whether hidden flags exist.
5. Resume: does `--resume <id>` accept a kickoff prompt? (Muse's resume dropped it; Swarm then passes guidance as a note.)
6. Hooks: which events fire, the stdin payload, whether the launching process's env reaches the hook process, which events can inject model context, and whether the native question tool fires a hook.
7. MCP config: location, per-launch override, `${VAR}` expansion in `env`, and what it inherits from other CLIs.
8. Skills dir, symlink versus copy support, plugin/marketplace commands, and whether superpowers is installed or can be.
9. Imports of other CLIs' config (Claude, Cursor): what is loaded by default and how to switch it off; isolation via a home/data-dir variable.
10. Native wake: any side channel to push a prompt into a live session.
11. Session, transcript and context files, and any compaction knobs.
12. Read-only print mode for the advisor.
13. Usage and quota source, and whether reading it triggers keychain prompts.
14. Image reading (attachments).
15. Pane text for idle, busy and dialog states, from real tmux captures.

Copy-paste recipes (replace `$CLI`; run from a scratch dir):

```sh
# 1. binary, version, process name
command -v $CLI; $CLI --version
tmux -L probe new-session -d -s p -x 160 -y 50 -c "$PWD" "exec $CLI"
sleep 3; tmux -L probe display -p -t p '#{pane_title}|#{pane_current_command}'

# 4. is a flag real? (an unknown flag exits non-zero with "unexpected argument")
$CLI --some-flag --version

# 15. capture each pane state with escapes, then match on ANSI-stripped text
tmux -L probe capture-pane -p -e -t p > capture-idle.txt
tmux -L probe send-keys -t p 'sleep 20 then say done' Enter
sleep 2; tmux -L probe capture-pane -p -e -t p > capture-busy.txt

# paste behaviour: bracketed paste, then Enter
printf 'line one\nline two' | tmux -L probe load-buffer -
tmux -L probe paste-buffer -p -t p; tmux -L probe capture-pane -p -e -t p
tmux -L probe send-keys -t p Enter

# interrupt: send C-c once, capture, then check whether a second C-c quits
tmux -L probe send-keys -t p C-c; tmux -L probe capture-pane -p -t p

# 6. hook probe: register a script for every event that dumps stdin and env,
#    run one real prompt that uses a tool, then diff payloads and env
cat > hook.sh <<'EOF'
#!/bin/sh
d=${PROBE_OUT:-/tmp}/hook-$1; cat > "$d.stdin.json"; env | sort > "$d.env"
EOF
chmod +x hook.sh
# plant a distinct codeword in each event's output and ask the model to list
# every codeword it was told: only events whose output reaches the model show up

# 11. find where a session is written
find ~/.$CLI* -newer capture-idle.txt -type f 2>/dev/null | head
```

Keep paid probes few and cheap. Quota can run out mid-probe (a free-tier grok account stopped after about $0.28), so run the model-free probes (version, auth, hook registration, MCP startup) first.

## 2. Spec rules

- A new kind always needs a DB migration. `agents.kind` has a CHECK constraint, and SQLite can only change it by rebuilding the table. Never write "DB models: none": muse's spec did, and every real muse spawn failed until `internal/db/schema/0008_allow_muse_agent_kind.sql` landed.
- The migration must re-state every kind and carry every column added by later ALTERs (`role_overrides`, `auto_restart`, `kind_reason`, `advisor_requested_effort`, `low_token`, and anything newer). Read the most recent rebuild, `internal/db/schema/0011_designer_and_artifact_kinds.sql`, and the migrations after it first.
- The spec must say, for each row of the checklist in section 3, what the new kind does or that it deliberately has no support.
- Ship the parity guard (section 7) in the first package, before the adapter, so the remaining packages turn it green.

## 3. Seam checklist

Do the areas in this order. "Caught" says what notices a miss: **C** compile error, **T** an existing test, **-** nothing (falls to a `default` branch). Every `-` row needs a parity test row or a manual check.

**Kind and database**

| Area | Where | Caught |
|---|---|---|
| Kind enum, `AgentKinds`, `Display()` | `internal/kinds/kinds.go` | T |
| Runtime alias | `internal/runtime/types.go` | - |
| DB CHECK migration, with a test that inserts every kind | `internal/db/schema/`, `internal/db/db_test.go` | T |

**Install**

| Area | Where | Caught |
|---|---|---|
| Kind, display name, config/data/skills dirs. `install.Kinds` is a second enum, kept in step with `kinds.AgentKinds` by hand | `internal/install/config.go` | T (count is hard-coded in `internal/install/config_test.go`) |
| Binary detection and writer dispatch (forgetting dispatch is silent) | `internal/install/agents.go` | - |
| Writer: MCP entry, hooks if the CLI dispatches them, attribution off | `internal/install/muse.go`, `internal/install/cursor.go` | - |
| Doctor check, plus the hard-coded binary list for the "Agents" check | `internal/install/doctor.go` | T partly |
| Uninstall, legacy cleanup, migrate | `internal/install/uninstall.go`, `internal/install/legacy.go`, `internal/migrate/runner.go`, `internal/migrate/integrations.go` | T partly |
| Skills link mode (probe symlink versus copy; agy needed copy) | `internal/install/skills.go` | T |
| Plugin support matrix and marketplace/vendoring | `internal/install/plugins.go` | - |

**Catalog and adapter**

| Area | Where | Caught |
|---|---|---|
| Superpowers check (default `false` makes orchestrator Preflight refuse the kind) | `internal/catalog/service.go` | - |
| Model fetcher, parser, human labels | `internal/catalog/fetch.go`, `internal/catalog/model.go` | - |
| Adapter: Launch/Resume, flags, model and effort mapping, idle/busy/process regexes, dialogs, interrupt keys, session discovery or hooks, rename. Register it in `init()`; a missing `register` shows up only as "isn't installed" at Preflight | `internal/adapter/adapter.go`, `internal/adapter/muse.go`, `internal/adapter/cursor.go` | C for the method set only |
| HOME isolation: an adapter that sets HOME to a per-launch dir must call `linkSwarmShared` from setupEnv so the shared swarm folders keep resolving; never link `run/` (it holds the launch dir: a cycle). Full rule: section 9, "From isolating HOME" | `internal/adapter/swarmhome.go`, `internal/adapter/muse.go`, `internal/adapter/agy.go` | T |
| `Busy()` is not cosmetic: the reconcile tick samples it on every live pane, and a match (with no dialog on screen) makes the session read busy (`Store.BusySince`), which silences `agent.undeliverable`, `agent.stale` and `progress_deadlock`, shows the board's busy label, and starts the 15 min progress reminder. It also feeds the urgent interrupt (a pending pause or handoff can cancel a pane-busy worker whose hooks are silent, any kind) and the agy native-wake skip (a busy agy is not woken by a second `agy --conversation` process). A regex that also matches an idle pane shows up as a stuck-forever busy label (until the 60 min busy ceiling); a regex that never matches leaves a hookless kind reading idle mid-turn. Check it against both a busy and an idle capture | `internal/runtime/busy.go`, `internal/adapter/muse.go` | - |
| Optional adapter seams (`PasteReady`, `LastReply`, `SessionContext`, `ModelObserver`, `SessionRenamer`) | `internal/runtime/wake.go`, `internal/runtime/print.go`, `internal/runtime/budget.go`, `internal/runtime/observedmodel.go` | - |
| Context and transcript readers | `internal/adapter/context.go` | - |

**Runtime tables and branches**

| Area | Where | Caught |
|---|---|---|
| Login hint (`loginCommand`); the Go, web and menubar hints must agree | `internal/runtime/agents.go` | - |
| Native todo tool name for the todos-sync text | `internal/runtime/inbox.go` | T |
| Native compaction cap (`NativeCapTokens`) and context sampling | `internal/runtime/budget.go` | - |
| Hooked versus unhooked question set; `answer_text` handling for unhooked kinds | `internal/runtime/requests.go`, `internal/runtime/native.go` | - |
| Wake: native, hook notice or tmux paste (and when the pane is paste-ready) | `internal/runtime/wake.go` | - |
| Resume-with-kickoff, launch-limit and low-token branches | `internal/runtime/agents.go` | - |
| Fallback is generic, but fires only for kinds with a usage source or a pane usage-limit line (`adapter.UsageLimiter`, `kindExhausted`) | `internal/runtime/fallback.go`, `internal/runtime/usagelimit.go` | T |

**Hooks, usage, advisor and other daemon pieces**

| Area | Where | Caught |
|---|---|---|
| Hook handler: event-name mapping, question-tool list, payload variants, `swarm hook <kind>` usage string | `internal/hook/handler.go` | - |
| Usage source (or an explicit "no source") | `internal/usage/usage.go`, `internal/usagegate/usagegate.go` | - |
| Advisor command, answer parser and transcript path, or a documented "no advisor" | `internal/advisor/run.go`, `internal/advisor/transcript.go` | - |
| MCP tool text that names kinds (for example `answer_text`) | `internal/mcpserver/tools.go` | - |
| Attachment probe row | `internal/attachments/attachments.go` | - |
| Paste and submit behaviour in the tmux layer | `internal/spawn/tmux.go` | - |
| Settings defaults are derived from `AgentKinds`; check, don't edit | `internal/settings/settings.go` | T |

**Surfaces and text**

| Area | Where | Caught |
|---|---|---|
| Web union, label, login command | `web/src/types.ts`, `web/src/copy.ts` | C once the union is extended |
| Web icon map and SVG copy | `web/src/components/icons.tsx`, `web/src/assets/agents/` | C |
| Menubar enum and `selectable` | `apps/menubar/Sources/SwarmBarKit/Wire.swift` | C |
| Menubar label, login command, icon name | `apps/menubar/Sources/SwarmBarKit/Copy.swift`, `apps/menubar/Sources/SwarmBarUI/AgentIcon.swift` | C |
| Menubar bundle icon lists | `apps/menubar/scripts/bundle.sh`, `apps/menubar/scripts/check-bundle.sh` | T |
| Menubar tests and ordered fixtures (muse missed two) | `apps/menubar/Tests/SwarmBarTests/`, `apps/menubar/Tests/Fixtures/` | T |
| Skill text naming the native question tool per kind: edit `skills/` then run `make skills-sync` so `internal/install/skills/` matches | `skills/swarm/SKILL.md`, `internal/install/skills/swarm/SKILL.md` | - |
| README supported list, keychain table, advisor list, troubleshooting | `README.md` | - |

## 4. How agents talk

- All agent traffic goes through the swarm MCP tools (`swarm_sync`, `swarm_send`, `swarm_checkpoint`, `swarm_ask`, `swarm_read`) and the daemon inbox, `internal/runtime/inbox.go`. The kind's writer installs the MCP entry; the entry needs the `SWARM_*` values per launch. If the CLI expands `${VAR}` in MCP `env`, one static entry works. If not (muse), write literal values into a per-launch config. Muse's missing MCP env was a Critical bug: its agents could not reach the daemon.
- "You have mail" delivery, tried in this order (`internal/runtime/wake.go`):
  1. The adapter's native `Wake`, if the CLI has a side channel to push a prompt into a live session.
  2. For hooked kinds, inbox notices ride on hook `additionalContext` of events that reach the model.
  3. Otherwise a tmux paste after a delay, when the hook turn is over or the pane is idle. `PasteReady` or a question dialog still refuses. See "Idle is the hook turn".
- Questions. For a **hooked** kind whose native question tool fires PreToolUse, the hook intercepts it and `swarm_ask kind:"question"` is refused; add the kind to `questionHookKinds` in `internal/runtime/requests.go` only after a live probe shows the hook fires. For an **unhooked** kind, agents use `swarm_ask kind:"question"` and report native approvals with `native_answer` and `answer_text`; the daemon can verify the answer against the CLI's session log (`internal/runtime/native.go`). Children always ask their parent with `swarm_send`.
- Only put kickoff text and notices on events that reach the model. Check with the codeword probe in section 1; an event that accepts `additionalContext` but discards it is a trap.

## 5. Auth and usage

- Preflight order (`internal/runtime/agents.go`): enabled in settings, adapter registered, installed, `AuthOK()`, model in catalog, effort supported, superpowers for orchestrators. Each failure produces a message the user sees, so make `AuthOK()` an offline check (a status command or an auth file), never a network call.
- Login hints live in three places and must agree: `loginCommand` in `internal/runtime/agents.go`, `AGENT_LOGIN_CMD` in `web/src/copy.ts`, and `loginCommand` in `apps/menubar/Sources/SwarmBarKit/Copy.swift`.
- Usage source: prefer one that never prompts for a keychain password. Muse went export reader, then MSP probe, then keychain mint, then back (reverted the next day) because the keychain item's team-id partition prompted users. A kind with no source is never "exhausted", so `internal/runtime/fallback.go` never substitutes it; say so in the spec instead of leaving it implicit. Document the credential in the README keychain table.
- A free account may expose its quota only as a 429 or an in-TUI card. Capture that text; the adapter needs it as a dialog matcher.
- **Declare the hard-stop line.** The usage gate reads only the headline meter at 100%, and a kind's blocking cap need not be the headline (muse's weekly cap is account-wide and read 99% while the pane said "Usage limit reached ... wait for usage to reset at Oct 26 at 12:00 AM", BUG-110), so the pane is the primary signal. Capture the kind's usage-limit line and implement `adapter.UsageLimiter` (`UsageLimit(capture, now) (line, resetsAt, ok)`; muse's is `Muse.UsageLimit`). The runtime (`endIfUsageLimited`, `internal/runtime/usagelimit.go`) then ends a session that shows it before its first checkpoint, from both `watchStartup` and the reconcile tick, marks the kind exhausted until the reset (`kindExhausted` is what every gate caller asks), and re-admits a workflow step through the usage fallback. A kind with no known line gets a reasoned entry in `usageLimitNone` in `TestKindParity`. A modal that needs a person (grok's card, codex's picker) is not a hard stop; keep it a dialog.

## 6. Icons

- One source of truth: `assets/icons/*.svg`, with each icon's origin and licence in `assets/icons/LICENSES.md`. Source from Simple Icons (CC0) or LobeHub `@lobehub/icons-static-svg` (MIT); never draw an approximation (muse's first icon was an invented sparkle, replaced in a later package).
- Normalise to a 24x24 template image: `perl apps/menubar/scripts/normalize-icons.pl`.
- Web keeps a byte-identical copy in `web/src/assets/agents/` (check with `cmp`) wired in `web/src/components/icons.tsx`.
- Menubar loads `<name>.svg` from the app bundle; add the name to both `apps/menubar/scripts/bundle.sh` and `apps/menubar/scripts/check-bundle.sh`, to `AgentIcon.swift`, and to the icon assets test under `apps/menubar/Tests/SwarmBarTests/`.

## 7. Guards and verification

Add to the first package:

- `TestKindParity`: for every value in `kinds.AgentKinds`, assert a registered adapter, a catalog fetcher, a usage source (or an explicit none), an advisor command and parser (or an explicit none), a superpowers check that is not the default `false`, a `loginCommand` arm, a `todosNext` entry, a `NativeCapTokens` decision, a hooked or unhooked classification, an `install.Kinds` entry with a `SkillsDir` and a doctor check, a skills link mode, and a plugin matrix row.
- A test that `install.Kinds` and `kinds.AgentKinds` hold the same set.

Then verify, in this order:

```sh
make test-go                      # vet, fmt, web build, go test -race ./..., coverage gate
make test-web                     # pnpm test (vitest); run `cd web && pnpm typecheck` for types only
make test-menubar                 # swift build + swift test + menubar coverage
apps/menubar/scripts/bundle.sh    # then apps/menubar/scripts/check-bundle.sh
<KIND>_LIVE_PROBE=1 go test ./internal/adapter/ -run Probe -v   # gated live probes, see internal/adapter/muse_wake_probe_test.go
```

Gate live probes behind an env var (`MUSE_LIVE_PROBE=1` is the pattern) and `t.Skip` without it, so CI never calls a paid CLI.

Finally, spawn every role on the new kind in a real Swarm and watch it: an orchestrator that passes Preflight with superpowers, a child that asks a native question, pause and resume (does the kickoff survive?), a wake while idle and while busy, an interrupt, the usage row, the advisor, and an uninstall that leaves no trace. Muse's DB CHECK bug and env bug both passed unit tests and failed only here.

## 8. Size limits: brief and launch command

Two limits bit long briefs (BUG-106, BUG-105). Both are tooling limits, so the rule is a guard, not a reminder.

**The brief cap (6000 characters).** `items.MaxBrief`, `items.ErrBriefTooLong` and `items.BriefFits` (`internal/items/brief_limit.go`) are the only definition. `items.Create` and `items.Update` refuse a brief over it (an update that doesn't write a brief never re-measures an old one), `runtime.RenderBrief` measures every kickoff brief with the same call, and `Store.preflightBriefs` renders every workflow step's brief at `swarm_workflow start` and refuses before anything spawns. Cause of BUG-106: the cap was only checked at render time, so a brief grown by an update let the build step spawn and failed the review step. Never add a second constant or a `len(brief) > N` of your own; call `items.BriefFits` / `items.ValidateBrief`. Guards: `go test ./internal/items/ -run 'OverlongBrief|LegacyOverlong' -count=1`, `go test ./internal/runtime/ -run 'BriefLimitIsShared|LegacyOverlongBrief|RefusesOverlongBrief' -count=1`.

**The tmux command line (~16 KB).** tmux refuses `new-session` with `command too long` once argv plus every `-e K=V` passes about 16 KB (tmux 3.7c: 16300 bytes passed, 17000 failed; probe with `tmux -L probe new-session -d -- sleep 5 "$(head -c 17000 /dev/zero | tr '\0' x)"`). The assignment brief never rides argv (the agent reads it with `swarm_sync`). What does is the kickoff and, for grok, `--rules <instructions>`. `spawn.Spawner.Start` therefore writes any argv element of 256 bytes or more to `<home>/run/launch/<session>/arg-N` and starts the agent through `launch.sh` in that dir, which `exec`s the same command with each element read back byte for byte. What to do: pass the kickoff as one whole argv element (a kind that splices it into a longer string, e.g. `--prompt=<kickoff>`, escapes the launcher); keep env values small (they are not moved); keep `Spawner.LaunchDir` set where a Spawner is built (`cmd/swarm/daemon.go`), because `Start` fails loudly rather than overflow without it. Never put the kickoff, instructions or a brief in `-e` or argv by another route.

Traps when diagnosing: "command too long" surfaces as `tmux new-session <name>: ... exit status 1: command too long` on the run, which looks like a brief problem but isn't; `fakeTmux` in `internal/runtime` records argv before the launcher rewrites it, so runtime tests can't see this limit (only the real-tmux test in `internal/spawn` can, and it skips when tmux isn't installed). Guards: `go test ./internal/spawn/ -run 'Beyond|Launcher' -count=1` (57 KB kickoff through real tmux, byte for byte) and `go test ./internal/adapter/ -run KickoffAsOneArgv -count=1` (every kind hands the kickoff over as one element).

## 9. Lessons learned

From muse (2026-09-23 to 2026-10-09):

- DB CHECK constraint missed; every real spawn failed. The spec said "DB models: none".
- Install writer dispatch missed; the kind installed but wrote nothing.
- MCP env not forwarded (Critical): the agent could not reach the daemon.
- Resume dropped the kickoff prompt.
- An invented launch flag (`-i`) reached the adapter.
- The exec'd process name differed from the binary name, so liveness checks failed.
- Fresh-machine plugin install was untested; uninstall and doctor coverage were incomplete.
- Menubar tests with ordered fixtures were missed by the plan.
- The first icon was invented, not sourced.
- Usage source churned because the keychain prompted users.
- A hook plugin shipped before proving the CLI passes env to hook processes, and was reverted.
- The advisor was never added, and nothing flagged it.
- A muse session blocked on its weekly usage cap sat "running" for hours: the poller read the cap as 99%, the pane's limit line sat above an ordinary idle `❯`, and the only relay was `no_ack`. Cause: the gate checks the headline meter at 100% and no kind read its pane for a limit line. Detect: `sqlite3 -readonly ~/.swarm/swarm.db "select meters_json, headline_id from usage_snapshots where agent_kind='muse'"` shows a non-headline meter near 100, and `sqlite3 -readonly ~/.swarm/swarm.db "select a.name, s.state from sessions s join agents a on a.id = s.agent_id where s.first_sync_at is null and s.state in ('spawning','running')"` lists a running agent that never synced, whose pane (`tmux capture-pane`) shows the limit line. Guards: `go test ./internal/runtime/ -run 'UsageLimit|TestKindParity' -count=1` and `go test ./internal/adapter/ -run UsageLimit -count=1`. Gaps: the exhaustion record is in memory (a daemon restart forgets it until the next session shows the line again), and a line that names no reset marks the kind exhausted for one hour. A pane-noted reset reaches the daemon's quota-reset loop (`Store.UsageLimitResets`), so relays held for that kind are flushed about a minute after it, same as a poller cutoff.

From muse, mid-turn delivery and hooks (probed 2026-10-29 against muse 1.4.14-R7309.1, private tmux server, throwaway config with the swarm MCP removed):

- **A line pasted mid-turn and submitted with Enter steers the running turn.** It shows as a "Queued input" block (footer "steering the running turn") and reaches the model at its next boundary: when a tool finishes or muse backgrounds it after 10 s. A live coder's 11:00:09 message was steered 22 s later (one paste delay) and drained at 11:00:35. There is no late-paste bug; the "71 and 78 minute" gaps in `messages` were redeliveries of unacked mail (see AGENTS.md: `delivered_at` is the last delivery).
- **A re-paste while the notice is still queued only stacks a duplicate** (seen live: 31 s after the first paste, both drained). `Store.noticeStillQueued` holds the re-paste while `Muse.QueuedInput(capture, agentName)` sees a block holding our notice (it carries the agent name), for at most `repasteCap`; input the agent or operator queued does not hold it. Successful pastes are logged (`wake: pasted <name> (n pending)`).
- **An Enter right after the paste is sometimes swallowed** and the notice stays in the composer (twice by hand, not reproduced in five mid-turn pastes through `PasteLine`'s own load-buffer, paste-buffer, 0.5 s, Enter sequence). `Store.resubmitIfHeld` reads the composer after each paste (`Muse.ComposerHolds`: the lines from the last `❯` to the box's bottom rule, first 30 non-space characters, because the TUI hard wraps) and sends Enter again, at most three times, 0.7 s apart. A kind opts in by implementing `ComposerHolds(capture, text string) bool`. `tryPaste` returns the check and `WakeDue` runs all of them concurrently after the paste loop, so a slow muse pane never delays another session's paste.
- **Settings-level hooks work.** `settings.json` `hooks` in Claude's shape fire PreToolUse, PostToolUse, UserPromptSubmit and Stop, with muse's own `session_id` on stdin and `MUSE_SESSION_ID` in env but no `SWARM_*` (the hook env is muse's small allowlist), so `museHookCommand` bakes `env SWARM_SESSION=... SWARM_TOKEN_FILE=... SWARM_URL=... swarm hook muse <Event>` into each command (a shell runs it; quoted paths with spaces work). stdout reaches the model: `hookSpecificOutput.additionalContext` from PreToolUse, PostToolUse and UserPromptSubmit (plain stdout from UserPromptSubmit too), a PreToolUse `permissionDecision: "deny"` fails the tool with "tool blocked by hook: <reason>", and a Stop `{"decision":"block","reason"}` re-fires Stop with `stop_hook_active: true` and lands the reason as a developer context block. Not probed, so not emitted: `updatedInput`, `sessionTitle`. SessionStart fires at the first turn, not at launch, so it is not registered. `transcript_path` is always null and the shell tool is named `bash`.
- **Muse fires the same hooks for its internal `submit_reminder_decision` tool, from child sessions with their own `session_id`.** Left alone they would record a child id as the provider session (resume breaks) and eat the inbox notice into a sub-agent; `Muse.IgnoreHook` drops them before the handler touches the session. Any new hooked kind needs the same question answered: do sub-agents fire the parent's hooks, and with which session id?
- `request_user_input` still dispatches no hook; the old plugin-hook probes (2026-09-25/26) stand.
- Probe recipe: private `tmux -L` server, `env -u SWARM_SESSION -u SWARM_TOKEN_FILE -u SWARM_URL XDG_CONFIG_HOME=<scratch>` with a copy of `~/.config/muse/settings.json` minus `mcpServers`, `auth.json` symlinked, a logging hook script, `--model muse-spark-1.3 --reasoning-effort minimal` for the one real-model turn. Muse's own `~/.local/share/muse/sessions/<yyyy>/<mm>/<dd>/<session>/session.jsonl` records what reached the model (`inbox_item_queued`/`inbox_item_drained` for pastes, `context_block_updated` for hook context) and is the way to read delivery latency.

From grok (probed 2026-10-16 against grok 1.0.60; findings, not code):

- The trust flag is `--trust`, hidden from `--help`; muse's `--trust-workspace` is rejected. `--trust` persists a folder entry in the user's trust file, so clean up probe entries.
- Grok imports the Claude and Cursor world by default: skills, plugins, `~/.claude.json` MCP servers (including a second, unscoped `swarm`), Claude settings hooks, rules. Per-launch switches turn most of it off, but not Claude plugin hooks or plugin MCP servers. Plan for isolation with a dedicated home plus the switches, and decide on auth (a symlinked auth file risks racing the single-use refresh token).
- Hooks fire with Claude-compatible JSON (both snake_case and camelCase keys) and the hook process inherits the launcher's env, so grok is a hooked kind. But SessionStart and UserPromptSubmit output never reaches the model; use PreToolUse/PostToolUse `additionalContext` or the Stop block reason, and send the kickoff in the launch prompt.
- A PreToolUse deny on the native question tool suppresses the question, which makes the hooked question path possible.
- Some flags silently change behaviour: `--disallowed-tools` turns leader mode off, and leader mode is what makes native wake through the leader socket possible. Probe flag interactions, not just flags.
- The `❯` input box is drawn in every state, so a bare-prompt idle regex is wrong; idle and busy are told apart by the footer line. A modal card swallows pasted input, so it needs a dialog matcher that sets `PasteReady` false.
- `--debug-file` writes MCP bearer tokens and the account email in clear text. Never enable it by default; delete probe logs.
- A free account's quota showed up only as a 429 and a TUI card, not in any usage endpoint.

What the grok probes decided (2026-10-16, delivered across the grok packages; evidence in the SPIKE-52 probe notes):

- **One leader per agent.** Hooks and MCP servers run as children of the grok leader with the env of the TUI that started it. Two TUIs on one `--leader-socket` both reported the first TUI's `SWARM_SESSION` and token file, so a shared leader breaks agent identity. Each Swarm session launches its own leader on its own socket (`internal/adapter/grokleader_life.go`). Before sharing any per-session process across agents, probe whose env its children inherit.
- **A leader-less launch relies on `--leader` being opt-in.** When the per-session socket path would not fit `sun_path`, the launch passes neither `--leader` nor `--leader-socket` and wake pastes (`leaderArgs`). That is safe only if grok starts a leader solely when `--leader` is passed, which is how the probes saw it (the flag is hidden; `--help` lists only `--leader-socket`). `--no-leader` parses, also hidden, but its effect was not proven (it needs a logged-in run), so Swarm does not pass it. If a grok update starts leaders by default, probe a leader-off flag and pass it here, or a long-path agent would join the Swarm grok home's shared leader and take on another agent's identity.
- **The grok home is 0700.** It holds session logs with token prefixes. `WriteGrok` and `RefreshGrokPlugins` create it 0700 and tighten an older, looser one (`ensureGrokHome`).
- **Exhaustion is noted only for quota text.** `usage-exhausted`, `usage limit` and `tokens (actual/limit)` count. A bare `retry_state exhausted` and the per-second 429 (`resource-exhausted ... Requests per Second (actual/limit)`) clear within seconds and must not mark grok out for 24 hours (`internal/usage/grok.go`).
- **One env helper, `grokhome.Env`.** Everything that execs grok (launch, catalog, usage, advisor, install) takes its environment from `internal/grokhome/grokhome.go`: `GROK_HOME` under the Swarm home plus every `GROK_<CLAUDE|CURSOR|CODEX>_<SURFACE>_ENABLED=0` compat switch. A call site that skips it reads `~/.grok`, loads the operator's other agents' config and finds no Swarm session. Never set `GROK_ASK_USER_QUESTION`: the question card stays on and Swarm answers it.
- **Auth is shared by symlink, proven by a real refresh.** `auth.json` and `auth.json.lock` in the Swarm grok home link to `~/.grok/` (`internal/install/grok.go`). Natural token refreshes through the links wrote the new token to `~/.grok/auth.json`, kept the link and took the lock through its link; a symlinked auth file had looked risky for the single-use refresh token, and only the live refresh settled it. Not covered: grok creating the lock through a dangling link on a fresh install. `GROK_AUTH_PATH` is a no-symlink alternative whose refresh is untested. If grok ever replaces the link with a real file, copy the new file over `~/.grok/auth.json`; never restore an old backup (its refresh token is spent).
- **Superpowers is installed natively, and never disabled by name.** `grok plugin install` puts it under `<grok home>/installed-plugins/superpowers-<hash>`, but its skills load only while `"superpowers"` is absent from `[plugins] disabled`; a disabled entry beats the `enabled` one install adds, and the native copy shadows Claude's by name. The install also rewrites `config.toml`, so `WriteGrok` installs first and merges Swarm's keys after. Every other Claude plugin is disabled before each launch (`RefreshGrokPlugins`); grok has no allowlist.
- **Skills load through `[skills] paths`.** A symlinked skill dir inside `$GROK_HOME/skills` is ignored; one inside a `[skills] paths` directory loads. That is why grok's skills link mode is `Symlink` and `Config.SkillsDir` points at a directory listed in `paths`.
- **The usage meter is the CLI billing proxy, read with grok's cached token.** `GET https://cli-chat-proxy.grok.com/v1/billing?format=credits` with `Authorization: Bearer <key>` and `x-xai-token-auth: xai-grok-cli`, where `key` is the `https://auth.x.ai::<client id>` entry of `auth.json` (the Swarm grok home's link, then `~/.grok/auth.json`, so the meter works before `swarm install` makes the link). It returns `creditUsagePercent` and the weekly period, matching the TUI's `/usage`; a period with no `creditUsagePercent` is 0% used (the field is omitted at zero). The server keeps accepting a token for a while past `expires_at`, so the meter always tries it. On a 401/403 it runs `grok models` in that grok home, which renews the token silently under grok's own `auth.json.lock` (live-verified 2026-10-16), and tries once more (`internal/usage/grok.go`). A token still rejected after that, or whose renewal failed, is not renewed again until `auth.json` holds a different token, so a dead login never starts grok on every poll. Per-session tokens and cost come from `grok usage <session id>`, run under the Swarm grok home.
- **Never let a background job reach grok's sign-in.** Calling `authenticate` (`cached_token`) over `grok agent stdio` with no usable `auth.json` falls through to grok's browser login, so the old stdio meter opened a login page on every menubar refresh (CHORE-104). Pollers, probes and catalog fetches read `auth.json` and call HTTP; they start grok only to renew a login that has a `refresh_token`, and never refresh the token themselves: it is single-use, and a second refresher can sign the user out. Detect it with `swarm doctor` (the "Grok auth" line) and with `grep '"inline auth flow"\|cached_token fallthrough' <grok home>/logs/unified.jsonl`. `internal/adapter/grokleader.go` still calls `authenticate cached_token` when an agent launches; launches are gated by `AuthOK`, which checks the same `auth.json`.
- **"Couldn't start agent." on a grok launch is usually a slow macOS file-events daemon, not grok or Swarm config (TASK-1438, 2026-10-27).** Every grok process asks `fseventsd` (via `FSEventsGetCurrentEventId`) for its config and skills watchers. When `fseventsd` is overloaded (one call took 113 s; `fseventsd` at 45% CPU and 1.2 GB resident), the TUI's leader never answers `initialize` within the TUI's fixed 30 s, the TUI falls back to an in-process agent, and that session then sits at "▾ Queued 1" for 20-170 s. Swarm's `watchStartup` used to fail any spawning pane that stayed unchanged for 30 s, so the launch became a race grok usually lost, twice in a row (auto-retry hits the same daemon). Fix: `adapter.StartupStaller`; grok declares 5 minutes (`grokStartupStall`), other kinds keep `startupStallTimeout` (30 s). Guarded by `go test ./internal/runtime/ -run TestStartupHonorsAKindsLongerStallLimit -count=1` and `go test ./internal/adapter/ -run TestGrokDeclaresALongStartupStallLimit -count=1`. Detect the cause: `grep '"connect finished"' ~/.swarm/grok/logs/unified.jsonl | tail -5` (`connect_target":"leader"`, `outcome":"timeout"`, `stuck_in":"acp_initialize"` on every launch), and `ps -o %cpu,rss,etime -p $(pgrep -x fseventsd)`. Never lower grok's stall limit back to 30 s, and never read a leader timeout as a broken leader. The leader-log flood `WARN Registration failed ... Connection closed` is Swarm's own wake dial giving up on a stalled leader, a symptom. The warning `--no-subagents has no effect in leader mode` is printed by grok 1.0.60 itself on every leader launch and is harmless (a byte grep of the binary will not find it). The real fix for the machine is restarting `fseventsd` (`sudo killall fseventsd`) or rebooting.
- **The Swarm grok home must not self-update (TASK-1438).** `~/.swarm/grok` has `[cli] installer = 'internal'`, and on 2026-10-27 08:29 it moved `version.json` and `bin/{grok,agent}` to 1.0.70 while Swarm kept launching the PATH `grok` (1.0.60, the only build in `grokLeaderVersions`). It was not the cause of the launch failures (both builds time out the same way), but a home on an unprobed build is a trap. `grokhome.Env` therefore sets `GROK_DISABLE_AUTOUPDATER=1`, and `swarm doctor` ("Grok version") fails when `<grok home>/version.json` names a different build than `grok --version`. To tell which binary a live grok runs, never trust `version.json`: `lsof -p <pid> | grep txt` shows `~/.grok/downloads/grok-<version>-...`. To clear a mismatch, remove `<grok home>/version.json` and the `bin/` links, or install the matching grok. Guarded by `go test ./internal/grokhome/ ./internal/install/ -run 'TestEnvSetsHomeAndCompatOff|TestCheckGrokFlagsAVersionMismatch' -count=1`.
- **A failed launch advances its workflow at once (BUG-127).** `failSession` used to leave the workflow run `active` until the next ~15 s workflow tick, so a launch that failed after its auto-retry looked stuck before it escalated. It now calls `advance` right away (auto-retry, then escalate), except when the workflow lock is already held (the failure came from inside `advance`; locking again would deadlock). Guarded by `go test ./internal/runtime/ -run TestFailedLaunchAfterAutoRetryEscalatesWithoutATick -count=1`.
- **Hook context rides on tool hooks.** SessionStart and UserPromptSubmit output never reaches the model; PreToolUse and PostToolUse `additionalContext` do, and a Stop block reason continues the turn once. So the kickoff goes in the launch prompt and inbox notices ride on tool hooks. An MCP call reaches the hooks as `tool_name: "swarm__swarm_sync"`, with `tool_input` holding the `use_tool` wrapper `{tool_name, tool_input}`, so match tool names with that shape in mind (`internal/hook/handler.go` handles the hooked events).
- **A leader prompt queues, it does not steer.** `session/prompt` mid-turn runs after the current turn's Stop and fires its own UserPromptSubmit, so a native wake is safe while the agent is busy. Passing `--disallowed-tools` silently turns leader mode off, which also turns the native wake off.
- **Parity stays guarded.** `TestKindParity` (`internal/runtime/kindparity_test.go`) and `TestKindParityInstall` (`internal/install/kindparity_test.go`) fail until a new kind has an entry in every table above. A deliberate gap needs a commented exception in the test (e.g. `advisorDisabled` for a kind whose advisor stays off until a live write test proves its read-only form, as muse's did until CHORE-102).
- **Every Claude agent fails within 30 s, never syncs and leaves no transcript: the pane is stuck on the bypass-permissions accept dialog (BUG-139, 2026-10-30).** `--dangerously-skip-permissions` makes Claude Code ask "WARNING: Claude Code running in Bypass Permissions mode ... Yes, I accept" unless `skipDangerousModePermissionPrompt` is true in user, local or managed scope (or `--settings`). Swarm launches with `--setting-sources project,local`, so the user's own `~/.claude/settings.json` copy never counts. Up to 2.1.312 Claude Code leaked excluded settings anyway; 2.1.314 ("Fixed a settings file excluded with `--setting-sources` still supplying some settings") stopped that, so every launch hit the dialog. Fix: `settingsJSON` (internal/adapter/claude.go) always writes `skipDangerousModePermissionPrompt: true` into the per-launch `--settings` file. Never rely on a user-scope setting for a headless launch: anything Swarm needs goes into the `--settings` file or the project config `writeProjectSwarmConfig` writes. Detect: `sqlite3 -readonly ~/.swarm/swarm.db "select a.name, substr(s.failure_text,1,200) from sessions s join agents a on a.id=s.agent_id where s.state='failed' order by s.started_at desc limit 5"` shows the dialog text, and `~/.swarm/logs/daemon.err.log` shows `wake: paste for <agent> skipped/failed (pane not idle ...)`. Traps: the missing `bypassPermissionsModeAccepted` in `~/.claude.json` is not the cause (it was gone for weeks while launches worked). Accepting the dialog once by hand does not help, because the answer is stored in user settings, which agents don't read. An agent cannot reproduce this by starting Claude Code from its shell (Swarm's PreToolUse hook blocks nested agent invocations), so read `failure_text` instead. When every Claude launch breaks at once, check `ls -l ~/.local/bin/` for an overnight auto-update. Guard: `go test ./internal/adapter -run TestClaudeSkipsBypassPermissionsPromptOnLaunchAndResume`.
- **A Claude agent stuck Running with "swarm MCP CONNECT_TIMEOUT" is Claude Code freezing at startup, not the shim or the daemon (TASK-1458, 2026-10-28).** On Claude Code 2.1.310-2.1.311, when several sessions launched together, the Claude process stopped for 30-500 s. It could not read the shim's `initialize` reply, which sat unread on the pipe, and when it woke it ran its overdue 30 s connect check first. In both incidents the daemon saw SessionStart and then no hook from that session until the connect ended; the shim was up and had written its stderr. Fixes: `claudeEnv` (internal/adapter/claude.go) sets `MCP_TIMEOUT=600000`; the shim's wake stream no longer dies at its 60 s request timeout and resets its backoff (`Shim.streamClient`, `bridgeWake`); the `/mcp` wrapper (`mcpserver.Server.Handler`) records each session's JSON-RPC methods (`Store.MCPSeen`) and reconcile (`checkMCPDown`, internal/runtime/mcpdown.go) relays `mcp_down` to the parent, once per session, when a Claude session 3+ minutes old has fired a hook 2+ minutes after it started (SessionStart alone is the freeze signature and does not count) but never sent `tools/list`; only startup failures are covered, a shim that dies after listing tools is not flagged; slow `/mcp` requests (over 5 s) log `mcp: slow request: agent=... method=... took=...` in `~/.swarm/logs/daemon.err.log`. Detect the cause: `grep -h 'Connection timeout triggered\|connected (transport: stdio) in\|Starting connection with timeout' ~/Library/Caches/claude-cli-nodejs/-Users-*-swarm-work-<agent>/mcp-logs-swarm/*.jsonl`. A "triggered after N ms" far above the limit, or a connect that "succeeded" after 30+ s, means Claude froze; a new session should say `timeout of 600000ms`. Workaround for a flagged agent: `swarm_control retry` (the orchestrator's `mcp_down` relay says so); retry flagged agents one at a time, since launching several Claude agents together is the trigger for the freeze. Traps: the shim's stderr line `wake stream: context deadline exceeded` was the old 60 s stream cut, a symptom, not the cause; running `swarm mcp` by hand always answers in milliseconds, so it proves nothing; the `no_ack` relay fires on every slow start and does not mean MCP is down; the MCP-seen record is in memory, so `BootedAt` exempts sessions that started before the daemon did; answering `initialize` inside the shim would not help, because the reply is already waiting on the pipe. Guards: `go test ./internal/adapter -run RaiseTheMCPConnectTimeout`, `go test ./internal/mcpshim -run 'OutlivesTheRequestTimeout|BackoffResets'`, `go test ./internal/runtime -run TestMCPDown`, `go test ./internal/mcpserver -run 'RecordsTheMethods|LogsSlowRequests'`.

From cursor and Antigravity (probed 2026-10-29 against cursor-agent 2026.10.22 and agy 1.4.9; notes with the exact probes and pane text: `~/.swarm/research/CHORE-167/2026-10-29-cursor-agy-mid-turn.md`):

- **Native wake is a second process.** `Agy.Wake` runs `agy --conversation <id> --input-format stream-json`. While the TUI turn is busy that is a hidden parallel agent on the same conversation (own model calls, a duplicate tool call, nothing in the pane, the TUI turn ends without its answer). `WakeDue` therefore skips native wake for an agy that is busy (`BusyNow`: open tool call, pane footer) without recording a wake failure; a busy agy gets the batch from its PreInvocation hook notice, an idle one from the native wake. Test: `TestNativeWakeSkipsABusyAgy`. Any new kind whose wake spawns a process must pass the same busy gate.
- **agy hooks.** PreInvocation fires before every model call inside a turn (four times in a two-tool turn), and its `injectSteps` `ephemeralMessage` reaches the model mid-turn: that is the mid-turn path. PreToolUse `injectSteps` is rejected ("unknown field injectSteps") and makes the tool call fail, so `Agy.HookOutput` emits nothing for PreToolUse and the handler holds that context, like PostToolUse's, for the next PreInvocation. Test: `TestAgyHookOutputNeverInjectsStepsOnPreToolUse`.
- **Pasted lines are next-turn for both kinds.** A line pasted mid-turn waits (agy: "▸ … (alt+m to send immediately)"; cursor: the "follow-ups" box). Alt+M (agy) or Enter on the queued item (cursor) steers it into the running turn, but cursor's steer also moves the running shell command to the background. Do not automate steering; hook notices already cover mid-turn.
- **cursor hooks.** postToolUse `additional_context` reaches the model mid-turn, appended to the tool result in `<system_reminder>`. A queued follow-up fires `beforeSubmitPrompt` while a tool still runs, which the handler maps to UserPromptSubmit; for cursor that is not a turn boundary, so only stop and sessionStart clear the open tool count. Test: `TestCursorQueuedFollowUpDoesNotEndTheTurn`.
- **Stranded cursor follow-up.** After a server error ("The server failed 4 times") a pasted prompt can sit in the follow-ups box ("enter steer · ↑ select/edit · esc cancel") and never run, the pane showing "→ Add a follow-up" and no "ctrl+c to stop". `Cursor.Idle` is false while that box holds an item (a notice never stacks on it) and the "Stranded follow-up" prompt pattern presses one plain Enter when the composer is empty ("→ Add a follow-up"), bounded and then escalated by the usual prompt retry. Up+Enter never submits it (checked live on cursor-agent 2026.10.22, 2026-10-29: Up opens the line for editing and Enter only closes the edit). The variant with the hint "↑ to edit" and the cancelled prompt refilled in the composer reads non-idle too but gets no keys, since Enter would replay that prompt. Behind a running turn (busy footer) the item is left alone. The fixtures `pane-followups-*.txt` were composed from the probe's description, not captured byte for byte: re-capture them if cursor changes the box wording.
- **Probing safely.** Use a scratch dir, a private tmux server, `env -u SWARM_*` and, for agy, an isolated HOME with a copied `settings.json`: accepting the trust prompt otherwise writes the real `~/.gemini/antigravity-cli/settings.json`.

From isolating HOME (muse, agy, TASK-1493):

- **An adapter that isolates HOME must link the shared swarm folders through `linkSwarmShared`.** Cause: skills tell agents to use `~/.swarm/screenshots/<KEY>/` and sibling paths, but an isolated HOME (`<launchDir>/muse-home`, `<launchDir>/agy-home`) makes `~/.swarm` resolve inside the launch dir, so every agent's `mkdir -p` recreates the shared folders there instead of reaching the real ones. What to do: call `linkSwarmShared(isolatedHome, userHome, swarmHome, logf)` from setupEnv (pass the adapter's `d.Log`) (`internal/adapter/swarmhome.go`; wired in `Muse.setupEnv` in `internal/adapter/muse.go` and `Agy.setupEnv` in `internal/adapter/agy.go`). It creates `<isolatedHome>/` plus the swarm home's path relative to the user home as a real directory and symlinks in only the allowlist (`swarmSharedDirs`: screenshots, specs, plans, research, designs, kb), creating each real target first so an absent dir is never skipped. Never do: link `run/` (it holds the launch dir the isolated HOME itself lives under -- a `find -L` cycle), db files, `config.json`, or anything else; never `RemoveAll` a real dir the agent created, since setupEnv reruns against the same launchDir on Resume (only stale links are replaced, real content is left alone). When the swarm home is not a direct child of the user home (outside it, or nested like `~/foo/.swarm`), link nothing: a nested home is reached through a linked ancestor in the isolated HOME (muse links `~/foo` whole), so any write there would land in the real home and could delete its links or turn shared dirs into self-links. Guards: `go test ./internal/adapter/ -run 'SwarmShared|SwarmHome|SwarmDir' -count=1`.

## 10. Idle is the hook turn

Cause: a finished worker was closed only once `ad.Idle` matched the pane (`killCompletedAfter` 60s, otherwise the 2h cap). Muse draws a placeholder on the `❯` line, so that match failed and a finished coder stayed up.

What to do:

- Turn open and close live in `internal/runtime/turn.go` (`NoteHookTurn`, `MarkPromptSubmitted`, `SessionIdle`). Never put them in `internal/runtime/busy.go`. `BusyNow` and the board busy label are a different signal (an open tool call, the grok leader's "working" push, or the pane busy footer) and another change owns that file.
- Open on a non-blocked PreToolUse, on UserPromptSubmit except cursor (`beforeSubmitPrompt` fires mid-turn), and on SessionStart. Close on Stop, StopCancelled, or StopFailure when the hook is not blocked. A blocked Stop stays open. Grok's Stop with reason `shutdown` does not close.
- Persist `sessions.turn_open`, `turn_since`, and `turn_close_event` only when the open/closed state changes. `lastHookAt` stays in memory: after a restart the row still says open or closed and `lastHookAt` is zero, so the stale fallback below can apply on the first tick.
- A daemon paste that submits a prompt calls `MarkPromptSubmitted` before that prompt's hook. It opens the turn and refreshes `lastHookAt`, so a just-pasted idle pane is not treated as a lost Stop.
- `SessionIdle`: grok `LeaderBusy` is never idle. A known hook close is idle. An open turn is idle only when the last hook is zero or older than 15 minutes (`hookTurnStale`) and the pane is idle. No hook state uses `ad.Idle`.
- The progress nudge's working stretch reads that hook turn and does not store it. `TurnEnded` (`internal/runtime/busy.go`) still clears an open tool call. When the call is still open and the turn is known, open, and stale, the idle mark is `toolSince`. Idle under 15 minutes after an interrupted call keeps the stretch: a wake before `hookTurnStale` is not stale, the mark is now, and that silence counts as work. A daemon paste calls `MarkPromptSubmitted` before the prompt's hook and records that mark before it refreshes `lastHookAt`, so an Escape or a crash with no Stop is not nudged on the first tool call after the wake. `TurnEnded` uses the same mark when the hook was not refreshed yet. The handler records a real Stop with `NoteHookTurn` before `TurnEnded`, so a long tool that ends with Stop keeps the mark at the Stop. A think with tools at 0 and the turn still open does not start a new stretch. `BusyNow` is unchanged.
- Reaping, the waiting title, and the quota-reset paste use `SessionIdle`. Paste, print samples, muse context samples, and the urgent interrupt use it for "is the turn over", and still refuse a question dialog (`PasteReady`, or `PromptPatterns` for a kind without `PasteReady`). A paste is also allowed when the pane itself is idle, so a notice that never landed can be sent again after the paste opened the turn. Grok's urgent interrupt still refuses an idle pane, because `C-c` there arms quit.
- The startup check in `agents.go` (the launch is finished when the pane is idle or shows the busy footer) stays a pane check. A hook-closed turn must not skip the busy footer that shows the process got past its trust dialog.
- Claude registers `StopFailure` in the per-launch settings. Claude Code 2.1.314 has that event, and it closes the turn (an API error, including a usage limit Claude reports that way). An Escape that emits neither Stop nor StopFailure stays open until the 15-minute stale fallback, and only if the pane is then idle. The pane usage-limit path `endIfUsageLimitedLive` still ends a session that draws the limit line before its first checkpoint.
- Muse's idle regex matches a bare `❯`, or a dim (SGR 2) or grey (`38;2;103;108;116`, `38;2;124;130;138`, `38;2;138;144;152`) placeholder after it. Typed text has neither and is not idle. Do not match the suggestion words. `Tmux.Capture` is `capture-pane -p -e -J`, so those escapes are still in the capture. Do not run `IdlePrompt` through `StripANSI`.

How to add a kind: register every hook event the CLI actually emits for turn start and turn end, and map the CLI's names in `normalize` (`internal/hook/handler.go`). A turn-end event has to be listed in both `endsTurn` and `hookEndsTurn`, because runtime cannot import hook. Prove it with `go test ./internal/hook/ ./internal/runtime/ -run 'HookEventsOpenAndCloseTheTurn|SessionIdle|CompletedHookClosedSessionIsReapedDespiteThePane' -count=1`. Capture the idle pane with `tmux capture-pane -p -e -J`. If the prompt line carries a placeholder, tell it from typed text by its ANSI dim or grey, and add a fixture under `internal/adapter/testdata/<kind>/`.

How to reproduce the muse case: finish a muse worker whose last hook is Stop and leave the pane on `❯` plus the dim grey suggestion "Ask to monitor a running build or log and get pinged on changes" (muse 1.4.14). Before this change `museIdle` did not match, and the session lived until the 2h cap. The fixture `internal/adapter/testdata/muse/pane-idle-placeholder.txt` joins the prompt colour and dim grey from a live `capture-pane -e` of muse 1.4.14 with that suggestion string from the binary; the captured composer was empty, so the words were not on that pane. Guard: `go test ./internal/adapter/ -run TestMusePlaceholderSuggestionReadsIdle -count=1`.

Per kind, from the installed CLIs on 2026-10-30 (binary strings and the cursor bundle, not a live Escape). Where Stop does not fire, the turn stays open until the 15-minute stale fallback and an idle pane.

| Kind | Turn-end hooks Swarm registers | Interrupt or failure |
| --- | --- | --- |
| claude | Stop, StopFailure | StopFailure is present in Claude Code 2.1.314. An Escape that emits neither event uses the 15-minute fallback. |
| grok | Stop, StopFailure, StopCancelled | Both failure and cancel are registered. A Stop with reason `shutdown` does not close the turn. |
| codex | Stop | The standalone binary has an Interrupt hook (`hooks/src/events/interrupt.rs`) that can write a continuation prompt, and it has no StopFailure string. Swarm does not register Interrupt: a continuation is still the same turn, and closing on it could reap a worker that kept going. An Escape or API error that does not also emit Stop uses the 15-minute fallback. |
| cursor | stop | cursor-agent 2026.10.22 maps Stop to `stop` and has no StopFailure. `beforeSubmitPrompt` does not open the turn. `postToolUseFailure` is an internal step, not in the hook map the installer speaks. Same 15-minute fallback. |
| muse | Stop | muse-bin 1.4.14 says `StopFailure` and `PostToolBatch` are staged schema values and stay production-dark until downstream acceptance, and that Interrupt is observation-only. Do not register StopFailure until a capture shows it fire. Same 15-minute fallback. A usage-limit line still ends a session that has not checkpointed, through the pane path. |
| agy | Stop | The agy binary's hook args are pre/post invocation, pre/post tool, and stop. There is no failure hook. `internal/install/agy.go` registers those five, and they are what reach the handler: `normalize` maps PreInvocation to UserPromptSubmit, which opens the turn (it fires before each model call), PostInvocation to PostToolUse, which does not close, and PreToolUse also opens. |

Never decide reaping or the waiting title from `ad.Idle` alone once the session has hook state. Never edit `busy.go` to store turn open/close. Never strip ANSI before `IdlePrompt`.

Guard: `go test ./internal/runtime/ ./internal/hook/ ./internal/adapter/ -run 'HookEventsOpenAndCloseTheTurn|SessionIdle|CompletedHookClosed|PasteGateFollowsTheHookTurn|MusePlaceholder|MuseTurnEndedFollows|PrintTurnTickFollows|UrgentInterruptFiresWhenTheHook' -count=1`.

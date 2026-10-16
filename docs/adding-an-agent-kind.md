# Adding an agent kind

Follow this when you add a CLI (a new "kind", like `muse`, `cursor` or `grok`) to Swarm, or change how an existing kind is launched, hooked or measured. A kind touches about 45 production files in 12 areas plus a DB migration, and most of the seam is not enforced by the compiler. Muse shipped with a dozen same-day fix rounds because of that; section 8 lists them.

Worked examples: `internal/adapter/muse.go` (unhooked, isolated HOME), `internal/adapter/cursor.go` (hooked via `hooks.json`), `internal/adapter/claude.go` (fully hooked). Muse's delivery spec and plan are in `docs/specs/2026-09-23-muse-first-class-agent.md` and the menubar follow-up in `docs/specs/2026-09-23-menubar-muse-support.md`.

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
| Fallback is generic, but only fires for kinds with a usage source | `internal/runtime/fallback.go` | - |

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
  3. Otherwise a tmux paste after a delay, when the pane is idle (`PasteReady` gates this when dialogs can swallow input).
- Questions. For a **hooked** kind whose native question tool fires PreToolUse, the hook intercepts it and `swarm_ask kind:"question"` is refused; add the kind to `questionHookKinds` in `internal/runtime/requests.go` only after a live probe shows the hook fires. For an **unhooked** kind, agents use `swarm_ask kind:"question"` and report native approvals with `native_answer` and `answer_text`; the daemon can verify the answer against the CLI's session log (`internal/runtime/native.go`). Children always ask their parent with `swarm_send`.
- Only put kickoff text and notices on events that reach the model. Check with the codeword probe in section 1; an event that accepts `additionalContext` but discards it is a trap.

## 5. Auth and usage

- Preflight order (`internal/runtime/agents.go`): enabled in settings, adapter registered, installed, `AuthOK()`, model in catalog, effort supported, superpowers for orchestrators. Each failure produces a message the user sees, so make `AuthOK()` an offline check (a status command or an auth file), never a network call.
- Login hints live in three places and must agree: `loginCommand` in `internal/runtime/agents.go`, `AGENT_LOGIN_CMD` in `web/src/copy.ts`, and `loginCommand` in `apps/menubar/Sources/SwarmBarKit/Copy.swift`.
- Usage source: prefer one that never prompts for a keychain password. Muse went export reader, then MSP probe, then keychain mint, then back (reverted the next day) because the keychain item's team-id partition prompted users. A kind with no source is never "exhausted", so `internal/runtime/fallback.go` never substitutes it; say so in the spec instead of leaving it implicit. Document the credential in the README keychain table.
- A free account may expose its quota only as a 429 or an in-TUI card. Capture that text; the adapter needs it as a dialog matcher.

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

## 8. Lessons learned

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
- **Hook context rides on tool hooks.** SessionStart and UserPromptSubmit output never reaches the model; PreToolUse and PostToolUse `additionalContext` do, and a Stop block reason continues the turn once. So the kickoff goes in the launch prompt and inbox notices ride on tool hooks. An MCP call reaches the hooks as `tool_name: "swarm__swarm_sync"`, with `tool_input` holding the `use_tool` wrapper `{tool_name, tool_input}`, so match tool names with that shape in mind (`internal/hook/handler.go` handles the hooked events).
- **A leader prompt queues, it does not steer.** `session/prompt` mid-turn runs after the current turn's Stop and fires its own UserPromptSubmit, so a native wake is safe while the agent is busy. Passing `--disallowed-tools` silently turns leader mode off, which also turns the native wake off.
- **Parity stays guarded.** `TestKindParity` (`internal/runtime/kindparity_test.go`) and `TestKindParityInstall` (`internal/install/kindparity_test.go`) fail until a new kind has an entry in every table above. A deliberate gap needs a commented exception in the test, as muse's advisor has (`advisorDisabled`): its read-only form is built but unproven live, so it stays off until a live write test passes.

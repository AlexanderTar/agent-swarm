# Linux support plan

Goal: `swarm` (daemon, CLI, board, agents in tmux) installs, runs and passes its
test and coverage gates on Linux (systemd user session, glibc distro), with the
same safety invariants as on macOS. The Swift menu bar app stays macOS-only; on
Linux the board and the CLI are the UI, plus desktop notifications.

Baseline (Pop!_OS 24.04, Go 1.27.3 and tmux 3.8 from Homebrew on Linux):
`go build ./...` and `go vet ./...` are clean, and `go test ./...` passes except
`TestSlowClientDoesNotBlockOthers` (internal/httpapi). The gaps are runtime ones:
`swarm install` aborts (it runs `launchctl`), `swarm doctor` always fails
(Ghostty `.app`, plist rows), and several features shell out to macOS tools.

## Principles

- **Pick the platform once, at composition.** Packages expose both
  implementations of each OS concern (a launchd and a systemd service, a
  Ghostty-via-`open` and a Linux terminal launcher, keychain and file token
  readers). `cmd/swarm` chooses by `runtime.GOOS` when it builds `Config` /
  `Deps`, so no package carries a mutable `GOOS` field and both
  implementations are unit-tested (and count for coverage) on every host.
  Build tags only where code cannot compile elsewhere (already the case for
  `grokleader_peer_{darwin,linux}.go`). Simulated tests do not prove real
  systemd or desktop behaviour, so each such phase ends with a probe on this
  Linux machine.
- **macOS behaviour is unchanged.** Every darwin branch keeps today's argv,
  copy and files; existing tests keep passing unmodified except where a test
  hard-codes something that becomes OS-dependent (then it pins `GOOS=darwin`).
- **Same safety invariants.** Production-only switches (`SWARM_TMUX_SOCKET=swarm`,
  `SWARM_USAGE=live`, and the new `SWARM_NOTIFY=desktop`) are set only in the
  installed service definition. A non-default `SWARM_HOME` never touches the
  live service or agent configs. Tests can never run the real service manager.
- Every phase ends with `make test-go` and `make lint` green on Linux (and the
  darwin code paths still covered by tests), one commit per logical change,
  and a Codex review of the phase diff before the next phase starts.

## Phase 1 — Build, tests and tooling run on Linux

1. **Makefile.** `GO` defaults to `/opt/homebrew/bin/go` only when it exists,
   else `go` from PATH. `codesign` in `build` only on Darwin (`uname -s`).
   `test` runs `test-menubar` only on Darwin; `install` runs `install-app` only
   on Darwin (on Linux it is `install-daemon`). `DEFAULT_SIGN_IDENTITY`'s
   `security` call only on Darwin.
2. **`TestSlowClientDoesNotBlockOthers`.** Find why the stalled stream is not
   closed on Linux (hypotheses: Linux loopback buffers absorb the 19 MB burst,
   or the server's write deadline fires but the conn is not torn down) and fix
   the test or the server. Production SSE must still drop a stalled client.
3. **`scripts/e2e.sh`** default PATH uses `/home/linuxbrew/.linuxbrew/bin`
   instead of `/opt/homebrew/bin` on Linux (it already honours
   `SWARM_E2E_PATH`). Keep its "no real agent CLI" rule enforceable: the
   script aborts if `claude`, `codex`, `agy`, `cursor-agent`, `muse` or `grok`
   resolves on the pinned PATH, and it unsets `SWARM_NOTIFY`. Run it on Linux
   now; product-gap failures are fixed in the phase that owns them.
4. **CI.** Add `Go tests and coverage (Linux)` on `ubuntu-latest` (Homebrew is
   preinstalled on the hosted image: `brew install tmux`), `LANG=C.UTF-8`, and
   `End-to-end (fake adapter, Linux)` as soon as e2e passes on Linux, so every
   later phase is exercised by it. Keep the macOS jobs. Update `docs/ci.md`
   (job table, the "Go tests run on macOS, not Linux" trap) in the same change.

## Phase 2 — Service manager: launchd on macOS, systemd user unit on Linux

1. **`install.Service` interface** (in `internal/install`): `Path()`,
   `Render() []byte`, `Install(ctx, run, dryRun, w)`, `Uninstall(ctx, run)`,
   `Check() Check` (doctor row), `Describe()` (copy: "launch agent" / "systemd
   user service"). `launchd` keeps today's plist and argv byte-for-byte;
   `systemd` is new. `install.Config` gains `Manager` (`launchd` or `systemd`,
   the service manager, chosen once by `newConfig` from `runtime.GOOS`; empty
   means launchd so existing callers are unchanged) and `SystemdUserDir`
   (`$XDG_CONFIG_HOME/systemd/user`, default `~/.config/systemd/user`) next to
   `LaunchAgentsDir`. `Config.Service()` returns the implementation; nothing
   else computes the path.
2. **Unit file** `dev.swarm.daemon.service` (same label as launchd so the
   isolation guards and docs keep one name):
   ```ini
   [Unit]
   Description=Agent Swarm daemon
   [Service]
   Type=simple
   ExecStart="<bin>" daemon --home "<home>"
   WorkingDirectory=<home>
   Environment="PATH=<LaunchPath>" "SWARM_HOME=<home>" "SWARM_TMUX_SOCKET=swarm" "SWARM_USAGE=live" "SWARM_NOTIFY=desktop"
   Restart=on-failure
   RestartSec=10
   KillMode=process
   StandardOutput=append:<home>/logs/daemon.out.log
   StandardError=append:<home>/logs/daemon.err.log
   [Install]
   WantedBy=default.target
   ```
   - `KillMode=process`: the daemon starts the private tmux server, which
     daemonizes but stays in the unit's cgroup. With the default
     `control-group`, every `swarm install` re-run or crash restart would kill
     tmux and every agent; launchd does not (it only kills the job's process
     group, and tmux calls `setsid`). systemd discourages `process`, but it is
     the documented way to keep sessions across restarts (Debian's
     `ssh.service` uses it for the same reason). Before committing to it,
     probe with a transient `systemd-run --user -p KillMode=process` unit on
     this machine: stop, crash (SIGKILL the daemon), restart/reinstall and
     uninstall must leave tmux and its panes running and the restarted daemon
     must re-adopt them; record the results in the commit.
     *Probe result (2026-10-25, systemd 255):* with `KillMode=process` the
     tmux server and its pane kept their pids through SIGKILL of the main
     process (restarted by `Restart=on-failure`), `systemctl --user restart`
     and `stop`; with the default `control-group`, `stop` killed them. No
     `systemd-run --scope` fallback is needed, so none is built.
   - `Restart=on-failure` + `RestartSec=10` is launchd's
     `KeepAlive{SuccessfulExit=false, Crashed=true}` + `ThrottleInterval 10`:
     a clean exit stays stopped on both.
   - Escaping is per directive, from systemd.syntax/systemd.exec: in
     `ExecStart=` quote each word, escape `"` and `\`, `%` → `%%`, `$` → `$$`;
     in `Environment=` quote each assignment, escape `"` and `\`, `%` → `%%`,
     and leave `$` alone (no expansion there); in `WorkingDirectory=` and the
     `append:` log paths only `%` → `%%` (specifiers are expanded there, quotes
     are not parsed). A home containing a newline is refused. Tests cover spaces, `%`, `$`,
     `"` and `\` in the binary, home and PATH, and run `systemd-analyze
     verify` on the rendered unit when it is on PATH (skipped otherwise).
   - `USER`/`HOME` are set by systemd for user units; not written.
3. **Install/uninstall argv (Linux).** Install: write unit, `systemctl --user
   daemon-reload`, `systemctl --user enable dev.swarm.daemon.service`,
   `systemctl --user restart dev.swarm.daemon.service`. Uninstall: `systemctl
   --user disable --now …` (not-loaded tolerated), remove unit,
   `daemon-reload`. Dry run prints the unit and argv. If linger is off
   (`loginctl show-user <uid> -p Linger`), print one line: without linger
   systemd stops your user services when you log out; `loginctl
   enable-linger` keeps the user manager (and the daemon) running. Never
   enable linger ourselves.
4. **v1 legacy and migrate.** Agent Swarm 1.x was macOS-only.
   `RemoveLegacyShared`'s `launchctl bootout dev.swarm.updater` runs only
   under launchd (this is today's hard failure in `swarm install` on Linux).
   The path-based leftover removal stays OS-neutral. `swarm migrate` under
   systemd prints "Agent Swarm 1.x only ran on macOS, so there is nothing to
   migrate." and exits 0 in every mode (as macOS does when there is no 1.x
   data), before touching anything.
5. **Test guard.** `execx`'s `refuseUnderTest` also refuses `systemctl` and
   `loginctl`, and every real entry point applies it: `Run`, `RunEnv`,
   `RunFor`, `Start`, `StartEnv` and the new detached starter (today
   `Start`/`StartEnv` skip it). `TestRealRunnersRefuseLaunchctlUnderGoTest`
   is extended to every entry point and every refused name.
6. **Doctor.** The "Launch agent" row becomes the service's `Check()`:
   darwin unchanged; linux reads the unit (has `daemon --home` and a `PATH=`
   env) and names `swarm install` as the fix. Copy in `cmd/swarm`
   (`main.go` usage, `liveUsage`, uninstall message) says "service" where it
   is OS-neutral.
7. **`docs/isolated-runs.md`**: add the systemd stub (`systemctl`/`loginctl`
   on PATH), the fixed unit name, and how to check/restore the live unit.

## Phase 3 — Desktop integration (terminal, clipboard, environment)

1. **Terminal launcher.** `httpapi.Deps` gets a `Terminal` launcher chosen in
   `cmd/swarm`. macOS keeps today's flow byte-for-byte (menu bar first, 1.5 s
   timer, `open -na Ghostty --args -e …` through `Run`, which returns at once).
   Linux has no menu bar, so the handler launches straight away, before it
   answers:
   - argv from a pure function over `LookPath`: first of `ghostty -e`,
     `xdg-terminal-exec`, `x-terminal-emulator -e`, `gnome-terminal --`,
     `konsole -e`, `kitty`, `alacritty -e`, `wezterm start --`, `foot`,
     `xterm -e`, each followed by `<tmux> -L <socket> attach -t <name>` (tmux
     binary and socket from the store, R11, S-1, S-2). Each argv form is
     checked against the terminal's docs and tested.
   - started detached through a new `execx` starter (own session via
     `Setsid`, stdin/stdout to `/dev/null`, no timeout, reaped in a
     goroutine), never through `Run` (whose 30 s cap would kill a foreground
     terminal), and refused under `go test` like the others.
   - the graphical environment comes from the user manager at launch time
     (`systemctl --user show-environment`: `DISPLAY`, `WAYLAND_DISPLAY`,
     `XAUTHORITY`, `DBUS_SESSION_BUS_ADDRESS`, `XDG_RUNTIME_DIR`), falling
     back to the daemon's own environment only when that command fails, so a
     daemon started before the graphical login (linger) still opens on the
     current session. `show-environment` shell-escapes some values (`$'…'`),
     so it gets a real parser with tests for quoted and escaped values, not a
     split on `=`. Because a desktop may leave stale values behind at logout,
     a display only counts when its socket exists (`$XDG_RUNTIME_DIR/
     $WAYLAND_DISPLAY`, or `/tmp/.X11-unix/X<n>` for `DISPLAY=:<n>`).
   - errors reach the caller: no terminal found → 409 naming the candidates;
     no live graphical session → 409 "No desktop session to open a terminal
     in; use swarm attach"; exec failure → 500. After a successful start the
     handler waits up to 500 ms: a non-zero exit in that window (bad argv,
     display refused) is a 500 with the terminal's stderr; exit 0 (terminals
     that hand off to a server, like gnome-terminal) or still running is
     success, answered as `opened_by: "daemon"`.
2. **`swarm attach` in place.** The CLI sends `{"in_place": true}`; the server
   then skips the GUI fallback timer (today a CLI attach also opens a Ghostty
   window 1.5 s later when no menu bar answers; on Linux that would be every
   time). The `terminal.open` event is unchanged, so the menu bar behaves as
   before.
3. **tmux.conf copy-command.** darwin: `pbcopy` (unchanged); linux: `wl-copy`
   when found, else `xclip -selection clipboard -in`, else `xsel -ib`, else
   leave it unset (tmux falls back to OSC 52 `set-clipboard`).
4. **Agent pane env (`spawn.BaseEnv`).** `TMPDIR` only when set (Linux usually
   leaves it unset; an explicit empty `TMPDIR=` makes Rust's `temp_dir()`
   return `""`). `LANG`: darwin keeps `en_US.UTF-8`; linux uses the daemon's
   `LANG` when it is a UTF-8 locale, else `C.UTF-8`. Pass `XDG_RUNTIME_DIR`
   and `DBUS_SESSION_BUS_ADDRESS` when set (stable per user; Secret Service
   keyrings need them). Not `DISPLAY`/`WAYLAND_DISPLAY`: they can be stale in
   a long-lived pane.
5. **Doctor rows.** Linux: the Ghostty row becomes "Terminal" (OK with the
   terminal found; FAIL listing the candidates otherwise). tmux/uv fix hints
   are OS-aware (Linux: distro tmux is usually older than 3.6; `brew install
   tmux` with Homebrew on Linux, or build from source; uv's installer URL).
   Orphan scan: `ps -axEww` → `ps axeww -o pid=,ppid=,command=` on Linux
   (procps rejects `-E`), and an orphan is a process whose parent is pid 1
   *or* the user's `systemd --user` (a subreaper); add `dockerd`,
   `containerd`, `containerd-shim` to the shared-daemon list.
6. **Linux defaults and hints.** Default `scan_excludes` and the walker's
   fixed skips gain the Linux counterparts (`~/snap`, `~/Videos`,
   `~/.local/share/Trash` is already a dot-dir) when built for Linux; the
   missing-`uv` hint in `install/agents.go` and doctor is OS-aware.

## Phase 4 — Usage tokens and agent processes on Linux

1. **One token reader** for usage polling and the catalog fetcher
   (`usage/usage.go` keychain + `catalog/fetch.go`'s duplicate):
   darwin keeps `security find-generic-password`; linux reads Claude's
   `$CLAUDE_CONFIG_DIR/.credentials.json` (default `~/.claude/.credentials.json`,
   same `claudeAiOauth` JSON), and Cursor's on-disk login (location to be
   verified against cursor-agent on Linux; until verified, Cursor usage shows
   "unavailable" with the reason instead of a wrong number).
2. **Pane process names (measure first).** `wake.panePasteReady` matches
   `pane_current_command` against each adapter's `ProcessNames` and then the
   adapter's idle check. Hypothesis to test: on Linux tmux reports the
   foreground process-group leader's argv[0], so an npm-installed codex
   (`node …/codex.js`, which spawns the native binary in the same group)
   reads as `node` and never matches `^codex$` (Cursor already accepts
   `node`). Measure the real pane command of each agent installed here
   (`claude`, `codex`) in a scratch tmux socket; only if a name differs,
   widen that adapter's patterns, keeping both gates (name and idle) and never
   accepting a shell. Table test with the measured Linux names.
3. **Paste chunking** (`spawn.PasteLine` 512 B / 50 ms) was calibrated on the
   Darwin tty. Re-run the paste tests and one real long paste on Linux; adjust
   only if Linux truncates.

## Phase 5 — LAN access on Linux

1. **Primary IP.** Linux: `ip -4 route get 1.1.1.1` → `src`; fall back to the
   UDP-dial `LocalAddr` trick (no packet sent). darwin unchanged.
2. **mDNS.** A second `lan.Registrar` supervising two processes:
   `avahi-publish -a -R swarm.local <ip>` and `avahi-publish -s -H swarm.local
   Swarm _http._tcp <port>` (the service must point at the published host,
   not Avahi's default one). Avahi reports on stderr, so the starter used
   here merges stderr into the stream the registrar reads (new `execx`
   option; the fake gets the same). Status: registered only once both print
   "Established under name"; an address collision is an error status (not a
   silently renamed host); a renamed service is a warning. Both processes are
   killed on Stop and restarted together with the existing backoff. Missing
   `avahi-publish` → status error "Install avahi-utils to publish
   swarm.local; the board is still at http://<ip>:<port>".
3. Firewall copy in `swarm lan enable`: OS-aware (macOS dialog vs
   `ufw allow <port>/tcp`).

## Phase 6 — No menu bar on Linux: notifications and settings

1. **Desktop notifications.** A daemon-side notifier, enabled only when
   `SWARM_NOTIFY=desktop` (set only by the installed unit, so tests, `make dev`
   and e2e never pop banners). `Events.Subscribe` only gives a coalescing
   wake-up, so the notifier keeps a cursor and drains `Events.After(cursor)`
   after every wake, like the SSE handler. Startup order, as in the SSE
   handler: `Subscribe` first, then `cursor = Events.Latest` (old
   notifications are not replayed), then one drain before the first wait, so
   an event committed between those steps is still delivered (tested). For
   each `notification.created` it runs
   `notify-send --app-name=Swarm --urgency=<level> TITLE BODY` with the same
   launch-time session environment as the terminal launcher. Mapping from
   `settings.notifications[level]` (levels `info`, `attention`, `action`): a
   banner only when `center` is true; `sound` adds the freedesktop sound hint
   (`--hint=string:sound-name:message-new-instant`); urgency info→low,
   attention→normal, action→critical. No action buttons in v1; the body
   carries the item key. A missing `notify-send` or desktop session is logged
   once, not per notification.
2. **`swarm settings`** CLI: `swarm settings` (print JSON) and `swarm
   settings set KEY VALUE` backed by a new `PATCH /api/settings` that merges
   the given top-level keys into the stored settings, validates like PUT and
   kicks reconcile the same way. Today `settings.Store.Put` reads before its
   transaction and rewrites every key, so PATCH gets a new store method that
   reads, merges, validates and writes the changed keys inside one
   transaction (a CLI edit cannot overwrite a concurrent change to another
   key). Linux users can then change
   enabled agents, role defaults and limits without the menu bar.
3. **`swarm notifications [--read-all]`** CLI over the existing endpoints.
4. Out of scope (follow-ups): a web Settings page and notification list on the
   board, a Linux tray app.

## Phase 7 — Docs, copy and CI

1. README: a Linux section (requirements: systemd user session, tmux ≥ 3.6 via
   Homebrew on Linux or source, a terminal, `notify-send`, optional avahi;
   install steps; what the menu bar does that Linux lacks). CONTRIBUTING:
   setting up on Linux (`make test` skips the menu bar there).
2. User-facing "on this Mac" copy becomes OS-neutral ("on this computer")
   where the string is not macOS-specific by nature; update the tests that
   assert it.
3. Final check on this Linux machine: isolated `swarm install --dry-run`
   (fake HOME, `systemctl` stub), an isolated daemon with the fake adapter,
   board reachable, agent attach, notification shown. A real `swarm install`
   on this machine only with the user's go-ahead (it edits `~/.claude`,
   `~/.codex` and installs a user service).

## Risks and open questions

- `KillMode=process` leaves the tmux server in the unit's cgroup after a stop;
  systemd logs a "left-over process" notice on the next start but keeps it
  (probed, Phase 2.2).
- logind `KillUserProcesses=yes` kills user services at logout unless linger is
  on; we only warn.
- Cursor's Linux credential location is unverified (no cursor-agent here).
- Distros without Homebrew and with tmux < 3.6 cannot run Swarm until they
  build tmux; doctor says so.
- Spec references: the spec (`docs/specs`) describes macOS; Linux behaviour is
  recorded in this plan and the README rather than rewriting the spec.

## Review log

- **Codex round 1** (plan v1). Taken: async, failure-reporting Linux terminal
  launch (§3.1); Avahi stderr, `-H`, collision handling (§5.2); guard every
  `execx` entry point (§2.5); per-directive unit escaping, `KillMode` probe,
  restart semantics (§2.2); launch-time desktop environment and no stale
  `DISPLAY` in panes (§3.1, §3.4); measure process names before changing them
  (§4.2); Linux scan excludes and uv hint (§3.6); level-based notification
  mapping and a merging `PATCH` for settings (§6); e2e guard against real
  agent CLIs and Linux e2e in CI from Phase 1 (§1.3–4); platform chosen at
  composition instead of `GOOS` fields (Principles). Not taken: changing the
  default enabled agents when Claude is not installed. That behaviour is the
  same on macOS, so it is a separate change.
- **Codex round 2** (plan v2, "needs changes"). Taken: early-exit check after
  a terminal starts (§3.1); `%` escaping for `WorkingDirectory=` and log paths
  (§2.2); a real `show-environment` parser and socket-liveness check for stale
  displays (§3.1); a transactional store method behind `PATCH` (§6.2); a
  cursor-draining notifier (§6.1); `Config.Manager` instead of a `GOOS` field
  (§2.1). Dropped the `systemd-run --scope` fallback because the probe passed,
  so it needs no guard. Still not taken: Codex-only default settings (same on
  macOS, separate change).
- **Codex phase 2 review.** Taken: `SWARM_NOTIFY=desktop` in the unit now,
  read from Phase 6 on; `XDG_CONFIG_HOME` and macOS-only migrate notes in
  `docs/isolated-runs.md`. Not taken: the `Service` interface is `Noun`, `Path`,
  `Render`, `Commands`, `Start`, `Stop`, `Removed` instead of §2.1's sketch,
  and doctor's launchd row keeps its own plist check (unchanged macOS code);
  the systemd row goes through `systemdCheck(Config)`.
- **Codex round 3** (plan v3, "needs changes"). Taken: subscribe → `Latest`
  → drain ordering for the notifier (§6.1). Declined again, on purpose: the
  Claude-first default settings. A Codex-only Mac has the same default today,
  so it is not a Linux gap, and Phase 6's `swarm settings set` gives Linux
  users the way to change it. The plan is final at this point; each phase's
  diff gets its own Codex review.

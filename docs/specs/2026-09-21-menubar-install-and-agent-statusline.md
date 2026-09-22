# Menubar install (blank notification icon) and agent status line off

## Context
Two small, unrelated fixes shipped together: **Part A** blank notification icon (Makefile), **Part B** hide the user's custom Claude status line in Swarm-spawned agent sessions (`internal/adapter/claude.go`).

### Part A: blank notification icon
- macOS banners from Swarm (e.g. "Couldn't deliver messages") show a blank/default icon.
- The daemon never posts banners. The menubar app does, via `UNUserNotificationCenter.add` (`apps/menubar/Sources/SwarmBar/SystemServices.swift:47`), bundle id `dev.swarm.menubar`.
- Bundle, `Swarm.icns` (`CFBundleIconFile`), ad-hoc signature and the LaunchServices record for `/Applications/Swarm.app` are all correct.
- Best-supported cause (the banner itself cannot be observed): `make install-app` (`Makefile:62-66`) does `rm -rf /Applications/Swarm.app` then `cp -R` while the app is running. Live evidence: pid 67510 started 07:33, the bundle was rewritten 07:47, and the process is executing a deleted inode. The icon lookup for a live sender goes through that process, fails, and NotificationCenter caches the blank result.
- Affected: `Makefile` only. Collision warning: none.

### Part B: status line in agent sessions
- `~/.claude/settings.json` sets `statusLine` to `npx -y ccstatusline@latest` (refreshInterval 10). Every Swarm agent pane inherits it, drawing a row (`Sonnet 5 high | none | (no PR) | ~/GitHub/agent-swarm | ▓░░░… 143k/1.0M | Session … Weekly …`) and running `npx` every 10s per agent.
- Swarm already passes a per-launch `--settings` file (`internal/adapter/claude.go:25` `settingsJSON`, wired at `:56-69`). `--settings` outranks user settings.
- Verified live (2026-09-21, Claude Code 2.1.278, scratch tmux session, no prompts sent): `{"statusLine":{"type":"command","command":"true"}}` in `--settings` removes the row entirely; only the built-in `⏵⏵ bypass permissions on` footer remains. `{"statusLine":null}` is REJECTED (`Expected object, but received undefined`, a blocking settings-error dialog). `--setting-sources project,local` also removes it but drops the user's skills/model/effort defaults and is not used.
- Nothing in the repo parses the status line (grep of `*.go`, `*.md`, `*.sh` for statusline: no hits). `tui: fullscreen` in user settings is left alone: idle-prompt detection was written against that layout.
- Collision warning: `docs/specs/2026-09-21-message-delivery-reliability.md` also edits `internal/adapter/claude.go` and `claude_test.go` (`claudeIdle` regex, ~line 109). Different regions; rebase whichever lands second.

## Locked decisions
1. Never replace a bundle under a running process: `install-app` quits Swarm first (osascript quit, `pkill -x Swarm` fallback after a 5s wait) and `open`s the new bundle after `cp -R`.
2. Part B: blank the status line with a command that prints nothing (`true`), never `null`, never by editing the user's `~/.claude/settings.json`. Only `statusLine` is overridden; plugins, skills, hooks and `tui` are untouched (user call, 2026-09-21: "only UI plugins, not actual plugins").
3. Do NOT add (Part A): `killall usernoted`, `killall Dock`, `lsregister -f`, `Assets.car` / `CFBundleIconName`. Nothing measured points at them; LaunchServices already resolves the id to `/Applications/Swarm.app`.
4. Immediate user fix, run once: `pkill -x Swarm; sleep 1; open /Applications/Swarm.app`. Only if the next banner is still blank: `killall NotificationCenter` (relaunches itself).
5. Optional hygiene: `lsregister -u` the dead worktree copy `~/GitHub/agent-swarm--menubar-followups/apps/menubar/.build/Swarm.app` (its directory no longer exists).

## DB models / Screens
n/a.

## Model / API types
One new key in the map returned by `(*Claude).settingsJSON(s Spec) ([]byte, error)` (signature unchanged):
```go
"statusLine": map[string]any{"type": "command", "command": "true"},
```
Serialised: `"statusLine":{"command":"true","type":"command"}`. Applies to every Claude launch and resume (`Launch`, `Resume`), all roles.

## User-facing copy
n/a.

## File list
- Changed: `Makefile` (`install-app` recipe); `internal/adapter/claude.go` (`settingsJSON`); `internal/adapter/claude_test.go` (new test).
- Added: `scripts/check-app-fresh.sh` (proves the running process executes the on-disk binary).
- Reused unchanged: `apps/menubar/scripts/bundle.sh`, `Swarm.icns`.
- Deleted: none.

## Verification
1. `make -n install-app`: quit line precedes `rm -rf`, `open` line follows `cp -R`.
2. `make install-app` while Swarm is running, then `scripts/check-app-fresh.sh` exits 0 (running inode equals on-disk inode).
3. Trigger any notification (e.g. via the daemon); banner shows the purple three-node icon.
4. If still blank after the relaunch: next hypothesis is an icns-only bundle on macOS 26 needing `Assets.car` + `CFBundleIconName`. The advisor judged this unlikely; try it only then.

5. Part B: `go test ./internal/adapter/ -run 'ClaudeSettingsJSON' -v` passes; spawn one agent (`swarm` CLI spawn as usual), `tmux -L swarm capture-pane -p -t <session> | tail -4` shows only the `⏵⏵ bypass permissions on` footer, no `Sonnet 5 high | …` row; `ps aux | grep ccstatusline` shows no process for that agent; your own plain `claude` session still shows the status line.

## Explicitly out of scope
- Rebuilding the icon as an asset catalog, restarting `usernoted` / Dock, re-registering the bundle, changing notification content or categories, signing identity.
- Part B: disabling plugins, skills, hooks or `tui: fullscreen` in agent sessions; `--bare`; `--setting-sources`; `--disable-slash-commands` (it would disable the `swarm` skill); removing the user's own `statusLine`; the `advisorModel` leaking in from user settings ("Advisor Tool (experimental) is on" banner) — separate call.

## Implementation notes (2026-09-21, as built)
- Part A: `make install-app` now quits Swarm, replaces the bundle and reopens it; `scripts/check-app-fresh.sh` reported `STALE` before the relaunch and `fresh` after.
- Part B: `statusLine` = `{"type":"command","command":"true"}` added to `settingsJSON`; new sessions launched after the deploy have no user status line. Sessions already running keep the settings file they started with.
- The icon fix could not be confirmed by observing a banner; if the banner is still blank after the relaunch, the next hypothesis in the spec applies.

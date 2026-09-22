# Spec: Enable Mouse Mode in Swarm Tmux Server

## Context
When attaching to an agent tmux session (via `swarm attach <agent>` or via Ghostty / menubar), mouse wheel scrolling in `codex` and `agy` (and `cursor`) cycles through previous prompt history rather than scrolling the conversation scrollback buffer. This does not happen in `claude`.

## Root Cause
1. Swarm's `~/.swarm/tmux.conf` (`spawn.TmuxConf()`) does not set `mouse on`, so tmux defaults to `mouse off`.
2. Terminal emulators (Ghostty, iTerm2, Terminal.app) running in alternate screen mode with mouse tracking disabled translate mouse wheel events into Up Arrow (`\e[A`) and Down Arrow (`\e[B`) keys.
3. Codex, agy, and cursor-agent do not enable terminal mouse reporting (`mouse_any_flag: 0`), so they receive Up/Down arrow keys and cycle through prompt history.
4. Claude Code requests mouse reporting (`mouse_any_flag: 1`), so the terminal sends mouse events instead of arrow keys. Claude also explicitly hints: `tmux detected · scroll with PgUp/PgDn · or add 'set -g mouse on' to ~/.tmux.conf for wheel scroll`.

## Locked Decisions
- Add `set -g mouse on` to `spawn.TmuxConf()` in `internal/spawn/tmux.go`.
- The daemon already checks and rewrites `~/.swarm/tmux.conf` whenever `TmuxConf()` changes (`cmd/swarm/daemon.go:206`), so the change persists and propagates automatically.
- Running `tmux -L swarm set -g mouse on` immediately resolves the issue for live agent sessions without restarting the daemon or agents.

## Files
- `internal/spawn/tmux.go`: Add `set -g mouse on` to `TmuxConf()`.
- `internal/spawn/tmux_test.go`: Assert `set -g mouse on` is in `TmuxConf()`.

## Verification
- `go test ./internal/spawn/...`
- `go test ./cmd/swarm/...`
- Live inspection of `tmux -L swarm show-options -g mouse`.

# Implementation Plan: Enable Mouse Mode in Swarm Tmux Server

## Tasks
1. Update `internal/spawn/tmux_test.go` to assert `set -g mouse on` is in `TmuxConf()`.
2. Update `internal/spawn/tmux.go` `TmuxConf()` to include `set -g mouse on`.
3. Run `go test ./internal/spawn/...` and `go test ./cmd/swarm/...` to verify.

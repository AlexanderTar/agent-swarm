# Implementation Plan: Repository Scan Exclusions, Symlink Protection, and Code Signing

## Tasks
1. Update `internal/settings/settings_test.go` and `internal/settings/settings.go` for new `ScanExcludes` defaults (`~/Music`, `~/Pictures`, `~/Movies`).
2. Update `internal/repos/walk_test.go` and `internal/repos/walk.go` for built-in skips and symlink skip-order guard.
3. Update `Makefile` to auto-detect `Swarm Dev` code signing identity.
4. Update `README.md` with Code Signing and macOS permissions documentation.
5. Verify test suite (`go test ./internal/repos/... ./internal/settings/...`).

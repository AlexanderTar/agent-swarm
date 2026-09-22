# Spec: Repository Scan Exclusions, Symlink Protection, and Code Signing

## Context
When Swarm runs or redeploys, macOS displays repeated TCC permission prompts requesting access to Downloads, Documents, Music, Photos Library, and network drives.
There are two root causes:
1. `Makefile` uses ad-hoc code signing (`--sign "-"`), which binds macOS permissions to the ephemeral binary hash (`cdhash`). Every build produces a new hash, resetting all previous TCC approvals.
2. `internal/repos/walk.go` starts at `$HOME` and only skips `~/Library` and `~/.Trash` (with `~/Downloads` in default settings). It descends into `~/Music`, `~/Pictures` (Photos), and `~/Movies`. In addition, `walk.go` resolves symlinks (such as `~/Google Drive` or network mounts) before checking whether they are in the skip list.

## Locked Decisions
1. **Code signing**:
   - `Makefile` checks for the persistent local identity `Swarm Dev` using `security find-identity -v -p codesigning`. If found, `SWARM_SIGN_IDENTITY` defaults to `Swarm Dev`. If absent, it falls back to `-`.
   - `README.md` documents how to create and trust the `Swarm Dev` certificate locally so developers and users have persistent TCC approvals.
2. **Scan exclusions**:
   - `internal/repos/walk.go` hardcodes `Music`, `Pictures`, and `Movies` alongside `Library` and `.Trash` in its built-in `skip` set.
   - `internal/repos/walk.go` checks `skip[path]` and `skipNames` *before* evaluating symlinks (`d.Type()&fs.ModeSymlink != 0`), preventing symlinks inside or pointing to skipped/network directories from triggering TCC prompts or blocking filesystem operations.
   - `internal/settings/settings.go` includes `"~/Music"`, `"~/Pictures"`, and `"~/Movies"` in default `ScanExcludes`.
   - Live SQLite settings in `~/.swarm/swarm.db` are updated with the new exclusions.

## Files
- `Makefile`: Auto-detect `Swarm Dev` for `SWARM_SIGN_IDENTITY`.
- `README.md`: Add section on code signing and macOS permissions.
- `internal/repos/walk.go`: Built-in skip for `Music`, `Pictures`, `Movies`; check skip before symlink evaluation.
- `internal/repos/walk_test.go`: Tests for `Music`/`Pictures`/`Movies` skip and skipped symlinks.
- `internal/settings/settings.go`: Default `ScanExcludes` includes `Music`, `Pictures`, `Movies`.
- `internal/settings/settings_test.go`: Assert updated default `ScanExcludes`.

## Verification
- `go test ./internal/repos/...`
- `go test ./internal/settings/...`
- `make build` verifies `SWARM_SIGN_IDENTITY` resolves to `Swarm Dev`.

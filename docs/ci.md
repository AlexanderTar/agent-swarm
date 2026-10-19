# Continuous integration

`.github/workflows/ci.yml` runs every check the repo has on pull requests to `main`, on pushes to `main` and on manual dispatch (`workflow_dispatch`). `main` requires a pull request for everyone but the repository owner, so a change reaches `main` through a PR whose checks are green. The job names below are the required status checks: rename a job only together with the branch protection.

## What runs where

| Job (status check name) | Runner | Commands |
|---|---|---|
| Go format and vet | ubuntu | `make web-build`, `make fmt vet` |
| Go tests and coverage | macOS | `make web-build`, `brew install tmux gnupg`, `go test -race -timeout 30m ./...`, `scripts/cover.sh` |
| Web tests and typecheck | ubuntu | `pnpm typecheck`, `pnpm test` (vitest with coverage), in `web/` |
| Web e2e (Playwright, mock daemon) | ubuntu | `pnpm exec playwright install --with-deps chromium`, `pnpm e2e`, in `web/` |
| Skills mirror is in sync | ubuntu | `make skills-sync`, then `git diff --exit-code -- internal/install/skills` and no untracked files there |
| Menu bar app tests and coverage | macOS | `make test-menubar` (`swift build -c release`, `swift test`, `apps/menubar/scripts/cover.sh`) |
| End-to-end (fake adapter) | macOS | `make web-build`, then `scripts/e2e.sh` with `SWARM_E2E_PATH` set |

The workflow sets `GO=go`, because the Makefile defaults `GO` to `/opt/homebrew/bin/go`. `make build` and the install targets codesign and never run in CI. The shared web setup (Node 22, pnpm 10, frozen-lockfile install, cached pnpm store) is `.github/actions/setup-web`.

## Reproduce a job locally

Run the job's commands from the table at the repo root with `GO=go` (or your own Go) in the environment. Each one runs unchanged on a Mac with the toolchain from the README.

`make test-go` is the local superset of the two Go jobs: it runs vet, the gofmt check, the race tests and the coverage gate in one go. CI splits it into two jobs so they run in parallel; if you change `test-go` in the Makefile, change the workflow with it.

`web/biome.json` exists, but `@biomejs/biome` is not a dependency and the code is not biome-clean, so CI does not run biome.

## Traps

- **Go packages embed `web/dist`.** `go vet ./...` and `go test` fail to compile until `make web-build` has run. `web/dist` is git-ignored, so a fresh checkout always needs the build first.
- **`scripts/e2e.sh` pins `PATH`** (`/opt/homebrew/bin:/usr/bin:/bin`) so the daemon can never find a real agent CLI. A toolchain installed elsewhere (CI's `setup-go`) is not on that PATH; set `SWARM_E2E_PATH` to the full list, with the Go directory first: `SWARM_E2E_PATH="$(dirname "$(command -v go)"):/opt/homebrew/bin:/usr/bin:/bin" scripts/e2e.sh`.
- **The Go tests run on macOS, not Linux.** The daemon only targets macOS. In a Linux container (non-root user, no git identity) `TestSlowClientDoesNotBlockOthers` fails every time because it depends on macOS socket-buffer behaviour, and `TestRenameWindowSetsTheWindowName` fails unless `LANG` is a UTF-8 locale (tmux replaces emoji with `_` otherwise; the macOS jobs set `LANG=en_US.UTF-8`). Linux is fine for `gofmt` and `go vet` (`GOOS=linux go vet ./...` is clean). Running the suite as root also fails the permission tests (`TestTokenEmptyIsReplacedUnreadableFails`, `TestCreateSucceedsWhenTheExcludeIsUnwritable`, `TestReclaimCodexHomesLogsARemoveFailureAndKeepsSweeping`), because root ignores file modes.
- **The e2e job runs on macOS because it needs tmux 3.6+.** Observed with Debian's tmux 3.3a (Docker, `golang:1.27-bookworm`): the pause and crash scenarios (`TestScenario08`, `09`, `10`) fail, with "tmux: server exited unexpectedly" in the daemon log. Ubuntu's apt tmux is older than 3.6 as well.
- **e2e scenarios must follow the daemon's current contract.** They had drifted (print-then-ask approvals, finishing checkpoints, waiver text in gate errors) and failed on `main` before CI existed. When you change a daemon message or flow, run `scripts/e2e.sh` and update the scenarios in the same change. Scenarios that start several agents (`TestScenario08`) can time out on a heavily loaded machine; rerun them alone with `scripts/e2e.sh -run TestScenario08` before calling them broken. Do not write a scenario that depends on winning a race with the daemon's 5s reconcile tick (`TestScenario08b` did, and flaked): put the freeze in place first, then free the slot.
- **Registering fixture repos in e2e:** `h.finishRepo` renames the repo after the test, because the catalog is keyed by directory name and `t.TempDir()` leaves are all `001`.

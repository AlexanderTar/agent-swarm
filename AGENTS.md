# Testing and coverage (strict)

`make test-go` must pass before any work is done. It runs the Go tests and `scripts/cover.sh`, the coverage gate from spec §23.3.

The thresholds in `scripts/cover.sh` are 85% for `items`, `runtime`, `hook`, `mcpserver`, `worktree` and `migrate`, and 75% for every other `internal` package that has tests. Never lower, waive or bypass them: no editing the script, no excluding packages or files, no skipping the gate.

When a package falls below its threshold, add real behaviour tests that assert outcomes and error paths until it is back above it. Tests that only execute code without checking results do not count.

`make lint` must pass before you commit (gofmt, go vet, pinned staticcheck and Biome; see docs/ci.md, Lint). CI runs the same checks.

Adding or changing an agent kind: follow docs/adding-an-agent-kind.md

Running the `swarm` binary against a scratch `SWARM_HOME`, or writing a test near `install`, `uninstall`, `migrate` or `launchctl`: follow docs/isolated-runs.md. A scratch `SWARM_HOME` alone does not protect the live daemon (the launchd labels are fixed); use a fake `HOME`, a `launchctl` stub on `PATH`, and never `--live` without both. Tests inject `execx.Fake`; the real runners refuse `launchctl` under `go test`.

Briefs and launch commands have hard size limits (6000-character brief cap checked where a brief is written; tmux's ~16 KB command line, so the kickoff rides in a launch file, never argv or env). Never add your own length check or put kickoff text on the tmux command line: see section 8 of docs/adding-an-agent-kind.md, guarded by `go test ./internal/items/ ./internal/spawn/ ./internal/adapter/`.

# /api/state stays bounded

The menu bar refetches `GET /api/state` on every daemon event. Never add per-node DB work for finished agents, and prune before building nodes, not after. How the trim works, the caps, and how to measure the response (`curl -w` with `~/.swarm/run/daemon.port` and `daemon.token`): docs/api-state.md.

# Continuous integration

GitHub Actions runs every check on pull requests and pushes to `main` (`.github/workflows/ci.yml`); `main` requires a PR for everyone but the owner. What runs where, how to reproduce a job locally and the CI traps (tmux 3.6+, `SWARM_E2E_PATH`, embedded `web/dist`, plus the runner-only failures: OCR tests need `ocrCapture`/`ocrText` (dark, opaque, 2x), Playwright must wait for animations and renders, e2e scenarios leave no live agents behind): docs/ci.md. When you add a check, add it to the workflow and to that doc in the same change.

# Contributing

Agent Swarm is open source under Apache-2.0 (`LICENSE`, `NOTICE`). Follow CONTRIBUTING.md for every change: signed conventional commits with a scope (`fix(usage): …`), no agent attribution lines, one logical change per commit, and pull requests against `main` that are squash-merged. Vendored third-party files keep their upstream licence and record their source and changes (`internal/install/skills/vendor/README.md`, `assets/icons/LICENSES.md`).

When you hit a bug in Swarm itself while working here, report it with `swarm_report_bug`. `swarm bugs --issue <id>` files a stored report as a GitHub issue on this repository (ask the user first; `--dry-run` previews it). CONTRIBUTING.md has the opt-in Swarm instruction snippets that let other users' agents offer that command or turn fixes into pull requests.

# Testing and coverage (strict)

`make test-go` must pass before any work is done. It runs the Go tests and `scripts/cover.sh`, the coverage gate from spec §23.3.

The thresholds in `scripts/cover.sh` are 85% for `items`, `runtime`, `hook`, `mcpserver`, `worktree` and `migrate`, and 75% for every other `internal` package that has tests. Never lower, waive or bypass them: no editing the script, no excluding packages or files, no skipping the gate.

When a package falls below its threshold, add real behaviour tests that assert outcomes and error paths until it is back above it. Tests that only execute code without checking results do not count.

Adding or changing an agent kind: follow docs/adding-an-agent-kind.md

# Contributing

Agent Swarm is open source under Apache-2.0 (`LICENSE`, `NOTICE`). Follow CONTRIBUTING.md for every change: signed conventional commits with a scope (`fix(usage): …`), no agent attribution lines, one logical change per commit, and pull requests against `main` that are squash-merged. Vendored third-party files keep their upstream licence and record their source and changes (`internal/install/skills/vendor/README.md`, `assets/icons/LICENSES.md`).

When you hit a bug in Swarm itself while working here, report it with `swarm_report_bug`. `swarm bugs --issue <id>` files a stored report as a GitHub issue on this repository (ask the user first; `--dry-run` previews it). CONTRIBUTING.md has the opt-in Swarm instruction snippets that let other users' agents offer that command or turn fixes into pull requests.

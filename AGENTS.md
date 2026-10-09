# Testing and coverage (strict)

`make test-go` must pass before any work is done. It runs the Go tests and `scripts/cover.sh`, the coverage gate from spec §23.3.

The thresholds in `scripts/cover.sh` are 85% for `items`, `runtime`, `hook`, `mcpserver`, `worktree` and `migrate`, and 75% for every other `internal` package that has tests. Never lower, waive or bypass them: no editing the script, no excluding packages or files, no skipping the gate.

When a package falls below its threshold, add real behaviour tests that assert outcomes and error paths until it is back above it. Tests that only execute code without checking results do not count.

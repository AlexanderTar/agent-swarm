# Worktree reclaim

A squash already on the remote default branch counts as merged; a conflicting or differing merge-tree does not. Retained trees are re-checked by the reclaim pass, and build churn is discarded only after the root is done or cancelled. Rules and commands: docs/worktree-cleanup.md. A worktree folder with no `.git` is `not_a_worktree` (watchman-cookie-only ones are deleted); never read a git failure there as dirty (same doc).

Headless Chrome leaks a `code_sign_clone` copy of its app bundle (hundreds of thousands of inodes) unless it quits cleanly: an agent that starts Chrome keeps `--user-data-dir` inside its own scratchpad or work dir and closes it (CDP `Browser.close`, or SIGTERM and wait, never SIGKILL first) before `completed`/`handoff`; the reclaim passes kill Chrome profiles inside a reclaimed dir and sweep stale unheld clones. Cause, rules and detection: docs/worktree-cleanup.md, "Headless Chrome and `code_sign_clone` leaks".

# Testing and coverage (strict)

`make test-go` must pass before any work is done. It runs the Go tests and `scripts/cover.sh`, the coverage gate from spec §23.3.

The thresholds in `scripts/cover.sh` are 85% for `items`, `runtime`, `hook`, `mcpserver`, `worktree` and `migrate`, and 75% for every other `internal` package that has tests. Never lower, waive or bypass them: no editing the script, no excluding packages or files, no skipping the gate.

When a package falls below its threshold, add real behaviour tests that assert outcomes and error paths until it is back above it. Tests that only execute code without checking results do not count.

`make lint` must pass before you commit (gofmt, go vet, pinned staticcheck and Biome; see docs/ci.md, Lint). CI runs the same checks.

Adding or changing an agent kind: follow docs/adding-an-agent-kind.md

A grok launch that fails with "Couldn't start agent." usually means macOS `fseventsd` is overloaded (grok's startup waits on it for minutes), not a broken grok or config; the Swarm grok home must never self-update away from the probed build. Cause, detection (`grep '"connect finished"' ~/.swarm/grok/logs/unified.jsonl | tail -5`, `swarm doctor`) and the traps: docs/adding-an-agent-kind.md, the TASK-1438 grok notes.

A Claude agent stuck Running with "swarm MCP CONNECT_TIMEOUT" is usually Claude Code freezing at startup, not the shim or daemon: sessions get `MCP_TIMEOUT=600000`, and the daemon relays `mcp_down` to the parent for a Claude session that never listed tools. Cause, detection (`grep -h 'Connection timeout triggered' ~/Library/Caches/claude-cli-nodejs/-Users-*-swarm-work-<agent>/mcp-logs-swarm/*.jsonl`), workaround (`swarm_control retry`) and traps: docs/adding-an-agent-kind.md, the TASK-1458 notes.

A usage gate check (`Exhausted`) never stands alone: ask `Store.kindExhausted` (internal/runtime/usagelimit.go), which adds the pane-observed limit to the poller. Every kind declares its pane usage-limit line or a reason it has none: docs/adding-an-agent-kind.md section 5, guarded by `go test ./internal/runtime/ -run TestKindParity -count=1`.

Running the `swarm` binary against a scratch `SWARM_HOME`, or writing a test near `install`, `uninstall`, `migrate` or `launchctl`: follow docs/isolated-runs.md. A scratch `SWARM_HOME` alone does not protect the live daemon (the launchd labels are fixed); use a fake `HOME`, a `launchctl` stub on `PATH`, and never `--live` without both. Tests inject `execx.Fake`; the real runners refuse `launchctl` under `go test`.

Briefs and launch commands have hard size limits (6000-character brief cap checked where a brief is written; tmux's ~16 KB command line, so the kickoff rides in a launch file, never argv or env). Never add your own length check or put kickoff text on the tmux command line: see section 8 of docs/adding-an-agent-kind.md, guarded by `go test ./internal/items/ ./internal/spawn/ ./internal/adapter/`.

Queued agents and parked operations (capacity pauses, resumes, retries) start only from the reconcile tick (`EnforceCapacity`, `ResumeOperations`, `DrainQueue` in `Reconcile`, every 5 s), so a change that affects scheduling, such as `max_concurrent_agents`, must call `runtime.Store.Kick()` (coalescing; wakes `ReconcileLoop` now) from the code that saves it (`putSettings` in internal/httpapi/config.go). Never call `DrainQueue` or `ResumeOperations` from a request goroutine: `startQueued`'s claim has no `state='queued'` guard, so two concurrent drains can start the same agent twice. A test that needs to prove "starts without waiting for a tick" runs `ReconcileLoop` with an `After` that never fires (see `TestKickRunsAReconcileTickBeforeTheTimer`, `TestPutSettingsKicksReconcileWhenTheAgentLimitChanges`).

Swarm cancels a running turn only for a pending pause or handoff request (`IsUrgent`) to a busy worker whose hooks have been silent for 30 s, once per message, never for an idle worker, an open question, a permission dialog or a grok card, and grok gets one `C-c` only (a second quits it). When and how, per kind: docs/urgent-interrupt.md.

Delivery to a busy muse session (probed 2026-10-29, muse 1.4.14): a pasted line steers the running turn and is pasted within one paste delay, so `messages.created_at` to `delivered_at` is not delivery latency: `delivered_at` is the LAST delivery, overwritten whenever an unacked message comes back on a later sync or attempt (check `delivery_count`, and read the agent's own `~/.local/share/muse/sessions/.../session.jsonl` for when a notice reached the model). Never re-paste a batch while the pane still shows it as "Queued input" (`Store.noticeStillQueued`), and a paste's Enter can be swallowed, so `Store.resubmitIfHeld` re-sends it while the composer still holds the text. Muse is a hooked kind through settings-level hooks written into the per-launch `settings.json` (`museHookCommand` bakes the `SWARM_*` env into argv because the hook env has none); muse also fires them for its internal `submit_reminder_decision` tool with a child `session_id`, which `Muse.IgnoreHook` drops. Details and the probe recipe: docs/adding-an-agent-kind.md section 9, "From muse, mid-turn delivery and hooks".

Delivery to a busy cursor or agy session (probed 2026-10-29): never natively wake a busy agy (`Agy.Wake` starts a hidden parallel agent on the same conversation; `WakeDue` skips it while `BusyNow`), never emit `injectSteps` for agy PreToolUse (it fails the tool call), and a cursor `beforeSubmitPrompt` mid-tool is a queued follow-up, not a turn end. A cursor pane with a stranded follow-up (the "enter steer" box and no busy footer) reads non-idle and is submitted with a plain Enter when its composer is empty (never Up+Enter; the "↑ to edit" variant with a refilled composer gets no keys). Details and the research notes path: docs/adding-an-agent-kind.md section 9, "From cursor and Antigravity".

# /api/state stays bounded

The menu bar refetches `GET /api/state` on every daemon event. Never add per-node DB work for finished agents, and prune before building nodes, not after. How the trim works, the caps, and how to measure the response (`curl -w` with `~/.swarm/run/daemon.port` and `daemon.token`): docs/api-state.md.

# Continuous integration

GitHub Actions runs every check on pull requests and pushes to `main` (`.github/workflows/ci.yml`); `main` requires a PR for everyone but the owner. What runs where, how to reproduce a job locally and the CI traps (tmux 3.6+, `SWARM_E2E_PATH`, embedded `web/dist`, plus the runner-only failures: OCR tests need `ocrCapture`/`ocrText` (dark, opaque, 2x), Playwright must wait for animations and renders, e2e scenarios leave no live agents behind, menubar code sleeps only through `taskSleep(_:)`, never `Task.sleep(for:)`, installer tests stub every host-specific command and pass in the ubuntu Docker command, Vercel's ignoreCommand diffs `VERCEL_GIT_PREVIOUS_SHA..HEAD` and builds when that sha is unset or missing from the shallow clone, menubar centering tests poll the window position with a bounded timeout instead of a fixed wait): docs/ci.md. When you add a check, add it to the workflow and to that doc in the same change.

# Contributing

Swarm is open source under Apache-2.0 (`LICENSE`, `NOTICE`). Follow CONTRIBUTING.md for every change: signed conventional commits with a scope (`fix(usage): …`), no agent attribution lines, one logical change per commit, and pull requests against `main` that are squash-merged. Vendored third-party files keep their upstream licence and record their source and changes (`internal/install/skills/vendor/README.md`, `assets/icons/LICENSES.md`).

When you hit a bug in Swarm itself while working here, report it with `swarm_report_bug`. `swarm bugs --issue <id>` files a stored report as a GitHub issue on this repository (ask the user first; `--dry-run` previews it). CONTRIBUTING.md has the opt-in Swarm instruction snippets that let other users' agents offer that command or turn fixes into pull requests.

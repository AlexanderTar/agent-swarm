# Claude remediation follow-up — 2026-09-23 08:56 BST

Session `83e3eeed` progressed past research: spec + 6-task TDD plan committed on
branch `worktree-superpowers-remediation` (worktree `.claude/worktrees/superpowers-remediation`).
User said "Proceed"; implementation pending execution-method choice.

New findings vs the earlier 3-front summary:
- Delivery matrix: codex `setupEnv` carries zero state (worst case); agy fix misses plugin cache; cursor unverified. Fix = per-adapter parity + check the isolated path, not real home.
- Enforcement split: spawned agents gateable (close `POST /api/items` bypass at `checkpoint.go:395` via `ArtifactsFor`); interactive sessions need a Claude Code hook (logged as follow-up, not built).
- Their 6 tasks: codex carry-over, agy plugin cache, parity guard test, cursor verify, completed-gate, SKILL.md path fix.

CONFLICT — needs parent decision: their spec moves SKILL.md spike paths to `~/.swarm/specs|plans/`;
repo CLAUDE.md and our muse spec/plan (Task 9/R2, committed) say `docs/specs/` + `docs/plans/`.
Do not implement R2 until this is resolved. Overlap note: their parity-guard test must
account for muse (no isolated HOME by design).

RESOLVED — 2026-09-23 09:52 BST, by the parent (asked directly in session `83e3eeed`,
which had independently reached the same fork point earlier in the same conversation and
already gotten an explicit answer): `~/.swarm/specs|plans/` wins, not `docs/specs/`/`docs/plans/`.
Rationale given: matches the existing `~/.swarm/worktrees` centralization decision — one
consistent swarm-owned location outside any repo the orchestrator may not own, never
`docs/superpowers/` or `~/.superpowers/`. `worktree-superpowers-remediation` (Task 6,
commit `f082c0a`) already ships `skills/swarm-orchestrator/SKILL.md:45` this way, including
its `internal/install/skills` go:embed mirror, and is being merged to `main` now.
Task 9/R2: skip the `docs/specs/`+`docs/plans/` rewrite as planned — the target state it
should verify (not re-implement) is `~/.swarm/specs/<YYYY-MM-DD>-<slug>.md` /
`~/.swarm/plans/<YYYY-MM-DD>-<slug>.md`, "never write them into a repo," on both the skill
file and its embed mirror. If `main` doesn't already have that wording by the time R2 runs,
something regressed — grep for `~/.superpowers` or `docs/specs/<.*SPIKE-KEY` in
`skills/swarm-orchestrator/SKILL.md` first rather than assuming this note is stale.
R1/R3 overlap: R3 (`internal/runtime/text.go`'s kickoff MUST-mandate) is untouched by and
complementary to `worktree-superpowers-remediation`'s Task 5 (a hard DB-level gate on
`completed` requiring a registered plan/debug_report for epic/bug roots) — no conflict, both
land. R1's per-kind delivery probes for codex/agy should find them already fixed (Tasks 1-2)
once merged; only muse itself is new territory for R1.

MUSE NATIVE WAKE — spike result, 2026-09-23 10:35-10:55 BST (session `83e3eeed`, at the
user's direct request, separate from the R1/R2/R3 work above). The muse spec's own probe
(`docs/specs/2026-09-23-muse-first-class-agent.md:44-45`) found `muse session-message send`
fails with `external_agent_ingress_closed`, "no opt-in found," and concluded Wake stays
tmux-paste. There IS an opt-in — it was just incomplete. Extracted the real binary
(`~/.local/bin/muse` is a launcher wrapper; the actual ~275MB binary is
`~/.local/bin/muse-bin-1.3.0-R3401.1`) and its embedded skill docs via `strings`. Findings,
each empirically verified live (echo provider for the free ones, one real paid `meta`
provider turn at `--reasoning-effort minimal` for the last):

1. Four env gates are needed, not one: `MUSE_EXPERIMENTAL_MONITOR=on`,
   `MUSE_EXPERIMENTAL_LOCAL_SESSION_MESSAGING=1`,
   `MUSE_EXPERIMENTAL_EXTERNAL_AGENT_INGRESS=on`, `MUSE_EXPERIMENTAL_TAG=on`. Setting all
   four on the querying/sending process opens `session-message list` (confirmed: went from
   `external_agent_ingress_closed` to a real session listing, 49->52 skills loaded).
2. The TARGET session needs none of the four at launch — a session launched with zero flags
   still appeared in `list` once the querying side set all four. So this needs no change to
   how swarm launches muse (`Launch()`/`setupEnv` untouched).
3. `send` still fails with a DIFFERENT, deeper error: `sender_unverified` ("sender session
   context is missing"). Tested three escalating ways to satisfy it, all failed identically:
   (a) raw external shell with all four flags set; (b) the TUI's own `!` shell-escape from
   inside a live muse session (a genuine child process of that session, all four flags set);
   (c) a REAL agentic bash-tool call inside a real, paid `meta`-provider session (not the `!`
   escape — an actual LLM-issued tool call, confirmed via tmux capture showing the tool
   invocation and its output). All three: identical `sender_unverified`.
4. Conclusion: sender verification is NOT env-var claim, NOT process ancestry, NOT "ran via
   the real bash tool vs. a shell escape." It's something deeper in what the embedded skill
   docs call a coordinator/lane registration protocol (`start`/`arm`/`recover` verbs,
   `host-manager/scripts/lane_runtime.py`) that a bare `muse session-message send` invocation
   — however it's invoked — doesn't go through. Reaching it would mean implementing that
   protocol's client side, not just setting env vars.

**Verdict for R1/native-wake: not proven feasible.** `session-message`-based wake needs the
full lane/coordinator handshake, not just the four env gates — a materially bigger lift than
the spec's current framing suggests. The `muse serve` MSP path (`session/resume` -> `turn/start`,
stdio JSON-RPC, schema exportable via `muse schema generate-json-schema`) is a separate,
more promising fallback — a proper structured protocol instead of the TUI messaging feature
— but untested; flagging as a distinct future spike, not attempted here (the writer-lease
semantics of `session/resume` on an already-live TUI session are unknown and need their own
probe). If R1 lands on tmux-paste for muse for now, that's the right call given this.

# Runtime report followup: F7 / F12 / F13 (research only)

Date: 2026-09-26. Scope: the three 2026-09-24 investigation findings that
lacked sufficient reproduction. Read-only: no live agent was paused,
messaged, restarted or mutated; every test cited ran against temporary
databases. **A research task never ships behavior changes** — this record
adds no production code, and any runtime fix it motivates becomes its own
follow-up package.

Baseline: worktree `agent-swarm--agent-continuity-4`, branch
`feat/agent-continuity-4` at `3458e0b` (Batch 4 lifecycle guards; base
`6da51b8` which already holds Batch 2 preservation/recovery and the Batch 4
read/relay remainder commits `d7789d7`, `80b4b74`, `dbabe0b`).

## F7 — request counts and terminal-answer delivery

Baseline: code at `3458e0b`; incident text is the 2026-09-24 investigation
§F7 (request reads by req ID unsupported; orphan sweep withdraws on session
death; both cited records now `answered`; item-list counts vs terminal
answers need reproduction).

Reproduction (code trace + fresh isolated tests, 2026-09-26):

1. Req-ID reads: `internal/mcpserver/tools.go:441` routes `req_` refs
   through `RequestByID` and reports unresolvable refs as per-ref `errors`
   entries without failing the call (Batch 4 commit `80b4b74`). Evidence:
   `go test ./internal/mcpserver/ -run TestReadMixedRefsReturnsKnownResults
   -count=1` → PASS.
2. Counts: `internal/items/store.go:681` (`enrich`) fills `OpenRequests`;
   `internal/httpapi/runtime.go:676` (`openRequestsWire`) lists them via
   the open-state filter; `internal/runtime/requests.go:170` (`Requests`)
   selects `state = 'open'`.
3. Terminal answers: `ResolveAnsweredInTerminal` (question/blocker rows
   resolved after a human-typed prompt), `ResolvePrompt` for terminal
   prompts, and the `Answer`/`Approve`/`RequestChanges` user actions.
   `withdrawOrphanedRequests` (`internal/runtime/reconcile.go:240`)
   withdraws open HITL rows only when the owning agent is finished or its
   newest session is terminal, and skips rows under an in-flight
   replacement operation; paused/interrupted agents keep their rows for
   delivery on resume. Evidence:
   `go test ./internal/runtime/ -run
   'TestUnackedMessageEscalatesAtThirdDelivery|TestBlockedCheckpointOpensHITLRequest'
   -count=1` → PASS (request opening mechanics).

Result: "vanish is not physical deletion" holds in code — withdrawal is a
state change with IDs retained, and req-ID readability now exists where the
investigation found "unsupported". Still open: exact count semantics
(direct-item vs root aggregate) and terminal-answer delivery timing were
not reproduced here.

Disposition: follow-up package F7-FU (isolated fake-provider root with a
child assignment: compare item detail/list/open-request counts
before/after answer, then session replacement; trace the request ID
through hook, DB response and inbox). Keep content-bound approval
validation. No code changed by this record.

## F12 — startup unacked timing

Baseline: code at `3458e0b`; incident text is the 2026-09-24 investigation
§F12 (report only; retry may inherit old delivery counters/deadlines).

Reproduction (code trace + fresh isolated test, 2026-09-26): message
delivery state is keyed by agent, never by session or generation —
`internal/runtime/inbox.go:238` (`maxFullDeliveries = 3`),
`envelopes()` (`inbox.go:422`: `delivery_count + 1`, `delivered_at`
stamped per delivery), `staleUnackedFor`/`unackedFor` (agent-scoped
`WHERE to_agent_id = ?` with no session predicate), and
`escalateUnacked` firing at the third delivery. `startSession`
(`internal/runtime/agents.go:1092`) inserts the sessions row, token,
cwd and launch only — it never resets `delivery_count`/`delivered_at`.
So a Retry or successor generation inherits the predecessor's counters:
a message delivered twice to the dead session reaches the successor with
`delivery_count = 2`. Note the stale comment on `RecoveryBundle`
(`internal/runtime/recovery.go:37-39`) describing a "reset
per-generation delivery eligibility on startSession" — no such reset
exists in `startSession`; eligibility is per-agent in the queries above.
Evidence: `go test ./internal/runtime/ -run
TestUnackedMessageEscalatesAtThirdDelivery -count=1` → PASS (single
generation escalation mechanics).

Result: the inheritance mechanism is confirmed in code. The defect itself
— a relay firing before the successor's first sync gets a chance — is
NOT reproduced: that needs a fake-clock timeline (enqueue, failed
attempt, Retry, provider startup, first sync, ack, relay) with message
IDs and generation stamps. No timer change was made, per the "defer any
timer change until reproduced" rule.

Disposition: follow-up package F12-FU (isolated fake-clock probe
`F12RetryAckTimeline` on the `clockStore` harness; if it reproduces,
propose generation-scoped delivery grace without suppressing genuine
stuck-recipient escalation, keeping acked rows acked and IDs stable).
No code changed by this record.

## F13 — provider/mode advisor matrix

Baseline: code at `3458e0b`; incident text is the 2026-09-24 investigation
§F13 (server advertises `swarm_advise` for simulated mode; Claude adapter
configures a native advisor model; tool availability in real provider
sessions unproven).

Reproduction (config/code inspection only — no provider calls,
2026-09-26): `Mode` (`internal/advisor/mode.go:11`): advisor model `""`
or `"none"` → no advisor; Claude session + Claude advisor + catalog
`AdvisorCapable` → `"native"`; every other advisor-bearing combination →
`"simulated"`. Capability comes from the catalog family allowlist
(`internal/catalog/parse.go`, haiku excluded) via `resolveAdvisor`
(`internal/runtime/agents.go:704`). The `swarm_advise` MCP tool is
advertised only when `Caller.AdvisorMode == "simulated"`
(`internal/mcpserver/server.go:76`); a nil `Advisor` service answers
with an explicit "no advisor is configured for this agent" error
(`internal/mcpserver/tools.go:903`). The native path is provider config
only: `advisorModel` is set solely by the Claude adapter
(`internal/adapter/claude.go:45`); the codex/cursor/muse/agy adapters
carry no advisor model. Evidence: `go test ./internal/advisor/ -count=1`
→ ok (`TestMode`, transcript/parse tests).

Result (matrix, from code — provider-session tool presence unproven):

| Session kind | Advisor model | Catalog capable | Mode | Agent sees |
|---|---|---|---|---|
| Claude | Claude model | yes | native | provider-native consult; no `swarm_advise` tool |
| Claude | Claude model | no (e.g. haiku) | simulated | `swarm_advise` |
| non-Claude | any model | — | simulated | `swarm_advise` |
| any | none / "" | — | none | explicit "no advisor is configured" error |

Disposition: follow-up package F13-FU (isolated-assignment smoke per
kind × mode recording stored advisor metadata, `tools/list` output and
provider capability; add an explicit missing-capability diagnostic where
warranted; never present a simulated or unconsulted answer as a consulted
one). No code changed by this record.

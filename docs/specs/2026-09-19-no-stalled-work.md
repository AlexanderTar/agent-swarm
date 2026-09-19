# No stalled work: three reliability fixes for the runtime monitor loop

## Context

Three separate incidents hit the same class of problem today, live, while
running real agents against this daemon: work silently stalls with no signal
either the daemon or the orchestrator can act on.

1. `s0-review-2` (a reviewer agent) stalled on Claude Code's trust-folder
   dialog during startup. `watchStartup` (`internal/runtime/agents.go`)
   correctly timed it out and called `failSession`, but the diagnostic text
   that would explain *why* — the actual pane content at the moment of
   failure — was captured and then thrown away before anyone could read it.
2. `s0-1-fixes`/`s0-review-3` repeatedly showed `session.waiting: true` in
   the menubar app (a hollow status dot, "Waiting" label) while genuinely
   mid-task with an active spinner. Root-caused live: the busy spinner line
   renders with a leading ANSI SGR color escape
   (`"\x1b[38;5;174m·Hyperspacing…"`), and the `Busy()` regex in
   `internal/adapter` is anchored at line-start with no ANSI awareness, so it
   never matched. **Shipped today, commit `c9a5f88` on `main`.** Included
   here because it's part of the same "the monitor loop must not report a
   healthy agent as stalled" problem as items 2 and 3 below, and because the
   fix pattern (strip ANSI before the regex that decides liveness) is the
   template item 3's dialog/stall detection already uses and this document
   is the record of that whole incident cluster.
3. The swarm-orchestrator skill tells orchestrators "Don't poll. End your
   turn when waiting; the daemon wakes you" (`skills/swarm-orchestrator/
   SKILL.md`). A child that crashes or hangs before it ever sends its first
   checkpoint leaves the orchestrator waiting on a wake event that will
   never come, with nothing telling it to check.

Affected repos/worktrees: `internal/runtime`, `internal/httpapi`,
`internal/db/schema`, `skills/swarm-orchestrator`, all in the primary
checkout (`/Users/alexandertar/GitHub/agent-swarm`, branch `main`). Items 1
and 3 are dispatched to two other agents working in parallel, in their own
worktrees:

- Item 1: `../agent-swarm--failure-text`, branch `feat/session-failure-text`.
- Item 3: `../agent-swarm--ack-timeout`, branch `feat/orchestrator-ack-timeout`.

Both of those implementers touch `internal/runtime/agents.go` and
`internal/runtime/reconcile.go`; each should expect the other's incoming
merge to touch nearby but non-overlapping code (item 1 is inside
`failSession`, item 3 is inside `resolveAlive`/`Reconcile`'s per-tick loop and
a new sibling function) and resolve any line-shift conflicts at merge time
rather than treating them as a design collision.

## Locked decisions

1. **Item 2 (waiting-flag ANSI bug) is done, not proposed.** No further
   decision needed; recorded here for the incident-cluster history. See
   commit `c9a5f88`: `internal/adapter/adapter.go` (`idle()` now strips ANSI
   via new exported `adapter.StripANSI` before the `Busy()` check only —
   `IdlePrompt()` deliberately still sees the raw capture, because it relies
   on a dim placeholder's own escape codes to tell an empty prompt apart from
   real typed input; stripping first would erase that distinction),
   `internal/adapter/claude_test.go` (`TestClaudeIdleAgainstTheCapturedPanes`
   gained a case for it), `internal/adapter/testdata/claude/
   pane-busy-ansi.txt` (new fixture, captured from the real `s0-review-3`
   pane with `tmux capture-pane -e`), `internal/runtime/agents.go` (its
   private `stripANSI` now forwards to `adapter.StripANSI` instead of
   keeping its own copy of the regex).
2. **Item 1: persist the full failure pane text on the session row itself**,
   not just in a notification argument the current notify template
   discards. A new nullable `failure_text` column on `sessions`, written by
   `failSession` in the same transaction that already writes the
   `agent.preflight_failed` notification and the parent relay (added by the
   unrelated `5675a14` fix earlier today), not a second round-trip.
3. **Item 1 does not change the notification body.** `notifyrules.go`'s
   `agent.preflight_failed` rule keeps rendering `{reason}` (first line)
   only — that's still the right short-form summary for a notification
   banner. `failure_text` is for someone actively debugging a stalled
   session to pull up on demand, not for the notification stream.
4. **Item 3: the daemon pushes a timeout event; the orchestrator does not
   self-schedule a wake.** Chosen over the alternative (the orchestrator
   asking the daemon to wake it at a fixed time regardless of other events)
   because it needs no new "wake me at time T" primitive and mirrors a
   mechanism that already exists (`agent.stale` in `internal/runtime/
   reconcile.go`: `staleAfter` constant, checked inside `resolveAlive`).
   User-approved explicitly over the self-schedule alternative.
5. **Item 3's timeout is keyed on "no checkpoint yet since this attempt
   started," not on general inactivity.** `agent.stale` already covers
   "was active, has gone quiet" for a session with a checkpoint history;
   this is the gap where the daemon has never heard from the child at all
   since `startSession`. Default timeout: 2 minutes (the user's explicit
   number), a new constant alongside `staleAfter`, not derived from it.
6. **Item 3's exact checkpoint-kind condition and dedup mechanism are left
   to that item's implementer**, who resolves them by reading
   `internal/runtime/checkpoint.go`'s actual `CheckpointKind` handling and
   `internal/runtime/reconcile.go`'s existing dedup pattern for
   `agent.stale`/`tmux.unknown` (the `notify: deduped ...: within 30s` log
   line seen in production today), and documents the resolved decision in
   their own commit message rather than this spec guessing it. This spec
   fixes the *shape* of the fix (daemon-pushed relay, mirroring the
   crashed/interrupted pattern) and the *timeout value* (2 minutes); it does
   not fix the SQL predicate.
7. **Item 3 requires a `skills/swarm-orchestrator/SKILL.md` update** telling
   the orchestrator what to do when it receives the new relay event: inspect
   the child's session state (`swarm_read`) and retry if failed/crashed —
   the same shape as the file's existing line "Fixes after review:
   `swarm_control retry` with the findings as `note`." The implementer
   writes the exact wording; this spec locks the requirement, not the prose.

## DB models

`internal/db/schema/0001_init.sql`, `sessions` table (this project has no
incremental migration chain for its own v2+ schema — see `internal/migrate`,
which only imports the legacy v1 format — so schema changes are made
directly in this one file):

```sql
CREATE TABLE sessions (
  ...
  needs_compaction_notice INTEGER NOT NULL DEFAULT 0,
  failure_text        TEXT,   -- NEW: full pane text at the moment failSession fired; NULL for every non-failed session and for every session that failed before this column existed
  last_seen_at        INTEGER,
  ...
);
```

Placed after `needs_compaction_notice` (the last of the existing nullable
per-session diagnostic-ish columns) purely for readability; SQLite has no
column-order constraint that matters here.

## Model / API types (exact)

`internal/runtime/model.go`, `Session` struct gains one field:

```go
type Session struct {
	ID, AgentID                            string
	Attempt, Generation                    int
	ProviderSessionID, TokenHash, TmuxName string
	Cwd, CwdKind                           string
	State                                  SessionState
	Waiting                                bool
	PauseScope                             string
	PauseRoot                              bool
	PauseDeadlineAt                        *time.Time
	StopBlocks                             int
	NeedsCompactionNotice                  bool
	LastSeenAt, LastWakeAt                 *time.Time
	ExitCode                               *int
	FailureText                            *string // NEW
	StartedAt                              time.Time
	EndedAt                                *time.Time
}
```

`internal/runtime/agents.go`, `LatestSession` (the one function that builds a
full `Session` for external consumption — `httpapi.sessionInfoOut`'s only
data source) gains the column in its `SELECT`/`Scan`:

```go
err := s.DB.QueryRowContext(ctx, `SELECT
    id, agent_id, attempt, generation, COALESCE(provider_session_id, ''), token_hash, tmux_name,
    cwd, cwd_kind, state, waiting, COALESCE(pause_scope, ''), pause_root, pause_deadline_at, stop_blocks,
    needs_compaction_notice, last_seen_at, last_wake_at, exit_code, failure_text, started_at, ended_at
    FROM sessions WHERE agent_id = ? ORDER BY generation DESC, attempt DESC LIMIT 1`, agentID).Scan(
    &ses.ID, &ses.AgentID, &ses.Attempt, &ses.Generation, &ses.ProviderSessionID, &ses.TokenHash,
    &ses.TmuxName, &ses.Cwd, &ses.CwdKind, &st, &waiting, &ses.PauseScope, &pauseRoot, &pauseDeadline,
    &ses.StopBlocks, &needsCompaction, &lastSeen, &lastWake, &exitCode, &failureText, &started, &ended,
)
// failureText is a new sql.NullString; ses.FailureText = &failureText.String when .Valid, same pattern as exitCode above it.
```

Other `FROM sessions` scan sites (`internal/runtime/inbox.go`,
`reconcile.go`, `pause.go`, `wake.go`) select narrower column sets for their
own internal bookkeeping and never build a full `Session` for external
exposure — none of them need `failure_text` and none change.

`internal/httpapi/runtime.go`, `sessionInfoWire` gains the wire field and
`sessionInfoOut` populates it:

```go
type sessionInfoWire struct {
	ID           string  `json:"id"`
	State        string  `json:"state"`
	Attempt      int     `json:"attempt"`
	Generation   int     `json:"generation"`
	Waiting      bool    `json:"waiting"`
	Stale        bool    `json:"stale"`
	TmuxAlive    bool    `json:"tmux_alive"`
	FailureText  *string `json:"failure_text"` // NEW; nil unless State == "failed" and this attempt actually recorded one
	StartedAt    int64   `json:"started_at"`
	EndedAt      *int64  `json:"ended_at"`
}
```

```go
func (s *Server) sessionInfoOut(ctx context.Context, ses runtime.Session, live map[string]bool) sessionInfoWire {
	w := sessionInfoWire{ID: ses.ID, State: string(ses.State), Attempt: ses.Attempt, Generation: ses.Generation,
		Waiting: ses.Waiting, TmuxAlive: live[ses.TmuxName], FailureText: ses.FailureText,
		StartedAt: db.Millis(ses.StartedAt), EndedAt: optMs(ses.EndedAt)}
	...
}
```

No new HTTP route: this rides the existing `GET /api/agents` /
`GET /api/agents/{name}` tree response every agent-list consumer (menubar,
web) already fetches, exactly like every other `sessionInfoWire` field.

`internal/runtime/agents.go`, `failSession` gains one write inside its
existing transaction (the one `5675a14` added for the notify+relay; no
second transaction):

```go
func (s *Store) failSession(ctx context.Context, a Agent, ses Session, paneText string) error {
	if err := s.SetSessionState(ctx, ses.ID, Failed); err != nil {
		return err
	}
	...
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET failure_text = ? WHERE id = ?`, paneText, ses.ID); err != nil {
			return err
		}
		if err := s.notify(ctx, tx, NotifyInput{ /* unchanged */ }); err != nil {
			return err
		}
		...
	})
}
```

`internal/runtime/reconcile.go`, item 3's new timeout constant sits beside
the existing one:

```go
const staleAfter = 30 * time.Minute
const ackTimeout = 2 * time.Minute // NEW: no checkpoint since this attempt's startSession
```

The exact predicate that reads `ackTimeout` (which of `resolveAlive`'s
existing queries it joins into, and the dedup key it uses) is Locked
Decision 6 — left to `feat/orchestrator-ack-timeout`'s implementer.

## File list

**Item 1** (`feat/session-failure-text`, in progress elsewhere):
- `internal/db/schema/0001_init.sql` — add `failure_text` column
- `internal/runtime/model.go` — `Session.FailureText`
- `internal/runtime/agents.go` — `LatestSession` scan, `failSession` write
- `internal/runtime/agents_test.go` — extend `TestFailSessionRelaysToParent`
  (added by `5675a14` today) to assert `failure_text` persisted, or a new
  adjacent test if the implementer judges that clearer
- `internal/httpapi/runtime.go` — `sessionInfoWire`, `sessionInfoOut`
- `internal/httpapi/runtime_test.go` (or wherever `agentNodeOut`/
  `sessionInfoOut` are already tested) — wire-shape assertion

**Item 2** (done, `c9a5f88`):
- `internal/adapter/adapter.go`, `internal/adapter/claude_test.go`,
  `internal/adapter/testdata/claude/pane-busy-ansi.txt`,
  `internal/runtime/agents.go`

**Item 3** (`feat/orchestrator-ack-timeout`, in progress elsewhere):
- `internal/runtime/reconcile.go` — `ackTimeout` constant, the predicate and
  notify+relay call (mirroring the `crashed` branch's shape)
- `internal/runtime/reconcile_test.go` — new test(s) for the timeout firing
  and for it *not* firing once a first checkpoint lands
- `internal/notifyrules/notifyrules.go` — one new rule for the new
  notification kind
- `skills/swarm-orchestrator/SKILL.md` — reaction guidance (Locked
  Decision 7)

**Explicitly not touched by any of the three:** `internal/mcpserver/*`,
`internal/usage/*`, `internal/usagegate/*`, `web/*`, `apps/menubar/*`.

## Verification

Commands (each item's own worktree runs its own subset; the coordinating
session runs the full set again after merging both):
```
go build ./...
go vet ./...
gofmt -l .
go test -race ./...
```

Scenarios:
1. Item 1: a session that hits `failSession` (e.g. the existing
   `TestFailSessionRelaysToParent` harness) has `failure_text` set to the
   exact pane text passed in; a session that completes normally has
   `failure_text` NULL/nil at every layer (DB, `Session`, wire JSON).
2. Item 2 (already verified): `TestClaudeIdleAgainstTheCapturedPanes` passes
   with the new `pane-busy-ansi.txt` case; full suite green post-merge
   (confirmed today).
3. Item 3: an agent spawned with a fake adapter/tmux that never produces a
   checkpoint gets the new relay message in its parent's inbox within (in
   test-clock time) `ackTimeout`; an agent that sends any checkpoint before
   `ackTimeout` elapses never gets one; an orchestrator with no parent
   (`ParentAgentID == ""`) doesn't panic or attempt a relay, mirroring the
   existing `crashed`/`interrupted` relay guards.
4. End-to-end, once both merge: `go test -race ./...` and
   `apps/menubar && swift test` both green with zero regressions, matching
   every other fix landed today in this same incident cluster.

## Explicitly out of scope

- **Network / swarm-infra-down handling.** No observed failure of this kind
  in any investigation today. Speculative design against a failure mode
  nobody has actually seen would be exactly the kind of unrequested
  robustness this project's own operating style argues against. Revisit if
  it actually happens.
- **The MCP stdio transport hang** (today's feedback draft `d0e5e67b`:
  `swarm_checkpoint`/`swarm_read`/`swarm_items` hung ~30 minutes despite the
  daemon's own HTTP port responding instantly throughout). This is a
  different subsystem — the MCP stdio bridge, not the reconcile/tmux
  monitoring loop this spec covers — and needs its own dedicated
  investigation into that transport, not a guess folded in here.
- **tmux-detected usage-limit screen → automatic fallback-agent trigger.**
  The ask: pattern-match a real Claude Code usage-limit screen in a
  captured pane, the same way a trust-folder dialog is matched, and invoke
  the already-merged usage-fallback mechanism (`internal/runtime/
  fallback.go`'s `resolveUsageFallback`, merged to `main` earlier today).
  Deferred because, unlike all three items above, nobody has captured what
  a real usage-limit screen actually looks like in a live pane, or
  confirmed whether it's a one-time `StartupDialogs`-shaped screen or
  something that can appear mid-session. That needs its own brainstorming
  pass anchored to a real captured example before anyone designs a
  detection regex against it — the exact mistake this spec's own item 2
  shows the cost of (a regex written against an assumption instead of a
  captured pane silently fails).

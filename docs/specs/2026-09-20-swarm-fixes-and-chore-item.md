# Swarm Fixes: Idle Notifications, TCC App Data Prompts, Quota Reset Ping, and Chore Items

## Context

Four issues and enhancements in Agent Swarm are addressed together in this specification:
1. **Idle notification spam**: Agents that hit rate limits sit quiet in tmux without checkpointing. After 30 minutes of silence, `s.Now().Sub(r.lastActivity()) >= staleAfter` triggers `agent.stale` in `internal/runtime/reconcile.go`. Because `notify.Raise` only deduplicates by `dedup_key` within a 30-second window (`dedupWindow = 30 * time.Second`), every 30 seconds thereafter a new `agent.stale` notification is written and broadcast over SSE, flooding the macOS Notification Center and menubar app.
2. **Repeated "“swarm” would like to access data from other apps" dialogs**: The Go CLI/daemon `swarm` is compiled via `go build` without an explicit code signing identity (`Identifier=a.out`, ad-hoc signed by the linker). When `swarm` spawns agent sessions in tmux (such as Claude Code `claude-code`), the child process executes under sandbox enforcement (`sandboxd`). When it accesses containers in `~/Library/Containers/`, macOS TCC enforces `kTCCServiceSystemPolicyAppData`. Because `swarm` is the responsible process in the process tree (`responsible_path=/Users/alexandertar/.swarm/bin/swarm`), macOS attributes the TCC request to `swarm`. Because `swarm` has `Identifier=a.out`, TCC treats each recompile or execution as transient, repeatedly prompting the user.
3. **Quota reset wakeup pings**: The daemon tracks usage reset times (`ResetsAt`) per agent in `internal/usage`. When an agent hits a rate limit and is blocked or idle, it stays dormant even after its quota reset window has passed. One minute after any agent's `ResetsAt` cutoff passes, the daemon should inspect all live and idle sessions for that agent kind and send a ping/wake to resume work.
4. **Top-level CHORE item**: Currently, top-level work items in Swarm are strictly `epic`, `bug`, and `spike`. Many engineering tasks (maintenance, refactoring, dependency updates, one-off chores) do not fit the formal "feature spike → epic → story → task" or "debug spike → bug → task" lifecycle. We introduce a top-level `chore` item type (`CHORE-xxx`), allow it to directly contain tasks, and make it the default type spawnable from the menubar app's "New orchestrator" window.

Affected repos/worktrees:
- Repository: `/Users/alexandertar/GitHub/agent-swarm`
- Files touched:
  - `internal/runtime/reconcile.go`, `internal/runtime/reconcile_test.go`
  - `Makefile`
  - `internal/usage/usage.go`, `internal/usage/poller_test.go`, `internal/runtime/wake.go`, `internal/runtime/store.go`
  - `internal/items/model.go`, `internal/items/store.go`, `internal/items/store_test.go`
  - `internal/ids/ids.go`, `internal/ids/ids_test.go`
  - `internal/db/schema/0003_add_chore.sql`
  - `internal/runtime/materialize.go`, `internal/runtime/artifacts.go`
  - `apps/menubar/Sources/SwarmBarKit/Wire.swift`
  - `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift`
  - `apps/menubar/Sources/SwarmBarKit/Copy.swift`
  - `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`
  - `apps/menubar/Tests/SwarmBarTests/NewOrchestratorFormTests.swift`

---

## Locked Decisions

1. **Idle notifications fire once per silence period**:
   - `reconcile.go` must check if an `agent.stale` notification has already been raised for this agent/item since `r.lastActivity()`.
   - If a notification already exists with `created_at >= r.lastActivity()`, suppress further notifications.
   - If new activity occurs (hook call, sync, checkpoint), `r.lastActivity()` updates. If the agent subsequently becomes quiet for another 30 minutes, exactly one new notification will fire.
2. **Stable code signing identity for `swarm` binary**:
   - `Makefile` and `install-daemon` must sign `bin/swarm` using:
     `codesign --force --sign "${SWARM_SIGN_IDENTITY:--}" --identifier dev.swarm.daemon bin/swarm`
   - This matches `apps/menubar/scripts/bundle.sh` (`dev.swarm.menubar`) and `internal/install/launchd.go` (`Label = "dev.swarm.daemon"`).
   - With a stable identifier (`dev.swarm.daemon`), macOS TCC binds permission to `identifier "dev.swarm.daemon"`, persisting user consent across rebuilds when `SWARM_SIGN_IDENTITY` is set, and preventing `a.out` churn.
3. **Usage cutoff wakeup schedule & debounce**:
   - The daemon's usage poller or runtime checks active reset times (`ResetsAt`).
   - When `now >= ResetsAt + 1*time.Minute`:
     - Pass the active `cutoff` timestamp to `WakeOnQuotaReset(ctx, kind, cutoff)`.
     - Filter sessions: `AND (ses.last_wake_at IS NULL OR ses.last_wake_at < cutoff)` so each cutoff cycle fires exactly once per session.
     - For each matching session:
       - If the agent is idle in tmux, paste the idle token (`IdleToken = "swarm: inbox (call swarm_sync)"`).
       - If native wake is supported (e.g. Claude MCP channel wake), call `ad.Wake`.
       - Record `last_wake_at = now` to debounce.
4. **Chore top-level item definition & migration safety**:
   - `Chore items.Type = "chore"` is added to `internal/items`.
   - Key prefix is `CHORE-` (e.g. `CHORE-1`, `CHORE-2`).
   - Allowed parent of `Task`: `allowedParents[Task] = []Type{Story, Bug, Spike, Chore}`.
   - `Chore` has no parent (`ParentKey == ""`), matching `Epic`, `Bug`, `Spike`.
   - Menubar app's "New orchestrator" defaults to `Chore`. The intent picker offers:
     - `Chore` (default)
     - `Feature spike`
     - `Debug spike`
   - A chore orchestrator is created via `POST /api/spikes` with `intent: "chore"`. When materialized, it produces a top-level `CHORE-xxx` item with child tasks.
   - **Migration safety**: `internal/db/db.go`'s `migrate` function must execute migrations on a dedicated connection with `PRAGMA foreign_keys = OFF` before the transaction, so table recreation migrations for `items` do not fail foreign key checks.

---

## DB Models & Migrations

### Migration `0003_add_chore.sql`

In SQLite, the `items` table has a `CHECK (type IN ('epic','story','task','bug','spike'))` constraint. In SQLite 3.37+, table recreation is required to alter a CHECK constraint. Because `sessions`, `requests`, `notifications`, `checkpoints`, and `item_deps` have foreign keys pointing to `items(id)`, `db.go` executes migrations with foreign keys temporarily disabled on the migration connection:

```sql
-- Migration 0003_add_chore.sql: Add 'chore' to items.type CHECK constraint

CREATE TABLE items_new (
  id                   TEXT PRIMARY KEY,
  key                  TEXT UNIQUE NOT NULL,
  type                 TEXT NOT NULL CHECK (type IN ('epic','story','task','bug','spike','chore')),
  parent_id            TEXT REFERENCES items(id),
  root_id              TEXT NOT NULL REFERENCES items(id),
  title                TEXT NOT NULL,
  brief                TEXT NOT NULL,
  acceptance_json      TEXT NOT NULL,
  status               TEXT NOT NULL CHECK (status IN ('draft','ready','in_progress','blocked','in_review','awaiting_approval','done','cancelled')),
  status_before_block  TEXT,
  priority             INTEGER NOT NULL,
  role_hint            TEXT,
  tdd_exempt           TEXT,
  confirmed_repos_json TEXT NOT NULL,
  repos_version        INTEGER NOT NULL,
  repo_hints_json      TEXT NOT NULL,
  suggested_repos_json TEXT NOT NULL,
  spike_intent         TEXT,
  origin_spike_id      TEXT REFERENCES items(id),
  legacy_key           TEXT,
  sort_order           INTEGER NOT NULL,
  revision             INTEGER NOT NULL,
  archived_at          INTEGER,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

INSERT INTO items_new SELECT * FROM items;

DROP TABLE items;
ALTER TABLE items_new RENAME TO items;

CREATE INDEX items_root ON items(root_id);
CREATE INDEX items_parent ON items(parent_id);
CREATE INDEX items_status ON items(status);
```

---

## Model / API Types

### 1. Go Runtime (`internal/items`, `internal/ids`, `internal/runtime`)

```go
// internal/items/model.go
const (
    Epic  Type = "epic"
    Story Type = "story"
    Task  Type = "task"
    Bug   Type = "bug"
    Spike Type = "spike"
    Chore Type = "chore"
)

// internal/ids/ids.go
var keyTypes = map[string]bool{
    "epic": true, "story": true, "task": true, "bug": true, "spike": true, "chore": true,
}

// internal/runtime/reconcile.go: Stale Notification Check
func (s *Store) alreadyNotifiedStale(ctx context.Context, agentID string, since time.Time) (bool, error) {
    var count int
    err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications
        WHERE agent_id = ? AND kind = 'agent.stale' AND created_at >= ?`,
        agentID, db.Millis(since)).Scan(&count)
    return count > 0, err
}
```

### 2. Quota Reset Wakeup (`internal/runtime/wake.go` / `internal/usage`)

```go
// internal/runtime/wake.go
// WakeOnQuotaReset wakes all live or waiting sessions belonging to kind that have
// not already been woken for this cutoff cycle (last_wake_at < cutoff).
func (s *Store) WakeOnQuotaReset(ctx context.Context, kind AgentKind, cutoff time.Time) (int, error)
```

### 3. Swift Menubar App (`Wire.swift`, `NewOrchestratorForm.swift`)

```swift
// apps/menubar/Sources/SwarmBarKit/Wire.swift
public enum SpikeIntent: String, Codable, Sendable, CaseIterable {
    case chore
    case feature
    case debug
}

// apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift
public final class NewOrchestratorForm {
    public var intent: SpikeIntent = .chore // Default is chore
    // ...
}
```

---

## Screens & UI Sketches

### Menubar App: New Orchestrator Window (`NewOrchestratorView.swift`)

```
+-------------------------------------------------------------+
| New orchestrator                                            |
|                                                             |
| Name                                                        |
| [ Clean up obsolete migrations                            ] |
| Agent name: clean-up-obsolete-migrations                    |
|                                                             |
| Intent                                                      |
| [ Chore (default) | Feature spike | Debug spike           ] |
| Creates an orchestrator to execute any maintenance,         |
| refactor, or general chore task.                            |
|                                                             |
| Repositories (optional)                                     |
| [ Search repositories...                                  ] |
| [x] agent-swarm                                             |
|                                                             |
| Agent                                                       |
| [ Claude        v ]  Model: [ Opus                      v ] |
|                                                             |
| Request (optional)                                          |
| +---------------------------------------------------------+ |
| | Remove old 0001 migration files after backup...         | |
| +---------------------------------------------------------+ |
|                                                             |
| ----------------------------------------------------------- |
|                                   [ Cancel ]  [ Start ]     |
+-------------------------------------------------------------+
```

---

## User-Facing Copy

### 1. Intent Captions (`Copy.swift`)
- `Copy.choreIntent`: `"Chore"`
- `Copy.choreCaption`: `"Creates a top-level chore orchestrator for maintenance, refactoring, or general work."`
- `Copy.featureSpike`: `"Feature spike"`
- `Copy.featureCaption`: `"Creates a spike to explore this request and turn it into an epic."`
- `Copy.debugSpike`: `"Debug spike"`
- `Copy.debugCaption`: `"Creates a spike to find the root cause and turn it into a bug with a fix plan."`

### 2. Notifications (`internal/notifyrules/notifyrules.go`)
- `item.created.chore`:
  - Title: `"Chore ready"`
  - Body: `"{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready."`
  - Category: `"swarm.item"`

---

## File List

### Modified Files
- `internal/runtime/reconcile.go`: Add `alreadyNotifiedStale` check to prevent repeated `agent.stale` notifications.
- `internal/runtime/reconcile_test.go`: Add test verifying `agent.stale` only fires once per 30m idle period.
- `Makefile`: Update `build` and `install-daemon` to run `codesign` with `--identifier dev.swarm.daemon`.
- `internal/ids/ids.go`: Add `"chore"` to `keyTypes`.
- `internal/ids/ids_test.go`: Add test for `CHORE-` key generation.
- `internal/items/model.go`: Add `Chore Type = "chore"`.
- `internal/items/store.go`: Allow `Chore` as top-level and as parent to `Task`.
- `internal/items/store_test.go`: Test chore creation and child tasks under chore.
- `internal/runtime/materialize.go`: Support `rootType = items.Chore`.
- `internal/runtime/artifacts.go`: Update `validateTreeShape` to allow `chore` root.
- `internal/runtime/wake.go`: Add `WakeOnQuotaReset` to ping sessions when quota reset cutoff has passed.
- `internal/usage/usage.go`: Expose nearest reset time cutoffs for agents.
- `cmd/swarm/daemon.go`: Wire periodic check for quota reset wakeup (every minute).
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`: Add `.chore` to `SpikeIntent`.
- `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift`: Set `.chore` as default intent.
- `apps/menubar/Sources/SwarmBarKit/Copy.swift`: Add chore copy strings.
- `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`: Add segmented picker option for Chore.

### New Files
- `internal/db/schema/0003_add_chore.sql`: Database migration adding `'chore'` to `items.type` CHECK constraint.

---

## Verification

1. **Idle Notification Test**:
   - `go test -run TestStaleAfterThirtyMinutesOfSilence internal/runtime/reconcile_test.go`
   - Advance time by 31m: verify 1 notification. Advance time by another 30m without activity: verify STILL 1 notification.
2. **Codesign Verification**:
   - `make build`
   - `codesign -dvvv bin/swarm`: verify `Identifier=dev.swarm.daemon`.
3. **Quota Reset Ping Test**:
   - Create mock live session in waiting/idle state for Claude.
   - Advance clock past `ResetsAt + 1*time.Minute`.
   - Trigger check: verify `ad.Wake` or `Tmux.PasteLine` is executed and session receives wake token.
4. **Chore Item & Menubar Test**:
   - Run `go test ./internal/items/... ./internal/ids/... ./internal/runtime/...`
   - Create item with type `items.Chore`: verify key is `CHORE-1`.
   - Run Swift tests in `apps/menubar`: `swift test`. Verify `NewOrchestratorForm` defaults to `.chore`.

---

## Explicitly Out of Scope

- Automatic quota resets for third-party tools without reset headers.
- Multi-tier chore hierarchy (e.g. sub-chores); chores contain tasks directly.
- Full Disk Access auto-granting (macOS SIP prevents programmatic granting; stable codesigning prevents repeated requests once granted).

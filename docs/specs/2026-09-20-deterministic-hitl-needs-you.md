# Deterministic HITL "Needs you" Specification

## Context
"Needs you" (in the Menubar popover and Web Board inbox) is currently polluted with routine workflow ceremony requests (`confirm_repos`, `approve_section` for every spec section, `approve_plan`, `approve_report`, `accept_epic`, `accept_fix`, `close_spike`). Meanwhile, genuine human-in-the-loop (HITL) blocker events are completely missing:
1. Native question tool calls (`ask_question` in Antigravity, `AskUserQuestion` in Claude, `request_user_input` in Codex, question tools in Cursor) run in terminal modals or prompts without opening a request in Swarm.
2. Terminal prompts (trust prompts, permission requests, auto/edit mode confirmations) that appear during normal `Running` execution are not detected because `watchStartup` only runs during `Spawning`. Sessions sit blocked on input until timing out after 5 minutes as `agent.stale`.
3. Checkpoints with `kind: "blocked"` and `blockers: [...]` transition items to `Blocked` but do not open an actionable request in "Needs you".

This specification defines a deterministic, strictly gated HITL system that reserves "Needs you" exclusively for events requiring human intervention: native questions, terminal prompts, and genuine blocker questions/logs.

## Locked Decisions
1. **Strict HITL Admission**: "Needs you" admits ONLY three request categories:
   - `question`: interactive questions from agents to user (intercepted native question tools, or explicit `swarm_ask kind: "question"`).
   - `prompt`: terminal prompts requiring button/key response or approval (trust prompts, tool permission prompts, auto/edit mode confirmations).
   - `blocker`: explicit blockers logged by an agent via MCP `swarm_blocker` or checkpoints with `kind: "blocked"` containing non-empty `blockers`.
2. **Workflow Ceremony Removed from "Needs you"**:
   - `confirm_repos`, `approve_section`, `approve_plan`, `approve_report`, `accept_epic`, `accept_fix`, `close_spike` are categorized as procedural reviews.
   - They do NOT count towards the "Needs you" badge count and do NOT appear in the "Needs you" inbox list unless the user explicitly filters by "Reviews" or "All" in the Web Board.
3. **Multi-Agent Interception Architecture**:
   - **Antigravity (`agy`)**: Intercept `ask_question` in `PreToolUse`.
   - **Claude (`claude`)**: Intercept `AskUserQuestion` in `PreToolUse`.
   - **Codex (`codex`)**: Intercept `request_user_input` and `experimental_request_user_input` in `PreToolUse`. Intercept `PermissionRequest` hook event.
   - **Cursor (`cursor`)**: Intercept `ask_question` in `PreToolUse`.
   - **All Agents (Live Prompt Detection)**: Runtime reconciler checks tmux capture against adapter `PromptPatterns()` while in `Running` state to catch interactive prompts that bypass hooks.
4. **Dedicated Blocker MCP Tool**: Add `swarm_blocker` MCP endpoint allowing any agent to deterministically log an actionable blocker.
5. **Deterministic Resolution**: Answering a question or resolving a prompt in Menubar or Board delivers the answer/keypress back to the agent session and clears the request immediately.

## DB Models & Schema Changes

### Migration: `0004_hitl_requests.sql`
```sql
-- Differentiate HITL requests from procedural reviews
ALTER TABLE requests ADD COLUMN is_hitl INTEGER NOT NULL DEFAULT 0;

-- Backfill existing questions and add index for fast HITL open queries
UPDATE requests SET is_hitl = 1 WHERE kind IN ('question', 'prompt', 'blocker');
CREATE INDEX requests_hitl_open ON requests(is_hitl, state, created_at);

-- Update requests table CHECK constraint to include 'prompt' and 'blocker' kinds
-- Note: SQLite table recreation pattern with PRAGMA foreign_keys = OFF
```

Schema definition for `requests`:
```sql
CREATE TABLE requests (
  id                TEXT PRIMARY KEY,
  kind              TEXT NOT NULL CHECK (kind IN
                      ('question','prompt','blocker','confirm_repos','approve_section',
                       'approve_plan','approve_report','accept_epic','accept_fix','close_spike')),
  is_hitl           INTEGER NOT NULL DEFAULT 0,
  agent_id          TEXT REFERENCES agents(id),
  session_id        TEXT REFERENCES sessions(id),
  item_id           TEXT NOT NULL REFERENCES items(id),
  artifact_id       TEXT REFERENCES artifacts(id),
  section_id        TEXT,
  section_sha256    TEXT,
  prompt            TEXT NOT NULL CHECK (length(prompt) <= 1000),
  options_json      TEXT NOT NULL DEFAULT '[]',
  state             TEXT NOT NULL CHECK (state IN ('open','approved','changes_requested','answered','withdrawn','stale')),
  confirmed_json    TEXT,
  artifact_revision INTEGER,
  binding_json      TEXT,
  response_text     TEXT,
  responded_via     TEXT CHECK (responded_via IN ('menubar','board','cli')),
  responded_at      INTEGER,
  created_at        INTEGER NOT NULL
);
```

## Model & API Types

### Go (`internal/runtime/requests.go`)
```go
type RequestKind string

const (
    KindQuestion      RequestKind = "question"
    KindPrompt        RequestKind = "prompt"
    KindBlocker       RequestKind = "blocker"
    KindConfirmRepos  RequestKind = "confirm_repos"
    KindApproveSection RequestKind = "approve_section"
    KindApprovePlan   RequestKind = "approve_plan"
    KindApproveReport RequestKind = "approve_report"
    KindAcceptEpic    RequestKind = "accept_epic"
    KindAcceptFix     RequestKind = "accept_fix"
    KindCloseSpike    RequestKind = "close_spike"
)

type RequestWire struct {
    ID               string          `json:"id"`
    Kind             RequestKind     `json:"kind"`
    IsHITL           bool            `json:"is_hitl"`
    AgentName        *string         `json:"agent_name"`
    ItemKey          string          `json:"item_key"`
    ItemTitle        string          `json:"item_title"`
    RootKey          string          `json:"root_key"`
    ArtifactID       *string         `json:"artifact_id"`
    ArtifactRevision *int            `json:"artifact_revision"`
    SectionID        *string         `json:"section_id"`
    SectionTitle     *string         `json:"section_title"`
    SectionSHA256    *string         `json:"section_sha256"`
    Prompt           string          `json:"prompt"`
    Options          json.RawMessage `json:"options"`
    State            RequestState    `json:"state"`
    Confirmed        []string        `json:"confirmed"`
    Binding          json.RawMessage `json:"binding"`
    ResponseText     *string         `json:"response_text"`
    RespondedVia     *string         `json:"responded_via"`
    RespondedAt      *int64          `json:"responded_at"`
    CreatedAt        int64           `json:"created_at"`
}
```

### Swift (`apps/menubar/Sources/SwarmBarKit/Wire.swift`)
```swift
public enum RequestKind: String, Codable, Sendable {
    case question, prompt, blocker
    case confirmRepos = "confirm_repos", approveSection = "approve_section"
    case approvePlan = "approve_plan", approveReport = "approve_report"
    case acceptEpic = "accept_epic", acceptFix = "accept_fix", closeSpike = "close_spike"
}

public struct SwarmRequest: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var kind: RequestKind
    public var isHITL: Bool
    public var agentName: String?
    public var itemKey: String
    public var itemTitle: String
    public var sectionTitle: String?
    public var prompt: String
    public var state: String
    public var createdAt: Timestamp
    public var proposedRepos: Int?
    public var options: [String]?
}
```

## Agent Interception Matrix

| Agent | Question Tool (`PreToolUse`) | Permission / Prompt Hook | Terminal Prompt Regex (`reconcile.go`) |
|---|---|---|---|
| **Antigravity** | `ask_question` | N/A | `Do you trust the contents of this project\?` |
| **Claude** | `AskUserQuestion` | `PreToolUse` (when tool requires approval) | `Is this a project you created or one you trust\?`, `I am using this for local development` |
| **Codex** | `request_user_input`, `experimental_request_user_input` | `PermissionRequest` hook event | `Do you trust the contents of this directory\?`, `Hooks can run outside the sandbox` |
| **Cursor** | `ask_question` | `PreToolUse` | `Allow / Deny`, `Do you want to continue\?` |

## MCP Tool Definition: `swarm_blocker`
```json
{
  "name": "swarm_blocker",
  "description": "Log a genuine blocker that requires user or orchestrator intervention to proceed.",
  "inputSchema": {
    "type": "object",
    "required": ["reason"],
    "properties": {
      "reason": {
        "type": "string",
        "description": "Clear explanation of what is blocking execution."
      },
      "options": {
        "type": "array",
        "items": {"type": "string"},
        "description": "Optional list of suggested decisions or unblocking actions."
      }
    }
  }
}
```

## Screens & UI Sketches

### Menubar Popover: "Needs you" Section
```
┌────────────────────────────────────────┐
│ Needs you                            1 │
│ ────────────────────────────────────── │
│ TASK-66 · Test-driven verification     │
│ [?] s4-red: Missing AWS credentials    │
│ [ Answer ]                [ Terminal ] │
│                                        │
│ Agents                               4 │
│ ...                                    │
└────────────────────────────────────────┘
```
- Only displays requests where `is_hitl == true` and `state == "open"`.
- Badge count equals `openHITLRequests.count`.
- Empty state: "Nothing needs your attention." (no badge when 0).

### Web Board: Inbox Panel
```
┌────────────────────────────┬──────────────────────────────────────────┐
│ Needs you                  │ TASK-66 · Test-driven verification      │
│ [ All | Questions | Prompts│ s4-red asked:                            │
│   | Blockers | Reviews ]   │ "Missing AWS credentials for S3 mock."   │
│                            │                                          │
│ ● TASK-66 · 2m             │ Suggested options:                       │
│   Missing AWS credentials  │ [1] Provide test credentials             │
│                            │ [2] Use local filesystem mock            │
│                            │                                          │
│                            │ Response:                                │
│                            │ [ Type answer here...                  ] │
│                            │ [ Send Answer ]           [ Terminal ]   │
└────────────────────────────┴──────────────────────────────────────────┘
```
- Default view shows HITL items.
- "Reviews" filter available for procedural workflow items (`confirm_repos`, `approve_section`, etc.).

## All User-Facing Copy
- `Copy.emptyNeedsYou`: `"Nothing needs your attention."`
- `Copy.hitlQuestion`: `"%@ asked: %@"`
- `Copy.hitlPrompt`: `"%@ is waiting for approval: %@"`
- `Copy.hitlBlocker`: `"%@ is blocked: %@"`
- `Copy.answer`: `"Answer"`
- `Copy.sendAnswer`: `"Send Answer"`
- `Copy.openTerminal`: `"Open Terminal"`
- Notification titles:
  - Question: `"Question from %@ (%@)"`
  - Prompt: `"Approval needed for %@ (%@)"`
  - Blocker: `"Blocker on %@ (%@)"`

## File List
### Modified Files
- `internal/db/schema/0001_init.sql` (and migration `0004_hitl_requests.sql`): Add `is_hitl`, update `CHECK (kind IN ...)`.
- `internal/runtime/requests.go`: Add `KindPrompt`, `KindBlocker`, `IsHITL` handling, and `AskBlocker`.
- `internal/runtime/checkpoint.go`: On `kind: "blocked"` with non-empty `blockers`, open HITL `blocker` request.
- `internal/runtime/reconcile.go`: Add live `PromptPatterns` check in `resolveAlive`.
- `internal/hook/handler.go`: Intercept `ask_question`, `AskUserQuestion`, `request_user_input`, `PermissionRequest`.
- `internal/adapter/adapter.go`, `claude.go`, `codex.go`, `cursor.go`, `agy.go`: Add `PromptPatterns() []PromptMatcher` to `Adapter` interface.
- `internal/mcpserver/tools.go`: Register `swarm_blocker`.
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`: Add `isHITL`, `options` to `SwarmRequest`.
- `apps/menubar/Sources/SwarmBarKit/AppModel.swift`: Filter `openRequests` on `isHITL == true`.
- `apps/menubar/Sources/SwarmBarUI/Popover/NeedsYouSection.swift`: Render HITL rows with question, prompt, or blocker styling.
- `web/src/types.ts`, `web/src/logic/inbox.ts`, `web/src/panels/NeedsYou.tsx`: Add `is_hitl` filtering.

## Verification
1. `go test -v ./internal/runtime/... -run "TestHITL.*"`
2. `go test -v ./internal/hook/... -run "TestQuestionInterception.*"`
3. `cd apps/menubar && swift test`
4. `cd web && pnpm test`
5. End-to-end scenario:
   - Agent invokes `ask_question` -> request created with `is_hitl=1`, `kind=question`.
   - "Needs you" shows badge `1` with question text.
   - User answers via menubar -> answer delivered via `s.Answer`, request marked `answered`, badge returns to 0.

## Explicitly Out of Scope
- Changing autonomous auto-approval logic for spec/plan reviews.
- Remote terminal emulation in browser (uses existing `openTerminal` tmux integration).

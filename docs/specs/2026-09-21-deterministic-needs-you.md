# Deterministic "Needs you" Specification

- **Date**: 2026-09-21 (revised the same day after two new user requirements, see section 1)
- **Status**: Draft. Five assumptions await user confirmation, each marked ASSUMPTION: prompt rows target the asking agent's terminal (decision 5); `swarm answer` CLI stays (decision 7e); any human prompt closes all of that agent's question/blocker rows (section 4.5); agy has no `prompt` payload (section 4.5); a web row click both selects and opens the terminal (section 6). The approvals-keep-Approve assumption is now locked (decision 6, user-confirmed 2026-09-21).
- **Repos / dirs**: `agent-swarm` (`internal/runtime`, `internal/hook`, `internal/adapter`, `internal/httpapi`, `internal/notifyrules`, `web/`, `apps/menubar/`, `skills/swarm`)
- **Companion plan**: `docs/plans/2026-09-21-deterministic-needs-you.md`
- **Tracking**: ad-hoc session. No Notion page, no Swarm board task (user, 2026-09-21).

---

## 1. Context

### The rule

"Needs you" must be 100% deterministic: no item from an agent's free text, a tmux scrape or an orchestrator summary. Every human-in-the-loop (HITL) loop goes through the orchestrator, and a row disappears from Swarm the moment the loop is resolved or its owner is gone.

### Two requirements added by the user on 2026-09-21

1. "Native question tools need to universally work for any agent we support." So native question tools stay enabled on every adapter. The earlier draft blocked them for everyone; that is dropped.
2. "We need to also remove 'Answer' button and input from the UI, it should only be information, clicking on it would route to a corresponding orchestrator tmux session for user's approval." So "Needs you" becomes read-only on the web board and in the menubar popover, and its macOS notifications. A click opens the orchestrator's terminal. The user answers there.

### What is broken (verified against code and `~/.swarm/swarm.db`, 2026-09-21)

The panel shows `requests` rows with `is_hitl = 1` and `kind IN (question, prompt, blocker)` (`web/src/logic/inbox.ts:5-14`, `apps/menubar/Sources/SwarmBarKit/AppModel.swift:288-292`; both default `is_hitl` from kind identically). Six defects put junk in that set or leave it there:

| # | Defect | Evidence |
|---|---|---|
| 1 | Nothing closes an open HITL row when its session ends. `resolveDead` terminal branches (`internal/runtime/reconcile.go`, `resolveDead` at line 366), `Cancel` (`internal/runtime/agents.go` ~1083-1089) and `SetSessionState` (`agents.go` ~1260-1273) never touch `requests`. `agents.state` is only set to `finished` on the `completed` branch, so it is not a usable "agent ended" predicate; `sessions.state` is. | Live: 2 open rows, both owned by `finished` agents. |
| 2 | Workers open rows directly. `swarm_ask` and `swarm_blocker` are in `sharedTools()` (`internal/mcpserver/server.go:86-89`) with no role check; `swarm_checkpoint kind:blocked` with `blockers[]` inserts an `is_hitl` blocker for every caller (`internal/runtime/checkpoint.go:376-380`); native question tools open a row from PreToolUse for every caller (`internal/hook/handler.go:428-437`). `skills/swarm/SKILL.md:17` says "ask parent first" only as prose. | STORY-23 was a worker's `swarm_blocker`. |
| 3 | Reconcile scrapes the tmux pane and creates `prompt` rows (`internal/runtime/reconcile.go:528-565`) from the same regexes `Claude.StartupDialogs()` already auto-answers (`internal/adapter/claude.go:122-133`, `watchStartup` at `agents.go:1020-1032`). A trust dialog that appears after startup handling ends becomes a row nobody can answer usefully. | STORY-22 "Trust this project". |
| 4 | A `PermissionRequest` prompt's text is the raw command, which matches no `PromptMatcher.Title`, so the pattern-vanish loop (`reconcile.go:565-593`) marks it not-active and resolves it `via=terminal` on the next tick while the terminal is still blocked. | `resolveAlive`. |
| 5 | PostToolUse resolves "the newest open `question` of this session" regardless of which question the finished tool asked (`handler.go:451-465`). A native answer can close a `swarm_ask` row. | `handler.go:457-461`. |
| 6 | The UI can "answer" a native-question row: `Answer` enqueues a `user_answer` message (`requests.go:669-678`) but the terminal dialog stays up, so the answer never reaches the tool. | `requests.go:669-678`. |

Defect 6 is removed by requirement 2 (no answer UI at all), not patched.

Also found: `ResolvePrompt` sends `action` to tmux as one key (`requests.go:755`, `s.Tmux.Keys(ctx, tmuxName, action)`) while `Claude.PromptPatterns()` uses `"Down+Enter"`. `tmux send-keys` has no key named `Down+Enter`. Both callers that pass an action (the menubar Approve button and the `/resolve` route) are deleted by this spec, so the branch is deleted with them. The auto-answer in the pane-scrape fix splits on `+`.

### Live cleanup already done (2026-09-21)

Set to `state='withdrawn'`, `responded_at` = now: `req_01M30AJV6K5F0A55QXXEGE0Z13` (STORY-22 trust prompt, agent `s2-review-2`) and `req_01M31BTW3YTWT2ZP96R4HDN97F` (STORY-23 blocker, agent `s3-fix-b-routes`). Open HITL rows now: 0. This spec makes sure it stays 0.

### Collision warnings

- `docs/specs/2026-09-21-claude-idle-wake-and-pause-reaper.md` (untracked, another agent) edits `internal/adapter/claude.go` (`claudeIdle`) and `internal/runtime/pause.go`. This spec touches `claude.go` only in `ParseHook` (one field). Different hunk.
- `docs/specs/2026-09-21-message-delivery-reliability.md` edits `internal/hook/handler.go:216` (`state <> 'acked'` counts) and `internal/adapter/claude.go`. `handler.go` is shared: keep the two diffs in different hunks. This spec adds a `parent_agent_id` column to the `load()` query in the same function; rebase on whichever lands first.
- `docs/specs/2026-09-21-menubar-install-and-agent-statusline.md` edits `claude.go` `settingsJSON`. Different hunk.
- The web and menubar UI files have no other open spec.

---

## 2. Locked decisions

1. **One creator.** A HITL row (`is_hitl=1`) is created only by (a) an agent with no parent (a top-level orchestrator or spike) through `swarm_ask kind:question`, `swarm_blocker`, `swarm_checkpoint kind:blocked`, or a native question tool, or (b) the daemon's `PermissionRequest` hook for a terminal permission dialog of any agent. Nothing else.
2. **Relay is enforced in code, not prose.** An agent with `ParentAgentID != ""` cannot open a question or blocker row.
   - `swarm_ask kind:question` and `swarm_blocker` are refused with the message in section 7.
   - `swarm_checkpoint kind:blocked` still succeeds (the checkpoint is a status write and already relays `blockers[]` to the parent as a `relay` message, `checkpoint.go:400-406`); it only skips the row insert.
   - A native question tool call from a parented agent is blocked in PreToolUse with the same instruction (section 5). Nothing is inserted.
3. **Native question tools stay enabled on every adapter for top-level agents** (user requirement 1). The PreToolUse hook opens a row (as today); PostToolUse closes it, matched on the question text, never on "newest".
4. **"Needs you" is information only** (user requirement 2). No Answer button, no answer input, no Approve button on HITL rows, on either surface or in a notification. Clicking a row opens the terminal of the target agent (decision 5). The user answers in that terminal.
5. **Terminal target, resolved once by the daemon** and sent to both clients as `RequestWire.terminal_agent`:
   - `question` and `blocker` rows: the root orchestrator of the asking agent's tree, found by following `parent_agent_id` to the agent whose parent is NULL. By decision 2 that is the asking agent itself; the walk also covers a legacy row from a parented agent.
   - `prompt` rows (a permission dialog): **the asking agent itself**, because the dialog lives in that agent's pane, not the orchestrator's. **ASSUMPTION – confirm with user:** this is the one exception to "orchestrator terminal". It is rare: Claude, Codex, Cursor and agy all launch with permission-bypass flags (`--dangerously-skip-permissions`, `--dangerously-bypass-approvals-and-sandbox`, `--yolo`), so only an adapter or mode that still prompts raises one.
   - Other kinds (approvals) get `terminal_agent = null`.
6. **Approval kinds are untouched** (user-confirmed 2026-09-21). `approve_section`, `approve_plan`, `approve_report`, `accept_epic`, `accept_fix`, `confirm_repos`, `close_spike` (all `is_hitl=0`, web "Approvals/Reviews" tabs, menubar Review button) keep their Approve / Request changes / Confirm / Close buttons and endpoints.
7. **A human answers by using the terminal; Swarm notices deterministically.** An open `question` or `blocker` row of a top-level agent closes on any of:
   - (a) PostToolUse of the matching native question tool (prompt text equal), `answered` via `terminal`;
   - (b) a human-typed prompt in that agent's terminal (`UserPromptSubmit` whose text is not daemon-originated, section 4.5), `answered` via `terminal`, response text `Answered in terminal`;
   - (c) the asking agent's `swarm_ask withdraw:<id>`, `withdrawn`;
   - (d) the session-end sweep (decision 8), `withdrawn`;
   - (e) the CLI `swarm answer REQ TEXT`, unchanged. **ASSUMPTION – confirm with user:** `swarm answer` and `POST /api/requests/{id}/answer` stay as a terminal-only power path (requirement 2 says "UI"). They enqueue the existing `user_answer` message, so that delivery code is not dead. If the user wants them gone too, delete `cmdAnswer`, the route, `Store.Answer` and the four Go tests listed in section 8.3.
8. **One backstop, not N call sites.** A single sweep in `Reconcile` withdraws every open HITL row whose owner is gone. It also heals rows left over from before this change and after a daemon restart. A `paused` or `interrupted` session keeps its rows: the answer is delivered as a message on resume. Only `completed`, `failed`, `crashed`, `cancelled` sessions, or `agents.state = 'finished'`, trigger the sweep.
9. **No schema change.** `requests.state` has a CHECK of `open, approved, changes_requested, answered, withdrawn, stale`; a new state needs a table rebuild. `withdrawn` already means "the asker went away" (`withdraw()` in `requests.go`), so the sweep uses it.
10. **The pane scrape never creates a row.** When a `PromptPattern` matches a live session, the daemon presses the matcher's keys once per (session, title) and logs it. The vanish-resolve loop is deleted.
11. **Permission rows resolve on evidence.** The next `PostToolUse` in that session whose command equals the row's prompt (or any `PostToolUse` if the command is empty) resolves it `via=terminal`. Pattern absence never resolves anything.
12. **Dead write paths are deleted with the UI that used them** (section 4.7): `POST /api/requests/{id}/resolve`, `ResolvePrompt`'s tmux `action` argument, web `api.answer` / `api.resolvePrompt`, menubar `answer` / `resolvePrompt` client methods, drafts and `answering` state, the Approve button for prompt rows, the notification text-input reply.
13. **Unresolved-by-design gap, stated not hidden.** If an adapter ignores a PreToolUse block on a question tool, a parented worker can sit in its own terminal with a dialog and no row. Its Agents-list row still shows the existing `waiting` flag. The sweep cannot help (no row). See the matrix in section 5.

---

## 3. DB models

**No schema change, no migration.**

### Sweep SQL (exact)

```sql
SELECT r.id
FROM requests r
JOIN agents a ON a.id = r.agent_id
WHERE r.state = 'open'
  AND r.is_hitl = 1
  AND (
    a.state = 'finished'
    OR (SELECT se.state FROM sessions se
        WHERE se.agent_id = r.agent_id
        ORDER BY se.generation DESC, se.attempt DESC LIMIT 1)
       IN ('completed', 'failed', 'crashed', 'cancelled')
  )
ORDER BY r.created_at;
```

"Latest session" uses the same order as `LatestSession` (`generation DESC, attempt DESC`, `agents.go:1288`). An agent whose newest attempt is running keeps its rows even if an older attempt crashed.

Each id then goes through `closeRequestTx` (same body as today's `withdraw()`):

```sql
UPDATE requests SET state = 'withdrawn', responded_at = ? WHERE id = ? AND state = 'open';
```

followed by `events.RequestResolved` with the request wire and `Items.ReconcileTx(itemKey)`. Index used: `requests_hitl_open(is_hitl, state, created_at)`. No new index.

### Terminal target SQL (exact), used by `RequestWireTx`

Root orchestrator of the asking agent (question and blocker rows):

```sql
WITH RECURSIVE up(name, parent) AS (
  SELECT name, parent_agent_id FROM agents WHERE id = ?
  UNION ALL
  SELECT a.name, a.parent_agent_id FROM agents a JOIN up ON a.id = up.parent
)
SELECT name FROM up WHERE parent IS NULL LIMIT 1;
```

The asking agent (prompt rows): `SELECT name FROM agents WHERE id = ?`. `agents_parent` index covers the walk; trees are a few levels deep.

### Human-prompt close SQL (exact)

```sql
SELECT id FROM requests
WHERE agent_id = ? AND is_hitl = 1 AND kind IN ('question', 'blocker') AND state = 'open';
```

Keyed by `agent_id`, not `session_id`: after a pause and resume the row belongs to the old session and the human types into the new one. `prompt` rows are excluded (a permission dialog is closed only by PostToolUse evidence, decision 11).

### One-off cleanup for legacy rows

None needed: the two live rows are already withdrawn and the sweep handles any future leftover on the first tick after deploy.

---

## 4. Model / API types

Package paths as in the repo. Signatures were read from the current code on 2026-09-21.

### 4.1 `internal/runtime/requests.go`

```go
// closeRequestTx withdraws one open request inside the caller's tx: UPDATE,
// request.resolved event, item reconcile. withdraw() and the sweep share it.
func (s *Store) closeRequestTx(ctx context.Context, tx *sql.Tx, reqID string) error

// errRelayToParent is the refusal a parented agent gets from swarm_ask
// (kind question) and swarm_blocker. "parent" is swarm_send's alias for the
// caller's parent (skills/swarm/SKILL.md rule 5), so no parent lookup is needed.
const errRelayToParent = "You report to an orchestrator, not the user. Send this to it with swarm_send (to: \"parent\", kind: \"question\") and keep working on anything you are not blocked on."

// requireTopLevel returns the refusal for an agent that has a parent, nil otherwise.
func requireTopLevel(a Agent) error

// ResolveSessionPrompts resolves open prompt requests of one session once a
// PostToolUse proves the permission dialog was answered. Rows whose prompt
// equals command, or the fallback "Permission requested", match;
// command == "" resolves all of the session's open prompts.
func (s *Store) ResolveSessionPrompts(ctx context.Context, sessionID, command string) error

// ResolveQuestionByPrompt closes the session's open native-question row whose
// prompt equals the one this tool call asked, answered via terminal. No match
// is not an error (the tool may have been blocked, or the row swept).
func (s *Store) ResolveQuestionByPrompt(ctx context.Context, sessionID, prompt, answer string) error

// ResolveAnsweredInTerminal closes every open question/blocker row of an agent
// after a human-typed prompt: answered, via terminal, "Answered in terminal".
func (s *Store) ResolveAnsweredInTerminal(ctx context.Context, agentID string) error
```

`askQuestion` and `AskBlocker` call `requireTopLevel(a)` right after `sessionAndAgent`. `AskPrompt` does not (decision 1b). `AskQuestion` (public wrapper, called by the hook intercept and `requests_test.go:543`) is unchanged and keeps the guard through `askQuestion`.

`RequestWire` (`requests.go:74`) gains one field:

```go
TerminalAgent *string `json:"terminal_agent"`
```

set in `RequestWireTx` after `AgentName`:

```go
// terminalAgent is the tmux session the user answers a HITL row in (decision 5).
// nil for non-HITL kinds and for a row with no agent.
func (s *Store) terminalAgent(ctx context.Context, tx *sql.Tx, r Request) *string
```

### 4.2 `internal/runtime/text.go`

```go
// IsDaemonPrompt reports whether a UserPromptSubmit text was written by the
// daemon, not typed by the user. Every daemon prompt is either the idle token
// (wake.go tryPaste) or carries ShortPreamble: Kickoff, ResumeKickoff,
// PendingNotice, ControlNotice and CompactionNotice all do (text.go).
func IsDaemonPrompt(prompt string) bool {
	p := strings.TrimSpace(prompt)
	return p == IdleToken || strings.Contains(p, ShortPreamble)
}
```

`Preamble` starts with `ShortPreamble`, so one `Contains` covers both.

### 4.3 `internal/runtime/reconcile.go`

```go
// withdrawOrphanedRequests is the sweep in section 3. Runs every Reconcile
// tick, before sweepFinishedRoots (which reads open requests in a tree).
func (s *Store) withdrawOrphanedRequests(ctx context.Context) error

// markPromptAnswered reports true the first time it is called for a
// (sessionID, title) pair, false afterwards. In-memory like lastAliveAt.
func (s *Store) markPromptAnswered(sessionID, title string) bool
```

`Store` (`internal/runtime/model.go`, next to `lastAliveAt`) gains `promptAnswered map[string]bool`, guarded by `bookkeepingMu`.

`resolveAlive`'s scrape block becomes:

```go
if !idle {
	for _, m := range ad.PromptPatterns() {
		if m.Match == nil || m.Action == "" || !m.Match.MatchString(capture) {
			continue
		}
		if s.markPromptAnswered(r.SessionID, m.Title) {
			if err := s.Tmux.Keys(ctx, r.TmuxName, strings.Split(m.Action, "+")...); err != nil {
				s.logf("reconcile: auto-answer %q for %s: %v", m.Title, r.SessionID, err)
			}
		}
		break
	}
}
```

The whole "Auto-resolve any open prompt request whose pattern is no longer present" block (`reconcile.go:565-593`) is deleted.

### 4.4 `internal/runtime/checkpoint.go`

```go
if in.Kind == BlockedCkp && len(in.Blockers) > 0 && a.ParentAgentID == "" {
```

The parent relay message below it is unchanged.

### 4.5 `internal/adapter` and `internal/hook`

`adapter.HookInput` (`internal/adapter/adapter.go:65`) gains one field:

```go
Prompt string // UserPromptSubmit text; empty for adapters that don't send it
```

`ParseHook` fills it:
- `claude.go`: raw struct gains `Prompt string \`json:"prompt"\``.
- `codex.go`: same.
- `cursor.go`: same (its `beforeSubmitPrompt` input is `{"prompt": ..., "attachments": ...}`).
- `agy.go`: not filled. **ASSUMPTION – confirm with user:** agy's `PreInvocation` payload is not documented in the sources read for this spec, so decision 7(b) is inactive for agy; its rows close by 7(a), (c), (d) and (e).

`hook.sessionRow` (`handler.go:165`) gains `ParentAgentID string`, and `load()` selects `COALESCE(a.parent_agent_id, '')`.

```go
// isQuestionTool is the one list of native "ask the human" tools (was inlined twice).
func isQuestionTool(name string) bool

// nativeQuestionRelay is the PreToolUse block reason for a parented agent.
const nativeQuestionRelay = "[swarm] You report to an orchestrator, not the user. Send this question to it with swarm_send (to: \"parent\", kind: \"question\") instead of a question tool."
```

`decide` changes:
- **PreToolUse**, before the subagent budget check: if `isQuestionTool(in.ToolName)`: for `s.ParentAgentID != ""` return `HookDecision{Block: true, Reason: nativeQuestionRelay}` and open nothing; otherwise fall through to the existing `AskQuestion` intercept (top-level agents).
- **PostToolUse**: the "newest open question" branch is replaced by:
  ```go
  if isQuestionTool(in.ToolName) && h.RT != nil && s.ID != "" {
  	prompt, _ := extractQuestion(in.ToolName, in.RawToolInput)
  	answer := extractToolResponseText(in.ToolResponse)
  	if answer == "" {
  		answer = "Resolved in terminal"
  	}
  	if err := h.RT.ResolveQuestionByPrompt(ctx, s.ID, prompt, answer); err != nil {
  		h.logf("hook: resolve question for %s: %v", s.ID, err)
  	}
  }
  if h.RT != nil && s.ID != "" {
  	if err := h.RT.ResolveSessionPrompts(ctx, s.ID, in.Command); err != nil {
  		h.logf("hook: resolve prompts for %s: %v", s.ID, err)
  	}
  }
  ```
  A PostToolUse payload without `tool_input` yields the placeholder prompt `"<tool> called"`, which matches no row, so nothing closes (safe; 7(b)/(c)/(d) backstop). Claude, Codex and Cursor PostToolUse payloads carry `tool_input` (see section 5).
- **UserPromptSubmit**, first statement:
  ```go
  if in.Prompt != "" && !runtime.IsDaemonPrompt(in.Prompt) && h.RT != nil && s.AgentID != "" {
  	if err := h.RT.ResolveAnsweredInTerminal(ctx, s.AgentID); err != nil {
  		h.logf("hook: close rows after human prompt for %s: %v", s.ID, err)
  	}
  }
  ```

**Discriminator and its failure modes (decision 7b).**
- A prompt is "human" iff it is non-empty and is neither exactly `IdleToken` nor contains `ShortPreamble`. That covers all daemon prompt sources found in code: `PasteLine(IdleToken)` at `wake.go:194` and `wake.go:367`, and the `Kickoff` / `ResumeKickoff` argv prompt (`agents.go:832-846`), which both embed a preamble.
- Failure mode 1, safe direction: a human prompt merged with a daemon paste into one submission (queued input) contains `ShortPreamble`, so it is treated as daemon-originated and does not close the row; the row stays until the next human prompt or another close trigger.
- Failure mode 2, coarse: any human prompt, even an unrelated one ("how is it going"), closes all of that agent's open question/blocker rows. Accepted: the user is in that terminal and has seen the row's text. **ASSUMPTION – confirm with user.**
- Failure mode 3: a human who types the idle token or the preamble text verbatim. Not realistic; no mitigation.
- Empty `Prompt` (agy, or a payload without it) never closes anything.

### 4.6 `internal/httpapi`

`requestWire` is `runtime.RequestWire`; `terminal_agent` rides on it, so `GET /api/requests`, the SSE `request.opened` / `request.resolved` payloads and the state snapshot all carry it with no handler change.

### 4.7 Removed

| Removed | Callers (all removed or rewritten in this spec) |
|---|---|
| `POST /api/requests/{id}/resolve` route, `handleResolvePrompt`, `resolvePromptBody` (`internal/httpapi/requests.go:23,177-205`) | web `api.resolvePrompt` (`web/src/api.ts:111-112`) used by `PromptView` in `web/src/panels/Review.tsx`; menubar `HTTPDaemonClient.resolvePrompt` (`HTTPDaemonClient.swift:122`). Mock daemon route `resolve` (`web/src/mock/daemon.ts:384,460`). |
| `ResolvePrompt(ctx, id, action, via)` tmux `action` argument and its key-sending branch (`requests.go:741-760`); new signature `ResolvePrompt(ctx, id, via string) (Request, error)` | the deleted `/resolve` handler; `ResolveSessionPrompts` (passes `""` today); three Go tests, section 8.3. |
| web `api.answer` (`web/src/api.ts:104`), `C.sendAnswer`, `C.approve`, `useDraft(request.id, "answer")` in `QuestionView` | `QuestionView.tsx` only. `C.answer` stays: it is the `question` value of `SCOPE_LABEL` (`web/src/logic/review.ts:6`), a `Record<RequestKind, string>` that needs every key; it is never rendered for `question`. |
| menubar `DaemonClient.answer` / `resolvePrompt`, `HTTPDaemonClient`, `MockDaemonClient` (`resolvedPrompts`), `AppModel.answerDrafts` / `answering` / `sendAnswer` / `resolvePrompt` / `requestTerminal`, `Copy.answer` / `sendAnswer` / `answerNotSent` / `approve`, `NotificationAction.answer`, `Notifier.answerFailed`, `NotificationActionSpec.textInput` and the `UNTextInputNotificationAction` branch in `SystemServices.swift:37-41` | `NeedsYouSection.swift`, `Notifier.swift`, `SystemServices.swift`, `AppModel.swift`; tests in section 8.3. |

Kept because a caller remains: `POST /api/requests/{id}/answer`, `Store.Answer`, `user_answer` (CLI `swarm answer`, `cmd/swarm/runtime_cmds.go:441`); `Store.ResolveQuestion` (reused by 4.1); `ResolvePrompt` (reused by `ResolveSessionPrompts`).

### 4.8 Web (`web/src`)

`types.ts` `Request` gains `terminal_agent: string | null;`.

```ts
// web/src/logic/inbox.ts
export type RequestTarget =
  | { kind: "terminal"; agent: string }
  | { kind: "unavailable"; hint: string };

// requestTarget: what a Needs-you row's click does. null for a request with no terminal_agent.
export function requestTarget(r: Request, agents: AgentNode[]): RequestTarget | null {
  if (!r.is_hitl || !r.terminal_agent) return null;
  const a = flattenAgents(agents).find((n) => n.name === r.terminal_agent);
  if (a?.session?.tmux_alive) return { kind: "terminal", agent: a.name };
  return { kind: "unavailable", hint: a?.session?.state === "paused" || a?.session?.state === "interrupted" ? C.orchestratorPaused : C.orchestratorNotRunning };
}
```

`QuestionView` and `PromptView` render the prompt and options as static text plus one `Open orchestrator terminal` button; both use `requestTarget`. The terminal call stays `api.agentAction(name, "terminal")` (`POST /api/agents/{name}/terminal`).

### 4.9 Menubar (`apps/menubar/Sources`)

`Wire.swift` `SwarmRequest` gains `public var terminalAgent: String?` (`case terminalAgent = "terminal_agent"`; the custom `init(from:)` decodes it with `decodeIfPresent`).

```swift
// AppModel.swift
public enum RequestTarget: Equatable { case terminal(String), unavailable(String) }

/// What tapping a Needs-you row does. nil for a request with no terminal agent.
public func requestTarget(_ r: SwarmRequest) -> RequestTarget? {
    guard r.isHITL, let name = r.terminalAgent else { return nil }
    guard let a = AgentTree.flatten(state.agents).first(where: { $0.name == name }) else {
        return .unavailable(Copy.orchestratorNotRunning)
    }
    if tmuxAlive(a) { return .terminal(name) }
    switch a.session?.state {
    case .paused, .interrupted: return .unavailable(Copy.orchestratorPaused)
    default: return .unavailable(Copy.orchestratorNotRunning)
    }
}

public func openRequest(_ r: SwarmRequest) async {
    if case let .terminal(name)? = requestTarget(r) { await openTerminal(name) }
}
```

`Notifier.handle` gains one action id and one closure:

```swift
public static let openOrchestrator = "open_orchestrator"   // NotificationAction
// Notifier.init(..., openRequestTerminal: @escaping @MainActor (String) async -> Void)  // request id in, AppModel.openRequest out
```

`Notifier.category(forKind:)` maps `request.question`, `request.prompt` and `request.blocker` to `"swarm.question"`. The `swarm.question` category has exactly one action: `open_orchestrator`, title `Copy.openOrchestratorTerminal`. Default click (banner tap) on those kinds calls `openRequestTerminal(request)`. Approval kinds keep `swarm.approval` and Review.

### 4.10 `internal/notifyrules/notifyrules.go`

Categories only: `request.prompt` and `request.blocker` change from `swarm.approval` / `swarm.agent` to `swarm.question`. Titles and bodies are unchanged. `internal/notify/notify_test.go:33` table updates to match.

---

## 5. Native question tools per adapter

Documentation read on 2026-09-21: `code.claude.com/docs/en/hooks.md`, `developers.openai.com/codex/hooks`, `cursor.com/docs/hooks`. I did not run any agent, so nothing here is verified live.

| Adapter | Question tool name in `isQuestionTool` | PreToolUse reaches it | Block output today (`HookOutput`) | Documented as blocking? | `UserPromptSubmit` has `prompt`? | Status |
|---|---|---|---|---|---|---|
| Claude | `AskUserQuestion` | Yes: hook installed with no matcher for all tools (`claude.go:36`); docs list `AskUserQuestion` among PreToolUse matchers | `hookSpecificOutput.permissionDecision: "deny"` (`claude.go:155-161`) | Yes: "`deny` prevents the tool call" | Yes (`"prompt"` in the UserPromptSubmit input) | Code + docs VERIFIED; live UNVERIFIED |
| Codex | `request_user_input`, `experimental_request_user_input` | Matcher `*` installed (`internal/install/codex.go:25`); docs say "other local function tools" reach PreToolUse but "some specialized tool paths can opt out" and do not name this tool | `{"decision":"block","reason":...}` (`codex.go:86-89`), the older shape the docs say Codex still accepts | Yes for supported tools; this tool UNVERIFIED | Yes (`prompt`) | UNVERIFIED live for both the block and the intercept |
| Cursor | `ask_question` (name from the user's brief; the docs do not name a question tool) | `preToolUse` installed, no matcher (`internal/install/cursor.go:14`); docs: "fires for all tool types (Shell, Read, Write, MCP, Task, etc.)" | `{"permission":"deny","agent_message":...}` (`cursor.go:83-95`) | Yes: `permission: "deny"` | Yes (`beforeSubmitPrompt` input `prompt`) | UNVERIFIED live |
| agy | `ask_question` (existing test `TestQuestionToolInterceptionCreatesHITLRequest`, agy subtest) | `PreToolUse` matcher `*` installed (`internal/install/agy.go:18`) | `{"decision":"deny","reason":...}` (`agy.go:60-66`) | Not verified (no agy hook docs read) | Unknown, so decision 7(b) is off | UNVERIFIED |

What this means:
- **Top-level agents (all adapters):** the tool is never blocked. The row is opened by the same PreToolUse intercept as today, so "native question tools universally work" holds wherever the tool reaches the hook. Where it does not (a tool path that opts out of hooks), no row appears; the question is still answerable in that terminal, only not listed in Needs you. Adding the tool to an adapter's list is a one-line change to `isQuestionTool`.
- **Parented agents:** the block is what enforces relay. Where an adapter ignores it (decision 13), no row is created and nothing else happens. To find out, the first live run per adapter is a scenario in section 9 (marked "live, per adapter").
- **Every close path other than 7(b) works on every adapter.** 7(b) needs `prompt` (Claude, Codex, Cursor).

---

## 6. Screens

No new surface; two existing ones lose controls and gain one click target. Tokens are the existing ones (menubar: `.caption` + `.secondary` for the item line, default body for the prompt, `.callout` + `.secondary` for the empty state; web: `text-muted` for secondary lines, `border-line`, `bg-raised` for hover/selection, `text-base` for the prompt).

### Menubar popover: one clickable row (orchestrator terminal alive)

```
┌────────────────────────────────────────────┐
│ ⌄ Needs you                              1 │
│                                            │
│ ┌────────────────────────────────────────┐ │
│ │ STORY-19 · Auth rewrite                │ │  caption, secondary
│ │ Keep the email after a failed login?   │ │  body, up to 3 lines
│ │                          [terminal ▸]  │ │  icon button, help: "Open orchestrator terminal"
│ └────────────────────────────────────────┘ │  whole card tappable; hover = raised background
└────────────────────────────────────────────┘
```

### Menubar popover: orchestrator paused (row not tappable)

```
┌────────────────────────────────────────────┐
│ ⌄ Needs you                              1 │
│                                            │
│ STORY-23 · Signing                         │
│ Need the release token to continue.        │
│ Orchestrator is paused. Resume it to       │  caption, secondary; no icon button
│ continue.                                  │
└────────────────────────────────────────────┘
```

Empty state (existing, unchanged):

```
│ ⌄ Needs you                              0 │
│   Nothing needs your attention.            │
```

Approval rows below `Needs you` keep their `[Review]` button exactly as today.

### Web board, Needs you list and detail (HITL rows)

```
┌ Needs you ─────────────┬──────────────────────────────────────┐
│ [All][Questions][Appr..]│ Which sync strategy?                 │  text-base, whitespace-pre-wrap
│ ● Which sync strategy?  │                                      │
│   SPIKE-3 · 20m         │ Options offered                      │  text-muted label
│ ● Which validation lib… │  • CRDT                              │  plain list, not buttons
│   TASK-104 · 15m        │  • Last write wins                   │
│                         │                                      │
│                         │ [ Open orchestrator terminal ]       │  one button, disabled when not alive
│                         │ Orchestrator is paused. Resume it…   │  only when unavailable, text-muted
└─────────────────────────┴──────────────────────────────────────┘
```

Clicking a list row selects it (URL `req=` as today) **and** calls `POST /api/agents/{terminal_agent}/terminal` when the target is alive. **ASSUMPTION – confirm with user:** on the web a row click both selects and opens the terminal; the button in the detail pane repeats the action if the terminal window was closed.

Deliberately not on screen, on any surface: a text box, an `Answer` button, a `Send answer` button, option buttons that submit, an `Approve` button on question/prompt/blocker rows, any resolve or dismiss control, a notification reply field.

---

## 7. All user-facing copy

| Where | Exact text |
|---|---|
| Row hint and button, running orchestrator (menubar icon help, web button, notification action) | `Open orchestrator terminal` |
| Row hint, target paused or interrupted | `Orchestrator is paused. Resume it to continue.` |
| Row hint, target not running for any other reason, or not found | `Orchestrator isn't running.` |
| Web options label | `Options offered` |
| `swarm_ask kind:question` / `swarm_blocker` refused for a parented agent (error to the agent) | `You report to an orchestrator, not the user. Send this to it with swarm_send (to: "parent", kind: "question") and keep working on anything you are not blocked on.` |
| Native question tool blocked for a parented agent (PreToolUse reason) | `[swarm] You report to an orchestrator, not the user. Send this question to it with swarm_send (to: "parent", kind: "question") instead of a question tool.` |
| Response text stored when a human prompt closes a row | `Answered in terminal` |
| Response text stored when a tool finishes with no readable answer (existing) | `Resolved in terminal` |
| Blocked checkpoint from a parented agent | No new copy. Parent receives the existing `relay` message with `blockers[]`. |
| Sweep withdrawal | No notification, no toast. The row disappears. |
| Panel empty state | `Nothing needs your attention.` (existing) |
| Notification titles/bodies for `request.question` / `request.prompt` / `request.blocker` | unchanged (`Answer needed`, `Approval needed`, `Blocker reported` and their bodies) |
| New i18n keys | web `copy.ts` `C`: `openOrchestratorTerminal`, `orchestratorPaused`, `orchestratorNotRunning`, `optionsOffered`. Menubar `Copy.swift`: `openOrchestratorTerminal`, `orchestratorPaused`, `orchestratorNotRunning`. |
| Removed keys | web `C.sendAnswer`, `C.approve`; menubar `Copy.answer`, `Copy.sendAnswer`, `Copy.answerNotSent`, `Copy.approve`. Existing `openTerminal` (web `copy.ts:95`, menubar `Copy.swift:24`) stays: agent rows and the approvals flow use it. |
| `skills/swarm/SKILL.md` rule 5 bullets (lines 17-19) | Replace the three bullets with: `Only an agent with no parent may ask the user. If you have a parent, send it a question with swarm_send (to: "parent", kind: "question", body: "...") and end your turn to wait; it decides. Top-level agents may use their native question tool or swarm_ask. Keep working on anything that does not depend on the answer; otherwise end your turn. The user answers in your terminal and you see it as a normal message. If the user answers you in your terminal, call swarm_ask with withdraw for any open request. To report a blocker with no parent, call swarm_blocker (reason, optional options) or write a blocked checkpoint with blockers: [...]. With a parent, a blocked checkpoint reaches it automatically. A terminal permission dialog is raised by the daemon, not by you.` |

---

## 8. File list

### 8.1 Changed

Go:
- `internal/runtime/requests.go` – `closeRequestTx`, `requireTopLevel`, `errRelayToParent`, `ResolveSessionPrompts`, `ResolveQuestionByPrompt`, `ResolveAnsweredInTerminal`, `terminalAgent`, `RequestWire.TerminalAgent`; `withdraw()` uses `closeRequestTx`; `ResolvePrompt` loses `action`.
- `internal/runtime/reconcile.go` – sweep call, `withdrawOrphanedRequests`, `markPromptAnswered`, scrape block, vanish loop removed.
- `internal/runtime/model.go` – `promptAnswered` field on `Store`.
- `internal/runtime/checkpoint.go` – one condition.
- `internal/runtime/text.go` – `IsDaemonPrompt`.
- `internal/adapter/adapter.go` – `HookInput.Prompt`; `internal/adapter/{claude,codex,cursor}.go` – `ParseHook` reads `prompt`.
- `internal/hook/handler.go` – `sessionRow.ParentAgentID`, `isQuestionTool`, PreToolUse parented block, PostToolUse prompt match + permission evidence, UserPromptSubmit closer.
- `internal/httpapi/requests.go` – `/resolve` route, `handleResolvePrompt`, `resolvePromptBody` removed.
- `internal/notifyrules/notifyrules.go` – two category ids.
- `skills/swarm/SKILL.md` – rule 5 bullets.

Web:
- `web/src/types.ts`, `web/src/api.ts` (drop `answer`, `resolvePrompt`), `web/src/copy.ts`, `web/src/logic/inbox.ts` (`requestTarget`), `web/src/components/QuestionView.tsx`, `web/src/panels/Review.tsx` (`PromptView`), `web/src/panels/NeedsYou.tsx` (row click), `web/src/mock/daemon.ts` and `web/src/mock/fixtures.ts` (`terminal_agent`, drop `answer`/`resolve` routes).

Menubar:
- `Sources/SwarmBarKit/{Wire,AppModel,Notifier,Copy,DaemonClient,HTTPDaemonClient}.swift`, `Sources/SwarmBarUI/Popover/NeedsYouSection.swift`, `Sources/SwarmBar/SystemServices.swift` (the text-input branch); the `Notifier` init call is in `AppModel.swift:121`.

### 8.2 Reused unchanged
`Store.Ask`, `withdraw`, `finishOpen`, `RequestWireTx` (extended, not replaced), `Events.Append`, `Items.ReconcileTx`, `AskPrompt`, `AskQuestion`, `ResolveQuestion`, `Answer` and `POST .../answer` and the CLI, approval endpoints and UI (`approve`, `request-changes`, `confirm-repos`, `close-spike`, `Review`, `ConfirmRepos`, `RequestChanges`), `POST /api/agents/{name}/terminal`, `terminal-opened`, the `terminal.open` SSE event, `Terminals.swift`, `extractQuestion`.

### 8.3 Deleted, with test ledger
Production code deleted: the pane-scrape `AskPrompt` call, the vanish-resolve loop, the "newest open question" PostToolUse branch, the `/resolve` route and handler, `ResolvePrompt`'s key-sending branch, the web and menubar answer/approve UI and client methods, the notification text-input action.

**No test file is deleted.** Behaviors below are intentionally removed from the product; each test is rewritten to assert the new contract unless a row says "removed".

| Test | Change | Why |
|---|---|---|
| `internal/runtime/reconcile_test.go` `TestPromptDetectedInRunningSessionOpensHITLRequest` | rewritten to `TestPromptPatternAutoAnswersOncePerSessionAndOpensNoRequest` | a scraped prompt no longer becomes a row; keys are pressed once |
| `reconcile_test.go` `TestReconcileAutoResolvesPromptWhenDismissedInTerminal` | rewritten to `TestReconcileNeverResolvesAPromptRowByPatternAbsence` | pattern absence no longer resolves anything |
| `internal/hook/handler_test.go` `TestPostToolUseResolvesOpenQuestionRequest`, `...FallbackWhenEmptyResponse`, `TestPostToolUseClaudeResolvesOpenQuestionRequest` | same assertions, PostToolUse payloads gain the `tool_input` they carry in real hooks | PostToolUse now matches by prompt, not "newest" |
| `handler_test.go` `TestQuestionToolInterceptionCreatesHITLRequest` | unchanged (top-level agents still open a row) | requirement 1 |
| `internal/runtime/requests_test.go` `TestResolvePromptTransmitsKeysAndResolves` | rewritten to `TestResolvePromptResolvesAndSendsNoKeys` (state, via, second call conflicts, no keys) | `/resolve` and its key-sending are removed |
| `requests_test.go` `TestResolvePromptEmptyActionDoesNotSendKeys` | call site updated to the new signature `ResolvePrompt(ctx, id, "terminal")`, assertions unchanged | signature change only |
| `internal/httpapi/requests_test.go` `TestResolvePromptRouteSendsKeysAndResolves` | rewritten to `TestResolveRouteIsGone` (POST returns 404) | the route is removed |
| `httpapi/requests_test.go` `TestResolvePromptRouteConflictAlreadyResolved`, `...NotFound`, `...InvalidJSON` | **removed** (3 tests) | they exercise only the removed handler; the conflict and not-found paths of the shared resolve code stay covered by `TestResolveQuestionFromTerminal` and `TestAnswerApproveAndRequestChanges` |
| `internal/notify/notify_test.go` `TestRulesCoverSection175` | expected categories for `request.prompt`, `request.blocker` updated | category consolidation |
| `web/src/components/QuestionView.test.tsx` (3 tests: answers with an option; answers with typed text, keeps the draft and opens the terminal; disables sending while disconnected or empty) | rewritten: static prompt and options, no textbox or submit button, terminal button posts to `/api/agents/<terminal_agent>/terminal`, disabled when disconnected or not alive | the answer input is removed |
| `web/src/panels/Review.test.tsx` `delegates questions and repo confirmation` | rewritten: no `Send answer` button, `Open orchestrator terminal` present; repo confirmation part unchanged | answer UI removed |
| `Review.test.tsx` `renders Approve button for prompt requests and resolves on click` | rewritten: no `Approve` button on a prompt row, no `/resolve` POST | Approve for prompt rows removed |
| `Review.test.tsx` `launches terminal from prompt request and disables buttons when disconnected` | button label updated to `Open orchestrator terminal`, target is `terminal_agent` | label and target change |
| `web/src/App.flows.test.tsx` `filters the inbox through the URL` | last assertion changed to `queryByRole("textbox", { name: "Answer" })` is null | textbox removed |
| `web/src/api.test.ts` | the `api.answer` and `api.resolvePrompt` call lines and route stubs removed from the request-routes test | methods removed |
| `web/e2e/mock.spec.ts` `the inbox answers a question and approves a section` | rewritten: no textbox, option text is not a button, clicking the row posts the terminal request; the approve-section half is unchanged | answer UI removed |
| `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift` `testAnswerInline` | rewritten to `testNeedsYouRowsAreReadOnlyAndOpenTheOrchestratorTerminal` | answer state removed |
| `AppModelTests.swift` `testResolvePromptCallsAPIAndUpdatesState` | rewritten to `testPromptRowTargetsTheAskingAgentsTerminal` | Approve for prompt rows removed |
| `AppModelTests.swift` `testAnswerFailedNotificationActionSeedsTheDraft` | rewritten to `testRequestNotificationActionOpensTheOrchestratorTerminal` | the `answer.failed` draft seeding is removed |
| `AppModelTests.swift` line ~157 `requestTerminal` assertion | updated to `requestTarget` | rename |
| `NotifierTests.swift` `testAnswerActionPostsTheAnswer` | rewritten to `testOpenOrchestratorActionOpensTheTerminalAndNeverAnswers` | text-input reply removed |
| `NotifierTests.swift` `testAFailedAnswerIsReportedBackWithTheTypedText` | rewritten to `testNoNotificationCategoryHasATextInputAction` | no answer, so no failed-answer notification |
| `NotifierTests.swift` `testCategoriesAndActions` | expected `swarm.question` actions and the removed `textInput` field updated | category and text-input removal |
| `NotifierTests.swift` `testOtherActionsOpenTheBoardOrTerminal` | expectations updated: default click on a HITL request opens the terminal, approvals still open the board | click routing |
| `MockDaemonClientTests.swift`, `HTTPDaemonClientTests.swift` | the `answer` call, its route string and the `answer-request.json` body assertion are removed from the request-shape checks | client method removed |

If the user rejects the `swarm answer` assumption (decision 7e), add these Go tests to the ledger as removed: `TestAnswerApproveAndRequestChanges` (answer half), and the `answered` cases in `requests_test.go` at lines ~110-130 and ~419.

---

## 9. Verification

Order:
1. `go build ./... && go vet ./internal/runtime/... ./internal/hook/... ./internal/adapter/... ./internal/httpapi/...`
2. `go test ./internal/runtime/... ./internal/hook/... ./internal/adapter/... ./internal/httpapi/... ./internal/notify/... -count=1`
3. `go test ./... -count=1`
4. `cd web && npm run typecheck && npm test -- --run && npm run test:e2e`
5. `cd apps/menubar && swift test`
6. Live check after redeploy (release-symlink flow in memory `swarm-local-deploy`, Node 22):
   `sqlite3 -readonly ~/.swarm/swarm.db "SELECT count(*) FROM requests WHERE state='open' AND is_hitl=1;"` must equal the number of rows the panel shows.

End-to-end scenarios (each is a Go, vitest or XCTest test unless marked manual):
1. **Worker ask refused.** A spawned coder calls `swarm_ask kind:question`: error text matches section 7, zero rows.
2. **Worker blocker refused.** Same for `swarm_blocker`: zero rows, item state unchanged.
3. **Parent relay.** The same coder writes `swarm_checkpoint kind:blocked, blockers:[x]`: checkpoint succeeds, item becomes `blocked`, zero rows, parent has one `relay` message containing `x`.
4. **Top-level ask.** A spike agent asks via `swarm_ask`: one open row, `terminal_agent` is that agent's name. No HTTP call can answer it except the CLI path.
5. **Native tool, top-level.** `AskUserQuestion` (Claude), `ask_question` (agy), `request_user_input` (Codex) PreToolUse from a top-level agent: no block output, one open row; the matching PostToolUse closes it `answered` via `terminal`. Live, per adapter (manual, sections 5 and 10).
6. **Native tool, parented.** The same PreToolUse from a parented agent: block output equals the section 7 text, zero rows. Live, per adapter (manual).
7. **Cross-wire gone.** Open `swarm_ask` row "A?" plus a PostToolUse of a question tool whose input asks "B?": row "A?" stays `open`.
8. **Human prompt closes rows.** `UserPromptSubmit` with `prompt: "Use zod"` for an agent with an open question and blocker row: both `answered`, via `terminal`, text `Answered in terminal`. A `prompt` row of the same agent stays open.
9. **Wake paste does not close rows.** `UserPromptSubmit` with `prompt: "swarm: inbox (call swarm_sync)"`, with `Kickoff(...)` text, and with `PendingNotice(...)` text: rows stay `open`. Empty `prompt` (agy): rows stay `open`.
10. **Rows survive pause and resume.** Row opened in session 1, session 1 `paused`, session 2 started, human prompt in session 2: row closes (agent-keyed).
11. **Session ends with an open row.** For each of `completed`, `failed`, `crashed`, `cancelled`: one Reconcile tick leaves the row `withdrawn` with a `request.resolved` event.
12. **Paused keeps its row.** `paused` and `interrupted` sessions: row stays `open` across ticks.
13. **Retry keeps its row.** Agent whose attempt 1 crashed and attempt 2 is running: row stays `open`.
14. **Daemon restart heals.** Insert an open row for a `finished` agent directly in the DB, start a fresh `Store`, one Reconcile tick: `withdrawn`.
15. **Trust dialog auto-answered.** Fake capture contains the pattern on a running, non-idle session: `Keys` called once with the split keys across three ticks; zero rows.
16. **Permission dialog.** Codex `PermissionRequest` with command `terraform apply`: one open `prompt` row, `terminal_agent` is the asking agent; ticks with no matching capture keep it open; a `PostToolUse` with a different command does not close it; one with `terraform apply` closes it `via=terminal`.
17. **Terminal target.** `RequestWire.terminal_agent` for a question row of a parented legacy agent is its root orchestrator's name; for approval kinds it is `null`.
18. **Web read-only.** Needs you renders no textbox and no `Send answer` / `Approve` button for question, prompt and blocker rows; clicking a row with a live target posts `POST /api/agents/<terminal_agent>/terminal`; a paused target shows `Orchestrator is paused. Resume it to continue.` and posts nothing. The Approvals tab still shows `Approve`.
19. **Menubar read-only.** Same three assertions in XCTest; tapping the row calls `terminals.open(<terminal_agent>)`; the `swarm.question` notification category has one action, `open_orchestrator`, no text input.
20. **Live smoke (manual, 5 min).** Spawn one top-level orchestrator, make it call `swarm_ask`; the popover shows the row; tapping it opens Ghostty attached to the orchestrator; type a reply there; the row disappears within one event.

---

## 10. Explicitly out of scope

- Delivery reliability, idle detection and the `agent.undeliverable` alert (separate spec).
- `approve_plan`, `approve_section`, `approve_report`, `accept_epic`, `accept_fix`, `confirm_repos`, `close_spike` requests, their guards and their buttons (`is_hitl=0`, user-confirmed unchanged).
- Making terminal-dialog answers work from the board (requirement 2 removes the idea).
- A new `requests.state` value or migration.
- Removing `swarm answer` / `POST .../answer` (decision 7e, pending the user).
- Pre-trusting worktrees in `~/.claude.json` (unverified; the in-repo auto-answer is enough).
- Persisting `promptAnswered` across daemon restarts (a repeated keypress is possible only while the dialog text is still on screen).
- Live verification of block behavior on Codex, Cursor and agy (listed as UNVERIFIED in section 5).
- Recognising new native question tool names (Cursor's real name, agy's) beyond the four already in `isQuestionTool`.
- Changing role gating of other MCP tools.
- Any notification copy change.

## Implementation notes (2026-09-21, as built)
- Added Task 5b: the pane scrape only presses a matcher's keys when its optional `Require` regex is also on screen (`PromptMatcher.Require`, set to `claudeTrustYes` for the trust dialog), matching `StartupDialogs`. Reason: a blind `Down+Enter` on a dialog variant without the Yes line could select "No".
- Section 4.5 (human-prompt discriminator) is extended: `IsDaemonPrompt` is true for the idle token, any prompt containing `ShortPreamble`, or any prompt that starts with `[swarm]` (the quota-reset wake notice in `wake.go` has no preamble). agy sends no prompt text, so its rows never close this way.
- `ResolvePrompt` keeps `(ctx, id, via)` with no tmux keys; `/resolve` and the web/menubar `answer`/`resolvePrompt` client methods are deleted. `swarm answer` and `POST /api/requests/{id}/answer` stay.
- Deleted tests (only the ledger's three): `TestResolvePromptRouteConflictAlreadyResolved`, `TestResolvePromptRouteNotFound`, `TestResolvePromptRouteInvalidJSON` (they covered only the removed handler).
- Live result on deploy: the sweep withdrew the blocker of a cancelled agent within one reconcile tick; a running worker's blocker stayed open and its `terminal_agent` resolved to the root orchestrator.
- Known risk: for a parented agent, the PreToolUse block on native question tools is verified from code and docs for Claude only. If Codex, Cursor or agy ignore the block, the question stays in the worker's terminal with no Needs-you row.

# Menubar: orchestrate a board item / hand off to another agent

Date: 2026-09-29 · Branch: `feat/menubar-board-handoff` · Worktree: `../agent-swarm-board-handoff`
Status: spec (no code yet). Companion plan: `docs/plans/2026-09-29-menubar-board-handoff.md` .

## Context

Today the menubar can only start a *new* spike (`New orchestrator`, `POST /api/spikes`) and hand off an
agent to a fresh session of the *same* agent/model (`Handoff` in the row menu). There is no menubar path to
(a) start an orchestrator on an existing Ready board item, or (b) hand a live orchestrator off to a
different agent/model/effort/advisor.

This change adds one window, "Orchestrate board item", reachable from a split chevron beside
`New orchestrator` and from a new `Hand off to…` entry on orchestrator rows, plus one daemon extension:
`POST /api/agents/{name}/handoff` accepts an optional agent/model/effort/advisor switch that is applied
when the successor session starts.

Verified on `main` @ `5e79012`:
- Handoff route: `internal/httpapi/handoff.go:35-67` (`handoffBody{request_id,note}` → `RequestReplacement(ModeHandoff)`, 202 + `replacementWire`). No `allowedActions` gate.
- `RequestReplacement` `internal/runtime/replacement.go:88-171`; non-test callers: `capacity.go:118`, `pause.go:993`, `agents.go:790`, `httpapi/handoff.go:60`, `mcpserver/orchestrator.go:822`.
- `startSuccessor` `replacement.go:916-1011` calls `startSession(ctx, a, attempt, gen+1, false, "", succMode)` (`agents.go:1360`): **every handoff successor is already a fresh provider session** (`resume=false`, `providerID=""`) seeded by `SuccessorKickoff(..., "handoff")` (`text.go:315`) plus the bound recovery manifest / handoff checkpoint. A cross-kind successor therefore needs no new launch mode.
- `applyRetryFallback` `fallback.go:78-110` is the precedent for rewriting kind/model/effort/advisor on an agent row mid-walk; `resolveAdvisor` `agents.go:849`; `Preflight` `agents.go:167`.
- `StartOrchestrator` `agents.go:557`; `POST /api/items/{key}/orchestrator` `httpapi/spawn.go:101-135` (wrapped in `s.idempotent`).
- `GET /api/items?view=flat` `httpapi/items.go:171`; `items.List` rejects `type=chore` (`items/list.go:41`), orders `updated_at DESC`.
- Menubar: footer `PopoverView.swift:110-123`, "More" menu pattern `:95-104`; row context menu `AgentsSection.swift:111-115`; `Window("new-orchestrator")` `App.swift:163-168`; `WideOptionPicker` is `private` at `NewOrchestratorView.swift:479`; native-advisor effort rule `NewOrchestratorForm.swift:153-159` (`advisorEffortOptions`).

Collision warnings: `feat/finish-with-pr` just merged into the same files (`AgentActions.swift`, `Copy.swift`,
`PopoverView.swift`); rebase before starting. Migration number 0023 must be re-checked at merge time.

Caveat (scope call, user-approved addition): a Ready item whose last orchestrator was user-cancelled is
restarted in place by `recoverableOrchestrator`/`restartOrchestratorInPlace` (`agents.go:741-806`), which
today **ignores** the agent/model/advisor sent. This change applies them — see "Picks on an in-place restart".

## Locked decisions

1. Split button: left half is today's `New orchestrator` button unchanged; right half is a chevron `Menu` with exactly one entry, `Orchestrate board item…`.
2. Row menu gets `Hand off to…` on **top-level orchestrator rows only**, shown when the row's `Handoff` action exists and is enabled. It opens the same window with that orchestrator's item preselected. Existing `Handoff` stays.
3. The window has no note, repos, name, intent or request fields. Only Item + the Agent/Advisor grid.
4. Picker defaults always come from Settings (`CatalogRules.prefill`), including for handoff (not the orchestrator's current kind).
5. Ready start reuses `POST /api/items/{key}/orchestrator` with `agent/model/effort/advisor`, **no `roles`** → `role_overrides` NULL → workers use Settings.
6. Handoff-with-switch extends the existing handoff route; no new route. The automated handoff walk runs unchanged (pre-pause, HANDOFF notice, predecessor saves its handoff checkpoint, stop, settle, admit); the switch is applied at `PhaseStarting`, immediately before `startSession`.
7. On switch the agent row gets: new `kind/model/effort`, advisor re-resolved via `resolveAdvisor(newKind, choice)`, `role_overrides = NULL`, `kind_reason = ''` (explicit user choice reads as "settings-equivalent", model.go:111). The window always sends `agent` (no "keep current" option), so a handoff with values identical to the row still applies the switch and clears `role_overrides`.
8. Switch is orchestrator-only; non-orchestrators get 400.
9. No usage-fallback substitution on the switch (a normal handoff successor never re-resolves fallback either; the user picked explicitly).
10. Data: no new endpoint. Client joins `GET /api/items?view=flat` with `/api/state` agents already held by `AppModel`.
11. The shared Agent/Advisor logic is **extracted** from `NewOrchestratorForm` into `AgentPickerModel` (Kit) and `AgentPickerGrid` (UI); both windows use them. Existing tests are ported to `form.picker.*`, never deleted.
12. An in-place restart (`StartOrchestrator` → `restartOrchestratorInPlace`, ModeRecover) applies the request's picks
    iff `in.Kind != ""`, with the handoff switch's semantics (see "Picks on an in-place restart").

Assumptions: "live orchestrator" = `role == .orchestrator`, `state ∈ {queued, active}`, `itemKey == item.key`
(exact assignment, as `recoverableOrchestrator` matches `item_id`). Eligible item = `parent_key == nil`,
`type ∈ {epic, bug, chore, spike}`, and (live orchestrator) OR (`status == ready` AND none).

## DB models

Migration `internal/db/schema/0023_handoff_switch.sql` (mirrors 0015's `manifest_path` column add):

```sql
-- 0023_handoff_switch.sql: a handoff may carry a pending agent switch, applied when the
-- successor starts (docs/specs/2026-09-29-menubar-board-handoff.md). '' = no switch.
ALTER TABLE agent_operations ADD COLUMN switch_json TEXT NOT NULL DEFAULT '';
```

`switch_json` is `json.Marshal(runtime.AgentSwitch)`; only `ModeHandoff` rows written by `RequestHandoffTo`
(and ModeRecover rows written by `restartOrchestratorInPlace` with picks) carry it. It is read ad hoc (`SELECT switch_json FROM agent_operations WHERE id = ?`) inside
`startSuccessor`, so the three `Operation` scanners (`operationByKey`, `pendingOperationTx`, `getOperation`)
are untouched. No index, no backfill. `agents` table unchanged.

## Model / API types

### Go — runtime (`internal/runtime`)

```go
// model.go
// AgentSwitch is a handoff's requested successor identity (spec 2026-09-29-menubar-board-handoff).
type AgentSwitch struct {
	Kind    AgentKind      `json:"kind"`
	Model   string         `json:"model"`
	Effort  string         `json:"effort,omitempty"`
	Advisor *AdvisorChoice `json:"advisor,omitempty"` // nil = Settings, None = no advisor
}

// replacement.go
func (s *Store) RequestReplacement(ctx context.Context, agentID string, mode ReplacementMode, requestKey, note string) (Operation, error) {
	return s.requestReplacement(ctx, agentID, mode, requestKey, note, "")
}
// requestReplacement = today's RequestReplacement body; the INSERT gains switch_json.
func (s *Store) requestReplacement(ctx context.Context, agentID string, mode ReplacementMode, requestKey, note, switchJSON string) (Operation, error)

// RequestHandoffTo records a handoff whose successor runs as sw. Order:
//  1. requestKey != "" and (agent, key) exists → operationByKey (replay, no validation).
//  2. agent.Role != RoleOrchestrator → 400 errSwitchNotOrchestrator.
//  3. agent.State not queued/active → 409 errSwitchNotLive.
//  4. Preflight{Kind, Model, Effort, Role: RoleOrchestrator} → plain error (httpapi maps 422).
//  5. requestReplacement(ModeHandoff, requestKey, "", json(sw)).
func (s *Store) RequestHandoffTo(ctx context.Context, agentID, requestKey string, sw AgentSwitch) (Operation, error)

// applyAgentSwitch runs in startSuccessor for every successor, after the reason switch and
// before startSession. switch_json == "" → (a, nil) (plain handoffs, retries, resumes, capacity). Else: Preflight again (catalog/auth may have moved), then
// resolveAdvisor(sw.Kind, sw.Advisor), then one UPDATE:
//   UPDATE agents SET kind=?, model=?, effort=?, advisor_kind=NULLIF(?,''), advisor_model=NULLIF(?,''),
//     advisor_effort=NULLIF(?,''), advisor_mode=NULLIF(?,''), advisor_requested_effort=NULLIF(?,''),
//     role_overrides=NULL, kind_reason='' WHERE id=?
// and returns the updated Agent (passed on to startSession and watchStartup).
func (s *Store) applyAgentSwitch(ctx context.Context, opID string, a Agent) (Agent, error)
```

`startSuccessor` change (after the `reason` switch, before `startSession`):

```go
if a, err = s.applyAgentSwitch(ctx, op.ID, a); err != nil {
	// same Notify.Raise("agent.preflight_failed", reason) as the retry branch above
	return s.setPhase(ctx, op.ID, PhaseStarting, PhaseBlocked, err.Error())
}
```

Failure semantics: Preflight fails → no row change, op `blocked`, predecessor session stays `interrupted`,
`agent.preflight_failed` notification; the row's existing Resume/Retry act on the **unchanged** kind.
`startSession` fails after the UPDATE → op `blocked` (existing path); the row keeps the new kind, which is
what the user asked for, and Retry launches it. `stopPredecessor` still uses the old kind's interrupt keys
because the switch is not applied until `PhaseStarting`.

### Picks on an in-place restart (user-approved addition)

```go
// agents.go — StartOrchestrator's recover branch
} else if ok {
	var sw *AgentSwitch
	if in.Kind != "" { // picks sent (menubar, web); CLI without --agent sends "" → unchanged
		sw = &AgentSwitch{Kind: in.Kind, Model: in.Model, Effort: in.Effort, Advisor: in.Advisor}
	}
	return s.restartOrchestratorInPlace(ctx, rec, sw, in.RepoPaths)
}

// restartOrchestratorInPlace: sw == nil → today's body (RequestReplacement(ModeRecover, "", "")).
// sw != nil → Preflight{sw.Kind, sw.Model, sw.Effort, RoleOrchestrator, RepoPaths} first (plain error →
// spawn.go's wrapPreflightErr → 422 preflight_failed, nothing recorded), then
// requestReplacement(ctx, a.ID, ModeRecover, "", "", json(sw)).
func (s *Store) restartOrchestratorInPlace(ctx context.Context, a Agent, sw *AgentSwitch, repoPaths []string) (Agent, bool, error)
```

The switch is applied by the same `applyAgentSwitch` at `PhaseStarting`: new kind/model/effort, advisor
re-resolved, `role_overrides = NULL`, `kind_reason = ''`, no usage-fallback substitution (decision 9 parity).
Callers checked: the menubar always sends picks; **web `choicePayload` (`web/src/logic/catalog.ts:170`) always
sends `agent`+`model`, so a web Start on a cancelled orchestrator now also applies its picks** (improvement, no
web change); the CLI (`cmd/swarm/runtime_cmds.go:179`) sends `"agent":""` without `--agent` → unchanged.
A web `roles` map on this path was already ignored and stays ignored (`role_overrides` is cleared).
`TestCancelThenStartRecoversSameOrchestrator` sends `Kind: Fake` and now takes the switch path; it must still pass.

New error strings (`replacement.go`):

```go
const errSwitchNotOrchestrator = "Only an orchestrator can switch agents on handoff."   // CodeBadRequest
const errSwitchNotLive        = "This orchestrator is no longer running. Reopen the list." // CodeConflict
```

### Go — httpapi

```go
// handoff.go
type handoffBody struct {
	RequestID string             `json:"request_id"`
	Note      string             `json:"note"`
	Agent     string             `json:"agent,omitempty"`
	Model     string             `json:"model,omitempty"`
	Effort    string             `json:"effort,omitempty"`
	Advisor   *advisorChoiceBody `json:"advisor,omitempty"` // object or "none"
}
```

`handoffAgent`: `body.Agent == ""` and any of model/effort/advisor set → 400 `bad_request` "Choose an agent.".
`body.Agent != ""` → `RequestHandoffTo(ctx, a.ID, body.RequestID, AgentSwitch{Kind, Model, Effort,
advisorChoiceFromBody(body.Advisor)})`, errors through `wrapPreflightErr` (plain → 422 `preflight_failed`).
`body.Note` is ignored on the switch path. Otherwise unchanged. Response always 202 `replacementWire`.

Idempotency is two mechanisms: start uses `s.idempotent` (`idempotency` table, caller = token hash);
handoff uses `agent_operations_agent_key (agent_id, request_key)`.

Errors (exact copy, status, source):

| Case | Status/code | Message |
|---|---|---|
| unknown agent | 404 not_found | `No agent named <name>.` (handoff.go:53) |
| model/effort/advisor without agent | 400 bad_request | `Choose an agent.` (new) |
| switch on non-orchestrator | 400 bad_request | `Only an orchestrator can switch agents on handoff.` (new) |
| orchestrator finished/acknowledged | 409 conflict | `This orchestrator is no longer running. Reopen the list.` (new) |
| another replacement in flight | 409 conflict | `A replacement is already in progress: <op id>.` (replacement.go:131) |
| no session yet | 400 bad_request | `This agent has no session to replace yet.` (replacement.go:112) |
| root accepted | 409 conflict | `Root accepted; write completed.` (finish.go:18) |
| bad model / auth / not installed / superpowers | 422 preflight_failed | Preflight sentences (agents.go:167-210), e.g. `Choose a model available for this agent.` |
| start: item already orchestrated | 409 conflict | `This item already has an orchestrator.` (agents.go:583) |
| start in place: bad picks | 422 preflight_failed | Preflight sentences (via spawn.go `wrapPreflightErr`) |

### Swift — SwarmBarKit

```swift
// Wire.swift
public struct BoardItem: Codable, Sendable, Equatable, Identifiable {
    public var key: String
    public var type: String          // epic|story|task|bug|spike|chore (string: unknown types must not fail decode)
    public var status: String
    public var title: String
    public var parentKey: String?
    public var id: String { key }
    enum CodingKeys: String, CodingKey { case key, type, status, title, parentKey = "parent_key" }
}
public struct BoardItemList: Codable, Sendable, Equatable { public var items: [BoardItem] }

public struct StartOrchestratorBody: Codable, Sendable, Equatable {   // POST /api/items/{key}/orchestrator
    public var requestId: String
    public var agent: AgentKind
    public var model: String
    public var effort: String?
    public var advisor: AdvisorPayload
    enum CodingKeys: String, CodingKey { case agent, model, effort, advisor, requestId = "request_id" }
}

public struct HandoffRequest: Codable, Sendable, Equatable {          // POST /api/agents/{name}/handoff
    public var requestId: String
    public var agent: AgentKind?      // nil = plain handoff (today's body)
    public var model: String?
    public var effort: String?
    public var advisor: AdvisorPayload?
    enum CodingKeys: String, CodingKey { case agent, model, effort, advisor, requestId = "request_id" }
}
```

`HandoffRequest` replaces the private `HTTPDaemonClient.HandoffBody` (deleted); `agent(_:.handoff,…)` sends
`HandoffRequest(requestId:)` — synthesized `encodeIfPresent` keeps today's wire `{"request_id":…}`.

```swift
// DaemonClient.swift — protocol additions (HTTP + Mock)
func boardItems() async throws -> [BoardItem]                                    // GET /api/items?view=flat
func startOrchestrator(itemKey: String, _ body: StartOrchestratorBody) async throws -> AgentNode // timeout 60
func handoff(_ name: String, _ body: HandoffRequest) async throws                // 202, body ignored; timeout 60
```

`MockDaemonClient`: `boardItemList: [BoardItem]` (fixture `items.json`, optional → `[]`),
`startResult: Result<AgentNode, DaemonError>?`; records `"items"`, `"start <KEY> <agent> <model>"`,
`"handoff <name> <agent> <model>"`.

```swift
// AgentPickerModel.swift (new; code MOVED from NewOrchestratorForm, not copied)
@MainActor @Observable public final class AgentPickerModel {
    public private(set) var choice: AgentChoice
    public private(set) var advisor: AdvisorChoice
    public private(set) var advisorEffort: String
    public private(set) var catalog: [AgentCatalogEntry] = []
    public private(set) var agentChangeErrors = FieldErrors()
    public private(set) var effortNote: String?
    public let settings: Settings
    public init(settings: Settings)                          // CatalogRules.prefill(settings)
    public func apply(catalog: [AgentCatalogEntry])          // today's load() normalisation
    public var errors: FieldErrors                           // CatalogRules.validate(..., role: .orchestrator)
    public var agentOptions, modelOptions, advisorAgentOptions, advisorModelOptions: [PickerOption] { get }
    public var effortOptions: [PickerOption]? { get }
    public var advisorEffortOptions: [PickerOption]? { get } // nil for native Claude-on-Claude (unchanged rule)
    public func setAgent(_:), setModel(_:), setEffort(_:), setAdvisorAgent(_:), setAdvisorModel(_:), setAdvisorEffort(_:)
    public var effortPayload: String? { get }                // normalised, "" → nil
    public var advisorPayload: AdvisorPayload { get }        // today's advisorBody()
}
// NewOrchestratorForm: `public let picker: AgentPickerModel`; body() reads picker.*; load() calls picker.apply.

// BoardHandoff.swift (new)
public struct BoardItemRow: Equatable, Sendable, Identifiable {
    public var item: BoardItem
    public var orchestrator: AgentNode?
    public var id: String { item.key }
}
public enum BoardHandoffRules {
    static let eligibleTypes: Set<String> = ["epic", "bug", "chore", "spike"]
    /// Eligible rows: live-orchestrator rows first, then Ready; daemon order (updated_at DESC) within each.
    public static func rows(_ items: [BoardItem], agents: [AgentNode]) -> [BoardItemRow]
    public static func title(_ r: BoardItemRow) -> String                  // "EPIC-12 · Ship auth"
    /// "<name> · <model label> · <effort> · <state>" (effort omitted when previewEffortLabel is nil)
    /// or "<Type> · Ready".
    public static func detail(_ r: BoardItemRow, catalog: [AgentCatalogEntry]) -> String
    public static func canHandOff(_ a: AgentNode, connected: Bool) -> Bool // AgentTree.actions(a, tmuxAlive: true, connected:) has enabled .handoff
}

@MainActor @Observable public final class BoardHandoffForm {
    public let picker: AgentPickerModel
    public private(set) var rows: [BoardItemRow] = []
    public var selectedKey: String?
    public private(set) var loading = true
    public private(set) var loadError: String?               // Copy.boardItemsLoadFailed
    public private(set) var failure: String?
    public private(set) var submitting = false
    public var connected: Bool
    public init(client: DaemonClient, settings: Settings, agents: [AgentNode], connected: Bool, preselectAgent: String?)
    public func load() async                                 // catalog + boardItems in parallel; select preselect's row, else first
    public func update(agents: [AgentNode])                  // re-join on every state change; keeps selection if still eligible
    public var selected: BoardItemRow? { get }
    public var isHandoff: Bool { get }                       // selected?.orchestrator != nil
    public var primaryLabel: String { get }                  // see copy table
    public var caption: String? { get }                      // queuedCaption | handoffUnavailable | nil
    public var canSubmit: Bool { get }
    public func primary() async -> Bool                      // loadError → load(); else submit; true = close window
}
```

Submit: handoff → `client.handoff(orch.name, HandoffRequest(requestId, agent, model, effortPayload,
advisorPayload))`; Ready → `client.startOrchestrator(itemKey:, StartOrchestratorBody(...))`. Request-id
rule copied from `NewOrchestratorForm.submit` (same entries → same id; any edit or `.api` error → new id).
`queued` = `NewOrchestratorForm.wouldQueue(agents, max: settings.maxConcurrentAgents)`.
`canSubmit = connected && !submitting && !loading && selected != nil && picker.errors.isValid && (!isHandoff || canHandOff)`.

`AppModel`: `public var boardHandoffPreselect: String?` (agent name) set before `openWindow`, consumed and
cleared by `makeBoardHandoffForm()`. `Window` scenes take no value, so preselection rides on `AppModel`.

### Swift — SwarmBarUI / app

- `AgentPickerGrid(picker:)` (new file) = today's `agentFields` Grid + caption, moved verbatim.
- `WideOptionPicker` moves out of `NewOrchestratorView.swift` to `AgentPickerGrid.swift`, `internal`, and gains
  `detail: ((PickerOption) -> NSAttributedString?)? = nil`. With `detail`, each `NSMenuItem.attributedTitle` is
  line 1 (system font) + `\n` + line 2 (`.smallSystemFontSize`, `secondaryLabelColor`, agent icon as
  `NSTextAttachment` from `Icons.image`), and the cell is set `usesItemFromMenu = false` with
  `menuItem = NSMenuItem(title: line1)` so the closed button shows one line; both `updateNSView` and the
  coordinator's `changed(_:)` reset `cell.menuItem` to the newly selected row's line-1 item.
- `BoardHandoffView(form:onDone:onCancel:)` (new), `BoardHandoffHost` + `Window(Copy.orchestrateBoardItem, id: "board-handoff")`
  in `App.swift`, `.windowResizability(.contentSize)`, `.defaultSize(width: 820, height: 300)`, centered like new-orchestrator
  (the centering block is extracted into `PopoverHost.openCentered(_ id: String)` and used by both). The host adds
  `.onChange(of: model.state.agents) { _, a in form?.update(agents: a) }` and
  `.onChange(of: model.connected) { _, up in form?.connected = up }` (as `NewOrchestratorHost`, App.swift:216).
- `PopoverView.init(model:openNewOrchestrator:openBoardHandoff:openSettings:)` with
  `openBoardHandoff: @escaping (String?) -> Void = { _ in }`; passed to `AgentsSection` → `AgentRowView`.

## Screens

Popover footer (split button; right half is a borderless `Menu`, `chevron.down`, `.menuIndicator(.hidden)`, width 18):

```
┌──────────────────────────────────────────────┐
│ [+ New orchestrator][⌄]                 [⊞] │   ⌄ opens:  ┌───────────────────────────┐
└──────────────────────────────────────────────┘             │ Orchestrate board item…   │
  both halves disabled while !connected                      └───────────────────────────┘
```

Row context menu (top-level orchestrator, Handoff enabled):

```
 Pause group / Cancel / … (unchanged)
 Handoff
 Hand off to…          ← new, directly below Handoff
```

Window — handoff item selected (padding 22 h / 12 v, footer divider, same as New orchestrator):

```
┌ Orchestrate board item ───────────────────────────────────────────────────────┐
│ Item    [ EPIC-12 · Ship auth                                            ⌃⌄ ] │
│   open menu rows:  EPIC-12 · Ship auth                                         │
│                    ◉ orch-ship-auth · Claude Opus 5 · High · Running           │
│                    BUG-7 · Login crash                                         │
│                    Bug · Ready                                                 │
│                                                                               │
│ Agent  [◉ Claude ⌄]  Model [Claude Opus 5          ⌄]  Effort [High     ⌄]   │
│ Advisor[◉ Claude ⌄]  Model [Claude Fable 5.1       ⌄]  (effort hidden: native)│
│ Defaults from Settings                                                        │
├───────────────────────────────────────────────────────────────────────────────┤
│ <caption, caption font, secondary>                  [Cancel]  [ Hand off ]    │
└───────────────────────────────────────────────────────────────────────────────┘
```

Variants: Ready item → primary `Start orchestrator` / `Queue orchestrator` with caption
`Starts when an agent slot becomes available.` · failure → red `⚠ <failure>` line above Item, primary
`Try again` · handoff not currently possible (queued/spawning/pausing/limit-owned) → caption
`This orchestrator can't hand off right now.`, primary disabled · loading → Item popup disabled, primary
disabled · empty → Item row shows secondary `No board items to orchestrate`, grid still shown, primary
disabled · load failure → red `Couldn't load board items.` in place of the Item popup, primary `Try again`
(reruns `load()`). Deliberately absent: note, name, intent, repos, request, images, advisor for non-picker cases.

## All user-facing copy (`Copy.swift`)

| Key | Text |
|---|---|
| `orchestrateBoardItem` | `Orchestrate board item` (window title) |
| `orchestrateBoardItemMenu` | `Orchestrate board item…` |
| `moreStartOptions` | `More ways to start` (chevron help + accessibility) |
| `handOffTo` | `Hand off to…` |
| `handOff` | `Hand off` |
| `item` | `Item` |
| `noBoardItems` | `No board items to orchestrate` |
| `boardItemsLoadFailed` | `Couldn't load board items.` |
| `handoffFailed` | `Couldn't hand off. Your entries are saved.` (+ " " + daemon message unless unreachable) |
| `handoffUnavailable` | `This orchestrator can't hand off right now.` |
| `ready` | `Ready` |
| `itemType(_:)` | `Epic` / `Bug` / `Chore` / `Spike` (others: capitalised slug) |
| `boardItemTitle(_:_:)` | `"\(key) · \(title)"` |
| reused | `startOrchestrator`, `queueOrchestrator`, `tryAgain`, `cancel`, `defaultsFromSettings`, `queuedCaption`, `launchFailed` (Ready start failure), `runningLabel` |

Row line 2 state = `AgentTree.handoffStatus(a) ?? DisplayState(a).label ?? Copy.runningLabel`.
Daemon strings: see the error table above. No new notification kinds (`agent.preflight_failed` reused).

## File list

Changed (Go): `internal/httpapi/handoff.go`, `internal/runtime/replacement.go`, `internal/runtime/model.go`, `internal/runtime/agents.go`.
New (Go): `internal/db/schema/0023_handoff_switch.sql`, `internal/db/schema_0023_handoff_switch_test.go`,
`internal/runtime/handoff_switch_test.go`. Extended tests: `internal/httpapi/handoff_test.go`.

Changed (Swift): `SwarmBarKit/{Wire,DaemonClient,HTTPDaemonClient,NewOrchestratorForm,AppModel,Copy}.swift`,
`SwarmBarUI/{NewOrchestratorView,Popover/PopoverView,Popover/AgentsSection}.swift`, `SwarmBar/App.swift`.
New (Swift): `SwarmBarKit/{AgentPickerModel,BoardHandoff}.swift`, `SwarmBarUI/{AgentPickerGrid,BoardHandoffView}.swift`,
`Tests/SwarmBarTests/{AgentPickerModelTests,BoardHandoffTests,BoardHandoffRenderTests}.swift`.
Ported (not deleted): `NewOrchestratorFormTests.swift` (72 refs → `f.picker.*`), `NewOrchestratorRenderTests.swift` (7),
`HTTPDaemonClientTests.swift` (handoff body now `HandoffRequest`), `PopoverRenderTests.swift` (compile unchanged thanks to default arg).

Reused unchanged: `CatalogRules`, `AgentTree.actions/handoffStatus`, `DisplayState`, `StartOrchestrator`,
`resolveAdvisor`, `Preflight`, `startSession`, `SuccessorKickoff`, `s.idempotent`, `advisorChoiceBody`.
Deleted: private `HTTPDaemonClient.HandoffBody`; `NewOrchestratorView.agentFields` (moved); private `WideOptionPicker` definition (moved). No tests deleted.

## Verification

Commands, in order (from the worktree root):

1. `go test ./internal/db/ -run 0023` — column exists, default `''`, existing rows keep `''`.
2. `go test -race ./internal/runtime/ -run 'HandoffSwitch|Replacement|Handoff'`
3. `go test -race ./internal/httpapi/ -run Handoff`
4. `make test-go` (vet, gofmt, race, cover).
5. `cd apps/menubar && swift test --filter 'AgentPicker|BoardHandoff|NewOrchestrator|HTTPDaemonClient|Popover'`, then `make test-menubar`.
6. Manual on `make dev` daemon + `SWARM_URL=http://127.0.0.1:17777 swift run SwarmBar`: scenarios below.

Go tests (new, in `handoff_switch_test.go`, harness `newStoreWithFallback` = Claude + Codex, returns `(s, tm)`; the fake adapter is `s.Adapters[Codex].(*adapter.Fake)`):
- `TestHandoffSwitchAppliesAtSuccessor`: claude orchestrator with `role_overrides` set → `RequestHandoffTo(codex, gpt-6-astra, high, advisor None)` → `ResumeOperations` → op `succeeded`; agent row kind `codex`, model, effort, advisor columns empty, `role_overrides` NULL, `kind_reason ''`; `fa.LastSpec.ProviderSessionID == ""`; kickoff contains `continuing in a fresh session after handoff`.
- `TestHandoffSwitchReplayReturnsSameOp`: same key twice → same op id, one row; replay skips validation.
- `TestHandoffSwitchRejectsNonOrchestrator` / `…FinishedAgent` / `…BadModel` (400 / 409 / Preflight error, no op row).
- `TestHandoffSwitchPreflightFailsAtStartBlocks`: disable codex after request → op `blocked` with Preflight text, agent row unchanged, `agent.preflight_failed` raised.
- `TestHandoffSwitchNativeAdvisorReResolved`: codex→claude with claude advisor → `advisor_mode` native, `advisor_effort` empty.
- `TestPlainHandoffLeavesSwitchEmpty`: `RequestReplacement` rows have `switch_json ''`; kind unchanged (regression).
- `TestStartAfterCancelAppliesPicks`: claude orchestrator started with `Roles` set → Cancel → `StartOrchestrator(codex, gpt-6-astra, high)` → same id/name, kind `codex`, `role_overrides` NULL, `kind_reason ''`, one recover op `succeeded`.
- `TestStartAfterCancelWithoutPicksKeepsKind`: same, second Start with `Kind: ""` → kind stays `claude`, `role_overrides` kept, `switch_json ''`.
- `TestStartAfterCancelBadPicksIsPreflightError`: bad model → error, no new op row, agent row unchanged.
httpapi (`handoff_test.go`): 202 with switch; `{"model":"x"}` → 400 `Choose an agent.`; bad model → 422 `preflight_failed`; `"advisor":"none"` accepted.

Swift tests: `BoardHandoffRules.rows` (eligibility incl. chore, excludes in-progress without orchestrator, children, finished orchestrators, story/task; ordering); `detail` for orchestrator (effort fallback via `previewEffortLabel`, nil effort omitted) and Ready; `canHandOff` for queued/spawning/pausing/replacement-reason; form primary labels (Hand off / Start / Queue / Try again), preselect, load failure → `Try again` reruns load, empty list disables primary, request-id reuse; `HTTPDaemonClient` encodes `HandoffRequest` without switch as exactly `{"request_id":…}` and with switch incl. `"advisor":"none"`; `StartOrchestratorBody` has no `roles`/`repos`; render test: closed Item popup is one line high.

Manual scenarios: (a) Ready epic → Start → row appears with chosen agent; (b) at capacity → Queue orchestrator + caption → agent queued; (c) live Claude orchestrator → Hand off to Codex → row shows `Saving handoff…` → `Starting…` → Codex icon, same name; workers spawned afterwards use Settings roles; (d) Cancel closes, nothing sent; (e) daemon down → primary disabled; (f) pick a model then disable that agent in Settings → 422 text in red, `Try again`; (g) handoff while another handoff runs → 409 text; (h) `Hand off to…` preselects the right item; (i) no eligible items → empty state; (j) exhaust, Ready start of a fresh orchestrator: picked
agent out of usage → `StartOrchestrator` substitutes the fallback and raises `agent.fallback_used` (existing, agents.go:591-597);
(k) exhaust, handoff switch to an out-of-usage agent → launches as picked, no substitution, no notification (decision 9); (l) cancel a live orchestrator, pick it in the window (Ready) with
another agent → Start → same name restarts with the picked agent (decision 12); an in-place restart to an
out-of-usage agent launches as picked (no substitution).

## Explicitly out of scope

- Switching agent on handoff via MCP `swarm_control` or `swarm` CLI (HTTP only).
- A handoff note field in the window; repos/name/intent on Ready start.
- Usage-fallback substitution on a switched successor.
- Switching non-orchestrator agents; changing Settings role defaults from this window.
- A new server endpoint or server-side filtering for eligible items; board (web) UI changes.
- Advisor fields on `AgentNode` / pane preview.

## Resolved while writing

- `RequestHandoffTo` + private `requestReplacement(…, switchJSON)` instead of changing `RequestReplacement`'s signature (5 callers).
- Switch stored as one `switch_json` column (0023), read ad hoc; applied at `PhaseStarting`, not at request time.
- Successor is already a fresh session for every handoff; no cross-kind branch needed.
- Preflight twice (request → 422, start → `blocked` + `agent.preflight_failed`, row unchanged).
- `kind_reason = ''`, `role_overrides = NULL` on switch; no usage fallback applied.
- No new endpoint: client join on `itemKey` + live state; eligibility and ordering in `BoardHandoffRules`.
- Preselection via `AppModel.boardHandoffPreselect`; primary gated by `AgentTree.actions` handoff availability.
- In-place restart honours picks iff `in.Kind != ""`, via the same `switch_json` + `applyAgentSwitch` (decision 12).
- Picker logic extracted to `AgentPickerModel`/`AgentPickerGrid`; tests ported to `form.picker.*`.

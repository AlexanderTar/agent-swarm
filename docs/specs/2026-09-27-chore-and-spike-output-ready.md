# Spike Output Lands Ready, and Chores Are Chores — Specification

- **Date**: 2026-09-27
- **Status**: Locked. Decisions 1–5 were made by the user through the orchestrator on 2026-09-27; decisions 6–8 are additions this spec needed to make those work and are listed separately.
- **Repos / dirs**: `agent-swarm` (`internal/items`, `internal/runtime`, `cmd/swarm`, `skills/swarm-orchestrator`, `internal/install/skills` via `make skills-sync`, `web/src`, `README.md`)
- **Companion plan**: `docs/plans/2026-09-27-chore-and-spike-output-ready.md`
- **Worktree**: `agent-swarm-chore-ready` on branch `fix/chore-and-spike-output-ready` (off `origin/main` at `db01a0a`)

---

## 1. Context

### A. Spike output gets stuck in Draft

The user said: "bugs and epics produced by spikes are in draft after spike completion, should be ready."

- `internal/runtime/materialize.go:154` creates the materialized root (epic, bug, or chore for legacy chore spikes) with `Status: items.Draft`. Its children are created `items.Ready` (`:184`).
- `internal/items/transition.go:200` lets only a user or orchestrator move Draft→Ready. For roots, `checkRoot` (`:327`) lets the daemon move Ready→InProgress only (`:340`). No rule moves a Draft root anywhere. Spikes are different: `checkSpike` allows Draft→InProgress (`:367`).
- The root orchestrator's first `accepted` checkpoint calls `tryTransition(InProgress)` (`internal/runtime/checkpoint.go:1394-1400`). On a Draft root that is denied, and `tryTransition` (`:891`) logs the denial and swallows it.
- `reconcileRoot` (`internal/items/transition.go:596`) only acts on Ready (`:597`), InProgress or InReview (`:608`). A Draft root is skipped, so `accept_epic`/`accept_fix` never opens even after an `integrated` checkpoint.
- Nothing else starts a Ready root. `StartOrchestrator` (`internal/runtime/agents.go:457`) runs only from `POST /api/items/{key}/orchestrator` (`internal/httpapi/spawn.go:104`), which the user triggers. So creating the root Ready does not auto-spawn anything, and its repos are already confirmed at materialize (`Repos: spike.Repos`, `materialize.go:155`).
- Stuck on the live board: BUG-2, BUG-3, EPIC-11, BUG-4. Each is Draft with an orchestrator that already wrote `accepted` while the root was Draft.

### B. A chore is created as a spike

The user said: "a chore is a chore, not a spike, it should be self-contained CHORE-{num} item on the board which may or may not have sub-tasks, it is not meant to produce any top-level items on the board but purely work on the scope that the user has specified."

- `items.Chore` already exists (`internal/items/model.go:19`), with the `CHORE` key prefix (`internal/ids/ids.go:83`) and tasks-only children (`internal/items/store.go:86`).
- Choosing "chore" today creates a **spike** with `spike_intent = "chore"`. `StartSpike` (`internal/runtime/agents.go:274`) always passes `Type: items.Spike` (`:292`) and passes `Intent` through (`:294`). `createSpike` (`internal/httpapi/spawn.go:39`) never validates it, and `CreateTx` accepts the value (`internal/items/store.go:377`). The orchestrator then gets `swarm-spike` (`internal/runtime/text.go:213-216`), with research, spec and plan approvals, and `swarm_materialize` turns it into a Draft CHORE root (`materialize.go:312-316`).
- Menubar: sends `intent: "chore"` to `POST /api/spikes` (`apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift:9,195`). It decodes only `agent` and `queued` from the response (`Wire.swift:682-686`), so it needs no wire change.
- Web: `NEW_TYPES` (`web/src/components/Header.tsx:10`) has no chore. The chore branch in `App.tsx:54` can't be reached and would show the spike caption anyway. `NewSpikeSheet` (`web/src/panels/NewSpikeSheet.tsx:27,84-90`) offers feature/debug only.
- CLI: `swarm new` rejects chore (`cmd/swarm/runtime_cmds.go:108,118`; usage `cmd/swarm/main.go:29`).
- MCP: a top-level orchestrator can propose chore roots (`internal/mcpserver/orchestrator.go:104,166-189`). That stays allowed from epic, bug and spike orchestrators.
- A CHORE root gets `swarm-orchestrator` (`text.go:217`), which is correct. But a CHORE root can never reach Done:
  - `check()` sends it to `checkTask` (`transition.go:223-226`).
  - `ReconcileTx` has no chore case (`:534`).
  - `rootState.finished` requires at least one child (`:471`).
  - The daemon can't move Draft→InProgress.

### Corrections to the scoping notes (verified)

1. `transition.go` is `internal/items/transition.go`, not `internal/runtime`. `spawn.go:39` is `internal/httpapi/spawn.go:39`.
2. `skills/swarm-spike/SKILL.md` never mentions chore. The only chore intent text is `skills/swarm-orchestrator/SKILL.md:33`.
3. **Promoting a stuck root by itself does not unstick it.** `acceptedSince` (`transition.go:393`) requires `checkpoints.created_at >= items.updated_at`, and `setStatus` bumps `updated_at`. BUG-2/3/4 and EPIC-11 already have `accepted` checkpoints from while they were Draft, so a Draft→Ready move leaves them at Ready. Decision 6 handles this.
4. The web board also lacks chore in `LEVEL_TYPES.top` and `PARENT_TYPES.task` (`web/src/logic/tree.ts:8,16`), in the type filters (`Header.tsx:9`, `web/src/state/url.ts:22`), and in `checkMove`'s Done case (`web/src/logic/transitions.ts:25-38`). Without these, a CHORE row shows but can't be filtered, doesn't appear on the Kanban "top" level, and has no accept hint.
5. The `accept_fix` copy says "fix" in three places: the native prompt (`internal/runtime/native.go:148-150`), the `reconcileRoot` request prompt (`transition.go:679-682`) and the notification (`internal/notifyrules/notifyrules.go:54`). This spec branches the first two on chore. The notification is keyed by request kind and stays as it is (out of scope, §10).
6. `internal/runtime/materialize_test.go` has no test for the chore branch. §9 adds one.

### Collision warnings

- Another branch is rewriting `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift` and the other new-orchestrator-dialog menubar files (`docs/specs/2026-09-26-new-orchestrator-dialog.md`). **This spec touches no menubar file.**
- `internal/items/transition.go` and `internal/items/store.go` are hot files. Keep edits local to the functions named below.

---

## 2. Locked decisions (do not reopen)

1. **Spike output lands Ready.** `createTree` creates the root `Status: items.Ready` for every root type (epic, bug, and chore for legacy chore spikes). Children are unchanged (Ready). When the **user** starts an orchestrator on a Draft root (epic, bug or chore), `StartOrchestrator` first promotes the root to Ready as `items.User("board")`. Orchestrator-proposed top-level items (`docs/specs/2026-09-26-mcp-top-level-items.md`) still land Draft.
2. **Chore creation.** Choosing chore anywhere creates a Ready `CHORE-N` root with no intent. That covers `POST /api/spikes` with `intent: "chore"`, the web New item → Chore, `swarm new --intent chore`, and the menubar through the same wire. The root keeps the brief (`Request`) and the suggested repos (`Repos` → `SuggestedRepos`). Its orchestrator runs `swarm-orchestrator`, confirms repos through the existing `confirm_repos`, and works directly on the user's scope. It may create tasks only (the existing `allowedParents` already forbids stories under a chore) and never proposes top-level items. A chore has no spec or plan approval gates.
3. **Chore proposal refused.** `swarm_items create` with no `parent` from an orchestrator whose root is a chore is refused with: `A chore works only on its own scope. It can't propose top-level items.`
4. **Chore acceptance reuses `accept_fix`** through the native approval lane (`docs/specs/2026-09-26-epic-approval-lane.md`). Once the chore's work is integrated, `reconcileRoot` opens `accept_fix` on the chore root. It is routed to the root orchestrator as a native question, exactly like a bug's. There is no new `RequestKind`, because the menubar decodes `RequestKind` strictly. A chore reaches Done through that acceptance, including a chore with zero tasks. The existing `RootDone` hook then finishes its agents (`docs/specs/2026-09-26-root-finish-and-cancel-cascade.md`).
5. **Spike + intent chore is refused at creation** (`CreateTx`) with: `A chore isn't a spike. Create type chore.` Materialize's chore branch keeps working so live SPIKE-20 (awaiting approval, intent chore) isn't stranded. There is no DB migration, and existing spike rows are untouched.

### Added by this spec (needed to deliver 1–5)

6. **A Draft root's earlier `accepted` counts on promotion.** When a user or orchestrator moves an epic, bug or chore root from Draft to Ready, and the root already has at least one `accepted` checkpoint, `TransitionTx` moves it on to InProgress in the same transaction. The following `ReconcileTx` then opens `accept_*` if the root is already integrated. This is safe because nothing ever transitions an item *into* Draft: `check()` only allows Blocked back to its saved status. So any `accepted` checkpoint on a Draft root belongs to its current lifecycle. Reopening (Done/Cancelled→Ready) is unaffected, because its `from` is not Draft. This is the recovery path for BUG-2/3/4 and EPIC-11: one board move to Ready, or `swarm start KEY` (which promotes and then answers "already has an orchestrator").
7. **Promotion happens before anything else in `StartOrchestrator`**, in its own transaction, so it covers the recover-in-place path (`restartOrchestratorInPlace`) too. If the start is then refused (existing orchestrator conflict, preflight, usage), the root stays Ready. That is harmless, because a Ready root never auto-spawns anything, and the user asked to start it.
8. **Chore copy.** Chores get their own accept copy wherever it is decided per item type: the Done denial, the request prompt and the native prompt (§6). The kind stays `accept_fix`.

### Assumptions on open questions

- `POST /api/items` with `type: "chore"` (a bare board item with no orchestrator) keeps working and still lands Draft. Starting an orchestrator on it promotes it (decision 1).
- The web New spike sheet (header "New spike" button) keeps feature/debug only. Chore is reached through New item → Chore, which opens the same sheet component in chore mode, titled "New chore". The spike sheet never offers chore, because a chore isn't a spike.
- The web `Item.spike_intent` TS type stays `"feature" | "debug" | null`. The legacy SPIKE-20 row still carries `"chore"` at runtime, and nothing in the web branches on it.

---

## 3. DB models

**No change.** No table, column, enum, index or migration. `items.spike_intent` keeps any existing `'chore'` values, and new rows never get one.

---

## 4. Model / API types

### Go: `internal/items`

No exported signature changes. New unexported helpers in `internal/items/transition.go`:

```go
// isAcceptRoot reports whether t is a root type that finishes through
// accept_epic/accept_fix: epic, bug, chore.
func isAcceptRoot(t Type) bool { return t == Epic || t == Bug || t == Chore }

// acceptKind is the acceptance request kind for a root type.
func acceptKind(t Type) string {
	if t == Epic {
		return "accept_epic"
	}
	return "accept_fix" // bug and chore (decision 4)
}
```

Changed behavior:

| Function | File:line | Change |
|---|---|---|
| `check` | `transition.go:223` | `case Epic, Bug, Chore:` → `checkRoot` |
| `checkRoot` | `transition.go:336-339` | Done denial: epic `Accept this epic to mark it Done.`, chore `Accept this chore to mark it Done.`, else `Accept this fix to mark it Done.` |
| `TransitionTx` | `transition.go:55-73` | after `setStatus`, if `from == Draft && to == Ready && isAcceptRoot(it.Type) && it.ID == it.RootID` and an `accepted` checkpoint exists on `it.ID` (any time), `setStatus(InProgress)` (decision 6) |
| `rootState` | `transition.go:471` | `st.finished = fin == n && (n > 0 \|\| it.Type == Chore)` |
| `ReconcileTx` | `transition.go:534` | `case Epic, Bug, Chore:` → `reconcileRoot` |
| `reconcileRoot` | `transition.go:615-618, 679-682` | `kind := acceptKind(it.Type)`; prompt: epic `Review completed work and accept the epic.`, bug `Review the fix and accept it.`, chore `Review the chore and accept it.` |
| `CreateTx` | `store.go:377-379` | first `if in.Type == Spike && in.SpikeIntent == "chore"` → `errf(CodeBadRequest, "A chore isn't a spike. Create type chore.")`; then the existing check becomes `in.SpikeIntent != "feature" && in.SpikeIntent != "debug"` with unchanged copy |
| `CreateTx` | `store.go:~405` (top-level orchestrator branch, before the Ready check) | `root, err := s.getByID(ctx, tx, by.RootID)`; if `root.Type == Chore` → `errf(CodeBadRequest, "A chore works only on its own scope. It can't propose top-level items.")` |

### Go: `internal/runtime`

```go
// StartSpike keeps its signature:
func (s *Store) StartSpike(ctx context.Context, in SpikeInput) (string, Agent, bool, error)
```

Behavior: before `s.Items.Create` (`agents.go:287`), build the `CreateInput` as follows.

```go
ci := items.CreateInput{Type: items.Spike, Title: in.Name, SpikeIntent: in.Intent,
	Brief: in.Request, SuggestedRepos: in.Repos}
if in.Intent == "chore" { // decision 2: a chore is not a spike
	ci.Type, ci.SpikeIntent, ci.Status = items.Chore, "", items.Ready
}
it, err := s.Items.Create(ctx, ci, items.User("board"))
```

Everything after that is unchanged: agent row, assignment message, session, preflight-failure path. `RoleSkills(RoleOrchestrator, items.Chore)` already returns `swarm-orchestrator` (`text.go:217`).

New unexported helper in `internal/runtime/agents.go`, called as the first statement after `s.Items.Get` in `StartOrchestrator` (`:458`):

```go
// promoteDraftRoot moves a Draft epic/bug/chore root to Ready as the user
// starting its orchestrator (decisions 1 and 7). Anything else is a no-op.
func (s *Store) promoteDraftRoot(ctx context.Context, it items.Item) (items.Item, error) {
	if it.ID != it.RootID || it.Status != items.Draft ||
		(it.Type != items.Epic && it.Type != items.Bug && it.Type != items.Chore) {
		return it, nil
	}
	return s.Items.Transition(ctx, it.Key, items.Ready, items.User("board"))
}
```

`internal/runtime/materialize.go:154`: `Status: items.Draft` → `Status: items.Ready`. The chore branch at `:312-316` stays as it is.

`internal/runtime/native.go:143-153` (`nativePromptFor`, `KindAcceptEpic, KindAcceptFix`): also select `type`. When the kind is `accept_fix` and the type is `chore`, return
`NativePrompt{Header: "Accept chore", Question: truncateWithToken(fmt.Sprintf("Accept %s %q as done?", key, title), req.ID), Options: approveOptions}`. The header is 12 characters, within the native header limit that "Accept epic" and "Accept fix" already fit.

### HTTP wire

No shape change.
- `POST /api/spikes` body is unchanged (`intent: "feature" | "debug" | "chore"`).
- Response `spikeResponseWire{item, agent, queued}` is unchanged. For chore, `item.type = "chore"`, `item.key = "CHORE-N"`, `item.status = "ready"`, `item.spike_intent = null`.

### CLI

`swarm new --name N --intent feature|debug|chore [--repo PATH...] [--agent A --model M --effort E] [--request TEXT]`. The validation at `runtime_cmds.go:118` accepts `chore`.

### TypeScript (`web/src`)

```ts
// types.ts
export interface CreateSpikeBody { /* … */ intent: "feature" | "debug" | "chore"; /* … */ }

// logic/spawnForm.ts
export interface SpikeFormState extends SpawnFormState { intent: "feature" | "debug" | "chore"; request: string }

// views/props.ts
export type SheetState =
  | null
  | { kind: "spike"; caption?: string; chore?: boolean }
  | { kind: "item"; type: "epic" | "bug" | "story" | "task"; parentKey?: string }
  | { kind: "spawn"; itemKey: string };

// panels/NewSpikeSheet.tsx
export function NewSpikeSheet(p: { caption?: string; chore?: boolean; onClose(): void; onCreated(key: string): void }): JSX.Element | null;

// logic/tree.ts
export const LEVEL_TYPES = { tasks: ["task"], stories: ["story"], top: ["epic", "bug", "spike", "chore"] };
export const PARENT_TYPES = { story: ["epic"], task: ["story", "bug", "spike", "chore"] };

// components/Header.tsx
const TYPES: ItemType[] = ["epic", "story", "task", "bug", "spike", "chore"];
const NEW_TYPES: ItemType[] = ["epic", "bug", "story", "task", "spike", "chore"];

// state/url.ts
const TYPES: readonly ItemType[] = ["epic", "story", "task", "bug", "spike", "chore"];

// logic/transitions.ts checkMove, to === "done":
case "chore":
  return { ok: false, reason: C.choreDone, special: "accept" };

// App.tsx newItem
const newItem = (type: ItemType, parentKey?: string) =>
  setSheet(type === "chore" ? { kind: "spike", chore: true }
    : type === "spike" ? { kind: "spike", caption: C.spikeViaNewItem }
    : { kind: "item", type, parentKey });
// (the existing call site keeps parentKey; a chore is top-level, so its parentKey is ignored)

// mock/daemon.ts createSpike: when body.intent === "chore", key `CHORE-${++counter}`,
// status "ready", spike_intent null (makeItem derives type "chore" from the key).
```

---

## 5. Screens

### 5.1 Web header, New item menu (changed: one more entry at the end)

```
┌ Agent Swarm ─────────────────────── [Needs you 2] [New spike] [New item ▾] ┐
│                                                               ┌──────────┐ │
│                                                               │ Epic     │ │
│                                                               │ Bug      │ │
│                                                               │ Story    │ │
│                                                               │ Task     │ │
│                                                               │ Spike    │ │
│                                                               │ Chore    │ │  ← new
│                                                               └──────────┘ │
```
Same menu styling (`w-32`, `menuitem` role). The type filter `<select>` gains "Chores" (from `TYPE_PLURAL`).

### 5.2 New chore sheet (NewSpikeSheet in chore mode)

```
┌ New chore ───────────────────────────────────────────── ✕ ┐
│ Name                                                      │
│ [ Bump deps                                            ]  │
│ Agent name: bump-deps                                     │
│                                                           │
│ Creates a chore orchestrator that works on the scope you  │   ← text-muted caption, no Intent control
│ describe.                                                 │
│                                                           │
│ Repositories (optional)                                   │
│ [ repo picker … ]                                         │
│ The orchestrator asks you to confirm repositories before  │   ← chore repos caption
│ it starts work.                                           │
│                                                           │
│ Agent / Model / Effort / Advisor / Worker Roles …         │   ← AgentFields, unchanged
│                                                           │
│ Request (optional)                                        │
│ [                                                      ]  │
├───────────────────────────────────────────────────────────┤
│ (Starts when an agent slot becomes available.)  [Cancel] [Start] │
└───────────────────────────────────────────────────────────┘
```
- Dialog title (accessible name): `New chore`.
- **Not on this screen:** the Intent segmented control, the spike caption "Spikes start with an intent. Use New spike.", and any spec or plan wording.
- The error, queued and retry states are the existing `NewSpikeSheet` ones, unchanged. The load-failure sheet uses the title `New chore` in chore mode.
- The header's "New spike" sheet is unchanged: Feature spike / Debug spike.

### 5.3 Board, Hierarchy CHORE row

```
▾ 🔧 CHORE-3  Bump deps                                   Ready        P2
    □ TASK-41  Bump go deps                               In progress
    □ TASK-42  Bump web deps                              Ready
  🔧 CHORE-4  Rotate signing cert                         In review    P2   ← zero tasks; no disclosure
```
- The Wrench icon (`icons.tsx:26`) and `TYPE_LABEL.chore` already exist. Rows use the existing row component, with no new tokens.
- "Add child" on a chore row offers Task only, through `PARENT_TYPES.task`.
- A zero-task chore shows no disclosure triangle, which is existing behavior for a childless root.

### 5.4 Kanban, top level and Details

```
Ready            In progress        In review                Done
┌────────────┐                      ┌──────────────────────┐
│🔧 CHORE-3  │                      │🔧 CHORE-4            │
│Bump deps   │                      │Rotate signing cert   │
└────────────┘                      │[Review]              │  ← existing accept_fix Review button
                                    └──────────────────────┘
```
- Chores now appear at level "top", because `LEVEL_TYPES.top` includes chore.
- Moving to Done in Details or Kanban shows the disabled hint `Accept this chore to mark it Done.` with `special: "accept"`, the same affordance a bug gets.
- The Review panel for a chore's `accept_fix` is the existing accept body, unchanged. Its label follows the kind ("Accept fix"), see §10.

---

## 6. All user-facing copy (verbatim)

| Where | Key / site | Copy |
|---|---|---|
| Go, `CreateTx` spike intent chore | `store.go` | `A chore isn't a spike. Create type chore.` |
| Go, `CreateTx` chore-rooted proposal | `store.go` | `A chore works only on its own scope. It can't propose top-level items.` |
| Go, `CreateTx` other bad intent (unchanged) | `store.go:378` | `Spikes start with an intent. Use New spike.` |
| Go, `checkRoot` chore Done denial | `transition.go` | `Accept this chore to mark it Done.` |
| Go, `reconcileRoot` chore request prompt | `transition.go` | `Review the chore and accept it.` |
| Go, native prompt header (chore) | `native.go` | `Accept chore` |
| Go, native prompt question (chore) | `native.go` | `Accept CHORE-N "<title>" as done?` + the existing ` ⟦swarm:<req>⟧` token |
| CLI flag help | `runtime_cmds.go:108` | `feature, debug or chore` |
| CLI validation error | `runtime_cmds.go:119` | `swarm: --name is required and --intent must be feature, debug or chore` |
| CLI usage line | `main.go:29`, `README.md:149` | `  new --name N --intent feature\|debug\|chore [--repo PATH...] [--agent A --model M --effort E] [--request TEXT]` |
| CLI success (unchanged format) | `runtime_cmds.go:155` | `CHORE-3 created. Agent bump-deps is active.` |
| Web `C.newChore` | `copy.ts` | `New chore` |
| Web `C.choreCaption` | `copy.ts` | `Creates a chore orchestrator that works on the scope you describe.` |
| Web `C.choreReposCaption` | `copy.ts` | `The orchestrator asks you to confirm repositories before it starts work.` |
| Web `C.choreDone` | `copy.ts` | `Accept this chore to mark it Done.` |
| Web New item menu entry | `TYPE_LABEL.chore` (exists) | `Chore` |
| Web type filter option | `TYPE_PLURAL.chore` (exists) | `Chores` |
| Skill `swarm-orchestrator` | §6.1 below | verbatim |

The notification copy for `request.accept_fix` (`Fix acceptance needed` / `{KEY}: Review the fix and accept it.`) is unchanged, see §10.

### 6.1 Skill text

In `skills/swarm-orchestrator/SKILL.md`:

- Line 33, replace `` (`intent` is required for a spike — `feature`, `debug` or `chore`) `` with `` (`intent` is required for a spike — `feature` or `debug`; a chore is `type: "chore"`, never a spike) ``.
- Line 53, replace `` `accept_epic` (`accept_fix` for a bug) `` with `` `accept_epic` (`accept_fix` for a bug or a chore) ``.
- Insert this section between `## Owning an item` and `## Spikes`:

```markdown
## Chores
- A chore (`CHORE-N`) is the user's own scope, not a proposal. It has no spec, plan or report to approve and no `swarm_materialize`.
- Confirm repositories first with `swarm_ask kind: "confirm_repos"`, exactly as for a spike: keep or drop (with a reason) every repo the user suggested.
- Do the work directly. For a small scope, do it yourself in your own worktree. Otherwise create tasks under the chore (`swarm_items create` with `parent: <CHORE-KEY>`, `type: "task"`, a workflow) and run them as usual. A chore has no stories; zero tasks is fine.
- Never propose top-level items from a chore — Swarm refuses it. Put out-of-scope findings in your final summary instead.
- When the work is merged and verified, write `integrated` on the chore with the merged sha per repo and the verification results. The daemon opens `accept_fix` for the chore and sends you a `request_open` relay; ask and forward it like a bug's acceptance. On approval the chore moves to Done.
```

The `swarm-spike` skill needs no change (verified: it has no chore text). Run `make skills-sync` after editing, and commit `internal/install/skills/swarm-orchestrator/SKILL.md` too.

---

## 7. File list

**Changed**
- `internal/items/transition.go`: check/ReconcileTx chore cases, `rootState`, `reconcileRoot` kind/prompt, `checkRoot` chore denial, `TransitionTx` Draft→Ready accepted rule, helpers `isAcceptRoot`/`acceptKind`.
- `internal/items/store.go`: spike intent chore refusal, chore-rooted proposal refusal.
- `internal/runtime/materialize.go`: root `Status: items.Ready`.
- `internal/runtime/agents.go`: `StartSpike` chore branch, `promoteDraftRoot`, call in `StartOrchestrator`.
- `internal/runtime/native.go`: chore native prompt.
- `cmd/swarm/runtime_cmds.go`, `cmd/swarm/main.go`, `README.md:149`: CLI intent chore.
- `skills/swarm-orchestrator/SKILL.md` plus the synced copy `internal/install/skills/swarm-orchestrator/SKILL.md`.
- `web/src/components/Header.tsx`, `web/src/App.tsx`, `web/src/views/props.ts`, `web/src/panels/NewSpikeSheet.tsx`, `web/src/logic/spawnForm.ts`, `web/src/types.ts`, `web/src/logic/tree.ts`, `web/src/state/url.ts`, `web/src/logic/transitions.ts`, `web/src/copy.ts`, `web/src/mock/daemon.ts`.

**Tests changed or added (ported, none deleted)**
- `internal/items/transition_test.go` (new chore and Draft-promotion tests).
- `internal/items/store_test.go`: port `TestCreateSpikeWithChoreIntent` (`:713`) to assert the refusal; add the chore-proposal test.
- `internal/runtime/materialize_test.go`: port `:150` to Ready; add debug-root Ready and legacy chore spike tests.
- `internal/runtime/agents_test.go`: port `TestStartSpikeWithChoreIntentAndLongBrief` (`:344`); add promotion tests.
- `internal/runtime/approval_lane_test.go`: extend `TestNativePromptForAcceptKinds`; add a chore end-to-end test.
- `cmd/swarm/runtime_cmds_test.go`: extend `TestNewRequiresNameAndIntent` (`:149`); add a chore body test.
- `web/src/App.test.tsx:130`, `web/src/App.flows.test.tsx`, `web/src/panels/NewSpikeSheet.test.tsx`, `web/src/mock/daemon.test.ts`, `web/src/logic/transitions.test.ts`, `web/src/copy.test.ts`, `web/src/logic/kanban.test.ts` (or `tree` test), `web/src/state/url` test.

**Reused unchanged**
- `internal/httpapi/spawn.go` (`createSpike`, `startOrchestrator`), `internal/runtime/confirm.go` (`confirm_repos`), `internal/runtime/requests.go` (accept routing by kind: `terminalAgent :273`, binding `:582`, approve `:1222`), the `RootDone` hook, `internal/runtime/text.go` `RoleSkills`, `internal/runtime/checkpoint.go`, `internal/mcpserver/orchestrator.go`, `skills/swarm-spike/SKILL.md`, all of `apps/menubar`, `internal/notifyrules`.

**Deleted**: nothing.

---

## 8. Verification

### Command order

1. `go vet ./...`
2. `go test ./internal/items/... ./internal/runtime/... ./internal/mcpserver/... ./internal/httpapi/... ./cmd/swarm/...`
3. `go test ./...`
4. `make skills-sync && git diff --exit-code internal/install/skills` (the sync is clean after the commit)
5. `cd web && pnpm test && pnpm typecheck` (or `make test-web`)
6. `make web-build && go build ./...` (`make build` is optional, because it codesigns with `SWARM_SIGN_IDENTITY`; never install to the live daemon)

### End-to-end scenarios (each is a test in the plan)

| # | Scenario | Expected |
|---|---|---|
| E1 | Feature spike materializes, then the user starts the root's orchestrator | EPIC root `ready`, children `ready`. The orchestrator's `accepted` moves it to `in_progress`. Children done plus `integrated` open `accept_epic` (`in_review`). Approve moves it to `done`. |
| E2 | Debug spike materializes | BUG root `ready`. `accepted`, then `integrated`, open `accept_fix`. Approve moves it to `done`. |
| E3 | User starts an orchestrator on a Draft proposal (EPIC created Draft by an orchestrator) | Root `ready` before the agent spawns. The new orchestrator's `accepted` moves it to `in_progress`. |
| E4 | User starts on a Draft root whose start is then refused (existing active orchestrator) | Conflict `This item already has an orchestrator.`; root is `ready` (and `in_progress` if it has an earlier `accepted`, decision 6). |
| E5 | Stuck-root recovery: Draft BUG with an `accepted` and a current `integrated`, tasks done; user moves Draft→Ready | Ends `in_review` with one open `accept_fix`. |
| E6 | Reopen unaffected: Done epic, user moves to Ready | Stays `ready` (existing `TestEpicAcceptanceFlow` still passes). |
| E7 | Chore from API/web/CLI/menubar: `POST /api/spikes {intent:"chore"}` | `CHORE-1`, type `chore`, `ready`, `spike_intent` null, brief = request, `suggested_repos` = repos, orchestrator kickoff names `swarm-orchestrator`. |
| E8 | Chore with one task | Task done, `accepted`, then `integrated` on CHORE: `accept_fix` opens, routed to the orchestrator as a `request_open` relay with native prompt `Accept CHORE-1 "…" as done?`. `native_answer approve` moves CHORE to `done`. |
| E9 | Chore with zero tasks | `accepted`, then `integrated`: `accept_fix` opens; approve moves it to `done`. |
| E10 | Chore declined | `request_changes` on `accept_fix`: CHORE returns to `in_progress`. A new `integrated` opens a fresh `accept_fix`. |
| E11 | Chore proposal refused | A chore-rooted orchestrator `swarm_items create` with no parent (any type): `A chore works only on its own scope. It can't propose top-level items.` No row is created. From an epic-rooted orchestrator it still succeeds (Draft). |
| E12 | Chore can't add a story | `swarm_items create type:"story" parent:"CHORE-1"`: the existing `A story can't be a child of a chore.` |
| E13 | Spike intent chore refused | `CreateTx{Type: Spike, SpikeIntent: "chore"}` from any actor: `A chore isn't a spike. Create type chore.` (`bad_request`). |
| E14 | SPIKE-20-style materialize | A spike row with `spike_intent='chore'` set directly, confirmed repos, approved plan with a chore-root swarm-tree: `swarm_materialize` creates `CHORE-1` `ready` with a Ready task; the spike reaches `done`. |
| E15 | CLI | `swarm new --name x --intent chore` posts `"intent":"chore"`; `--intent sideways` is refused before the request with the new copy. |
| E16 | Web | New item → Chore opens dialog "New chore" with the chore caption and no Intent radios; Start posts `intent:"chore"`; the mock returns `CHORE-n` `ready`; the board selects it. |
| E17 | Web Done hint | `checkMove(chore, "done")` = `{ok:false, reason:"Accept this chore to mark it Done.", special:"accept"}`. |

### Live rollout (the user runs this, never this branch's agents)

After the user redeploys (`make install-daemon`, see memory `swarm-local-deploy`), move BUG-2, BUG-3, EPIC-11 and BUG-4 from Draft to Ready on the board. Each lands `in_progress` (decision 6), or `in_review` with `accept_*` open if it is already integrated.

---

## 9. Test inventory (ported vs new)

| Test | Status | New assertion |
|---|---|---|
| `materialize_test.go:150` `TestMaterializeBuildsTheEpicTree` | port | `root.Status == items.Ready`, message `the root starts as ready` |
| `store_test.go:713` `TestCreateSpikeWithChoreIntent` | port (behavior intentionally changed, decision 5) | error `A chore isn't a spike. Create type chore.`, `CodeBadRequest` |
| `agents_test.go:344` `TestStartSpikeWithChoreIntentAndLongBrief` | port, renamed `TestStartSpikeWithChoreIntentCreatesAReadyChore` | `it.Type == items.Chore`, `it.Status == items.Ready`, `it.SpikeIntent == ""`, key prefix `CHORE-`, brief intact, name `first-pass-cleanup`, `SuggestedRepos` equals input |
| `runtime_cmds_test.go:149` `TestNewRequiresNameAndIntent` | extend | `--intent chore` reaches the stub (`POST /api/spikes`, body has `"intent":"chore"`) |
| `App.test.tsx:130` | port | menu = `Epic, Bug, Story, Task, Spike, Chore` |
| `App.flows.test.tsx` | extend | E16 |
| `NewSpikeSheet.test.tsx` | extend | chore mode title, caption, no radios, payload `intent:"chore"` |
| `mock/daemon.test.ts:184` | extend | chore branch |
| `transitions.test.ts` | extend | E17 |
| `approval_lane_test.go:19` `TestNativePromptForAcceptKinds` | extend | chore prompt |
| new: `TestChoreAcceptanceFlow`, `TestChoreWithNoTasksReachesDone`, `TestChoreDoneDenial`, `TestDraftRootPromotionHonorsEarlierAccepted`, `TestSpikeIntentChoreIsRefused` (table), `TestChoreRootCannotProposeTopLevel`, `TestMaterializeDebugRootIsReady`, `TestMaterializeLegacyChoreSpike`, `TestStartOrchestratorPromotesDraftRoot`, `TestStartOrchestratorPromotesEvenWhenRefused`, `TestChoreEndToEndAcceptFix` | new | per §8 |

---

## 10. Explicitly out of scope

- Menubar: every file, including `NewOrchestratorView.swift`, the form caption "Creates a top-level chore orchestrator…", and `Wire.swift`. The wire is unchanged.
- A new `accept_chore` request kind, and notification copy per item type (`request.accept_fix` still reads "Fix acceptance needed"). The web Review label ("Accept fix") also stays keyed by kind.
- Renaming `POST /api/spikes`, `StartSpike`, `SpikeInput` or `NewSpikeSheet`.
- Migrating or rewriting existing `spike_intent='chore'` rows (SPIKE-20), and auto-promoting the live stuck roots. The user does that with one board move each (§8 rollout).
- Auto-spawning an orchestrator on a Ready root.
- Changing orchestrator proposals (they still land Draft), or letting a child orchestrator propose.
- `swarm-spike` skill changes, and a chore-specific skill.
- A CLI `--intent chore` alias such as `swarm chore`.

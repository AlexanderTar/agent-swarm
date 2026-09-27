# Web board re-dress: dark design system, shadcn components, action toasts

## Context

**Problem.** The web board (`web/`, Vite + React 19 + Tailwind v4, embedded in the Go binary via `web/embed.go`) is built from hand-rolled primitives: a custom `Sheet` (`aside` + window keydown listener), a custom `Segmented` radio group, two hand-written ARIA menus (`MoveToMenu`, the Header "New item" menu), native `<select>`, native `<details>`, native checkboxes, and ad-hoc button class strings. Focus handling, keyboard support, and alignment are reimplemented per component and are inconsistent. Only failures produce toasts; successful actions give no confirmation.

**Change.**
1. One dark-only design system ("Graphite + Iris") expressed as shadcn CSS variables.
2. Every interactive primitive migrates to a styled shadcn component (Radix under the hood).
3. Item Details, New item, New orchestrator (spike), and Start orchestrator all open in shadcn `Sheet`s.
4. The New orchestrator form follows the macOS menubar dialog (`docs/specs/2026-09-26-new-orchestrator-dialog.md`): Chore/Feature/Debug intent, one flat repository list, aligned `Agent/Model` and `Advisor/Model` rows.
5. Sonner toasts confirm every successful mutation and report every failure.

**Repos / worktrees.** Only `agent-swarm`, worktree `../agent-swarm--web-shadcn-redress`, branch `feat/web-shadcn-redress` from `origin/main` (`a4359a7`).

**Collision check.** Codex's `feat/new-orchestrator-dialog` (`../agent-swarm--new-orchestrator-dialog`, session `01a0dd01-e273-71f3-a2d3-d3eb8e6d6e6c`) changes `internal/repos`, `internal/httpapi`, `internal/runtime`, and `apps/menubar` — **no `web/` files**. There is no file collision. Ordering dependency only: the flat web repository list relies on that branch's backend filter that hides linked worktrees from `/api/repos`. Package C may be built before that merge (the list renders whatever `/api/repos` returns), but ship this branch after `feat/new-orchestrator-dialog` merges, or linked worktrees appear in the web list.

**Caveats needing a scope call (resolved in Locked decisions):** Details modality; the Advisor Effort row codex is still iterating on; removing search/Recent/groups from the repo chooser.

## Locked decisions

1. **Dark only.** `color-scheme: dark`; tokens on `:root`; no `light-dark()`, no `.dark` class, no `@custom-variant dark`, no theme toggle.
2. **shadcn on Radix**, installed with the shadcn CLI (`pnpm dlx shadcn@latest init` / `add`), components copied into `web/src/components/ui/`. No Base UI.
3. **Details is a non-modal right Sheet** (`modal={false}`), 560 px wide, driven by `?item=` as today. The board stays clickable behind it; clicking another card swaps the sheet content in place (the `key={url.item}` remount stays). `onInteractOutside` and `onPointerDownOutside` call `preventDefault()` so clicking the board never closes it; Close button and Escape do. No overlay. On narrow screens (`NARROW` media query) it is `w-full`; the separate narrow "Back" branch in `App.tsx` is deleted because the sheet covers the board and Close returns to it.
4. **Form sheets are modal** (overlay `bg-black/60`), right side, and stack above Details. Radix handles nested focus and Escape (Escape closes only the top sheet). Widths: New item 480 px, Start orchestrator 520 px, New orchestrator 720 px. All become `w-full` below 640 px.
5. **`components/Sheet.tsx` stays as a thin wrapper** keeping its current props (`title, subtitle?, width?, onClose, children, footer?`) plus `modal?: boolean` (default `true`). Callers keep compiling while packages land in any order.
6. **Legacy token aliases.** Package A maps the old utility names (`canvas, panel, raised, line, ink, muted, accent, ok, warn, bad`) onto the new variables so untouched files keep rendering. Package E deletes the aliases after the last usage is gone (`grep` proves zero).
7. **Primary contrast.** Primary fill `#6E5CE6` with white text (4.8:1, AA at 13 px). Link / focus-ring / selected-check colour on dark surfaces is `#A298FF` (7.4:1 on the background). The earlier `#7C6CF2` fails AA with white (4.0:1) and is not used.
8. **Toasts** use Sonner, bottom-left (sheets occupy the right edge and their footers hold Cancel/Queue), 4 s for success, 6 s for errors, max 3 visible. The existing `useToast()` hook keeps its signature for errors and gains typed helpers (see API types). No Undo actions.
9. **Repository chooser = menubar parity.** Source is `ReposResponse.all` only. Missing repos are omitted. Deduplicate by `path`, sort by `name` (`localeCompare`) then `path`. No search, no Recent, no groups, no per-group "All". Rows show name + shortened full path, plus a small warning dot (aria-label `C.repoDirty`) for dirty repos. Rows toggle on click / Space / Enter (web has no Command-click convention); a selected row shows a check icon and `bg-accent`. Up to 8 rows visible (30 px each), then the list scrolls inside a `ScrollArea`. Keep Add folder and Rescan. If a rescan drops a selected ID, remove it and show the menubar's selection notice.
10. **Intent adds Chore.** `Chore | Feature spike | Debug spike`, default `feature` (unchanged). "New item → Chore" and "New item → Spike" both open New orchestrator with intent preset (`chore` / `feature`) instead of today's caption-only redirect.
11. **"New spike" is renamed "New orchestrator"** in the header and sheet title, matching the menubar.
12. **Advisor is two menus.** `Advisor [agent ▾]` with "No advisor", then `Model [▾]`. Claude lists only advisor-capable models; other agents list their visible models, matching Go Settings validation. "No advisor" disables Model and shows `—`. Changing the advisor agent picks the Settings advisor model when that agent matches, else the first eligible model for that agent. The payload is unchanged (`advisorPayload` still derives advisor effort from Settings).
13. **Agent Effort row stays conditional** (shown only when `effortOptions` returns options), placed under the Agent row, aligned to the Model column. **No Advisor Effort row** — codex is still changing its semantics on its branch; follow-up after merge.
14. **Worker roles** (`AgentFields` `<details>`) becomes a shadcn `Collapsible`, closed by default; each role uses the same grid as the agent rows.
15. **Tests are ported, never deleted,** except the three intentionally obsolete behaviours listed under File list → Deleted.
16. **Fonts: Onest (UI) + Fragment Mono (keys, paths).** Chosen by the user on 2026-09-27 from a Google Fonts comparison; self-hosted through Fontsource.
17. `ui/*.tsx` files are generated vendor code: no direct unit tests (coverage already counts only `src/**/*.ts`), but biome lints them. Behaviour is covered through the feature tests that render them.

**Assumptions.** The repo response shape (`ReposResponse { recent, groups, all, scanning, scanned_at }`) is unchanged by codex's branch (its plan says "keep the wire format"). The backend already accepts `intent: "chore"` on `POST /api/spikes` (`internal/runtime/materialize.go:312`).

## DB models

None. No schema, migration, or Go change.

## Design system

### Palette (dark only)

| shadcn variable | Hex | Legacy alias (removed in E) | Use |
|---|---|---|---|
| `--background` | `#0B0B0E` | `canvas` | page |
| `--foreground` | `#EDEDF2` | `ink` | text |
| `--card` / `--card-foreground` | `#121217` / `#EDEDF2` | `panel` | header, cards, sheets |
| `--popover` / `--popover-foreground` | `#17171D` / `#EDEDF2` | — | menus, select content |
| `--muted` | `#1C1C23` | `raised` | subtle fills |
| `--muted-foreground` | `#8A8A99` | `muted` | captions, paths |
| `--accent` / `--accent-foreground` | `#1C1C23` / `#EDEDF2` | — | hover, selected row |
| `--border` | `#26262F` | `line` | dividers |
| `--input` | `#2F2F3A` | — | field borders |
| `--ring` | `#A298FF` | — | focus ring |
| `--primary` / `--primary-foreground` | `#6E5CE6` / `#FFFFFF` | — | primary buttons |
| `--link` | `#A298FF` | `accent` | text links, selected check |
| `--secondary` / `--secondary-foreground` | `#1C1C23` / `#EDEDF2` | — | secondary buttons |
| `--destructive` | `#F0616D` | `bad` | errors, destructive |
| `--success` | `#3FB97A` | `ok` | done, approved |
| `--warning` | `#E5A13A` | `warn` | needs you, stale |
| `--info` | `#4C9AFF` | — | running |
| `--radius` | `0.375rem` (6 px) | | |

### Type and density

- Onest Variable 13 px body (self-hosted via `@fontsource-variable/onest`; the board is embedded in the Go binary and must work offline, so no Google Fonts CDN link) (`body { font-size: 13px }` stays), 12 px captions (`text-xs`), 15 px sheet titles (`text-[15px] font-semibold`), 14 px app title.
- Fragment Mono 12 px (`@fontsource/fragment-mono`, weight 400 only — never set mono text bold) for item keys (`.key` stays) and repository paths.
- Controls `h-8` (buttons, inputs, select triggers); `size="sm"` buttons `h-7`; icon buttons `size-8`. List rows 30 px. Sheet padding `px-5 py-4`; field gap `space-y-4`; label-to-control `gap-1.5`.
- Focus: `ring-2 ring-ring ring-offset-2 ring-offset-background` via shadcn defaults.

### `web/src/index.css` (target)

```css
@import "tailwindcss";
@import "tw-animate-css";
@import "@fontsource-variable/onest";
@import "@fontsource/fragment-mono";

:root {
  color-scheme: dark;
  --radius: 0.375rem;
  --background: #0b0b0e;
  --foreground: #ededf2;
  --card: #121217;
  --card-foreground: #ededf2;
  --popover: #17171d;
  --popover-foreground: #ededf2;
  --primary: #6e5ce6;
  --primary-foreground: #ffffff;
  --secondary: #1c1c23;
  --secondary-foreground: #ededf2;
  --muted: #1c1c23;
  --muted-foreground: #8a8a99;
  --accent: #1c1c23;
  --accent-foreground: #ededf2;
  --destructive: #f0616d;
  --border: #26262f;
  --input: #2f2f3a;
  --ring: #a298ff;
  --link: #a298ff;
  --success: #3fb97a;
  --warning: #e5a13a;
  --info: #4c9aff;
}

@theme inline {
  --font-sans: "Onest Variable", ui-sans-serif, system-ui, sans-serif;
  --font-mono: "Fragment Mono", ui-monospace, monospace;
  --radius-sm: calc(var(--radius) - 2px);
  --radius-md: var(--radius);
  --radius-lg: calc(var(--radius) + 2px);
  --color-background: var(--background);
  --color-foreground: var(--foreground);
  --color-card: var(--card);
  --color-card-foreground: var(--card-foreground);
  --color-popover: var(--popover);
  --color-popover-foreground: var(--popover-foreground);
  --color-primary: var(--primary);
  --color-primary-foreground: var(--primary-foreground);
  --color-secondary: var(--secondary);
  --color-secondary-foreground: var(--secondary-foreground);
  --color-muted: var(--muted);
  --color-muted-foreground: var(--muted-foreground);
  --color-accent: var(--accent);
  --color-accent-foreground: var(--accent-foreground);
  --color-destructive: var(--destructive);
  --color-border: var(--border);
  --color-input: var(--input);
  --color-ring: var(--ring);
  --color-link: var(--link);
  --color-success: var(--success);
  --color-warning: var(--warning);
  --color-info: var(--info);
  /* Legacy aliases — deleted in Package E once grep finds no usage. */
  --color-canvas: var(--background);
  --color-panel: var(--card);
  --color-raised: var(--muted);
  --color-line: var(--border);
  --color-ink: var(--foreground);
  --color-ok: var(--success);
  --color-warn: var(--warning);
  --color-bad: var(--destructive);
}

@layer base {
  * { @apply border-border outline-ring/50; }
  body { @apply bg-background text-foreground font-sans antialiased; font-size: 13px; }
}

.key { @apply font-mono; font-size: 12px; }
```

**Alias collision:** the legacy `text-accent` (link blue) and `text-muted` (grey text) names collide with shadcn's `accent`/`muted` (surface colours). Package A therefore rewrites every `text-accent` → `text-link`, `bg-accent` (primary button usage) → `bg-primary`, and `text-muted` → `text-muted-foreground` with one mechanical codemod before the aliases take effect. `bg-raised`, `border-line`, `bg-panel`, `bg-canvas`, `text-ink`, `text-ok/warn/bad` stay aliased until their files migrate.

## Component mapping

| Today | shadcn replacement | Files |
|---|---|---|
| `components/Sheet.tsx` custom `aside` | wrapper over `ui/sheet` (`side="right"`) | Sheet.tsx + 4 sheets + Details |
| Details side pane in `App.tsx` | `Sheet modal={false}` 560 px | App.tsx, Details.tsx |
| `components/Segmented.tsx` | `ui/toggle-group` (`type="single"`, `variant="outline"`, `size="sm"`) | Segmented.tsx keeps its props; Header, NewItemSheet, NewSpikeSheet, Dependencies |
| Header view switch | `ui/tabs` (`TabsList` only; the URL still owns the view) | Header.tsx |
| native `<select>` | `ui/select` | Header, NewItemSheet (parent), AgentFields, Details (priority) |
| `MoveToMenu` hand-rolled menu | `ui/dropdown-menu`; locked options are `disabled` items with a `Lock` icon and reason subtext | MoveToMenu.tsx |
| Header "New item" menu | `ui/dropdown-menu` | Header.tsx |
| Agent row action buttons | `ui/button` `size="sm"`; destructive confirm via `ui/alert-dialog` (today's `confirm` string) | AgentRow.tsx |
| `<details>` / hand-made disclosures | `ui/collapsible` | AgentFields, WorkflowSection, CheckpointList, Details |
| native checkbox | `ui/checkbox` | ConfirmRepos |
| `<input>` / `<textarea>` / label spans | `ui/input`, `ui/textarea`, `ui/label` | all forms, Header search, Editable, AddDependency, RequestChanges, QuestionView |
| class-string buttons | `ui/button` variants: `default` (primary), `secondary`, `outline`, `ghost`, `destructive`, `link`; sizes `default h-8`, `sm h-7`, `icon size-8` | every file with `<button` |
| status pills / type labels | `ui/badge` with variants `outline`, `success`, `warning`, `info`, `destructive` | StatusLabel.tsx |
| Details tablist | `ui/tabs` | Details.tsx |
| banners | `ui/alert` (`destructive` / default) | Banners.tsx, form error blocks |
| scroll regions | `ui/scroll-area` | RepoPicker list, sheets' bodies |
| hover hints | `ui/tooltip` | icon-only buttons (Close, remove criterion, add dependency) |
| `components/Toast.tsx` custom stack | `ui/sonner` `<Toaster>` + `useToast` shim | Toast.tsx, App.tsx, test/render.tsx |

Installed via CLI: `button badge input textarea label select dropdown-menu sheet tabs toggle-group collapsible checkbox alert alert-dialog scroll-area tooltip separator sonner`.

## Model / API types

No wire changes except the intent union.

```ts
// web/src/types.ts
export type SpikeIntent = "chore" | "feature" | "debug";
export interface CreateSpikeBody { request_id: string; name: string; intent: SpikeIntent; repos?: string[]; /* rest unchanged */ }

// web/src/logic/spawnForm.ts
export interface SpikeFormState extends SpawnFormState { intent: SpikeIntent; request: string }

// web/src/views/props.ts
export type SheetState =
  | null
  | { kind: "spike"; intent?: SpikeIntent }          // caption field removed
  | { kind: "item"; type: NewType; parentKey?: string }
  | { kind: "spawn"; itemKey: string };

// web/src/components/Sheet.tsx
export function Sheet(p: {
  title: string; subtitle?: string; width?: number; modal?: boolean;
  onClose(): void; children: ReactNode; footer?: ReactNode;
}): JSX.Element;

// web/src/components/Segmented.tsx (props unchanged; renders ToggleGroup)
export function Segmented<T extends string>(p: { label: string; value: T; options: { value: T; label: string }[]; onChange(v: T): void; disabled?: boolean }): JSX.Element;

// web/src/logic/repos.ts — new
export function chooserRows(r: ReposResponse): Repo[];            // all, !missing, dedupe by path, sort name→path
export function shortPath(path: string): string;                  // "/Users/x/GitHub/a" → "~/GitHub/a"
export function reconcileSelection(sel: string[], rows: Repo[]): { selection: string[]; removed: number };

// web/src/logic/catalog.ts — new (advisor split)
export function advisorAgentOptions(enabled: AgentKind[]): Option[];                     // agents + {value:"none", label:C.noAdvisor}
export function advisorModelOptions(catalog: AgentCatalogEntry[], agent: AgentKind): Option[]; // modelOptions(entry, agent === "claude")
export function changeAdvisorAgent(value: AgentKind | "none", settings: Settings, catalog: AgentCatalogEntry[]): AdvisorChoice;
// "none" → "none"; else Settings advisor model if settings.roles.advisor.agent === value and it is eligible,
// else first eligible model of that agent, else "none".

// web/src/logic/toasts.ts — new, pure copy builders (unit-tested)
export type AgentVerb = "pause" | "resume" | "cancel" | "ack" | "retry" | "terminal";
export function agentActionToast(verb: AgentVerb, name: string, scope?: "session" | "subtree"): string;
export function startedToast(agent: AgentNode, itemKey: string): string; // queued → T.toastQueued, else T.toastStarted

// web/src/components/Toast.tsx
export interface ToastInput { message: string; action?: { label: string; onClick(): void } } // unchanged
export function useToast(): {
  (t: ToastInput): void;              // existing call sites: error toast (6 s)
  success(message: string, description?: string): void; // 4 s
  error(message: string, description?: string): void;   // 6 s
};
export function Toaster(): JSX.Element; // <SonnerToaster position="bottom-left" visibleToasts={3} theme="dark" />
```

`useToast()` returns a callable function object so the ~10 existing `toast({ message })` call sites compile unchanged and render as error toasts.

## Toast coverage

Every successful mutation toasts; every failure that toasts today still toasts, now as `toast.error`.

| Action (file) | Success copy | Failure |
|---|---|---|
| Create item (`NewItemSheet`) | `T.toastItemCreated(key)` | inline alert (unchanged) |
| Create orchestrator (`NewSpikeSheet`) | `startedToast(r.agent, r.item.key)` | inline banner (unchanged) |
| Start orchestrator (`SpawnSheet`) | `startedToast(agent, itemKey)` | inline (unchanged) |
| Status move (`Details`, `Kanban` drop, `MoveToMenu`) | `T.toastMoved(key, STATUS_LABEL[status])` | `toast.error(failureMessage…)` (unchanged text) |
| Locked move | — | `toast.error(check.reason)`; spike special keeps its "View spike" action |
| Title / brief / priority / acceptance save (`Details`) | none (inline edit is its own feedback) | `toast.error(errorText)` |
| Add dependency (`AddDependency`) | `T.toastDepAdded(key, blockedBy)` | inline (unchanged) |
| Agent pause/resume/cancel/ack/retry (`AgentRow`) | `agentActionToast(verb, name, scope)` | `toast.error(errorText)` |
| Open terminal (`AgentRow`, `Details`, `NeedsYou`, `QuestionView`) | `agentActionToast("terminal", name)` | `toast.error(errorText)` |
| Approve (`Review`) | `T.toastApproved(item_key)` | `toast.error` / stale path unchanged |
| Close spike (`Review`) | `T.toastSpikeClosed(item_key)` | same |
| Request changes (`RequestChanges`) | `T.toastChangesSent(agent_name ?? item_key)` | inline (unchanged) |
| Confirm repos (`ConfirmRepos`) | `T.toastReposConfirmed(n)` | inline (unchanged) |
| Add folder (`RepoPicker`) | `T.toastRepoAdded(name)` | inline (unchanged) |
| Rescan (`RepoPicker`) | `T.toastRescanned(found, missing)` | `toast.error(errorText)` (today silently swallowed) |

## All user-facing copy (added to `web/src/copy.ts` in Package A)

```ts
// C
newOrchestrator: "New orchestrator",          // replaces C.newSpike in Header + sheet title
choreIntent: "Chore",
choreCaption: "Creates a top-level chore orchestrator for maintenance, refactoring, or general work.",
// C.noAdvisor ("No advisor") already exists — reused
advisorModel: "Advisor model",                 // aria-label of the advisor Model select
agentEffort: "Agent Effort",
reposEmpty: "No repositories found.",
reposScanning: "Scanning repositories…",
reposUnavailable: "Repositories unavailable.",
selectRepoHint: "Click to select one or more repositories.",
close: "Close",
openDetails: "Item details",                   // Details sheet aria-label fallback while loading

// T
toastItemCreated: (key: string) => `Created ${key}`,
toastStarted: (name: string, key: string) => `Started ${name} on ${key}`,
toastQueued: (name: string) => `Queued ${name}. It starts when an agent slot becomes available.`,
toastMoved: (key: string, status: string) => `Moved ${key} to ${status}`,
toastDepAdded: (key: string, blockedBy: string) => `${key} is now blocked by ${blockedBy}`,
toastPaused: (name: string) => `Pausing ${name}`,
toastPausedGroup: (name: string) => `Pausing ${name} and its agents`,
toastResumed: (name: string) => `Resumed ${name}`,
toastCancelled: (name: string) => `Cancelled ${name}`,
toastAcked: (name: string) => `Acknowledged ${name}`,
toastRetrying: (name: string) => `Retrying ${name}`,
toastTerminal: (name: string) => `Opening terminal for ${name}`,
toastApproved: (key: string) => `Approved ${key}`,
toastSpikeClosed: (key: string) => `Closed ${key}`,
toastChangesSent: (who: string) => `Sent change request to ${who}`,
toastReposConfirmed: (n: number) => `Confirmed ${n} ${n === 1 ? "repository" : "repositories"}`,
toastRepoAdded: (name: string) => `Added ${name}`,
toastRescanned: (found: number, missing: number) =>
  missing ? `Found ${found} repositories · ${missing} missing` : `Found ${found} repositories`,
reposNoLonger: (n: number) =>
  `${n} selected ${n === 1 ? "repository is" : "repositories are"} no longer available.`,
```

Changed copy: `AGENT_LABEL.agy` "agy" → "Antigravity" (every agent menu, icon aria-label, and label-derived message such as `T.superpowersMissing`). `AGENT_LOGIN_CMD.agy` stays `agy` — it is the CLI command, not a display name.

Removed copy: `C.spikeViaNewItem` (redirect caption replaced by intent preset), `C.searchRepos`, `C.recent` (only if no other caller; grep first). `C.newSpike` is removed after Header and sheet switch to `C.newOrchestrator`. `copy.test.ts` is updated for each removal and addition.

## Screens

### Board (header + view), no sheet open

```text
┌────────────────────────────────────────────────────────────────────────────────────────────┐
│ Swarm                                        [● Needs you 3] [New orchestrator] [New item ▾]│ card bg, h-12
│ [🔍 Search…            ] Type [All ▾]  Status [All ▾]   Clear filters          12 matches │
│ [Hierarchy | Kanban | Dependencies]   Card level [Tasks ▾]  Group by [Root ▾]              │ Tabs + Selects
├────────────────────────────────────────────────────────────────────────────────────────────┤
│ view (background #0B0B0E), full width — Details no longer steals 410 px                    │
└────────────────────────────────────────────────────────────────────────────────────────────┘
```
- Needs you: `Button variant="secondary"` with a `warning` dot when > 0; no dot at 0.
- New orchestrator: `outline`. New item: `default` (primary) + chevron.
- All controls on a row share `h-8` and baseline; labels are `text-muted-foreground`.
- Not on screen: theme toggle, light mode, breadcrumbs.

### Details sheet (non-modal, 560 px)

```text
                                     ┌───────────────────────────────────────────┐
                                     │ TASK-12  ◇ Task            [Move to ▾] [×]│ key mono, type badge
                                     │ Wire the retry banner                     │ 15px title, editable
                                     │ [In progress] Priority [P2 ▾]  Parent EPIC-3│
                                     ├───────────────────────────────────────────┤
                                     │ [Overview | Agents | Checkpoints | Deps]  │ Tabs
                                     │ Brief …                                   │
                                     │ Acceptance …                              │
                                     │ ▸ Workflow (collapsed)                    │ Collapsible
                                     │ Agents                                    │
                                     │  ● coder-retry  Codex · gpt-x  Running    │
                                     │    [Terminal] [Pause] [Cancel]            │ sm buttons
                                     └───────────────────────────────────────────┘
```
- Board behind remains interactive; clicking another card swaps content.
- Loading: skeleton lines (`bg-muted animate-pulse`), title `C.openDetails`.
- Error: `Alert variant="destructive"` with Retry `Button variant="link"`.
- Outside-view banner (`OutsideViewBanner`) renders as an `Alert` at the top of the sheet body.

### New orchestrator sheet (modal, 720 px)

```text
┌──────────────────────────────────────────────────────────────────────────┐
│ New orchestrator                                                     [×] │
├──────────────────────────────────────────────────────────────────────────┤
│ Name                                                                     │
│ [                                                                    ]   │
│ Agent name: retry-banner                                                 │ muted xs
│                                                                          │
│ Intent   [ Chore | Feature spike | Debug spike ]                         │ ToggleGroup
│          Creates a spike to explore this request and turn it into an epic.│
│                                                                          │
│ Repositories (optional)                                     2 selected   │
│ The spike suggests repositories and asks you to confirm them.            │
│ ┌──────────────────────────────────────────────────────────────────────┐ │
│ │ ✓ agent-swarm        ~/GitHub/agent-swarm                            │ │ selected: bg-accent, check in --link
│ │   coffee-hub         ~/GitHub/coffee-hub                             │ │
│ │ ✓ endurio-chat       ~/GitHub/endurio-chat                           │ │
│ │   …up to 8 rows, then ScrollArea                                     │ │
│ └──────────────────────────────────────────────────────────────────────┘ │
│ [Add folder…]                              Scanned 5m ago   [Rescan]     │ ghost sm buttons
│ 1 selected repository is no longer available.                            │ only after rescan drop
│                                                                          │
│ Agent    [◆ Claude      ▾]  Model  [Claude Sonnet 5 (latest)        ▾]  │ grid 72px|150px|52px|1fr
│ Effort                      [Default (high)                        ▾]  │ only when effortOptions
│ Advisor  [◆ Claude      ▾]  Model  [Claude Opus 5.5 (latest)        ▾]  │ "No advisor" → Model disabled "—"
│ Defaults from Settings                                                   │
│ ▸ Worker roles                                                           │ Collapsible
│                                                                          │
│ Request (optional)                                                       │
│ [                                                                    ]   │ Textarea rows=5
├──────────────────────────────────────────────────────────────────────────┤
│ Starts when an agent slot becomes available.        [Cancel] [Queue…]   │ pinned footer
└──────────────────────────────────────────────────────────────────────────┘
```
- Repository states keep the list box at the 8-row height: empty → `C.reposEmpty`, scanning → `C.reposScanning`, error → `C.reposUnavailable` + Retry link.
- Add folder opens an inline `Input` row + `Add` button (web cannot open a native folder panel), error below in destructive text.
- Errors: failure banner is an `Alert variant="destructive"` at the top of the body; field errors sit under their control in `text-destructive text-xs`.
- Not on screen: repository search, Recent, groups, per-group All, Advisor Effort.

### New item sheet (modal, 480 px)

```text
┌──────────────────────────────────────────────┐
│ New item                                 [×] │
├──────────────────────────────────────────────┤
│ Type   [ Epic | Bug | Story | Task ]         │
│ Parent [EPIC-3 · Authentication          ▾]  │ Select; hidden when type has no parents
│ Title  [                                  ]  │
│ Brief  [                                  ]  │ Textarea rows=4
│ Acceptance criteria                          │
│  [ criterion 1                        ] [−]  │ icon ghost + Tooltip
│  [+ Add criterion]                           │ ghost sm
├──────────────────────────────────────────────┤
│                         [Cancel] [Create]    │
└──────────────────────────────────────────────┘
```

### Start orchestrator sheet (modal, 520 px)

Same header/footer; body is `AgentFields` (identical grid to New orchestrator) and existing captions. Subtitle `EPIC-12 · Authentication`.

### Needs you / Review / Kanban / Hierarchy / Dependencies

Layouts unchanged; only primitives change (Buttons, Badges, Selects, Collapsibles, Tabs, Checkbox in ConfirmRepos, Alert for banners). Kanban cards: `card` bg, `border-border`, `rounded-md`, 8 px padding, key in mono, status Badge. Drop-target highlight `ring-1 ring-ring`.

### Toasts

```text
┌──────────────────────────────────┐
│ ✓ Moved TASK-12 to In review     │  bottom-left, popover bg, border, success icon in --success
└──────────────────────────────────┘
┌──────────────────────────────────────────────┐
│ ⚠ Spikes can't be moved by hand. [View spike]│  error: icon in --destructive, action = link button
└──────────────────────────────────────────────┘
```

## File list

**Added**
- `web/components.json`, `web/src/lib/utils.ts` (`cn`), `web/src/components/ui/*.tsx` (18 components listed above).
- `web/src/logic/toasts.ts`, `web/src/logic/toasts.test.ts`.
- Fonts: add `@fontsource-variable/onest`, `@fontsource/fragment-mono`; remove `@fontsource-variable/inter`, `@fontsource-variable/jetbrains-mono`.
- Dependencies: `radix-ui` (or the per-package `@radix-ui/*` the CLI chooses), `class-variance-authority`, `clsx`, `tailwind-merge`, `tw-animate-css`, `sonner`.

**Changed**
- Config: `web/tsconfig.json` (`baseUrl: "."`, `paths: {"@/*": ["./src/*"]}`), `web/vite.config.ts` (`resolve.alias["@"]`), `web/src/index.css`, `web/biome.json` (only if lint config must include `ui/`).
- Tests infra: `web/src/test/setup.ts` (polyfills `hasPointerCapture`, `releasePointerCapture`, `setPointerCapture`, `scrollIntoView`), `web/src/components/Toast.tsx` (`ToastProvider` now mounts the Sonner `<Toaster />`, so `test/render.tsx` needs no change).
- Logic: `copy.ts`, `types.ts`, `logic/repos.ts`, `logic/catalog.ts`, `logic/spawnForm.ts`, `views/props.ts`, `mock/daemon.ts` (accept `chore`), `mock/fixtures.ts` if needed.
- Components: `Sheet, Segmented, Toast, MoveToMenu, Header, AgentFields, RepoPicker, AgentRow, StatusLabel, Banners, CheckpointList, ConfirmRepos, WorkflowSection, QuestionView, RequestChanges, AddDependency, ArtifactViewer, Markdown (prose colours), icons` (tokens only).
- Panels/views: `App.tsx, Details, NewItemSheet, NewSpikeSheet, SpawnSheet, Review, NeedsYou, Kanban, Hierarchy, Dependencies`.
- Tests ported (every one): `primitives.test.tsx` (width stays inline so the 420px assertion holds, focus lands inside dialog, ToggleGroup role `group` + items `radio`), all 15 `selectOptions` call sites → open combobox + click option, `App.test.tsx`, `App.flows.test.tsx`, `SpawnSheet/NewSpikeSheet/NewItemSheet/Details/Review/NeedsYou.test.tsx`, `AgentFields/AgentRow/RepoPicker/Toast/ConfirmRepos/CheckpointList/WorkflowSection/QuestionView/RequestChanges.test.tsx`, `Kanban/Hierarchy/Dependencies.test.tsx`, `logic/repos.test.ts`, `logic/catalog.test.ts`, `copy.test.ts`, `e2e/mock.spec.ts`, `e2e/live.spec.ts`.

**Reused unchanged:** `api.ts`, `sse.ts`, `data/*`, `state/*`, `logic/{inbox,kanban,tree,transitions,graphLayout,review,timeline,format,kebab,newItem,requestTitle,confirmRepos,agentActions}.ts`, `@xyflow/react` graph (restyled via tokens only), `@dnd-kit` drag logic.

**Deleted (intentionally obsolete behaviour — stated here per test policy)**
1. `repoSections()` + test "orders sections Recent, local groups, owner groups, All and merges same-name groups" — the chooser no longer has sections (Locked decision 9).
2. `selectAll()` + its assertions inside "selects, toggles and summarises" — no per-group All. The `toggleRepo`/`selectedLine` assertions in that test stay.
3. `repoSubtitle()` + its half of "abbreviates the parent folder and builds the subtitle" — replaced by `shortPath` (full path, no owner); the test is rewritten for `shortPath`, not dropped. `parentFolder` is deleted if no other caller.
4. RepoPicker search input behaviour and its test case(s) in `RepoPicker.test.tsx` — search removed; the file's other cases are ported.
5. `App.tsx` narrow "Back" button branch — Details sheet Close replaces it. `App.test.tsx` "replaces the view with the details panel on narrow windows" is rewritten to assert the sheet is full width and Close returns to the board, not deleted.

## Verification

Command order (from `web/` in the worktree):
1. `pnpm install`
2. `pnpm typecheck`
3. `pnpm test` (coverage threshold 80 % lines must hold)
4. `pnpm e2e`
5. `pnpm build`
6. `cd .. && go test ./web/...` (embed still finds `dist/`)
7. `pnpm dev:mock`, then Playwright screenshots at 1280×800 and 390×844 of: board, Details sheet, New orchestrator (empty, 12 repos, No advisor), New item, a toast. Opus UI review against this spec.

End-to-end scenarios (mock daemon):
1. **Create item happy path:** New item → Task → parent → title → Create → sheet closes, Details sheet opens on the new key, toast "Created TASK-n".
2. **Create item error:** mock returns 409 → inline alert, sheet stays open, no success toast.
3. **New orchestrator, chore:** choose Chore → caption changes → select 2 repos → Queue → toast "Queued …" when slots are busy, else "Started … on CHORE-n". Payload `intent: "chore"`.
4. **New item → Chore/Spike:** opens New orchestrator with intent preset.
5. **No advisor:** Advisor → No advisor → Model disabled showing "—"; payload has no advisor.
6. **Advisor agent switch:** Codex → Claude picks the Settings advisor model when it's Claude, else Claude's first advisor-capable model.
7. **Rescan drops a selected repo:** notice "1 selected repository is no longer available.", selection count drops, toast "Found n repositories · 1 missing".
8. **Repo states:** empty / scanning / failed each keep the list height and show their copy.
9. **Move via menu:** Move to → In review → toast "Moved TASK-n to In review"; locked option is disabled with reason text.
10. **Kanban drop on locked column:** error toast with reason; spike card shows "View spike" action.
11. **Agent actions:** Pause (orchestrator) → "Pausing X and its agents"; Cancel orchestrator with live children → AlertDialog confirm → "Cancelled X"; API failure → error toast.
12. **Approve / Close spike / Request changes / Confirm repos:** each shows its success toast; conflict path still shows the stale banner and no success toast.
13. **Sheets stacking:** Details open → Start orchestrator → Escape closes only Start orchestrator; focus returns to its trigger inside Details; Escape again closes Details.
14. **Non-modal Details:** with Details open, clicking another Kanban card swaps Details content; the sheet stays open.
15. **Disconnected daemon:** submit buttons disabled, connection banner (Alert) visible, no toasts fire.
16. **Keyboard:** Tab reaches every control in the New orchestrator sheet in visual order; repo rows toggle with Space; Select opens with Enter/ArrowDown.

## Explicitly out of scope

- Light mode or a theme toggle.
- Advisor Effort row (follows codex's branch after merge).
- Any Go, API, or menubar change; repository classification.
- Replacing `@xyflow/react`, `@dnd-kit`, or `react-markdown`.
- New views, new filters, keyboard shortcuts beyond what Radix provides, command palette.
- Undo actions in toasts.
- Changes to item/agent business rules (`logic/transitions`, `logic/agentActions`).

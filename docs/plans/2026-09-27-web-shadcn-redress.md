# Web Board Re-dress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan unit by unit. Follow `superpowers:test-driven-development` for every behaviour change and `skills/swarm-batching/SKILL.md` for package boundaries. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the web board's hand-rolled primitives with styled shadcn components on one dark "Graphite + Iris" design system, open all item/orchestrator forms and item details in Sheets, match the menubar's New orchestrator UX, and confirm every mutation with a Sonner toast.

**Architecture:** shadcn components (Radix) are vendored into `web/src/components/ui/` and themed by CSS variables in `web/src/index.css`. Existing app-level wrappers (`Sheet`, `Segmented`, `Toast`) keep their public props and switch their internals, so feature files migrate package by package without breaking the build. Pure logic (repo chooser rows, advisor split, toast copy) lands first as tested `.ts` modules; UI packages consume it.

**Tech Stack:** React 19, Vite 8, Tailwind v4 (`@tailwindcss/vite`), shadcn CLI + Radix, Sonner, Fontsource (Onest, Fragment Mono), Vitest 5 + Testing Library + jsdom, Playwright, pnpm, Go embed.

**Spec:** [`docs/specs/2026-09-27-web-shadcn-redress.md`](../specs/2026-09-27-web-shadcn-redress.md) — executors read both. The spec owns palette values, screen sketches, copy strings, and the toast table; this plan references them by section rather than re-deciding them.

## Global Constraints

- Worktree `/Users/alexandertar/GitHub/agent-swarm--web-shadcn-redress`, branch `feat/web-shadcn-redress`. All commands run from `web/` unless noted.
- Dark only: `color-scheme: dark`; no `light-dark()`, no `.dark` class, no theme toggle, no `next-themes`.
- Fonts: `@fontsource-variable/onest` (UI), `@fontsource/fragment-mono` (keys, paths; weight 400 only — never bold mono). No Google Fonts CDN (board is embedded and must work offline).
- Palette hex values: exactly the spec's "Palette (dark only)" table. Primary `#6E5CE6` + white text; links/ring `#A298FF`.
- Controls `h-8`; `size="sm"` `h-7`; icon buttons `size-8`; list rows 30 px; sheet body padding `px-5 py-4`; field gap `space-y-4`.
- Sheet widths: New item 480, Start orchestrator 520, New orchestrator 720, Details 560 (non-modal). All `max-w-full`.
- Toasts: Sonner, `position="bottom-left"`, `visibleToasts={3}`, success 4000 ms, error 6000 ms, no Undo.
- Copy: every string lives in `web/src/copy.ts`; exact strings are in the spec's "All user-facing copy" section. No inline user-facing literals in components.
- Radix `Select` forbids `value=""` on items: use the sentinels `ALL = "__all"` / `NONE = "__none"` and map to `""` at the boundary.
- Tests are **ported, never deleted**. The only removals allowed are the five listed under the spec's "Deleted (intentionally obsolete behaviour)". Any other test that stops fitting gets rewritten to the new contract in the same unit.
- Every error surface keeps using `errorText(e)` (reason ?? message).
- Never `git add -A`; never `git commit --amend`. Stage explicit paths. Commit messages end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- Per-unit TDD: write the failing test, run it and record RED (behaviour failure, not a syntax/setup error), implement minimally, run GREEN, commit. Package verification runs once at the end of the package.

## Package map and ordering

| Package | Units | Workflow | Runs | Done when |
|---|---|---|---|---|
| **A. Foundation** | A1–A3 | `ui-tdd-reviewed` | first, parallel with B | shadcn + tokens + fonts + test infra + Sonner shim in place; whole suite green; app renders dark |
| **B. Logic & copy contract** | B1–B4 | `tdd-reviewed` | first, parallel with A | copy, toast builders, repo chooser rules, advisor split, chore intent tested |
| **C. Primitives** | C1–C4 | `ui-tdd-reviewed` | after A | Sheet, Segmented, MoveToMenu, StatusLabel/Banners are shadcn-backed with ported tests |
| **D. Forms** | D1–D5 | `ui-tdd-reviewed` | after B + C, parallel with E | New item / New orchestrator / Start orchestrator match spec screens; creation toasts |
| **E. Details & panels** | E1–E4 | `ui-tdd-reviewed` | after B + C, parallel with D | Details is a non-modal Sheet; agent/review/dependency actions toast |
| **F. Header, views, cleanup** | F1–F5 | `ui-tdd-reviewed` | after D + E | Header/Kanban/Hierarchy/Dependencies migrated; aliases + old fonts removed; e2e + screenshots + UI review pass |

Max 2 agents in flight. Implementers: Claude Sonnet (latest), dispatched with the package text below. Reviewers: Claude Opus (latest) — code reviewer, plus UI reviewer for `ui-tdd-reviewed` packages (UI review uses the spec's Screens section and Playwright screenshots from `pnpm dev:mock`).

**D ∥ E file-collision guard:** D owns `App.tsx` only for the `SheetState`/`newItem` lines (D5); E owns `App.tsx` for the Details sheet block (E1). Whichever lands second rebases; they touch disjoint hunks. Neither touches `copy.ts` (B owns it).

**Codex ordering note:** do not merge this branch to `main` before `feat/new-orchestrator-dialog` merges (the flat repo list relies on its backend worktree filter). Building before that is fine.

---

## Package A · Foundation

**Files:** `web/package.json`, `web/pnpm-lock.yaml`, `web/components.json`, `web/tsconfig.json`, `web/vite.config.ts`, `web/src/index.css`, `web/src/lib/utils.ts`, `web/src/components/ui/*.tsx`, `web/src/test/setup.ts`, `web/src/test/select.ts`, `web/src/test/select.test.tsx`, `web/src/components/Toast.tsx`, `web/src/components/Toast.test.tsx`, plus the mechanical class rename across `web/src/**/*.tsx`.

**Interfaces produced:**
- `@/` import alias → `web/src/`.
- `cn(...inputs: ClassValue[]): string` in `@/lib/utils`.
- `@/components/ui/{button,badge,input,textarea,label,select,dropdown-menu,sheet,tabs,toggle-group,toggle,collapsible,checkbox,alert,alert-dialog,scroll-area,tooltip,separator}`.
- `SheetContent` accepts `overlay?: boolean` (default `true`).
- Test helpers `pickOption`, `comboText`, `optionTexts` in `src/test/select.ts`.
- `useToast()` returning a callable with `.success` / `.error`; `ToastProvider` still wraps children and now mounts the Sonner `Toaster`.

### Unit A1: shadcn, alias, tokens, fonts

- [ ] **Step 1: Write the failing test** — `web/src/lib/utils.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { cn } from "@/lib/utils";

describe("cn", () => {
  it("merges conflicting tailwind classes, last wins", () => {
    expect(cn("px-2 h-8", false && "hidden", "px-4")).toBe("h-8 px-4");
  });
});
```

- [ ] **Step 2: Run to verify RED** — `pnpm vitest run src/lib/utils.test.ts`. Expected: FAIL resolving `@/lib/utils` (alias and module missing).

- [ ] **Step 3: Alias** — `tsconfig.json` `compilerOptions` gains:

```json
"baseUrl": ".",
"paths": { "@/*": ["./src/*"] }
```

`vite.config.ts`: add `import { fileURLToPath } from "node:url";` and inside `defineConfig({...})`:

```ts
resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
```

- [ ] **Step 4: shadcn init + components**

```bash
pnpm dlx shadcn@latest init --base-color neutral --css-variables -y
pnpm dlx shadcn@latest add button badge input textarea label select dropdown-menu sheet tabs toggle-group collapsible checkbox alert alert-dialog scroll-area tooltip separator
pnpm add sonner tw-animate-css @fontsource-variable/onest @fontsource/fragment-mono
pnpm remove @fontsource-variable/inter @fontsource-variable/jetbrains-mono
```

If `init` prompts, answer: style `new-york`, TypeScript yes, CSS file `src/index.css`, components alias `@/components`, utils alias `@/lib/utils`, icon library `lucide`. Verify `components.json` has `"rsc": false`, `"tailwind": { "css": "src/index.css", "cssVariables": true }`. Do **not** `add sonner` via the CLI (its wrapper imports `next-themes`); A3 writes our own Toaster. Confirm `src/lib/utils.ts` exports `cn` (`clsx` + `tailwind-merge`).

- [ ] **Step 5: Tokens** — replace `web/src/index.css` wholesale with the spec's "`web/src/index.css` (target)" block (the CLI overwrote it; ours wins), but with the font imports `@import "@fontsource-variable/onest";` and `@import "@fontsource/fragment-mono";` and `--font-sans: "Onest Variable", …` / `--font-mono: "Fragment Mono", …`. Delete any `.dark { … }` block and `@custom-variant dark` the CLI added. Add `"**/src/components/ui/**"` to `biome.json` `files.ignore` (vendored code; biome is not installed or run in any verify chain today, so this only keeps a future `biome check` from flagging generated files).

- [ ] **Step 6: `ui/sheet.tsx` overlay switch** — in the generated `SheetContent`, add prop `overlay = true` and render `{overlay && <SheetOverlay />}` instead of the unconditional `<SheetOverlay />`. Keep the rest of the generated file.

- [ ] **Step 7: Class-name codemod** (legacy names that collide with shadcn semantics; run from `web/`):

```bash
grep -rl --include='*.tsx' -e 'text-accent' -e 'bg-accent' -e 'text-muted' src | grep -v '/ui/' | \
  xargs sed -i '' -E 's/\btext-accent\b/text-link/g; s/\bbg-accent\b/bg-primary/g; s/\btext-muted\b/text-muted-foreground/g'
grep -rn --include='*.tsx' -E '\b(text-accent|bg-accent|text-muted)\b' src | grep -v '/ui/' | grep -v 'muted-foreground'
```

Expected second command output: empty. (`bg-accent` today only means "primary button"; `text-white` beside it stays.)

- [ ] **Step 8: Verify GREEN** — `pnpm vitest run src/lib/utils.test.ts` PASS; `pnpm typecheck` PASS; `pnpm test` PASS (class rename is not asserted by tests; if a test asserts `text-accent`/`text-muted` classes, update the assertion to the new name).

- [ ] **Step 9: Commit**

```bash
git add package.json pnpm-lock.yaml components.json biome.json tsconfig.json vite.config.ts src/index.css src/lib/utils.ts src/lib/utils.test.ts src/components/ui
git add $(git diff --name-only -- 'src/**/*.tsx')
git commit -m "feat(web): shadcn foundation, Graphite+Iris tokens, Onest + Fragment Mono"
```

### Unit A2: Test infra for Radix

- [ ] **Step 1: Write the failing test** — `web/src/test/select.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { comboText, optionTexts, pickOption } from "./select";

function Host() {
  const [v, setV] = useState("a");
  return (
    <Select value={v} onValueChange={setV}>
      <SelectTrigger aria-label="Letter"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value="a">Alpha</SelectItem>
        <SelectItem value="b">Beta</SelectItem>
      </SelectContent>
    </Select>
  );
}

describe("Radix Select test helpers", () => {
  it("reads, lists and picks options", async () => {
    const user = userEvent.setup();
    render(<Host />);
    expect(comboText("Letter")).toBe("Alpha");
    expect(await optionTexts(user, "Letter")).toEqual(["Alpha", "Beta"]);
    await pickOption(user, "Letter", "Beta");
    expect(comboText("Letter")).toBe("Beta");
  });
});
```

- [ ] **Step 2: RED** — `pnpm vitest run src/test/select.test.tsx`. Expected: FAIL (`./select` missing; after creating it, jsdom errors `target.hasPointerCapture is not a function` / `scrollIntoView is not a function` until Step 3's polyfills land).

- [ ] **Step 3: Implement** — `web/src/test/select.ts`:

```ts
import { screen } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

type Name = string | RegExp;

export const comboText = (name: Name) => screen.getByRole("combobox", { name }).textContent ?? "";

export async function optionTexts(user: UserEvent, name: Name): Promise<string[]> {
  await user.click(screen.getByRole("combobox", { name }));
  const texts = (await screen.findAllByRole("option")).map((o) => o.textContent ?? "");
  await user.keyboard("{Escape}");
  return texts;
}

export async function pickOption(user: UserEvent, name: Name, option: Name): Promise<void> {
  await user.click(screen.getByRole("combobox", { name }));
  await user.click(await screen.findByRole("option", { name: option }));
}
```

Append to `web/src/test/setup.ts` (below the existing stubs):

```ts
import { toast } from "sonner";

// Radix (Select, DropdownMenu, Sheet) calls pointer-capture and scrollIntoView APIs jsdom lacks.
Object.assign(Element.prototype, {
  hasPointerCapture: () => false,
  setPointerCapture: () => {},
  releasePointerCapture: () => {},
  scrollIntoView: () => {},
});

// Sonner keeps toasts in module state; clear between tests so one test's toast never leaks into the next.
afterEach(() => toast.dismiss());
```

(Move the `import { toast } from "sonner";` to the top import block; `afterEach` is already imported.)

- [ ] **Step 4: GREEN** — `pnpm vitest run src/test/select.test.tsx` PASS. If the trigger click does not open under jsdom, set `userEvent.setup({ pointerEventsCheck: 0 })` in the test and helpers' callers, and record which was needed in the commit body.

- [ ] **Step 5: Commit** — `git add src/test/setup.ts src/test/select.ts src/test/select.test.tsx && git commit -m "test(web): Radix select helpers and jsdom polyfills"`

### Unit A3: Sonner behind `useToast`

- [ ] **Step 1: Port the test first** — rewrite `web/src/components/Toast.test.tsx`:

```tsx
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ToastProvider, useToast } from "./Toast";

function Trigger({ onAction }: { onAction: () => void }) {
  const toast = useToast();
  return (
    <>
      <button type="button" onClick={() => toast({ message: "This spike reaches Done after materialization.", action: { label: "View spike", onClick: onAction } })}>legacy</button>
      <button type="button" onClick={() => toast.success("Created TASK-9")}>ok</button>
    </>
  );
}

describe("Toast (Sonner)", () => {
  it("legacy call shows an error toast with its action and dismisses after 6 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onAction = vi.fn();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<ToastProvider><Trigger onAction={onAction} /></ToastProvider>);
    await user.click(screen.getByRole("button", { name: "legacy" }));
    expect(await screen.findByText("This spike reaches Done after materialization.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "View spike" }));
    expect(onAction).toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(6_600));
    expect(screen.queryByText("This spike reaches Done after materialization.")).not.toBeInTheDocument();
  });

  it("success toast disappears after 4 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<ToastProvider><Trigger onAction={vi.fn()} /></ToastProvider>);
    await user.click(screen.getByRole("button", { name: "ok" }));
    expect(await screen.findByText("Created TASK-9")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(4_600));
    expect(screen.queryByText("Created TASK-9")).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 2: RED** — `pnpm vitest run src/components/Toast.test.tsx`. Expected: FAIL (`toast.success is not a function`).

- [ ] **Step 3: Implement** — replace `web/src/components/Toast.tsx`:

```tsx
import type { ReactNode } from "react";
import { Toaster as SonnerToaster, toast as sonner } from "sonner";

export interface ToastInput { message: string; action?: { label: string; onClick: () => void } }

export type ToastFn = ((t: ToastInput) => void) & {
  success(message: string, description?: string): void;
  error(message: string, description?: string): void;
};

const SUCCESS_MS = 4000;
const ERROR_MS = 6000;

// Legacy `toast({ message })` call sites are all failure or refusal messages, so they render as errors.
const toastFn: ToastFn = Object.assign(
  (t: ToastInput) => {
    sonner.error(t.message, { duration: ERROR_MS, action: t.action && { label: t.action.label, onClick: t.action.onClick } });
  },
  {
    success: (message: string, description?: string) => { sonner.success(message, { description, duration: SUCCESS_MS }); },
    error: (message: string, description?: string) => { sonner.error(message, { description, duration: ERROR_MS }); },
  },
);

export const useToast = (): ToastFn => toastFn;

export function Toaster() {
  return (
    <SonnerToaster
      theme="dark"
      position="bottom-left"
      visibleToasts={3}
      toastOptions={{
        classNames: {
          toast: "bg-popover text-popover-foreground border border-border shadow-lg font-sans text-[13px]",
          description: "text-muted-foreground",
          actionButton: "!bg-transparent !text-link font-medium",
          success: "[&_[data-icon]]:text-success",
          error: "[&_[data-icon]]:text-destructive",
        },
      }}
    />
  );
}

export function ToastProvider({ children }: { children: ReactNode }) {
  return (
    <>
      {children}
      <Toaster />
    </>
  );
}
```

- [ ] **Step 4: GREEN** — `pnpm vitest run src/components/Toast.test.tsx` PASS, then `pnpm test`. If the 4 s / 6 s dismissals never fire, check `document.visibilityState` in jsdom first — Sonner pauses timers while the document is hidden — before touching durations. Existing tests that asserted `getByRole("status")` for a toast: port them to `findByText(<message>)` in this unit (grep: `grep -rn 'role("status")\|"status"' src --include='*.test.tsx'`).

- [ ] **Step 5: Commit** — `git add src/components/Toast.tsx src/components/Toast.test.tsx <ported test files> && git commit -m "feat(web): Sonner toasts behind useToast"`

**Package A verify:** `pnpm typecheck && pnpm test && pnpm build && (cd .. && go test ./web/...)`. Then `pnpm dev:mock`, open `http://localhost:5173`, confirm dark background `#0B0B0E` and Onest text. Screenshot for the UI reviewer.

---

## Package B · Logic & copy contract

**Files:** `web/src/copy.ts`, `web/src/copy.test.ts`, `web/src/logic/toasts.ts`, `web/src/logic/toasts.test.ts`, `web/src/logic/repos.ts`, `web/src/logic/repos.test.ts`, `web/src/logic/catalog.ts`, `web/src/logic/catalog.test.ts`, `web/src/types.ts`, `web/src/logic/spawnForm.ts`, `web/src/logic/spawnForm.test.ts`, `web/src/mock/daemon.test.ts`.

**Interfaces produced** (exact, consumed by D/E/F):

```ts
// types.ts
export type SpikeIntent = "chore" | "feature" | "debug";
// CreateSpikeBody.intent: SpikeIntent; SpikeFormState.intent: SpikeIntent

// logic/toasts.ts
export type AgentVerb = "pause" | "resume" | "cancel" | "ack" | "retry" | "terminal";
export function agentActionToast(verb: AgentVerb, name: string, scope?: "session" | "subtree"): string;
export function startedToast(agent: Pick<AgentNode, "name" | "state">, itemKey: string): string;

// logic/repos.ts
export function shortPath(path: string): string;
export function chooserRows(r: ReposResponse): Repo[];
export function reconcileSelection(sel: string[], rows: Repo[]): { selection: string[]; removed: number };

// logic/catalog.ts
export function advisorAgentOptions(enabled: AgentKind[]): Option[];
export function advisorModelOptions(catalog: AgentCatalogEntry[], agent: AgentKind): Option[];
export function changeAdvisorAgent(value: AgentKind | "none", settings: Settings, catalog: AgentCatalogEntry[]): AdvisorChoice;
```

Copy keys: every `C.*` / `T.*` in the spec's "All user-facing copy" block.

### Unit B1: Copy additions

- [ ] **Step 1: Failing test** — append to `web/src/copy.test.ts`:

```ts
describe("re-dress copy (spec: All user-facing copy)", () => {
  it("has menubar-parity strings", () => {
    expect(C.newOrchestrator).toBe("New orchestrator");
    expect(C.choreIntent).toBe("Chore");
    expect(C.choreCaption).toBe("Creates a top-level chore orchestrator for maintenance, refactoring, or general work.");
    expect(C.agentEffort).toBe("Agent Effort");
    expect(C.advisorModel).toBe("Advisor model");
    expect(C.reposEmpty).toBe("No repositories found.");
    expect(C.reposScanning).toBe("Scanning repositories…");
    expect(C.reposUnavailable).toBe("Repositories unavailable.");
    expect(C.close).toBe("Close");
    expect(T.reposNoLonger(1)).toBe("1 selected repository is no longer available.");
    expect(T.reposNoLonger(2)).toBe("2 selected repositories are no longer available.");
  });

  it("has toast strings", () => {
    expect(T.toastItemCreated("TASK-9")).toBe("Created TASK-9");
    expect(T.toastStarted("auth-orch", "EPIC-3")).toBe("Started auth-orch on EPIC-3");
    expect(T.toastQueued("auth-orch")).toBe("Queued auth-orch. It starts when an agent slot becomes available.");
    expect(T.toastMoved("TASK-9", "In review")).toBe("Moved TASK-9 to In review");
    expect(T.toastDepAdded("TASK-9", "TASK-2")).toBe("TASK-9 is now blocked by TASK-2");
    expect(T.toastPausedGroup("o")).toBe("Pausing o and its agents");
    expect(T.toastReposConfirmed(1)).toBe("Confirmed 1 repository");
    expect(T.toastReposConfirmed(3)).toBe("Confirmed 3 repositories");
    expect(T.toastRescanned(12, 0)).toBe("Found 12 repositories");
    expect(T.toastRescanned(12, 1)).toBe("Found 12 repositories · 1 missing");
  });
});
```

- [ ] **Step 2: RED** — `pnpm vitest run src/copy.test.ts` → FAIL (`undefined` / not a function).
- [ ] **Step 3: Implement** — add every `C` and `T` key from the spec's copy block verbatim to `copy.ts` (`C.noAdvisor` already exists; reuse). Do **not** remove `C.newSpike`, `C.spikeViaNewItem`, `C.searchRepos`, `C.recent` yet — their consumers migrate in D/F, which remove them.
- [ ] **Step 4: GREEN** — `pnpm vitest run src/copy.test.ts` PASS.
- [ ] **Step 5: Commit** — `git add src/copy.ts src/copy.test.ts && git commit -m "feat(web): copy for re-dress, toasts and menubar parity"`

### Unit B2: Toast copy builders

- [ ] **Step 1: Failing test** — `web/src/logic/toasts.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { agentActionToast, startedToast } from "./toasts";

describe("toast builders", () => {
  it("names each agent action", () => {
    expect(agentActionToast("pause", "coder-1", "session")).toBe("Pausing coder-1");
    expect(agentActionToast("pause", "orch", "subtree")).toBe("Pausing orch and its agents");
    expect(agentActionToast("resume", "coder-1")).toBe("Resumed coder-1");
    expect(agentActionToast("cancel", "coder-1")).toBe("Cancelled coder-1");
    expect(agentActionToast("ack", "coder-1")).toBe("Acknowledged coder-1");
    expect(agentActionToast("retry", "coder-1")).toBe("Retrying coder-1");
    expect(agentActionToast("terminal", "coder-1")).toBe("Opening terminal for coder-1");
  });

  it("distinguishes queued from started", () => {
    expect(startedToast({ name: "o", state: "queued" }, "EPIC-3")).toBe("Queued o. It starts when an agent slot becomes available.");
    expect(startedToast({ name: "o", state: "active" }, "EPIC-3")).toBe("Started o on EPIC-3");
  });
});
```

- [ ] **Step 2: RED** — `pnpm vitest run src/logic/toasts.test.ts` → FAIL (module missing).
- [ ] **Step 3: Implement** — `web/src/logic/toasts.ts`:

```ts
import { T } from "../copy";
import type { AgentEndpoint, AgentNode } from "../types";

export type AgentVerb = AgentEndpoint;

export function agentActionToast(verb: AgentVerb, name: string, scope?: "session" | "subtree"): string {
  switch (verb) {
    case "pause": return scope === "subtree" ? T.toastPausedGroup(name) : T.toastPaused(name);
    case "resume": return T.toastResumed(name);
    case "cancel": return T.toastCancelled(name);
    case "ack": return T.toastAcked(name);
    case "retry": return T.toastRetrying(name);
    case "terminal": return T.toastTerminal(name);
  }
}

export const startedToast = (agent: Pick<AgentNode, "name" | "state">, itemKey: string): string =>
  agent.state === "queued" ? T.toastQueued(agent.name) : T.toastStarted(agent.name, itemKey);
```

- [ ] **Step 4: GREEN** — PASS.
- [ ] **Step 5: Commit** — `git add src/logic/toasts.ts src/logic/toasts.test.ts && git commit -m "feat(web): toast copy builders"`

### Unit B3: Repository chooser rules

- [ ] **Step 1: Failing test** — in `web/src/logic/repos.test.ts`, add (keep the existing `toggleRepo`/`selectedLine`/`scanLine` assertions):

```ts
import { chooserRows, reconcileSelection, shortPath } from "./repos";

describe("repo chooser (menubar parity)", () => {
  const repo = (id: string, name: string, path: string, missing = false) =>
    ({ id, name, path, missing, dirty: false, remote_owner: null }) as unknown as Repo;

  it("shortens home paths only", () => {
    expect(shortPath("/Users/alex/GitHub/agent-swarm")).toBe("~/GitHub/agent-swarm");
    expect(shortPath("/opt/src/tool")).toBe("/opt/src/tool");
  });

  it("uses all, drops missing, dedupes by path, sorts by name then path", () => {
    const r = {
      recent: [repo("x", "zzz", "/Users/a/zzz")], groups: [],
      all: [
        repo("b", "beta", "/Users/a/beta"),
        repo("a2", "alpha", "/Users/a/work/alpha"),
        repo("a1", "alpha", "/Users/a/alpha"),
        repo("dup", "beta", "/Users/a/beta"),
        repo("gone", "aaa", "/Users/a/aaa", true),
      ],
      scanning: false, scanned_at: null,
    } as unknown as ReposResponse;
    expect(chooserRows(r).map((x) => x.id)).toEqual(["a1", "a2", "b"]);
  });

  it("drops selected ids that are no longer rows", () => {
    const rows = [repo("a", "a", "/a"), repo("b", "b", "/b")];
    expect(reconcileSelection(["a", "c", "d"], rows)).toEqual({ selection: ["a"], removed: 2 });
    expect(reconcileSelection(["b", "a"], rows)).toEqual({ selection: ["b", "a"], removed: 0 });
  });
});
```

Rewrite the existing "abbreviates the parent folder and builds the subtitle" test to assert `shortPath` on the same fixture paths (spec Deleted #3). Delete the "orders sections Recent, local groups…" test and the `selectAll` assertions (spec Deleted #1, #2) — nothing else.

- [ ] **Step 2: RED** — `pnpm vitest run src/logic/repos.test.ts` → FAIL (exports missing).
- [ ] **Step 3: Implement** — in `web/src/logic/repos.ts` add:

```ts
export const shortPath = (path: string) => path.replace(/^\/Users\/[^/]+/, "~");

export function chooserRows(r: ReposResponse): Repo[] {
  const byPath = new Map<string, Repo>();
  for (const repo of r.all) if (!repo.missing && !byPath.has(repo.path)) byPath.set(repo.path, repo);
  return [...byPath.values()].sort((a, b) => a.name.localeCompare(b.name) || a.path.localeCompare(b.path));
}

export function reconcileSelection(sel: string[], rows: Repo[]): { selection: string[]; removed: number } {
  const ids = new Set(rows.map((r) => r.id));
  const selection = sel.filter((id) => ids.has(id));
  return { selection, removed: sel.length - selection.length };
}
```

Delete `repoSections`, `RepoSection`, `selectAll`, `repoSubtitle`, and `parentFolder` **only if** `grep -rn 'repoSections\|selectAll\|repoSubtitle\|parentFolder' src` shows no caller outside `RepoPicker.tsx` and the test. `RepoPicker.tsx` still imports them until D3 — so in this unit keep them, and D3 deletes them together with their last caller.

- [ ] **Step 4: GREEN** — PASS.
- [ ] **Step 5: Commit** — `git add src/logic/repos.ts src/logic/repos.test.ts && git commit -m "feat(web): flat repository chooser rules"`

### Unit B4: Advisor split and chore intent

- [ ] **Step 1: Failing tests** — append to `web/src/logic/catalog.test.ts`:

```ts
import { seed } from "../mock/fixtures";
import { advisorAgentOptions, advisorModelOptions, changeAdvisorAgent } from "./catalog";

describe("advisor split (menubar parity)", () => {
  const db = seed();

  it("offers enabled agents plus No advisor", () => {
    expect(advisorAgentOptions(["claude", "codex"]).map((o) => o.label)).toEqual(["Claude", "Codex", "No advisor"]);
    expect(advisorAgentOptions(["claude"]).at(-1)?.value).toBe("none");
  });

  it("lists only advisor-capable Claude models", () => {
    const labels = advisorModelOptions(db.catalog, "claude").map((o) => o.label);
    expect(labels).not.toContain("Haiku 4.5");
    expect(labels.length).toBeGreaterThan(0);
  });

  it("keeps the Settings advisor model for the Settings agent, else the first capable model", () => {
    expect(changeAdvisorAgent("claude", db.settings, db.catalog)).toEqual({ agent: "claude", model: db.settings.roles.advisor?.model });
    const codex = changeAdvisorAgent("codex", db.settings, db.catalog);
    expect(codex).toEqual({ agent: "codex", model: advisorModelOptions(db.catalog, "codex")[0]?.value });
    expect(changeAdvisorAgent("none", db.settings, db.catalog)).toBe("none");
  });
});
```

Append to `web/src/logic/spawnForm.test.ts`:

```ts
it("sends the chore intent unchanged", () => {
  const db = seed();
  const fields = prefill(db.settings, "orchestrator", db.catalog);
  const body = spikePayload({ name: "Tidy deps", intent: "chore", repos: [], request: "", fields }, db.settings, db.catalog, "req-1");
  expect(body.intent).toBe("chore");
});
```

Append to `web/src/mock/daemon.test.ts` a case posting `intent: "chore"` to `POST /api/spikes` and asserting the created item's `spike_intent === "chore"` (copy the file's existing create-spike case and change the intent).

- [ ] **Step 2: RED** — `pnpm vitest run src/logic/catalog.test.ts src/logic/spawnForm.test.ts src/mock/daemon.test.ts` → FAIL (missing exports; `"chore"` type error surfaces in `pnpm typecheck`).
- [ ] **Step 3: Implement** — `types.ts`: add `export type SpikeIntent = "chore" | "feature" | "debug";` and set `CreateSpikeBody.intent: SpikeIntent`. `spawnForm.ts`: `SpikeFormState.intent: SpikeIntent`. `catalog.ts`:

```ts
export const advisorAgentOptions = (enabled: AgentKind[]): Option[] => [
  ...agentOptions(enabled),
  { value: "none", label: C.noAdvisor },
];

// Claude alone filters to advisor-capable models (preserves advisorOptions' existing rule).
export const advisorModelOptions = (catalog: AgentCatalogEntry[], agent: AgentKind): Option[] =>
  modelOptions(entryFor(catalog, agent), agent === "claude");

export function changeAdvisorAgent(value: AgentKind | "none", settings: Settings, catalog: AgentCatalogEntry[]): AdvisorChoice {
  if (value === "none") return "none";
  const models = advisorModelOptions(catalog, value).filter((o) => !o.disabled);
  const saved = settings.roles.advisor;
  if (saved?.agent === value && models.some((o) => o.value === saved.model)) return { agent: value, model: saved.model };
  const first = models[0];
  return first ? { agent: value, model: first.value } : "none";
}
```

If the mock daemon test fails because the mock validates intent, widen that check to accept `"chore"`.

- [ ] **Step 4: GREEN** — the three files PASS; `pnpm typecheck` PASS.
- [ ] **Step 5: Commit** — `git add src/types.ts src/logic/spawnForm.ts src/logic/spawnForm.test.ts src/logic/catalog.ts src/logic/catalog.test.ts src/mock/daemon.ts src/mock/daemon.test.ts && git commit -m "feat(web): advisor agent/model split and chore intent"`

**Package B verify:** `pnpm typecheck && pnpm test`.

---

## Package C · Primitives (after A)

**Files:** `components/Sheet.tsx`, `components/Segmented.tsx`, `components/MoveToMenu.tsx`, `components/StatusLabel.tsx`, `components/Banners.tsx`, `components/primitives.test.tsx`, plus callers' tests that break from role changes (`Details.test.tsx`, `Kanban.test.tsx`, `Hierarchy.test.tsx` for MoveToMenu; header tests for Segmented).

**Interfaces:** consumes A's `ui/*`, `cn`. Produces unchanged public props for `Sheet` (+ `modal?: boolean`), `Segmented`, `MoveToMenu`, `StatusPill`, `StateDot`, banners.

### Unit C1: Sheet wrapper

- [ ] **Step 1: Port the test first** — in `primitives.test.tsx` `describe("Sheet")`: keep "closes with the button and Escape" as is (inline width stays, so `toHaveStyle({ width: "420px" })` still holds). Change the focus test's first expectation from `Close` having focus to:

```ts
expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true);
```

Add a headerless case (`header={false}`: `getByRole("dialog", { name: "Details" })` resolves, and no visible `heading` with that text exists) and:

```tsx
it("non-modal sheet has no overlay and ignores outside clicks", async () => {
  const onClose = vi.fn();
  const user = userEvent.setup();
  render(<><button type="button">outside</button><Sheet title="Details" modal={false} onClose={onClose}>body</Sheet></>);
  expect(document.querySelector("[data-slot=sheet-overlay]")).toBeNull();
  await user.click(screen.getByRole("button", { name: "outside" }));
  expect(onClose).not.toHaveBeenCalled();
  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalledTimes(1);
});
```

- [ ] **Step 2: RED** — `pnpm vitest run src/components/primitives.test.tsx -t Sheet` → the new test FAILS (custom sheet has no `modal` prop; outside click semantics absent / overlay selector check depends on shadcn).
- [ ] **Step 3: Implement** — `components/Sheet.tsx`:

```tsx
import type { ReactNode } from "react";
import { Sheet as UiSheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";

export function Sheet(p: {
  title: string; subtitle?: string; width?: number; modal?: boolean; header?: boolean;
  onClose(): void; children: ReactNode; footer?: ReactNode;
}) {
  const modal = p.modal ?? true;
  // Non-modal (Details): the board stays usable, so an outside click must not dismiss the sheet.
  const keepOpen = modal ? undefined : (e: Event) => e.preventDefault();
  return (
    <UiSheet open modal={modal} onOpenChange={(open) => { if (!open) p.onClose(); }}>
      <SheetContent
        side="right"
        overlay={modal}
        aria-label={p.title}
        style={{ width: p.width ? `${p.width}px` : undefined }}
        className="flex w-full max-w-full flex-col gap-0 bg-card p-0 sm:max-w-full"
        onInteractOutside={keepOpen}
        onPointerDownOutside={keepOpen}
        {...(p.subtitle ? {} : { "aria-describedby": undefined })}
      >
        {p.header === false ? (
          // Details draws its own header row; Radix still needs a title for the dialog's accessible name.
          <SheetTitle className="sr-only">{p.title}</SheetTitle>
        ) : (
          <SheetHeader className="border-b border-border px-5 py-4">
            <SheetTitle className="text-[15px] font-semibold">{p.title}</SheetTitle>
            {p.subtitle && <SheetDescription>{p.subtitle}</SheetDescription>}
          </SheetHeader>
        )}
        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">{p.children}</div>
        {p.footer && <SheetFooter className="flex-row items-center justify-end gap-2 border-t border-border px-5 py-3">{p.footer}</SheetFooter>}
      </SheetContent>
    </UiSheet>
  );
}
```

The generated close button (`sr-only` text "Close") satisfies `getByRole("button", { name: "Close" })`; if the generated label differs, set it to `C.close`.

- [ ] **Step 4: GREEN** — `pnpm vitest run src/components/primitives.test.tsx` PASS; `pnpm test` — fix any sheet-caller test that asserted the old `aside`/focus target by porting it to `getByRole("dialog", { name })`.
- [ ] **Step 5: Commit** — `git add src/components/Sheet.tsx src/components/primitives.test.tsx <ported tests> && git commit -m "feat(web): Sheet wrapper on shadcn Sheet with non-modal mode"`

### Unit C2: Segmented → ToggleGroup

- [ ] **Step 1: Port the test** — `describe("Segmented")`: keep `getByRole("radio", …)` + `aria-checked` (Radix single ToggleGroup items are `role="radio"`). Change `getByRole("radiogroup", { name: "Scope" })` → `getByRole("group", { name: "Scope" })`. Add: clicking the already-selected option keeps it selected (Radix emits `""` on deselect; we must ignore it):

```tsx
await user.click(screen.getByRole("radio", { name: "Neighbourhood" }));
expect(screen.getByRole("radio", { name: "Neighbourhood" })).toHaveAttribute("aria-checked", "true");
```

- [ ] **Step 2: RED** — FAIL on the `group` role.
- [ ] **Step 3: Implement** — `components/Segmented.tsx`:

```tsx
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

export function Segmented<T extends string>(p: {
  label: string; value: T; options: { value: T; label: string }[]; onChange(v: T): void; disabled?: boolean;
}) {
  return (
    <ToggleGroup
      type="single"
      variant="outline"
      size="sm"
      aria-label={p.label}
      value={p.value}
      disabled={p.disabled}
      // Radix sends "" when the active item is clicked again; a segmented control never deselects.
      onValueChange={(v) => { if (v) p.onChange(v as T); }}
    >
      {p.options.map((o) => (
        <ToggleGroupItem key={o.value} value={o.value} className="h-7 px-3 data-[state=on]:bg-accent data-[state=on]:text-foreground">
          {o.label}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  );
}
```

- [ ] **Step 4: GREEN** — `pnpm test` (grep callers' tests for `radiogroup` and port each to `group`).
- [ ] **Step 5: Commit** — `git commit -m "feat(web): Segmented renders a shadcn ToggleGroup"` with explicit paths.

### Unit C3: MoveToMenu → DropdownMenu

- [ ] **Step 1: Failing test** — add `describe("MoveToMenu")` to `primitives.test.tsx`:

```tsx
it("lists moves, disables locked ones with their reason, and reports the pick", async () => {
  const user = userEvent.setup();
  const onMove = vi.fn();
  const item = { key: "TASK-1", type: "task", status: "in_progress", revision: 1 } as unknown as Movable;
  render(<MoveToMenu item={item} onMove={onMove} />);
  await user.click(screen.getByRole("button", { name: "Move to…" }));
  const items = await screen.findAllByRole("menuitem");
  expect(items.length).toBeGreaterThan(0);
  const locked = items.find((i) => i.getAttribute("aria-disabled") === "true");
  if (locked) expect(locked.textContent).toMatch(/\S/);
  const open = items.find((i) => i.getAttribute("aria-disabled") !== "true");
  await user.click(open!);
  expect(onMove).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button", { name: "Move to…" })).toHaveFocus();
});
```

(Use `C.moveTo`'s actual string for the button name; import `Movable` from `../logic/transitions`. Pick a fixture status that yields at least one enabled and one locked option — read `moveOptions` to choose.)

- [ ] **Step 2: RED** — hand-rolled items are `disabled` buttons, not `aria-disabled` menuitems → FAIL.
- [ ] **Step 3: Implement** — `components/MoveToMenu.tsx`:

```tsx
import { ChevronDown, Lock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { C } from "../copy";
import { type MoveCheck, type Movable, moveOptions } from "../logic/transitions";
import type { ItemStatus } from "../types";

export function MoveToMenu(p: { item: Movable; onMove(status: ItemStatus, check: MoveCheck): void; disabled?: boolean; buttonLabel?: string; ariaLabel?: string }) {
  const enabled = (c: MoveCheck) => c.ok || c.special !== undefined;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" disabled={p.disabled} aria-label={p.ariaLabel}>
          {p.buttonLabel ?? C.moveTo}
          <ChevronDown className="size-3.5 opacity-60" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        {moveOptions(p.item).map((o) => (
          <DropdownMenuItem key={o.status} disabled={!enabled(o.check)} onSelect={() => p.onMove(o.status, o.check)} className="flex-col items-start gap-0.5">
            <span className="flex items-center gap-1.5">
              {!enabled(o.check) && <Lock aria-hidden className="size-3" />}
              {o.label}
            </span>
            {!o.check.ok && <span className="text-xs text-muted-foreground">{o.check.reason}</span>}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
```

- [ ] **Step 4: GREEN** — `pnpm test`. Port `Details.test.tsx` / `Kanban.test.tsx` / `Hierarchy.test.tsx` cases that clicked `getByRole("menuitem")` then expected `disabled` → `aria-disabled="true"`.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): Move to menu on shadcn DropdownMenu"`.

### Unit C4: StatusLabel → Badge, Banners → Alert

- [ ] **Step 1: Test** — extend `describe("icons and labels")`:

```tsx
it("renders the status as a badge with a tone", () => {
  render(<StatusPill status="awaiting_approval" />);
  const pill = screen.getByText("Awaiting approval");
  expect(pill.closest("[data-slot=badge]")).toHaveAttribute("data-tone", "warning");
});
```

and a Banners test: `render(<ConnectionBanner onRetry={fn} />)`; expect `getByRole("alert")` and a Retry button that calls `fn`.

- [ ] **Step 2: RED** — no `data-slot=badge` / no `role=alert` → FAIL.
- [ ] **Step 3: Implement** — add tone variants to `ui/badge.tsx` `badgeVariants`:

```ts
success: "border-success/30 bg-success/10 text-success",
warning: "border-warning/30 bg-warning/10 text-warning",
info: "border-info/30 bg-info/10 text-info",
```

`StatusPill` maps `ItemStatus` → tone (`awaiting_approval`/`blocked` → `warning`, `in_progress` → `info`, `done` → `success`, `cancelled` → `outline`, others → `outline`) and renders `<Badge variant={tone} data-tone={tone}>{STATUS_LABEL[status]}</Badge>`. `StateDot` keeps its markup; swap its colour classes to `bg-success`/`bg-warning`/`bg-destructive`/`bg-muted-foreground`/`bg-info` per `stateTone`. Banners: wrap each in `<Alert variant={destructive ? "destructive" : "default"}>` with `AlertDescription` and a `Button variant="link" size="sm"` for Retry/actions.
- [ ] **Step 4: GREEN** — `pnpm test`.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): status badges and alert banners"`.

**Package C verify:** `pnpm typecheck && pnpm test && pnpm build`; screenshot board + one open sheet.

---

## Package D · Forms (after B + C)

**Files:** `panels/NewItemSheet.tsx(+test)`, `components/AgentFields.tsx(+test)`, `components/RepoPicker.tsx(+test)`, `logic/repos.ts` (deletions), `panels/NewSpikeSheet.tsx(+test)`, `panels/SpawnSheet.tsx(+test)`, `views/props.ts`, `App.tsx` (sheet-state lines only), `App.flows.test.tsx`, `copy.ts` (removals only: `newSpike`, `spikeViaNewItem`, `searchRepos`, `recent` once unused).

**Interfaces consumed:** B's `chooserRows`, `shortPath`, `reconcileSelection`, `advisorAgentOptions`, `advisorModelOptions`, `changeAdvisorAgent`, `startedToast`, `SpikeIntent`, copy keys; C's `Sheet`, `Segmented`; A's `pickOption`/`comboText`/`optionTexts`, `useToast().success`.

**Shared form idiom** (use in every D unit):

```tsx
<div className="space-y-1.5">
  <Label htmlFor={id}>{C.title}</Label>
  <Input id={id} … className="h-8" />
  {error && <p className="text-xs text-destructive">{error}</p>}
</div>
```

Footer: `<Button variant="secondary" onClick={onClose}>{C.cancel}</Button><Button disabled={…} onClick={…}>{label}</Button>`. Failure banner: `<Alert variant="destructive" role="alert">`.

### Unit D1: New item sheet

- [ ] **Step 1: Port tests** — in `NewItemSheet.test.tsx` replace every `user.selectOptions(screen.getByRole("combobox", { name: "Parent" }), X)` with `await pickOption(user, "Parent", X)` and `select.value` reads with `comboText("Parent")`. Add:

```tsx
it("toasts the created key", async () => {
  const onCreated = vi.fn();
  const { user } = renderWithDaemon(<NewItemSheet type="task" onClose={vi.fn()} onCreated={onCreated} />, { events: false });
  await user.type(await screen.findByRole("textbox", { name: "Title" }), "Wire banner");
  await user.click(screen.getByRole("button", { name: "Create item" }));
  await waitFor(() => expect(onCreated).toHaveBeenCalled());
  expect(await screen.findByText(`Created ${onCreated.mock.calls[0][0]}`)).toBeInTheDocument();
});
```

(Use the actual `C.createItem` string and a parent if `task` requires one — mirror the file's existing happy-path test.)
- [ ] **Step 2: RED** — no toast; `pickOption` fails on native select → FAIL.
- [ ] **Step 3: Implement** — `NewItemSheet`: `width={480}`; Parent becomes `Select` with `NONE` sentinel:

```tsx
const NONE = "__none";
<Select value={parentKey || NONE} onValueChange={(v) => { setParentTouched(true); set({ parentKey: v === NONE ? "" : v }); }}>
  <SelectTrigger id={parentId} aria-label={C.parent} className="h-8 w-full"><SelectValue /></SelectTrigger>
  <SelectContent>
    <SelectItem value={NONE}>—</SelectItem>
    {parentOptions(all, form.type).map((i) => <SelectItem key={i.key} value={i.key}>{`${i.key} · ${i.title}`}</SelectItem>)}
  </SelectContent>
</Select>
```

Inputs → `Input`/`Textarea` + `Label`; acceptance remove → `Button variant="ghost" size="icon"` with `Minus` icon + `Tooltip`, keeping `aria-label`s; add → `Button variant="ghost" size="sm"` "+ Add criterion" keeping `aria-label={`Add ${C.acceptance}`}`. After `create.run` resolves: `toast.success(T.toastItemCreated(item.key))` then `p.onCreated(item.key)`.
- [ ] **Step 4: GREEN** — `pnpm vitest run src/panels/NewItemSheet.test.tsx` PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): New item sheet on shadcn fields with created toast"`.

### Unit D2: AgentFields grid, advisor split, Collapsible roles

- [ ] **Step 1: Port tests** — `AgentFields.test.tsx`: replace the native helpers:

```ts
import { comboText, optionTexts, pickOption } from "../test/select";
// selectedText(name) → comboText(name); [...select(n).options] → await optionTexts(user, n); user.selectOptions → pickOption
```

Rewrite advisor assertions to the split:

```tsx
it("splits advisor into agent and model", async () => {
  const user = userEvent.setup();
  render(<Host />);
  expect(comboText("Advisor")).toBe("Claude");
  expect(comboText("Advisor model")).toBe("Fable (latest)");
  expect(await optionTexts(user, "Advisor")).toEqual(["Claude", "Codex", "No advisor"]);
  expect(await optionTexts(user, "Advisor model")).not.toContain("Haiku 4.5");
  await pickOption(user, "Advisor", "No advisor");
  expect(screen.getByRole("combobox", { name: "Advisor model" })).toBeDisabled();
  expect(comboText("Advisor model")).toBe("—");
  await pickOption(user, "Advisor", "Codex");
  expect(comboText("Advisor model")).toBe("GPT-6 Astra");
});
```

(Fixture labels: confirm against `mock/fixtures.ts`; the first test in the file shows "Fable (latest)" and "GPT-6 Astra".) Port the worker-roles `<details>` test to click `getByRole("button", { name: "Worker roles" })` and assert `aria-expanded`.
- [ ] **Step 2: RED** — FAIL (no "Advisor model" combobox).
- [ ] **Step 3: Implement** — `Field` becomes a grid row using `Select`; the whole block is a 4-column grid so Agent and Advisor rows align:

```tsx
const ROW = "grid grid-cols-[72px_150px_52px_minmax(0,1fr)] items-center gap-x-2 gap-y-1";

function Pick(p: { label: string; value: string; options: Option[]; onChange(v: string): void; disabled?: boolean; placeholder?: string; icon?: boolean }) {
  const known = p.options.some((o) => o.value === p.value);
  return (
    <Select value={p.disabled ? "" : p.value} onValueChange={p.onChange} disabled={p.disabled}>
      <SelectTrigger aria-label={p.label} className="h-8 w-full"><SelectValue placeholder={p.placeholder} /></SelectTrigger>
      <SelectContent>
        {!known && p.value && <SelectItem value={p.value}>{p.value}</SelectItem>}
        {p.options.map((o) => (
          <SelectItem key={o.value} value={o.value} disabled={o.disabled}>
            {p.icon && o.value !== "none" ? <span className="flex items-center gap-2"><AgentIcon kind={o.value as AgentKind} />{o.label}</span> : o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
```

Rows:

```tsx
<div className={ROW}>
  <Label>{C.agent}</Label>
  <Pick label={C.agent} icon value={choice.agent} options={agentOptions(p.settings.enabled_agents)} onChange={…changeAgent…} />
  <Label>{C.model}</Label>
  <Pick label={C.model} value={choice.model} options={modelOptions(entry)} onChange={…changeModel…} />
  {(errors.agent || agentError || modelErr) && <p className="col-start-2 col-end-5 text-xs text-destructive">{…}</p>}
  {efforts && (<><Label>{C.effort}</Label><span className="col-span-2" /><Pick label={C.effort} value={choice.effort} options={efforts} onChange={…} /></>)}
  {note && <p className="col-start-4 text-xs text-muted-foreground">{note}</p>}
  <Label>{C.advisor}</Label>
  <Pick label={C.advisor} icon value={advisor === "none" ? "none" : advisor.agent} options={advisorAgentOptions(p.settings.enabled_agents)}
        onChange={(v) => p.onChange({ ...p.value, advisor: changeAdvisorAgent(v as AgentKind | "none", p.settings, p.catalog) })} />
  <Label>{C.model}</Label>
  <Pick label={C.advisorModel} value={advisor === "none" ? "" : advisor.model} placeholder="—" disabled={advisor === "none"}
        options={advisor === "none" ? [] : advisorModelOptions(p.catalog, advisor.agent)}
        onChange={(m) => advisor !== "none" && p.onChange({ ...p.value, advisor: { agent: advisor.agent, model: m } })} />
  {errors.advisor && <p className="col-start-2 col-end-5 text-xs text-destructive">{errors.advisor}</p>}
</div>
<p className="text-xs text-muted-foreground">{C.defaultsFromSettings}</p>
```

The Effort label keeps `C.effort` ("Effort") so existing `comboText("Effort")` tests hold; label in column 1, control under the Model column, per the spec sketch. Worker roles: `Collapsible` with `CollapsibleTrigger asChild` → `Button variant="ghost" size="sm"` (chevron rotates on `data-state=open`), each role a `fieldset` using the same `ROW` grid (Agent / Model, Effort under Model). Remove `advisorOptions`/`encodeAdvisor`/`decodeAdvisor` imports from this file; keep those functions in `catalog.ts` only if another caller remains (grep), else delete them and port/delete their `catalog.test.ts` cases **as replaced by the split tests in B4** — state that in the commit body.
- [ ] **Step 4: GREEN** — `pnpm vitest run src/components/AgentFields.test.tsx` PASS; `pnpm typecheck`.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): aligned agent/advisor rows with split advisor menus"`.

### Unit D3: Repository chooser

- [ ] **Step 1: Rewrite `RepoPicker.test.tsx`** to the flat list (spec Deleted #4 removes only the "searches" case and the section/group assertions):

```tsx
const rows = () => within(screen.getByRole("listbox", { name: "Repositories (optional)" })).getAllByRole("option");

it("lists each present repo once, sorted, with its path", async () => {
  renderWithDaemon(<Host />, { events: false });
  await screen.findByRole("listbox", { name: "Repositories (optional)" });
  const names = rows().map((r) => r.getAttribute("data-name"));
  expect(names).toEqual([...names].sort((a, b) => a!.localeCompare(b!)));
  expect(names).not.toContain("old-api");                  // missing → omitted
  expect(new Set(names).size).toBe(names.length);          // once each
  expect(screen.getAllByText(/^~\/GitHub\//).length).toBeGreaterThan(0);
});

it("toggles rows by click and Space and summarises", async () => {
  const { user } = renderWithDaemon(<Host />, { events: false });
  await screen.findByRole("listbox");
  const chat = screen.getByRole("option", { name: /endurio-chat/ });
  await user.click(chat);
  expect(chat).toHaveAttribute("aria-selected", "true");
  expect(screen.getByText("Selected: endurio-chat")).toBeInTheDocument();
  chat.focus();
  await user.keyboard(" ");
  expect(chat).toHaveAttribute("aria-selected", "false");
});

it("notices a selected repo that disappears after rescan", async () => {
  const daemon = createMockDaemon();
  const { user } = renderWithDaemon(<Host initial={["repo_chat"]} />, { daemon, events: false });
  await screen.findByRole("listbox");
  daemon.db.repos.all = daemon.db.repos.all.filter((r) => r.id !== "repo_chat");
  await user.click(screen.getByRole("button", { name: "Rescan" }));
  expect(await screen.findByText("1 selected repository is no longer available.")).toBeInTheDocument();
});

it("keeps the list box for empty, scanning and failed states", async () => {
  const daemon = createMockDaemon();
  daemon.db.repos.all = []; daemon.db.repos.recent = []; daemon.db.repos.groups = [];
  renderWithDaemon(<Host />, { daemon, events: false });
  expect(await screen.findByText("No repositories found.")).toBeInTheDocument();
});
```

Keep and port "adds a folder and rescans" and "shows the daemon's reason…" (they now also expect `Added newrepo` / `Found … repositories` toasts), and "shows the scanning footer". Dirty repos: assert the row contains an element with `aria-label` = `C.repoDirty` (replaces the old inline caption).
- [ ] **Step 2: RED** — no listbox → FAIL.
- [ ] **Step 3: Implement** — `components/RepoPicker.tsx`:

```tsx
export function RepoPicker(p: { selected: string[]; onChange(ids: string[]): void; label: string; caption?: string }) {
  const toast = useToast();
  const [adding, setAdding] = useState(false);
  const [path, setPath] = useState("");
  const [addError, setAddError] = useState("");
  const [notice, setNotice] = useState("");
  const repos = useRepos("");
  const add = useMutation((api, folder: string) => api.addRepo(folder), ["repos:"]);
  const rescan = useMutation((api) => api.rescanRepos(), ["repos:"]);
  const data = repos.data;
  const rows = useMemo(() => (data ? chooserRows(data) : []), [data]);
  const labelId = useId();

  // Menubar parity: reconcile after an explicit rescan only (not on every load), so a just-added
  // folder is never dropped by a refetch that has not caught up yet.
  const [reconcilePending, setReconcilePending] = useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: selection edits must not re-trigger reconciliation
  useEffect(() => {
    if (!reconcilePending || !data || data.scanning) return;
    setReconcilePending(false);
    const r = reconcileSelection(p.selected, rows);
    if (r.removed > 0) { p.onChange(r.selection); setNotice(T.reposNoLonger(r.removed)); }
  }, [rows]);

  const toggle = (id: string) => { setNotice(""); p.onChange(toggleRepo(p.selected, id)); };
  …
  return (
    <fieldset className="space-y-1.5">
      <div className="flex items-baseline justify-between">
        <legend id={labelId} className="font-medium">{p.label}</legend>
        {p.selected.length > 0 && <span className="text-xs text-muted-foreground">{p.selected.length} selected</span>}
      </div>
      {p.caption && <p className="text-xs text-muted-foreground">{p.caption}</p>}
      <ScrollArea className="h-[242px] rounded-md border border-border">   {/* 8 × 30px + border */}
        <div role="listbox" aria-multiselectable aria-labelledby={labelId} className="p-0.5">
          {!data && repos.error && <State text={C.reposUnavailable} onRetry={repos.reload} />}
          {data && rows.length === 0 && <State text={data.scanning ? C.reposScanning : C.reposEmpty} />}
          {rows.map((r) => {
            const on = p.selected.includes(r.id);
            return (
              <div key={r.id} role="option" aria-selected={on} data-name={r.name} tabIndex={0}
                   onClick={() => toggle(r.id)}
                   onKeyDown={(e) => { if (e.key === " " || e.key === "Enter") { e.preventDefault(); toggle(r.id); } }}
                   className={cn("flex h-[30px] cursor-default items-center gap-2 rounded-sm px-2 outline-none focus-visible:ring-2 focus-visible:ring-ring", on ? "bg-accent" : "hover:bg-accent/60")}>
                <Check aria-hidden className={cn("size-3.5 text-link", !on && "invisible")} />
                <span className="w-44 truncate font-medium">{r.name}</span>
                <span className="key truncate text-muted-foreground">{shortPath(r.path)}</span>
                {r.dirty && <span aria-label={C.repoDirty} title={C.repoDirty} className="ml-auto size-1.5 shrink-0 rounded-full bg-warning" />}
              </div>
            );
          })}
        </div>
      </ScrollArea>
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="sm" onClick={() => setAdding((a) => !a)}>{C.addFolder}</Button>
        <span className="ml-auto text-xs text-muted-foreground">{data ? scanLine(data) : ""}</span>
        <Button variant="ghost" size="sm" disabled={rescan.pending} onClick={() => void doRescan()}>{C.rescan}</Button>
      </div>
      {adding && ( /* form: Input aria-label={C.addFolder} + Button size="sm" "Add"; addError in text-xs text-destructive */ )}
      {notice && <p className="text-xs text-muted-foreground">{notice}</p>}
      <p className="text-xs">{selectedLine(p.selected, rows)}</p>
    </fieldset>
  );
}
```

with `doRescan = async () => { try { const r = await rescan.run(); setReconcilePending(true); toast.success(T.toastRescanned(r.found, r.missing)); } catch (e) { toast.error(errorText(e)); } }`, `submitFolder` adding `toast.success(T.toastRepoAdded(repo.name))` on success, and `State` a centered `text-muted-foreground` line (+ `Button variant="link" size="sm"` Retry). The inline `/* form … */` above is the existing add-folder `<form>` with `Input`/`Button` swapped in — keep its submit/error logic byte-for-byte. Then delete `repoSections`, `RepoSection`, `selectAll`, `repoSubtitle`, `knownRepos`, `parentFolder` from `logic/repos.ts` if grep shows no other caller, and `C.searchRepos`/`C.recent` if unused (update `copy.test.ts` if it lists them).
- [ ] **Step 4: GREEN** — `pnpm vitest run src/components/RepoPicker.test.tsx src/logic/repos.test.ts` PASS.
- [ ] **Step 5: Commit** — body lists the deleted helpers and the removed "searches"/sections test cases with the spec reference. `git commit -m "feat(web): flat repository chooser matching the menubar"`.

### Unit D4: New orchestrator sheet

- [ ] **Step 1: Port tests** — `NewSpikeSheet.test.tsx`: replace the `caption=` prop usage with `intent="feature"`; update title expectations to "New orchestrator". Add:

```tsx
it("offers Chore and sends it", async () => {
  const onCreated = vi.fn();
  const { user, daemon } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, { events: false });
  const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
  await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Tidy deps");
  await user.click(within(sheet).getByRole("radio", { name: "Chore" }));
  expect(within(sheet).getByText("Creates a top-level chore orchestrator for maintenance, refactoring, or general work.")).toBeInTheDocument();
  await user.click(within(sheet).getByRole("button", { name: /Start|Queue/ }));
  await waitFor(() => expect(onCreated).toHaveBeenCalled());
  expect(daemon.calls.find((c) => c.path === "/api/spikes")?.body).toMatchObject({ intent: "chore" });
  expect(await screen.findByText(/^(Started tidy-deps on |Queued tidy-deps\.)/)).toBeInTheDocument();
});

it("presets the intent", async () => {
  renderWithDaemon(<NewSpikeSheet intent="chore" onClose={vi.fn()} onCreated={vi.fn()} />, { events: false });
  expect(await screen.findByRole("radio", { name: "Chore" })).toHaveAttribute("aria-checked", "true");
});
```

(Check `daemon.calls[].body` shape in `mock/daemon.ts`; the existing "creates the spike…" test shows how the file asserts the payload — follow it.)
- [ ] **Step 2: RED** — FAIL (no Chore, wrong title, no `intent` prop).
- [ ] **Step 3: Implement** — `NewSpikeSheet`: props `{ intent?: SpikeIntent; onClose(); onCreated(key) }` (drop `caption`); `useState<SpikeIntent>(p.intent ?? "feature")`; title `C.newOrchestrator`; `width={720}`; intent row:

```tsx
<div className="grid grid-cols-[72px_1fr] items-start gap-x-2 gap-y-1">
  <Label className="h-7 leading-7">{C.intent}</Label>
  <Segmented<SpikeIntent> label={C.intent} value={intent} onChange={setIntent}
    options={[{ value: "chore", label: C.choreIntent }, { value: "feature", label: C.featureSpike }, { value: "debug", label: C.debugSpike }]} />
  <p className="col-start-2 text-xs text-muted-foreground">{intent === "chore" ? C.choreCaption : intent === "feature" ? C.featureCaption : C.debugCaption}</p>
</div>
```

Request `Textarea rows={5}`; footer caption `C.queuedCaption` left (`mr-auto text-xs text-muted-foreground`) when busy; buttons `Button variant="secondary"` / `Button`. Failure banner → `Alert variant="destructive"`. On success: `toast.success(startedToast(r.agent, r.item.key))` then `p.onCreated(r.item.key)`. The load-error branch uses `Alert` + `Button variant="link"` Retry.
- [ ] **Step 4: GREEN** — PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): New orchestrator sheet with chore intent and start toast"`.

### Unit D5: Start orchestrator sheet + App sheet state

- [ ] **Step 1: Tests** — `SpawnSheet.test.tsx`: port `selectOptions` calls to `pickOption`; add a success-toast assertion (`findByText(/^(Started|Queued) /)`) to its happy path. `App.flows.test.tsx`: port the "New item → Spike" flow to expect `dialog` named "New orchestrator" with `radio "Feature spike"` checked, and add "New item → Chore" expecting `radio "Chore"` checked. Header button name "New spike" → "New orchestrator" in any App test.
- [ ] **Step 2: RED** — FAIL.
- [ ] **Step 3: Implement** — `views/props.ts`: `{ kind: "spike"; intent?: SpikeIntent }`. `App.tsx`:

```tsx
const newItem = (type: ItemType, parentKey?: string) =>
  setSheet(type === "spike" ? { kind: "spike", intent: "feature" } : type === "chore" ? { kind: "spike", intent: "chore" } : { kind: "item", type, parentKey });
…
{sheet?.kind === "spike" && <NewSpikeSheet intent={sheet.intent} onClose={closeSheet} onCreated={created} />}
```

`SpawnSheet`: `width={520}`, shadcn buttons, `toast.success(startedToast(agent, p.itemKey))` after `start.run` resolves. Remove `C.spikeViaNewItem` if now unused. (The Header label rename happens in F1; until then the Header still says `C.newSpike` — keep that key until F1 removes it.)
- [ ] **Step 4: GREEN** — `pnpm test`.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): chore/spike presets and start toast"`.

**Package D verify:** `pnpm typecheck && pnpm test && pnpm build`; screenshots of New orchestrator (empty, 12 repos, No advisor), New item, Start orchestrator at 1280×800 and 390×844 for the UI reviewer.

---

## Package E · Details & panels (after B + C)

**Files:** `App.tsx` (Details block), `App.test.tsx`, `panels/Details.tsx(+test)`, `components/AgentRow.tsx(+test)`, `panels/Review.tsx(+test)`, `components/RequestChanges.tsx(+test)`, `components/ConfirmRepos.tsx(+test)`, `components/QuestionView.tsx(+test)`, `panels/NeedsYou.tsx(+test)`, `components/AddDependency.tsx`, `components/CheckpointList.tsx(+test)`, `components/WorkflowSection.tsx(+test)`, `components/ArtifactViewer.tsx`, `components/Markdown.tsx`.

### Unit E1: Details in a non-modal Sheet

- [ ] **Step 1: Tests** — `App.test.tsx`: rewrite "replaces the view with the details panel on narrow windows" (spec Deleted #5) to: on narrow, selecting an item opens `dialog` named after the item key/title; clicking `Close` removes it and the view is visible. Add (wide): with Details open, clicking another card changes the dialog's content and the dialog stays open. `Details.test.tsx`: port priority `selectOptions` → `pickOption(user, "Priority", "P1")`; tablist → Radix Tabs (`getByRole("tab", { name })`, `aria-selected`); add a status-move toast assertion: after moving to an allowed status, `findByText("Moved TASK-… to …")`.
- [ ] **Step 2: RED** — FAIL.
- [ ] **Step 3: Implement** — `App.tsx`: delete the `data-testid="details"` side `div` and the narrow Back branch; always render the view `section`; render:

```tsx
{showDetails && (
  <Sheet title={url.item} header={false} modal={false} width={narrow ? undefined : 560} onClose={() => setUrl({ item: "" })}>
    {outside && <OutsideViewBanner … />}
    <Details key={url.item} … />
  </Sheet>
)}
```

Keep `data-testid="details"` on the Sheet body wrapper (`<div data-testid="details">`) so existing tests that locate it keep working. `Details.tsx`: its own close button is removed (the Sheet's absolutely positioned close button sits top-right; leave `pr-10` on the header row so Move to never sits under it); header row = key (`.key`), type badge, `MoveToMenu` right-aligned; title editable at 15 px; priority → `Select` (`aria-label={C.priority}`); tablist → `Tabs`/`TabsList`/`TabsTrigger`; loading → three `h-3 animate-pulse rounded bg-muted` lines; error → `Alert variant="destructive"` + Retry link. In `save`, when `body.status` succeeded: `toast.success(T.toastMoved(item.key, STATUS_LABEL[body.status]))`. Terminal action success → `toast.success(agentActionToast("terminal", name))`.
- [ ] **Step 4: GREEN** — `pnpm vitest run src/App.test.tsx src/panels/Details.test.tsx` PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): item details open in a non-modal sheet"`.

### Unit E2: Agent rows

- [ ] **Step 1: Tests** — `AgentRow.test.tsx`: add success toasts for pause (orchestrator → "Pausing X and its agents"), resume, cancel, ack, retry; replace the `window.confirm` stub test with: clicking Cancel on an orchestrator with live children opens `alertdialog` with `T.cancelOrchestrator` text; "Cancel" in the dialog aborts (no API call), confirm button proceeds and toasts "Cancelled X". Finished `<details>` → `Collapsible` trigger `button` named `T.finished(n)` with `aria-expanded`.
- [ ] **Step 2: RED** — FAIL.
- [ ] **Step 3: Implement** — buttons → `Button variant="outline" size="sm"` (`destructive` variant for Cancel); confirm via controlled `AlertDialog` (state `confirming: AgentAction | null`), action button label `C.cancel`… use `AlertDialogCancel` = `C.keepRunning` if it exists, else `C.back`; grep `copy.ts` and if neither exists add `keepRunning: "Keep running"` to `copy.ts` + `copy.test.ts` in this unit. On success `toast.success(agentActionToast(a.endpoint, agent.name, a.body?.scope))`. Remove `window.confirm`.
- [ ] **Step 4: GREEN**, **Step 5: Commit** — `git commit -m "feat(web): agent actions with confirm dialog and toasts"`.

### Unit E3: Review, Needs you, questions

- [ ] **Step 1: Tests** — `Review.test.tsx`: approve → `findByText("Approved <item_key>")`; close spike → `"Closed <item_key>"`; conflict path asserts **no** success toast. `RequestChanges.test.tsx`: success → `"Sent change request to <agent>"`. `ConfirmRepos.test.tsx`: checkboxes are Radix `Checkbox` (`getByRole("checkbox")` still works; `toBeChecked` works via `aria-checked`) — port any `.checked` property reads; success → `"Confirmed N repositories"`. `QuestionView.test.tsx` / `NeedsYou.test.tsx`: terminal success toast.
- [ ] **Step 2: RED** — FAIL.
- [ ] **Step 3: Implement** — swap primitives (Button, Textarea, Checkbox + Label, Badge, Alert) and add the toasts from the spec's toast table for each file. `NeedsYou` filter chips → `Segmented`.
- [ ] **Step 4: GREEN**, **Step 5: Commit** — `git commit -m "feat(web): review and inbox actions on shadcn with toasts"`.

### Unit E4: Disclosures, dependency add, prose

- [ ] **Step 1: Tests** — `CheckpointList.test.tsx` / `WorkflowSection.test.tsx`: any `<details>`/`summary` interaction → click `button` trigger, assert `aria-expanded`. Add an `AddDependency` success test in `Details.test.tsx`: `findByText("TASK-x is now blocked by TASK-y")`.
- [ ] **Step 2: RED** — FAIL.
- [ ] **Step 3: Implement** — `Collapsible` in CheckpointList, WorkflowSection, ArtifactViewer; `AddDependency` → `Input` + `Button size="sm"` + success toast `T.toastDepAdded`; `Markdown.tsx` prose colours → `text-foreground`, links `text-link`, code `bg-muted font-mono`.
- [ ] **Step 4: GREEN**, **Step 5: Commit** — `git commit -m "feat(web): collapsibles, dependency toast, prose tokens"`.

**Package E verify:** `pnpm typecheck && pnpm test && pnpm build`; screenshots: Details open over Kanban (wide), Details narrow, Start orchestrator stacked over Details, a success and an error toast.

---

## Package F · Header, views, cleanup (after D + E)

**Files:** `components/Header.tsx`, `App.test.tsx`, `views/Kanban.tsx(+test)`, `views/Hierarchy.tsx(+test)`, `views/Dependencies.tsx(+test)`, `components/icons.tsx`, `index.css`, `copy.ts`/`copy.test.ts` (removals), `e2e/mock.spec.ts`, `e2e/live.spec.ts`, `package.json` (only if a dep became unused).

### Unit F1: Header

- [ ] **Step 1: Tests** — in `App.test.tsx` (header cases): port filter `selectOptions` → `pickOption(user, "Type", "Bugs")` etc. (sentinel `All`); view switch → `getByRole("tab", { name: "Kanban" })`; "New item" → `getByRole("button", { name: "New item" })` opens `menuitem`s Epic/Bug/Story/Task/Spike; "New orchestrator" button opens the sheet; Needs-you button shows the warning dot only when > 0 (`data-dot`).
- [ ] **Step 2: RED** — FAIL.
- [ ] **Step 3: Implement** — `Header.tsx`: row 1 `h-12` title `text-[14px] font-semibold`, `Button variant="secondary"` Needs you (+ `size-1.5 rounded-full bg-warning` dot, `data-dot`), `Button variant="outline"` `C.newOrchestrator`, `DropdownMenu` with `Button` trigger `C.newItem` + `ChevronDown`. Row 2 `Input type="search"` (`w-64 h-8`, `Search` icon), Type/Status `Select`s with `ALL = "__all"` ↔ `""`. Row 3 `Tabs value={url.view} onValueChange={(v) => setUrl({ view: v as View })}` with `TabsList`/`TabsTrigger` only; Card level / Group by `Select`s (Group by `disabled` when level is `top`). Remove `C.newSpike` from `copy.ts` + `copy.test.ts`.
- [ ] **Step 4: GREEN**, **Step 5: Commit** — `git commit -m "feat(web): header on shadcn tabs, selects and menus"`.

### Unit F2: Kanban

- [ ] **Step 1: Tests** — `Kanban.test.tsx`: port `selectOptions`; successful drop → `findByText("Moved TASK-… to …")`; failed patch → error toast text unchanged; locked spike drop still shows "View spike" action.
- [ ] **Step 2: RED** — FAIL (no success toast).
- [ ] **Step 3: Implement** — in `move()`, after `await patch.run(...)` resolves: `toast.success(T.toastMoved(card.key, STATUS_LABEL[status]))`. Cards: `rounded-md border border-border bg-card p-2`, key `.key text-muted-foreground`, status `Badge`; column headers `text-xs font-medium text-muted-foreground uppercase tracking-wide`; drop highlight `ring-1 ring-ring`; empty-state `Button variant="link"` for Clear filters / New item.
- [ ] **Step 4: GREEN**, **Step 5: Commit** — `git commit -m "feat(web): kanban cards restyled with move toast"`.

### Unit F3: Hierarchy, Dependencies, icons

- [ ] **Step 1: Tests** — port any `selectOptions` / `radiogroup` / raw-button-class assertions in `Hierarchy.test.tsx` and `Dependencies.test.tsx`; add-child menu → `DropdownMenu` (`menuitem`).
- [ ] **Step 2: RED**, **Step 3: Implement** — rows `h-[30px]`, hover `bg-accent/60`, selected `bg-accent`; add-child `DropdownMenu`; Dependencies scope `Segmented`, hops `Select`; React Flow node styles → token classes (`bg-card border-border text-foreground`, edges `stroke: var(--border)`, selected `var(--ring)`); `icons.tsx` type/agent colour classes → tokens.
- [ ] **Step 4: GREEN**, **Step 5: Commit** — `git commit -m "feat(web): hierarchy and dependency views on tokens"`.

### Unit F4: Remove legacy aliases

- [ ] **Step 1: Failing check** — add `web/src/tokens.test.ts`:

```ts
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const walk = (d: string): string[] => readdirSync(d).flatMap((f) => {
  const p = join(d, f);
  return statSync(p).isDirectory() ? walk(p) : p.endsWith(".tsx") ? [p] : [];
});

describe("design tokens", () => {
  it("no component uses the pre-shadcn colour names", () => {
    const legacy = /\b(?:bg|text|border|ring|divide)-(?:canvas|panel|raised|line|ink|ok|warn|bad)\b/;
    const hits = walk(fileURLToPath(new URL(".", import.meta.url))).filter((f) => legacy.test(readFileSync(f, "utf8")));
    expect(hits).toEqual([]);
  });
});
```

- [ ] **Step 2: RED** — lists remaining files.
- [ ] **Step 3: Implement** — rename remaining usages (`bg-canvas→bg-background`, `bg-panel→bg-card`, `bg-raised→bg-muted`, `border-line→border-border`, `text-ink→text-foreground`, `text-ok→text-success`, `text-warn→text-warning`, `text-bad→text-destructive`, same for `bg-*/10` variants), then delete the "Legacy aliases" block from `index.css`.
- [ ] **Step 4: GREEN** — `pnpm test` + `pnpm build` (Tailwind would silently drop unknown classes, so also run the test above).
- [ ] **Step 5: Commit** — `git commit -m "chore(web): drop legacy colour aliases"`.

### Unit F5: e2e, screenshots, final verification

- [ ] **Step 1: Port e2e** — `e2e/mock.spec.ts` / `live.spec.ts`: `page.selectOption(...)` → `page.getByRole("combobox", { name }).click(); page.getByRole("option", { name }).click()`; "New spike" → "New orchestrator"; Details locator → `page.getByRole("dialog")`; add one mock scenario asserting a success toast after creating an item.
- [ ] **Step 2: RED/GREEN** — `pnpm e2e` fails before the port, passes after.
- [ ] **Step 3: Screenshots** — `pnpm dev:mock`; Playwright captures at 1280×800 and 390×844: board (Kanban), Details sheet, New orchestrator (empty / 12 repos / No advisor), New item, success toast, error toast. Save under `web/e2e/__screenshots__/redress/` (gitignored if the dir is; otherwise attach to the review, do not commit).
- [ ] **Step 4: Full verification** (in order): `pnpm install --frozen-lockfile && pnpm typecheck && pnpm test && pnpm e2e && pnpm build && (cd .. && go test ./web/...)`. All pass; coverage ≥ 80 % lines.
- [ ] **Step 5: Commit** — `git commit -m "test(web): port e2e to shadcn controls"`.

**Package F verify:** the Step 4 chain + Opus UI review of the screenshots against the spec's Screens section and the 16 end-to-end scenarios (walk each in `pnpm dev:mock`).

---

## Review focus (all packages)

- A test disappeared from a diff without appearing in the spec's Deleted list → reject.
- Radix `SelectItem value=""` anywhere → reject (runtime error).
- Toast fired on the conflict/stale path → reject.
- Mono text bolded, hard-coded hex in a component, or a light-mode class → reject.
- Details closing on board click, or form sheets not closing top-first on Escape → reject.

## Swarm tree (for `swarm_items` registration)

Skeleton: at registration, expand each unit with `steps` [RED test named in its unit above, GREEN, commit] and each package with `brief` (its "Done when" from the package map) and `acceptance` (its unit titles as outcomes), so registration raises no "TDD script without a test step" warnings.

```json
[
 {"ref":"p-a","type":"task","title":"Web re-dress A: shadcn foundation, tokens, fonts, Sonner","role_hint":"coder","repos":["agent-swarm"],
  "units":[{"title":"A1 shadcn, alias, tokens, fonts"},{"title":"A2 Radix test infra"},{"title":"A3 Sonner behind useToast"}],
  "verify":["cd web && pnpm typecheck && pnpm test && pnpm build","go test ./web/..."],"workflow":{"template":"ui-tdd-reviewed"}},
 {"ref":"p-b","type":"task","title":"Web re-dress B: copy, toast builders, repo chooser rules, advisor split, chore intent","role_hint":"coder","repos":["agent-swarm"],
  "units":[{"title":"B1 copy"},{"title":"B2 toast builders"},{"title":"B3 repo chooser rules"},{"title":"B4 advisor split + chore"}],
  "verify":["cd web && pnpm typecheck && pnpm test"],"workflow":{"template":"tdd-reviewed"}},
 {"ref":"p-c","type":"task","title":"Web re-dress C: shadcn primitives","blocked_by":["p-a"],"role_hint":"coder","repos":["agent-swarm"],
  "units":[{"title":"C1 Sheet"},{"title":"C2 Segmented"},{"title":"C3 MoveToMenu"},{"title":"C4 Badge + Alert"}],
  "verify":["cd web && pnpm typecheck && pnpm test && pnpm build"],"workflow":{"template":"ui-tdd-reviewed"}},
 {"ref":"p-d","type":"task","title":"Web re-dress D: forms","blocked_by":["p-b","p-c"],"role_hint":"coder","repos":["agent-swarm"],
  "units":[{"title":"D1 New item"},{"title":"D2 AgentFields"},{"title":"D3 RepoPicker"},{"title":"D4 New orchestrator"},{"title":"D5 Start orchestrator + App sheet state"}],
  "verify":["cd web && pnpm typecheck && pnpm test && pnpm build"],"workflow":{"template":"ui-tdd-reviewed"}},
 {"ref":"p-e","type":"task","title":"Web re-dress E: Details sheet and panels","blocked_by":["p-b","p-c"],"role_hint":"coder","repos":["agent-swarm"],
  "units":[{"title":"E1 Details sheet"},{"title":"E2 Agent rows"},{"title":"E3 Review/inbox"},{"title":"E4 Disclosures + deps + prose"}],
  "verify":["cd web && pnpm typecheck && pnpm test && pnpm build"],"workflow":{"template":"ui-tdd-reviewed"}},
 {"ref":"p-f","type":"task","title":"Web re-dress F: header, views, cleanup, e2e","blocked_by":["p-d","p-e"],"role_hint":"coder","repos":["agent-swarm"],
  "units":[{"title":"F1 Header"},{"title":"F2 Kanban"},{"title":"F3 Hierarchy/Dependencies"},{"title":"F4 Drop aliases"},{"title":"F5 e2e + screenshots"}],
  "verify":["cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test && pnpm e2e && pnpm build","go test ./web/..."],"workflow":{"template":"ui-tdd-reviewed"}}
]
```

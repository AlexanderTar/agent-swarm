import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useConnection } from "../data/hooks";
import { useAgents, useItems, useRequests } from "../data/queries";
import { createMockDaemon } from "../mock/daemon";
import { seed } from "../mock/fixtures";
import { renderWithDaemon } from "../test/render";
import type { CardLevel, Filter, Grouping } from "../types";
import { Kanban } from "./Kanban";

const none: Filter = { q: "", type: "", status: "" };

function Host(p: {
  filter?: Filter;
  level?: CardLevel;
  group?: Grouping;
  onReview?: (id: string) => void;
  onSelect?: (k: string) => void;
  onLevel?: (l: CardLevel) => void;
  loaded?: boolean;
}) {
  const items = useItems();
  const agents = useAgents();
  const requests = useRequests();
  const conn = useConnection();
  if (!items.data || !agents.data || !requests.data) return null;
  return (
    <Kanban
      items={items.data.items}
      loaded={p.loaded ?? true}
      agents={agents.data}
      requests={requests.data}
      filter={p.filter ?? none}
      selected=""
      connected={conn.connected}
      level={p.level ?? "tasks"}
      group={p.group ?? "root"}
      onSelect={p.onSelect ?? vi.fn()}
      onLevel={p.onLevel ?? vi.fn()}
      onReview={p.onReview ?? vi.fn()}
      onClearFilters={vi.fn()}
      onNewItem={vi.fn()}
    />
  );
}

const card = (key: string) => screen.getByTestId(`card-${key}`);
const cell = (lane: string, status: string) => screen.getByTestId(`cell-${lane}-${status}`);
// DndContext renders its own hidden `role="status"` live region (aria-live="assertive") for
// screen-reader drag announcements, alongside the ToastProvider's own `role="status"` container
// (aria-live="polite") — so a bare `getByRole("status")` matches both once a view wraps in
// DndContext. Disambiguate by the aria-live value rather than weakening the query to `getAllByRole`.
const toastRegion = () => screen.getAllByRole("status").find((el) => el.getAttribute("aria-live") === "polite") as HTMLElement;

describe("Kanban view (§16.7)", () => {
  it("builds lanes, headers and collapsed columns for task cards", async () => {
    renderWithDaemon(<Host />, { events: false });
    const lane = await screen.findByTestId("lane-EPIC-12");
    expect(lane).toHaveTextContent("4 tasks · 1 need you · 1 blocked");
    expect(screen.getByTestId("lane-EPIC-30")).toHaveTextContent("All 2 tasks finished · 1 done, 1 cancelled.");
    expect(screen.getByTestId("lane-orphans")).toHaveTextContent("Unassigned · Parent missing");
    expect(screen.getByTestId("col-ready")).toHaveTextContent("Ready · 3");
    expect(screen.getByTestId("col-draft")).toHaveAttribute("data-collapsed", "true");
    expect(screen.getByTestId("col-done")).toHaveAttribute("data-collapsed", "true");
    expect(screen.queryByTestId("col-awaiting_approval")).not.toBeInTheDocument();
    expect(within(cell("EPIC-12", "in_progress")).getByTestId("card-TASK-101")).toBeInTheDocument();
    expect(within(cell("BUG-7", "blocked")).getByText("No items")).toBeInTheDocument();
  });

  it("shows every conditional card line", async () => {
    renderWithDaemon(<Host />, { events: false });
    await screen.findByTestId("card-TASK-101");
    expect(card("TASK-101")).toHaveTextContent("STORY-40 · Login");
    expect(card("TASK-101")).toHaveTextContent("login-form-coder");
    expect(card("TASK-101")).toHaveTextContent("Running");
    expect(card("TASK-104")).toHaveTextContent("Needs 1");
    expect(card("TASK-104")).toHaveTextContent("Blocked by TASK-102");
    expect(card("TASK-104")).toHaveTextContent("Waiting");
    expect(card("TASK-103")).not.toHaveTextContent("Blocked by");
  });

  it("shows ROOT / PARENT in flat mode and story progress at story level", async () => {
    const a = renderWithDaemon(<Host group="flat" />, { events: false });
    expect(await screen.findByTestId("card-TASK-101")).toHaveTextContent("EPIC-12 / STORY-40");
    a.unmount();
    renderWithDaemon(<Host level="stories" />, { events: false });
    expect(await screen.findByText("Stories within epics")).toBeInTheDocument();
    expect(card("STORY-40")).toHaveTextContent("0 of 3 tasks done");
    expect(screen.queryByTestId("col-awaiting_approval")).not.toBeInTheDocument();
  });

  it("shows top-level cards flat with Awaiting approval", async () => {
    renderWithDaemon(<Host level="top" />, { events: false });
    expect(await screen.findByTestId("col-awaiting_approval")).toBeInTheDocument();
    expect(screen.queryByTestId("lane-EPIC-12")).not.toBeInTheDocument();
    expect(card("EPIC-12")).toHaveTextContent("0 of 2 stories done");
    expect(within(cell("flat", "awaiting_approval")).getByTestId("card-SPIKE-3")).toBeInTheDocument();
  });

  it("expands a collapsed column targeted by the status filter and remembers collapse", async () => {
    const a = renderWithDaemon(<Host filter={{ ...none, status: "done" }} />, { events: false });
    expect(await screen.findByTestId("col-done")).toHaveAttribute("data-collapsed", "false");
    expect(screen.getByTestId("col-done")).toHaveAttribute("title", "2 of 2 items");
    a.unmount();
    const b = renderWithDaemon(<Host />, { events: false });
    await b.user.click(await screen.findByRole("button", { name: /^Ready · 3/ }));
    await b.user.click(within(screen.getByTestId("lane-EPIC-12")).getByRole("button", { name: /EPIC-12/ }));
    b.unmount();
    renderWithDaemon(<Host />, { events: false });
    expect(await screen.findByTestId("col-ready")).toHaveAttribute("data-collapsed", "true");
    expect(screen.getByTestId("lane-EPIC-12")).toHaveAttribute("data-collapsed", "true");
    expect(screen.getByTestId("lane-EPIC-12")).toHaveTextContent("4 tasks · 1 need you · 1 blocked");
  });

  it("moves a card, shows Updating… and settles", async () => {
    // F12 (preflight ruling): the mock resolves a PATCH and its refetch entirely in microtasks, so
    // "Updating…" can be gone again before the test ever observes it. Hold the response open so the
    // pending state is deterministic, matching the pattern `d.hold()` exists for.
    const d = createMockDaemon();
    const release = d.hold("PATCH /api/items/TASK-103");
    const { user, daemon } = renderWithDaemon(<Host />, { daemon: d });
    await user.click(within(await screen.findByTestId("card-TASK-103")).getByRole("button", { name: "Move to… TASK-103" }));
    await user.click(screen.getByRole("menuitem", { name: /Blocked/ }));
    expect(within(cell("EPIC-12", "blocked")).getByTestId("card-TASK-103")).toHaveTextContent("Updating…");
    release();
    await waitFor(() => expect(card("TASK-103")).not.toHaveTextContent("Updating…"));
    expect(within(cell("EPIC-12", "blocked")).getByTestId("card-TASK-103")).toBeInTheDocument();
    expect(daemon.calls.find((c) => c.method === "PATCH")).toMatchObject({ path: "/api/items/TASK-103", body: { status: "blocked" } });
  });

  it("clears the pending marker even when the item settles at a different status than requested (Important 1)", async () => {
    // A concurrent actor (an orchestrator, or the daemon itself) can land the item somewhere other
    // than the status this card asked for, inside the refetch window. The pending marker must clear
    // on that resolution too — keying it on "did the PATCH land" (the item's revision changed), not
    // "did it land where I asked" (an exact status match that may never come).
    const d = createMockDaemon();
    d.override("PATCH /api/items/TASK-103", () => {
      const it = d.db.items.find((i) => i.key === "TASK-103");
      if (it) {
        it.status = "in_review";
        it.revision += 1;
      }
      return { status: 200, body: it };
    });
    const { user } = renderWithDaemon(<Host />, { daemon: d, events: false });
    await user.click(within(await screen.findByTestId("card-TASK-103")).getByRole("button", { name: "Move to… TASK-103" }));
    await user.click(screen.getByRole("menuitem", { name: /Blocked/ }));
    await waitFor(() => expect(card("TASK-103")).not.toHaveTextContent("Updating…"));
    expect(within(cell("EPIC-12", "in_review")).getByTestId("card-TASK-103")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move to… TASK-103" })).toBeEnabled();
  });

  it("clears the pending marker on a refusal so the card isn't left stuck disabled (Important 1)", async () => {
    const d = createMockDaemon();
    const reason = "Couldn't update status. The item remains Ready.";
    d.override("PATCH /api/items/TASK-103", { status: 422, body: { error: { code: "transition_denied", message: reason, reason } } });
    const { user } = renderWithDaemon(<Host />, { daemon: d, events: false });
    await user.click(within(await screen.findByTestId("card-TASK-103")).getByRole("button", { name: "Move to… TASK-103" }));
    await user.click(screen.getByRole("menuitem", { name: /Blocked/ }));
    await waitFor(() => expect(toastRegion()).toHaveTextContent(reason));
    expect(card("TASK-103")).not.toHaveTextContent("Updating…");
    expect(screen.getByRole("button", { name: "Move to… TASK-103" })).toBeEnabled();
  });

  it("returns a refused card with the exact toast", async () => {
    const d = createMockDaemon();
    const reason = "Couldn't update status. The item remains Ready.";
    d.override("PATCH /api/items/TASK-103", { status: 422, body: { error: { code: "transition_denied", message: reason, reason } } });
    const { user } = renderWithDaemon(<Host />, { daemon: d, events: false });
    await user.click(within(await screen.findByTestId("card-TASK-103")).getByRole("button", { name: "Move to… TASK-103" }));
    await user.click(screen.getByRole("menuitem", { name: /Blocked/ }));
    await waitFor(() => expect(toastRegion()).toHaveTextContent(reason));
    expect(within(cell("EPIC-12", "ready")).getByTestId("card-TASK-103")).toBeInTheDocument();
  });

  it("restores scroll once real content mounts, not on the pre-load null render (F16)", async () => {
    // F16 (T21-23 review): Kanban's own `loaded` prop starts false while the shell is still waiting
    // on the first GET /api/items, and the component returns null for that render — so `scroller` is
    // never attached to a DOM node on the one run an empty-deps effect gets. Reproduce that exact
    // sequence: mount with loaded=false (Kanban renders null), then flip loaded=true (the real
    // scroller div mounts) on the SAME component instance, the way the real shell does once its
    // query resolves.
    // Bypass the Host/query wrapper: the real bug is about ONE Kanban instance transitioning from
    // `loaded=false` to `loaded=true` on a rerender, which async query resolution can't reliably
    // force deterministically in a test. Render Kanban directly with fixture data so the
    // false→true transition happens exactly where we control it.
    localStorage.setItem("swarm.kanban.scroll", JSON.stringify({ left: 120, top: 40 }));
    const scrollTo = vi.fn();
    Element.prototype.scrollTo = scrollTo;
    const db = seed();
    const kanban = (loaded: boolean) => (
      <Kanban
        items={db.items}
        loaded={loaded}
        agents={db.agents}
        requests={db.requests}
        filter={none}
        selected=""
        connected
        level="tasks"
        group="root"
        onSelect={vi.fn()}
        onLevel={vi.fn()}
        onReview={vi.fn()}
        onClearFilters={vi.fn()}
        onNewItem={vi.fn()}
      />
    );
    const { rerender } = renderWithDaemon(kanban(false), { events: false });
    expect(screen.queryByTestId("lane-EPIC-12")).not.toBeInTheDocument();
    expect(scrollTo).not.toHaveBeenCalled();
    rerender(kanban(true));
    expect(await screen.findByTestId("lane-EPIC-12")).toBeInTheDocument();
    expect(scrollTo).toHaveBeenCalledWith(120, 40);
  });

  it("routes an epic on Done to acceptance and explains a spike on Done", async () => {
    const onReview = vi.fn();
    const onSelect = vi.fn();
    const { user } = renderWithDaemon(<Host level="top" onReview={onReview} onSelect={onSelect} />, { events: false });
    await user.click(within(await screen.findByTestId("card-EPIC-12")).getByRole("button", { name: "Move to… EPIC-12" }));
    await user.click(screen.getByRole("menuitem", { name: /^Done/ }));
    expect(onReview).toHaveBeenCalledWith("req_accept");
    expect(within(cell("flat", "in_progress")).getByTestId("card-EPIC-12")).toBeInTheDocument();
    await user.click(within(card("SPIKE-3")).getByRole("button", { name: "Move to… SPIKE-3" }));
    await user.click(screen.getByRole("menuitem", { name: /^Done/ }));
    expect(toastRegion()).toHaveTextContent("This spike reaches Done after materialization.");
    await user.click(screen.getByRole("button", { name: "View spike" }));
    expect(onSelect).toHaveBeenCalledWith("SPIKE-3");
  });

  it("shows the type-filter hint and switches card level", async () => {
    const onLevel = vi.fn();
    const { user } = renderWithDaemon(<Host filter={{ ...none, type: "spike" }} onLevel={onLevel} />, { events: false });
    expect(await screen.findByText("Spikes have no task cards yet. Switch Card level to Top-level items to see them.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show top-level items" }));
    expect(onLevel).toHaveBeenCalledWith("top");
  });

  it("drags TASK-103 with the keyboard: Space picks up, arrows move, Enter drops (F8)", async () => {
    // dnd-kit's KeyboardSensor moves a virtual "collision rect" by a fixed 25px per arrow press
    // (verified against @dnd-kit/core's defaultKeyboardCoordinateGetter) starting from the active
    // node's real getBoundingClientRect() captured AT PICKUP, and finds the droppable under it via
    // each container's own getBoundingClientRect() — which jsdom always reports as an all-zero
    // rect, making every column indistinguishable and the initial capture always (0,0). Stub rects
    // before Space is pressed (not after) so both captures see real, distinguishable positions: one
    // column per 25px step, so 2 ArrowRight presses move exactly from "ready" to "blocked".
    const colX = { ready: 25, in_progress: 50, blocked: 75, in_review: 100 } as const;
    const stubRect = (x: number) => ({ x, y: 0, width: 24, height: 20, top: 0, left: x, right: x + 24, bottom: 20, toJSON: () => ({}) }) as DOMRect;
    const spy = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const testid = this.getAttribute("data-testid") ?? "";
      const col = /^cell-EPIC-12-(ready|in_progress|blocked|in_review)$/.exec(testid)?.[1] as keyof typeof colX | undefined;
      if (col) return stubRect(colX[col]);
      if (testid === "card-TASK-103") return stubRect(colX.ready);
      return stubRect(9999);
    });
    try {
      const { daemon } = renderWithDaemon(<Host />, { events: false });
      const el = await screen.findByTestId("card-TASK-103");
      el.focus();
      fireEvent.keyDown(el, { code: "Space" });
      // KeyboardSensor attaches its own document-level keydown listener via a `setTimeout(…, 0)` in
      // its constructor (see @dnd-kit/core's KeyboardSensor#attach) rather than synchronously —
      // flush that macrotask before sending the next key, or Arrow/Enter dispatch into a sensor
      // that isn't listening yet and silently no-op.
      await new Promise((r) => setTimeout(r, 0));
      // `lockFor` renders a refused column's reason purely from the `dragging` state dnd-kit's own
      // onDragStart sets — no coordinates or collision detection involved — so a lock hint appearing
      // here is direct proof Space started a keyboard drag via the composed listeners (F8).
      expect(within(cell("EPIC-12", "in_progress")).getByText("Couldn't update status. The item remains Ready.")).toBeInTheDocument();
      fireEvent.keyDown(el, { code: "ArrowRight" });
      fireEvent.keyDown(el, { code: "ArrowRight" });
      fireEvent.keyDown(el, { code: "Enter" });
      await waitFor(() => expect(daemon.calls.find((c) => c.method === "PATCH")).toMatchObject({ path: "/api/items/TASK-103", body: { status: "blocked" } }));
      expect(within(cell("EPIC-12", "blocked")).getByTestId("card-TASK-103")).toBeInTheDocument();
    } finally {
      spy.mockRestore();
    }
  });

  it("disables moves while disconnected", async () => {
    const { daemon } = renderWithDaemon(<Host />);
    const btn = await screen.findByRole("button", { name: "Move to… TASK-103" });
    expect(btn).toBeEnabled();
    daemon.disconnect();
    await waitFor(() => expect(screen.getByRole("button", { name: "Move to… TASK-103" })).toBeDisabled());
    expect(card("TASK-103")).toHaveAttribute("aria-disabled", "true");
  });
});

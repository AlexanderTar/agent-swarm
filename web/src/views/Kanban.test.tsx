import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useConnection } from "../data/hooks";
import { useAgents, useItems, useRequests } from "../data/queries";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import type { CardLevel, Filter, Grouping } from "../types";
import { Kanban } from "./Kanban";

const none: Filter = { q: "", type: "", status: "" };

function Host(p: { filter?: Filter; level?: CardLevel; group?: Grouping; onReview?: (id: string) => void; onSelect?: (k: string) => void; onLevel?: (l: CardLevel) => void }) {
  const items = useItems();
  const agents = useAgents();
  const requests = useRequests();
  const conn = useConnection();
  if (!items.data || !agents.data || !requests.data) return null;
  return (
    <Kanban
      items={items.data.items}
      loaded
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

  it("disables moves while disconnected", async () => {
    const { daemon } = renderWithDaemon(<Host />);
    const btn = await screen.findByRole("button", { name: "Move to… TASK-103" });
    expect(btn).toBeEnabled();
    daemon.disconnect();
    await waitFor(() => expect(screen.getByRole("button", { name: "Move to… TASK-103" })).toBeDisabled());
    expect(card("TASK-103")).toHaveAttribute("aria-disabled", "true");
  });
});

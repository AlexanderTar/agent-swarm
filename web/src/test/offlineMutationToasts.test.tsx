import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";
import type { ReactElement } from "react";
import { toast as sonner } from "sonner";
import { describe, expect, it, vi } from "vitest";
import { AddDependency } from "../components/AddDependency";
import { ConfirmRepos } from "../components/ConfirmRepos";
import { RequestChanges } from "../components/RequestChanges";
import { useConnection } from "../data/hooks";
import { useAgents, useItems, useRequests } from "../data/queries";
import { createMockDaemon, type MockDaemon } from "../mock/daemon";
import { Details } from "../panels/Details";
import { NewItemSheet } from "../panels/NewItemSheet";
import { NewSpikeSheet } from "../panels/NewSpikeSheet";
import { Review } from "../panels/Review";
import { SpawnSheet } from "../panels/SpawnSheet";
import { Kanban } from "../views/Kanban";
import { renderWithDaemon } from "./render";

function Board() {
  const items = useItems();
  const agents = useAgents();
  const requests = useRequests();
  const { connected } = useConnection();
  if (!items.data || !agents.data || !requests.data) return null;
  return <Kanban items={items.data.items} loaded agents={agents.data} requests={requests.data}
    filter={{ q: "", type: "", status: "" }} selected="" connected={connected} level="tasks" group="root"
    onSelect={vi.fn()} onLevel={vi.fn()} onReview={vi.fn()} onClearFilters={vi.fn()} onNewItem={vi.fn()} />;
}

const cases: { name: string; route: string; setup?: (d: MockDaemon) => void; ui(d: MockDaemon): ReactElement; click(user: UserEvent): Promise<void> }[] = [
  { name: "new item", route: "POST /api/items", ui: () => <NewItemSheet type="epic" onClose={vi.fn()} onCreated={vi.fn()} />,
    click: async (u) => { await u.type(await screen.findByRole("textbox", { name: "Title" }), "Offline test"); await u.click(screen.getByRole("button", { name: "Create item" })); } },
  { name: "new spike", route: "POST /api/spikes", setup: (d) => { d.db.settings.max_concurrent_agents = 8; },
    ui: () => <NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />,
    click: async (u) => { await u.type(await screen.findByRole("textbox", { name: "Name" }), "Offline test"); await u.click(screen.getByRole("button", { name: "Start orchestrator" })); } },
  { name: "spawn", route: "POST /api/items/EPIC-20/orchestrator", setup: (d) => { d.db.settings.max_concurrent_agents = 8; },
    ui: () => <SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />,
    click: async (u) => { await u.click(await screen.findByRole("button", { name: "Start orchestrator" })); } },
  { name: "add dependency", route: "POST /api/items/TASK-103/deps", ui: () => <AddDependency itemKey="TASK-103" disabled={false} />,
    click: async (u) => { await u.click(screen.getByRole("button", { name: /Add dependency/ })); await u.type(screen.getByRole("searchbox", { name: "Add dependency" }), "TASK-104"); await u.click(await screen.findByRole("button", { name: /TASK-104/ })); } },
  { name: "confirm repositories", route: "POST /api/requests/req_repos/confirm-repos",
    ui: (d) => <ConfirmRepos request={d.db.requests.find((r) => r.id === "req_repos")!} connected />,
    click: async (u) => { await u.click(await screen.findByRole("button", { name: "Confirm repositories" })); } },
  { name: "request changes", route: "POST /api/requests/req_section/request-changes",
    ui: () => <RequestChanges requestId="req_section" agentName="offline-spike-orchestrator" connected />,
    click: async (u) => { await u.click(screen.getByRole("button", { name: "Request changes" })); await u.type(screen.getByRole("textbox", { name: "Comment" }), "Fix queue"); await u.click(screen.getByRole("button", { name: "Send change request" })); } },
  { name: "review approve", route: "POST /api/requests/req_plan/approve",
    ui: (d) => <Review request={d.db.requests.find((r) => r.id === "req_plan")!} connected />,
    click: async (u) => { await u.click(await screen.findByRole("button", { name: "Approve plan" })); } },
  { name: "review approve refusal", route: "POST /api/requests/req_plan/approve",
    setup: (d) => d.override("POST /api/requests/req_plan/approve", { status: 422, body: { error: { code: "bad_request", message: "Approval refused." } } }),
    ui: (d) => <Review request={d.db.requests.find((r) => r.id === "req_plan")!} connected />,
    click: async (u) => { await u.click(await screen.findByRole("button", { name: "Approve plan" })); } },
  { name: "details move", route: "PATCH /api/items/TASK-103",
    ui: () => <Details itemKey="TASK-103" connected onClose={vi.fn()} onSelect={vi.fn()} onReview={vi.fn()} onStartOrchestrator={vi.fn()} />,
    click: async (u) => { await u.click(await screen.findByRole("button", { name: "Ready" })); await u.click(screen.getByRole("menuitem", { name: /^Blocked/ })); } },
  { name: "details move refusal", route: "PATCH /api/items/TASK-103",
    setup: (d) => d.override("PATCH /api/items/TASK-103", { status: 422, body: { error: { code: "transition_denied", message: "Move refused." } } }),
    ui: () => <Details itemKey="TASK-103" connected onClose={vi.fn()} onSelect={vi.fn()} onReview={vi.fn()} onStartOrchestrator={vi.fn()} />,
    click: async (u) => { await u.click(await screen.findByRole("button", { name: "Ready" })); await u.click(screen.getByRole("menuitem", { name: /^Blocked/ })); } },
  { name: "board move", route: "PATCH /api/items/TASK-103", ui: () => <Board />,
    click: async (u) => { await u.click(within(await screen.findByTestId("card-TASK-103")).getByRole("button", { name: "Move to… TASK-103" })); await u.click(screen.getByRole("menuitem", { name: /Blocked/ })); } },
  { name: "board move refusal", route: "PATCH /api/items/TASK-103",
    setup: (d) => d.override("PATCH /api/items/TASK-103", { status: 422, body: { error: { code: "transition_denied", message: "Move refused." } } }),
    ui: () => <Board />,
    click: async (u) => { await u.click(within(await screen.findByTestId("card-TASK-103")).getByRole("button", { name: "Move to… TASK-103" })); await u.click(screen.getByRole("menuitem", { name: /Blocked/ })); } },
];

describe("offline mutation toast audit", () => {
  it.each([
    { name: "details", ui: <Details itemKey="TASK-103" connected onClose={vi.fn()} onSelect={vi.fn()} onReview={vi.fn()} onStartOrchestrator={vi.fn()} />, trigger: "Ready" },
    { name: "board", ui: <Board />, trigger: "Move to… TASK-103" },
  ])("does not dispatch $name move from a menu opened before transport closes", async ({ ui, trigger }) => {
    const d = createMockDaemon();
    let live: ReturnType<typeof useConnection>["live"] | undefined;
    function Host() {
      live = useConnection().live;
      return ui;
    }
    const { user } = renderWithDaemon(<Host />, { daemon: d });
    await user.click(await screen.findByRole("button", { name: trigger }));
    const item = screen.getByRole("menuitem", { name: /^Blocked/ });
    live!.connected = false;
    live!.epoch++;
    fireEvent.click(item);
    live!.connected = true;
    await act(async () => {});
    expect(d.calls.filter((c) => c.method === "PATCH" && c.path === "/api/items/TASK-103")).toHaveLength(0);
  });

  it("does not dispatch an open dependency result after transport closes before rerender", async () => {
    const d = createMockDaemon();
    let live: ReturnType<typeof useConnection>["live"] | undefined;
    function Host() {
      live = useConnection().live;
      return <AddDependency itemKey="TASK-103" disabled={false} />;
    }
    const { user } = renderWithDaemon(<Host />, { daemon: d });
    await user.click(screen.getByRole("button", { name: /Add dependency/ }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "TASK-104");
    const result = await screen.findByRole("button", { name: /TASK-104/ });
    live!.connected = false;
    live!.epoch++;
    fireEvent.click(result);
    await act(async () => {});
    expect(d.calls.filter((c) => c.method === "POST" && c.path === "/api/items/TASK-103/deps")).toHaveLength(0);
  });

  it("keeps an open dependency result disabled while REST returns but the stream remains closed", async () => {
    const d = createMockDaemon();
    const { user } = renderWithDaemon(<AddDependency itemKey="TASK-103" disabled={false} />, { daemon: d });
    await user.click(screen.getByRole("button", { name: /Add dependency/ }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "TASK-104");
    const result = await screen.findByRole("button", { name: /TASK-104/ });
    d.disconnect();
    await waitFor(() => expect(result).toBeDisabled());
    d.reconnect();
    expect(result).toBeDisabled();
    fireEvent.click(result);
    expect(d.calls.filter((c) => c.method === "POST" && c.path === "/api/items/TASK-103/deps")).toHaveLength(0);
  });

  it.each(cases)("suppresses $name completion after disconnect", async ({ route, setup, ui, click }) => {
    const d = createMockDaemon();
    setup?.(d);
    const release = d.hold(route);
    const success = vi.spyOn(sonner, "success");
    const error = vi.spyOn(sonner, "error");
    const { user } = renderWithDaemon(ui(d), { daemon: d });
    await click(user);
    act(() => d.disconnect());
    await act(async () => { release(); });
    await waitFor(() => expect(d.calls.some((c) => `${c.method} ${c.path}` === route)).toBe(true));
    expect(success).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();
    success.mockRestore(); error.mockRestore();
  });
});

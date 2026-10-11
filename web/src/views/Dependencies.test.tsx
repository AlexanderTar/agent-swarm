import { act, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { useItems } from "../data/queries";
import { createMockDaemon } from "../mock/daemon";
import { clickNode, renderWithDaemon } from "../test/render";
import { pickOption } from "../test/select";
import type { Filter } from "../types";
import { Dependencies } from "./Dependencies";

const none: Filter = { q: "", type: "", status: "" };

function Host(p: { selected: string; filter?: Filter; onSelect?: (k: string) => void }) {
  const items = useItems();
  if (!items.data) return null;
  return <Dependencies items={items.data.items} loaded filter={p.filter ?? none} selected={p.selected} connected onSelect={p.onSelect ?? vi.fn()} />;
}
const node = (k: string) => screen.getByTestId(`node-${k}`);
const nodeBox = (k: string) => (node(k).closest(".react-flow__node") as HTMLElement).style.transform;

describe("Dependencies view (§16.8)", () => {
  it("shows the root scope with story boxes, external nodes and the legend", async () => {
    renderWithDaemon(<Host selected="TASK-102" />, { events: false });
    expect(await screen.findByTestId("node-TASK-104")).toHaveTextContent("Validate inputs");
    expect(node("TASK-104")).toHaveTextContent("In review");
    expect(node("TASK-98")).toHaveAttribute("data-external", "true");
    expect(node("TASK-98")).toHaveTextContent("BUG-7");
    expect(screen.getByText("STORY-40 Login")).toBeInTheDocument();
    expect(screen.getByText("A → B: B waits for A")).toBeInTheDocument();
    expect(within(node("TASK-104")).getByLabelText("Needs you")).toBeInTheDocument();
  });

  it("selects nodes and opens the root of an external node", async () => {
    const onSelect = vi.fn();
    const { user } = renderWithDaemon(<Host selected="TASK-102" onSelect={onSelect} />, { events: false });
    // See test/render.tsx's clickNode() for why a node click uses fireEvent instead of user.click.
    clickNode(await screen.findByTestId("node-TASK-104"));
    expect(onSelect).toHaveBeenCalledWith("TASK-104");
    clickNode(node("TASK-98"));
    expect(onSelect).not.toHaveBeenCalledWith("TASK-98");
    await user.click(screen.getByRole("button", { name: "Open root" }));
    expect(onSelect).toHaveBeenCalledWith("BUG-7");
  });

  it("switches to the neighbourhood and expands one hop", async () => {
    const { user, daemon } = renderWithDaemon(<Host selected="TASK-104" />, { events: false });
    await screen.findByTestId("node-TASK-104");
    expect(screen.getByRole("combobox", { name: "Hops" })).toBeDisabled();
    await user.click(screen.getByRole("radio", { name: "Neighbourhood" }));
    await waitFor(() => expect(screen.queryByTestId("node-TASK-98")).not.toBeInTheDocument());
    expect(screen.queryByTestId("node-TASK-103")).not.toBeInTheDocument();
    await pickOption(user, "Hops", "2");
    expect(await screen.findByTestId("node-TASK-98")).toBeInTheDocument();
    expect(daemon.calls.some((c) => c.path === "/api/items/TASK-104/graph?scope=neighbourhood&hops=2")).toBe(true);
    await user.click(screen.getByRole("button", { name: "Reset" }));
    await waitFor(() => expect(screen.queryByTestId("node-TASK-98")).not.toBeInTheDocument());
  });

  it("continues expanding the neighbourhood beyond three hops", async () => {
    const { user, daemon } = renderWithDaemon(<Host selected="TASK-104" />, { events: false });
    await screen.findByTestId("node-TASK-104");
    await user.click(screen.getByRole("radio", { name: "Neighbourhood" }));
    for (let n = 2; n <= 4; n += 1) await user.click(screen.getByRole("button", { name: "Expand one hop" }));
    expect(daemon.calls.some((c) => c.path === "/api/items/TASK-104/graph?scope=neighbourhood&hops=4")).toBe(true);
  });

  it("dims non-matching nodes and keeps positions when statuses change", async () => {
    const { daemon } = renderWithDaemon(<Host selected="TASK-102" filter={{ ...none, status: "blocked" }} />);
    await screen.findByTestId("node-TASK-104");
    expect(node("TASK-104")).toHaveAttribute("data-dimmed", "true");
    expect(node("TASK-102")).toHaveAttribute("data-dimmed", "false");
    const before = nodeBox("TASK-104");
    const t = daemon.db.items.find((i) => i.key === "TASK-104");
    if (t) t.status = "done";
    act(() => daemon.emit("item.changed", { key: "TASK-104", root_key: "EPIC-12" }));
    await waitFor(() => expect(node("TASK-104")).toHaveTextContent("Done"));
    expect(nodeBox("TASK-104")).toBe(before);
  });

  it("finds a node", async () => {
    const onSelect = vi.fn();
    const { user } = renderWithDaemon(<Host selected="TASK-102" onSelect={onSelect} />, { events: false });
    await screen.findByTestId("node-TASK-104");
    await user.type(screen.getByPlaceholderText("Find…"), "validate{Enter}");
    expect(onSelect).toHaveBeenCalledWith("TASK-104");
  });

  it("shows the empty state and picks a start item", async () => {
    const onSelect = vi.fn();
    const a = renderWithDaemon(<Host selected="TASK-110" />, { events: false });
    expect(await screen.findByText("No dependencies for this item.")).toBeInTheDocument();
    a.unmount();
    renderWithDaemon(<Host selected="" onSelect={onSelect} />, { events: false });
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith("EPIC-12"));
  });

  it("below 1100 it opens on the graph of the first root instead of selecting it", async () => {
    vi.spyOn(window, "matchMedia").mockImplementation(((query: string) =>
      ({ matches: query.includes("max-width: 1099"), media: query, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia);
    const onSelect = vi.fn();
    renderWithDaemon(<Host selected="" onSelect={onSelect} />, { events: false });
    expect(await screen.findByText("No dependencies for this item.")).toBeInTheDocument();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("does not re-select a root after the selection is closed, and keeps the graph", async () => {
    const onSelect = vi.fn();
    function Closable() {
      const [sel, setSel] = useState("");
      const items = useItems();
      if (!items.data) return null;
      return (
        <>
          <button type="button" onClick={() => setSel("")}>Close details</button>
          <Dependencies items={items.data.items} loaded filter={none} selected={sel} connected onSelect={(k) => { onSelect(k); setSel(k); }} />
        </>
      );
    }
    const { user } = renderWithDaemon(<Closable />, { events: false });
    await screen.findByText("No dependencies for this item.");
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith("EPIC-12");
    await user.click(screen.getByRole("button", { name: "Close details" }));
    await new Promise((r) => setTimeout(r, 50));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(screen.getByText("No dependencies for this item.")).toBeInTheDocument();
  });

  it("lays the toolbar out as one scroll row below md", async () => {
    renderWithDaemon(<Host selected="TASK-102" />, { events: false });
    await screen.findByTestId("node-TASK-104");
    const bar = screen.getByRole("button", { name: "Fit" }).parentElement!;
    expect(bar).toHaveClass("max-md:flex-nowrap", "max-md:overflow-x-auto");
  });

  it("surfaces a failed graph load with the daemon's reason and a working retry (standing rule)", async () => {
    const d = createMockDaemon();
    let attempt = 0;
    d.override("GET /api/items/TASK-102/graph", () => {
      attempt += 1;
      if (attempt === 1) {
        return { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load the graph." } } };
      }
      return { status: 200, body: { nodes: [], edges: [] } };
    });
    const user = userEvent.setup();
    renderWithDaemon(<Host selected="TASK-102" />, { daemon: d, events: false });
    expect(await screen.findByText("Couldn't load the graph.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Couldn't load the graph.")).not.toBeInTheDocument());
  });
});

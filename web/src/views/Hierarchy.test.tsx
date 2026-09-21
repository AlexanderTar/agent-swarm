import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { seed } from "../mock/fixtures";
import type { Filter } from "../types";
import { Hierarchy } from "./Hierarchy";
import type { HierarchyProps } from "./props";

const none: Filter = { q: "", type: "", status: "" };
function setup(p: Partial<HierarchyProps> = {}) {
  const props: HierarchyProps = {
    items: seed().items, loaded: true, filter: none, selected: "", connected: true,
    onSelect: vi.fn(), onAddChild: vi.fn(), onNewItem: vi.fn(), onClearFilters: vi.fn(), ...p,
  };
  const user = userEvent.setup();
  const r = render(<Hierarchy {...props} />);
  return { ...r, props, user };
}
const row = (key: string) => screen.getByRole("treeitem", { name: new RegExp(`^${key} `) });

describe("Hierarchy view (§16.6)", () => {
  it("renders the columns, ordering, indentation and statuses", () => {
    setup();
    expect(screen.getByText("WORK ITEM")).toBeInTheDocument();
    const keys = screen.getAllByRole("treeitem").map((r) => r.getAttribute("data-key"));
    expect(keys.slice(0, 7)).toEqual(["EPIC-12", "STORY-40", "TASK-101", "TASK-102", "TASK-104", "STORY-41", "TASK-103"]);
    expect(row("EPIC-12")).toHaveAttribute("aria-level", "1");
    expect(row("TASK-101")).toHaveAttribute("aria-level", "3");
    expect(row("EPIC-12")).toHaveClass("font-semibold");
    expect(within(row("TASK-102")).getByText("Blocked")).toBeInTheDocument();
  });

  it("shows agent counts with a tooltip that open the Agents section", async () => {
    const { user, props } = setup();
    const count = within(row("EPIC-12")).getByRole("button", { name: "3" });
    expect(count).toHaveAttribute("title", "3 agents working in this item and its children.");
    await user.click(count);
    expect(props.onSelect).toHaveBeenCalledWith("EPIC-12", "agents");
    expect(within(row("TASK-104")).getByLabelText("Needs you")).toBeInTheDocument();
  });

  it("folds finished items by default and expands them on click", async () => {
    const { user } = setup();
    expect(row("EPIC-30")).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("treeitem", { name: /^STORY-50 / })).not.toBeInTheDocument();
    await user.click(within(row("EPIC-30")).getByRole("button", { name: "Expand EPIC-30" }));
    expect(row("STORY-50")).toBeInTheDocument();
  });

  it("collapses rows and remembers it", async () => {
    const { user, unmount } = setup();
    await user.click(within(row("STORY-40")).getByRole("button", { name: "Collapse STORY-40" }));
    expect(screen.queryByRole("treeitem", { name: /^TASK-101 / })).not.toBeInTheDocument();
    unmount();
    setup();
    expect(row("STORY-40")).toHaveAttribute("aria-expanded", "false");
  });

  it("moves with the keyboard", async () => {
    const onSelect = vi.fn();
    const { user, rerender, props } = setup({ selected: "TASK-101", onSelect });
    screen.getByRole("tree").focus();
    await user.keyboard("{ArrowDown}");
    expect(onSelect).toHaveBeenLastCalledWith("TASK-102");
    await user.keyboard("{ArrowUp}");
    expect(onSelect).toHaveBeenLastCalledWith("STORY-40");
    rerender(<Hierarchy {...props} selected="STORY-40" />);
    await user.keyboard("{ArrowLeft}");
    expect(row("STORY-40")).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{ArrowRight}");
    expect(row("STORY-40")).toHaveAttribute("aria-expanded", "true");
    await user.keyboard("{Enter}");
    expect(onSelect).toHaveBeenLastCalledWith("STORY-40");
    await user.click(row("BUG-7"));
    expect(onSelect).toHaveBeenLastCalledWith("BUG-7");
  });

  it("dims context rows while filtering", () => {
    setup({ filter: { ...none, q: "session" } });
    expect(row("EPIC-12")).toHaveAttribute("data-context", "true");
    expect(row("EPIC-12")).toHaveClass("opacity-50");
    expect(within(row("EPIC-12")).getByText("context")).toBeInTheDocument();
    expect(row("TASK-102")).toHaveAttribute("data-context", "false");
  });

  it("offers Add story on epics and Add task on stories, bugs and spikes", async () => {
    const { user, props } = setup();
    fireEvent.contextMenu(row("EPIC-12"));
    await user.click(screen.getByRole("menuitem", { name: "Add story" }));
    expect(props.onAddChild).toHaveBeenCalledWith("EPIC-12", "story");
    fireEvent.contextMenu(row("BUG-7"));
    expect(screen.queryByRole("menuitem", { name: "Add story" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Add task" }));
    expect(props.onAddChild).toHaveBeenCalledWith("BUG-7", "task");
    fireEvent.contextMenu(row("TASK-101"));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("shows the empty and filtered states", async () => {
    const a = setup({ items: [] });
    await a.user.click(screen.getByRole("button", { name: "New item" }));
    expect(screen.getByText("No work items yet.")).toBeInTheDocument();
    expect(a.props.onNewItem).toHaveBeenCalled();
    a.unmount();
    const b = setup({ filter: { ...none, q: "zzz" } });
    expect(screen.getByText("No items match these filters.")).toBeInTheDocument();
    await b.user.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(b.props.onClearFilters).toHaveBeenCalled();
    b.unmount();
    setup({ items: [], loaded: false });
    expect(screen.queryByText("No work items yet.")).not.toBeInTheDocument();
  });

  it("moves focus into the context menu on open and back to the tree on close (standing rule)", async () => {
    const { user } = setup();
    screen.getByRole("tree").focus();
    fireEvent.contextMenu(row("EPIC-12"));
    expect(screen.getByRole("menuitem", { name: "Add story" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("tree")).toHaveFocus();
    fireEvent.contextMenu(row("EPIC-12"));
    await user.click(screen.getByRole("menuitem", { name: "Add story" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("tree")).toHaveFocus();
  });
});

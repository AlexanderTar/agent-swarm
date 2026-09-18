import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { renderWithDaemon } from "./test/render";

describe("App shell (§16.5)", () => {
  it("shows the header and the Needs you count", async () => {
    const { user } = renderWithDaemon(<App />);
    expect(screen.getByRole("heading", { name: "Agent Swarm" })).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Needs you 9" }));
    expect(window.location.hash).toBe("#/inbox");
  });

  it("round-trips filters through the URL and shows matches only while filtering", async () => {
    const { user } = renderWithDaemon(<App />);
    await screen.findByRole("button", { name: /Needs you/ });
    expect(screen.queryByRole("button", { name: "Clear filters" })).not.toBeInTheDocument();
    await user.type(screen.getByRole("searchbox", { name: "Search name or key…" }), "login");
    await user.selectOptions(screen.getByRole("combobox", { name: "Type" }), "task");
    await user.selectOptions(screen.getByRole("combobox", { name: "Status" }), "in_progress");
    expect(window.location.hash).toBe("#/hierarchy?q=login&type=task&status=in_progress");
    expect(await screen.findByText("1 matches")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(window.location.hash).toBe("#/hierarchy");
    expect(screen.queryByText(/matches/)).not.toBeInTheDocument();
  });

  it("restores filters from the URL", async () => {
    renderWithDaemon(<App />, { hash: "#/kanban?q=session&status=blocked&level=stories&group=flat" });
    expect(screen.getByRole("searchbox", { name: "Search name or key…" })).toHaveValue("session");
    expect(screen.getByRole("combobox", { name: "Status" })).toHaveValue("blocked");
    expect(screen.getByRole("combobox", { name: "Card level" })).toHaveValue("stories");
    expect(screen.getByRole("combobox", { name: "Group by" })).toHaveValue("flat");
  });

  it("keeps the selection when switching views", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/hierarchy?item=TASK-102" });
    expect(await screen.findByTestId("details")).toHaveTextContent("TASK-102");
    await user.click(screen.getByRole("radio", { name: "Kanban" }));
    expect(window.location.hash).toBe("#/kanban?item=TASK-102");
    expect(screen.getByTestId("details")).toHaveTextContent("TASK-102");
    await user.click(screen.getByRole("radio", { name: "Dependencies" }));
    expect(window.location.hash).toBe("#/dependencies?item=TASK-102");
  });

  it("shows kanban controls only on Kanban and forces Flat for top-level cards", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/hierarchy" });
    expect(screen.queryByRole("combobox", { name: "Card level" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "Kanban" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Card level" }), "top");
    expect(screen.getByRole("combobox", { name: "Group by" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Group by" })).toHaveValue("flat");
    expect(window.location.hash).toBe("#/kanban?level=top");
  });

  it("warns when the selection is outside the current view", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/kanban?item=EPIC-12" });
    expect(await screen.findByText("This item is outside the current view.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show in hierarchy" }));
    expect(window.location.hash).toBe("#/hierarchy?item=EPIC-12");
    expect(screen.queryByText("This item is outside the current view.")).not.toBeInTheDocument();
  });

  it("shows the disconnected banner", async () => {
    const { daemon } = renderWithDaemon(<App />);
    await screen.findByRole("button", { name: /Needs you/ });
    daemon.disconnect();
    expect(await screen.findByRole("alert")).toHaveTextContent("Connection lost. Status changes are unavailable.");
  });

  it("replaces the view with the details panel on narrow windows", async () => {
    vi.spyOn(window, "matchMedia").mockImplementation(
      (q: string) => ({ matches: q === "(max-width: 1099px)", addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList,
    );
    const { user } = renderWithDaemon(<App />, { hash: "#/hierarchy?item=TASK-102" });
    expect(screen.queryByTestId("view")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "← Back" }));
    expect(window.location.hash).toBe("#/hierarchy");
    expect(screen.getByTestId("view")).toBeInTheDocument();
  });

  it("offers every type in New item", async () => {
    const { user } = renderWithDaemon(<App />);
    await user.click(screen.getByRole("button", { name: "New item" }));
    expect(within(screen.getByRole("menu", { name: "New item" })).getAllByRole("menuitem").map((m) => m.textContent)).toEqual([
      "Epic", "Bug", "Story", "Task", "Spike",
    ]);
  });

  it("moves focus into the New item menu on open and back to the trigger on close (standing rule)", async () => {
    const { user } = renderWithDaemon(<App />);
    const trigger = screen.getByRole("button", { name: "New item" });
    await user.click(trigger);
    expect(within(screen.getByRole("menu", { name: "New item" })).getAllByRole("menuitem")[0]).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu", { name: "New item" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Epic" }));
    expect(screen.queryByRole("menu", { name: "New item" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });
});

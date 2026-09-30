import { screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { createMockDaemon } from "./mock/daemon";
import { renderWithDaemon } from "./test/render";
import { comboText } from "./test/select";

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

const roomy = () => {
  const d = createMockDaemon();
  d.db.settings.max_concurrent_agents = 8;
  return d;
};

describe("App flows", () => {
  it("opens New orchestrator from the header and presets Feature spike from New item", async () => {
    const { user } = renderWithDaemon(<App />, { daemon: roomy() });
    await user.click(await screen.findByRole("button", { name: "New orchestrator" }));
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.click(within(sheet).getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "New item" }));
    await user.click(await screen.findByRole("menuitem", { name: "Spike" }));
    expect(await screen.findByRole("radio", { name: "Feature spike" })).toHaveAttribute("aria-checked", "true");
  });

  it("creates a spike and selects it", async () => {
    const { user } = renderWithDaemon(<App />, { daemon: roomy() });
    await user.click(await screen.findByRole("button", { name: "New orchestrator" }));
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Offline sync");
    await user.click(await within(sheet).findByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(window.location.hash).toMatch(/item=SPIKE-\d+/));
    expect(screen.queryByRole("dialog", { name: "New orchestrator" })).not.toBeInTheDocument();
  });

  it("creates a chore from New item and selects it (chore spec E16)", async () => {
    const { user } = renderWithDaemon(<App />, { daemon: roomy() });
    await user.click(await screen.findByRole("button", { name: "New item" }));
    await user.click(await screen.findByRole("menuitem", { name: "Chore" }));
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    expect(within(sheet).getByRole("radio", { name: "Chore" })).toHaveAttribute("aria-checked", "true");
    await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Bump deps");
    await user.click(await within(sheet).findByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(window.location.hash).toMatch(/item=CHORE-\d+/));
    expect(screen.queryByRole("dialog", { name: "New orchestrator" })).not.toBeInTheDocument();
  });

  it("creates a story under the selected epic", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/hierarchy?item=EPIC-12" });
    await screen.findByTestId("details-panel");
    await user.click(screen.getByRole("button", { name: "New item" }));
    await user.click(await screen.findByRole("menuitem", { name: "Story" }));
    const sheet = await screen.findByRole("dialog", { name: "New item" });
    expect(await within(sheet).findByRole("combobox", { name: "Parent" })).toBeInTheDocument();
    expect(comboText("Parent")).toContain("EPIC-12");
    await user.type(within(sheet).getByRole("textbox", { name: "Title" }), "Two-factor");
    await user.click(within(sheet).getByRole("button", { name: "Create item" }));
    await waitFor(() => expect(window.location.hash).toMatch(/item=STORY-\d+/));
  });

  it("adds a task from the hierarchy context menu", async () => {
    const { user } = renderWithDaemon(<App />);
    const row = await screen.findByRole("treeitem", { name: /^STORY-40 / });
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true }));
    await user.click(await screen.findByRole("menuitem", { name: "Add task" }));
    const sheet = await screen.findByRole("dialog", { name: "New item" });
    expect(within(sheet).getByRole("radio", { name: "Task" })).toHaveAttribute("aria-checked", "true");
    expect(await within(sheet).findByRole("combobox", { name: "Parent" })).toBeInTheDocument();
    expect(comboText("Parent")).toContain("STORY-40");
  });

  it("opens the spawn sheet from Details", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/hierarchy?item=EPIC-20", daemon: roomy() });
    await user.click(await screen.findByRole("button", { name: "Start orchestrator" }));
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    expect(within(sheet).getByText("EPIC-20 · Billing")).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Start orchestrator" })).not.toBeInTheDocument();
  });

  it("routes an epic dropped on Done to its acceptance review", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/kanban?level=top" });
    await user.click(await screen.findByRole("button", { name: "Move to… EPIC-12" }));
    await user.click(await screen.findByRole("menuitem", { name: /^Done/ }));
    expect(window.location.hash).toBe("#/inbox?level=top&req=req_accept");
    expect(await screen.findByRole("heading", { name: "Finish epic · EPIC-12 › Authentication" })).toBeInTheDocument();
  });

  it("reviews from Details and resolves a request in the inbox", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/hierarchy?item=SPIKE-3" });
    const needs = await screen.findByRole("region", { name: "Needs you" });
    await user.click(within(needs).getAllByRole("button", { name: "Review" })[1]!);
    expect(window.location.hash).toBe("#/inbox?item=SPIKE-3&req=req_section");
    expect(screen.queryByTestId("details")).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Approve section" }));
    expect(await screen.findByText("Already resolved.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "View item" }));
    expect(window.location.hash).toBe("#/hierarchy?item=SPIKE-3");
  });

  it("filters the inbox through the URL", async () => {
    const { user } = renderWithDaemon(<App />, { hash: "#/inbox" });
    await user.click(await screen.findByRole("radio", { name: "Questions" }));
    expect(window.location.hash).toBe("#/inbox?filter=questions");
    await user.click(screen.getByRole("button", { name: /TASK-104/ }));
    expect(window.location.hash).toBe("#/inbox?req=req_q2&filter=questions");
    expect(screen.queryByRole("textbox", { name: "Answer" })).not.toBeInTheDocument();
  });
});

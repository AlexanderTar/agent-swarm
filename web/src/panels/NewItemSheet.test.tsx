import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { NewItemSheet } from "./NewItemSheet";

describe("NewItemSheet (§16.5)", () => {
  it("shows the parent picker for stories and tasks with the right options", async () => {
    const { user } = renderWithDaemon(<NewItemSheet type="story" parentKey="BUG-7" onClose={vi.fn()} onCreated={vi.fn()} />, { events: false });
    const sheet = await screen.findByRole("dialog", { name: "New item" });
    const parent = await within(sheet).findByRole("combobox", { name: "Parent" });
    expect(parent).toHaveValue("");
    expect([...(parent as HTMLSelectElement).options].map((o) => o.value)).toEqual(["", "EPIC-12", "EPIC-20", "EPIC-30"]);
    await user.click(within(sheet).getByRole("radio", { name: "Task" }));
    expect(within(sheet).getByRole("combobox", { name: "Parent" })).toHaveValue("BUG-7");
    await user.click(within(sheet).getByRole("radio", { name: "Epic" }));
    expect(within(sheet).queryByRole("combobox", { name: "Parent" })).not.toBeInTheDocument();
  });

  it("keeps Create item disabled until valid", async () => {
    const { user } = renderWithDaemon(<NewItemSheet type="task" onClose={vi.fn()} onCreated={vi.fn()} />, { events: false });
    const create = await screen.findByRole("button", { name: "Create item" });
    expect(create).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "Title" }), "Add tests");
    expect(create).toBeDisabled();
    await user.selectOptions(await screen.findByRole("combobox", { name: "Parent" }), "STORY-40");
    expect(create).toBeEnabled();
    expect(screen.getByRole("textbox", { name: "Brief" })).toHaveAttribute("maxLength", "600");
    expect(screen.getByText("0/600")).toBeInTheDocument();
  });

  it("creates exactly one item on a double click with acceptance criteria", async () => {
    const d = createMockDaemon();
    const before = d.db.items.length;
    const onCreated = vi.fn();
    const { user } = renderWithDaemon(<NewItemSheet type="epic" onClose={vi.fn()} onCreated={onCreated} />, { daemon: d, events: false });
    await user.type(await screen.findByRole("textbox", { name: "Title" }), "Payments");
    await user.type(screen.getByRole("textbox", { name: "Acceptance 1" }), "Cards work");
    await user.click(screen.getByRole("button", { name: "Add Acceptance" }));
    await user.type(screen.getByRole("textbox", { name: "Acceptance 2" }), "Refunds work");
    await user.dblClick(screen.getByRole("button", { name: "Create item" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^EPIC-/)));
    expect(d.db.items.length).toBe(before + 1);
    expect(d.db.items.at(-1)).toMatchObject({ title: "Payments", acceptance: ["Cards work", "Refunds work"], status: "draft" });
  });

  it("shows a daemon error", async () => {
    const d = createMockDaemon();
    d.override("POST /api/items", { status: 400, body: { error: { code: "bad_request", message: "invalid parent" } } });
    const { user } = renderWithDaemon(<NewItemSheet type="bug" onClose={vi.fn()} onCreated={vi.fn()} />, { daemon: d, events: false });
    await user.type(await screen.findByRole("textbox", { name: "Title" }), "Crash");
    await user.click(screen.getByRole("button", { name: "Create item" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("invalid parent");
  });

  // Standing rule (flagged, not in the brief): a failed query gets a message + retry. The brief's
  // NewItemSheet used `items.data?.items ?? []` with no error check at all, so a failed items load
  // would silently degrade the parent picker to "no options" forever, with no indication anything
  // was wrong.
  it("surfaces a failed items load with a working retry (standing rule)", async () => {
    const d = createMockDaemon();
    const real = d.handle({ method: "GET", url: "/api/items?view=flat", headers: { authorization: `Bearer ${d.db.token}` } });
    let attempt = 0;
    d.override("GET /api/items", () => {
      attempt += 1;
      if (attempt === 1) return { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load items." } } };
      return real;
    });
    const { user } = renderWithDaemon(<NewItemSheet type="story" onClose={vi.fn()} onCreated={vi.fn()} />, { daemon: d, events: false });
    expect(await screen.findByText("Couldn't load items.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Couldn't load items.")).not.toBeInTheDocument());
    expect(await screen.findByRole("combobox", { name: "Parent" })).toBeInTheDocument();
  });
});

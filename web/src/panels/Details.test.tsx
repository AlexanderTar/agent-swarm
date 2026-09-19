import { screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import type { DetailsProps } from "../views/props";
import { Details } from "./Details";

function setup(itemKey: string, p: Partial<DetailsProps> = {}, daemon = createMockDaemon()) {
  const props: DetailsProps = {
    itemKey, connected: true, onClose: vi.fn(), onSelect: vi.fn(), onReview: vi.fn(), onStartOrchestrator: vi.fn(), ...p,
  };
  return { ...renderWithDaemon(<Details {...props} />, { daemon, events: false }), props };
}

// App.tsx renders `<Details key={url.item} itemKey={url.item} .../>` — the `key` is what forces a
// full remount (and a clean slate for any open editor) when the viewed item changes. Reproduced
// directly here rather than through App.tsx, since App.tsx does the identical thing with the same
// element.
function detailsFor(itemKey: string) {
  return (
    <Details key={itemKey} itemKey={itemKey} connected onClose={vi.fn()} onSelect={vi.fn()} onReview={vi.fn()} onStartOrchestrator={vi.fn()} />
  );
}

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
});

describe("Details panel (§16.9)", () => {
  it("shows the breadcrumb, title, status and priority", async () => {
    const { user, props } = setup("STORY-40");
    expect(await screen.findByText("EPIC-12 › STORY-40")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Login" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "In progress" })).toHaveAttribute("aria-haspopup", "menu");
    expect(screen.getByRole("combobox", { name: "Priority" })).toHaveValue("2");
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(props.onClose).toHaveBeenCalled();
  });

  it("edits the title in place and shows the conflict banner", async () => {
    const d = createMockDaemon();
    const { user, daemon } = setup("TASK-103", {}, d);
    await user.click(await screen.findByRole("button", { name: "Password reset form" }));
    const input = screen.getByRole("textbox", { name: "Title" });
    await user.clear(input);
    await user.type(input, "Reset form{Enter}");
    await waitFor(() => expect(daemon.calls.find((c) => c.method === "PATCH")?.body).toMatchObject({ title: "Reset form", revision: 1 }));
    expect(await screen.findByRole("button", { name: "Reset form" })).toBeInTheDocument();
    d.override("PATCH /api/items/TASK-103", { status: 409, body: { error: { code: "conflict", message: "stale" } } });
    await user.click(screen.getByRole("button", { name: "Brief" }));
    await user.type(screen.getByRole("textbox", { name: "Brief" }), "More detail");
    await user.tab();
    expect(await screen.findByText("This item changed elsewhere. Showing its latest status.")).toBeInTheDocument();
  });

  it("changes status and priority, and explains refusals", async () => {
    const { user, daemon } = setup("TASK-103");
    await user.click(await screen.findByRole("button", { name: "Ready" }));
    expect(screen.getByRole("menuitem", { name: /^Done/ })).toBeDisabled();
    await user.click(screen.getByRole("menuitem", { name: /^Blocked/ }));
    await waitFor(() => expect(daemon.calls.find((c) => c.method === "PATCH")?.body).toMatchObject({ status: "blocked" }));
    await user.selectOptions(await screen.findByRole("combobox", { name: "Priority" }), "0");
    await waitFor(() => expect(daemon.calls.filter((c) => c.method === "PATCH").at(-1)?.body).toMatchObject({ priority: 0 }));
  });

  it("routes Done on an epic to acceptance", async () => {
    const { user, props } = setup("EPIC-12");
    await user.click(await screen.findByRole("button", { name: "In progress" }));
    await user.click(screen.getByRole("menuitem", { name: /^Done/ }));
    expect(props.onReview).toHaveBeenCalledWith("req_accept");
  });

  it("lists what needs you with Review buttons", async () => {
    const { user, props } = setup("SPIKE-3");
    const section = await screen.findByRole("region", { name: "Needs you" });
    expect(within(section).getAllByRole("listitem").map((li) => li.firstChild?.textContent)).toEqual([
      "Which sync strategy?", 'Approve "Data model"', "Approve plan", "Confirm 2 repositories",
    ]);
    await user.click(within(section).getAllByRole("button", { name: "Review" })[1]!);
    expect(props.onReview).toHaveBeenCalledWith("req_section");
  });

  it("lists agents and scrolls to them when asked", async () => {
    setup("EPIC-12", { focus: "agents" });
    const agents = await screen.findByRole("region", { name: "Agents" });
    expect(within(agents).getByTestId("agent-auth-epic-orchestrator")).toBeInTheDocument();
    expect(within(agents).getByTestId("agent-login-form-coder")).toBeInTheDocument();
    await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalled());
  });

  it("shows the overview: brief, acceptance, dependencies, artifacts and origin", async () => {
    const { user, props } = setup("EPIC-12");
    expect(await screen.findByText("Sign-in for the chat app.")).toBeInTheDocument();
    expect(screen.getByText("Users can log in")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Spec · rev 3 · View" }));
    const dialog = screen.getByRole("dialog");
    expect(await within(dialog).findByText("Data model")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    await user.click(screen.getByRole("button", { name: "Started from SPIKE-2" }));
    expect(props.onSelect).toHaveBeenCalledWith("SPIKE-2");
  });

  it("shows blocked-by and blocks lines", async () => {
    setup("TASK-102");
    expect(await screen.findByText("TASK-98 (Done)")).toBeInTheDocument();
    expect(screen.getByText("TASK-104")).toBeInTheDocument();
  });

  it("adds a dependency and shows the cycle error", async () => {
    const { user, daemon } = setup("TASK-98");
    await user.click(await screen.findByRole("button", { name: "+ Add dependency" }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "validate");
    await user.click(await screen.findByRole("button", { name: "TASK-104 · Validate inputs" }));
    expect(await screen.findByText("This dependency would create a cycle.")).toBeInTheDocument();
    await user.clear(screen.getByRole("searchbox", { name: "Add dependency" }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "TASK-110");
    await user.click(await screen.findByRole("button", { name: "TASK-110 · Add crash regression test" }));
    await waitFor(() => expect(daemon.calls.some((c) => c.path === "/api/items/TASK-98/deps" && (c.body as { blocked_by: string }).blocked_by === "TASK-110")).toBe(true));
  });

  it("shows the checkpoints tab", async () => {
    const { user } = setup("TASK-101");
    await user.click(await screen.findByRole("tab", { name: "Checkpoints" }));
    expect(await screen.findByText(/Form renders; wiring submit\./)).toBeInTheDocument();
  });

  it("offers Start or View orchestrator on top-level items only", async () => {
    const a = setup("EPIC-20");
    await a.user.click(await screen.findByRole("button", { name: "Start orchestrator" }));
    expect(a.props.onStartOrchestrator).toHaveBeenCalledWith(expect.objectContaining({ key: "EPIC-20" }));
    a.unmount();
    const b = setup("EPIC-12");
    await b.user.click(await screen.findByRole("button", { name: "View orchestrator" }));
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    b.unmount();
    setup("TASK-101");
    await screen.findByText(/Build login form/);
    expect(screen.queryByRole("button", { name: "Start orchestrator" })).not.toBeInTheDocument();
  });

  it("disables changes while disconnected", async () => {
    setup("EPIC-20", { connected: false });
    expect(await screen.findByRole("button", { name: "Start orchestrator" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Draft" })).toBeDisabled();
  });

  // Standing rule (flagged, not in the brief): a failed query gets a message + retry, never a
  // permanent placeholder. The brief's stub only ever rendered "…" or the loaded panel.
  it("surfaces a failed item load with the daemon's reason and a working retry (standing rule)", async () => {
    const d = createMockDaemon();
    const real = d.handle({ method: "GET", url: "/api/items/TASK-101", headers: { authorization: `Bearer ${d.db.token}` } });
    let attempt = 0;
    d.override("GET /api/items/TASK-101", () => {
      attempt += 1;
      if (attempt === 1) return { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load the item." } } };
      return real;
    });
    const { user } = setup("TASK-101", {}, d);
    expect(await screen.findByText("Couldn't load the item.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Couldn't load the item.")).not.toBeInTheDocument());
    expect(await screen.findByTestId("details-panel")).toBeInTheDocument();
  });

  // Standing rule: ArtifactViewer's own query needs the same treatment — the brief left it showing
  // "…" forever on a failed load.
  it("surfaces a failed artifact load with a working retry (standing rule)", async () => {
    const d = createMockDaemon();
    const real = d.handle({ method: "GET", url: "/api/artifacts/art_epic_spec?revision=3", headers: { authorization: `Bearer ${d.db.token}` } });
    let attempt = 0;
    d.override("GET /api/artifacts/art_epic_spec", () => {
      attempt += 1;
      if (attempt === 1) return { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load the artifact." } } };
      return real;
    });
    const { user } = setup("EPIC-12", {}, d);
    await user.click(await screen.findByRole("button", { name: "Spec · rev 3 · View" }));
    const dialog = screen.getByRole("dialog");
    expect(await within(dialog).findByText("Couldn't load the artifact.")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(within(dialog).queryByText("Couldn't load the artifact.")).not.toBeInTheDocument());
  });

  // Standing rule (flagged, not in the brief): transient UI takes focus on open and restores it to
  // its trigger on close.
  it("focuses the artifact dialog on open and restores focus to its trigger on close", async () => {
    const { user } = setup("EPIC-12");
    const opener = await screen.findByRole("button", { name: "Spec · rev 3 · View" });
    await user.click(opener);
    const dialog = screen.getByRole("dialog");
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Close" })).toHaveFocus());
    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(opener).toHaveFocus();
  });

  it("focuses the dependency search on open and restores focus to its trigger on close", async () => {
    const { user } = setup("TASK-98");
    const opener = await screen.findByRole("button", { name: "+ Add dependency" });
    await user.click(opener);
    expect(screen.getByRole("searchbox", { name: "Add dependency" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(opener).toHaveFocus();
  });

  it("keeps the typed draft after a failed save instead of discarding it", async () => {
    const d = createMockDaemon();
    d.override("PATCH /api/items/TASK-103", { status: 500, body: { error: { code: "internal", message: "boom" } } });
    const { user } = setup("TASK-103", {}, d);
    await user.click(await screen.findByRole("button", { name: "Password reset form" }));
    const input = screen.getByRole("textbox", { name: "Title" });
    await user.clear(input);
    await user.type(input, "Reset form{Enter}");
    await waitFor(() => expect(d.calls.some((c) => c.method === "PATCH")).toBe(true));
    // The failed save must not discard the typed text or fall back to the server's stale copy —
    // the editor stays open with exactly what the user typed.
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Reset form");
    expect(screen.queryByRole("button", { name: "Password reset form" })).not.toBeInTheDocument();
  });

  it("disables the priority select and edit triggers while a patch is in flight", async () => {
    const d = createMockDaemon();
    const release = d.hold("PATCH /api/items/TASK-103");
    const { user } = setup("TASK-103", {}, d);
    const titleButton = await screen.findByRole("button", { name: "Password reset form" });
    const briefButton = screen.getByRole("button", { name: "Brief" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Priority" }), "0");
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Priority" })).toBeDisabled());
    expect(titleButton).toBeDisabled();
    expect(briefButton).toBeDisabled();
    release();
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Priority" })).toBeEnabled());
  });

  it("returns focus to the title button after an in-place edit saves", async () => {
    const { user } = setup("TASK-103");
    const titleButton = await screen.findByRole("button", { name: "Password reset form" });
    await user.click(titleButton);
    const input = screen.getByRole("textbox", { name: "Title" });
    await user.clear(input);
    await user.type(input, "Reset form{Enter}");
    await waitFor(() => expect(screen.getByRole("button", { name: "Reset form" })).toHaveFocus());
  });

  // Regression (final review, App.tsx): without `key={url.item}` on <Details>, a failed save left
  // an editor open holding its draft, and switching to an already-cached item reused the same
  // component instance instead of remounting — so the stale draft leaked onto the new item's panel
  // and committing it (Enter/blur) would PATCH the wrong item with the wrong text.
  it("drops a stale open editor instead of leaking its draft onto the next item", async () => {
    const d = createMockDaemon();
    const { user, rerender } = renderWithDaemon(detailsFor("TASK-101"), { daemon: d, events: false });
    // Load TASK-101 first so its detail is already cached when we switch back to it later —
    // matching the bug report's "detail already cached, so `!d` doesn't unmount anything".
    await screen.findByRole("button", { name: "Build login form" });

    rerender(detailsFor("TASK-103"));
    await user.click(await screen.findByRole("button", { name: "Password reset form" }));
    const input = screen.getByRole("textbox", { name: "Title" });
    await user.clear(input);
    await user.type(input, "Corrupted title");
    d.override("PATCH /api/items/TASK-103", { status: 409, body: { error: { code: "conflict", message: "stale" } } });
    await user.keyboard("{Enter}");
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/items/TASK-103" && c.method === "PATCH")).toBe(true));
    // Pre-existing, correct behavior: a failed save keeps the editor open with the typed draft.
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Corrupted title");

    rerender(detailsFor("TASK-101"));
    // TASK-101's own title shows, not a leftover editor holding TASK-103's draft.
    expect(await screen.findByRole("button", { name: "Build login form" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Title" })).not.toBeInTheDocument();
    // Nothing must ever PATCH TASK-101 with the stale "Corrupted title" draft.
    expect(d.calls.some((c) => c.path === "/api/items/TASK-101" && c.method === "PATCH")).toBe(false);
  });
});

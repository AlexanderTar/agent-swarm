import { screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { C } from "../copy";
import { createMockDaemon } from "../mock/daemon";
import { NOW } from "../mock/fixtures";
import { renderWithDaemon } from "../test/render";
import { RepoPicker } from "./RepoPicker";

function Host({ initial = [] as string[] }) {
  const [sel, setSel] = useState(initial);
  return <RepoPicker label="Repositories (optional)" caption="The spike suggests repositories and asks you to confirm them." selected={sel} onChange={setSel} />;
}

const rows = () => within(screen.getByRole("listbox", { name: "Repositories (optional)" })).getAllByRole("option");

describe("RepoPicker (§16.3)", () => {
  it("lists each present repo once, sorted, with its path and dirty state", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
    renderWithDaemon(<Host />, { events: false });
    await screen.findByRole("listbox", { name: "Repositories (optional)" });
    const names = rows().map((r) => r.getAttribute("data-name"));
    expect(names).toEqual([...names].sort((a, b) => a!.localeCompare(b!)));
    expect(names).not.toContain("old-api");
    expect(new Set(names).size).toBe(names.length);
    expect(screen.getAllByText(/^~\/GitHub\//).length).toBeGreaterThan(0);
    expect(screen.getByText("Scanned 2h ago")).toBeInTheDocument();
    expect(screen.getByText("The spike suggests repositories and asks you to confirm them.")).toBeInTheDocument();
    expect(rows().some((r) => within(r).queryByLabelText(C.repoDirty))).toBe(true);
    expect(screen.getAllByRole("img", { name: C.repoDirty }).length).toBeGreaterThan(0);
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

  it("waits for a delayed rescan response before removing selected repos and announces the change", async () => {
    const daemon = createMockDaemon();
    const { user } = renderWithDaemon(<Host initial={["repo_chat"]} />, { daemon, events: false });
    await screen.findByRole("option", { name: /endurio-chat/ });
    const release = daemon.hold("GET /api/repos");
    daemon.db.repos.all = daemon.db.repos.all.filter((r) => r.id !== "repo_chat");
    await user.click(screen.getByRole("button", { name: "Rescan" }));
    expect(screen.getByText("Selected: endurio-chat")).toBeInTheDocument();
    expect(screen.queryByText("1 selected repository is no longer available.")).not.toBeInTheDocument();
    release();
    const notice = await screen.findByText("1 selected repository is no longer available.");
    expect(notice).toHaveAttribute("aria-live", "polite");
    expect(screen.queryByText("Selected: endurio-chat")).not.toBeInTheDocument();
  });

  it("keeps the listbox for empty and scanning states", async () => {
    const daemon = createMockDaemon();
    daemon.db.repos.all = [];
    daemon.db.repos.recent = [];
    daemon.db.repos.groups = [];
    renderWithDaemon(<Host />, { daemon, events: false });
    expect(await screen.findByText("No repositories found.")).toBeInTheDocument();
    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });

  it("adds a folder and rescans", async () => {
    const { user, daemon } = renderWithDaemon(<Host />, { events: false });
    await screen.findByRole("listbox");
    await user.click(screen.getByRole("button", { name: "Add folder…" }));
    await user.type(screen.getByRole("textbox", { name: "Add folder…" }), "/tmp/not-a-repo{Enter}");
    expect(await screen.findByText("No git repository found in this folder.")).toBeInTheDocument();
    await user.clear(screen.getByRole("textbox", { name: "Add folder…" }));
    await user.type(screen.getByRole("textbox", { name: "Add folder…" }), "/Users/alex/code/newrepo{Enter}");
    expect(await screen.findByText("Selected: newrepo")).toBeInTheDocument();
    expect(await screen.findByText("Added newrepo")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Rescan" }));
    await waitFor(() => expect(daemon.calls.some((c) => c.path === "/api/repos/rescan")).toBe(true));
    expect(await screen.findByText(/^Found \d+ repositories/)).toBeInTheDocument();
  });

  it("shows the daemon's reason on a failed add", async () => {
    const daemon = createMockDaemon();
    daemon.override("POST /api/repos", { status: 422, body: { error: { code: "bad_request", message: "x", reason: "That folder has no .git directory." } } });
    const { user } = renderWithDaemon(<Host />, { daemon, events: false });
    await screen.findByRole("listbox");
    await user.click(screen.getByRole("button", { name: "Add folder…" }));
    await user.type(screen.getByRole("textbox", { name: "Add folder…" }), "/Users/alex/code/whatever{Enter}");
    expect(await screen.findByText("That folder has no .git directory.")).toBeInTheDocument();
  });

  it("shows the scanning footer", async () => {
    const daemon = createMockDaemon();
    daemon.db.repos.scanning = true;
    renderWithDaemon(<Host />, { daemon, events: false });
    expect(await screen.findByText("Scanning your home folder…")).toBeInTheDocument();
  });
});

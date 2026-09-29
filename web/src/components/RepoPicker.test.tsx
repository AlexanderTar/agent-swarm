import { act, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { C } from "../copy";
import { useConnection } from "../data/hooks";
import { createMockDaemon } from "../mock/daemon";
import { NOW } from "../mock/fixtures";
import { renderWithDaemon } from "../test/render";
import { RepoPicker } from "./RepoPicker";

function Host({ initial = [] as string[] }) {
  const [sel, setSel] = useState(initial);
  return <RepoPicker label="Repositories (optional)" caption="The spike suggests repositories and asks you to confirm them." selected={sel} onChange={setSel} />;
}

function ReconnectHost() {
  const { retry } = useConnection();
  return <><Host /><button type="button" onClick={retry}>Reconnect stream</button></>;
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

  it("shows a failed refresh instead of stale rows and restores selection on Retry", async () => {
    const daemon = createMockDaemon();
    const { user } = renderWithDaemon(<Host initial={["repo_chat"]} />, { daemon, events: false });
    expect(await screen.findByRole("option", { name: /endurio-chat/ })).toHaveAttribute("aria-selected", "true");
    const recovered = daemon.handle({ method: "GET", url: "/api/repos", headers: { authorization: `Bearer ${daemon.db.token}` } });
    daemon.override("GET /api/repos", { status: 500, body: { error: { code: "internal", message: "Refresh failed." } } });

    await user.click(screen.getByRole("button", { name: C.rescan }));
    expect(await screen.findByRole("alert")).toHaveTextContent(C.reposUnavailable);
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("1 selected")).toBeInTheDocument();

    daemon.override("GET /api/repos", recovered);
    const release = daemon.hold("GET /api/repos");
    await user.click(screen.getByRole("button", { name: C.retry }));
    await waitFor(() => expect(daemon.calls.filter((c) => c.method === "GET" && c.path === "/api/repos")).toHaveLength(3));
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    await act(async () => { release(); });
    expect(await screen.findByRole("option", { name: /endurio-chat/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByText(C.reposUnavailable)).not.toBeInTheDocument();
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

  it("disables Add folder, inline Add, and Rescan after disconnect", async () => {
    const { daemon, user } = renderWithDaemon(<Host />);
    await screen.findByRole("listbox");
    await user.click(screen.getByRole("button", { name: C.addFolder }));
    daemon.disconnect();
    await waitFor(() => expect(screen.getByRole("button", { name: C.add })).toBeDisabled());
    expect(screen.getByRole("button", { name: C.addFolder })).toBeDisabled();
    expect(screen.getByRole("button", { name: C.rescan })).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: C.addFolder }), "/tmp/repo{Enter}");
    expect(daemon.calls.filter((c) => c.method === "POST" && c.path.startsWith("/api/repos"))).toHaveLength(0);
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });

  it.each(["add", "rescan"])("shows no toast when disconnected during pending %s", async (action) => {
    const daemon = createMockDaemon();
    const { user } = renderWithDaemon(<Host />, { daemon });
    await screen.findByRole("listbox");
    const route = action === "add" ? "POST /api/repos" : "POST /api/repos/rescan";
    if (action === "add") {
      await user.click(screen.getByRole("button", { name: C.addFolder }));
      await user.type(screen.getByRole("textbox", { name: C.addFolder }), "/Users/alex/code/newrepo");
    }
    const release = daemon.hold(route);
    await user.click(screen.getByRole("button", { name: action === "add" ? C.add : C.rescan }));
    daemon.disconnect();
    await waitFor(() => expect(screen.getByRole("button", { name: C.rescan })).toBeDisabled());
    await act(async () => { release(); });
    await waitFor(() => expect(daemon.calls.some((c) => `${c.method} ${c.path}` === route)).toBe(true));
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });

  it.each([
    { action: "add", failed: false },
    { action: "rescan", failed: false },
    { action: "rescan", failed: true },
  ])("shows no toast when pending $action crosses disconnect and reconnect (failed: $failed)", async ({ action, failed }) => {
    const daemon = createMockDaemon();
    const { user } = renderWithDaemon(<ReconnectHost />, { daemon });
    await screen.findByRole("listbox");
    const route = action === "add" ? "POST /api/repos" : "POST /api/repos/rescan";
    if (action === "add") {
      await user.click(screen.getByRole("button", { name: C.addFolder }));
      await user.type(screen.getByRole("textbox", { name: C.addFolder }), "/Users/alex/code/newrepo");
    }
    if (failed) daemon.override(route, { status: 500, body: { error: { code: "internal", message: "Rescan failed." } } });
    const release = daemon.hold(route);
    await user.click(screen.getByRole("button", { name: action === "add" ? C.add : C.rescan }));
    act(() => daemon.disconnect());
    await waitFor(() => expect(screen.getByRole("button", { name: C.rescan })).toBeDisabled());
    daemon.reconnect();
    await user.click(screen.getByRole("button", { name: "Reconnect stream" }));
    await waitFor(() => expect(screen.getByRole("button", { name: C.addFolder })).toBeEnabled());
    await act(async () => { release(); });
    await waitFor(() => expect(screen.getByRole("button", { name: C.rescan })).toBeEnabled());
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });
});

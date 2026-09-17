import { screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { NOW } from "../mock/fixtures";
import { renderWithDaemon } from "../test/render";
import { RepoPicker } from "./RepoPicker";

function Host({ initial = [] as string[] }) {
  const [sel, setSel] = useState(initial);
  return <RepoPicker label="Repositories (optional)" caption="The spike suggests repositories and asks you to confirm them." selected={sel} onChange={setSel} />;
}

// F2: RepoPicker's own <fieldset> also has the implicit ARIA role "group" but no aria-label, so a
// raw getAllByRole("group") includes it as a leading, unlabeled entry. Filter to the labeled section
// groups (the ones repoSections() actually produces) instead of slicing blindly.
const labeledGroups = () => screen.getAllByRole("group").filter((g) => g.hasAttribute("aria-label"));

describe("RepoPicker (§16.3)", () => {
  it("shows sections, subtitles and repo states", async () => {
    // F3: scanLine's "Scanned Nh ago" is computed against the real clock; pin it to the fixture's NOW
    // (scanned_at is NOW - 120 min) so this doesn't depend on what hour the suite happens to run in.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
    renderWithDaemon(<Host />, { events: false });
    const recent = await screen.findByRole("group", { name: "Recent" });
    expect(within(recent).getByRole("checkbox", { name: /endurio-chat/ })).toBeInTheDocument();
    expect(within(recent).getByText("~/GitHub · EndurioApp")).toBeInTheDocument();
    expect(labeledGroups().map((g) => g.getAttribute("aria-label"))).toEqual(["Recent", "endurio", "AlexanderTar", "EndurioApp", "All"]);
    const all = screen.getByRole("group", { name: "All" });
    expect(within(all).getByRole("checkbox", { name: /old-api/ })).toBeDisabled();
    expect(within(all).getByText("Repository is unavailable. Choose another location.")).toBeInTheDocument();
    expect(within(all).getByText("Has uncommitted changes. The orchestrator works in its own worktree.")).toBeInTheDocument();
    expect(screen.getByText("Scanned 2h ago")).toBeInTheDocument();
    expect(screen.getByText("The spike suggests repositories and asks you to confirm them.")).toBeInTheDocument();
  });

  it("selects repos, selects a whole group and summarises", async () => {
    const { user } = renderWithDaemon(<Host />, { events: false });
    const recent = await screen.findByRole("group", { name: "Recent" });
    await user.click(within(recent).getByRole("checkbox", { name: /endurio-chat/ }));
    expect(screen.getByText("Selected: endurio-chat")).toBeInTheDocument();
    expect(within(screen.getByRole("group", { name: "All" })).getByRole("checkbox", { name: /endurio-chat/ })).toBeChecked();
    await user.click(within(screen.getByRole("group", { name: "endurio" })).getByRole("button", { name: "All" }));
    expect(screen.getByText("Selected: endurio-chat, endurio-app, endurio-landing")).toBeInTheDocument();
  });

  it("searches", async () => {
    const { user, daemon } = renderWithDaemon(<Host />, { events: false });
    await screen.findByRole("group", { name: "Recent" });
    await user.type(screen.getByPlaceholderText("Search repos…"), "landing");
    await waitFor(() => expect(daemon.calls.at(-1)?.path).toBe("/api/repos?q=landing"));
    // The daemon fetch for the new query key starts with no cached data, so "Recent" disappearing
    // could mean either "the filtered results loaded and have no Recent section" or "nothing has
    // rendered yet because the new key is still loading" (repoSections() only ever runs once `data`
    // is defined). Wait for the *loaded* state — the "All" group settling at exactly one checkbox,
    // which only happens once the "landing" response has actually landed — before trusting anything
    // else about what's on screen.
    await waitFor(() => expect(within(screen.getByRole("group", { name: "All" })).getAllByRole("checkbox")).toHaveLength(1));
    expect(screen.queryByRole("group", { name: "Recent" })).not.toBeInTheDocument();
  });

  it("adds a folder and rescans", async () => {
    const { user, daemon } = renderWithDaemon(<Host />, { events: false });
    await screen.findByRole("group", { name: "Recent" });
    await user.click(screen.getByRole("button", { name: "Add folder…" }));
    await user.type(screen.getByRole("textbox", { name: "Add folder…" }), "/tmp/not-a-repo{Enter}");
    expect(await screen.findByText("No git repository found in this folder.")).toBeInTheDocument();
    await user.clear(screen.getByRole("textbox", { name: "Add folder…" }));
    await user.type(screen.getByRole("textbox", { name: "Add folder…" }), "/Users/alex/code/newrepo{Enter}");
    expect(await screen.findByText("Selected: newrepo")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Rescan" }));
    await waitFor(() => expect(daemon.calls.some((c) => c.path === "/api/repos/rescan")).toBe(true));
  });

  it("shows the scanning footer", async () => {
    const daemon = createMockDaemon();
    daemon.db.repos.scanning = true;
    renderWithDaemon(<Host />, { daemon, events: false });
    expect(await screen.findByText("Scanning your home folder…")).toBeInTheDocument();
  });
});

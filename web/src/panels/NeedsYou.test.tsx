import { screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import type { InboxFilter } from "../types";
import { NeedsYou } from "./NeedsYou";

function Host(p: { initial?: string; onViewItem?: (k: string) => void }) {
  const [filter, setFilter] = useState<InboxFilter>("all");
  const [sel, setSel] = useState(p.initial ?? "");
  return (
    <NeedsYou
      filter={filter}
      selected={sel}
      connected
      onFilter={setFilter}
      onSelectRequest={setSel}
      onViewItem={p.onViewItem ?? vi.fn()}
      renderReview={(r) => <p>{`review:${r.id}`}</p>}
    />
  );
}

describe("NeedsYou inbox (§16.11)", () => {
  it("lists requests oldest first and shows the first one", async () => {
    const { user } = renderWithDaemon(<Host />, { events: false });
    const list = await screen.findByRole("list", { name: "Needs you" });
    // "all" now includes every open request, not just the HITL ones (2.2.5): 9 rows, none showing
    // a prompt — the row is the generic item/agent/"Waiting for your input" text (2.2.2).
    const rows = within(list).getAllByRole("button", { name: /Waiting for your input/ });
    expect(rows).toHaveLength(9);
    expect(rows[0]).toHaveTextContent("EPIC-12 · Authentication");
    expect(rows[0]).toHaveAttribute("aria-current", "true");
    expect(screen.queryByText("Which sync strategy?")).not.toBeInTheDocument();
    expect(screen.getByText("review:req_accept")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "Approvals" }));
    const appRows = within(screen.getByRole("list", { name: "Needs you" })).getAllByRole("button", { name: /Waiting for your input/ });
    // accept_epic/accept_fix are approvals too (2026-09-26 epic-approval-lane)
    expect(appRows).toHaveLength(7);
    expect(appRows[0]).toHaveTextContent("EPIC-12 · Authentication");
  });

  it("filters questions and approvals and selects a row", async () => {
    const { user } = renderWithDaemon(<Host />, { events: false });
    await screen.findByRole("list", { name: "Needs you" });
    await user.click(screen.getByRole("radio", { name: "Questions" }));
    expect(within(screen.getByRole("list", { name: "Needs you" })).getAllByRole("button", { name: /Waiting for your input/ })).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: /TASK-104/ }));
    expect(screen.getByText("review:req_q2")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "Approvals" }));
    expect(within(screen.getByRole("list", { name: "Needs you" })).getAllByRole("button", { name: /Waiting for your input/ })).toHaveLength(7);
  });

  it("has a terminal button that opens the terminal; selecting a row alone never does", async () => {
    const d = createMockDaemon();
    const { user } = renderWithDaemon(<Host />, { daemon: d, events: false });
    await user.click(await screen.findByRole("radio", { name: "Questions" }));
    const questionsList = screen.getByRole("list", { name: "Needs you" });
    const icons = within(questionsList).getAllByRole("button", { name: "Open agent terminal" });
    // both questions have a live terminal_agent
    expect(icons).toHaveLength(2);
    await user.click(icons[0]!); // req_question, the oldest
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/agents/offline-spike-orchestrator/terminal")).toBe(true));

    const before = d.calls.length;
    await user.click(screen.getByRole("radio", { name: "Approvals" }));
    const approvalsList = screen.getByRole("list", { name: "Needs you" });
    // Approvals with no terminal_agent (req_accept, oldest, before an
    // orchestrator binds it) show no icon at all (2.2.2).
    expect(within(approvalsList).queryAllByRole("button", { name: "Open agent terminal" })).toHaveLength(0);
    await user.click(within(approvalsList).getAllByRole("button", { name: /Waiting for your input/ })[0]!);
    expect(screen.getByText("review:req_accept")).toBeInTheDocument();
    expect(d.calls.slice(before).some((c) => c.path.endsWith("/terminal"))).toBe(false);
  });

  it("shows Already resolved for a request that closed", async () => {
    const d = createMockDaemon();
    const onViewItem = vi.fn();
    const { user } = renderWithDaemon(<Host initial="req_plan" onViewItem={onViewItem} />, { daemon: d });
    expect(await screen.findByText("review:req_plan")).toBeInTheDocument();
    d.handle({ method: "POST", url: "/api/requests/req_plan/approve", headers: { authorization: "Bearer mock-token" }, body: {} });
    expect(await screen.findByText("Already resolved.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "View item" }));
    expect(onViewItem).toHaveBeenCalledWith("SPIKE-3");
  });

  it("shows the empty state", async () => {
    const d = createMockDaemon();
    d.db.requests = [];
    renderWithDaemon(<Host />, { daemon: d, events: false });
    await waitFor(() => expect(screen.getAllByText("Nothing needs your attention.").length).toBeGreaterThan(0));
  });
});

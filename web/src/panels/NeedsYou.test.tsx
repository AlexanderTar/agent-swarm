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
    renderWithDaemon(<Host />, { events: false });
    const list = await screen.findByRole("list", { name: "Needs you" });
    const rows = within(list).getAllByRole("button");
    expect(rows[0]).toHaveTextContent("Accept epic");
    expect(rows[0]).toHaveTextContent(/EPIC-12 · \d+[mhd]/);
    expect(rows[3]).toHaveTextContent('Approve "Data model"');
    expect(rows[0]).toHaveAttribute("aria-current", "true");
    expect(screen.getByText("review:req_accept")).toBeInTheDocument();
  });

  it("filters questions and approvals and selects a row", async () => {
    const { user } = renderWithDaemon(<Host />, { events: false });
    await screen.findByRole("list", { name: "Needs you" });
    await user.click(screen.getByRole("radio", { name: "Questions" }));
    expect(within(screen.getByRole("list", { name: "Needs you" })).getAllByRole("button")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: /Which validation library\?/ }));
    expect(screen.getByText("review:req_q2")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "Approvals" }));
    expect(within(screen.getByRole("list", { name: "Needs you" })).getAllByRole("button")).toHaveLength(7);
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

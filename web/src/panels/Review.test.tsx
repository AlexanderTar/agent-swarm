import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { Review } from "./Review";

function setup(id: string, d = createMockDaemon(), connected = true) {
  const request = d.db.requests.find((r) => r.id === id)!;
  return renderWithDaemon(<Review key={request.id} request={request} connected={connected} />, { daemon: d, events: false });
}
const lastPost = (d: ReturnType<typeof createMockDaemon>) => d.calls.filter((c) => c.method === "POST").at(-1);

describe("Review (§16.11)", () => {
  it("shows exactly the section snapshot and approves it with its hash", async () => {
    const d = createMockDaemon();
    const rec = d.db.artifacts.find((a) => a.artifact.id === "art_spec")!;
    rec.revisions[4] = { markdown: "## Data model\n\nCHANGED ON DISK\n", sections: { "data-model": "## Data model\n\nCHANGED ON DISK\n" } };
    rec.artifact.head_revision = 4;
    const { user } = setup("req_section", d);
    expect(screen.getByRole("heading", { name: "Approve section · SPIKE-3 › Offline mode" })).toBeInTheDocument();
    expect(screen.getByText(/^Requested by offline-spike-orchestrator · /)).toBeInTheDocument();
    expect(screen.getByText('Spec revision 3 · Section "Data model"')).toBeInTheDocument();
    expect(await screen.findByText("A local queue of pending messages.")).toBeInTheDocument();
    expect(screen.queryByText("CHANGED ON DISK")).not.toBeInTheDocument();
    expect(screen.queryByText("Work offline.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "View full spec" }));
    expect(await screen.findByText("Work offline.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve section" }));
    await waitFor(() => expect(lastPost(d)).toMatchObject({
      path: "/api/requests/req_section/approve", body: { section_sha256: "sha-dm-3", artifact_revision: 3, via: "board" },
    }));
    expect(d.calls.some((c) => c.path === "/api/artifacts/art_spec?revision=3&section=data-model")).toBe(true);
  });

  it("reloads when the request changed", async () => {
    const d = createMockDaemon();
    d.override("POST /api/requests/req_section/approve", { status: 409, body: { error: { code: "conflict", message: "This request changed. Review the latest version." } } });
    const { user } = setup("req_section", d);
    await screen.findByText("A local queue of pending messages.");
    await user.click(screen.getByRole("button", { name: "Approve section" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This request changed. Review the latest version.");
  });

  it("approves a plan and a report", async () => {
    const d = createMockDaemon();
    const a = setup("req_plan", d);
    expect(screen.getByText("Plan revision 1")).toBeInTheDocument();
    expect(await screen.findByText("Two stories.")).toBeInTheDocument();
    await a.user.click(screen.getByRole("button", { name: "Approve plan" }));
    await waitFor(() => expect(lastPost(d)?.path).toBe("/api/requests/req_plan/approve"));
    a.unmount();
    const b = setup("req_report", d);
    expect(await screen.findByText("A stale token.")).toBeInTheDocument();
    await b.user.click(screen.getByRole("button", { name: "Approve report" }));
    await waitFor(() => expect(lastPost(d)?.path).toBe("/api/requests/req_report/approve"));
  });

  it("shows the accept-epic binding, verification, children and plan, and sends the binding back", async () => {
    const d = createMockDaemon();
    const { user } = setup("req_accept", d);
    expect(screen.getByRole("heading", { name: "Accept epic · EPIC-12 › Authentication" })).toBeInTheDocument();
    expect(screen.getByText("endurio-chat · epic/epic-12-authentication · a1b2c3d")).toBeInTheDocument();
    expect(await screen.findByText("✓ go test ./...")).toBeInTheDocument();
    const children = await screen.findByRole("list", { name: "Children" });
    expect(within(children).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      expect.stringContaining("STORY-40 Login"),
      expect.stringContaining("STORY-41 Password reset"),
    ]);
    await user.click(screen.getByRole("button", { name: "Plan · rev 2 · View" }));
    expect(await screen.findByText("Login and reset.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Close" }));
    await user.click(screen.getByRole("button", { name: "Accept epic" }));
    await waitFor(() => expect(lastPost(d)?.body).toMatchObject({
      binding: { item_revision: 7, integrated_checkpoint: "ckp_int", git: [{ repo: "endurio-chat", sha: "a1b2c3d4e5f6a7b8" }] },
    }));
    expect(screen.getByRole("button", { name: "Request changes" })).toBeInTheDocument();
  });

  it("shows a stale acceptance binding", async () => {
    const d = createMockDaemon();
    const req = d.db.requests.find((r) => r.id === "req_accept")!;
    const { user } = renderWithDaemon(
      <Review request={{ ...req, binding: { item_revision: 6, integrated_checkpoint: "ckp_old", git: [] } }} connected />,
      { daemon: d, events: false },
    );
    await user.click(await screen.findByRole("button", { name: "Accept epic" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This request changed. Review the latest version.");
  });

  it("accepts a fix with its failing verification line", async () => {
    setup("req_fix");
    expect(screen.getByRole("button", { name: "Accept fix" })).toBeInTheDocument();
    expect(await screen.findByText("✗ pnpm test — 1 flaky test")).toBeInTheDocument();
  });

  it("closes a spike", async () => {
    const d = createMockDaemon();
    const { user } = setup("req_close", d);
    expect(screen.getByText("Duplicate of EPIC-12")).toBeInTheDocument();
    expect(await screen.findByText("EPIC-12 already covers the retry banner.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Close spike" }));
    await waitFor(() => expect(lastPost(d)).toMatchObject({ path: "/api/requests/req_close/close-spike", body: { via: "board" } }));
  });

  it("delegates questions and repo confirmation", async () => {
    const a = setup("req_question");
    expect(screen.getByRole("button", { name: "Send answer" })).toBeInTheDocument();
    a.unmount();
    setup("req_repos");
    expect(await screen.findByRole("group", { name: "Proposed" })).toBeInTheDocument();
  });

  it("disables decisions while disconnected", async () => {
    setup("req_plan", createMockDaemon(), false);
    expect(screen.getByRole("button", { name: "Approve plan" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeDisabled();
  });
});

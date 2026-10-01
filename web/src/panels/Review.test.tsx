import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { makeAgent } from "../logic/agentActions";
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
    expect(await screen.findByText("Approved SPIKE-3")).toBeInTheDocument();
  });

  it("reloads when the request changed", async () => {
    const d = createMockDaemon();
    d.override("POST /api/requests/req_section/approve", { status: 409, body: { error: { code: "conflict", message: "This request changed. Review the latest version." } } });
    const { user } = setup("req_section", d);
    await screen.findByText("A local queue of pending messages.");
    await user.click(screen.getByRole("button", { name: "Approve section" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This request changed. Review the latest version.");
    expect(screen.queryByText("Approved SPIKE-3")).not.toBeInTheDocument();
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

  it("shows plan validation warnings above the plan snapshot", async () => {
    const d = createMockDaemon();
    const base = d.handle({ method: "GET", url: "/api/artifacts/art_plan?revision=1", headers: { Authorization: `Bearer ${d.db.token}` } });
    d.override("GET /api/artifacts/art_plan", { status: 200, body: {
      ...(base.body as object), warnings: ["Task t-1 is a single unit. Batch it with related units."],
    } });
    setup("req_plan", d);
    const heading = await screen.findByRole("heading", { name: "Plan warnings" });
    expect(screen.getByText("Task t-1 is a single unit. Batch it with related units.")).toBeInTheDocument();
    expect(heading.compareDocumentPosition(screen.getByText("Two stories.")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("shows the accept-epic binding, verification, children and plan, and sends the binding back", async () => {
    const d = createMockDaemon();
    const { user } = setup("req_accept", d);
    expect(screen.getByRole("heading", { name: "Finish epic · EPIC-12 › Authentication" })).toBeInTheDocument();
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
    expect(screen.getByRole("button", { name: "Create PR" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create PR + auto-merge" }));
    await waitFor(() => expect(lastPost(d)?.body).toMatchObject({
      binding: { item_revision: 7, integrated_checkpoint: "ckp_int", git: [{ repo: "endurio-chat", sha: "a1b2c3d4e5f6a7b8" }] },
      merge: "auto",
    }));
  });

  it("finishes with a PR and no auto-merge", async () => {
    const d = createMockDaemon();
    const { user } = setup("req_accept", d);
    await user.click(screen.getByRole("button", { name: "Create PR" }));
    await waitFor(() => expect(lastPost(d)?.body).toMatchObject({ binding: { item_revision: 7 }, merge: "manual" }));
  });

  it("offers only a local merge when no repo has a GitHub remote", async () => {
    const d = createMockDaemon();
    const req = d.db.requests.find((r) => r.id === "req_accept")!;
    const { user } = renderWithDaemon(<Review request={{ ...req, finish_local: true }} connected />, { daemon: d, events: false });
    expect(screen.queryByRole("button", { name: "Create PR" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Create PR + auto-merge" })).toBeNull();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Merge locally" }));
    await waitFor(() => expect(lastPost(d)?.body).toMatchObject({ binding: { item_revision: 7 }, merge: "local" }));
  });

  it("renders agent finish options as buttons and posts choice, comment and custom merge", async () => {
    const d = createMockDaemon();
    const req = d.db.requests.find((r) => r.id === "req_accept")!;
    const { user } = renderWithDaemon(
      <Review
        request={{ ...req, options: ["Squash-merge PR", "Push straight to main"], option_descriptions: ["Open a PR, squash when green", "Fast-forward main, no PR"] }}
        connected
      />,
      { daemon: d, events: false },
    );
    expect(screen.queryByRole("button", { name: "Create PR" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Create PR + auto-merge" })).toBeNull();
    expect(screen.getByText("Open a PR, squash when green")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Request changes/ })).toBeInTheDocument();
    await user.type(screen.getByLabelText("Comment (optional)"), "ship it");
    await user.click(screen.getByRole("button", { name: /Push straight to main/ }));
    await waitFor(() => expect(lastPost(d)?.body).toMatchObject({
      binding: { item_revision: 7 }, merge: "custom", choice: "Push straight to main", comment: "ship it",
    }));
  });

  it("keeps today's finish buttons when the request has no agent options", async () => {
    const d = createMockDaemon();
    setup("req_accept", d);
    expect(screen.getByRole("button", { name: "Create PR + auto-merge" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create PR" })).toBeInTheDocument();
  });

  it.each([
    ["req_section", "Approve section", { section_sha256: "sha-dm-3", artifact_revision: 3 }],
    ["req_plan", "Approve plan", { artifact_revision: 1 }],
    ["req_report", "Approve report", {}],
  ])("renders agent options on %s as approval buttons and sends choice and comment", async (id, fixedLabel, bound) => {
    const d = createMockDaemon();
    const req = d.db.requests.find((r) => r.id === id)!;
    const { user } = renderWithDaemon(
      <Review request={{ ...req, options: ["Looks good", "Approve with follow-ups"], option_descriptions: ["Ship as written", "Ship, then fix nits"] }} connected />,
      { daemon: d, events: false },
    );
    expect(screen.queryByRole("button", { name: fixedLabel })).toBeNull();
    expect(screen.getByText("Ship, then fix nits")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Comment (optional)"), "nit: rename x");
    await user.click(screen.getByRole("button", { name: /Approve with follow-ups/ }));
    await waitFor(() => expect(lastPost(d)?.body).toMatchObject({ ...bound, choice: "Approve with follow-ups", comment: "nit: rename x" }));
    expect(lastPost(d)?.body).not.toHaveProperty("merge");
  });

  it("offers an optional comment on a plain approval", async () => {
    const d = createMockDaemon();
    const { user } = setup("req_plan", d);
    await user.type(screen.getByLabelText("Comment (optional)"), "thanks");
    await user.click(screen.getByRole("button", { name: "Approve plan" }));
    await waitFor(() => expect(lastPost(d)?.body).toMatchObject({ comment: "thanks" }));
    expect(lastPost(d)?.body).not.toHaveProperty("choice");
  });

  it("shows the waiver and override count for the finish request's tree", async () => {
    const d = createMockDaemon();
    const req = d.db.requests.find((r) => r.id === "req_accept")!;
    const tree = d.db.items.filter((i) => i.root_key === req.root_key);
    tree[0]!.waivers = [{ gate: "verify", reason: "r", agent: "orch", at: 1 }, { gate: "tdd", reason: "r", agent: "orch", at: 1 }];
    tree[1]!.override = { status: "done", reason: "r", agent: "orch", at: 2 };
    setup("req_accept", d);
    expect(await screen.findByText("2 waivers, 1 override in this tree")).toBeInTheDocument();
  });

  it("hides the tree banner when nothing was waived or overridden", async () => {
    setup("req_accept");
    await screen.findByRole("list", { name: "Children" });
    expect(screen.queryByText(/in this tree/)).toBeNull();
  });

  it("shows a stale acceptance binding", async () => {
    const d = createMockDaemon();
    const req = d.db.requests.find((r) => r.id === "req_accept")!;
    const { user } = renderWithDaemon(
      <Review request={{ ...req, binding: { item_revision: 6, integrated_checkpoint: "ckp_old", git: [] } }} connected />,
      { daemon: d, events: false },
    );
    await user.click(await screen.findByRole("button", { name: "Create PR + auto-merge" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This request changed. Review the latest version.");
  });

  it("accepts a fix with its failing verification line", async () => {
    setup("req_fix");
    expect(screen.getByRole("button", { name: "Create PR" })).toBeInTheDocument();
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
    expect(await screen.findByText("Closed SPIKE-4")).toBeInTheDocument();
  });

  it("delegates questions and repo confirmation", async () => {
    const a = setup("req_question");
    expect(screen.queryByRole("button", { name: "Send answer" })).toBeNull();
    expect(screen.getByRole("button", { name: "Open orchestrator terminal" })).toBeInTheDocument();
    a.unmount();
    setup("req_repos");
    expect(await screen.findByRole("group", { name: "Proposed" })).toBeInTheDocument();
  });

  it("does not render Approve for prompt requests", async () => {
    const d = createMockDaemon();
    const promptReq = {
      ...d.db.requests[0]!,
      id: "req_prompt",
      kind: "prompt" as const,
      is_hitl: true,
      prompt: "Trust folder?",
      options: ["Enter"],
      agent_name: "test-agent",
      terminal_agent: "test-agent",
    };
    d.db.requests.push(promptReq);
    renderWithDaemon(<Review request={promptReq} connected={true} />, { daemon: d, events: false });
    expect(screen.getByText("Trust folder?")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(d.calls.some((c) => c.path.includes("/resolve"))).toBe(false);
  });

  it("launches terminal from prompt request and disables buttons when disconnected", async () => {
    const d = createMockDaemon();
    // The prompt row's terminal is the asking agent itself (spec decision 5): give it a live session.
    d.db.agents.push(makeAgent({ name: "test-agent", role: "coder" }));
    const promptReq = {
      ...d.db.requests[0]!,
      id: "req_prompt",
      kind: "prompt" as const,
      is_hitl: true,
      prompt: "Trust folder?",
      options: ["Enter"],
      agent_name: "test-agent",
      terminal_agent: "test-agent",
    };
    d.db.requests.push(promptReq);
    const { user, rerender } = renderWithDaemon(<Review request={promptReq} connected={true} />, { daemon: d, events: false });
    const termBtn = await screen.findByRole("button", { name: "Open orchestrator terminal" });
    await waitFor(() => expect(termBtn).toBeEnabled());
    await user.click(termBtn);
    await waitFor(() => expect(lastPost(d)).toMatchObject({
      path: "/api/agents/test-agent/terminal",
    }));

    rerender(<Review request={promptReq} connected={false} />);
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.getByRole("button", { name: "Open orchestrator terminal" })).toBeDisabled();
  });

  it("disables decisions while disconnected", async () => {
    setup("req_plan", createMockDaemon(), false);
    expect(screen.getByRole("button", { name: "Approve plan" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeDisabled();
  });

  it("shows the daemon's reason, not the raw message, when approve fails for a reason other than a stale binding", async () => {
    const d = createMockDaemon();
    d.override("POST /api/requests/req_plan/approve", {
      status: 500,
      body: { error: { code: "internal", message: "internal error", reason: "The plan artifact is locked for editing." } },
    });
    const { user } = setup("req_plan", d);
    await screen.findByText("Two stories.");
    await user.click(screen.getByRole("button", { name: "Approve plan" }));
    expect(await screen.findByText("The plan artifact is locked for editing.")).toBeInTheDocument();
    expect(screen.queryByText("internal error")).not.toBeInTheDocument();
  });

  it("shows a message and retry when the section snapshot fails to load", async () => {
    const d = createMockDaemon();
    d.override("GET /api/artifacts/art_spec", { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load the spec." } } });
    setup("req_section", d);
    expect(await screen.findByText("Couldn't load the spec.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("shows a message and retry when the item detail fails to load in an acceptance review", async () => {
    const d = createMockDaemon();
    d.override("GET /api/items/EPIC-12", { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load the item." } } });
    setup("req_accept", d);
    expect(await screen.findByText("Couldn't load the item.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("shows a message and retry when checkpoints fail to load in an acceptance review", async () => {
    const d = createMockDaemon();
    d.override("GET /api/items/EPIC-12/checkpoints", { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load checkpoints." } } });
    setup("req_accept", d);
    expect(await screen.findByText("Couldn't load checkpoints.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

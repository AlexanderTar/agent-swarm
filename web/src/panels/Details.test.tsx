import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { toast as sonner } from "sonner";
import { useConnection } from "../data/hooks";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { comboText, pickOption } from "../test/select";
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

function ConnectedDetails() {
  const { connected, retry } = useConnection();
  return <><Details itemKey="TASK-103" connected={connected} onClose={vi.fn()} onSelect={vi.fn()} onReview={vi.fn()} onStartOrchestrator={vi.fn()} /><button onClick={retry}>Reconnect stream</button></>;
}

describe("Details panel (§16.9)", () => {
  it("shows the Progress block only when the detail carries todos", async () => {
    const d = createMockDaemon();
    const real = d.handle({ method: "GET", url: "/api/items/EPIC-12", headers: { authorization: `Bearer ${d.db.token}` } });
    d.override("GET /api/items/EPIC-12", () => ({
      ...real,
      body: { ...(real.body as object), todos: [
        { id: "TASK-101", label: "TASK-101 · Login form", status: "in_progress", item_key: "TASK-101" },
        { id: "integrate", label: "Merging and verifying", status: "pending" },
      ] },
    }));
    const { user, props } = setup("EPIC-12", {}, d);
    expect(await screen.findByRole("heading", { name: "Progress 0/2" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "TASK-101 · Login form" }));
    expect(props.onSelect).toHaveBeenCalledWith("TASK-101");
  });

  describe("Awaiting merge block", () => {
    const todos = [
      { id: "TASK-101", label: "TASK-101 · Login form", status: "completed", item_key: "TASK-101" },
      { id: "integrate", label: "Merging and verifying", status: "completed" },
    ];
    const merge = { repo: "endurio-chat", kind: "pr", url: "https://github.com/o/endurio-chat/pull/9", number: 9, base: "main", head: "epic/epic-12", auto_merge: true, state: "open", checks: "pending" };
    function withDetail(extra: Record<string, unknown>, status = "in_review") {
      const d = createMockDaemon();
      const real = d.handle({ method: "GET", url: "/api/items/EPIC-12", headers: { authorization: `Bearer ${d.db.token}` } });
      const body = real.body as { item: object };
      d.override("GET /api/items/EPIC-12", () => ({ ...real, body: { ...body, item: { ...body.item, status }, todos, ...extra } }));
      return d;
    }

    it("renders above Progress while in review", async () => {
      setup("EPIC-12", {}, withDetail({ merges: [merge] }));
      const heading = await screen.findByRole("heading", { name: "Awaiting merge" });
      expect(heading.compareDocumentPosition(screen.getByRole("heading", { name: "Progress 2/2" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("waits for the orchestrator with no merges yet", async () => {
      setup("EPIC-12", {}, withDetail({ merges: [] }));
      expect(await screen.findByText("Awaiting merge — waiting for the orchestrator to open PRs.")).toBeInTheDocument();
    });

    it("is absent without a merges key", async () => {
      setup("EPIC-12", {}, withDetail({}));
      await screen.findByRole("heading", { name: "Progress 2/2" });
      expect(screen.queryByRole("heading", { name: "Awaiting merge" })).toBeNull();
      expect(screen.queryByText(/waiting for the orchestrator/)).toBeNull();
    });

    it("is absent once done", async () => {
      setup("EPIC-12", {}, withDetail({ merges: [merge] }, "done"));
      await screen.findByRole("heading", { name: "Progress 2/2" });
      expect(screen.queryByRole("heading", { name: "Awaiting merge" })).toBeNull();
    });
  });

  it("has no Progress block without todos", async () => {
    setup("EPIC-12");
    expect(await screen.findByTestId("details-panel")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /^Progress/ })).toBeNull();
  });

  it("stops the loading skeleton animation for reduced motion", async () => {
    const daemon = createMockDaemon();
    const release = daemon.hold("GET /api/items/TASK-103");
    const view = setup("TASK-103", {}, daemon);
    const skeleton = screen.getByLabelText("Item details");
    expect(skeleton.children).toHaveLength(3);
    for (const row of skeleton.children) expect(row).toHaveClass("motion-reduce:animate-none");
    release();
    await screen.findByRole("button", { name: "Password reset form" });
    view.unmount();
  });
  it("uses styled editors and inline actions with focus and disabled affordances", async () => {
    const disconnected = setup("EPIC-12", { connected: false });
    const title = await screen.findByRole("button", { name: "Authentication" });
    expect(title).toHaveAttribute("data-slot", "button");
    expect(title).toHaveClass("focus-visible:ring-ring/50");
    expect(title).toBeDisabled();
    const artifact = screen.getByRole("button", { name: "Spec · rev 3 · View" });
    expect(artifact).toHaveAttribute("data-variant", "link");
    expect(artifact).toHaveClass("h-auto", "p-0");

    disconnected.unmount();
    const connected = setup("TASK-103");
    await connected.user.click(await screen.findByRole("button", { name: "Password reset form" }));
    const input = screen.getByRole("textbox", { name: "Title" });
    expect(input).toHaveAttribute("data-slot", "input");
    expect(input).toHaveClass("focus-visible:ring-ring/50");
    connected.unmount();

    const brief = setup("TASK-103");
    await brief.user.click(await screen.findByRole("button", { name: "Brief" }));
    expect(screen.getByRole("textbox", { name: "Brief" })).toHaveAttribute("data-slot", "textarea");
    expect(screen.getByRole("textbox", { name: "Brief" })).toHaveClass("focus-visible:ring-ring/50");
    brief.unmount();
  });

  it("shows workflow runs from the item detail payload and opens their terminal", async () => {
    const d = createMockDaemon();
    const base = d.handle({ method: "GET", url: "/api/items/TASK-103", headers: { Authorization: `Bearer ${d.db.token}` } }).body as Record<string, unknown>;
    d.override("GET /api/items/TASK-103", { status: 200, body: {
      ...base,
      item: { ...(base.item as object), workflow: { template: "tdd-reviewed", max_rounds: 3, steps: [{ id: "build", run: "coder" }] } },
      workflow_state: { state: "running", round: 2, escalation: "", runs: [
        { step: "build", role: "coder", agent: "builder", state: "active", verdict: "", sha: "", round: 2, findings: [] },
      ] },
      crew: [{ agent: "builder", role: "coder", step: "build", state: "active" }],
    } });
    const { user } = setup("TASK-103", {}, d);
    await user.click(await screen.findByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" }));
    await user.click(screen.getByRole("button", { name: "builder" }));
    await waitFor(() => expect(d.calls).toContainEqual(expect.objectContaining({ method: "POST", path: "/api/agents/builder/terminal" })));
  });
  it.each([{ failed: false, reconnect: false }, { failed: true, reconnect: false }, { failed: false, reconnect: true }, { failed: true, reconnect: true }])
  ("suppresses workflow terminal completion after disconnect (failed: $failed, reconnect: $reconnect)", async ({ failed, reconnect }) => {
    const d = createMockDaemon();
    const base = d.handle({ method: "GET", url: "/api/items/TASK-103", headers: { Authorization: `Bearer ${d.db.token}` } }).body as Record<string, unknown>;
    d.override("GET /api/items/TASK-103", { status: 200, body: {
      ...base,
      item: { ...(base.item as object), workflow: { template: "tdd-reviewed", max_rounds: 3, steps: [{ id: "build", run: "coder" }] } },
      workflow_state: { state: "running", round: 2, escalation: "", runs: [
        { step: "build", role: "coder", agent: "builder", state: "active", verdict: "", sha: "", round: 2, findings: [] },
      ] },
      crew: [{ agent: "builder", role: "coder", step: "build", state: "active" }],
    } });
    const route = "POST /api/agents/builder/terminal";
    if (failed) d.override(route, { status: 409, body: { error: { code: "conflict", message: "Terminal unavailable." } } });
    const release = d.hold(route);
    const success = vi.spyOn(sonner, "success");
    const error = vi.spyOn(sonner, "error");
    const { user } = renderWithDaemon(<ConnectedDetails />, { daemon: d });
    await user.click(await screen.findByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" }));
    await user.click(screen.getByRole("button", { name: "builder" }));
    act(() => d.disconnect());
    await waitFor(() => expect(screen.getByRole("button", { name: "builder" })).toBeDisabled());
    if (reconnect) { d.reconnect(); await user.click(screen.getByRole("button", { name: "Reconnect stream" })); await waitFor(() => expect(screen.getByRole("button", { name: "builder" })).toBeEnabled()); }
    await act(async () => { release(); });
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/agents/builder/terminal")).toBe(true));
    expect(success).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();
    success.mockRestore(); error.mockRestore();
  });

  it("does not open a workflow terminal or toast while disconnected", async () => {
    const d = createMockDaemon();
    const base = d.handle({ method: "GET", url: "/api/items/TASK-103", headers: { Authorization: `Bearer ${d.db.token}` } }).body as Record<string, unknown>;
    d.override("GET /api/items/TASK-103", { status: 200, body: {
      ...base,
      item: { ...(base.item as object), workflow: { template: "tdd-reviewed", max_rounds: 3, steps: [{ id: "build", run: "coder" }] } },
      workflow_state: { state: "running", round: 2, escalation: "", runs: [
        { step: "build", role: "coder", agent: "builder", state: "active", verdict: "", sha: "", round: 2, findings: [] },
      ] },
    } });
    const { user } = setup("TASK-103", { connected: false }, d);
    await user.click(await screen.findByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" }));
    const terminal = screen.getByRole("button", { name: "builder" });
    expect(terminal).toBeDisabled();
    await user.click(terminal);
    expect(d.calls.some((c) => c.path.endsWith("/terminal"))).toBe(false);
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });
  it("shows the breadcrumb, title, status and priority", async () => {
    setup("STORY-40");
    expect(await screen.findByText("EPIC-12 › STORY-40")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Login" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "In progress" })).toHaveAttribute("aria-haspopup", "menu");
    expect(comboText("Priority")).toBe("P2");
    expect(screen.getByText("Story")).toBeInTheDocument();
    const header = screen.getByText("EPIC-12 › STORY-40").parentElement?.parentElement;
    expect(within(header!).getByRole("button", { name: "In progress" })).toBeInTheDocument();
  });

  it("toasts a status conflict without reporting a successful move", async () => {
    const d = createMockDaemon();
    d.override("PATCH /api/items/TASK-103", { status: 409, body: { error: { code: "conflict", message: "stale" } } });
    const { user } = setup("TASK-103", {}, d);
    await user.click(await screen.findByRole("button", { name: "Ready" }));
    await user.click(screen.getByRole("menuitem", { name: /^Blocked/ }));
    await waitFor(() => expect(screen.getAllByText("This item changed elsewhere. Showing its latest status.")).toHaveLength(2));
    await waitFor(() => expect(document.querySelector("[data-sonner-toast]")).toHaveTextContent("This item changed elsewhere. Showing its latest status."));
    expect(screen.queryByText("Moved TASK-103 to Blocked")).not.toBeInTheDocument();
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
    expect(await screen.findByRole("menuitem", { name: /^Done/ })).toHaveAttribute("aria-disabled", "true");
    await user.click(screen.getByRole("menuitem", { name: /^Blocked/ }));
    await waitFor(() => expect(daemon.calls.find((c) => c.method === "PATCH")?.body).toMatchObject({ status: "blocked" }));
    expect(await screen.findByText("Moved TASK-103 to Blocked")).toBeInTheDocument();
    await pickOption(user, "Priority", "P0");
    await waitFor(() => expect(daemon.calls.filter((c) => c.method === "PATCH").at(-1)?.body).toMatchObject({ priority: 0 }));
  });

  it("routes Done on an epic to acceptance", async () => {
    const { user, props } = setup("EPIC-12");
    await user.click(await screen.findByRole("button", { name: "In progress" }));
    await user.click(await screen.findByRole("menuitem", { name: /^Done/ }));
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
    expect(await screen.findByRole("tab", { name: "Agents" })).toHaveAttribute("aria-selected", "true");
    const agents = await screen.findByRole("region", { name: "Agents" });
    expect(within(agents).getByTestId("agent-auth-epic-orchestrator")).toBeInTheDocument();
    expect(within(agents).getByTestId("agent-login-form-coder")).toBeInTheDocument();
    await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalled());
  });

  it.each(["Overview", "Checkpoints", "Deps"] as const)("keeps %s after an agent-focused detail refetch and scrolls only once", async (chosenTab) => {
    const d = createMockDaemon();
    const base = d.handle({ method: "GET", url: "/api/items/EPIC-12", headers: { Authorization: `Bearer ${d.db.token}` } }).body as Record<string, unknown>;
    const { user, props, rerender } = setup("EPIC-12", { focus: "agents" }, d);
    await waitFor(() => expect(screen.getByRole("tab", { name: "Agents" })).toHaveAttribute("aria-selected", "true"));
    await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalledTimes(1));
    rerender(<Details {...props} />);
    expect(Element.prototype.scrollIntoView).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("tab", { name: chosenTab }));
    d.override("GET /api/items/EPIC-12", { status: 200, body: {
      ...base,
      item: { ...(base.item as object), title: "Refetched title" },
    } });
    await pickOption(user, "Priority", "P0");
    await screen.findByRole("button", { name: "Refetched title" });
    expect(screen.getByRole("tab", { name: chosenTab })).toHaveAttribute("aria-selected", "true");
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
    const { user } = setup("TASK-102");
    await user.click(await screen.findByRole("tab", { name: "Deps" }));
    expect(await screen.findByText("TASK-98 (Done)")).toBeInTheDocument();
    expect(screen.getByText("TASK-104")).toBeInTheDocument();
  });

  it("adds a dependency and shows the cycle error", async () => {
    const { user, daemon } = setup("TASK-98");
    await user.click(await screen.findByRole("tab", { name: "Deps" }));
    await user.click(await screen.findByRole("button", { name: "+ Add dependency" }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "validate");
    await user.click(await screen.findByRole("button", { name: "TASK-104 · Validate inputs" }));
    expect(await screen.findByText("This dependency would create a cycle.")).toBeInTheDocument();
    await user.clear(screen.getByRole("searchbox", { name: "Add dependency" }));
    await user.type(screen.getByRole("searchbox", { name: "Add dependency" }), "TASK-110");
    await user.click(await screen.findByRole("button", { name: "TASK-110 · Add crash regression test" }));
    await waitFor(() => expect(daemon.calls.some((c) => c.path === "/api/items/TASK-98/deps" && (c.body as { blocked_by: string }).blocked_by === "TASK-110")).toBe(true));
    expect(await screen.findByText("TASK-98 is now blocked by TASK-110")).toBeInTheDocument();
  });

  it("shows the checkpoints tab", async () => {
    const { user } = setup("TASK-101");
    await screen.findByRole("tab", { name: "Overview" });
    const overview = screen.getByRole("tab", { name: "Overview" });
    expect(document.getElementById(overview.getAttribute("aria-controls")!)).toHaveAttribute("role", "tabpanel");
    await user.click(await screen.findByRole("tab", { name: "Checkpoints" }));
    const checkpoints = screen.getByRole("tab", { name: "Checkpoints" });
    expect(document.getElementById(checkpoints.getAttribute("aria-controls")!)).toHaveAttribute("role", "tabpanel");
    expect(await screen.findByText(/Form renders; wiring submit\./)).toBeInTheDocument();
  });

  it("places four tabs before long content and keeps agents and dependencies in their panels", async () => {
    const d = createMockDaemon();
    const base = d.handle({ method: "GET", url: "/api/items/EPIC-12", headers: { Authorization: `Bearer ${d.db.token}` } }).body as Record<string, unknown>;
    d.override("GET /api/items/EPIC-12", { status: 200, body: {
      ...base,
      item: { ...(base.item as object), workflow: { template: "tdd-reviewed", max_rounds: 3, steps: [{ id: "build", run: "coder" }] } },
      workflow_state: { state: "running", round: 1, escalation: "", runs: [] },
    } });
    const { user } = setup("EPIC-12", {}, d);
    const tabs = await screen.findAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual(["Overview", "Agents", "Checkpoints", "Deps"]);
    const panel = screen.getByTestId("details-panel");
    const overview = screen.getByRole("tabpanel", { name: "Overview" });
    expect(panel.contains(tabs[0]!)).toBe(true);
    expect(tabs[0]!.compareDocumentPosition(overview) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(overview).getByText("Sign-in for the chat app.")).toBeInTheDocument();
    expect(within(overview).getByText("Users can log in")).toBeInTheDocument();
    const brief = within(overview).getByText("Brief");
    const acceptance = within(overview).getByText("Acceptance");
    const workflow = within(overview).getByRole("button", { name: /Workflow/ });
    const agents = within(overview).getByRole("region", { name: "Agents" });
    for (const [first, second] of [[brief, acceptance], [acceptance, workflow], [workflow, agents]] as const) {
      expect(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
    expect(within(agents).getByText("auth-epic-orchestrator")).toBeInTheDocument();
    expect(within(agents).getByText("login-form-coder")).toBeInTheDocument();
    expect(within(agents).getByText("login-review")).toBeInTheDocument();
    expect(within(overview).queryByTestId("agent-auth-epic-orchestrator")).not.toBeInTheDocument();
    await user.click(tabs[1]!);
    expect(within(screen.getByRole("tabpanel", { name: "Agents" })).getByTestId("agent-auth-epic-orchestrator")).toBeInTheDocument();
    await user.click(tabs[3]!);
    expect(within(screen.getByRole("tabpanel", { name: "Deps" })).getByRole("button", { name: "+ Add dependency" })).toBeInTheDocument();
  });

  it("offers Start or View orchestrator on top-level items only", async () => {
    const a = setup("EPIC-20");
    await a.user.click(await screen.findByRole("button", { name: "Start orchestrator" }));
    expect(a.props.onStartOrchestrator).toHaveBeenCalledWith(expect.objectContaining({ key: "EPIC-20" }));
    a.unmount();
    const b = setup("EPIC-12");
    await b.user.click(await screen.findByRole("button", { name: "View orchestrator" }));
    await waitFor(() => expect(screen.getByRole("tab", { name: "Agents" })).toHaveAttribute("aria-selected", "true"));
    await waitFor(() => expect(Element.prototype.scrollIntoView).toHaveBeenCalledTimes(1));
    b.rerender(<Details {...b.props} />);
    expect(Element.prototype.scrollIntoView).toHaveBeenCalledTimes(1);
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
    await user.click(await screen.findByRole("tab", { name: "Deps" }));
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
    await pickOption(user, "Priority", "P0");
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Priority" })).toBeDisabled());
    expect(titleButton).toBeDisabled();
    expect(briefButton).toBeDisabled();
    release();
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Priority" })).toBeEnabled());
  });

  it("disables an open title editor while its save is in flight", async () => {
    const d = createMockDaemon();
    const release = d.hold("PATCH /api/items/TASK-103");
    const { user } = setup("TASK-103", {}, d);
    await user.click(await screen.findByRole("button", { name: "Password reset form" }));
    const input = screen.getByRole("textbox", { name: "Title" });
    await user.clear(input);
    await user.type(input, "Reset form{Enter}");
    await waitFor(() => expect(input).toBeDisabled());
    expect(input).toHaveValue("Reset form");
    release();
    expect(await screen.findByRole("button", { name: "Reset form" })).toBeEnabled();
  });

  it("disables an open brief editor when connection drops", async () => {
    const d = createMockDaemon();
    const opened = setup("TASK-103", {}, d);
    await opened.user.click(await screen.findByRole("button", { name: "Brief" }));
    const textarea = screen.getByRole("textbox", { name: "Brief" });
    await opened.user.type(textarea, " More detail");
    opened.rerender(<Details {...opened.props} connected={false} />);
    expect(textarea).toBeDisabled();
    fireEvent.blur(textarea);
    expect(d.calls.some((call) => call.method === "PATCH")).toBe(false);
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

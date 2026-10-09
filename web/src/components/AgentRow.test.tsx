import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useConnection } from "../data/hooks";
import { makeAgent } from "../logic/agentActions";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import type { SessionState } from "../types";
import { AgentList, AgentRow } from "./AgentRow";

const streamState = vi.hoisted(() => ({ current: "connecting" }));
vi.mock("../sse", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../sse")>();
  return {
    ...actual,
    connectEvents: (options: Parameters<typeof actual.connectEvents>[0]) => actual.connectEvents({
      ...options,
      onState: (state) => { streamState.current = state; options.onState(state); },
    }),
  };
});

const daemon0 = () => createMockDaemon();

const ses = (state: SessionState, tmux_alive = true) => ({
  id: "s", state, attempt: 1, generation: 1, waiting: false, stale: false, tmux_alive, started_at: 0, ended_at: null,
});

function ReconnectRow() {
  const { connected, retry } = useConnection();
  return <><AgentRow agent={makeAgent({ name: "login-form-coder", session: ses("running") })} /><span data-testid="connection">{connected ? "online" : "offline"}</span><button type="button" onClick={retry}>Reconnect stream</button></>;
}

describe("AgentRow (§10.7 on the board)", () => {
  it("shows icon, name, role, state and the row's actions", async () => {
    renderWithDaemon(<AgentRow agent={makeAgent({ name: "login-form-coder", role: "coder", session: ses("running") })} />, { events: false });
    const row = screen.getByTestId("agent-login-form-coder");
    expect(within(row).getByRole("img", { name: "Claude" })).toBeInTheDocument();
    expect(row).toHaveTextContent("login-form-coder · Coder");
    expect(row).toHaveTextContent("Running");
    const buttons = within(row).getAllByRole("button");
    expect(buttons.map((b) => b.getAttribute("aria-label"))).toEqual(["Terminal", "Pause", "Cancel"]);
    // Icon-only: a lucide glyph, the label as tooltip and accessible name, no visible text.
    for (const b of buttons) {
      expect(b.querySelector("svg.lucide")).not.toBeNull();
      expect(b).toHaveAttribute("title", b.getAttribute("aria-label"));
      expect(b.textContent).toBe("");
    }
  });

  it("marks a low-token orchestrator with a leaf after the status label, never a worker", () => {
    const { unmount } = renderWithDaemon(<AgentRow agent={makeAgent({ name: "lt-orch", role: "orchestrator", session: ses("running"), low_token_effective: true })} />, { events: false });
    const row = screen.getByTestId("agent-lt-orch");
    const leaf = within(row).getByRole("img", { name: "Low-token mode is on" });
    expect(leaf.closest("[title]")).toHaveAttribute("title", "Low-token mode is on");
    expect(screen.getByText("Running").compareDocumentPosition(leaf) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    unmount();
    renderWithDaemon(<>
      <AgentRow agent={makeAgent({ name: "lt-worker", role: "coder", session: ses("running"), low_token_effective: true })} />
      <AgentRow agent={makeAgent({ name: "off-orch", role: "orchestrator", session: ses("running"), low_token_effective: false })} />
    </>, { events: false });
    expect(screen.queryByRole("img", { name: "Low-token mode is on" })).toBeNull();
  });

  it("says why the agent isn't on the user's role default", () => {
    const reason = "Role override set on support-chat-attachments-orchestrator-2";
    renderWithDaemon(<AgentRow agent={makeAgent({ name: "r", role: "ui_reviewer", kind_reason: reason, session: ses("running") })} />, { events: false });
    const line = within(screen.getByTestId("agent-r")).getByText(reason);
    expect(line).toHaveAttribute("title", reason);
  });

  it("shows no reason line for an agent on its role default", () => {
    renderWithDaemon(<AgentRow agent={makeAgent({ name: "d", session: ses("running") })} />, { events: false });
    expect(screen.getByTestId("agent-d")).not.toHaveTextContent(/override|out of usage/i);
  });

  it.each([
    ["paused", ses("paused"), ["Resume", "Cancel"]],
    ["interrupted", ses("interrupted"), ["Resume", "Acknowledge", "Cancel"]],
    ["crashed without pane", ses("crashed", false), ["Retry", "Acknowledge"]],
    ["stopping", ses("stopping"), ["Terminal", "Pausing…", "Cancel"]],
  ] as const)("%s", (_n, session, labels) => {
    renderWithDaemon(<AgentRow agent={makeAgent({ name: "x", session })} />, { events: false });
    expect(within(screen.getByTestId("agent-x")).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual(labels);
  });

  it("pauses an orchestrator's group through the daemon", async () => {
    const d = daemon0();
    const { daemon, user } = renderWithDaemon(<AgentRow agent={d.db.agents[0] ?? makeAgent()} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Pause group" }));
    await waitFor(() => expect(daemon.calls.at(-1)).toMatchObject({ method: "POST", path: "/api/agents/auth-epic-orchestrator/pause", body: { scope: "subtree" } }));
    expect(await screen.findByText("Pausing auth-epic-orchestrator and its agents")).toBeInTheDocument();
  });

  it.each([
    ["paused", "resume", "Resume", "Resumed action-agent"],
    ["interrupted", "ack", "Acknowledge", "Acknowledged action-agent"],
    ["crashed", "retry", "Retry", "Retrying action-agent"],
  ] as const)("toasts after %s agent action", async (state, endpoint, action, toast) => {
    const d = daemon0();
    d.override(`POST /api/agents/action-agent/${endpoint}`, { status: 200, body: {} });
    const { user } = renderWithDaemon(<AgentRow agent={makeAgent({ name: "action-agent", session: ses(state, state !== "crashed") })} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: action }));
    expect(await screen.findByText(toast)).toBeInTheDocument();
  });

  it("asks before cancelling an orchestrator with agents", async () => {
    const d = daemon0();
    const { user, daemon } = renderWithDaemon(<AgentRow agent={d.db.agents[0] ?? makeAgent()} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    const dialog = screen.getByRole("alertdialog");
    expect(dialog).toHaveTextContent("Cancel auth-epic-orchestrator and its 2 agents?");
    await user.click(within(dialog).getByRole("button", { name: "Keep running" }));
    expect(daemon.calls.some((c) => c.path.endsWith("/cancel"))).toBe(false);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(daemon.calls.filter((c) => c.path === "/api/agents/auth-epic-orchestrator/cancel")).toHaveLength(1));
    expect(await screen.findByText("Cancelled auth-epic-orchestrator")).toBeInTheDocument();
  });

  it("disables an open cancel confirmation when the daemon disconnects", async () => {
    const d = daemon0();
    const { user } = renderWithDaemon(<AgentRow agent={d.db.agents[0] ?? makeAgent()} />, { daemon: d });
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    const confirm = within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel" });
    d.disconnect();
    await waitFor(() => expect(confirm).toBeDisabled());
    fireEvent.click(confirm);
    fireEvent.keyDown(confirm, { key: "Enter" });
    expect(d.calls.filter((c) => c.path === "/api/agents/auth-epic-orchestrator/cancel")).toHaveLength(0);
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });

  it("toasts a daemon refusal", async () => {
    const d = daemon0();
    d.override("POST /api/agents/login-form-coder/pause", { status: 409, body: { error: { code: "conflict", message: "Still stopping. Try again in a few seconds." } } });
    const { user } = renderWithDaemon(<AgentRow agent={makeAgent({ name: "login-form-coder", session: ses("running") })} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Pause" }));
    expect(await screen.findByText("Still stopping. Try again in a few seconds.")).toBeInTheDocument();
  });

  it.each([
    { failed: false, reconnect: false },
    { failed: true, reconnect: false },
    { failed: false, reconnect: true },
    { failed: true, reconnect: true },
  ])("does not toast when a pending agent action settles after disconnect (failed: $failed, reconnect: $reconnect)", async ({ failed, reconnect }) => {
    const d = daemon0();
    const route = "POST /api/agents/login-form-coder/pause";
    if (failed) d.override(route, { status: 409, body: { error: { code: "conflict", message: "Still stopping. Try again in a few seconds." } } });
    const release = d.hold(route);
    const { user } = renderWithDaemon(<ReconnectRow />, { daemon: d });
    await user.click(screen.getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Pausing…" })).toBeDisabled());
    act(() => d.disconnect());
    await waitFor(() => expect(screen.getByTestId("connection")).toHaveTextContent("offline"));
    if (reconnect) {
      d.reconnect();
      await user.click(screen.getByRole("button", { name: "Reconnect stream" }));
      await waitFor(() => expect(screen.getByTestId("connection")).toHaveTextContent("online"));
    }
    await act(async () => { release(); });
    await waitFor(() => expect(d.calls.filter((c) => `${c.method} ${c.path}` === route)).toHaveLength(1));
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });

  it.each([false, true])("does not toast when a pending response settles before the disconnect render (failed: %s)", async (failed) => {
    const d = daemon0();
    const route = "POST /api/agents/login-form-coder/pause";
    if (failed) d.override(route, { status: 409, body: { error: { code: "conflict", message: "Still stopping." } } });
    const release = d.hold(route);
    let live: ReturnType<typeof useConnection>["live"] | undefined;
    function RowWithLifecycle() {
      const connection = useConnection();
      live = connection.live;
      return <><AgentRow agent={makeAgent({ name: "login-form-coder", session: ses("running") })} /><span data-testid="connection">{connection.connected ? "online" : "offline"}</span></>;
    }
    const { user } = renderWithDaemon(<RowWithLifecycle />, { daemon: d });
    await user.click(screen.getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Pausing…" })).toBeDisabled());
    await act(async () => {
      streamState.current = "open";
      d.disconnect();
      // Wait for the transport callback, then settle the response before React renders offline.
      for (let i = 0; i < 20 && streamState.current !== "closed"; i++) await Promise.resolve();
      expect(streamState.current).toBe("closed");
      expect(screen.getByTestId("connection")).toHaveTextContent("online");
      expect(live?.connected).toBe(false);
      release();
      await Promise.resolve();
      await Promise.resolve();
    });
    await waitFor(() => expect(screen.getByTestId("connection")).toHaveTextContent("offline"));
    expect(d.calls.filter((c) => `${c.method} ${c.path}` === route)).toHaveLength(1);
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });

  // The mock daemon flips its own copy of the agent, not the `agent` prop, so the prop's state stays
  // unchanged after the click: exactly the window where `useMutation.pending` is already false but the
  // refetch has not landed.
  it.each([
    ["pause", "Pause", "Pausing…", "running", "quiescing"],
    ["resume", "Resume", "Resuming…", "paused", "spawning"],
  ] as const)("keeps %s disabled until the agent's state changes", async (_e, label, busy, from, to) => {
    const name = `req-${_e}`;
    const d = daemon0();
    d.override(`POST /api/agents/${name}/${_e}`, { status: 200, body: {} });
    const { user, rerender } = renderWithDaemon(<AgentRow agent={makeAgent({ name, session: ses(from) })} />, { daemon: d, events: false });
    const btn = () => within(screen.getByTestId(`agent-${name}`)).getByRole("button", { name: /^(Pause|Pausing…|Resume|Resuming…)$/ });
    await user.click(screen.getByRole("button", { name: label }));
    await waitFor(() => expect(btn()).toHaveAccessibleName(busy));
    expect(btn()).toBeDisabled();
    // Let the mutation settle (pending false) with the agent unchanged: still disabled.
    await new Promise((r) => setTimeout(r, 20));
    expect(btn()).toBeDisabled();
    expect(btn()).toHaveAccessibleName(busy);
    rerender(<AgentRow agent={makeAgent({ name, session: ses(to) })} />);
    if (_e === "pause") {
      // Daemon's own disabled "Pausing…" takes over.
      expect(btn()).toBeDisabled();
      expect(btn()).toHaveAccessibleName("Pausing…");
    } else {
      expect(within(screen.getByTestId(`agent-${name}`)).queryByRole("button", { name: /Resum/ })).toBeNull();
    }
  });

  it("keeps pause disabled when the display state flips to waiting without the pause landing", async () => {
    const name = "req-flip";
    const d = daemon0();
    d.override(`POST /api/agents/${name}/pause`, { status: 200, body: {} });
    const { user, rerender } = renderWithDaemon(<AgentRow agent={makeAgent({ name, session: ses("running") })} />, { daemon: d, events: false });
    const btn = () => within(screen.getByTestId(`agent-${name}`)).getByRole("button", { name: /^(Pause|Pausing…)$/ });
    await user.click(screen.getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(btn()).toHaveAccessibleName("Pausing…"));
    // running -> waiting is a flag flip, not the pause landing: the raw session state is still "running".
    rerender(<AgentRow agent={makeAgent({ name, session: { ...ses("running"), waiting: true } })} />);
    expect(btn()).toBeDisabled();
    expect(btn()).toHaveAccessibleName("Pausing…");
    // The daemon's own state takes over once the pause is requested.
    rerender(<AgentRow agent={makeAgent({ name, session: ses("pause_requested") })} />);
    expect(btn()).toBeDisabled();
    expect(btn()).toHaveAccessibleName("Pausing…");
  });

  it("re-enables the button and toasts when the pause request fails", async () => {
    const d = daemon0();
    d.override("POST /api/agents/req-fail/pause", { status: 409, body: { error: { code: "conflict", message: "Still stopping. Try again in a few seconds." } } });
    const { user } = renderWithDaemon(<AgentRow agent={makeAgent({ name: "req-fail", session: ses("running") })} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Pause" }));
    expect(await screen.findByText("Still stopping. Try again in a few seconds.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Pause" })).toBeEnabled();
  });

  it("disables actions while disconnected", async () => {
    const d = daemon0();
    renderWithDaemon(<AgentRow agent={makeAgent({ name: "y", session: ses("running") })} />, { daemon: d });
    await waitFor(() => expect(screen.getByRole("button", { name: "Pause" })).toBeEnabled());
    d.disconnect();
    await waitFor(() => expect(screen.getByRole("button", { name: "Pause" })).toBeDisabled());
  });
});

describe("AgentList", () => {
  it("nests children and groups finished agents", async () => {
    const d = daemon0();
    const { user } = renderWithDaemon(<AgentList agents={d.db.agents.slice(0, 1)} />, { daemon: d, events: false });
    expect(screen.getByTestId("agent-login-form-coder")).toBeInTheDocument();
    const finished = screen.getByRole("button", { name: "Finished (1)" });
    expect(finished).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("agent-login-form-coder-1")).not.toBeInTheDocument();
    await user.click(finished);
    expect(finished).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByTestId("agent-login-form-coder-1")).toBeVisible();
    expect(within(screen.getByTestId("agent-login-form-coder-1")).queryAllByRole("button")).toEqual([]);
  });

  it("puts finished top-level agents in their own group", () => {
    renderWithDaemon(
      <AgentList agents={[makeAgent({ name: "live" }), makeAgent({ name: "done-one", state: "finished", session: ses("completed") })]} />,
      { events: false },
    );
    expect(screen.getByText("Finished (1)")).toBeInTheDocument();
  });
});

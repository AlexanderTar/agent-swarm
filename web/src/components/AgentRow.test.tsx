import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { makeAgent } from "../logic/agentActions";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import type { SessionState } from "../types";
import { AgentList, AgentRow } from "./AgentRow";

const daemon0 = () => createMockDaemon();

const ses = (state: SessionState, tmux_alive = true) => ({
  id: "s", state, attempt: 1, generation: 1, waiting: false, stale: false, tmux_alive, started_at: 0, ended_at: null,
});

describe("AgentRow (§10.7 on the board)", () => {
  it("shows icon, name, role, state and the row's actions", async () => {
    renderWithDaemon(<AgentRow agent={makeAgent({ name: "login-form-coder", role: "coder", session: ses("running") })} />, { events: false });
    const row = screen.getByTestId("agent-login-form-coder");
    expect(within(row).getByRole("img", { name: "Claude" })).toBeInTheDocument();
    expect(row).toHaveTextContent("login-form-coder · Coder");
    expect(row).toHaveTextContent("Running");
    expect(within(row).getAllByRole("button").map((b) => b.textContent)).toEqual(["Terminal", "Pause", "Cancel"]);
  });

  it.each([
    ["paused", ses("paused"), ["Resume", "Cancel"]],
    ["interrupted", ses("interrupted"), ["Resume", "Acknowledge", "Cancel"]],
    ["crashed without pane", ses("crashed", false), ["Retry", "Acknowledge"]],
    ["stopping", ses("stopping"), ["Terminal", "Pausing…"]],
  ] as const)("%s", (_n, session, labels) => {
    renderWithDaemon(<AgentRow agent={makeAgent({ name: "x", session })} />, { events: false });
    expect(within(screen.getByTestId("agent-x")).getAllByRole("button").map((b) => b.textContent)).toEqual(labels);
  });

  it("pauses an orchestrator's group through the daemon", async () => {
    const d = daemon0();
    const { daemon, user } = renderWithDaemon(<AgentRow agent={d.db.agents[0] ?? makeAgent()} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Pause group" }));
    await waitFor(() => expect(daemon.calls.at(-1)).toMatchObject({ method: "POST", path: "/api/agents/auth-epic-orchestrator/pause", body: { scope: "subtree" } }));
  });

  it("asks before cancelling an orchestrator with agents", async () => {
    const d = daemon0();
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const { user, daemon } = renderWithDaemon(<AgentRow agent={d.db.agents[0] ?? makeAgent()} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(confirm).toHaveBeenCalledWith("Cancel auth-epic-orchestrator and its 2 agents?");
    expect(daemon.calls.some((c) => c.path.endsWith("/cancel"))).toBe(false);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(daemon.calls.some((c) => c.path === "/api/agents/auth-epic-orchestrator/cancel")).toBe(true));
  });

  it("toasts a daemon refusal", async () => {
    const d = daemon0();
    d.override("POST /api/agents/login-form-coder/pause", { status: 409, body: { error: { code: "conflict", message: "Still stopping. Try again in a few seconds." } } });
    const { user } = renderWithDaemon(<AgentRow agent={makeAgent({ name: "login-form-coder", session: ses("running") })} />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Pause" }));
    expect(await screen.findByText("Still stopping. Try again in a few seconds.")).toBeInTheDocument();
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
    const finished = screen.getByText("Finished (1)");
    expect(screen.getByTestId("agent-login-form-coder-1")).not.toBeVisible();
    await user.click(finished);
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

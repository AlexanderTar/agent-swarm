import { act, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { toast as sonner } from "sonner";
import { useConnection } from "../data/hooks";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { QuestionView } from "./QuestionView";

const question = () => createMockDaemon().db.requests.find((r) => r.id === "req_question")!;

function ConnectedQuestion() {
  const { connected, retry } = useConnection();
  return <><QuestionView request={question()} connected={connected} /><button onClick={retry}>Reconnect stream</button></>;
}

describe("QuestionView (read-only, §16.11)", () => {
  it("shows the prompt and options as text, with no input and no submit button", () => {
    renderWithDaemon(<QuestionView request={question()} connected />, { events: false });
    expect(screen.getByText("Which sync strategy?")).toBeInTheDocument();
    expect(screen.getByText("CRDT")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "CRDT" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send answer" })).not.toBeInTheDocument();
  });

  it("opens the terminal of terminal_agent", async () => {
    const d = createMockDaemon();
    const { user } = renderWithDaemon(<QuestionView request={question()} connected />, { daemon: d, events: false });
    await user.click(await screen.findByRole("button", { name: "Open orchestrator terminal" }));
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/agents/offline-spike-orchestrator/terminal")).toBe(true));
    expect(await screen.findByText("Opening terminal for offline-spike-orchestrator")).toBeInTheDocument();
    expect(d.calls.some((c) => c.path.includes("/answer") || c.path.includes("/resolve"))).toBe(false);
  });

  it.each([{ failed: false, reconnect: false }, { failed: true, reconnect: false }, { failed: false, reconnect: true }, { failed: true, reconnect: true }])
  ("suppresses terminal completion after disconnect (failed: $failed, reconnect: $reconnect)", async ({ failed, reconnect }) => {
    const d = createMockDaemon();
    const route = "POST /api/agents/offline-spike-orchestrator/terminal";
    if (failed) d.override(route, { status: 409, body: { error: { code: "conflict", message: "Terminal unavailable." } } });
    const release = d.hold(route);
    const success = vi.spyOn(sonner, "success");
    const error = vi.spyOn(sonner, "error");
    const { user } = renderWithDaemon(<ConnectedQuestion />, { daemon: d });
    await user.click(await screen.findByRole("button", { name: "Open orchestrator terminal" }));
    act(() => d.disconnect());
    await waitFor(() => expect(screen.getByRole("button", { name: "Open orchestrator terminal" })).toBeDisabled());
    if (reconnect) { d.reconnect(); await user.click(screen.getByRole("button", { name: "Reconnect stream" })); await waitFor(() => expect(screen.getByRole("button", { name: "Open orchestrator terminal" })).toBeEnabled()); }
    await act(async () => { release(); });
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/agents/offline-spike-orchestrator/terminal")).toBe(true));
    expect(success).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();
    success.mockRestore(); error.mockRestore();
  });

  it("is disabled and explains when the orchestrator is paused, or the daemon is disconnected", async () => {
    const paused = { ...question(), terminal_agent: "crash-debug-orchestrator" };
    renderWithDaemon(<QuestionView request={paused} connected />, { events: false });
    expect(await screen.findByText("Orchestrator is paused. Resume it to continue.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open orchestrator terminal" })).toBeDisabled();
  });

  it("disables the terminal button while disconnected", async () => {
    renderWithDaemon(<QuestionView request={question()} connected={false} />, { events: false });
    expect(await screen.findByRole("button", { name: "Open orchestrator terminal" })).toBeDisabled();
  });
});

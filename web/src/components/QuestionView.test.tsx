import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { QuestionView } from "./QuestionView";

const question = () => createMockDaemon().db.requests.find((r) => r.id === "req_question")!;

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
    expect(d.calls.some((c) => c.path.includes("/answer") || c.path.includes("/resolve"))).toBe(false);
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

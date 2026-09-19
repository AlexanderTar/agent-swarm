import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { QuestionView } from "./QuestionView";

const question = () => createMockDaemon().db.requests.find((r) => r.id === "req_question")!;

describe("QuestionView (§16.11)", () => {
  it("answers with an option", async () => {
    const d = createMockDaemon();
    const { user } = renderWithDaemon(<QuestionView request={question()} connected />, { daemon: d, events: false });
    expect(screen.getByText("Which sync strategy?")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "CRDT" }));
    await waitFor(() => expect(d.calls.at(-1)).toMatchObject({ path: "/api/requests/req_question/answer", body: { text: "CRDT", via: "board" } }));
  });

  it("answers with typed text, keeps the draft and opens the terminal", async () => {
    const d = createMockDaemon();
    const first = renderWithDaemon(<QuestionView request={question()} connected />, { daemon: d, events: false });
    await first.user.type(screen.getByRole("textbox", { name: "Answer" }), "Use last write wins");
    first.unmount();
    const { user } = renderWithDaemon(<QuestionView request={question()} connected />, { daemon: d, events: false });
    expect(screen.getByRole("textbox", { name: "Answer" })).toHaveValue("Use last write wins");
    await user.click(screen.getByRole("button", { name: "Open terminal" }));
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/agents/offline-spike-orchestrator/terminal")).toBe(true));
    await user.click(screen.getByRole("button", { name: "Send answer" }));
    await waitFor(() => expect(d.calls.at(-1)?.body).toMatchObject({ text: "Use last write wins" }));
    expect(sessionStorage.getItem("swarm.draft.req_question.answer")).toBeNull();
  });

  it("disables sending while disconnected or empty", () => {
    renderWithDaemon(<QuestionView request={question()} connected={false} />, { events: false });
    expect(screen.getByRole("button", { name: "Send answer" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "CRDT" })).toBeDisabled();
  });
});

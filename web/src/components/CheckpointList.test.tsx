import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWithDaemon } from "../test/render";
import { CheckpointList } from "./CheckpointList";

describe("CheckpointList (§16.9)", () => {
  it("lists checkpoints and advice newest first with expandable details", async () => {
    const { user } = renderWithDaemon(<CheckpointList itemKey="TASK-101" agentNames={["login-form-coder"]} />, { events: false });
    const rows = await screen.findAllByRole("group");
    expect(rows.map((r) => r.querySelector("summary")?.textContent)).toEqual([
      expect.stringContaining("Form renders; wiring submit."),
      expect.stringContaining("Advice · claude/claude-fable-5-1 · 9s · 12.3k · $0.04"),
      expect.stringContaining("Starting the login form."),
    ]);
    expect(screen.getByText("Advisor · 12.3k tokens · $0.04")).toBeInTheDocument();
    await user.click(screen.getByText(/Form renders/));
    expect(screen.getByText("Wire submit")).toBeVisible();
    expect(screen.getByText("Waiting on API shape")).toBeVisible();
    expect(screen.getByText("endurio-chat task/task-101-login-form 9f8e7d6 dirty")).toBeVisible();
    expect(screen.getByText("✗ pnpm test login — expected failure")).toBeVisible();
    await user.click(screen.getByText(/Advice · claude/));
    expect(screen.getByText("Is a form library worth it?")).toBeVisible();
    expect(screen.getByText("No. Two fields don't need one.")).toBeVisible();
  });

  it("marks daemon-written checkpoints", async () => {
    renderWithDaemon(<CheckpointList itemKey="BUG-7" agentNames={[]} />, { events: false });
    expect(await screen.findByText("Written by Swarm")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    renderWithDaemon(<CheckpointList itemKey="TASK-103" agentNames={[]} />, { events: false });
    expect(await screen.findByText("No checkpoints yet.")).toBeInTheDocument();
  });
});

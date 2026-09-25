import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "../types";
import { WorkflowSection } from "./WorkflowSection";

const workflow = { template: "tdd-reviewed", steps: [{ id: "build", run: "coder" }, { id: "review", review: ["reviewer"], of: "build" }], max_rounds: 3 };
const state: NonNullable<ItemDetail["workflow_state"]> = {
  state: "running", round: 2, escalation: "", runs: [
    { step: "build", role: "coder", agent: "builder", state: "completed", verdict: "", sha: "abc", round: 2, findings: [] },
    { step: "review", role: "reviewer", agent: "critic", state: "completed", verdict: "changes_requested", sha: "abc", round: 2, findings: [
      { severity: "major", file: "internal/x.go", line: 42, unit: 3, summary: "Handle nil" },
    ] },
  ],
};

describe("WorkflowSection", () => {
  it("shows steps, run verdicts and unit-tagged findings", async () => {
    render(<WorkflowSection workflow={workflow} state={state} onOpenTerminal={vi.fn()} />);
    expect(screen.getByText("Workflow · tdd-reviewed · Running · Round 2 of 3")).toBeInTheDocument();
    expect(screen.getByText("build")).toBeInTheDocument();
    expect(screen.getByText("review")).toBeInTheDocument();
    expect(screen.getByText("Changes requested")).toBeInTheDocument();
    const disclosure = screen.getByText("1 findings");
    await userEvent.click(disclosure);
    expect(screen.getByText("[major] internal/x.go:42 — Handle nil [unit 3]")).toBeInTheDocument();
  });

  it("opens the run agent's terminal", async () => {
    const onOpenTerminal = vi.fn();
    render(<WorkflowSection workflow={workflow} state={state} onOpenTerminal={onOpenTerminal} />);
    await userEvent.click(screen.getByRole("button", { name: "critic" }));
    expect(onOpenTerminal).toHaveBeenCalledWith("critic");
  });

  it("shows the escalation reason and next action", () => {
    render(<WorkflowSection workflow={workflow} state={{ ...state, state: "escalated", escalation: "Review blocked" }} onOpenTerminal={vi.fn()} />);
    const alert = screen.getByRole("alert");
    expect(within(alert).getByText("Review blocked")).toBeInTheDocument();
    expect(within(alert).getByText("The orchestrator decides next.")).toBeInTheDocument();
  });

  it("is hidden for legacy tasks", () => {
    const { container } = render(<WorkflowSection workflow={null} state={undefined} onOpenTerminal={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });
});

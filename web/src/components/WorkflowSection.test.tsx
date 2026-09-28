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
  it("shows a chevron that rotates with the workflow disclosure state", async () => {
    render(<WorkflowSection workflow={workflow} state={state} connected onOpenTerminal={vi.fn()} />);
    const trigger = screen.getByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" });
    const chevron = trigger.querySelector("svg.lucide-chevron-right");
    expect(chevron).toBeInTheDocument();
    expect(chevron).toHaveClass("group-data-[state=open]:rotate-90");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
  });

  it("shows steps, run verdicts and unit-tagged findings", async () => {
    render(<WorkflowSection workflow={workflow} state={state} connected onOpenTerminal={vi.fn()} />);
    const workflowToggle = screen.getByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" });
    expect(workflowToggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("build")).not.toBeInTheDocument();
    await userEvent.click(workflowToggle);
    expect(workflowToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("build")).toBeInTheDocument();
    expect(screen.getByText("review")).toBeInTheDocument();
    expect(screen.getByText("Changes requested")).toBeInTheDocument();
    const disclosure = screen.getByRole("button", { name: "1 findings" });
    expect(disclosure).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(disclosure);
    expect(disclosure).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("[major] internal/x.go:42 — Handle nil [unit 3]")).toBeInTheDocument();
  });

  it("opens the run agent's terminal", async () => {
    const onOpenTerminal = vi.fn();
    render(<WorkflowSection workflow={workflow} state={state} connected onOpenTerminal={onOpenTerminal} />);
    await userEvent.click(screen.getByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" }));
    await userEvent.click(screen.getByRole("button", { name: "critic" }));
    expect(onOpenTerminal).toHaveBeenCalledWith("critic");
  });

  it("disables run terminals while disconnected", async () => {
    const onOpenTerminal = vi.fn();
    render(<WorkflowSection workflow={workflow} state={state} connected={false} onOpenTerminal={onOpenTerminal} />);
    await userEvent.click(screen.getByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" }));
    const terminal = screen.getByRole("button", { name: "critic" });
    expect(terminal).toBeDisabled();
    await userEvent.click(terminal);
    expect(onOpenTerminal).not.toHaveBeenCalled();
  });

  it("allows the workflow label to wrap in a narrow sheet", () => {
    render(<div style={{ width: 320 }}><WorkflowSection workflow={workflow} state={state} connected onOpenTerminal={vi.fn()} /></div>);
    const trigger = screen.getByRole("button", { name: "Workflow · tdd-reviewed · Running · Round 2 of 3" });
    expect(trigger).toHaveClass("whitespace-normal", "min-w-0", "break-words");
  });

  it("shows the escalation reason and next action", async () => {
    render(<WorkflowSection workflow={workflow} state={{ ...state, state: "escalated", escalation: "Review blocked" }} connected onOpenTerminal={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Workflow · tdd-reviewed · Escalated · Round 2 of 3" }));
    const alert = screen.getByRole("alert");
    expect(within(alert).getByText("Review blocked")).toBeInTheDocument();
    expect(within(alert).getByText("The orchestrator decides next.")).toBeInTheDocument();
  });

  it("is hidden for legacy tasks", () => {
    const { container } = render(<WorkflowSection workflow={null} state={undefined} connected onOpenTerminal={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });
});

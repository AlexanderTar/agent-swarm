import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { AgentIcon, Key, TypeIcon } from "./icons";
import { MoveToMenu } from "./MoveToMenu";
import { Segmented } from "./Segmented";
import { Sheet } from "./Sheet";
import { StateDot, StatusPill } from "./StatusLabel";

describe("icons and labels", () => {
  it("labels agent and type icons", () => {
    render(<><AgentIcon kind="codex" /><TypeIcon type="spike" /><Key>TASK-1</Key></>);
    expect(screen.getByRole("img", { name: "Codex" })).toBeInTheDocument();
    expect(screen.getByLabelText("Spike")).toBeInTheDocument();
    expect(screen.getByText("TASK-1")).toHaveClass("key");
  });

  it("always shows status text and state labels", () => {
    render(<><StatusPill status="awaiting_approval" /><StateDot state="quiescing" withLabel /><StateDot state="running" /></>);
    expect(screen.getByText("Awaiting approval")).toBeInTheDocument();
    expect(screen.getByText("Finishing current step")).toBeInTheDocument();
    expect(screen.getByLabelText("Running")).toBeInTheDocument();
  });
});

describe("Sheet", () => {
  it("closes with the button and Escape", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<Sheet title="Start orchestrator" subtitle="EPIC-12 · Authentication" onClose={onClose} footer={<button type="button">ok</button>}>body</Sheet>);
    expect(screen.getByRole("dialog", { name: "Start orchestrator" })).toHaveStyle({ width: "420px" });
    expect(screen.getByText("EPIC-12 · Authentication")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Close" }));
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});

describe("Segmented", () => {
  it("selects an option", async () => {
    const user = userEvent.setup();
    function Host() {
      const [v, setV] = useState<"root" | "neighbourhood">("root");
      return (
        <Segmented<"root" | "neighbourhood">
          label="Scope"
          value={v}
          onChange={setV}
          options={[{ value: "root", label: "Root" }, { value: "neighbourhood", label: "Neighbourhood" }]}
        />
      );
    }
    render(<Host />);
    expect(screen.getByRole("radio", { name: "Root" })).toHaveAttribute("aria-checked", "true");
    await user.click(screen.getByRole("radio", { name: "Neighbourhood" }));
    expect(screen.getByRole("radio", { name: "Neighbourhood" })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("radiogroup", { name: "Scope" })).toBeInTheDocument();
  });
});

describe("MoveToMenu", () => {
  const story = { key: "STORY-40", type: "story" as const, status: "in_review" as const, status_before_block: null };
  const epic = { key: "EPIC-12", type: "epic" as const, status: "in_review" as const, status_before_block: null };

  it("lists every other status with disabled reasons", async () => {
    const user = userEvent.setup();
    render(<MoveToMenu item={story} onMove={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Move to…" }));
    const done = screen.getByRole("menuitem", { name: /Done/ });
    expect(done).toBeDisabled();
    expect(done).toHaveTextContent("Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first.");
    expect(screen.getByRole("menuitem", { name: /Blocked/ })).toBeEnabled();
    expect(screen.queryByRole("menuitem", { name: /^In review/ })).not.toBeInTheDocument();
  });

  it("works from the keyboard and passes special checks through", async () => {
    const user = userEvent.setup();
    const onMove = vi.fn();
    render(<MoveToMenu item={epic} onMove={onMove} buttonLabel="In review" />);
    screen.getByRole("button", { name: "In review" }).focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await user.keyboard("{End}{ArrowUp}{Enter}");
    expect(onMove).toHaveBeenCalledWith("done", { ok: false, reason: "Accept this epic to mark it Done.", special: "accept" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("closes on Escape and can be disabled", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<MoveToMenu item={story} onMove={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Move to…" }));
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    rerender(<MoveToMenu item={story} onMove={vi.fn()} disabled />);
    expect(screen.getByRole("button", { name: "Move to…" })).toBeDisabled();
  });
});

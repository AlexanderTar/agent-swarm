import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { AgentIcon, Key, TypeIcon } from "./icons";
import { ConnectionBanner } from "./Banners";
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

  it("keeps the in-progress border visible on the dark palette", () => {
    render(<StatusPill status="in_progress" />);
    expect(screen.getByText("In progress")).toHaveClass("border-info/30");
  });

  it("renders status as a badge with a tone", () => {
    render(<StatusPill status="awaiting_approval" />);
    expect(screen.getByText("Awaiting approval")).toHaveAttribute("data-slot", "badge");
    expect(screen.getByText("Awaiting approval")).toHaveAttribute("data-tone", "warning");
  });

  it("renders a retryable connection alert", async () => {
    const onRetry = vi.fn();
    const user = userEvent.setup();
    render(<ConnectionBanner onRetry={onRetry} />);
    expect(screen.getByRole("alert")).toHaveAttribute("data-slot", "alert");
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("keeps muted state dots visible on the dark palette", () => {
    const { container } = render(<><StateDot state="queued" /><StateDot state="spawning" /><StateDot state="capacity_paused" /></>);
    const dots = container.querySelectorAll(".size-2");
    expect(dots[0]).toHaveClass("bg-muted-foreground");
    expect(dots[1]).toHaveClass("bg-muted-foreground", "animate-pulse", "motion-reduce:animate-none");
    expect(dots[2]).toHaveClass("border-muted-foreground");
  });
});

describe("Sheet", () => {
  it("closes with the button and Escape", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<Sheet title="Start orchestrator" subtitle="EPIC-12 · Authentication" onClose={onClose} footer={<button type="button">ok</button>}>body</Sheet>);
    expect(document.querySelector("[data-slot=sheet-overlay]")).toHaveClass("bg-black/60");
    expect(screen.getByRole("dialog", { name: "Start orchestrator" })).toHaveStyle({ "--sheet-width": "420px" });
    expect(screen.getByText("EPIC-12 · Authentication")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Close" }));
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("moves focus into itself on open and restores it to the trigger on close", async () => {
    const user = userEvent.setup();
    function Host() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>Open sheet</button>
          {open && (
            <Sheet title="Start orchestrator" onClose={() => setOpen(false)} footer={<button type="button">Go</button>}>
              body
            </Sheet>
          )}
        </>
      );
    }
    render(<Host />);
    const trigger = screen.getByRole("button", { name: "Open sheet" });
    trigger.focus();
    await user.click(trigger);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    // Focus must land inside the sheet, not stay behind it or fall to <body>.
    expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("keeps an accessible title without a visible header", () => {
    render(<Sheet title="Details" header={false} onClose={vi.fn()}>body</Sheet>);
    expect(screen.getByRole("dialog", { name: "Details" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Details" })).toHaveClass("sr-only");
  });

  it("associates a visible subtitle with the dialog", () => {
    render(<Sheet title="A long sheet title that could reach the close button" subtitle="Helpful context" onClose={vi.fn()}>body</Sheet>);
    const dialog = screen.getByRole("dialog", { name: /A long sheet title/ });
    expect(dialog).toHaveAccessibleDescription("Helpful context");
    expect(dialog.querySelector("[data-slot=sheet-header]")).toHaveClass("pr-16");
  });

  it("omits a description and clears the close button for headerless content", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    render(<Sheet title="Details" header={false} onClose={vi.fn()}>body</Sheet>);
    const dialog = screen.getByRole("dialog", { name: "Details" });
    expect(dialog).not.toHaveAttribute("aria-describedby");
    expect(screen.getByText("body")).toHaveClass("pt-16");
    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });

  it("keeps a headerless subtitle available as the dialog description", () => {
    render(<Sheet title="Details" subtitle="TASK-1 context" header={false} onClose={vi.fn()}>body</Sheet>);
    const dialog = screen.getByRole("dialog", { name: "Details" });
    expect(dialog).toHaveAccessibleDescription("TASK-1 context");
    expect(screen.getByText("TASK-1 context")).toHaveClass("sr-only");
  });

  it("fills narrow screens and applies configured widths from 640px up", () => {
    const { rerender } = render(<Sheet title="New item" width={480} onClose={vi.fn()}>body</Sheet>);
    const dialog = screen.getByRole("dialog", { name: "New item" });
    expect(dialog).toHaveClass("w-full", "sm:w-[var(--sheet-width)]", "max-w-full", "sm:max-w-full");
    expect(dialog.style.width).toBe("");
    expect(dialog).toHaveStyle({ "--sheet-width": "480px" });

    rerender(<Sheet title="Details" header={false} onClose={vi.fn()}>body</Sheet>);
    expect(screen.getByRole("dialog", { name: "Details" })).toHaveStyle({ "--sheet-width": "420px" });
  });

  it("gives Close a 44px target and disables sheet animations for reduced motion", () => {
    render(<Sheet title="New item" onClose={vi.fn()}>body</Sheet>);
    expect(screen.getByRole("button", { name: "Close" })).toHaveClass("size-11");
    expect(screen.getByRole("dialog", { name: "New item" })).toHaveClass("motion-reduce:animate-none");
    expect(document.querySelector("[data-slot=sheet-overlay]")).toHaveClass("motion-reduce:animate-none");
  });

  it("non-modal sheet has no overlay and ignores outside clicks", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<><button type="button">outside</button><Sheet title="Details" modal={false} onClose={onClose}>body</Sheet></>);
    expect(document.querySelector("[data-slot=sheet-overlay]")).toBeNull();
    await user.click(screen.getByRole("button", { name: "outside" }));
    expect(onClose).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("Segmented", () => {
  it("selects an option", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    function Host() {
      const [v, setV] = useState<"root" | "neighbourhood">("root");
      return (
        <Segmented<"root" | "neighbourhood">
          label="Scope"
          value={v}
          onChange={(value) => { onChange(value); setV(value); }}
          options={[{ value: "root", label: "Root" }, { value: "neighbourhood", label: "Neighbourhood" }]}
        />
      );
    }
    render(<Host />);
    expect(screen.getByRole("radio", { name: "Root" })).toHaveAttribute("aria-checked", "true");
    await user.click(screen.getByRole("radio", { name: "Neighbourhood" }));
    expect(screen.getByRole("radio", { name: "Neighbourhood" })).toHaveAttribute("aria-checked", "true");
    await user.click(screen.getByRole("radio", { name: "Neighbourhood" }));
    expect(screen.getByRole("radio", { name: "Neighbourhood" })).toHaveAttribute("aria-checked", "true");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("radiogroup", { name: "Scope" })).toHaveAttribute("data-slot", "toggle-group");
  });
});

describe("MoveToMenu", () => {
  const story = { key: "STORY-40", type: "story" as const, status: "in_review" as const, status_before_block: null };
  const epic = { key: "EPIC-12", type: "epic" as const, status: "in_review" as const, status_before_block: null };

  it("lists every other status with disabled reasons", async () => {
    const user = userEvent.setup();
    render(<MoveToMenu item={story} onMove={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Move to…" }));
    const done = await screen.findByRole("menuitem", { name: /Done/ });
    expect(done).toHaveAttribute("aria-disabled", "true");
    expect(done).toHaveAttribute("data-slot", "dropdown-menu-item");
    expect(done).toHaveTextContent("Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first.");
    expect(screen.getByRole("menuitem", { name: /Blocked/ })).not.toHaveAttribute("aria-disabled", "true");
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
    // A keyboard user who picked an entry must land back on the trigger, not <body>.
    expect(screen.getByRole("button", { name: "In review" })).toHaveFocus();
  });

  it("closes on Escape and can be disabled", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<MoveToMenu item={story} onMove={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Move to…" }));
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move to…" })).toHaveFocus();
    rerender(<MoveToMenu item={story} onMove={vi.fn()} disabled />);
    expect(screen.getByRole("button", { name: "Move to…" })).toBeDisabled();
  });
});

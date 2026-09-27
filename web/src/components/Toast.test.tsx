import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ToastProvider, useToast } from "./Toast";

function Trigger({ onAction }: { onAction: () => void }) {
  const toast = useToast();
  return (
    <>
      <button type="button" onClick={() => toast({ message: "This spike reaches Done after materialization.", action: { label: "View spike", onClick: onAction } })}>legacy action</button>
      <button type="button" onClick={() => toast({ message: "Failed to move item" })}>legacy</button>
      <button type="button" onClick={() => toast.success("Created TASK-9")}>ok</button>
    </>
  );
}

describe("Toast (Sonner)", () => {
  it("legacy call shows an error toast with its action", async () => {
    const onAction = vi.fn();
    const user = userEvent.setup();
    render(<ToastProvider><Trigger onAction={onAction} /></ToastProvider>);
    await user.click(screen.getByRole("button", { name: "legacy action" }));
    expect(await screen.findByText("This spike reaches Done after materialization.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "View spike" }));
    expect(onAction).toHaveBeenCalled();
  });

  it("legacy error dismisses after 6 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<ToastProvider><Trigger onAction={vi.fn()} /></ToastProvider>);
    await user.click(screen.getByRole("button", { name: /^legacy$/ }));
    expect(await screen.findByText("Failed to move item")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(6_600));
    expect(screen.queryByText("Failed to move item")).not.toBeInTheDocument();
  });

  it("success toast disappears after 4 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<ToastProvider><Trigger onAction={vi.fn()} /></ToastProvider>);
    await user.click(screen.getByRole("button", { name: "ok" }));
    expect(await screen.findByText("Created TASK-9")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(4_600));
    expect(screen.queryByText("Created TASK-9")).not.toBeInTheDocument();
  });
});

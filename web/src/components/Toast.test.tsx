import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ToastProvider, useToast } from "./Toast";

function Trigger({ onAction }: { onAction: () => void }) {
  const toast = useToast();
  return (
    <button type="button" onClick={() => toast({ message: "This spike reaches Done after materialization.", action: { label: "View spike", onClick: onAction } })}>
      go
    </button>
  );
}

describe("Toast", () => {
  it("shows a message with an action and dismisses it after 6 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onAction = vi.fn();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<ToastProvider><Trigger onAction={onAction} /></ToastProvider>);
    await user.click(screen.getByRole("button", { name: "go" }));
    expect(screen.getByRole("status")).toHaveTextContent("This spike reaches Done after materialization.");
    await user.click(screen.getByRole("button", { name: "View spike" }));
    expect(onAction).toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(6_100));
    expect(screen.queryByText("This spike reaches Done after materialization.")).not.toBeInTheDocument();
  });
});

import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { toast as sonner } from "sonner";
import { createApi } from "../api";
import { DataProvider, useConnection } from "../data/hooks";
import { ToastProvider, useToast } from "./Toast";

function Trigger({ onAction }: { onAction: () => void }) {
  const toast = useToast();
  return (
    <>
      <button type="button" onClick={() => toast({ message: "This spike reaches Done after materialization.", action: { label: "View spike", onClick: onAction } })}>legacy action</button>
      <button type="button" onClick={() => toast({ message: "Failed to move item" })}>legacy</button>
      <button type="button" onClick={() => toast.success("Created TASK-9")}>ok</button>
      <button type="button" onClick={() => toast.error("Could not start", "Try again later")}>error helper</button>
    </>
  );
}

const renderToast = (ui: ReactNode) => render(<DataProvider api={createApi()} events={false}><ToastProvider>{ui}</ToastProvider></DataProvider>);

describe("Toast (Sonner)", () => {
  it.each([{ failure: false, reconnect: false }, { failure: true, reconnect: false }, { failure: false, reconnect: true }, { failure: true, reconnect: true }])
  ("drops a held completion after disconnect (failure: $failure, reconnect: $reconnect)", async ({ failure, reconnect }) => {
    const success = vi.spyOn(sonner, "success");
    const error = vi.spyOn(sonner, "error");
    let live: ReturnType<typeof useConnection>["live"] | undefined;
    let release = () => {};
    const held = new Promise<void>((resolve) => { release = resolve; });
    function PendingToast() {
      const toast = useToast();
      live = useConnection().live;
      return <button onClick={() => { void held.then(() => failure ? toast.error("Failed") : toast.success("Done")); }}>Start</button>;
    }
    const user = userEvent.setup();
    render(<DataProvider api={createApi()} events={false}><ToastProvider><PendingToast /></ToastProvider></DataProvider>);
    await user.click(screen.getByRole("button", { name: "Start" }));
    live!.connected = false;
    live!.epoch++;
    if (reconnect) live!.connected = true;
    await act(async () => { release(); });
    expect(success).not.toHaveBeenCalled();
    expect(error).not.toHaveBeenCalled();
    success.mockRestore(); error.mockRestore();
  });

  it("drops a toast from an offline click before React rerenders", async () => {
    let live: ReturnType<typeof useConnection>["live"] | undefined;
    function OfflineToast() {
      const toast = useToast();
      live = useConnection().live;
      return <button onClick={() => toast({ message: "Offline refusal" })}>Refuse</button>;
    }
    const user = userEvent.setup();
    render(<DataProvider api={createApi()} events={false}><ToastProvider><OfflineToast /></ToastProvider></DataProvider>);
    live!.connected = false;
    await user.click(screen.getByRole("button", { name: "Refuse" }));
    expect(screen.queryByText("Offline refusal")).not.toBeInTheDocument();
  });

  it("drops a held action started during an offline render when transport reconnects", async () => {
    const success = vi.spyOn(sonner, "success");
    let live: ReturnType<typeof useConnection>["live"] | undefined;
    let release = () => {};
    const held = new Promise<void>((resolve) => { release = resolve; });
    function PendingToast() {
      const toast = useToast();
      live = useConnection().live;
      return <button onClick={() => { void held.then(() => toast.success("Stale completion")); }}>Start</button>;
    }
    const api = createApi();
    const ui = () => <DataProvider api={api} events={false}><ToastProvider><PendingToast /></ToastProvider></DataProvider>;
    const view = render(ui());
    live!.connected = false;
    live!.epoch++;
    view.rerender(ui());
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    live!.connected = true;
    await act(async () => { release(); });
    expect(success).not.toHaveBeenCalled();
    success.mockRestore();
  });

  it("legacy call shows an error toast with its action", async () => {
    const onAction = vi.fn();
    const user = userEvent.setup();
    renderToast(<Trigger onAction={onAction} />);
    await user.click(screen.getByRole("button", { name: "legacy action" }));
    expect(await screen.findByText("This spike reaches Done after materialization.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "View spike" }));
    expect(onAction).toHaveBeenCalled();
  });

  it("legacy error dismisses after 6 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderToast(<Trigger onAction={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: /^legacy$/ }));
    expect(await screen.findByText("Failed to move item")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(6_600));
    expect(screen.queryByText("Failed to move item")).not.toBeInTheDocument();
  });

  it("success toast disappears after 4 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderToast(<Trigger onAction={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "ok" }));
    expect(await screen.findByText("Created TASK-9")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(4_600));
    expect(screen.queryByText("Created TASK-9")).not.toBeInTheDocument();
  });

  it("error helper shows its description for 6 s", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderToast(<Trigger onAction={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "error helper" }));
    expect(await screen.findByText("Try again later")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(4_600));
    expect(screen.getByText("Could not start")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(2_000));
    expect(screen.queryByText("Could not start")).not.toBeInTheDocument();
  });
});

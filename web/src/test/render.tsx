import { fireEvent, type RenderResult, render } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactElement } from "react";
import { vi } from "vitest";
import { createApi } from "../api";
import { ToastProvider } from "../components/Toast";
import { DataProvider } from "../data/hooks";
import { type MockDaemon, createMockDaemon } from "../mock/daemon";
import { mockFetch } from "../mock/fetch";

// F20: the one backoff schedule every test that spins up a real SSE connection needs (short enough
// that a test never actually waits it out); shared here instead of redefined per test file.
export const TEST_BACKOFF: readonly number[] = [60_000];

export function renderWithDaemon(
  ui: ReactElement,
  o: { daemon?: MockDaemon; hash?: string; events?: boolean } = {},
): RenderResult & { daemon: MockDaemon; user: UserEvent } {
  const daemon = o.daemon ?? createMockDaemon();
  vi.stubGlobal("fetch", mockFetch(daemon));
  if (o.hash) window.history.replaceState(null, "", `/${o.hash}`);
  const api = createApi();
  const user = userEvent.setup();
  // Wrap via `wrapper` rather than pre-building the whole tree, so the RenderResult's own
  // `rerender(ui)` swaps only the given element and keeps DataProvider/ToastProvider mounted —
  // needed by tests that rerender a Host with different props on the same component instance
  // (e.g. F16's loaded=false → loaded=true transition).
  const result = render(ui, {
    wrapper: ({ children }) => (
      <DataProvider api={api} events={o.events ?? true} backoffMs={TEST_BACKOFF}>
        <ToastProvider>{children}</ToastProvider>
      </DataProvider>
    ),
  });
  return { ...result, daemon, user };
}

// React Flow's pane wires a d3-zoom pan-start handler to "mousedown"; with `nodesDraggable` false,
// nothing on a node stops that handler from also firing on a node click. Real browsers set
// `MouseEvent.view`, so d3-drag's `nodrag(event.view)` never breaks there, but jsdom (via
// @testing-library/user-event's full pointerdown→mousedown→mouseup→click sequence) leaves `view`
// null, and d3-drag crashes reading `view.document` — an unhandled exception that fails the test run
// even though the assertions around it pass. `fireEvent.click` dispatches only the "click" event a
// node's own `onClick` listens for, without the intermediate mousedown d3-zoom reacts to, so it
// exercises the same selection behavior without touching that unrelated environment gap. Centralised
// here so every React-Flow-node test uses one workaround instead of re-deriving it inline.
export function clickNode(el: Element): void {
  fireEvent.click(el);
}

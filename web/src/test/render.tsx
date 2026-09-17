import { type RenderResult, render } from "@testing-library/react";
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
  const result = render(
    <DataProvider api={api} events={o.events ?? true} backoffMs={TEST_BACKOFF}>
      <ToastProvider>{ui}</ToastProvider>
    </DataProvider>,
  );
  return { ...result, daemon, user };
}

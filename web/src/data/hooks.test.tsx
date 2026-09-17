import { act, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { createApi } from "../api";
import { createMockDaemon } from "../mock/daemon";
import { mockFetch } from "../mock/fetch";
import { TEST_BACKOFF, renderWithDaemon } from "../test/render";
import { DataProvider, useConnection, useMutation, useQuery } from "./hooks";
import { qk, useItems } from "./queries";

function setup(events = true) {
  const daemon = createMockDaemon();
  vi.stubGlobal("fetch", mockFetch(daemon));
  const api = createApi();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <DataProvider api={api} events={events} backoffMs={TEST_BACKOFF}>{children}</DataProvider>
  );
  return { daemon, wrapper };
}

describe("data hooks", () => {
  it("loads a query", async () => {
    const { wrapper } = setup(false);
    const { result } = renderHook(() => useItems(), { wrapper });
    expect(result.current.loading).toBe(true);
    await waitFor(() => expect(result.current.data?.items.length).toBeGreaterThan(0));
  });

  it("refetches when an SSE event invalidates the key", async () => {
    const { daemon, wrapper } = setup();
    const { result } = renderHook(() => useItems(), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    const before = daemon.calls.filter((c) => c.path === "/api/items?view=flat").length;
    act(() => daemon.emit("item.changed", { key: "EPIC-12", root_key: "EPIC-12" }));
    await waitFor(() => expect(daemon.calls.filter((c) => c.path === "/api/items?view=flat").length).toBe(before + 1));
  });

  it("reports the connection and refetches everything after reconnecting", async () => {
    const { daemon, wrapper } = setup();
    const { result } = renderHook(() => ({ conn: useConnection(), items: useItems() }), { wrapper });
    await waitFor(() => expect(result.current.conn.state).toBe("open"));
    expect(result.current.conn.connected).toBe(true);
    act(() => daemon.disconnect());
    await waitFor(() => expect(result.current.conn.state).toBe("closed"));
    expect(result.current.conn.connected).toBe(false);
    const before = daemon.calls.length;
    daemon.reconnect();
    act(() => result.current.conn.retry());
    await waitFor(() => expect(result.current.conn.state).toBe("open"));
    await waitFor(() => expect(daemon.calls.length).toBeGreaterThan(before));
  });

  it("mutations invalidate on success and expose errors", async () => {
    const { daemon, wrapper } = setup(false);
    const { result } = renderHook(
      () => ({
        items: useItems(),
        patch: useMutation((api, key: string, revision: number) => api.patchItem(key, { status: "blocked", revision }), [qk.items]),
      }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.items.data).toBeDefined());
    const rev = daemon.db.items.find((i) => i.key === "TASK-103")?.revision ?? 0;
    await act(() => result.current.patch.run("TASK-103", rev));
    await waitFor(() => expect(result.current.items.data?.items.find((i) => i.key === "TASK-103")?.status).toBe("blocked"));
    await act(async () => {
      await expect(result.current.patch.run("TASK-103", 0)).rejects.toMatchObject({ status: 409 });
    });
    expect(result.current.patch.error).toMatchObject({ code: "conflict" });
    act(() => result.current.patch.reset());
    expect(result.current.patch.error).toBeUndefined();
  });

  it("a null key does not load", () => {
    const { wrapper } = setup(false);
    const load = vi.fn();
    const { result } = renderHook(() => useQuery(null, load), { wrapper });
    expect(result.current).toMatchObject({ data: undefined, loading: false });
    expect(load).not.toHaveBeenCalled();
  });

  it("renderWithDaemon wires providers and the hash", async () => {
    function Probe() {
      const items = useItems();
      return <p>{items.data ? `${items.data.items.length} items at ${window.location.hash}` : "…"}</p>;
    }
    renderWithDaemon(<Probe />, { hash: "#/kanban" });
    expect(await screen.findByText(/items at #\/kanban/)).toBeInTheDocument();
  });
});

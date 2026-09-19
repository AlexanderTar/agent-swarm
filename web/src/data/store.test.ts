import { describe, expect, it, vi } from "vitest";
import { QueryStore } from "./store";

describe("QueryStore", () => {
  it("loads once, notifies and dedupes in-flight loads", async () => {
    const s = new QueryStore();
    const fn = vi.fn();
    s.subscribe("items", fn);
    const load = vi.fn(async () => [1]);
    await Promise.all([s.fetch("items", load), s.fetch("items", load)]);
    expect(load).toHaveBeenCalledTimes(1);
    expect(s.get("items")).toEqual({ data: [1], error: undefined, loading: false });
    expect(fn).toHaveBeenCalled();
    await s.fetch("items", load);
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("keeps data while refetching and records errors", async () => {
    const s = new QueryStore();
    s.subscribe("a", () => {});
    await s.fetch("a", async () => 1);
    let release!: () => void;
    const p = s.fetch("a", () => new Promise((r) => { release = () => r(2); }), true);
    expect(s.get("a")).toEqual({ data: 1, error: undefined, loading: true });
    release();
    await p;
    expect(s.get("a")?.data).toBe(2);
    await s.fetch("a", async () => { throw new Error("boom"); }, true);
    expect(s.get("a")).toMatchObject({ data: 2, loading: false });
    expect((s.get("a")?.error as Error).message).toBe("boom");
  });

  it("refetches subscribed keys on invalidate and drops the rest", async () => {
    const s = new QueryStore();
    const unsub = s.subscribe("item:A", () => {});
    let n = 0;
    await s.fetch("item:A", async () => ++n);
    await s.fetch("item:B", async () => 0);
    s.invalidate(["item:"]);
    await vi.waitFor(() => expect(s.get("item:A")?.data).toBe(2));
    expect(s.get("item:B")).toBeUndefined();
    unsub();
    s.invalidate([""]);
    expect(s.get("item:A")).toBeUndefined();
  });

  it("refetches again when invalidated during a load", async () => {
    const s = new QueryStore();
    s.subscribe("k", () => {});
    let n = 0;
    let release!: () => void;
    const first = s.fetch("k", () => new Promise<number>((r) => { n++; release = () => r(n); }));
    s.invalidate(["k"]);
    release();
    await first;
    await vi.waitFor(() => expect(n).toBe(2));
    release();
    await vi.waitFor(() => expect(s.get("k")?.data).toBe(2));
  });

  it("drops an in-flight read's stale response when it's invalidated with no subscribers (R1 Important)", async () => {
    const s = new QueryStore();
    const unsub = s.subscribe("k", () => {});
    let release!: () => void;
    const first = s.fetch("k", () => new Promise<number>((r) => { release = () => r(1); }));
    unsub(); // the view unmounts while the read is still in flight: zero subscribers left
    s.invalidate(["k"]);
    release(); // the pre-invalidate response lands
    await first;
    // The stale response must not commit as fresh data that a later non-forced fetch can short-circuit on.
    expect(s.get("k")).toBeUndefined();
    // "remount": a plain fetch call must actually load, not serve the dropped, stale value back.
    const reload = vi.fn(async () => 2);
    s.subscribe("k", () => {});
    await s.fetch("k", reload);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(s.get("k")?.data).toBe(2);
  });
});

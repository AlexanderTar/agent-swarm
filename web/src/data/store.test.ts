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
});

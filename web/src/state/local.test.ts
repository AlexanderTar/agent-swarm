import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { readJson, useLocalSet, writeJson } from "./local";

describe("local storage helpers", () => {
  it("reads and writes JSON with a fallback", () => {
    expect(readJson(localStorage, "k", [1])).toEqual([1]);
    writeJson(localStorage, "k", [2]);
    expect(readJson(localStorage, "k", [1])).toEqual([2]);
    localStorage.setItem("bad", "{");
    expect(readJson(localStorage, "bad", "x")).toBe("x");
  });

  it("survives a throwing storage", () => {
    const broken = { getItem: vi.fn(() => { throw new Error("denied"); }), setItem: vi.fn(() => { throw new Error("denied"); }) } as unknown as Storage;
    expect(readJson(broken, "k", 5)).toBe(5);
    expect(() => writeJson(broken, "k", 1)).not.toThrow();
  });

  it("remembers a toggled set", () => {
    const { result, unmount } = renderHook(() => useLocalSet("swarm.test", ["draft"]));
    expect([...result.current[0]]).toEqual(["draft"]);
    act(() => result.current[1]("draft"));
    act(() => result.current[1]("done"));
    expect([...result.current[0]]).toEqual(["done"]);
    unmount();
    const again = renderHook(() => useLocalSet("swarm.test", ["draft"]));
    expect([...again.result.current[0]]).toEqual(["done"]);
  });
});

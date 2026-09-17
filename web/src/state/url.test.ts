import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BoardUrl } from "./url";
import { DEFAULT_URL, filterOf, formatHash, parseHash, useBoardUrl } from "./url";

describe("url state (§16.5)", () => {
  it("defaults to hierarchy with no filters", () => {
    expect(parseHash("")).toEqual(DEFAULT_URL);
    expect(parseHash("#/")).toEqual(DEFAULT_URL);
    expect(formatHash(DEFAULT_URL)).toBe("#/hierarchy");
  });

  const cases: Array<[string, Partial<BoardUrl>]> = [
    ["#/hierarchy?q=login&type=task&status=blocked&item=TASK-102", { q: "login", type: "task", status: "blocked", item: "TASK-102" }],
    ["#/kanban?level=stories&group=flat", { view: "kanban", level: "stories", group: "flat" }],
    ["#/kanban?level=top&item=EPIC-12", { view: "kanban", level: "top", item: "EPIC-12" }],
    ["#/dependencies?item=TASK-102", { view: "dependencies", item: "TASK-102" }],
    ["#/inbox?req=req_1&filter=approvals", { view: "inbox", req: "req_1", filter: "approvals" }],
  ];

  it.each(cases)("round-trips %s", (hash, patch) => {
    const u = { ...DEFAULT_URL, ...patch };
    expect(parseHash(hash)).toEqual(u);
    expect(formatHash(u)).toBe(hash);
  });

  it("drops invalid values", () => {
    expect(parseHash("#/nope?type=story2&status=weird&level=x&group=y&filter=z")).toEqual(DEFAULT_URL);
  });

  it("encodes search text", () => {
    const u = { ...DEFAULT_URL, q: "a b&c" };
    expect(parseHash(formatHash(u)).q).toBe("a b&c");
  });

  it("extracts the filter", () => {
    expect(filterOf({ ...DEFAULT_URL, q: "x", type: "bug", status: "done" })).toEqual({ q: "x", type: "bug", status: "done" });
  });

  it("switching view keeps the selection", () => {
    window.history.replaceState(null, "", "/#/hierarchy?item=TASK-102");
    const { result } = renderHook(() => useBoardUrl());
    act(() => result.current[1]({ view: "kanban" }));
    expect(result.current[0]).toMatchObject({ view: "kanban", item: "TASK-102" });
    expect(window.location.hash).toBe("#/kanban?item=TASK-102");
  });

  it("keeps the parsed value stable across renders", () => {
    const { result, rerender } = renderHook(() => useBoardUrl());
    const first = result.current[0];
    rerender();
    expect(result.current[0]).toBe(first);
  });

  it("follows external hash changes", () => {
    const { result } = renderHook(() => useBoardUrl());
    act(() => {
      window.history.replaceState(null, "", "/#/dependencies?item=EPIC-12");
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(result.current[0]).toMatchObject({ view: "dependencies", item: "EPIC-12" });
  });
});

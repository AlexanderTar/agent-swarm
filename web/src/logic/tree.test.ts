import { describe, expect, it } from "vitest";
import type { Filter } from "../types";
import {
  ancestors, buildIndex, compareKeys, compareTopLevel, filterItems, hierarchyRows, isFilterActive, makeItem, matches, outsideView,
} from "./tree";

const none: Filter = { q: "", type: "", status: "" };
const items = [
  makeItem({ key: "BUG-7", title: "Login crash", priority: 1, created_at: 5 }),
  makeItem({ key: "EPIC-12", title: "Authentication", priority: 1, created_at: 9 }),
  makeItem({ key: "SPIKE-3", title: "Offline mode", priority: 2, created_at: 99 }),
  makeItem({ key: "STORY-41", title: "Password reset", parent_key: "EPIC-12", root_key: "EPIC-12", sort_order: 2 }),
  makeItem({ key: "STORY-40", title: "Login", parent_key: "EPIC-12", root_key: "EPIC-12", sort_order: 1 }),
  makeItem({ key: "TASK-102", title: "Session handling", parent_key: "STORY-40", root_key: "EPIC-12", status: "in_review" }),
  makeItem({ key: "TASK-99", title: "Login form", parent_key: "STORY-40", root_key: "EPIC-12", status: "done" }),
  makeItem({ key: "TASK-999", title: "Orphan", parent_key: "STORY-404", root_key: "EPIC-404" }),
];

describe("tree logic (§16.5, §16.6)", () => {
  it("derives the type from the key in the factory", () => {
    expect(makeItem({ key: "STORY-1" }).type).toBe("story");
  });

  it("matches key or title, type and status", () => {
    const t = items[5]!;
    expect(matches(t, { ...none, q: "task-10" })).toBe(true);
    expect(matches(t, { ...none, q: "SESSION" })).toBe(true);
    expect(matches(t, { ...none, q: "nope" })).toBe(false);
    expect(matches(t, { ...none, type: "story" })).toBe(false);
    expect(matches(t, { ...none, status: "in_review" })).toBe(true);
    expect(isFilterActive(none)).toBe(false);
    expect(isFilterActive({ ...none, q: "  " })).toBe(false);
    expect(isFilterActive({ ...none, status: "done" })).toBe(true);
  });

  it("orders keys numerically, top-level by priority then newest, children by sort order", () => {
    expect(["TASK-101", "TASK-99", "STORY-2"].sort(compareKeys)).toEqual(["STORY-2", "TASK-99", "TASK-101"]);
    const idx = buildIndex(items);
    expect(idx.roots.map((i) => i.key)).toEqual(["EPIC-12", "BUG-7", "SPIKE-3"]);
    expect(idx.children.get("EPIC-12")?.map((i) => i.key)).toEqual(["STORY-40", "STORY-41"]);
    expect(idx.children.get("STORY-40")?.map((i) => i.key)).toEqual(["TASK-99", "TASK-102"]);
    expect(idx.orphans.map((i) => i.key)).toEqual(["TASK-999"]);
    expect(ancestors("TASK-102", idx.byKey).map((i) => i.key)).toEqual(["EPIC-12", "STORY-40"]);
    expect(ancestors("TASK-999", idx.byKey)).toEqual([]);
  });

  it("breaks a top-level tie on the id so the order is stable", () => {
    const a = makeItem({ key: "EPIC-2", priority: 1, created_at: 7 });
    const b = makeItem({ key: "EPIC-1", priority: 1, created_at: 7 });
    expect(compareTopLevel(a, b)).toBeGreaterThan(0);
    expect(buildIndex([a, b]).roots.map((i) => i.key)).toEqual(["EPIC-1", "EPIC-2"]);
    expect(buildIndex([b, a]).roots.map((i) => i.key)).toEqual(["EPIC-1", "EPIC-2"]);
  });

  it("keeps ancestors of matches as context", () => {
    const r = filterItems(items, { ...none, q: "session" });
    expect([...r.matched]).toEqual(["TASK-102"]);
    expect([...r.context].sort()).toEqual(["EPIC-12", "STORY-40"]);
    expect(r.count).toBe(1);
  });

  it("builds rows with depth, collapse and context", () => {
    const all = hierarchyRows(items, none, new Set(["STORY-40"]));
    expect(all.map((r) => `${r.depth}:${r.item.key}${r.expanded ? "" : "+"}`)).toEqual([
      "0:EPIC-12", "1:STORY-40+", "1:STORY-41", "0:BUG-7", "0:SPIKE-3", "0:TASK-999",
    ]);
    const filtered = hierarchyRows(items, { ...none, q: "session" }, new Set(["STORY-40", "EPIC-12"]));
    expect(filtered.map((r) => [r.item.key, r.context, r.expanded])).toEqual([
      ["EPIC-12", true, true],
      ["STORY-40", true, true],
      ["TASK-102", false, true],
    ]);
    expect(filtered[0]?.hasChildren).toBe(true);
    expect(filtered[2]?.hasChildren).toBe(false);
  });

  it("folds done and cancelled items by default; toggling opens them", () => {
    const fin = [
      makeItem({ key: "EPIC-1", status: "done" }),
      makeItem({ key: "STORY-1", parent_key: "EPIC-1", root_key: "EPIC-1", status: "done" }),
      makeItem({ key: "EPIC-2", status: "cancelled", priority: 3 }),
      makeItem({ key: "STORY-2", parent_key: "EPIC-2", root_key: "EPIC-2", status: "cancelled" }),
    ];
    const keys = (t: string[], f = none) => hierarchyRows(fin, f, new Set(t)).map((r) => r.item.key);
    expect(keys([])).toEqual(["EPIC-1", "EPIC-2"]);
    expect(keys(["EPIC-1"])).toEqual(["EPIC-1", "STORY-1", "EPIC-2"]);
    expect(keys([], { ...none, status: "done" })).toEqual(["EPIC-1", "STORY-1"]);
  });

  it("detects a selection outside the current view", () => {
    expect(outsideView("TASK-102", items, none, "kanban", "tasks")).toBe(false);
    expect(outsideView("EPIC-12", items, none, "kanban", "tasks")).toBe(true);
    expect(outsideView("EPIC-12", items, none, "kanban", "top")).toBe(false);
    expect(outsideView("TASK-99", items, { ...none, status: "blocked" }, "hierarchy", "tasks")).toBe(true);
    expect(outsideView("STORY-40", items, { ...none, q: "session" }, "hierarchy", "tasks")).toBe(false);
    expect(outsideView("TASK-99", items, { ...none, status: "blocked" }, "dependencies", "tasks")).toBe(false);
    expect(outsideView("NOPE-1", items, none, "hierarchy", "tasks")).toBe(false);
    expect(outsideView("", items, none, "hierarchy", "tasks")).toBe(false);
  });
});

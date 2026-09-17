import { describe, expect, it } from "vitest";
import type { Filter } from "../types";
import { makeAgent } from "./agentActions";
import {
  agentLine, agentsByItem, blockedLine, buildLanes, columnCounts, columnTitle, columnsFor, DEFAULT_COLLAPSED_COLUMNS,
  dropId, effectiveCollapsed, effectiveGrouping, emptyState, needsLine, parentLine, parseDropId, progressLine,
} from "./kanban";
import { buildIndex, makeItem } from "./tree";

const none: Filter = { q: "", type: "", status: "" };
const epic = makeItem({ key: "EPIC-12", title: "Authentication", priority: 1 });
const bug = makeItem({ key: "BUG-7", title: "Login crash", priority: 2 });
const done = makeItem({ key: "EPIC-30", title: "Legacy", priority: 3 });
const story = makeItem({ key: "STORY-40", title: "Login", parent_key: "EPIC-12", root_key: "EPIC-12", progress: { done: 3, total: 5, unit: "tasks" } });
const t1 = makeItem({ key: "TASK-101", title: "Build login form", parent_key: "STORY-40", root_key: "EPIC-12", status: "in_progress", open_requests: 1 });
const t2 = makeItem({ key: "TASK-102", title: "Persist session", parent_key: "STORY-40", root_key: "EPIC-12", status: "blocked", blocked_by: ["TASK-98", "TASK-97"] });
const t3 = makeItem({ key: "TASK-98", title: "Fix race", parent_key: "BUG-7", root_key: "BUG-7", status: "done" });
const t4 = makeItem({ key: "TASK-150", parent_key: "EPIC-30", root_key: "EPIC-30", status: "done" });
const t5 = makeItem({ key: "TASK-151", parent_key: "EPIC-30", root_key: "EPIC-30", status: "cancelled" });
const orphan = makeItem({ key: "TASK-999", parent_key: "STORY-404", root_key: "EPIC-404" });
const items = [epic, bug, done, story, t1, t2, t3, t4, t5, orphan];

describe("kanban columns (§16.7)", () => {
  it("shows Awaiting approval only for top-level cards and forces flat grouping there", () => {
    expect(columnsFor("tasks")).not.toContain("awaiting_approval");
    expect(columnsFor("top")).toContain("awaiting_approval");
    expect(effectiveGrouping("top", "root")).toBe("flat");
    expect(effectiveGrouping("tasks", "root")).toBe("root");
    expect(DEFAULT_COLLAPSED_COLUMNS).toEqual(["draft", "done", "cancelled"]);
  });

  it("expands a collapsed column targeted by the status filter", () => {
    expect([...effectiveCollapsed(new Set(["draft", "done"]), { ...none, status: "done" })]).toEqual(["draft"]);
  });

  it("counts shown and total cards per column", () => {
    const c = columnCounts(items, { ...none, q: "login" }, "tasks");
    expect(c.get("in_progress")).toEqual({ shown: 1, total: 1 });
    expect(c.get("done")).toEqual({ shown: 0, total: 2 });
    expect(columnTitle("in_progress", { shown: 8, total: 13 }, true)).toEqual({ text: "In progress · 8", tooltip: "8 of 13 items" });
    expect(columnTitle("ready", { shown: 4, total: 4 }, false)).toEqual({ text: "Ready · 4" });
  });
});

describe("kanban lanes", () => {
  it("groups task cards by top-level item in hierarchy order with an orphan lane", () => {
    const lanes = buildLanes(items, none, "tasks", "root");
    expect(lanes.map((l) => l.id)).toEqual(["EPIC-12", "BUG-7", "EPIC-30", "orphans"]);
    expect(lanes[0]?.header).toBe("2 tasks · 1 need you · 1 blocked");
    expect(lanes[0]?.cards.map((c) => c.key)).toEqual(["TASK-101", "TASK-102"]);
    expect(lanes[1]).toMatchObject({ completedText: "All 1 tasks done.", defaultCollapsed: true });
    expect(lanes[2]).toMatchObject({ completedText: "All 2 tasks finished · 1 done, 1 cancelled.", defaultCollapsed: true });
    expect(lanes[0]?.defaultCollapsed).toBe(false);
    expect(lanes[3]?.root).toBeNull();
  });

  it("uses one flat lane and filters cards", () => {
    const flat = buildLanes(items, { ...none, status: "blocked" }, "tasks", "flat");
    expect(flat).toHaveLength(1);
    expect(flat[0]?.cards.map((c) => c.key)).toEqual(["TASK-102"]);
    expect(buildLanes(items, { ...none, q: "zzz" }, "tasks", "flat")).toEqual([]);
    expect(buildLanes(items, none, "top", "root")[0]?.cards.map((c) => c.key)).toEqual(["EPIC-12", "BUG-7", "EPIC-30"]);
    expect(buildLanes(items, none, "stories", "root")[0]?.header).toBe("1 stories");
  });
});

describe("kanban empty states (§17.4)", () => {
  it.each([
    [[], none, "tasks", { text: "No work items yet.", action: "newItem" }],
    [items, { ...none, type: "spike" }, "tasks", { text: "Spikes have no task cards yet. Switch Card level to Top-level items to see them.", action: "showTopLevel" }],
    [[epic], none, "tasks", { text: "No tasks yet. Open Hierarchy to add or inspect work." }],
    [[epic], none, "stories", { text: "No stories match. Stories belong to epics." }],
    [items, { ...none, type: "epic" }, "stories", { text: "No stories match. Stories belong to epics." }],
    [items, { ...none, q: "zzz" }, "tasks", { text: "No tasks match these filters.", action: "clearFilters" }],
    [items, { ...none, q: "zzz" }, "top", { text: "No items match these filters.", action: "clearFilters" }],
    [items, none, "tasks", null],
  ] as const)("%#", (list, f, level, out) => {
    expect(emptyState([...list], f, level)).toEqual(out);
  });
});

describe("card lines", () => {
  const idx = buildIndex(items);
  it("shows the parent, progress, blockers and needs badge", () => {
    expect(parentLine(t1, idx.byKey, "root")).toBe("STORY-40 · Login");
    expect(parentLine(t1, idx.byKey, "flat")).toBe("EPIC-12 / STORY-40");
    expect(parentLine(t3, idx.byKey, "flat")).toBe("BUG-7 · Login crash");
    expect(parentLine(epic, idx.byKey, "root")).toBeNull();
    expect(progressLine(story)).toBe("3 of 5 tasks done");
    expect(progressLine(t1)).toBeNull();
    expect(blockedLine(t2)).toBe("Blocked by TASK-98 +1");
    expect(blockedLine(t1)).toBeNull();
    expect(needsLine(t1)).toBe("Needs 1");
    expect(needsLine(t2)).toBeNull();
  });

  it("shows one agent with +N and skips finished agents", () => {
    const a1 = makeAgent({ name: "login-form-coder", item_key: "TASK-101" });
    const a2 = makeAgent({ name: "login-review", item_key: "TASK-101" });
    const gone = makeAgent({ name: "old", item_key: "TASK-101", state: "finished" });
    const orch = makeAgent({ name: "orch", role: "orchestrator", item_key: "EPIC-12", children: [a1, a2, gone] });
    const byItem = agentsByItem([orch]);
    expect(agentLine(t1, byItem)).toEqual({ agent: a1, extra: 1 });
    expect(agentLine(t2, byItem)).toBeNull();
  });

  it("encodes drop targets", () => {
    expect(dropId("EPIC-12", "ready")).toBe("EPIC-12|ready");
    expect(parseDropId("EPIC-12|in_review")).toEqual({ laneId: "EPIC-12", status: "in_review" });
  });
});

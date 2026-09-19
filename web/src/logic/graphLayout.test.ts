import { describe, expect, it } from "vitest";
import type { Graph } from "../types";
import { LayoutCache, NODE_W, dimmedKeys, layoutGraph, signature, storyOfFn, toFlow } from "./graphLayout";
import { buildIndex, makeItem } from "./tree";

const items = [
  makeItem({ key: "EPIC-12" }),
  makeItem({ key: "STORY-40", title: "Login", parent_key: "EPIC-12", root_key: "EPIC-12" }),
  makeItem({ key: "TASK-102", parent_key: "STORY-40", root_key: "EPIC-12" }),
  makeItem({ key: "TASK-104", parent_key: "STORY-40", root_key: "EPIC-12" }),
  makeItem({ key: "BUG-7" }),
  makeItem({ key: "TASK-98", parent_key: "BUG-7", root_key: "BUG-7" }),
];
const g: Graph = {
  nodes: [
    { key: "TASK-98", type: "task", title: "Fix race", status: "done", root_key: "BUG-7", external: true },
    { key: "TASK-102", type: "task", title: "Persist session", status: "blocked", root_key: "EPIC-12", external: false },
    { key: "TASK-104", type: "task", title: "Validate inputs", status: "ready", root_key: "EPIC-12", external: false },
  ],
  edges: [{ from: "TASK-98", to: "TASK-102" }, { from: "TASK-102", to: "TASK-104" }],
};
const byKey = buildIndex(items).byKey;

describe("graph layout (§16.8)", () => {
  it("lays out left to right", () => {
    const laid = layoutGraph(g, storyOfFn(byKey, "root"));
    const x = (k: string) => laid.nodes.get(k)?.x ?? Number.NaN;
    expect(x("TASK-98")).toBeLessThan(x("TASK-102"));
    expect(x("TASK-102")).toBeLessThan(x("TASK-104"));
    expect(laid.nodes.get("TASK-98")?.width).toBe(NODE_W);
    expect(laid.order).toEqual(["TASK-98", "TASK-102", "TASK-104"]);
  });

  it("draws a box around each story's tasks in root scope only", () => {
    const laid = layoutGraph(g, storyOfFn(byKey, "root"));
    const box = laid.groups.get("STORY-40");
    expect(box).toBeDefined();
    for (const k of ["TASK-102", "TASK-104"]) {
      const n = laid.nodes.get(k);
      expect(n && box && n.x >= box.x && n.x + n.width <= box.x + box.width).toBe(true);
      expect(n && box && n.y >= box.y && n.y + n.height <= box.y + box.height).toBe(true);
    }
    expect(laid.groups.has("BUG-7")).toBe(false);
    expect(layoutGraph(g, storyOfFn(byKey, "neighbourhood")).groups.size).toBe(0);
  });

  it("keeps positions when only statuses change", () => {
    const cache = new LayoutCache();
    const storyOf = storyOfFn(byKey, "root");
    const a = cache.get("EPIC-12:root", g, storyOf);
    const changed: Graph = { ...g, nodes: g.nodes.map((n) => ({ ...n, status: "done" as const })) };
    expect(cache.get("EPIC-12:root", changed, storyOf)).toBe(a);
    const grown: Graph = { ...g, edges: [...g.edges, { from: "TASK-98", to: "TASK-104" }] };
    expect(cache.get("EPIC-12:root", grown, storyOf)).not.toBe(a);
    expect(signature(g, storyOf)).not.toBe(signature(grown, storyOf));
  });

  it("dims non-matching nodes", () => {
    expect([...dimmedKeys(g, { q: "", type: "", status: "blocked" })].sort()).toEqual(["TASK-104", "TASK-98"]);
    expect(dimmedKeys(g, { q: "", type: "", status: "" }).size).toBe(0);
  });

  it("builds React Flow nodes and edges", () => {
    const laid = layoutGraph(g, storyOfFn(byKey, "root"));
    const flow = toFlow(g, laid, {
      selected: "TASK-102",
      dimmed: new Set(["TASK-98"]),
      needsYou: new Set(["TASK-104"]),
      storyLabel: (k) => `${k} ${byKey.get(k)?.title ?? ""}`,
    });
    expect(flow.nodes.map((n) => `${n.type}:${n.id}`)).toEqual([
      "story:group:STORY-40", "item:TASK-98", "item:TASK-102", "item:TASK-104",
    ]);
    expect(flow.nodes[0]).toMatchObject({ data: { label: "STORY-40 Login" }, focusable: false, selectable: false, zIndex: -1 });
    expect(flow.nodes[1]?.data).toMatchObject({ dimmed: true, needsYou: false });
    expect(flow.nodes[2]?.selected).toBe(true);
    expect(flow.nodes[3]?.data).toMatchObject({ needsYou: true });
    expect(flow.edges.map((e) => e.id)).toEqual(["TASK-98>TASK-102", "TASK-102>TASK-104"]);
  });
});

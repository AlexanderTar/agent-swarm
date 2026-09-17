import { Graph as DagreGraph, layout } from "@dagrejs/dagre";
import { MarkerType } from "@xyflow/react";
import type { Edge, Node } from "@xyflow/react";
import type { Filter, Graph, GraphNode, GraphScope, Item } from "../types";
import { isFilterActive, matches } from "./tree";

export const NODE_W = 180;
export const NODE_H = 64;
const PAD = 12;
const LABEL_H = 22;

export interface Box { x: number; y: number; width: number; height: number }
export interface Laid { nodes: Map<string, Box>; groups: Map<string, Box>; order: string[] }
export type StoryOf = (key: string) => string | null;

export function storyOfFn(byKey: Map<string, Item>, scope: GraphScope): StoryOf {
  return (key) => {
    if (scope !== "root") return null;
    const parent = byKey.get(key)?.parent_key;
    return parent && byKey.get(parent)?.type === "story" ? parent : null;
  };
}

export function signature(g: Graph, storyOf: StoryOf): string {
  const nodes = g.nodes.map((n) => `${n.key}@${storyOf(n.key) ?? ""}`).sort();
  const edges = g.edges.map((e) => `${e.from}>${e.to}`).sort();
  return `${nodes.join(",")}|${edges.join(",")}`;
}

export function layoutGraph(g: Graph, storyOf: StoryOf): Laid {
  const d = new DagreGraph();
  d.setGraph({ rankdir: "LR", nodesep: 28, ranksep: 72, marginx: 16, marginy: 16 });
  d.setDefaultEdgeLabel(() => ({}));
  for (const n of g.nodes) d.setNode(n.key, { width: NODE_W, height: NODE_H });
  for (const e of g.edges) d.setEdge(e.from, e.to);
  layout(d);

  const nodes = new Map<string, Box>();
  for (const n of g.nodes) {
    const p = d.node(n.key);
    nodes.set(n.key, { x: (p.x ?? 0) - NODE_W / 2, y: (p.y ?? 0) - NODE_H / 2, width: NODE_W, height: NODE_H });
  }

  // ponytail: story boxes are bounding rectangles after a flat layout; they can overlap when a
  // story's tasks aren't adjacent. Switch to dagre compound layout if that shows up in real trees.
  const members = new Map<string, Box[]>();
  for (const n of g.nodes) {
    const s = storyOf(n.key);
    const b = nodes.get(n.key);
    if (s && b) members.set(s, [...(members.get(s) ?? []), b]);
  }
  const groups = new Map<string, Box>();
  for (const [story, boxes] of members) {
    const minX = Math.min(...boxes.map((b) => b.x)) - PAD;
    const minY = Math.min(...boxes.map((b) => b.y)) - PAD - LABEL_H;
    const maxX = Math.max(...boxes.map((b) => b.x + b.width)) + PAD;
    const maxY = Math.max(...boxes.map((b) => b.y + b.height)) + PAD;
    groups.set(story, { x: minX, y: minY, width: maxX - minX, height: maxY - minY });
  }

  const order = [...nodes.entries()].sort(([, a], [, b]) => a.x - b.x || a.y - b.y).map(([k]) => k);
  return { nodes, groups, order };
}

export class LayoutCache {
  private readonly map = new Map<string, { sig: string; laid: Laid }>();
  get(scopeKey: string, g: Graph, storyOf: StoryOf): Laid {
    const sig = signature(g, storyOf);
    const hit = this.map.get(scopeKey);
    if (hit && hit.sig === sig) return hit.laid;
    const laid = layoutGraph(g, storyOf);
    this.map.set(scopeKey, { sig, laid });
    return laid;
  }
}

export function dimmedKeys(g: Graph, f: Filter): Set<string> {
  if (!isFilterActive(f)) return new Set();
  return new Set(g.nodes.filter((n) => !matches(n, f)).map((n) => n.key));
}

export interface ItemNodeData extends Record<string, unknown> { node: GraphNode; dimmed: boolean; needsYou: boolean }
export interface StoryNodeData extends Record<string, unknown> { label: string }

export function toFlow(
  g: Graph,
  laid: Laid,
  o: { selected: string; dimmed: ReadonlySet<string>; needsYou: ReadonlySet<string>; storyLabel: (key: string) => string },
): { nodes: Node[]; edges: Edge[] } {
  const stories: Node<StoryNodeData>[] = [...laid.groups].map(([key, b]) => ({
    id: `group:${key}`,
    type: "story",
    position: { x: b.x, y: b.y },
    data: { label: o.storyLabel(key) },
    style: { width: b.width, height: b.height },
    draggable: false,
    selectable: false,
    focusable: false,
    zIndex: -1,
  }));
  const byKey = new Map(g.nodes.map((n) => [n.key, n]));
  const itemNodes: Node<ItemNodeData>[] = laid.order.flatMap((key) => {
    const b = laid.nodes.get(key);
    const n = byKey.get(key);
    if (!b || !n) return [];
    return [{
      id: key,
      type: "item",
      position: { x: b.x, y: b.y },
      data: { node: n, dimmed: o.dimmed.has(key), needsYou: o.needsYou.has(key) },
      selected: key === o.selected,
      draggable: false,
      style: { width: b.width, height: b.height },
    }];
  });
  const edges: Edge[] = g.edges.map((e) => ({
    id: `${e.from}>${e.to}`,
    source: e.from,
    target: e.to,
    markerEnd: { type: MarkerType.ArrowClosed },
  }));
  return { nodes: [...stories, ...itemNodes], edges };
}

import { C, STATUS_LABEL, T, TYPE_PLURAL } from "../copy";
import { ITEM_STATUSES } from "../types";
import type { AgentNode, CardLevel, Filter, Grouping, Item, ItemStatus } from "../types";
import { flattenAgents, isFinished } from "./agentActions";
import { LEVEL_TYPES, ancestors, buildIndex, compareChildren, isFilterActive, matches } from "./tree";

export const columnsFor = (level: CardLevel): ItemStatus[] =>
  level === "top" ? [...ITEM_STATUSES] : ITEM_STATUSES.filter((s) => s !== "awaiting_approval");

export const effectiveGrouping = (level: CardLevel, group: Grouping): Grouping => (level === "top" ? "flat" : group);

export const DEFAULT_COLLAPSED_COLUMNS: ItemStatus[] = ["draft", "done", "cancelled"];

export function effectiveCollapsed(collapsed: ReadonlySet<string>, f: Filter): Set<string> {
  const s = new Set(collapsed);
  if (f.status) s.delete(f.status);
  return s;
}

export const levelCards = (items: Item[], level: CardLevel) => items.filter((i) => LEVEL_TYPES[level].includes(i.type));

const UNIT: Record<CardLevel, string> = { tasks: "tasks", stories: "stories", top: "items" };

export interface Lane {
  id: string;
  root: Item | null;
  cards: Item[];
  header: string;
  completedText: string | null;
  defaultCollapsed: boolean;
}

function makeLane(id: string, root: Item | null, cards: Item[], level: CardLevel): Lane {
  const unit = UNIT[level];
  const done = cards.filter((c) => c.status === "done").length;
  const cancelled = cards.filter((c) => c.status === "cancelled").length;
  const finished = cards.length > 0 && done + cancelled === cards.length;
  return {
    id,
    root,
    cards,
    header: T.laneHeader(
      cards.length,
      unit,
      cards.filter((c) => c.open_requests > 0).length,
      cards.filter((c) => c.status === "blocked").length,
    ),
    completedText: finished
      ? cancelled > 0
        ? T.laneFinished(cards.length, done, cancelled, unit)
        : T.laneDone(cards.length, unit)
      : null,
    defaultCollapsed: finished,
  };
}

export function buildLanes(items: Item[], f: Filter, level: CardLevel, group: Grouping): Lane[] {
  const idx = buildIndex(items);
  const cards = levelCards(items, level).filter((i) => matches(i, f));
  if (level === "top") cards.sort((a, b) => idx.roots.indexOf(a) - idx.roots.indexOf(b));
  else cards.sort(compareChildren);
  if (effectiveGrouping(level, group) === "flat") return cards.length ? [makeLane("flat", null, cards, level)] : [];
  const byRoot = new Map<string, Item[]>();
  const orphans: Item[] = [];
  for (const c of cards) {
    const top = c.parent_key === null ? c : ancestors(c.key, idx.byKey)[0];
    if (!top) orphans.push(c);
    else byRoot.set(top.key, [...(byRoot.get(top.key) ?? []), c]);
  }
  const lanes = idx.roots.filter((r) => byRoot.has(r.key)).map((r) => makeLane(r.key, r, byRoot.get(r.key) ?? [], level));
  if (orphans.length) lanes.push(makeLane("orphans", null, orphans, level));
  return lanes;
}

export interface ColumnCount { shown: number; total: number }

export function columnCounts(items: Item[], f: Filter, level: CardLevel): Map<ItemStatus, ColumnCount> {
  const all = levelCards(items, level);
  const shown = all.filter((i) => matches(i, f));
  return new Map(
    columnsFor(level).map((s) => [
      s,
      { shown: shown.filter((i) => i.status === s).length, total: all.filter((i) => i.status === s).length },
    ]),
  );
}

export function columnTitle(status: ItemStatus, c: ColumnCount, filtered: boolean): { text: string; tooltip?: string } {
  const text = T.colHeader(STATUS_LABEL[status], c.shown);
  return filtered ? { text, tooltip: T.colTooltip(c.shown, c.total) } : { text };
}

export type EmptyAction = "newItem" | "clearFilters" | "showTopLevel";

export function emptyState(items: Item[], f: Filter, level: CardLevel): { text: string; action?: EmptyAction } | null {
  if (items.length === 0) return { text: C.noItems, action: "newItem" };
  if (level === "tasks" && f.type !== "" && f.type !== "task") {
    return { text: T.typeHint(TYPE_PLURAL[f.type]), action: "showTopLevel" };
  }
  const all = levelCards(items, level);
  const anyMatch = all.some((i) => matches(i, f));
  if (level === "stories" && !anyMatch) return { text: C.kanbanNoStories };
  if (all.length === 0) return level === "tasks" ? { text: C.kanbanNoTasks } : { text: C.noItems, action: "newItem" };
  if (!anyMatch && isFilterActive(f)) {
    return { text: level === "tasks" ? C.kanbanFiltered : C.filteredNone, action: "clearFilters" };
  }
  return null;
}

export function parentLine(card: Item, byKey: Map<string, Item>, group: Grouping): string | null {
  const parent = card.parent_key ? byKey.get(card.parent_key) : undefined;
  if (!parent) return null;
  const root = byKey.get(card.root_key);
  if (group === "flat" && root && root.key !== parent.key) return `${root.key} / ${parent.key}`;
  return `${parent.key} · ${parent.title}`;
}

export const progressLine = (item: Item) =>
  item.type !== "task" && item.progress ? T.progress(item.progress.done, item.progress.total, item.progress.unit) : null;

export const blockedLine = (item: Item) =>
  item.blocked_by.length > 0 ? T.blockedBy(item.blocked_by[0] ?? "", item.blocked_by.length - 1) : null;

export const needsLine = (item: Item) => (item.open_requests > 0 ? T.needs(item.open_requests) : null);

export function agentsByItem(agents: AgentNode[]): Map<string, AgentNode[]> {
  const out = new Map<string, AgentNode[]>();
  for (const a of flattenAgents(agents)) {
    if (isFinished(a)) continue;
    out.set(a.item_key, [...(out.get(a.item_key) ?? []), a]);
  }
  return out;
}

export function agentLine(card: Item, byItem: Map<string, AgentNode[]>): { agent: AgentNode; extra: number } | null {
  const list = byItem.get(card.key);
  const first = list?.[0];
  return first && list ? { agent: first, extra: list.length - 1 } : null;
}

export const dropId = (laneId: string, status: ItemStatus) => `${laneId}|${status}`;

export function parseDropId(id: string): { laneId: string; status: ItemStatus } {
  const i = id.lastIndexOf("|");
  return { laneId: id.slice(0, i), status: id.slice(i + 1) as ItemStatus };
}

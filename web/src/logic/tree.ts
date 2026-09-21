import type { CardLevel, Filter, Item, ItemType, View } from "../types";

export type Matchable = Pick<Item, "key" | "title" | "type" | "status">;

export const LEVEL_TYPES: Record<CardLevel, ItemType[]> = {
  tasks: ["task"],
  stories: ["story"],
  top: ["epic", "bug", "spike"],
};

// F20: the allowed parent type per item type for a user-created item (top-level types have no
// parent and are absent here). The mock daemon and later item-creation UIs both need this rule;
// export it once so nobody re-derives it.
export const PARENT_TYPES: Partial<Record<ItemType, ItemType[]>> = {
  story: ["epic"],
  task: ["story", "bug", "spike"],
};

export const isFilterActive = (f: Filter) => f.q.trim() !== "" || f.type !== "" || f.status !== "";

export function matches(item: Matchable, f: Filter): boolean {
  const q = f.q.trim().toLowerCase();
  if (q && !item.key.toLowerCase().includes(q) && !item.title.toLowerCase().includes(q)) return false;
  if (f.type && item.type !== f.type) return false;
  if (f.status && item.status !== f.status) return false;
  return true;
}

function splitKey(k: string): [string, number] {
  const i = k.lastIndexOf("-");
  return [k.slice(0, i), Number(k.slice(i + 1))];
}

export function compareKeys(a: string, b: string): number {
  const [ap, an] = splitKey(a);
  const [bp, bn] = splitKey(b);
  return ap.localeCompare(bp) || an - bn;
}

// The id tiebreak keeps equal-priority, equal-age rows in one order across re-renders.
export const compareTopLevel = (a: Item, b: Item) =>
  a.priority - b.priority || b.created_at - a.created_at || a.id.localeCompare(b.id);
export const compareChildren = (a: Item, b: Item) => a.sort_order - b.sort_order || compareKeys(a.key, b.key);

export interface ItemIndex { byKey: Map<string, Item>; children: Map<string, Item[]>; roots: Item[]; orphans: Item[] }

export function buildIndex(items: Item[]): ItemIndex {
  const byKey = new Map(items.map((i) => [i.key, i]));
  const children = new Map<string, Item[]>();
  const roots: Item[] = [];
  const orphans: Item[] = [];
  for (const it of items) {
    if (it.parent_key === null) roots.push(it);
    else if (!byKey.has(it.parent_key)) orphans.push(it);
    else children.set(it.parent_key, [...(children.get(it.parent_key) ?? []), it]);
  }
  roots.sort(compareTopLevel);
  for (const list of children.values()) list.sort(compareChildren);
  orphans.sort(compareChildren);
  return { byKey, children, roots, orphans };
}

export function ancestors(key: string, byKey: Map<string, Item>): Item[] {
  const out: Item[] = [];
  let cur = byKey.get(key)?.parent_key ?? null;
  while (cur !== null) {
    const p = byKey.get(cur);
    if (!p) return [];
    out.unshift(p);
    cur = p.parent_key;
  }
  return out;
}

export interface FilterResult { matched: Set<string>; context: Set<string>; count: number }

export function filterItems(items: Item[], f: Filter): FilterResult {
  const byKey = new Map(items.map((i) => [i.key, i]));
  const matched = new Set(items.filter((i) => matches(i, f)).map((i) => i.key));
  const context = new Set<string>();
  for (const k of matched) for (const a of ancestors(k, byKey)) if (!matched.has(a.key)) context.add(a.key);
  return { matched, context, count: matched.size };
}

export interface TreeRow { item: Item; depth: number; context: boolean; hasChildren: boolean; expanded: boolean }

const isFinal = (s: Item["status"]) => s === "done" || s === "cancelled";

// Done/cancelled items start folded, unless a filter is active (so matches are never hidden inside
// a fold). `collapsed` and `opened` are the user's explicit overrides; collapsed wins.
export function hierarchyRows(
  items: Item[],
  f: Filter,
  collapsed: ReadonlySet<string>,
  opened: ReadonlySet<string> = new Set(),
): TreeRow[] {
  const idx = buildIndex(items);
  const fr = isFilterActive(f) ? filterItems(items, f) : null;
  const rows: TreeRow[] = [];
  const visit = (it: Item, depth: number) => {
    const context = fr?.context.has(it.key) ?? false;
    if (fr && !fr.matched.has(it.key) && !context) return;
    const kids = idx.children.get(it.key) ?? [];
    // Context rows stay open so matches are visible; the stored collapse state is untouched.
    const expanded = context || (!collapsed.has(it.key) && (opened.has(it.key) || fr !== null || !isFinal(it.status)));
    rows.push({ item: it, depth, context, hasChildren: kids.length > 0, expanded });
    if (expanded) for (const k of kids) visit(k, depth + 1);
  };
  for (const r of [...idx.roots, ...idx.orphans]) visit(r, 0);
  return rows;
}

export function outsideView(key: string, items: Item[], f: Filter, view: View, level: CardLevel): boolean {
  const item = items.find((i) => i.key === key);
  if (!item || view === "dependencies" || view === "inbox") return false;
  if (view === "kanban" && !LEVEL_TYPES[level].includes(item.type)) return true;
  if (!isFilterActive(f)) return false;
  const fr = filterItems(items, f);
  return !fr.matched.has(key) && (view === "kanban" || !fr.context.has(key));
}

export function makeItem(p: Partial<Item> & { key: string }): Item {
  const type = (p.type ?? p.key.slice(0, p.key.lastIndexOf("-")).toLowerCase()) as ItemType;
  return {
    id: `itm_${p.key}`,
    type,
    parent_id: p.parent_key ? `itm_${p.parent_key}` : null,
    parent_key: null,
    root_id: `itm_${p.root_key ?? p.key}`,
    root_key: p.key,
    title: p.key,
    brief: "",
    acceptance: [],
    status: "ready",
    status_before_block: null,
    priority: 2,
    role_hint: null,
    tdd_exempt: null,
    repos: [],
    repos_version: 0,
    suggested_repos: [],
    spike_intent: null,
    origin_spike_id: p.origin_spike_key ? `itm_${p.origin_spike_key}` : null,
    origin_spike_key: null,
    legacy_key: null,
    sort_order: 0,
    revision: 1,
    archived_at: null,
    created_at: 0,
    updated_at: 0,
    blocked_by: [],
    progress: null,
    active_agents: 0,
    open_requests: 0,
    context: false,
    ...p,
  };
}

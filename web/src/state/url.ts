import { useCallback, useSyncExternalStore } from "react";
import { ITEM_STATUSES } from "../types";
import type { CardLevel, Filter, Grouping, InboxFilter, ItemStatus, ItemType, View } from "../types";

export interface BoardUrl {
  view: View;
  q: string;
  type: ItemType | "";
  status: ItemStatus | "";
  level: CardLevel;
  group: Grouping;
  item: string;
  req: string;
  filter: InboxFilter;
}

export const DEFAULT_URL: BoardUrl = {
  view: "hierarchy", q: "", type: "", status: "", level: "tasks", group: "root", item: "", req: "", filter: "all",
};

const VIEWS: readonly View[] = ["hierarchy", "kanban", "dependencies", "inbox"];
const TYPES: readonly ItemType[] = ["epic", "story", "task", "bug", "spike"];
const LEVELS: readonly CardLevel[] = ["tasks", "stories", "top"];
const GROUPS: readonly Grouping[] = ["root", "flat"];
const FILTERS: readonly InboxFilter[] = ["all", "questions", "approvals"];

function pick<T extends string>(allowed: readonly T[], v: string | null, fallback: T): T {
  return v !== null && (allowed as readonly string[]).includes(v) ? (v as T) : fallback;
}

export function parseHash(hash: string): BoardUrl {
  const raw = hash.replace(/^#\/?/, "");
  const [path = "", search = ""] = raw.split("?", 2);
  const p = new URLSearchParams(search);
  return {
    view: pick(VIEWS, path, DEFAULT_URL.view),
    q: p.get("q") ?? "",
    type: pick<ItemType | "">(TYPES, p.get("type"), ""),
    status: pick<ItemStatus | "">(ITEM_STATUSES, p.get("status"), ""),
    level: pick(LEVELS, p.get("level"), DEFAULT_URL.level),
    group: pick(GROUPS, p.get("group"), DEFAULT_URL.group),
    item: p.get("item") ?? "",
    req: p.get("req") ?? "",
    filter: pick(FILTERS, p.get("filter"), DEFAULT_URL.filter),
  };
}

const ORDER: (keyof BoardUrl)[] = ["q", "type", "status", "level", "group", "item", "req", "filter"];

export function formatHash(u: BoardUrl): string {
  const p = new URLSearchParams();
  for (const k of ORDER) if (u[k] !== DEFAULT_URL[k]) p.set(k, u[k]);
  const s = p.toString().replace(/\+/g, "%20");
  return `#/${u.view}${s ? `?${s}` : ""}`;
}

export const filterOf = (u: BoardUrl): Filter => ({ q: u.q, type: u.type, status: u.status });

function subscribe(cb: () => void) {
  window.addEventListener("hashchange", cb);
  return () => window.removeEventListener("hashchange", cb);
}
const snapshot = () => window.location.hash;

export function useBoardUrl(): [BoardUrl, (patch: Partial<BoardUrl>) => void] {
  const hash = useSyncExternalStore(subscribe, snapshot);
  const update = useCallback((patch: Partial<BoardUrl>) => {
    const next = formatHash({ ...parseHash(window.location.hash), ...patch });
    if (next === window.location.hash) return;
    window.history.replaceState(null, "", next);
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  }, []);
  return [parseHash(hash), update];
}

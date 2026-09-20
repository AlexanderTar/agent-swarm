import type { CreateItemBody, Item, ItemType } from "../types";
import { PARENT_TYPES as TREE_PARENT_TYPES, compareKeys } from "./tree";

export type NewType = "epic" | "bug" | "story" | "task";

// Deviation from the brief: logic/tree.ts already exports PARENT_TYPES for exactly this rule ("The
// mock daemon and later item-creation UIs both need this rule; export it once so nobody re-derives
// it" -- this sheet is that later UI). Derive from it instead of re-typing the story/task arrays a
// second time, so this can't drift from the table the daemon mirrors (same F20 ruling T21 applied to
// Hierarchy.tsx). epic/bug have no parent type at all, so they're the only literal entries here.
export const PARENT_TYPES: Record<NewType, ItemType[]> = {
  epic: [],
  bug: [],
  story: TREE_PARENT_TYPES.story ?? [],
  task: TREE_PARENT_TYPES.task ?? [],
};
export const TITLE_MAX = 200;

export interface NewItemForm { type: NewType; parentKey: string; title: string; brief: string; acceptance: string[] }

export const parentOptions = (items: Item[], type: NewType) =>
  items
    .filter((i) => PARENT_TYPES[type].includes(i.type) && i.status !== "cancelled")
    .sort((a, b) => compareKeys(a.key, b.key));

export function initialParent(items: Item[], type: NewType, hint?: string): string {
  if (!hint) return "";
  return parentOptions(items, type).some((i) => i.key === hint) ? hint : "";
}

export function canCreate(f: NewItemForm): boolean {
  const title = f.title.trim();
  if (title.length === 0 || title.length > TITLE_MAX) return false;
  return PARENT_TYPES[f.type].length === 0 || f.parentKey !== "";
}

export function newItemPayload(f: NewItemForm, requestId: string): CreateItemBody {
  const body: CreateItemBody = {
    request_id: requestId,
    type: f.type,
    title: f.title.trim(),
    brief: f.brief,
    acceptance: f.acceptance.map((a) => a.trim()).filter(Boolean),
  };
  return PARENT_TYPES[f.type].length > 0 ? { ...body, parent_key: f.parentKey } : body;
}

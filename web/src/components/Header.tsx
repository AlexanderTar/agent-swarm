import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { C, STATUS_LABEL, T, TYPE_LABEL, TYPE_PLURAL } from "../copy";
import { effectiveGrouping } from "../logic/kanban";
import type { BoardUrl } from "../state/url";
import { ITEM_STATUSES } from "../types";
import type { CardLevel, Grouping, ItemStatus, ItemType, View } from "../types";
import { Segmented } from "./Segmented";

const TYPES: ItemType[] = ["epic", "story", "task", "bug", "spike"];
const NEW_TYPES: ItemType[] = ["epic", "bug", "story", "task", "spike"];
const VIEWS: { value: View; label: string }[] = [
  { value: "hierarchy", label: C.hierarchy },
  { value: "kanban", label: C.kanban },
  { value: "dependencies", label: C.dependencies },
];
const selectCls = "rounded border border-line bg-canvas px-2 py-1";

export function Header(p: {
  url: BoardUrl;
  setUrl(patch: Partial<BoardUrl>): void;
  matches: number | null;
  needsYou: number;
  onNewSpike(): void;
  onNewItem(type: ItemType): void;
}) {
  const { url, setUrl } = p;
  const [menu, setMenu] = useState(false);
  // T15/T17 standing rule: transient UI takes focus on open and restores it on close.
  const newItemTrigger = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (menu) menuRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }, [menu]);
  const closeMenu = () => {
    setMenu(false);
    newItemTrigger.current?.focus();
  };
  const onMenuKeyDown = (e: KeyboardEvent) => {
    if (e.key !== "Escape") return;
    e.stopPropagation();
    closeMenu();
  };
  return (
    <header className="space-y-2 border-b border-line bg-panel px-4 py-3">
      <div className="flex items-center gap-3">
        <h1 className="text-base font-semibold">{C.appTitle}</h1>
        <button type="button" onClick={() => setUrl({ view: "inbox" })} className="ml-auto rounded bg-raised px-2 py-1">
          {T.needsYouButton(p.needsYou)}
        </button>
        <button type="button" onClick={p.onNewSpike} className="rounded border border-line px-2 py-1">{C.newSpike}</button>
        <div className="relative">
          <button
            ref={newItemTrigger}
            type="button"
            aria-haspopup="menu"
            aria-expanded={menu}
            onClick={() => setMenu((m) => !m)}
            className="rounded bg-accent px-2 py-1 text-white"
          >
            {C.newItem}
          </button>
          {menu && (
            <div
              ref={menuRef}
              role="menu"
              aria-label={C.newItem}
              tabIndex={-1}
              onKeyDown={onMenuKeyDown}
              className="absolute right-0 z-30 mt-1 w-32 rounded border border-line bg-panel p-1 shadow-lg"
            >
              {NEW_TYPES.map((t) => (
                <button
                  key={t}
                  type="button"
                  role="menuitem"
                  className="block w-full rounded px-2 py-1 text-left hover:bg-raised"
                  onClick={() => {
                    closeMenu();
                    p.onNewItem(t);
                  }}
                >
                  {TYPE_LABEL[t]}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <input
          type="search"
          aria-label={C.search}
          placeholder={C.search}
          value={url.q}
          onChange={(e) => setUrl({ q: e.target.value })}
          className={`${selectCls} w-64`}
        />
        <label className="flex items-center gap-1">
          {C.type}
          <select aria-label={C.type} value={url.type} onChange={(e) => setUrl({ type: e.target.value as ItemType | "" })} className={selectCls}>
            <option value="">{C.all}</option>
            {TYPES.map((t) => <option key={t} value={t}>{TYPE_PLURAL[t]}</option>)}
          </select>
        </label>
        <label className="flex items-center gap-1">
          {C.status}
          <select aria-label={C.status} value={url.status} onChange={(e) => setUrl({ status: e.target.value as ItemStatus | "" })} className={selectCls}>
            <option value="">{C.all}</option>
            {ITEM_STATUSES.map((s) => <option key={s} value={s}>{STATUS_LABEL[s]}</option>)}
          </select>
        </label>
        {p.matches !== null && (
          <>
            <button type="button" onClick={() => setUrl({ q: "", type: "", status: "" })} className="text-accent">{C.clearFilters}</button>
            <span className="ml-auto text-muted">{T.matches(p.matches)}</span>
          </>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Segmented label="View" value={url.view} options={VIEWS} onChange={(v) => setUrl({ view: v })} />
        {url.view === "kanban" && (
          <>
            <label className="flex items-center gap-1">
              {C.cardLevel}
              <select aria-label={C.cardLevel} value={url.level} onChange={(e) => setUrl({ level: e.target.value as CardLevel })} className={selectCls}>
                <option value="tasks">{C.tasks}</option>
                <option value="stories">{C.stories}</option>
                <option value="top">{C.topLevel}</option>
              </select>
            </label>
            <label className="flex items-center gap-1">
              {C.groupBy}
              <select
                aria-label={C.groupBy}
                value={effectiveGrouping(url.level, url.group)}
                disabled={url.level === "top"}
                onChange={(e) => setUrl({ group: e.target.value as Grouping })}
                className={selectCls}
              >
                <option value="root">{C.groupRoot}</option>
                <option value="flat">{C.flat}</option>
              </select>
            </label>
          </>
        )}
      </div>
    </header>
  );
}

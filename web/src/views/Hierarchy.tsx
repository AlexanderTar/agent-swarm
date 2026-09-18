import { ChevronDown, ChevronRight } from "lucide-react";
import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { Key, TypeIcon } from "../components/icons";
import { StatusPill } from "../components/StatusLabel";
import { C, T } from "../copy";
import { hierarchyRows, isFilterActive } from "../logic/tree";
import { useLocalSet } from "../state/local";
import type { HierarchyProps } from "./props";

export function Hierarchy(p: HierarchyProps) {
  const [collapsed, toggle] = useLocalSet("swarm.hierarchy.collapsed", []);
  const [menu, setMenu] = useState<{ key: string; type: "story" | "task" } | null>(null);
  const rows = hierarchyRows(p.items, p.filter, collapsed);
  const tree = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  // Standing rule: transient UI (the add-child context menu) takes focus on open and restores it
  // to its trigger on close. A context menu has no single trigger button, so "the trigger" here is
  // the tree itself — the single tab stop that owns keyboard navigation for every row.
  useEffect(() => {
    if (menu) menuRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }, [menu]);
  const closeMenu = () => {
    setMenu(null);
    tree.current?.focus();
  };

  if (!p.loaded) return null;
  if (p.items.length === 0) {
    return (
      <div className="p-8 text-center">
        <p>{C.noItems}</p>
        <button type="button" onClick={p.onNewItem} className="mt-2 text-accent">{C.newItem}</button>
      </div>
    );
  }
  if (rows.length === 0 && isFilterActive(p.filter)) {
    return (
      <div className="p-8 text-center">
        <p>{C.filteredNone}</p>
        <button type="button" onClick={p.onClearFilters} className="mt-2 text-accent">{C.clearFilters}</button>
      </div>
    );
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const idx = rows.findIndex((r) => r.item.key === p.selected);
    const cur = rows[idx];
    const go = (i: number) => {
      const r = rows[Math.max(0, Math.min(rows.length - 1, i))];
      if (r) p.onSelect(r.item.key);
    };
    if (e.key === "ArrowDown") go(idx + 1);
    else if (e.key === "ArrowUp") go(idx < 0 ? 0 : idx - 1);
    else if (e.key === "ArrowLeft" && cur?.hasChildren && cur.expanded && !cur.context) toggle(cur.item.key);
    else if (e.key === "ArrowRight" && cur?.hasChildren && !cur.expanded) toggle(cur.item.key);
    else if (e.key === "Enter" && cur) p.onSelect(cur.item.key);
    else return;
    e.preventDefault();
  };

  return (
    <div className="p-2">
      <div className="grid grid-cols-[1fr_140px_64px] px-2 py-1 text-[11px] uppercase tracking-wide text-muted">
        <span>WORK ITEM</span>
        <span>STATUS</span>
        <span className="text-right">AGENTS</span>
      </div>
      {/* biome-ignore lint/a11y/useSemanticElements: ARIA tree pattern */}
      <div ref={tree} role="tree" aria-label={C.hierarchy} tabIndex={0} onKeyDown={onKeyDown} className="outline-none">
        {rows.map((r) => {
          const it = r.item;
          const top = r.depth === 0;
          const childType = it.type === "epic" ? "story" : it.type === "story" || it.type === "bug" || it.type === "spike" ? "task" : null;
          return (
            <div
              key={it.key}
              role="treeitem"
              aria-label={`${it.key} ${it.title}`}
              aria-level={r.depth + 1}
              aria-expanded={r.hasChildren ? r.expanded : undefined}
              aria-selected={it.key === p.selected}
              data-key={it.key}
              data-context={r.context}
              onClick={() => p.onSelect(it.key)}
              onContextMenu={(e) => {
                e.preventDefault();
                setMenu(childType ? { key: it.key, type: childType } : null);
              }}
              className={`relative grid cursor-pointer grid-cols-[1fr_140px_64px] items-center rounded px-2 py-1 hover:bg-raised ${
                top ? "mt-3 font-semibold" : ""
              } ${r.context ? "opacity-50" : ""} ${it.key === p.selected ? "bg-raised" : ""}`}
            >
              <span className="flex min-w-0 items-center gap-1.5" style={{ paddingLeft: r.depth * 20 }}>
                {r.depth > 0 && <span aria-hidden className="absolute top-0 bottom-0 w-px bg-line" style={{ left: 8 + (r.depth - 1) * 20 + 10 }} />}
                {r.hasChildren ? (
                  <button
                    type="button"
                    aria-label={`${r.expanded ? "Collapse" : "Expand"} ${it.key}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggle(it.key);
                    }}
                  >
                    {r.expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                  </button>
                ) : (
                  <span className="w-3.5" />
                )}
                <TypeIcon type={it.type} />
                <Key>{it.key}</Key>
                <span className="truncate">{it.title}</span>
                {r.context && <span className="rounded bg-raised px-1 text-[11px] font-normal">{C.context}</span>}
              </span>
              <span><StatusPill status={it.status} /></span>
              <span className="flex items-center justify-end gap-1.5">
                {it.active_agents > 0 && (
                  <button
                    type="button"
                    title={T.agentsTooltip(it.active_agents)}
                    onClick={(e) => {
                      e.stopPropagation();
                      p.onSelect(it.key, "agents");
                    }}
                  >
                    {it.active_agents}
                  </button>
                )}
                {it.open_requests > 0 && <span role="img" aria-label={C.needsYou} className="size-2 rounded-full bg-warn" />}
              </span>
              {menu?.key === it.key && (
                <div
                  ref={menuRef}
                  role="menu"
                  tabIndex={-1}
                  onKeyDown={(e) => {
                    if (e.key !== "Escape") return;
                    e.stopPropagation();
                    closeMenu();
                  }}
                  className="absolute top-full left-8 z-30 rounded border border-line bg-panel p-1 font-normal shadow-lg"
                >
                  <button
                    type="button"
                    role="menuitem"
                    onClick={(e) => {
                      e.stopPropagation();
                      closeMenu();
                      p.onAddChild(it.key, menu.type);
                    }}
                    className="block rounded px-2 py-1 hover:bg-raised"
                  >
                    {menu.type === "story" ? C.addStory : C.addTask}
                  </button>
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

import {
  DndContext, type DragEndEvent, type DragStartEvent, KeyboardSensor, PointerSensor, useDraggable, useDroppable, useSensor, useSensors,
} from "@dnd-kit/core";
import { ChevronDown, ChevronRight, Lock } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { AgentIcon, Key, TypeIcon } from "../components/icons";
import { MoveToMenu } from "../components/MoveToMenu";
import { useToast } from "../components/Toast";
import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { displayState, stateLabel } from "../logic/agentActions";
import {
  type Lane, agentLine, agentsByItem, blockedLine, buildLanes, columnCounts, columnTitle, columnsFor, DEFAULT_COLLAPSED_COLUMNS,
  dropId, effectiveCollapsed, effectiveGrouping, emptyState, needsLine, parentLine, parseDropId, progressLine,
} from "../logic/kanban";
import { checkMove, failureMessage } from "../logic/transitions";
import { buildIndex, isFilterActive } from "../logic/tree";
import { readJson, storage, useLocalSet, writeJson } from "../state/local";
import type { AgentNode, Item, ItemStatus } from "../types";
import type { KanbanProps } from "./props";

const LANES_KEY = "swarm.kanban.lanes";
const SCROLL_KEY = "swarm.kanban.scroll";

function Card(p: {
  card: Item;
  pending: boolean;
  disabled: boolean;
  parent: string | null;
  agent: { agent: AgentNode; extra: number } | null;
  onSelect(): void;
  onMove(status: ItemStatus): void;
}) {
  const { card } = p;
  const drag = useDraggable({ id: card.key, disabled: p.disabled || p.pending });
  const style = drag.transform ? { transform: `translate(${drag.transform.x}px, ${drag.transform.y}px)` } : undefined;
  const needs = needsLine(card);
  const blocked = blockedLine(card);
  const progress = progressLine(card);
  return (
    <div
      ref={drag.setNodeRef}
      style={style}
      data-testid={`card-${card.key}`}
      {...drag.attributes}
      {...drag.listeners}
      aria-disabled={p.disabled || p.pending}
      onClick={p.onSelect}
      // F8 (preflight ruling): compose with dnd-kit's own keydown handler instead of replacing it —
      // a bare `onKeyDown` after `{...drag.listeners}` clobbers KeyboardSensor's Space-to-pick-up
      // entirely. Select on Enter only while this card isn't the one being keyboard-dragged, so a
      // drop (also Enter, per the KeyboardSensor config below) isn't immediately reinterpreted as a
      // card selection.
      onKeyDown={(e) => {
        drag.listeners?.onKeyDown?.(e);
        if (e.key === "Enter" && !drag.isDragging) p.onSelect();
      }}
      className="w-[256px] cursor-grab space-y-1 rounded-md border border-line bg-panel p-2 shadow-sm"
    >
      <div className="flex items-center gap-1.5">
        <TypeIcon type={card.type} />
        <Key>{card.key}</Key>
        {needs && <span className="ml-auto rounded bg-warn/15 px-1 text-[11px] text-warn">{needs}</span>}
        {/* biome-ignore lint/a11y/noStaticElementInteractions: stops card selection */}
        <span className={needs ? "" : "ml-auto"} onClick={(e) => e.stopPropagation()} onPointerDown={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
          <MoveToMenu item={card} buttonLabel="…" ariaLabel={`${C.moveTo} ${card.key}`} disabled={p.disabled || p.pending} onMove={(s) => p.onMove(s)} />
        </span>
      </div>
      <p className="line-clamp-3" title={card.title}>{card.title}</p>
      {p.parent && <p className="truncate text-muted">{p.parent}</p>}
      {p.agent && (
        <p className="flex items-center gap-1">
          <AgentIcon kind={p.agent.agent.kind} />
          <span className="truncate">{p.agent.agent.name}</span>
          {p.agent.extra > 0 && <span className="text-muted">+{p.agent.extra}</span>}
          <span className="ml-auto text-muted">{stateLabel(displayState(p.agent.agent))}</span>
        </p>
      )}
      {blocked && <p className="text-bad">{blocked}</p>}
      {progress && <p className="text-muted">{progress}</p>}
      {p.pending && <p className="text-accent">{C.updating}</p>}
    </div>
  );
}

function Cell(p: { id: string; testId: string; collapsed: boolean; lock: string | null; empty: boolean; children: ReactNode }) {
  // Locked cells stay droppable: the drop runs move(), which returns the card with the §17.3 toast.
  const drop = useDroppable({ id: p.id });
  if (p.collapsed) return <div data-testid={p.testId} className="w-[44px] shrink-0" />;
  return (
    <div
      ref={drop.setNodeRef}
      data-testid={p.testId}
      className={`w-[280px] shrink-0 space-y-2 p-3 ${drop.isOver ? "bg-raised" : ""}`}
    >
      {p.lock !== null && (
        <p className="flex items-center gap-1 text-[12px] text-muted"><Lock aria-hidden className="size-3" />{p.lock}</p>
      )}
      {p.empty && p.lock === null && <p className="text-muted">{C.emptyColumn}</p>}
      {p.children}
    </div>
  );
}

export function Kanban(p: KanbanProps) {
  const toast = useToast();
  const [storedCols, toggleCol] = useLocalSet("swarm.kanban.columns", DEFAULT_COLLAPSED_COLUMNS);
  const [laneState, setLaneState] = useState<Record<string, boolean>>(() => readJson(storage("localStorage"), LANES_KEY, {}));
  const [pending, setPending] = useState<Record<string, { to: ItemStatus; revision: number }>>({});
  const [dragging, setDragging] = useState<Item | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const patch = useMutation(
    (api, key: string, status: ItemStatus, revision: number) => api.patchItem(key, { status, revision }),
    ["items", "item:", "graph:"],
  );
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    // F8: Space alone starts a keyboard drag; Enter is freed up for card selection instead of also
    // starting one (§16.7: "Space to pick up, arrows to move, Enter to drop, Esc to cancel").
    useSensor(KeyboardSensor, { keyboardCodes: { start: ["Space"], cancel: ["Escape"], end: ["Space", "Enter"] } }),
  );

  const group = effectiveGrouping(p.level, p.group);
  const idx = useMemo(() => buildIndex(p.items), [p.items]);
  const byItem = useMemo(() => agentsByItem(p.agents), [p.agents]);
  const lanes = buildLanes(p.items, p.filter, p.level, p.group);
  const cols = columnsFor(p.level);
  const counts = columnCounts(p.items, p.filter, p.level);
  const collapsedCols = effectiveCollapsed(storedCols, p.filter);
  const filtered = isFilterActive(p.filter);
  const empty = emptyState(p.items, p.filter, p.level);

  // Drop pending markers once the refetched item's revision has moved past the one the PATCH was
  // sent against. Important #1 (T21-23 review): a concurrent change can land the item at a status
  // other than the one this card asked for — clearing on an exact status match left the marker (and
  // the disabled drag/Move-to controls it drives) stuck forever whenever that happened. A revision
  // bump is proof the PATCH round-trip settled, regardless of where it landed.
  useEffect(() => {
    setPending((cur) => {
      const next = { ...cur };
      for (const [k, v] of Object.entries(cur)) if (idx.byKey.get(k)?.revision !== v.revision) delete next[k];
      return Object.keys(next).length === Object.keys(cur).length ? cur : next;
    });
  }, [idx]);

  // F16 (T21-23 review): Kanban first mounts with `p.loaded === false` and returns null (see the
  // `if (!p.loaded) return null` guard below), so `scroller` is never attached to a DOM node on the
  // one run an empty-deps effect gets. Depend on `p.loaded` so this fires once the scroller actually
  // exists.
  useEffect(() => {
    const pos = readJson(storage("localStorage"), SCROLL_KEY, { left: 0, top: 0 });
    scroller.current?.scrollTo?.(pos.left, pos.top);
  }, [p.loaded]);

  async function move(card: Item, status: ItemStatus) {
    if (status === card.status) return;
    const check = checkMove(card, status);
    if (!check.ok && check.special === "accept") {
      const req = p.requests.find((r) => r.item_key === card.key && (r.kind === "accept_epic" || r.kind === "accept_fix"));
      if (req) p.onReview(req.id);
      else toast({ message: check.reason });
      return;
    }
    if (!check.ok && check.special === "spike") {
      toast({ message: check.reason, action: { label: C.viewSpike, onClick: () => p.onSelect(card.key) } });
      return;
    }
    if (!check.ok) {
      toast({ message: check.reason });
      return;
    }
    setPending((cur) => ({ ...cur, [card.key]: { to: status, revision: card.revision } }));
    try {
      await patch.run(card.key, status, card.revision);
    } catch (e) {
      setPending(({ [card.key]: _drop, ...rest }) => rest);
      toast({ message: failureMessage(e, card) });
    }
  }

  const onDragStart = (e: DragStartEvent) => setDragging(idx.byKey.get(String(e.active.id)) ?? null);
  const onDragEnd = (e: DragEndEvent) => {
    const card = dragging;
    setDragging(null);
    if (card && e.over) void move(card, parseDropId(String(e.over.id)).status);
  };

  if (!p.loaded) return null;
  if (empty) {
    const actions = {
      newItem: [C.newItem, p.onNewItem],
      clearFilters: [C.clearFilters, p.onClearFilters],
      showTopLevel: [C.showTopLevel, () => p.onLevel("top")],
    } as const;
    const a = empty.action ? actions[empty.action] : null;
    return (
      <div className="p-8 text-center">
        <p>{empty.text}</p>
        {a && <button type="button" onClick={a[1]} className="mt-2 text-accent">{a[0]}</button>}
      </div>
    );
  }

  const laneCollapsed = (l: Lane) => laneState[l.id] ?? l.defaultCollapsed;
  const toggleLane = (l: Lane) => {
    const next = { ...laneState, [l.id]: !laneCollapsed(l) };
    setLaneState(next);
    writeJson(storage("localStorage"), LANES_KEY, next);
  };
  const cardsIn = (l: Lane, s: ItemStatus) =>
    l.cards.filter((c) => (pending[c.key]?.to ?? c.status) === s);
  const lockFor = (s: ItemStatus): string | null => {
    if (!dragging || s === dragging.status) return null;
    const c = checkMove(dragging, s);
    return c.ok || c.special ? null : c.reason;
  };

  return (
    <DndContext sensors={sensors} onDragStart={onDragStart} onDragEnd={onDragEnd} onDragCancel={() => setDragging(null)}>
      {p.level === "stories" && <p className="px-3 pt-2 text-muted">{C.storiesCaption}</p>}
      <div
        ref={scroller}
        onScroll={(e) => writeJson(storage("localStorage"), SCROLL_KEY, { left: e.currentTarget.scrollLeft, top: e.currentTarget.scrollTop })}
        className="h-full overflow-auto"
      >
        <div className="sticky top-0 z-20 flex w-max border-b border-line bg-canvas">
          {cols.map((s) => {
            const t = columnTitle(s, counts.get(s) ?? { shown: 0, total: 0 }, filtered);
            const collapsed = collapsedCols.has(s);
            return (
              <button
                key={s}
                type="button"
                data-testid={`col-${s}`}
                data-collapsed={collapsed}
                title={t.tooltip}
                onClick={() => toggleCol(s)}
                className={`shrink-0 px-3 py-2 text-left font-medium ${collapsed ? "w-[44px] truncate" : "w-[280px]"}`}
              >
                {collapsed ? String(counts.get(s)?.shown ?? 0) : t.text}
                {collapsed && <span className="sr-only">{t.text}</span>}
              </button>
            );
          })}
        </div>
        {lanes.map((l) => {
          const collapsed = group === "root" && laneCollapsed(l);
          return (
            <section key={l.id} data-testid={`lane-${l.id}`} data-collapsed={collapsed} className="w-max border-b border-line">
              {group === "root" && (
                <button type="button" onClick={() => toggleLane(l)} className="sticky left-0 flex items-center gap-2 px-3 py-2 font-semibold">
                  {collapsed ? <ChevronRight className="size-3.5" /> : <ChevronDown className="size-3.5" />}
                  {l.root ? <><Key>{l.root.key}</Key><span>{l.root.title}</span></> : <span>{C.unassignedLane}</span>}
                  <span className="font-normal text-muted">{collapsed && l.completedText ? l.completedText : l.header}</span>
                </button>
              )}
              {!collapsed && (
                <div className="flex">
                  {cols.map((s) => {
                    const list = cardsIn(l, s);
                    return (
                      <Cell
                        key={s}
                        id={dropId(l.id, s)}
                        testId={`cell-${l.id}-${s}`}
                        collapsed={collapsedCols.has(s)}
                        lock={lockFor(s)}
                        empty={list.length === 0}
                      >
                        {list.map((c) => (
                          <Card
                            key={c.key}
                            card={c}
                            pending={pending[c.key] !== undefined}
                            disabled={!p.connected}
                            parent={parentLine(c, idx.byKey, group)}
                            agent={agentLine(c, byItem)}
                            onSelect={() => p.onSelect(c.key)}
                            onMove={(s2) => void move(c, s2)}
                          />
                        ))}
                      </Cell>
                    );
                  })}
                </div>
              )}
            </section>
          );
        })}
      </div>
    </DndContext>
  );
}

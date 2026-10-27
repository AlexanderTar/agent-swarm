import {
  DndContext,
  type DragEndEvent,
  type DragStartEvent,
  KeyboardSensor,
  PointerSensor,
  useDraggable,
  useDroppable,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import { ChevronDown, ChevronRight, Lock } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { MoveToMenu } from "../components/MoveToMenu";
import { StatusPill } from "../components/StatusLabel";
import { useToast } from "../components/Toast";
import { AgentIcon, Key, TypeIcon } from "../components/icons";
import { Button } from "../components/ui/button";
import { C, ROLE_EMOJI, STATUS_LABEL, T } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useNow } from "../data/useNow";
import { displayState, stateLabel, stateText } from "../logic/agentActions";
import {
  DEFAULT_COLLAPSED_COLUMNS,
  type Lane,
  agentLine,
  agentsByItem,
  blockedLine,
  buildLanes,
  columnCounts,
  columnTitle,
  columnsFor,
  crewForCard,
  dropId,
  effectiveCollapsed,
  effectiveGrouping,
  emptyState,
  needsLine,
  parentLine,
  parseDropId,
  progressLine,
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
  crew: ReturnType<typeof crewForCard>;
  onSelect(): void;
  onMove(status: ItemStatus): void;
}) {
  const { card } = p;
  const now = useNow();
  const drag = useDraggable({ id: card.key, disabled: p.disabled || p.pending });
  const style = drag.transform
    ? { transform: `translate(${drag.transform.x}px, ${drag.transform.y}px)` }
    : undefined;
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
      className="w-[256px] max-sm:w-full cursor-grab space-y-1 rounded-md border border-border bg-card p-2 shadow-sm"
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <TypeIcon type={card.type} />
        <span className="shrink-0 whitespace-nowrap">
          <Key>{card.key}</Key>
        </span>
        <StatusPill status={card.status} />
        {card.type === "task" && (card.workflow_state?.round ?? 0) > 1 && (
          <span className="rounded bg-primary/10 px-1 text-[11px] text-link">
            R{card.workflow_state?.round}
          </span>
        )}
        {needs && (
          <span className="ml-auto rounded bg-warning/15 px-1 text-[11px] text-warning">
            {needs}
          </span>
        )}
        <span
          className={needs ? "" : "ml-auto"}
          onClick={(e) => e.stopPropagation()}
          onPointerDown={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
        >
          <MoveToMenu
            item={card}
            buttonLabel="…"
            ariaLabel={`${C.moveTo} ${card.key}`}
            disabled={p.disabled || p.pending}
            onMove={(s) => p.onMove(s)}
          />
        </span>
      </div>
      <p className="line-clamp-3" title={card.title}>
        {card.title}
      </p>
      {p.parent && <p className="truncate text-muted-foreground">{p.parent}</p>}
      {p.agent && (
        <p className="flex items-center gap-1">
          <AgentIcon kind={p.agent.agent.kind} />
          <span className="min-w-0 flex-1 truncate">{p.agent.agent.name}</span>
          {p.agent.extra > 0 && <span className="text-muted-foreground">+{p.agent.extra}</span>}
          <span className="shrink-0 whitespace-nowrap text-muted-foreground">
            {displayState(p.agent.agent) === "busy"
              ? "Busy"
              : stateLabel(displayState(p.agent.agent))}
          </span>
        </p>
      )}
      {p.agent && displayState(p.agent.agent) === "busy" && (
        <p className="text-muted-foreground">{stateText(p.agent.agent, now, "line")}</p>
      )}
      {p.crew && (
        <div
          aria-label="Crew"
          className="flex items-center gap-0.5 text-xs"
          title="Active workflow crew"
        >
          {p.crew.roles.map((role, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a crew can list the same role twice and the list is static, so there is no stable id
            <span key={i} role="img" aria-label={role}>
              {ROLE_EMOJI[role]}
            </span>
          ))}
          {p.crew.extra > 0 && <span className="text-muted-foreground">+{p.crew.extra}</span>}
        </div>
      )}
      {blocked && <p className="text-destructive">{blocked}</p>}
      {progress && <p className="text-muted-foreground">{progress}</p>}
      {p.pending && <p className="text-link">{C.updating}</p>}
    </div>
  );
}

function Cell(p: {
  id: string;
  testId: string;
  collapsed: boolean;
  lock: string | null;
  empty: boolean;
  children: ReactNode;
}) {
  // Locked cells stay droppable: the drop runs move(), which returns the card with the §17.3 toast.
  const drop = useDroppable({ id: p.id });
  if (p.collapsed)
    return (
      <div
        data-testid={p.testId}
        className="w-[44px] shrink-0 max-sm:w-[132px] max-sm:snap-start"
      />
    );
  return (
    <div
      ref={drop.setNodeRef}
      data-testid={p.testId}
      className={`w-[280px] shrink-0 space-y-2 p-3 max-sm:w-[85vw] max-sm:snap-start ${drop.isOver ? "ring-1 ring-ring" : ""}`}
    >
      {p.lock !== null && (
        <p className="flex items-center gap-1 text-[12px] text-muted-foreground">
          <Lock aria-hidden className="size-3" />
          {p.lock}
        </p>
      )}
      {p.empty && p.lock === null && <p className="text-muted-foreground">{C.emptyColumn}</p>}
      {p.children}
    </div>
  );
}

// Touch uses the Move to… menu, so only mouse and pen pointers may start a drag; otherwise a swipe
// on a card can start one that sticks until reload.
const pointerDown =
  PointerSensor.activators[0] ??
  (() => {
    throw new Error("dnd-kit PointerSensor has no activators");
  })();
export class MousePenSensor extends PointerSensor {
  static activators = [
    {
      eventName: pointerDown.eventName,
      handler: (
        e: Parameters<typeof pointerDown.handler>[0],
        o: Parameters<typeof pointerDown.handler>[1],
      ) => e.nativeEvent.pointerType !== "touch" && pointerDown.handler(e, o),
    },
  ];
}

export function Kanban(p: KanbanProps) {
  const toast = useToast();
  const { live } = useConnection();
  const [storedCols, toggleCol] = useLocalSet("swarm.kanban.columns", DEFAULT_COLLAPSED_COLUMNS);
  const [laneState, setLaneState] = useState<Record<string, boolean>>(() =>
    readJson(storage("localStorage"), LANES_KEY, {}),
  );
  const [pending, setPending] = useState<Record<string, { to: ItemStatus; revision: number }>>({});
  const [dragging, setDragging] = useState<Item | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const patch = useMutation(
    (api, key: string, status: ItemStatus, revision: number) =>
      api.patchItem(key, { status, revision }),
    ["items", "item:", "graph:"],
  );
  const sensors = useSensors(
    useSensor(MousePenSensor, { activationConstraint: { distance: 4 } }),
    // F8: Space alone starts a keyboard drag; Enter is freed up for card selection instead of also
    // starting one (§16.7: "Space to pick up, arrows to move, Enter to drop, Esc to cancel").
    useSensor(KeyboardSensor, {
      keyboardCodes: { start: ["Space"], cancel: ["Escape"], end: ["Space", "Enter"] },
    }),
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
      for (const [k, v] of Object.entries(cur))
        if (idx.byKey.get(k)?.revision !== v.revision) delete next[k];
      return Object.keys(next).length === Object.keys(cur).length ? cur : next;
    });
  }, [idx]);

  // F16 (T21-23 review): Kanban first mounts with `p.loaded === false` and returns null (see the
  // `if (!p.loaded) return null` guard below), so `scroller` is never attached to a DOM node on the
  // one run an empty-deps effect gets. Depend on `p.loaded` so this fires once the scroller actually
  // exists.
  // biome-ignore lint/correctness/useExhaustiveDependencies: p.loaded re-runs the scroll restore once the scroller exists, the effect does not read it
  useEffect(() => {
    const pos = readJson(storage("localStorage"), SCROLL_KEY, { left: 0, top: 0 });
    scroller.current?.scrollTo?.(pos.left, pos.top);
  }, [p.loaded]);

  async function move(card: Item, status: ItemStatus) {
    if (!live.connected) return;
    if (status === card.status) return;
    const check = checkMove(card, status);
    if (!check.ok && check.special === "accept") {
      const req = p.requests.find(
        (r) => r.item_key === card.key && (r.kind === "accept_epic" || r.kind === "accept_fix"),
      );
      if (req) p.onReview(req.id);
      else toast({ message: check.reason });
      return;
    }
    if (!check.ok && check.special === "spike") {
      toast({
        message: check.reason,
        action: { label: C.viewSpike, onClick: () => p.onSelect(card.key) },
      });
      return;
    }
    if (!check.ok) {
      toast({ message: check.reason });
      return;
    }
    setPending((cur) => ({ ...cur, [card.key]: { to: status, revision: card.revision } }));
    try {
      await patch.run(card.key, status, card.revision);
      toast.success(T.toastMoved(card.key, STATUS_LABEL[status]));
    } catch (e) {
      setPending(({ [card.key]: _drop, ...rest }) => rest);
      toast({ message: failureMessage(e, card) });
    }
  }

  const onDragStart = (e: DragStartEvent) =>
    setDragging(idx.byKey.get(String(e.active.id)) ?? null);
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
        {a && (
          <Button type="button" variant="link" onClick={a[1]} className="mt-2">
            {a[0]}
          </Button>
        )}
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
    <DndContext
      sensors={sensors}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onDragCancel={() => setDragging(null)}
    >
      {p.level === "stories" && (
        <p className="px-3 pt-2 text-muted-foreground">{C.storiesCaption}</p>
      )}
      <div
        ref={scroller}
        onScroll={(e) =>
          writeJson(storage("localStorage"), SCROLL_KEY, {
            left: e.currentTarget.scrollLeft,
            top: e.currentTarget.scrollTop,
          })
        }
        className="h-full overflow-auto pl-[max(0px,env(safe-area-inset-left))] pr-[max(0px,env(safe-area-inset-right))] max-sm:snap-x max-sm:snap-mandatory"
      >
        <div className="max-md:w-max max-md:min-w-full">
          <div className="sticky top-0 z-20 flex w-max border-b border-border bg-background">
            {cols.map((s) => {
              const t = columnTitle(s, counts.get(s) ?? { shown: 0, total: 0 }, filtered);
              const collapsed = collapsedCols.has(s);
              return (
                <Button
                  key={s}
                  type="button"
                  variant="ghost"
                  data-testid={`col-${s}`}
                  data-collapsed={collapsed}
                  title={t.tooltip}
                  aria-label={
                    collapsed ? `${STATUS_LABEL[s]} ${counts.get(s)?.shown ?? 0}` : undefined
                  }
                  onClick={() => toggleCol(s)}
                  className={`h-auto shrink-0 justify-start rounded-none px-3 py-2 text-left text-xs font-medium uppercase tracking-wide text-muted-foreground ${collapsed ? "w-[44px] truncate max-sm:w-[132px]" : "w-[280px] max-sm:w-[85vw]"} max-sm:snap-start`}
                >
                  {collapsed && <span className="sr-only max-sm:hidden">{STATUS_LABEL[s]}</span>}
                  {collapsed ? (
                    <>
                      <span className="max-sm:hidden">{counts.get(s)?.shown ?? 0}</span>
                      <span className="hidden truncate max-sm:inline">
                        {STATUS_LABEL[s]} · {counts.get(s)?.shown ?? 0}
                      </span>
                    </>
                  ) : (
                    t.text
                  )}
                </Button>
              );
            })}
          </div>
          {lanes.map((l) => {
            const collapsed = group === "root" && laneCollapsed(l);
            return (
              <section
                key={l.id}
                data-testid={`lane-${l.id}`}
                data-collapsed={collapsed}
                className="w-max border-b border-border max-md:min-w-full"
              >
                {group === "root" && (
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => toggleLane(l)}
                    className="sticky left-0 h-auto justify-start gap-2 px-3 py-2 max-md:h-auto max-md:min-h-11 max-sm:max-w-[100vw] max-sm:flex-wrap max-sm:whitespace-normal max-sm:text-left"
                  >
                    {collapsed ? (
                      <ChevronRight className="size-3.5" />
                    ) : (
                      <ChevronDown className="size-3.5" />
                    )}
                    {l.root ? (
                      <>
                        <Key>{l.root.key}</Key>
                        <span className="font-semibold">{l.root.title}</span>
                      </>
                    ) : (
                      <span className="font-semibold">{C.unassignedLane}</span>
                    )}
                    <span className="font-normal text-muted-foreground">
                      {collapsed && l.completedText ? l.completedText : l.header}
                    </span>
                  </Button>
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
                              crew={crewForCard(c, byItem)}
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
      </div>
    </DndContext>
  );
}

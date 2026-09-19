import {
  Background, Handle, type Node, type NodeProps, Position, ReactFlow, ReactFlowProvider, useReactFlow,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useEffect, useMemo, useRef, useState } from "react";
import { errorText } from "../api";
import { Key } from "../components/icons";
import { Segmented } from "../components/Segmented";
import { StatusPill } from "../components/StatusLabel";
import { C } from "../copy";
import { useGraph } from "../data/queries";
import { type ItemNodeData, LayoutCache, type StoryNodeData, dimmedKeys, storyOfFn, toFlow } from "../logic/graphLayout";
import { buildIndex } from "../logic/tree";
import type { GraphNode, GraphScope } from "../types";
import type { DependenciesProps } from "./props";

function ItemNode({ data }: NodeProps<Node<ItemNodeData>>) {
  const n = data.node;
  return (
    <div
      data-testid={`node-${n.key}`}
      data-dimmed={data.dimmed}
      data-external={n.external}
      className={`h-full rounded-md border bg-panel p-2 ${n.external ? "border-dashed" : ""} ${data.dimmed ? "opacity-35" : ""}`}
    >
      <Handle type="target" position={Position.Left} />
      <div className="flex items-center gap-1">
        <Key>{n.key}</Key>
        {n.external && <span className="text-[11px] text-muted">{n.root_key}</span>}
        {data.needsYou && <span role="img" aria-label={C.needsYou} className="ml-auto size-2 rounded-full bg-warn" />}
      </div>
      <p className="line-clamp-2 text-[12px]">{n.title}</p>
      <StatusPill status={n.status} />
      <Handle type="source" position={Position.Right} />
    </div>
  );
}

const StoryNode = ({ data }: NodeProps<Node<StoryNodeData>>) => (
  <div className="h-full rounded-lg border border-line bg-raised/40 px-2 py-1 text-[12px] text-muted">{data.label}</div>
);

const nodeTypes = { item: ItemNode, story: StoryNode };

function Graph(p: DependenciesProps) {
  const rf = useReactFlow();
  const [scope, setScope] = useState<GraphScope>("root");
  const [hops, setHops] = useState(1);
  const [picked, setPicked] = useState<GraphNode | null>(null);
  const [find, setFind] = useState("");
  const cache = useRef(new LayoutCache());
  const graph = useGraph(p.selected || null, scope, hops);
  const idx = useMemo(() => buildIndex(p.items), [p.items]);

  useEffect(() => {
    if (!p.selected && idx.roots[0]) p.onSelect(idx.roots[0].key);
  }, [p.selected, idx, p.onSelect]);
  useEffect(() => setPicked(null), [p.selected]);

  const g = graph.data;
  const flow = useMemo(() => {
    if (!g) return null;
    const rootKey = idx.byKey.get(p.selected)?.root_key ?? p.selected;
    const cacheKey = scope === "root" ? `${rootKey}:root` : `${p.selected}:neighbourhood:${hops}`;
    const storyOf = storyOfFn(idx.byKey, scope);
    const laid = cache.current.get(cacheKey, g, storyOf);
    const needsYou = new Set(p.items.filter((i) => i.open_requests > 0).map((i) => i.key));
    return toFlow(g, laid, {
      selected: p.selected,
      dimmed: dimmedKeys(g, p.filter),
      needsYou,
      storyLabel: (k) => `${k} ${idx.byKey.get(k)?.title ?? ""}`,
    });
  }, [g, idx, p.items, p.selected, p.filter, scope, hops]);

  const touching = g?.edges.some((e) => e.from === p.selected || e.to === p.selected) ?? false;

  const onFind = () => {
    const q = find.trim().toLowerCase();
    const hit = g?.nodes.find((n) => n.key.toLowerCase().includes(q) || n.title.toLowerCase().includes(q));
    if (!hit) return;
    p.onSelect(hit.key);
    const n = rf.getNode(hit.key);
    if (n) void rf.setCenter(n.position.x, n.position.y, { duration: 200 });
  };

  // Standing rule: a failed query gets a message and a retry, never a permanent (here: silently
  // blank) placeholder — the brief's version never reads `graph.error`, so a failed load would show
  // an empty pane forever with no indication anything went wrong. Checked after every hook above, so
  // this early return never changes the hook call order between renders.
  if (graph.error) {
    return (
      <div className="p-8 text-center">
        <p className="text-bad">{errorText(graph.error)}</p>
        <button type="button" onClick={graph.reload} className="mt-2 text-accent">{C.retry}</button>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
        <span>{C.scope}</span>
        <Segmented
          label={C.scope}
          value={scope}
          onChange={(s) => { setScope(s); setHops(1); }}
          options={[{ value: "root", label: C.root }, { value: "neighbourhood", label: C.neighbourhood }]}
        />
        <button type="button" disabled={scope !== "neighbourhood"} onClick={() => setHops((h) => h + 1)} className="text-accent disabled:text-muted">{C.expandHop}</button>
        <button type="button" onClick={() => void rf.fitView()}>{C.fit}</button>
        <button type="button" aria-label="−" onClick={() => void rf.zoomOut()}>−</button>
        <button type="button" aria-label="+" onClick={() => void rf.zoomIn()}>+</button>
        <button type="button" onClick={() => { setHops(1); void rf.fitView(); }}>{C.reset}</button>
        <input
          type="search"
          placeholder={C.find}
          aria-label={C.find}
          value={find}
          onChange={(e) => setFind(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && onFind()}
          className="rounded border border-line bg-canvas px-2 py-0.5"
        />
        {picked && (
          <button type="button" onClick={() => p.onSelect(picked.root_key)} className="text-accent">{C.openRoot}</button>
        )}
      </div>
      <p className="px-3 py-1 text-muted">{C.depLegend}</p>
      {g && !touching ? (
        <p className="p-8 text-center">{C.noDeps}</p>
      ) : (
        <div className="min-h-[400px] flex-1">
          {flow && (
            <ReactFlow
              nodes={flow.nodes}
              edges={flow.edges}
              nodeTypes={nodeTypes}
              nodesDraggable={false}
              nodesConnectable={false}
              fitView
              onNodeClick={(_, n) => {
                if (n.type !== "item") return;
                const data = n.data as ItemNodeData;
                if (data.node.external) setPicked(data.node);
                else p.onSelect(n.id);
              }}
            >
              <Background />
            </ReactFlow>
          )}
        </div>
      )}
    </div>
  );
}

export function Dependencies(p: DependenciesProps) {
  if (!p.loaded) return null;
  return (
    <ReactFlowProvider>
      <Graph {...p} />
    </ReactFlowProvider>
  );
}

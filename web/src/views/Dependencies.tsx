import {
  Background, Handle, type Node, type NodeProps, Position, ReactFlow, ReactFlowProvider, useReactFlow,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { useEffect, useMemo, useRef, useState } from "react";
import { errorText } from "../api";
import { Key } from "../components/icons";
import { Segmented } from "../components/Segmented";
import { StatusPill } from "../components/StatusLabel";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
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
      className={`h-full rounded-md border border-border bg-card p-2 text-foreground ${n.external ? "border-dashed" : ""} ${data.dimmed ? "opacity-35" : ""}`}
    >
      <Handle type="target" position={Position.Left} />
      <div className="flex items-center gap-1">
        <Key>{n.key}</Key>
        {n.external && <span className="text-[11px] text-muted-foreground">{n.root_key}</span>}
        {data.needsYou && <span role="img" aria-label={C.needsYou} className="ml-auto size-2 rounded-full bg-warning" />}
      </div>
      <p className="line-clamp-2 text-[12px]">{n.title}</p>
      <StatusPill status={n.status} />
      <Handle type="source" position={Position.Right} />
    </div>
  );
}

const StoryNode = ({ data }: NodeProps<Node<StoryNodeData>>) => (
  <div className="h-full rounded-lg border border-border bg-muted/40 px-2 py-1 text-[12px] text-muted-foreground">{data.label}</div>
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
        <p className="text-destructive">{errorText(graph.error)}</p>
        <Button type="button" variant="link" onClick={graph.reload} className="mt-2">{C.retry}</Button>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        <span>{C.scope}</span>
        <Segmented
          label={C.scope}
          value={scope}
          onChange={(s) => { setScope(s); setHops(1); }}
          options={[{ value: "root", label: C.root }, { value: "neighbourhood", label: C.neighbourhood }]}
        />
        <label className="flex items-center gap-1.5">{C.hops}
          <Select value={String(hops)} disabled={scope !== "neighbourhood"} onValueChange={(v) => setHops(Number(v))}>
            <SelectTrigger aria-label={C.hops}><SelectValue /></SelectTrigger>
            <SelectContent>{[1, 2, 3].map((n) => <SelectItem key={n} value={String(n)}>{n}</SelectItem>)}</SelectContent>
          </Select>
        </label>
        <Button type="button" variant="outline" onClick={() => void rf.fitView()}>{C.fit}</Button>
        <Button type="button" variant="outline" aria-label="−" onClick={() => void rf.zoomOut()}>−</Button>
        <Button type="button" variant="outline" aria-label="+" onClick={() => void rf.zoomIn()}>+</Button>
        <Button type="button" variant="outline" onClick={() => { setHops(1); void rf.fitView(); }}>{C.reset}</Button>
        <Input
          type="search"
          placeholder={C.find}
          aria-label={C.find}
          value={find}
          onChange={(e) => setFind(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && onFind()}
          className="h-8 w-40"
        />
        {picked && (
          <Button type="button" variant="link" onClick={() => p.onSelect(picked.root_key)}>{C.openRoot}</Button>
        )}
      </div>
      <p className="px-3 py-1 text-muted-foreground">{C.depLegend}</p>
      {g && !touching ? (
        <p className="p-8 text-center">{C.noDeps}</p>
      ) : (
        <div className="min-h-[400px] flex-1">
          {flow && (
            <ReactFlow
              nodes={flow.nodes}
              edges={flow.edges.map((e) => ({ ...e, style: { stroke: "var(--border)" } }))}
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

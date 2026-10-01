import { useEffect, useRef, useState } from "react";
import { ApiError, errorText } from "../api";
import { AddDependency } from "../components/AddDependency";
import { AgentList } from "../components/AgentRow";
import { ArtifactViewer } from "../components/ArtifactViewer";
import { CheckpointList } from "../components/CheckpointList";
import { MergeList } from "../components/MergeList";
import { MoveToMenu } from "../components/MoveToMenu";
import { TodoList } from "../components/TodoList";
import { useToast } from "../components/Toast";
import { WorkflowSection } from "../components/WorkflowSection";
import { Alert } from "../components/ui/alert";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { Textarea } from "../components/ui/textarea";
import { C, STATUS_LABEL, T, TYPE_LABEL } from "../copy";
import { useConnection, useInvalidate, useMutation } from "../data/hooks";
import { qk, useItemDetail } from "../data/queries";
import { displayState, flattenAgents, isFinished } from "../logic/agentActions";
import { agentActionToast } from "../logic/toasts";
import { ARTIFACT_LABEL, requestTitle } from "../logic/requestTitle";
import { checkMove, failureMessage } from "../logic/transitions";
import type { Item, ItemStatus, PatchItemBody, Priority } from "../types";
import type { DetailsProps } from "../views/props";

function Editable(p: { label: string; value: string; multiline?: boolean; maxLength?: number; disabled: boolean; onSave(v: string): Promise<boolean>; className?: string }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(p.value);
  const trigger = useRef<HTMLButtonElement>(null);
  // Tracks the editing state as of the last render, so the focus effect below can tell "just closed"
  // apart from "freshly mounted" (both look like `editing === false`).
  const wasEditing = useRef(editing);
  const save = async () => {
    if (draft === p.value) { setEditing(false); return; }
    // Keep the editor open (and the typed draft) until the save actually settles. A failed PATCH
    // must not discard what the user typed or fall back to the server's stale copy — only a
    // success closes the editor.
    if (await p.onSave(draft)) setEditing(false);
  };
  // Standing rule: this inline editor is transient UI too — closing it (blur, Enter, Escape) drops
  // focus to <body> unless something claims it back. Restore it to the trigger button, same as a
  // menu/dialog would. Only on the true editing->closed transition, not on mount (which would
  // needlessly steal focus every time this panel opens). Deferred one tick: closing via the Enter
  // key is still mid-dispatch of that same keydown when this effect runs, and focusing the button
  // synchronously here means the still-in-flight native keyup for Enter lands on an already-focused
  // button, which the browser treats as "activate it" -- reopening the editor it just closed.
  useEffect(() => {
    const shouldFocus = wasEditing.current && !editing;
    wasEditing.current = editing;
    if (!shouldFocus) return undefined;
    const id = setTimeout(() => trigger.current?.focus());
    return () => clearTimeout(id);
  }, [editing]);
  if (!editing) {
    return (
      <Button
        ref={trigger}
        type="button"
        variant="ghost"
        aria-label={p.multiline ? p.label : undefined}
        disabled={p.disabled}
        onClick={() => { setDraft(p.value); setEditing(true); }}
        className={`h-auto w-full justify-start whitespace-pre-wrap p-1 text-left ${p.className ?? ""}`}
      >
        {p.value || <span className="text-muted-foreground">—</span>}
      </Button>
    );
  }
  const common = {
    "aria-label": p.label,
    autoFocus: true,
    value: draft,
    disabled: p.disabled,
    maxLength: p.maxLength,
    onBlur: save,
  };
  return p.multiline ? (
    <Textarea {...common} rows={4} onChange={(e) => setDraft(e.target.value)} />
  ) : (
    <Input {...common} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => e.key === "Enter" && save()} />
  );
}

export function Details(p: DetailsProps) {
  const detail = useItemDetail(p.itemKey);
  const invalidate = useInvalidate();
  const toast = useToast();
  const { live } = useConnection();
  const [tab, setTab] = useState<"overview" | "agents" | "checkpoints" | "deps">("overview");
  const pendingAgentScroll = useRef<string | null>(null);
  const agentsNode = useRef<HTMLElement | null>(null);
  const [stale, setStale] = useState(false);
  const [viewing, setViewing] = useState<{ id: string; revision: number } | null>(null);
  const patch = useMutation((api, key: string, body: PatchItemBody) => api.patchItem(key, body), ["items", "item:", "graph:"]);
  const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));

  useEffect(() => {
    if (p.focus !== "agents") return;
    pendingAgentScroll.current = "";
    setTab("agents");
    if (agentsNode.current) {
      agentsNode.current.scrollIntoView?.({ block: "start" });
      pendingAgentScroll.current = null;
    }
  }, [p.focus, p.itemKey]);

  const setAgentsRef = (node: HTMLElement | null) => {
    agentsNode.current = node;
    if (!node || pendingAgentScroll.current === null) return;
    if (pendingAgentScroll.current) node.querySelector<HTMLElement>(`#agent-${pendingAgentScroll.current}`)?.scrollIntoView?.({ block: "center" });
    else node.scrollIntoView?.({ block: "start" });
    pendingAgentScroll.current = null;
  };

  const d = detail.data;
  // Standing rule: a failed load gets a message + retry, never a permanent "…" placeholder.
  if (detail.error) {
    return (
      <Alert variant="destructive" className="m-4">
        {errorText(detail.error)}{" "}
        <Button type="button" variant="link" className="h-auto p-0" onClick={() => detail.reload()}>
          {C.retry}
        </Button>
      </Alert>
    );
  }
  if (!d) return <div className="space-y-3 p-4" aria-label={C.openDetails}>{[1, 2, 3].map((n) => <div key={n} className="h-3 animate-pulse rounded bg-muted motion-reduce:animate-none" />)}</div>;
  const item = d.item;

  const save = async (body: Omit<PatchItemBody, "revision">): Promise<boolean> => {
    try {
      await patch.run(item.key, { ...body, revision: item.revision });
      setStale(false);
      if (body.status) toast.success(T.toastMoved(item.key, STATUS_LABEL[body.status]));
      return true;
    } catch (e) {
      if (e instanceof ApiError && e.code === "conflict") {
        setStale(true);
        invalidate([qk.item(item.key), "items"]);
      }
      toast.error(body.status ? failureMessage(e, item) : errorText(e));
      return false;
    }
  };

  const onMove = (status: ItemStatus) => {
    if (!live.connected) return;
    const check = checkMove(item, status);
    if (!check.ok && check.special === "accept") {
      const req = d.requests.find((r) => r.kind === "accept_epic" || r.kind === "accept_fix");
      if (req) p.onReview(req.id);
      else toast({ message: check.reason });
      return;
    }
    if (!check.ok) {
      toast({ message: check.reason });
      return;
    }
    void save({ status });
  };

  const topLevel = item.parent_key === null;
  const open = item.status !== "done" && item.status !== "cancelled";
  const orchestrator = flattenAgents(d.agents).find(
    (a) => a.role === "orchestrator" && a.item_key === item.key && (a.state === "active" || a.state === "queued"),
  );
  const crumbs = [...d.ancestors.map((a) => a.key), item.key].join(" › ");

  return (
    <div data-testid="details-panel" className="space-y-4">
      {stale && <p className="rounded bg-warning/10 px-2 py-1 text-warning">{C.staleRevision}</p>}
      <div className="flex items-start justify-between gap-2 pr-10">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="key text-muted-foreground">{crumbs}</span>
          <Badge variant="outline">{TYPE_LABEL[item.type]}</Badge>
        </div>
        <MoveToMenu item={item} buttonLabel={STATUS_LABEL[item.status]} disabled={!p.connected || patch.pending} onMove={onMove} />
      </div>
      {(item.waivers?.length || item.override) && (
        <ul className="space-y-0.5 text-warning/80">
          {item.waivers?.map((w) => <li key={w.gate}>{C.waivedRow(w.gate, w.reason)}</li>)}
          {item.override && <li>{C.overrideRow(STATUS_LABEL[item.override.status], item.override.reason)}</li>}
        </ul>
      )}
      <Editable
        label={C.title}
        value={item.title}
        maxLength={200}
        disabled={!p.connected || patch.pending}
        onSave={(title) => save({ title })}
        className="text-base font-semibold"
      />
      <div className="flex items-center justify-between gap-2">
        <label className="flex items-center gap-1">
          {C.priority}
          <Select value={String(item.priority)} disabled={!p.connected || patch.pending} onValueChange={(value) => void save({ priority: Number(value) as Priority })}>
            <SelectTrigger aria-label={C.priority} size="sm"><SelectValue /></SelectTrigger>
            <SelectContent>{[0, 1, 2, 3].map((n) => <SelectItem key={n} value={String(n)}>{`P${n}`}</SelectItem>)}</SelectContent>
          </Select>
        </label>
      </div>

      {d.merges?.length && (item.status === "in_review" || d.merges.some((m) => m.kind === "kept")) ? <MergeList merges={d.merges} /> : null}
      {item.status === "in_review" && d.merges?.length === 0 && (
        <p className="border-t border-border pt-3 text-muted-foreground">{C.awaitingOrchestrator}</p>
      )}
      {d.todos && <TodoList todos={d.todos} onSelect={p.onSelect} />}

      <Tabs value={tab} onValueChange={(value) => setTab(value as typeof tab)}>
        <TabsList variant="line" className="w-full justify-start overflow-x-auto">
          <TabsTrigger value="overview">{C.overview}</TabsTrigger>
          <TabsTrigger value="agents">{C.agents}</TabsTrigger>
          <TabsTrigger value="checkpoints">{C.checkpoints}</TabsTrigger>
          <TabsTrigger value="deps">{C.deps}</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="space-y-3">
          <div>
            <h4 className="text-muted-foreground">{C.brief}</h4>
            <Editable label={C.brief} value={item.brief} multiline disabled={!p.connected || patch.pending} onSave={(brief) => save({ brief })} />
          </div>
          {item.acceptance.length > 0 && (
            <div>
              <h4 className="text-muted-foreground">{C.acceptance}</h4>
              <ul className="list-disc pl-5">{item.acceptance.map((a) => <li key={a}>{a}</li>)}</ul>
            </div>
          )}
          <WorkflowSection workflow={item.workflow} state={d.workflow_state} connected={p.connected} onOpenTerminal={(name) => {
            if (!live.connected) return;
            const epoch = live.epoch;
            void terminal.run(name).then(() => { if (live.connected && live.epoch === epoch) toast.success(agentActionToast("terminal", name)); })
              .catch((e: unknown) => { if (live.connected && live.epoch === epoch) toast({ message: errorText(e) }); });
          }} />
          <section aria-label={C.agents} className="border-t border-border pt-3">
            <h3 className="mb-1 font-semibold">{C.agents}</h3>
            <ul className="space-y-1 text-sm">
              {flattenAgents(d.agents).filter((agent) => !isFinished(agent)).map((agent) => (
                <li key={agent.id} className="flex justify-between gap-2">
                  <span className="truncate">{agent.name}</span>
                  <span className="shrink-0 text-muted-foreground">{displayState(agent)}</span>
                </li>
              ))}
            </ul>
          </section>
          {d.artifacts.length > 0 && (
            <div>
              <h4 className="text-muted-foreground">{C.artifacts}</h4>
              {d.artifacts.map((a) => (
                <Button key={a.id} type="button" variant="link" onClick={() => setViewing({ id: a.id, revision: a.head_revision })} className="block h-auto p-0">
                  {`${ARTIFACT_LABEL[a.kind]} · rev ${a.head_revision} · ${C.view}`}
                </Button>
              ))}
            </div>
          )}
          {item.origin_spike_key && (
            <Button type="button" variant="link" onClick={() => p.onSelect(item.origin_spike_key ?? "")} className="h-auto p-0">
              {T.startedFrom(item.origin_spike_key)}
            </Button>
          )}
          {d.requests.length > 0 && (
            <section aria-label={C.needsYou} className="border-t border-border pt-3">
              <h3 className="mb-1 font-semibold">{C.needsYou}</h3>
              <ul className="space-y-1">
                {[...d.requests].sort((a, b) => a.created_at - b.created_at).map((r) => (
                  <li key={r.id} className="flex items-center justify-between gap-2">
                    <span className="truncate">{requestTitle(r)}</span>
                    <Button type="button" variant="link" onClick={() => p.onReview(r.id)} className="h-auto p-0">{C.review}</Button>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </TabsContent>
        <TabsContent value="agents">
          <section ref={setAgentsRef} aria-label={C.agents} className="pt-2">
            <AgentList agents={d.agents} />
          </section>
        </TabsContent>
        <TabsContent value="checkpoints">
          <CheckpointList itemKey={item.key} agentNames={flattenAgents(d.agents).map((a) => a.name)} />
        </TabsContent>
        <TabsContent value="deps" className="space-y-3">
          {d.deps.blocked_by.length > 0 && (
            <p>
              {`${C.blockedBy}: `}
              {d.deps.blocked_by.map((b: Item, i) => (
                <span key={b.key}>{i > 0 && ", "}<Button type="button" variant="link" onClick={() => p.onSelect(b.key)} className="h-auto p-0">{`${b.key} (${STATUS_LABEL[b.status]})`}</Button></span>
              ))}
            </p>
          )}
          {d.deps.blocks.length > 0 && (
            <p>
              {`${C.blocks}: `}
              {d.deps.blocks.map((b: Item, i) => (
                <span key={b.key}>{i > 0 && ", "}<Button type="button" variant="link" onClick={() => p.onSelect(b.key)} className="h-auto p-0">{b.key}</Button></span>
              ))}
            </p>
          )}
          <AddDependency itemKey={item.key} disabled={!p.connected} />
        </TabsContent>
      </Tabs>

      {topLevel && open && (
        <div className="border-t border-border pt-3">
          {orchestrator ? (
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                if (tab === "agents") document.getElementById(`agent-${orchestrator.name}`)?.scrollIntoView?.({ block: "center" });
                else { pendingAgentScroll.current = orchestrator.name; setTab("agents"); }
              }}
            >
              {C.viewOrchestrator}
            </Button>
          ) : (
            <Button type="button" disabled={!p.connected} onClick={() => p.onStartOrchestrator(item)}>
              {C.startOrchestrator}
            </Button>
          )}
        </div>
      )}

      {viewing && <ArtifactViewer artifactId={viewing.id} revision={viewing.revision} onClose={() => setViewing(null)} />}
    </div>
  );
}

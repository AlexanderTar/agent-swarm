import { useEffect, useRef, useState } from "react";
import { ApiError, errorText } from "../api";
import { AddDependency } from "../components/AddDependency";
import { AgentList } from "../components/AgentRow";
import { ArtifactViewer } from "../components/ArtifactViewer";
import { CheckpointList } from "../components/CheckpointList";
import { MoveToMenu } from "../components/MoveToMenu";
import { useToast } from "../components/Toast";
import { WorkflowSection } from "../components/WorkflowSection";
import { Alert } from "../components/ui/alert";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "../components/ui/tabs";
import { C, STATUS_LABEL, T } from "../copy";
import { useInvalidate, useMutation } from "../data/hooks";
import { qk, useItemDetail } from "../data/queries";
import { flattenAgents } from "../logic/agentActions";
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
      <button
        ref={trigger}
        type="button"
        aria-label={p.multiline ? p.label : undefined}
        disabled={p.disabled}
        onClick={() => { setDraft(p.value); setEditing(true); }}
        className={`block w-full whitespace-pre-wrap text-left ${p.className ?? ""}`}
      >
        {p.value || <span className="text-muted-foreground">—</span>}
      </button>
    );
  }
  const common = {
    "aria-label": p.label,
    autoFocus: true,
    value: draft,
    maxLength: p.maxLength,
    onBlur: save,
    className: "w-full rounded border border-line bg-canvas px-2 py-1",
  };
  return p.multiline ? (
    <textarea {...common} rows={4} onChange={(e) => setDraft(e.target.value)} />
  ) : (
    <input {...common} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => e.key === "Enter" && save()} />
  );
}

export function Details(p: DetailsProps) {
  const detail = useItemDetail(p.itemKey);
  const invalidate = useInvalidate();
  const toast = useToast();
  const [tab, setTab] = useState<"overview" | "checkpoints">("overview");
  const [stale, setStale] = useState(false);
  const [viewing, setViewing] = useState<{ id: string; revision: number } | null>(null);
  const agentsRef = useRef<HTMLElement>(null);
  const patch = useMutation((api, key: string, body: PatchItemBody) => api.patchItem(key, body), ["items", "item:", "graph:"]);
  const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));

  useEffect(() => {
    if (p.focus === "agents" && detail.data) agentsRef.current?.scrollIntoView?.({ block: "start" });
  }, [p.focus, detail.data]);

  const d = detail.data;
  // Standing rule: a failed load gets a message + retry, never a permanent "…" placeholder.
  if (detail.error) {
    return (
      <Alert variant="destructive" className="m-4">
        {errorText(detail.error)}{" "}
        <button type="button" className="text-link underline" onClick={() => detail.reload()}>
          {C.retry}
        </button>
      </Alert>
    );
  }
  if (!d) return <div className="space-y-3 p-4" aria-label={C.openDetails}>{[1, 2, 3].map((n) => <div key={n} className="h-3 animate-pulse rounded bg-muted" />)}</div>;
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
      } else toast({ message: body.status ? failureMessage(e, item) : errorText(e) });
      return false;
    }
  };

  const onMove = (status: ItemStatus) => {
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
    <div data-testid="details-panel" className="space-y-4 p-4">
      {stale && <p className="rounded bg-warn/10 px-2 py-1 text-warn">{C.staleRevision}</p>}
      <div className="flex items-start justify-between gap-2 pr-10">
        <span className="key text-muted-foreground">{crumbs}</span>
      </div>
      <Editable
        label={C.title}
        value={item.title}
        maxLength={200}
        disabled={!p.connected || patch.pending}
        onSave={(title) => save({ title })}
        className="text-base font-semibold"
      />
      <div className="flex items-center justify-between gap-2">
        <MoveToMenu item={item} buttonLabel={STATUS_LABEL[item.status]} disabled={!p.connected || patch.pending} onMove={onMove} />
        <label className="flex items-center gap-1">
          {C.priority}
          <Select value={String(item.priority)} disabled={!p.connected || patch.pending} onValueChange={(value) => void save({ priority: Number(value) as Priority })}>
            <SelectTrigger aria-label={C.priority} size="sm"><SelectValue /></SelectTrigger>
            <SelectContent>{[0, 1, 2, 3].map((n) => <SelectItem key={n} value={String(n)}>{`P${n}`}</SelectItem>)}</SelectContent>
          </Select>
        </label>
      </div>

      {d.requests.length > 0 && (
        <section aria-label={C.needsYou} className="border-t border-line pt-3">
          <h3 className="mb-1 font-semibold">{C.needsYou}</h3>
          <ul className="space-y-1">
            {[...d.requests].sort((a, b) => a.created_at - b.created_at).map((r) => (
              <li key={r.id} className="flex items-center justify-between gap-2">
                <span className="truncate">{requestTitle(r)}</span>
                <button type="button" onClick={() => p.onReview(r.id)} className="text-link">{C.review}</button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section ref={agentsRef} aria-label={C.agents} className="border-t border-line pt-3">
        <h3 className="mb-1 font-semibold">{C.agents}</h3>
        <AgentList agents={d.agents} />
      </section>

      <WorkflowSection workflow={item.workflow} state={d.workflow_state} onOpenTerminal={(name) => {
        void terminal.run(name).then(() => toast.success(agentActionToast("terminal", name))).catch((e: unknown) => toast({ message: errorText(e) }));
      }} />

      <Tabs value={tab} onValueChange={(value) => setTab(value as typeof tab)}>
        <TabsList variant="line">{(["overview", "checkpoints"] as const).map((t) => <TabsTrigger key={t} value={t}>{t === "overview" ? C.overview : C.checkpoints}</TabsTrigger>)}</TabsList>
      </Tabs>

      {tab === "overview" ? (
        <div className="space-y-3">
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
          {d.deps.blocked_by.length > 0 && (
            <p>
              {`${C.blockedBy}: `}
              {d.deps.blocked_by.map((b: Item, i) => (
                <span key={b.key}>{i > 0 && ", "}<button type="button" onClick={() => p.onSelect(b.key)} className="text-link">{`${b.key} (${STATUS_LABEL[b.status]})`}</button></span>
              ))}
            </p>
          )}
          {d.deps.blocks.length > 0 && (
            <p>
              {`${C.blocks}: `}
              {d.deps.blocks.map((b: Item, i) => (
                <span key={b.key}>{i > 0 && ", "}<button type="button" onClick={() => p.onSelect(b.key)} className="text-link">{b.key}</button></span>
              ))}
            </p>
          )}
          <AddDependency itemKey={item.key} disabled={!p.connected} />
          {d.artifacts.length > 0 && (
            <div>
              <h4 className="text-muted-foreground">{C.artifacts}</h4>
              {d.artifacts.map((a) => (
                <button key={a.id} type="button" onClick={() => setViewing({ id: a.id, revision: a.head_revision })} className="block text-link">
                  {`${ARTIFACT_LABEL[a.kind]} · rev ${a.head_revision} · ${C.view}`}
                </button>
              ))}
            </div>
          )}
          {item.origin_spike_key && (
            <button type="button" onClick={() => p.onSelect(item.origin_spike_key ?? "")} className="text-link">
              {T.startedFrom(item.origin_spike_key)}
            </button>
          )}
        </div>
      ) : (
        <CheckpointList itemKey={item.key} agentNames={flattenAgents(d.agents).map((a) => a.name)} />
      )}

      {topLevel && open && (
        <div className="border-t border-line pt-3">
          {orchestrator ? (
            <button
              type="button"
              onClick={() => document.getElementById(`agent-${orchestrator.name}`)?.scrollIntoView?.({ block: "center" })}
              className="rounded border border-line px-3 py-1"
            >
              {C.viewOrchestrator}
            </button>
          ) : (
            <button type="button" disabled={!p.connected} onClick={() => p.onStartOrchestrator(item)} className="rounded bg-primary px-3 py-1 text-white disabled:opacity-50">
              {C.startOrchestrator}
            </button>
          )}
        </div>
      )}

      {viewing && <ArtifactViewer artifactId={viewing.id} revision={viewing.revision} onClose={() => setViewing(null)} />}
    </div>
  );
}

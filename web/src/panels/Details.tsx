import { X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ApiError, errorText } from "../api";
import { AddDependency } from "../components/AddDependency";
import { AgentList } from "../components/AgentRow";
import { ArtifactViewer } from "../components/ArtifactViewer";
import { CheckpointList } from "../components/CheckpointList";
import { MoveToMenu } from "../components/MoveToMenu";
import { useToast } from "../components/Toast";
import { C, STATUS_LABEL, T } from "../copy";
import { useInvalidate, useMutation } from "../data/hooks";
import { qk, useItemDetail } from "../data/queries";
import { flattenAgents } from "../logic/agentActions";
import { ARTIFACT_LABEL, requestTitle } from "../logic/requestTitle";
import { checkMove, failureMessage } from "../logic/transitions";
import type { Item, ItemStatus, PatchItemBody, Priority } from "../types";
import type { DetailsProps } from "../views/props";

function Editable(p: { label: string; value: string; multiline?: boolean; maxLength: number; disabled: boolean; onSave(v: string): void; className?: string }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(p.value);
  const trigger = useRef<HTMLButtonElement>(null);
  // Tracks the editing state as of the last render, so the focus effect below can tell "just closed"
  // apart from "freshly mounted" (both look like `editing === false`).
  const wasEditing = useRef(editing);
  const save = () => {
    setEditing(false);
    if (draft !== p.value) p.onSave(draft);
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
        {p.value || <span className="text-muted">—</span>}
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

  useEffect(() => setStale(false), [p.itemKey]);
  useEffect(() => {
    if (p.focus === "agents" && detail.data) agentsRef.current?.scrollIntoView?.({ block: "start" });
  }, [p.focus, detail.data]);

  const d = detail.data;
  // Standing rule: a failed load gets a message + retry, never a permanent "…" placeholder.
  if (detail.error) {
    return (
      <p className="p-4 text-bad">
        {errorText(detail.error)}{" "}
        <button type="button" className="text-accent underline" onClick={() => detail.reload()}>
          {C.retry}
        </button>
      </p>
    );
  }
  if (!d) return <p className="p-4 text-muted">…</p>;
  const item = d.item;

  const save = async (body: Omit<PatchItemBody, "revision">) => {
    try {
      await patch.run(item.key, { ...body, revision: item.revision });
      setStale(false);
    } catch (e) {
      if (e instanceof ApiError && e.code === "conflict") {
        setStale(true);
        invalidate([qk.item(item.key), "items"]);
      } else toast({ message: body.status ? failureMessage(e, item) : errorText(e) });
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
      <div className="flex items-start justify-between gap-2">
        <span className="key text-muted">{crumbs}</span>
        <button type="button" aria-label="Close" onClick={p.onClose}><X className="size-4" /></button>
      </div>
      <Editable
        label={C.title}
        value={item.title}
        maxLength={200}
        disabled={!p.connected}
        onSave={(title) => void save({ title })}
        className="text-base font-semibold"
      />
      <div className="flex items-center justify-between gap-2">
        <MoveToMenu item={item} buttonLabel={STATUS_LABEL[item.status]} disabled={!p.connected || patch.pending} onMove={onMove} />
        <label className="flex items-center gap-1">
          {C.priority}
          <select
            aria-label={C.priority}
            value={String(item.priority)}
            disabled={!p.connected}
            onChange={(e) => void save({ priority: Number(e.target.value) as Priority })}
            className="rounded border border-line bg-canvas px-1"
          >
            {[0, 1, 2, 3].map((n) => <option key={n} value={n}>{`P${n}`}</option>)}
          </select>
        </label>
      </div>

      {d.requests.length > 0 && (
        <section aria-label={C.needsYou} className="border-t border-line pt-3">
          <h3 className="mb-1 font-semibold">{C.needsYou}</h3>
          <ul className="space-y-1">
            {[...d.requests].sort((a, b) => a.created_at - b.created_at).map((r) => (
              <li key={r.id} className="flex items-center justify-between gap-2">
                <span className="truncate">{requestTitle(r)}</span>
                <button type="button" onClick={() => p.onReview(r.id)} className="text-accent">{C.review}</button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section ref={agentsRef} aria-label={C.agents} className="border-t border-line pt-3">
        <h3 className="mb-1 font-semibold">{C.agents}</h3>
        <AgentList agents={d.agents} />
      </section>

      <div role="tablist" className="flex gap-3 border-t border-line pt-3">
        {(["overview", "checkpoints"] as const).map((t) => (
          <button key={t} type="button" role="tab" aria-selected={tab === t} onClick={() => setTab(t)} className={tab === t ? "font-semibold" : "text-muted"}>
            {t === "overview" ? C.overview : C.checkpoints}
          </button>
        ))}
      </div>

      {tab === "overview" ? (
        <div className="space-y-3">
          <div>
            <h4 className="text-muted">{C.brief}</h4>
            <Editable label={C.brief} value={item.brief} multiline maxLength={600} disabled={!p.connected} onSave={(brief) => void save({ brief })} />
          </div>
          {item.acceptance.length > 0 && (
            <div>
              <h4 className="text-muted">{C.acceptance}</h4>
              <ul className="list-disc pl-5">{item.acceptance.map((a) => <li key={a}>{a}</li>)}</ul>
            </div>
          )}
          {d.deps.blocked_by.length > 0 && (
            <p>
              {`${C.blockedBy}: `}
              {d.deps.blocked_by.map((b: Item, i) => (
                <span key={b.key}>{i > 0 && ", "}<button type="button" onClick={() => p.onSelect(b.key)} className="text-accent">{`${b.key} (${STATUS_LABEL[b.status]})`}</button></span>
              ))}
            </p>
          )}
          {d.deps.blocks.length > 0 && (
            <p>
              {`${C.blocks}: `}
              {d.deps.blocks.map((b: Item, i) => (
                <span key={b.key}>{i > 0 && ", "}<button type="button" onClick={() => p.onSelect(b.key)} className="text-accent">{b.key}</button></span>
              ))}
            </p>
          )}
          <AddDependency itemKey={item.key} disabled={!p.connected} />
          {d.artifacts.length > 0 && (
            <div>
              <h4 className="text-muted">{C.artifacts}</h4>
              {d.artifacts.map((a) => (
                <button key={a.id} type="button" onClick={() => setViewing({ id: a.id, revision: a.head_revision })} className="block text-accent">
                  {`${ARTIFACT_LABEL[a.kind]} · rev ${a.head_revision} · ${C.view}`}
                </button>
              ))}
            </div>
          )}
          {item.origin_spike_key && (
            <button type="button" onClick={() => p.onSelect(item.origin_spike_key ?? "")} className="text-accent">
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
            <button type="button" disabled={!p.connected} onClick={() => p.onStartOrchestrator(item)} className="rounded bg-accent px-3 py-1 text-white disabled:opacity-50">
              {C.startOrchestrator}
            </button>
          )}
        </div>
      )}

      {viewing && <ArtifactViewer artifactId={viewing.id} revision={viewing.revision} onClose={() => setViewing(null)} />}
    </div>
  );
}

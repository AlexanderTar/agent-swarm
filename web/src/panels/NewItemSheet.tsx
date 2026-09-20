import { useState } from "react";
import { errorText } from "../api";
import { Segmented } from "../components/Segmented";
import { Sheet } from "../components/Sheet";
import { C, TYPE_LABEL } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useItems } from "../data/queries";
import {
  type NewItemForm, type NewType, PARENT_TYPES, TITLE_MAX, canCreate, initialParent, newItemPayload, parentOptions,
} from "../logic/newItem";
import type { CreateItemBody } from "../types";

const TYPES: NewType[] = ["epic", "bug", "story", "task"];
const input = "mt-1 w-full rounded border border-line bg-canvas px-2 py-1";

export function NewItemSheet(p: { type: NewType; parentKey?: string; onClose(): void; onCreated(key: string): void }) {
  const items = useItems();
  const { connected } = useConnection();
  const all = items.data?.items ?? [];
  const [form, setForm] = useState<NewItemForm>({ type: p.type, parentKey: "", title: "", brief: "", acceptance: [""] });
  const [parentTouched, setParentTouched] = useState(false);
  const [requestId] = useState(() => crypto.randomUUID());
  const [error, setError] = useState("");
  const create = useMutation((api, body: CreateItemBody) => api.createItem(body), ["items", "item:"]);
  const parentKey = parentTouched ? form.parentKey : initialParent(all, form.type, p.parentKey);
  const current = { ...form, parentKey };
  const set = (patch: Partial<NewItemForm>) => setForm((f) => ({ ...f, ...patch }));

  const submit = async () => {
    if (create.pending) return;
    setError("");
    try {
      const item = await create.run(newItemPayload(current, requestId));
      p.onCreated(item.key);
    } catch (e) {
      // Standing rule: route every daemon error surface through errorText (reason ?? message), not
      // a raw e.message / String(e) fallback.
      setError(errorText(e));
    }
  };

  return (
    <Sheet
      title={C.newItem}
      onClose={p.onClose}
      footer={
        <>
          <button type="button" onClick={p.onClose} className="rounded border border-line px-3 py-1">{C.cancel}</button>
          <button type="button" disabled={!canCreate(current) || !connected || create.pending} onClick={() => void submit()} className="rounded bg-accent px-3 py-1 text-white disabled:opacity-50">
            {C.createItem}
          </button>
        </>
      }
    >
      {error && <p role="alert" className="rounded bg-bad/10 p-2 text-bad">{error}</p>}
      {/* Standing rule: a failed items load must not silently degrade the parent picker to "no
          options" forever -- surface it with a message + retry, same pattern used everywhere else. */}
      {items.error ? (
        <p className="text-bad">
          {errorText(items.error)}{" "}
          <button type="button" className="text-accent underline" onClick={() => items.reload()}>
            {C.retry}
          </button>
        </p>
      ) : null}
      <Segmented
        label={C.type}
        value={form.type}
        onChange={(type) => { set({ type }); setParentTouched(false); }}
        options={TYPES.map((t) => ({ value: t, label: TYPE_LABEL[t] }))}
      />
      {PARENT_TYPES[form.type].length > 0 && items.data && (
        <label className="block">
          <span>{C.parent}</span>
          <select
            aria-label={C.parent}
            value={parentKey}
            onChange={(e) => { setParentTouched(true); set({ parentKey: e.target.value }); }}
            className={input}
          >
            <option value="" />
            {parentOptions(all, form.type).map((i) => <option key={i.key} value={i.key}>{`${i.key} · ${i.title}`}</option>)}
          </select>
        </label>
      )}
      <label className="block">
        <span>{C.title}</span>
        <input aria-label={C.title} maxLength={TITLE_MAX} value={form.title} onChange={(e) => set({ title: e.target.value })} className={input} />
      </label>
      <label className="block">
        <span>{C.brief}</span>
        <textarea aria-label={C.brief} rows={4} value={form.brief} onChange={(e) => set({ brief: e.target.value })} className={input} />
      </label>
      <fieldset className="space-y-1">
        <legend>{C.acceptance}</legend>
        {form.acceptance.map((a, i) => (
          <div key={i} className="flex gap-1">
            <input
              aria-label={`${C.acceptance} ${i + 1}`}
              value={a}
              onChange={(e) => set({ acceptance: form.acceptance.map((x, j) => (j === i ? e.target.value : x)) })}
              className={input}
            />
            {form.acceptance.length > 1 && (
              <button type="button" aria-label={`Remove ${C.acceptance} ${i + 1}`} onClick={() => set({ acceptance: form.acceptance.filter((_, j) => j !== i) })}>−</button>
            )}
          </div>
        ))}
        <button type="button" aria-label={`Add ${C.acceptance}`} onClick={() => set({ acceptance: [...form.acceptance, ""] })} className="text-accent">+</button>
      </fieldset>
    </Sheet>
  );
}

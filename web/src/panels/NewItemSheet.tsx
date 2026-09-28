import { useId, useState } from "react";
import { Minus } from "lucide-react";
import { errorText } from "../api";
import { Segmented } from "../components/Segmented";
import { Sheet } from "../components/Sheet";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Textarea } from "../components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../components/ui/tooltip";
import { C, T, TYPE_LABEL } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useItems } from "../data/queries";
import {
  type NewItemForm, type NewType, PARENT_TYPES, TITLE_MAX, canCreate, initialParent, newItemPayload, parentOptions,
} from "../logic/newItem";
import type { CreateItemBody } from "../types";

const TYPES: NewType[] = ["epic", "bug", "story", "task"];
const NONE = "__none";

export function NewItemSheet(p: { type: NewType; parentKey?: string; onClose(): void; onCreated(key: string): void }) {
  const items = useItems();
  const { connected } = useConnection();
  const all = items.data?.items ?? [];
  const [form, setForm] = useState<NewItemForm>({ type: p.type, parentKey: "", title: "", brief: "", acceptance: [""] });
  const [parentTouched, setParentTouched] = useState(false);
  const [requestId] = useState(() => crypto.randomUUID());
  const [error, setError] = useState("");
  const parentId = useId();
  const titleId = useId();
  const briefId = useId();
  const toast = useToast();
  const create = useMutation((api, body: CreateItemBody) => api.createItem(body), ["items", "item:"]);
  const parentKey = parentTouched ? form.parentKey : initialParent(all, form.type, p.parentKey);
  const current = { ...form, parentKey };
  const set = (patch: Partial<NewItemForm>) => setForm((f) => ({ ...f, ...patch }));

  const submit = async () => {
    if (create.pending) return;
    setError("");
    try {
      const item = await create.run(newItemPayload(current, requestId));
      toast.success(T.toastItemCreated(item.key));
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
      width={480}
      onClose={p.onClose}
      footer={
        <>
          <Button variant="secondary" onClick={p.onClose}>{C.cancel}</Button>
          <Button disabled={!canCreate(current) || !connected || create.pending} onClick={() => void submit()}>
            {C.createItem}
          </Button>
        </>
      }
    >
      {error && <Alert variant="destructive" role="alert">{error}</Alert>}
      {/* Standing rule: a failed items load must not silently degrade the parent picker to "no
          options" forever -- surface it with a message + retry, same pattern used everywhere else. */}
      {items.error ? (
        <Alert variant="destructive">
          {errorText(items.error)}{" "}
          <Button variant="link" onClick={() => items.reload()}>
            {C.retry}
          </Button>
        </Alert>
      ) : null}
      <Segmented
        label={C.type}
        value={form.type}
        onChange={(type) => { set({ type }); setParentTouched(false); }}
        options={TYPES.map((t) => ({ value: t, label: TYPE_LABEL[t] }))}
      />
      {PARENT_TYPES[form.type].length > 0 && items.data && (
        <div className="space-y-1.5">
          <Label htmlFor={parentId}>{C.parent}</Label>
          <Select value={parentKey || NONE} onValueChange={(v) => { setParentTouched(true); set({ parentKey: v === NONE ? "" : v }); }}>
            <SelectTrigger id={parentId} aria-label={C.parent} className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE}>—</SelectItem>
              {parentOptions(all, form.type).map((i) => <SelectItem key={i.key} value={i.key}>{`${i.key} · ${i.title}`}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
      )}
      <div className="space-y-1.5">
        <Label htmlFor={titleId}>{C.title}</Label>
        <Input id={titleId} maxLength={TITLE_MAX} value={form.title} onChange={(e) => set({ title: e.target.value })} />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor={briefId}>{C.brief}</Label>
        <Textarea id={briefId} rows={4} value={form.brief} onChange={(e) => set({ brief: e.target.value })} />
      </div>
      <TooltipProvider><fieldset className="space-y-1">
        <legend>{C.acceptance}</legend>
        {form.acceptance.map((a, i) => (
          <div key={i} className="flex gap-1">
            <Input
              aria-label={`${C.acceptance} ${i + 1}`}
              value={a}
              onChange={(e) => set({ acceptance: form.acceptance.map((x, j) => (j === i ? e.target.value : x)) })}
              className="flex-1"
            />
            {form.acceptance.length > 1 && (
              <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon" aria-label={`Remove ${C.acceptance} ${i + 1}`} onClick={() => set({ acceptance: form.acceptance.filter((_, j) => j !== i) })}><Minus /></Button></TooltipTrigger><TooltipContent>{`Remove ${C.acceptance} ${i + 1}`}</TooltipContent></Tooltip>
            )}
          </div>
        ))}
        <Button variant="ghost" size="sm" aria-label={`Add ${C.acceptance}`} onClick={() => set({ acceptance: [...form.acceptance, ""] })}>{`+ ${C.addCriterion}`}</Button>
      </fieldset></TooltipProvider>
    </Sheet>
  );
}

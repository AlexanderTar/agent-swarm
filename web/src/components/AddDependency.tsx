import { useEffect, useRef, useState } from "react";
import { errorText } from "../api";
import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { useItems } from "../data/queries";
import { matches } from "../logic/tree";

export function AddDependency({ itemKey, disabled }: { itemKey: string; disabled: boolean }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [error, setError] = useState("");
  const items = useItems();
  const add = useMutation((api, blockedBy: string) => api.addDep(itemKey, blockedBy), ["items", "item:", "graph:"]);
  const trigger = useRef<HTMLButtonElement>(null);
  const search = useRef<HTMLInputElement>(null);
  // Standing rule: this search box is transient UI — take focus on open, restore it to the trigger
  // button on close (the brief opened it with no focus management at all).
  useEffect(() => {
    if (open) search.current?.focus();
  }, [open]);
  const close = () => {
    setOpen(false);
    trigger.current?.focus();
  };
  const hits = q.trim()
    ? (items.data?.items ?? []).filter((i) => i.key !== itemKey && matches(i, { q, type: "", status: "" })).slice(0, 8)
    : [];
  const pick = async (key: string) => {
    setError("");
    try {
      await add.run(key);
      setQ("");
      close();
    } catch (e) {
      setError(errorText(e));
    }
  };
  return (
    <div>
      <button ref={trigger} type="button" disabled={disabled} onClick={() => setOpen((o) => !o)} className="text-accent disabled:text-muted">
        {`+ ${C.addDependency}`}
      </button>
      {open && (
        <div className="mt-1 space-y-1">
          <input
            ref={search}
            type="search"
            aria-label={C.addDependency}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && close()}
            className="w-full rounded border border-line bg-canvas px-2 py-1"
          />
          {hits.map((i) => (
            <button key={i.key} type="button" disabled={add.pending} onClick={() => void pick(i.key)} className="block w-full truncate rounded px-2 py-0.5 text-left hover:bg-raised">
              {`${i.key} · ${i.title}`}
            </button>
          ))}
          {error && <p className="text-bad">{error}</p>}
        </div>
      )}
    </div>
  );
}

import { useEffect, useRef, useState } from "react";
import { errorText } from "../api";
import { C, T } from "../copy";
import { useMutation } from "../data/hooks";
import { useItems } from "../data/queries";
import { matches } from "../logic/tree";
import { useToast } from "./Toast";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

export function AddDependency({ itemKey, disabled }: { itemKey: string; disabled: boolean }) {
  const toast = useToast();
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
      toast.success(T.toastDepAdded(itemKey, key));
      setQ("");
      close();
    } catch (e) {
      setError(errorText(e));
    }
  };
  return (
    <div>
      <Button ref={trigger} variant="link" size="sm" type="button" disabled={disabled} onClick={() => setOpen((o) => !o)}>
        {`+ ${C.addDependency}`}
      </Button>
      {open && (
        <div className="mt-1 space-y-1">
          <Input
            ref={search}
            type="search"
            aria-label={C.addDependency}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && close()}
            className="w-full"
          />
          {hits.map((i) => (
            <Button key={i.key} variant="ghost" size="sm" type="button" disabled={add.pending} onClick={() => void pick(i.key)} className="block w-full truncate text-left">
              {`${i.key} · ${i.title}`}
            </Button>
          ))}
          {error && <p className="text-bad">{error}</p>}
        </div>
      )}
    </div>
  );
}

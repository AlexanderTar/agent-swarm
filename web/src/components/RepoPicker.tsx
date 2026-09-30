import { useEffect, useId, useMemo, useState } from "react";
import { Check } from "lucide-react";
import { ApiError, errorText } from "../api";
import { C, T } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useRepos } from "../data/queries";
import { chooserRows, reconcileSelection, scanLine, selectedLine, shortPath, toggleRepo } from "../logic/repos";
import { cn } from "../lib/utils";
import type { Repo } from "../types";
import { useToast } from "./Toast";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { ScrollArea } from "./ui/scroll-area";

export function RepoPicker(p: { selected: string[]; onChange(ids: string[]): void; label: string; caption?: string }) {
  const { connected, live } = useConnection();
  const toast = useToast();
  const [adding, setAdding] = useState(false);
  const [path, setPath] = useState("");
  const [addError, setAddError] = useState("");
  const [notice, setNotice] = useState("");
  const [created, setCreated] = useState<Repo[]>([]);
  const [reconcilePending, setReconcilePending] = useState(false);
  const repos = useRepos("");
  const add = useMutation((api, folder: string) => api.addRepo(folder), ["repos:"]);
  const rescan = useMutation((api) => api.rescanRepos(), ["repos:"]);
  const data = repos.data;
  const rows = useMemo(() => data ? chooserRows(data) : [], [data]);
  const labelId = useId();

  useEffect(() => {
    if (!reconcilePending || !data || data.scanning || repos.loading || repos.error) return;
    setReconcilePending(false);
    const result = reconcileSelection(p.selected, rows);
    if (result.removed > 0) {
      p.onChange(result.selection);
      setNotice(T.reposNoLonger(result.removed));
    }
  }, [reconcilePending, rows, data, repos.loading, repos.error, p]);

  const toggle = (id: string) => {
    setNotice("");
    p.onChange(toggleRepo(p.selected, id));
  };

  const submitFolder = async () => {
    if (!live.connected || add.pending) return;
    const startedAt = live.epoch;
    setAddError("");
    try {
      const repo = await add.run(path);
      if (!live.connected || live.epoch !== startedAt) return;
      setCreated((current) => [...current, repo]);
      p.onChange(toggleRepo(p.selected, repo.id));
      toast.success(T.toastRepoAdded(repo.name));
      setPath("");
      setAdding(false);
    } catch (e) {
      if (!live.connected || live.epoch !== startedAt) return;
      setAddError(e instanceof ApiError ? errorText(e) : C.notARepo);
    }
  };

  const doRescan = async () => {
    if (!live.connected || rescan.pending) return;
    const startedAt = live.epoch;
    try {
      const result = await rescan.run();
      if (!live.connected || live.epoch !== startedAt) return;
      setReconcilePending(true);
      toast.success(T.toastRescanned(result.found, result.missing));
    } catch (e) {
      if (!live.connected || live.epoch !== startedAt) return;
      toast.error(errorText(e));
    }
  };

  return (
    <fieldset className="space-y-1.5">
      <legend id={labelId} className="font-medium">{p.label}</legend>
      {p.selected.length > 0 && <p className="text-xs text-muted-foreground">{p.selected.length} selected</p>}
      {p.caption && <p className="text-xs text-muted-foreground">{p.caption}</p>}
      <ScrollArea className="h-[242px] w-full min-w-0 rounded-md border border-border">
        <div role="listbox" aria-multiselectable="true" aria-labelledby={labelId} className="p-0.5">
          {!!repos.error && <div role="alert" className="flex h-60 items-center justify-center gap-1 text-muted-foreground">{C.reposUnavailable}<Button variant="link" size="sm" onClick={repos.reload}>{C.retry}</Button></div>}
          {!repos.error && !repos.loading && data && rows.length === 0 && <div className="flex h-60 items-center justify-center text-muted-foreground">{data.scanning ? C.reposScanning : C.reposEmpty}</div>}
          {!repos.error && !repos.loading && rows.map((r) => {
            const on = p.selected.includes(r.id);
            return (
              <div key={r.id} role="option" aria-selected={on} data-name={r.name} tabIndex={0}
                onClick={() => toggle(r.id)}
                onKeyDown={(e) => { if (e.key === " " || e.key === "Enter") { e.preventDefault(); toggle(r.id); } }}
                className={cn("flex h-[30px] cursor-default items-center gap-2 rounded-sm px-2 outline-none focus-visible:ring-2 focus-visible:ring-ring", on ? "bg-accent" : "hover:bg-accent/60")}
              >
                <Check aria-hidden className={cn("size-3.5 text-link", !on && "invisible")} />
                <span className="w-28 shrink-0 truncate font-medium sm:w-44">{r.name}</span>
                <span className="key min-w-0 flex-1 truncate text-muted-foreground">{shortPath(r.path)}</span>
                {r.dirty && <span role="img" aria-label={C.repoDirty} title={C.repoDirty} className="ml-auto size-1.5 shrink-0 rounded-full bg-warning" />}
              </div>
            );
          })}
        </div>
      </ScrollArea>
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="sm" disabled={!connected} onClick={() => setAdding((a) => !a)}>{C.addFolder}</Button>
        <span className="ml-auto text-xs text-muted-foreground">{data ? scanLine(data) : ""}</span>
        <Button variant="ghost" size="sm" disabled={!connected || rescan.pending} onClick={() => void doRescan()}>{C.rescan}</Button>
      </div>
      {adding && (
        <form onSubmit={(e) => { e.preventDefault(); void submitFolder(); }} className="flex flex-wrap gap-2">
          <Input aria-label={C.addFolder} value={path} onChange={(e) => setPath(e.target.value)} className="min-w-0 flex-1" />
          <Button size="sm" type="submit" disabled={!connected || add.pending}>{C.add}</Button>
          {addError && <p className="w-full text-xs text-destructive">{addError}</p>}
        </form>
      )}
      <p aria-live="polite" className="text-xs text-muted-foreground">{notice}</p>
      <p className="text-xs">{selectedLine(p.selected, [...rows, ...created])}</p>
    </fieldset>
  );
}

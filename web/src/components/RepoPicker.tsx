import { useState } from "react";
import { ApiError } from "../api";
import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { useRepos } from "../data/queries";
import { knownRepos, repoSections, repoSubtitle, scanLine, selectAll, selectedLine, toggleRepo } from "../logic/repos";
import type { Repo } from "../types";

export function RepoPicker(p: { selected: string[]; onChange(ids: string[]): void; label: string; caption?: string }) {
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [path, setPath] = useState("");
  const [addError, setAddError] = useState("");
  const [created, setCreated] = useState<Repo[]>([]);
  const repos = useRepos(q);
  const add = useMutation((api, folder: string) => api.addRepo(folder), ["repos:"]);
  const rescan = useMutation((api) => api.rescanRepos(), ["repos:"]);
  const data = repos.data;

  const submitFolder = async () => {
    setAddError("");
    try {
      const repo = await add.run(path);
      setCreated((c) => [...c, repo]);
      p.onChange(toggleRepo(p.selected, repo.id));
      setPath("");
      setAdding(false);
    } catch (e) {
      setAddError(e instanceof ApiError ? e.message : C.notARepo);
    }
  };

  return (
    <fieldset className="space-y-2">
      <legend className="font-medium">{p.label}</legend>
      {p.caption && <p className="text-muted">{p.caption}</p>}
      <input
        type="search"
        placeholder={C.searchRepos}
        value={q}
        onChange={(e) => setQ(e.target.value)}
        className="w-full rounded border border-line bg-canvas px-2 py-1"
      />
      <div className="max-h-72 space-y-2 overflow-y-auto">
        {data &&
          repoSections(data).map((s) => (
            <div key={s.id} role="group" aria-label={s.title}>
              <div className="flex items-center justify-between text-muted">
                <span>{s.title}</span>
                {s.groupName && (
                  <button type="button" onClick={() => p.onChange(selectAll(p.selected, s.repos))} className="text-accent">
                    {C.all}
                  </button>
                )}
              </div>
              {s.repos.map((r) => (
                <label key={r.id} className={`flex gap-2 py-0.5 ${r.missing ? "opacity-60" : ""}`}>
                  <input type="checkbox" checked={p.selected.includes(r.id)} disabled={r.missing} onChange={() => p.onChange(toggleRepo(p.selected, r.id))} />
                  <span className="min-w-0">
                    <span className="font-medium">{r.name}</span> <span className="text-muted">{repoSubtitle(r)}</span>
                    {r.missing && <span className="block text-bad">{C.repoMissing}</span>}
                    {r.dirty && <span className="block text-muted">{C.repoDirty}</span>}
                  </span>
                </label>
              ))}
            </div>
          ))}
      </div>
      <div className="flex items-center justify-between gap-2">
        <button type="button" onClick={() => setAdding((a) => !a)} className="text-accent">{C.addFolder}</button>
        <span className="text-muted">{data ? scanLine(data) : ""}</span>
        <button
          type="button"
          disabled={rescan.pending}
          onClick={() => void rescan.run().catch(() => undefined)}
          className="text-accent"
        >
          {C.rescan}
        </button>
      </div>
      {adding && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void submitFolder();
          }}
        >
          <input
            aria-label={C.addFolder}
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="/Users/you/code/repo"
            className="w-full rounded border border-line bg-canvas px-2 py-1"
          />
          {addError && <p className="text-bad">{addError}</p>}
        </form>
      )}
      <p className="text-muted">{selectedLine(p.selected, [...(data ? knownRepos(data) : []), ...created])}</p>
    </fieldset>
  );
}

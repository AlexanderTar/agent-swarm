import { useState } from "react";
import { ApiError, errorText } from "../api";
import { C, T } from "../copy";
import { useInvalidate, useMutation } from "../data/hooks";
import { useRepos } from "../data/queries";
import { type ConfirmRow, confirmError, confirmModel, confirmPayload } from "../logic/confirmRepos";
import { knownRepos, toggleRepo } from "../logic/repos";
import type { ConfirmReposBody, Request } from "../types";
import { RepoPicker } from "./RepoPicker";

function Rows({ title, rows, checked, onToggle }: { title: string; rows: ConfirmRow[]; checked: string[]; onToggle(id: string): void }) {
  if (rows.length === 0) return null;
  return (
    <fieldset className="space-y-1">
      <legend className="font-medium">{title}</legend>
      {rows.map((r) => (
        <label key={r.id} className="flex gap-2">
          <input type="checkbox" checked={checked.includes(r.id)} disabled={r.repo?.missing} onChange={() => onToggle(r.id)} />
          <span>
            <span className="font-medium">{r.name}</span> <span className="text-muted">{r.subtitle}</span>
            {r.youSelected && <span className="ml-2 rounded bg-raised px-1 text-[11px]">{C.youSelected}</span>}
            {r.reason && <span className="block text-muted">{T.reason(r.reason)}</span>}
          </span>
        </label>
      ))}
    </fieldset>
  );
}

export function ConfirmRepos({ request, connected }: { request: Request; connected: boolean }) {
  const repos = useRepos("");
  const model = confirmModel(request, repos.data ? knownRepos(repos.data) : []);
  const [checked, setChecked] = useState<string[]>(model.initial);
  const [comment, setComment] = useState("");
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const invalidate = useInvalidate();
  const confirm = useMutation((api, body: ConfirmReposBody) => api.confirmRepos(request.id, body), ["requests", "items", "item:"]);
  const toggle = (id: string) => setChecked((c) => toggleRepo(c, id));

  const submit = async () => {
    const err = confirmError(checked);
    setError(err ?? "");
    if (err) return;
    try {
      await confirm.run(confirmPayload(checked, comment, model.version));
    } catch (e) {
      if (e instanceof ApiError && e.code === "conflict") {
        setStale(true);
        invalidate(["requests"]);
      } else setError(errorText(e));
    }
  };

  return (
    <div className="space-y-3">
      {stale && <p role="alert" className="rounded bg-warn/10 p-2 text-warn">{C.staleApproval}</p>}
      <p className="italic">{`“${request.prompt}”`}</p>
      <Rows title={C.proposed} rows={model.proposed} checked={checked} onToggle={toggle} />
      <Rows title={C.suggestedAdditions} rows={model.additions} checked={checked} onToggle={toggle} />
      <RepoPicker label={C.addAnother} selected={checked} onChange={setChecked} />
      <label className="block">
        <span>{C.commentOptional}</span>
        <input aria-label={C.commentOptional} value={comment} onChange={(e) => setComment(e.target.value)} className="mt-1 w-full rounded border border-line bg-canvas px-2 py-1" />
      </label>
      {error && <p className="text-bad">{error}</p>}
      <button type="button" disabled={!connected || confirm.pending} onClick={() => void submit()} className="rounded bg-accent px-3 py-1 text-white disabled:opacity-50">
        {C.confirmRepositories}
      </button>
    </div>
  );
}

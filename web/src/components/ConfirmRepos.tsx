import { useState } from "react";
import { ApiError, errorText } from "../api";
import { C, T } from "../copy";
import { useInvalidate, useMutation } from "../data/hooks";
import { useRepos } from "../data/queries";
import { type ConfirmRow, confirmError, confirmModel, confirmPayload } from "../logic/confirmRepos";
import { knownRepos, toggleRepo } from "../logic/repos";
import type { ConfirmReposBody, Request } from "../types";
import { RepoPicker } from "./RepoPicker";
import { useToast } from "./Toast";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Badge } from "./ui/badge";
import { Alert } from "./ui/alert";

function Rows({ title, rows, checked, onToggle }: { title: string; rows: ConfirmRow[]; checked: string[]; onToggle(id: string): void }) {
  if (rows.length === 0) return null;
  return (
    <fieldset className="space-y-1">
      <legend className="font-medium">{title}</legend>
      {rows.map((r) => (
        <Label key={r.id} className="flex items-start gap-2">
          <Checkbox checked={checked.includes(r.id)} disabled={r.repo?.missing} onCheckedChange={() => onToggle(r.id)} />
          <span>
            <span className="font-medium">{r.name}</span> <span className="text-muted-foreground">{r.subtitle}</span>
            {r.youSelected && <Badge variant="outline" className="ml-2">{C.youSelected}</Badge>}
            {r.reason && <span className="block text-muted-foreground">{T.reason(r.reason)}</span>}
          </span>
        </Label>
      ))}
    </fieldset>
  );
}

export function ConfirmRepos({ request, connected }: { request: Request; connected: boolean }) {
  const toast = useToast();
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
      toast.success(T.toastReposConfirmed(checked.length));
    } catch (e) {
      if (e instanceof ApiError && e.code === "conflict") {
        setStale(true);
        invalidate(["requests"]);
      } else setError(errorText(e));
    }
  };

  return (
    <div className="space-y-3">
      {stale && <Alert variant="destructive">{C.staleApproval}</Alert>}
      <p className="italic">{`“${request.prompt}”`}</p>
      <Rows title={C.proposed} rows={model.proposed} checked={checked} onToggle={toggle} />
      <Rows title={C.suggestedAdditions} rows={model.additions} checked={checked} onToggle={toggle} />
      <RepoPicker label={C.addAnother} selected={checked} onChange={setChecked} />
      <label className="block">
        <span>{C.commentOptional}</span>
        <Input aria-label={C.commentOptional} value={comment} onChange={(e) => setComment(e.target.value)} className="mt-1" />
      </label>
      {error && <p className="text-destructive">{error}</p>}
      <Button type="button" disabled={!connected || confirm.pending} onClick={() => void submit()}>
        {C.confirmRepositories}
      </Button>
    </div>
  );
}

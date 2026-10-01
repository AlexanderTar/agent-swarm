import { useState } from "react";
import { ApiError, errorText } from "../api";
import { ArtifactViewer } from "../components/ArtifactViewer";
import { ConfirmRepos } from "../components/ConfirmRepos";
import { Markdown } from "../components/Markdown";
import { QuestionView } from "../components/QuestionView";
import { RequestChanges } from "../components/RequestChanges";
import { useToast } from "../components/Toast";
import { Button } from "../components/ui/button";
import { Alert } from "../components/ui/alert";
import { Textarea } from "../components/ui/textarea";
import { C, STATUS_LABEL, T } from "../copy";
import { useInvalidate, useMutation, useQuery } from "../data/hooks";
import { useArtifact, useCheckpoints, useItemDetail } from "../data/queries";
import { ARTIFACT_LABEL } from "../logic/requestTitle";
import { SCOPE_LABEL, agentOptions, approveBody, closeResolution, gitBindingLine, isApprovalKind, reviewHeader } from "../logic/review";
import { verifyLine } from "../logic/timeline";
import type { AcceptBinding, MergeChoice, Request } from "../types";

function Snapshot({ r }: { r: Request }) {
  const [full, setFull] = useState(false);
  const section = r.kind === "approve_section" && !full ? r.section_id ?? undefined : undefined;
  const art = useArtifact(r.artifact_id, r.artifact_revision ?? undefined, section);
  // Standing rule: a failed load gets a message + retry, never a permanent "…" placeholder.
  if (art.error) {
    return (
      <Alert variant="destructive">
        {errorText(art.error)}{" "}
        <Button type="button" variant="link" size="sm" onClick={() => art.reload()}>
          {C.retry}
        </Button>
      </Alert>
    );
  }
  return (
    <div className="space-y-3">
      {r.kind === "approve_plan" && art.data?.warnings && art.data.warnings.length > 0 && (
        <Alert aria-label="Plan warnings" className="border-warning text-warning">
          <h3 className="font-semibold">Plan warnings</h3>
          <ul className="list-disc pl-5">{art.data.warnings.map((warning, i) => <li key={i}>{warning}</li>)}</ul>
        </Alert>
      )}
      {art.data ? <Markdown>{art.data.markdown}</Markdown> : <p className="text-muted-foreground">…</p>}
      {r.kind === "approve_section" && !full && (
        <Button type="button" variant="link" size="sm" onClick={() => setFull(true)}>{C.viewFullSpec}</Button>
      )}
    </div>
  );
}

function AcceptBody({ r }: { r: Request }) {
  const binding = r.binding as AcceptBinding;
  const detail = useItemDetail(r.item_key);
  const cps = useCheckpoints(r.item_key);
  const [viewing, setViewing] = useState<{ id: string; revision: number } | null>(null);
  const children = detail.data?.children ?? [];
  const finals = useQuery(
    detail.data ? `checkpoints:final:${r.item_key}:${children.map((c) => c.key).join(",")}` : null,
    (api) => Promise.all(children.map((c) => api.checkpoints(c.key, 1).then((l) => l[0] ?? null))),
  );
  // Standing rule: a failed load gets a message + retry, never a permanent blank panel. Checked
  // after every hook above runs, so this early return never changes the hook order between renders.
  if (detail.error || cps.error) {
    return (
      <Alert variant="destructive">
        {errorText(detail.error ?? cps.error)}{" "}
        <Button
          type="button"
          variant="link"
          size="sm"
          onClick={() => {
            detail.reload();
            cps.reload();
          }}
        >
          {C.retry}
        </Button>
      </Alert>
    );
  }
  const integrated = cps.data?.find((c) => c.id === binding.integrated_checkpoint);
  const plan = detail.data?.artifacts.find((a) => a.kind === "plan");
  return (
    <div className="space-y-3">
      <ul className="key space-y-0.5">{binding.git.map((g) => <li key={`${g.repo}${g.sha}`}>{gitBindingLine(g)}</li>)}</ul>
      {integrated && (
        <ul className="space-y-0.5">{integrated.verification.map((v) => <li key={v.cmd}>{verifyLine(v)}</li>)}</ul>
      )}
      <ul aria-label="Children" className="space-y-1">
        {children.map((c, i) => (
          <li key={c.key}>
            {`${c.key} ${c.title} — ${STATUS_LABEL[c.status]}`}
            {finals.data?.[i] && <span className="text-muted-foreground">{` · ${finals.data[i]?.summary}`}</span>}
          </li>
        ))}
      </ul>
      {plan && (
        <Button type="button" variant="link" size="sm" onClick={() => setViewing({ id: plan.id, revision: plan.head_revision })}>
          {`${ARTIFACT_LABEL.plan} · rev ${plan.head_revision} · ${C.view}`}
        </Button>
      )}
      {viewing && <ArtifactViewer artifactId={viewing.id} revision={viewing.revision} onClose={() => setViewing(null)} />}
    </div>
  );
}

function CloseBody({ r }: { r: Request }) {
  const cps = useCheckpoints(r.item_key);
  const done = cps.data?.find((c) => c.kind === "completed" && c.resolution);
  return (
    <div className="space-y-2">
      <p className="font-medium">{closeResolution(r)}</p>
      {done && <p>{done.summary}</p>}
    </div>
  );
}

export function Review({ request: r, connected }: { request: Request; connected: boolean }) {
  const head = reviewHeader(r);
  const [stale, setStale] = useState(false);
  const [comment, setComment] = useState("");
  const options = agentOptions(r);
  const isFinish = r.kind === "accept_epic" || r.kind === "accept_fix";
  const invalidate = useInvalidate();
  const toast = useToast();
  const decide = useMutation(
    (api, a: { kind: "approve" | "close"; merge?: MergeChoice; choice?: string }) =>
      a.kind === "approve" ? api.approve(r.id, approveBody(r, a.merge, a.choice, comment)) : api.closeSpike(r.id),
    ["requests", "items", "item:"],
  );
  const run = async (kind: "approve" | "close", merge?: MergeChoice, choice?: string) => {
    try {
      await decide.run({ kind, merge, choice });
      toast.success(kind === "close" ? T.toastSpikeClosed(r.item_key) : T.toastApproved(r.item_key));
    } catch (e) {
      if (e instanceof ApiError && e.code === "conflict") {
        setStale(true);
        invalidate(["requests", "item:", "artifact:", "checkpoints:"]);
      } else toast({ message: errorText(e) });
    }
  };

  return (
    <article className="space-y-4">
      <header className="space-y-0.5 border-b border-border pb-3">
        <h2 className="text-base font-semibold">{head.title}</h2>
        {head.by && <p className="text-muted-foreground">{head.by}</p>}
        {head.revision && <p className="text-muted-foreground">{head.revision}</p>}
      </header>
      {stale && <Alert variant="destructive">{C.staleApproval}</Alert>}

      {(r.kind === "question" || r.kind === "prompt" || r.kind === "blocker") && <QuestionView request={r} connected={connected} />}
      {r.kind === "confirm_repos" && <ConfirmRepos key={r.id} request={r} connected={connected} />}
      {(r.kind === "approve_section" || r.kind === "approve_plan" || r.kind === "approve_report") && <Snapshot r={r} />}
      {(r.kind === "accept_epic" || r.kind === "accept_fix") && <AcceptBody r={r} />}
      {r.kind === "close_spike" && <CloseBody r={r} />}

      {(isApprovalKind(r.kind) || r.kind === "close_spike") && (
        <footer className="flex flex-wrap items-start gap-2 border-t border-border pt-3">
          {isFinish && options.length > 0 ? (
            <>
              <div className="w-full space-y-2">
                {options.map((o, i) => (
                  <div key={o.label} className="flex flex-col items-start gap-0.5">
                    <Button type="button" variant={i === 0 ? "default" : "outline"} disabled={!connected || decide.pending} onClick={() => void run("approve", "custom", o.label)}>
                      {o.label}
                    </Button>
                    {o.description && <span className="text-muted-foreground">{o.description}</span>}
                  </div>
                ))}
              </div>
              <Textarea className="w-full" aria-label={C.approveComment} placeholder={C.approveComment} rows={2} maxLength={2000} value={comment} onChange={(e) => setComment(e.target.value)} />
            </>
          ) : isFinish ? (
            r.finish_local ? (
              <Button type="button" disabled={!connected || decide.pending} onClick={() => void run("approve", "local")}>
                {C.mergeLocally}
              </Button>
            ) : (
              <>
                <Button type="button" disabled={!connected || decide.pending} onClick={() => void run("approve", "auto")}>
                  {C.createPrAutoMerge}
                </Button>
                <Button type="button" variant="outline" disabled={!connected || decide.pending} onClick={() => void run("approve", "manual")}>
                  {C.createPr}
                </Button>
              </>
            )
          ) : (
            <Button type="button" disabled={!connected || decide.pending} onClick={() => void run(r.kind === "close_spike" ? "close" : "approve")}>
              {SCOPE_LABEL[r.kind]}
            </Button>
          )}
          <RequestChanges requestId={r.id} agentName={r.agent_name ?? r.item_key} connected={connected} />
        </footer>
      )}
    </article>
  );
}

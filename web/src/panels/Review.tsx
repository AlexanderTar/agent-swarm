import { useState } from "react";
import { ApiError, errorText } from "../api";
import { ArtifactViewer } from "../components/ArtifactViewer";
import { ConfirmRepos } from "../components/ConfirmRepos";
import { Markdown } from "../components/Markdown";
import { QuestionView } from "../components/QuestionView";
import { RequestChanges } from "../components/RequestChanges";
import { useToast } from "../components/Toast";
import { C, STATUS_LABEL } from "../copy";
import { useInvalidate, useMutation, useQuery } from "../data/hooks";
import { useArtifact, useCheckpoints, useItemDetail } from "../data/queries";
import { ARTIFACT_LABEL } from "../logic/requestTitle";
import { SCOPE_LABEL, approveBody, closeResolution, gitBindingLine, isApprovalKind, reviewHeader } from "../logic/review";
import { verifyLine } from "../logic/timeline";
import type { AcceptBinding, Request } from "../types";

function Snapshot({ r }: { r: Request }) {
  const [full, setFull] = useState(false);
  const section = r.kind === "approve_section" && !full ? r.section_id ?? undefined : undefined;
  const art = useArtifact(r.artifact_id, r.artifact_revision ?? undefined, section);
  // Standing rule: a failed load gets a message + retry, never a permanent "…" placeholder.
  if (art.error) {
    return (
      <p className="text-bad">
        {errorText(art.error)}{" "}
        <button type="button" className="text-accent underline" onClick={() => art.reload()}>
          {C.retry}
        </button>
      </p>
    );
  }
  return (
    <div className="space-y-3">
      {r.kind === "approve_plan" && art.data?.warnings && art.data.warnings.length > 0 && (
        <section aria-label="Plan warnings" className="rounded border border-warn bg-warn/10 p-2 text-warn">
          <h3 className="font-semibold">Plan warnings</h3>
          <ul className="list-disc pl-5">{art.data.warnings.map((warning, i) => <li key={i}>{warning}</li>)}</ul>
        </section>
      )}
      {art.data ? <Markdown>{art.data.markdown}</Markdown> : <p className="text-muted">…</p>}
      {r.kind === "approve_section" && !full && (
        <button type="button" onClick={() => setFull(true)} className="text-accent">{C.viewFullSpec}</button>
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
      <p className="text-bad">
        {errorText(detail.error ?? cps.error)}{" "}
        <button
          type="button"
          className="text-accent underline"
          onClick={() => {
            detail.reload();
            cps.reload();
          }}
        >
          {C.retry}
        </button>
      </p>
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
            {finals.data?.[i] && <span className="text-muted">{` · ${finals.data[i]?.summary}`}</span>}
          </li>
        ))}
      </ul>
      {plan && (
        <button type="button" onClick={() => setViewing({ id: plan.id, revision: plan.head_revision })} className="text-accent">
          {`${ARTIFACT_LABEL.plan} · rev ${plan.head_revision} · ${C.view}`}
        </button>
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
  const invalidate = useInvalidate();
  const toast = useToast();
  const decide = useMutation(
    (api, kind: "approve" | "close") => (kind === "approve" ? api.approve(r.id, approveBody(r)) : api.closeSpike(r.id)),
    ["requests", "items", "item:"],
  );
  const run = async (kind: "approve" | "close") => {
    try {
      await decide.run(kind);
    } catch (e) {
      if (e instanceof ApiError && e.code === "conflict") {
        setStale(true);
        invalidate(["requests", "item:", "artifact:", "checkpoints:"]);
      } else toast({ message: errorText(e) });
    }
  };

  return (
    <article className="space-y-4">
      <header className="space-y-0.5 border-b border-line pb-3">
        <h2 className="text-base font-semibold">{head.title}</h2>
        {head.by && <p className="text-muted">{head.by}</p>}
        {head.revision && <p className="text-muted">{head.revision}</p>}
      </header>
      {stale && <p role="alert" className="rounded bg-warn/10 p-2 text-warn">{C.staleApproval}</p>}

      {(r.kind === "question" || r.kind === "prompt" || r.kind === "blocker") && <QuestionView request={r} connected={connected} />}
      {r.kind === "confirm_repos" && <ConfirmRepos key={r.id} request={r} connected={connected} />}
      {(r.kind === "approve_section" || r.kind === "approve_plan" || r.kind === "approve_report") && <Snapshot r={r} />}
      {(r.kind === "accept_epic" || r.kind === "accept_fix") && <AcceptBody r={r} />}
      {r.kind === "close_spike" && <CloseBody r={r} />}

      {(isApprovalKind(r.kind) || r.kind === "close_spike") && (
        <footer className="flex flex-wrap items-start gap-2 border-t border-line pt-3">
          <button
            type="button"
            disabled={!connected || decide.pending}
            onClick={() => void run(r.kind === "close_spike" ? "close" : "approve")}
            className="rounded bg-accent px-3 py-1 text-white disabled:opacity-50"
          >
            {SCOPE_LABEL[r.kind]}
          </button>
          <RequestChanges requestId={r.id} connected={connected} />
        </footer>
      )}
    </article>
  );
}

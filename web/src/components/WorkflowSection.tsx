import type { ItemDetail, Workflow } from "../types";
import { ROLE_EMOJI, ROLE_LABEL } from "../copy";
import type { Role, WorkflowFinding } from "../types";

const VERDICT = {
  pass: { label: "Pass", className: "bg-ok/10 text-ok" },
  changes_requested: { label: "Changes requested", className: "bg-warn/10 text-warn" },
  blocked: { label: "Blocked", className: "bg-bad/10 text-bad" },
} as const;

function findingText(f: WorkflowFinding): string {
  return `[${f.severity}] ${f.file}${f.line ? `:${f.line}` : ""} — ${f.summary}${f.unit ? ` [unit ${f.unit}]` : ""}`;
}

export function WorkflowSection(p: { workflow: Workflow | null | undefined; state: ItemDetail["workflow_state"]; onOpenTerminal(name: string): void }) {
  if (!p.workflow || !p.state) return null;
  const { workflow, state } = p;
  const title = `Workflow · ${workflow.template ?? "custom"} · ${state.state.charAt(0).toUpperCase()}${state.state.slice(1)} · Round ${state.round} of ${workflow.max_rounds ?? 1}`;
  return (
    <section aria-label="Workflow" className="space-y-2 border-t border-line pt-3">
      <h3 className="font-semibold">{title}</h3>
      {state.state === "escalated" && (
        <div role="alert" className="rounded border border-bad bg-bad/10 p-2 text-bad">
          <p>{state.escalation}</p><p>The orchestrator decides next.</p>
        </div>
      )}
      <ol className="space-y-2 border-l border-line pl-3">
        {(workflow.steps ?? []).map((step) => (
          <li key={step.id}>
            <h4 className="font-medium">{step.id}</h4>
            <ul className="space-y-1 pl-2">
              {state.runs.filter((run) => run.step === step.id).map((run, i) => {
                const verdict = run.verdict ? VERDICT[run.verdict] : null;
                return (
                  <li key={run.id ?? `${run.round}-${run.role}-${i}`} className="text-sm">
                    <div className="flex flex-wrap items-center gap-2">
                      <span>{ROLE_EMOJI[run.role as Role] ?? "👤"} {ROLE_LABEL[run.role as Role] ?? run.role}</span>
                      {run.agent && <button type="button" className="text-accent underline" onClick={() => p.onOpenTerminal(run.agent)}>{run.agent}</button>}
                      <span className="rounded bg-raised px-1.5 py-0.5">{run.state.charAt(0).toUpperCase()}{run.state.slice(1)}</span>
                      {verdict && <span className={`rounded px-1.5 py-0.5 ${verdict.className}`}>{verdict.label}</span>}
                    </div>
                    {run.findings.length > 0 && <details className="pl-6">
                      <summary className="cursor-pointer text-accent">{run.findings.length} findings</summary>
                      <ul className="list-disc pl-5">{run.findings.map((f, j) => <li key={j}>{findingText(f)}</li>)}</ul>
                    </details>}
                  </li>
                );
              })}
            </ul>
          </li>
        ))}
      </ol>
    </section>
  );
}

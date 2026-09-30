import type { ItemDetail, Workflow } from "../types";
import { ROLE_EMOJI, ROLE_LABEL } from "../copy";
import type { Role, WorkflowFinding } from "../types";
import { ChevronRight } from "lucide-react";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./ui/collapsible";
import { Button } from "./ui/button";

const VERDICT = {
  pass: { label: "Pass", className: "bg-success/10 text-success" },
  changes_requested: { label: "Changes requested", className: "bg-warning/10 text-warning" },
  blocked: { label: "Blocked", className: "bg-destructive/10 text-destructive" },
} as const;

function findingText(f: WorkflowFinding): string {
  return `[${f.severity}] ${f.file}${f.line ? `:${f.line}` : ""} — ${f.summary}${f.unit ? ` [unit ${f.unit}]` : ""}`;
}

export function WorkflowSection(p: { workflow: Workflow | null | undefined; state: ItemDetail["workflow_state"]; connected: boolean; onOpenTerminal(name: string): void }) {
  if (!p.workflow || !p.state) return null;
  const { workflow, state } = p;
  const title = `Workflow · ${workflow.template ?? "custom"} · ${state.state.charAt(0).toUpperCase()}${state.state.slice(1)} · Round ${state.round} of ${workflow.max_rounds ?? 1}`;
  return (
    <section aria-label="Workflow" className="space-y-2 border-t border-border pt-3">
      <Collapsible>
      <h3 className="font-semibold">
        <CollapsibleTrigger asChild><Button variant="ghost" className="group h-auto w-full min-w-0 shrink justify-start whitespace-normal break-words px-0 text-left font-semibold"><ChevronRight aria-hidden className="size-4 shrink-0 transition-transform group-data-[state=open]:rotate-90" />{title}</Button></CollapsibleTrigger>
      </h3>
      <CollapsibleContent className="space-y-2">
      {state.state === "escalated" && (
        <div role="alert" className="rounded border border-destructive bg-destructive/10 p-2 text-destructive">
          <p>{state.escalation}</p><p>The orchestrator decides next.</p>
        </div>
      )}
      <ol className="space-y-2 border-l border-border pl-3">
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
                      {run.agent && <Button type="button" variant="link" disabled={!p.connected} className="h-auto p-0 disabled:text-muted-foreground disabled:no-underline" onClick={() => p.onOpenTerminal(run.agent)}>{run.agent}</Button>}
                      <span className="rounded bg-muted px-1.5 py-0.5">{run.state.charAt(0).toUpperCase()}{run.state.slice(1)}</span>
                      {verdict && <span className={`rounded px-1.5 py-0.5 ${verdict.className}`}>{verdict.label}</span>}
                    </div>
                    {run.findings.length > 0 && <Collapsible className="pl-6">
                      <CollapsibleTrigger asChild><Button variant="link" size="sm">{run.findings.length} findings</Button></CollapsibleTrigger>
                      <CollapsibleContent><ul className="list-disc pl-5">{run.findings.map((f, j) => <li key={j}>{findingText(f)}</li>)}</ul></CollapsibleContent>
                    </Collapsible>}
                  </li>
                );
              })}
            </ul>
          </li>
        ))}
      </ol>
      </CollapsibleContent>
      </Collapsible>
    </section>
  );
}

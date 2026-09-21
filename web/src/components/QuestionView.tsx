import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { useAgents } from "../data/queries";
import { requestTarget } from "../logic/inbox";
import type { Request } from "../types";

export function QuestionView({ request, connected }: { request: Request; connected: boolean }) {
  const agents = useAgents();
  const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));
  const options = Array.isArray(request.options) ? request.options : [];
  const target = requestTarget(request, agents.data ?? []);
  const open = target?.kind === "terminal" ? target.agent : undefined;
  return (
    <div className="space-y-3">
      <p className="whitespace-pre-wrap text-base">{request.prompt}</p>
      {options.length > 0 && (
        <div>
          <p className="text-muted">{C.optionsOffered}</p>
          <ul className="list-disc pl-5">
            {options.map((o) => (
              <li key={o}>{o}</li>
            ))}
          </ul>
        </div>
      )}
      <button
        type="button"
        disabled={!connected || !open}
        onClick={() => open && void terminal.run(open).catch(() => undefined)}
        className="rounded border border-line px-3 py-1 disabled:opacity-50"
      >
        {C.openOrchestratorTerminal}
      </button>
      {target?.kind === "unavailable" && <p className="text-muted">{target.hint}</p>}
    </div>
  );
}

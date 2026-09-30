import { errorText } from "../api";
import { C } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { useAgents } from "../data/queries";
import { requestTarget } from "../logic/inbox";
import { agentActionToast } from "../logic/toasts";
import type { Request } from "../types";
import { useToast } from "./Toast";
import { Button } from "./ui/button";

export function QuestionView({ request, connected }: { request: Request; connected: boolean }) {
  const toast = useToast();
  const agents = useAgents();
  const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));
  const { live } = useConnection();
  const options = Array.isArray(request.options) ? request.options : [];
  const target = requestTarget(request, agents.data ?? []);
  const open = target?.kind === "terminal" ? target.agent : undefined;
  return (
    <div className="space-y-3">
      <p className="whitespace-pre-wrap text-base">{request.prompt}</p>
      {options.length > 0 && (
        <div>
          <p className="text-muted-foreground">{C.optionsOffered}</p>
          <ul className="list-disc pl-5">
            {options.map((o) => (
              <li key={o}>{o}</li>
            ))}
          </ul>
        </div>
      )}
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={!connected || !open}
        onClick={() => { if (open && live.connected) {
          const epoch = live.epoch;
          void terminal.run(open).then(() => { if (live.connected && live.epoch === epoch) toast.success(agentActionToast("terminal", open)); })
            .catch((e: unknown) => { if (live.connected && live.epoch === epoch) toast.error(errorText(e)); });
        } }}
      >
        {C.openOrchestratorTerminal}
      </Button>
      {target?.kind === "unavailable" && <p className="text-muted-foreground">{target.hint}</p>}
    </div>
  );
}

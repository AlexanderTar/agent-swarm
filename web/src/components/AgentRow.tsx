import { errorText } from "../api";
import { ROLE_LABEL, T } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { type AgentAction, agentActions, displayState, isFinished } from "../logic/agentActions";
import type { AgentEndpoint, AgentNode } from "../types";
import { AgentIcon } from "./icons";
import { StateDot } from "./StatusLabel";
import { useToast } from "./Toast";

export function AgentRow({ agent, depth = 0 }: { agent: AgentNode; depth?: number }) {
  const { connected } = useConnection();
  const toast = useToast();
  const act = useMutation(
    (api, action: AgentEndpoint, body?: object) => api.agentAction(agent.name, action, body),
    ["agents", "item:"],
  );
  const onAction = async (a: AgentAction) => {
    if (a.confirm && !window.confirm(a.confirm)) return;
    try {
      await act.run(a.endpoint, a.body);
    } catch (e) {
      // F20 / contracts §2: reuse errorText rather than an inline `instanceof ApiError` ternary.
      toast({ message: errorText(e) });
    }
  };
  return (
    <div
      id={`agent-${agent.name}`}
      data-testid={`agent-${agent.name}`}
      className="flex items-center justify-between gap-2 py-1"
      style={{ paddingLeft: depth * 16 }}
    >
      <div className="min-w-0">
        <div className="flex items-center gap-1.5">
          <AgentIcon kind={agent.kind} />
          <span className="truncate" title={agent.name}>{`${agent.name} · ${ROLE_LABEL[agent.role]}`}</span>
        </div>
        <StateDot state={displayState(agent)} withLabel />
      </div>
      <div className="flex shrink-0 gap-1">
        {agentActions(agent).map((a) => (
          <button
            key={a.endpoint}
            type="button"
            disabled={a.disabled || !connected || act.pending}
            onClick={() => void onAction(a)}
            className="rounded border border-line px-1.5 py-0.5 hover:bg-raised disabled:opacity-50"
          >
            {a.label}
          </button>
        ))}
      </div>
    </div>
  );
}

function Finished({ agents, depth }: { agents: AgentNode[]; depth: number }) {
  if (agents.length === 0) return null;
  return (
    <details style={{ paddingLeft: depth * 16 }}>
      <summary className="cursor-pointer text-muted">{T.finished(agents.length)}</summary>
      {agents.map((a) => <AgentRow key={a.id} agent={a} />)}
    </details>
  );
}

function Tree({ agent, depth }: { agent: AgentNode; depth: number }) {
  return (
    <>
      <AgentRow agent={agent} depth={depth} />
      {agent.children.map((c) => <Tree key={c.id} agent={c} depth={depth + 1} />)}
      <Finished agents={agent.finished} depth={depth + 1} />
    </>
  );
}

export function AgentList({ agents }: { agents: AgentNode[] }) {
  return (
    <div>
      {agents.filter((a) => !isFinished(a)).map((a) => <Tree key={a.id} agent={a} depth={0} />)}
      <Finished agents={agents.filter(isFinished)} depth={0} />
    </div>
  );
}

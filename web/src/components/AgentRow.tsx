import { useEffect, useState } from "react";
import { errorText } from "../api";
import { C, ROLE_LABEL, T } from "../copy";
import { useConnection, useMutation } from "../data/hooks";
import { type AgentAction, agentActions, displayState, isFinished } from "../logic/agentActions";
import { agentActionToast } from "../logic/toasts";
import type { AgentEndpoint, AgentNode } from "../types";
import { AgentIcon } from "./icons";
import { StateDot } from "./StatusLabel";
import { useToast } from "./Toast";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "./ui/alert-dialog";
import { Button } from "./ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./ui/collapsible";

export function AgentRow({ agent, depth = 0 }: { agent: AgentNode; depth?: number }) {
  const { connected } = useConnection();
  const toast = useToast();
  const act = useMutation(
    (api, action: AgentEndpoint, body?: object) => api.agentAction(agent.name, action, body),
    ["agents", "item:"],
  );
  // `act.pending` clears before the refetch lands, so remember the click until the agent's state moves.
  const [requested, setRequested] = useState<AgentEndpoint | null>(null);
  const [confirming, setConfirming] = useState<AgentAction | null>(null);
  // Keyed on the raw session state: displayState also flips on waiting/stale flags without the pause landing.
  const sessionState = agent.session?.state;
  useEffect(() => setRequested(null), [sessionState]);
  const onAction = async (a: AgentAction) => {
    if (!connected) return;
    if (a.endpoint === "pause" || a.endpoint === "resume") setRequested(a.endpoint);
    try {
      await act.run(a.endpoint, a.body);
      toast.success(agentActionToast(a.endpoint, agent.name, a.body?.scope));
    } catch (e) {
      setRequested(null);
      // F20 / contracts §2: reuse errorText rather than an inline `instanceof ApiError` ternary.
      toast({ message: errorText(e) });
    }
  };
  return (
    <div
      id={`agent-${agent.name}`}
      data-testid={`agent-${agent.name}`}
      className="flex flex-col items-start gap-2 py-1 sm:flex-row sm:items-center sm:justify-between"
      style={{ paddingLeft: depth * 16 }}
    >
      <div className="min-w-0">
        <div className="flex items-center gap-1.5">
          <AgentIcon kind={agent.kind} />
          <span className="truncate" title={agent.name}>{`${agent.name} · ${ROLE_LABEL[agent.role]}`}</span>
        </div>
        {agent.kind_reason && (
          <p className="truncate text-xs text-muted-foreground" title={agent.kind_reason}>{agent.kind_reason}</p>
        )}
        <StateDot state={displayState(agent)} withLabel />
      </div>
      <div className="flex flex-wrap gap-1">
        {agentActions(agent).map((a) => {
          const inFlight = a.endpoint === requested;
          return (
            <Button
              key={a.endpoint}
              type="button"
              variant={a.endpoint === "cancel" ? "destructive" : "outline"}
              size="sm"
              disabled={a.disabled || inFlight || !connected || act.pending}
              onClick={() => a.confirm ? setConfirming(a) : void onAction(a)}
            >
              {inFlight ? (requested === "pause" ? C.pausing : C.resuming) : a.label}
            </Button>
          );
        })}
      </div>
      <AlertDialog open={confirming !== null} onOpenChange={(open) => { if (!open) setConfirming(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader><AlertDialogTitle>{confirming?.confirm}</AlertDialogTitle></AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{C.keepRunning}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={!connected} onClick={() => { const action = confirming; setConfirming(null); if (action) void onAction(action); }}>{C.cancel}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function Finished({ agents, depth }: { agents: AgentNode[]; depth: number }) {
  if (agents.length === 0) return null;
  return (
    <Collapsible style={{ paddingLeft: depth * 16 }}>
      <CollapsibleTrigger asChild><Button variant="ghost" size="sm">{T.finished(agents.length)}</Button></CollapsibleTrigger>
      <CollapsibleContent>{agents.map((a) => <AgentRow key={a.id} agent={a} />)}</CollapsibleContent>
    </Collapsible>
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

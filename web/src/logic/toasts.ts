import { T } from "../copy";
import type { AgentEndpoint, AgentNode } from "../types";

export type AgentVerb = AgentEndpoint;

export function agentActionToast(verb: AgentVerb, name: string, scope?: "session" | "subtree"): string {
  switch (verb) {
    case "pause": return scope === "subtree" ? T.toastPausedGroup(name) : T.toastPaused(name);
    case "resume": return T.toastResumed(name);
    case "cancel": return T.toastCancelled(name);
    case "ack": return T.toastAcked(name);
    case "retry": return T.toastRetrying(name);
    case "terminal": return T.toastTerminal(name);
  }
}

export const startedToast = (agent: Pick<AgentNode, "name" | "state">, itemKey: string): string =>
  agent.state === "queued" ? T.toastQueued(agent.name) : T.toastStarted(agent.name, itemKey);
